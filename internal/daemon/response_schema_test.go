package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// openAPISchema is the slice of a schema this check needs: what a response must
// contain, and which of its fields are arrays.
type openAPISchema struct {
	Type       schemaType               `yaml:"type"`
	Required   []string                 `yaml:"required"`
	Properties map[string]openAPISchema `yaml:"properties"`
	Items      *openAPISchema           `yaml:"items"`
	Ref        string                   `yaml:"$ref"`
}

// schemaType holds an OpenAPI 3.1 type, which is either a string or a list —
// `type: [string, "null"]` is how the spec marks a nullable field.
type schemaType []string

func (t *schemaType) UnmarshalYAML(n *yaml.Node) error {
	var one string
	if err := n.Decode(&one); err == nil {
		*t = schemaType{one}
		return nil
	}
	var many []string
	if err := n.Decode(&many); err != nil {
		return err
	}
	*t = many
	return nil
}

func (t schemaType) is(want string) bool {
	for _, v := range t {
		if v == want {
			return true
		}
	}
	return false
}

// nullable reports whether the spec explicitly permits null for this field, in
// which case a null response is correct rather than drift.
func (t schemaType) nullable() bool { return t.is("null") }

type openAPIDoc struct {
	Components struct {
		Schemas map[string]openAPISchema `yaml:"schemas"`
	} `yaml:"components"`
	// Typed per method rather than map[string]operation: a path item also
	// carries a `parameters:` sequence, which a map would try to decode as an
	// operation and fail on.
	Paths map[string]struct {
		Get *openAPIOperation `yaml:"get"`
	} `yaml:"paths"`
}

type openAPIOperation struct {
	Responses map[string]struct {
		Content map[string]struct {
			Schema openAPISchema `yaml:"schema"`
		} `yaml:"content"`
	} `yaml:"responses"`
}

// TestResponsesMatchTheirDeclaredSchema is the response-schema axis, the last
// guard gap the audits left open.
//
// Three guards already compare routes to the spec, request-body field names to
// handlers, and spec enums to domain constants. None looked at what a response
// actually contains — which is why `Coverage.status` was returned for months
// and documented nowhere, `RequirementStatus` listed four of six values, and
// `/doctor` and `/diff` answered `null` where the spec says `array`. Each was
// found by a human reading, one at a time.
//
// It checks the two properties that class keeps violating: every field the spec
// marks `required` is present, and every field typed `array` is a JSON array
// rather than null. It deliberately does not attempt full JSON-Schema
// validation — the value is in the drift that actually happens.
func TestResponsesMatchTheirDeclaredSchema(t *testing.T) {
	doc := loadOpenAPI(t)
	rt, ts, token := newTestServer(t)
	populatedProject, populatedIni, csID := appliedChangeSet(t, rt, ts, token)

	// Both fixtures, because the empty case is the one that returns null and it
	// is not reachable from the populated one: an applied change set produces
	// graph-audit findings, so /doctor is never empty there. A first version of
	// this test used only the populated fixture and the /doctor mutation
	// survived it — the same blind spot that let the defect ship.
	bareProject, bareIni := newTestProjectInitiative(t, rt, ts, token)

	checked := 0
	for _, fx := range []struct{ project, ini, cs string }{
		{populatedProject, populatedIni, csID},
		{bareProject, bareIni, ""},
	} {
		checked += checkAllGETs(t, doc, ts, token, fx.project, fx.ini, fx.cs)
	}

	if checked == 0 {
		t.Fatal("no response was checked against a schema; this test is not reading either side")
	}
	t.Logf("checked %d GET response(s) against their declared schema", checked)
}

func checkAllGETs(t *testing.T, doc openAPIDoc, ts *httptest.Server, token, projectID, iniID, csID string) int {
	t.Helper()
	checked := 0
	for rawPath, item := range doc.Paths {
		if item.Get == nil {
			continue
		}
		schema, ok := twoHundredSchema(item.Get.Responses)
		if !ok {
			continue
		}
		resolved, ok := resolve(doc, schema)
		if !ok {
			continue
		}
		// An array schema carries neither required nor properties, so a filter
		// written only for objects skipped every list endpoint — which is where
		// the null defects actually lived. /doctor was invisible to a first
		// version of this test for exactly that reason.
		if !resolved.Type.is("array") && len(resolved.Required) == 0 && len(resolved.Properties) == 0 {
			continue
		}

		url, ok := concretePath(rawPath, projectID, iniID, csID)
		if !ok {
			continue // a path this fixture cannot address (artifact ids, run ids)
		}

		resp := doReq(t, http.MethodGet, ts.URL+url, token, "", "")
		var body any
		decErr := json.NewDecoder(resp.Body).Decode(&body)
		status := resp.StatusCode
		resp.Body.Close()
		if status != http.StatusOK || decErr != nil {
			continue // unreachable in this fixture
		}
		checked++
		validate(t, doc, resolved, body, "GET "+rawPath, "")
	}
	return checked
}

func loadOpenAPI(t *testing.T) openAPIDoc {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc openAPIDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Paths) == 0 || len(doc.Components.Schemas) == 0 {
		t.Fatal("openapi.yaml parsed to nothing; the shape this test reads has changed")
	}
	return doc
}

// resolve follows a single $ref into components/schemas. One level is enough:
// the spec nests inline, and a ref to a ref does not occur.
func resolve(doc openAPIDoc, s openAPISchema) (openAPISchema, bool) {
	if s.Ref == "" {
		return s, true
	}
	name := strings.TrimPrefix(s.Ref, "#/components/schemas/")
	target, ok := doc.Components.Schemas[name]
	return target, ok
}

func twoHundredSchema(responses map[string]struct {
	Content map[string]struct {
		Schema openAPISchema `yaml:"schema"`
	} `yaml:"content"`
},
) (openAPISchema, bool) {
	r, ok := responses["200"]
	if !ok {
		return openAPISchema{}, false
	}
	c, ok := r.Content["application/json"]
	if !ok {
		return openAPISchema{}, false
	}
	return c.Schema, true
}

// concretePath substitutes the fixture's ids for the spec's path parameters.
// A path naming a parameter the fixture has no value for is skipped rather than
// guessed at.
func concretePath(raw, projectID, iniID, csID string) (string, bool) {
	out := raw
	out = strings.ReplaceAll(out, "{projectId}", projectID)
	out = strings.ReplaceAll(out, "{id}", iniID)
	if strings.Contains(raw, "change-sets/{id}") {
		out = strings.ReplaceAll(raw, "{projectId}", projectID)
		out = strings.ReplaceAll(out, "{id}", csID)
	}
	if strings.Contains(out, "{") {
		return "", false
	}
	return out, true
}

var _ = sort.Strings

// validate walks a decoded response against its schema, reporting the two
// drifts this axis exists for: a required field that is absent, and an
// array-typed field that is null.
//
// It recurses into arrays and nested objects. A first version decoded into
// map[string]any and so silently skipped every array-typed response — which is
// where the null defects actually were, so both mutations survived it.
func validate(t *testing.T, doc openAPIDoc, schema openAPISchema, value any, where, path string) {
	t.Helper()
	at := where
	if path != "" {
		at += " " + path
	}

	if schema.Type.is("array") {
		if value == nil {
			if !schema.Type.nullable() {
				t.Errorf("%s is null; the spec types it as an array, so a generated client "+
					"rejects this response", at)
			}
			return
		}
		items, ok := value.([]any)
		if !ok {
			t.Errorf("%s is %T; the spec types it as an array", at, value)
			return
		}
		if schema.Items == nil {
			return
		}
		itemSchema, ok := resolve(doc, *schema.Items)
		if !ok {
			return
		}
		// One element is enough: they share a schema, and reporting every row of
		// a long list would bury the finding.
		if len(items) > 0 {
			validate(t, doc, itemSchema, items[0], where, path+"[0]")
		}
		return
	}

	obj, ok := value.(map[string]any)
	if !ok {
		return // scalars carry nothing this check can assert
	}
	for _, req := range schema.Required {
		if _, present := obj[req]; !present {
			t.Errorf("%s omits %q, which the spec marks required", at, req)
		}
	}
	for name, prop := range schema.Properties {
		resolved, ok := resolve(doc, prop)
		if !ok {
			continue
		}
		v, present := obj[name]
		if !present {
			continue // absence is the required check's business
		}
		validate(t, doc, resolved, v, where, strings.TrimPrefix(path+"."+name, "."))
	}
}
