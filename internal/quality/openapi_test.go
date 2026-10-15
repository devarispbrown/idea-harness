package quality

import (
	"strings"
	"testing"
)

const specBase = `openapi: 3.0.3
info: { title: t, version: "1" }
paths:
  /items:
    parameters:
      - { name: X-Trace, in: header, required: false, schema: { type: string } }
    get:
      summary: List items
      parameters:
        - { name: limit, in: query, required: false, schema: { type: integer } }
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: { $ref: "#/components/schemas/ItemList" }
    post:
      requestBody:
        required: false
        content:
          application/json:
            schema: { $ref: "#/components/schemas/Item" }
      responses:
        "201": { description: created }
  /items/{id}:
    get:
      parameters:
        - { name: id, in: path, schema: { type: string } }
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: { $ref: "#/components/schemas/Item" }
components:
  schemas:
    Item:
      type: object
      required: [name]
      properties:
        id: { type: string }
        name: { type: string }
    ItemList:
      type: array
      items: { $ref: "#/components/schemas/Item" }
`

func diffKinds(t *testing.T, before, after string) (breaking, other []string, changes []APIChange) {
	t.Helper()
	changes, err := DiffOpenAPI("api/openapi.yaml", []byte(before), "api/openapi.yaml", []byte(after))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range changes {
		if c.Breaking {
			breaking = append(breaking, c.Kind)
		} else {
			other = append(other, c.Kind)
		}
	}
	return breaking, other, changes
}

func TestDiffOpenAPIIdenticalIsEmpty(t *testing.T) {
	b, o, _ := diffKinds(t, specBase, specBase)
	if len(b)+len(o) != 0 {
		t.Fatalf("identical specs differ: breaking=%v other=%v", b, o)
	}
}

func TestDiffOpenAPINonBreakingAdditions(t *testing.T) {
	after := strings.Replace(specBase, `        - { name: limit, in: query, required: false, schema: { type: integer } }`,
		`        - { name: limit, in: query, required: false, schema: { type: integer } }
        - { name: cursor, in: query, schema: { type: string } }`, 1)
	after = strings.Replace(after, "        name: { type: string }\n", "        name: { type: string }\n        tags: { type: array, items: { type: string } }\n", 1)
	after = strings.Replace(after, "components:", `  /health:
    get:
      responses:
        "200": { description: ok }
components:`, 1)
	b, o, _ := diffKinds(t, specBase, after)
	if len(b) != 0 {
		t.Fatalf("additions reported breaking: %v", b)
	}
	for _, want := range []string{"parameter_added", "path_added", "response_property_added"} {
		if !contains(o, want) {
			t.Errorf("missing non-breaking %s in %v", want, o)
		}
	}
}

func TestDiffOpenAPIBreakingChanges(t *testing.T) {
	cases := []struct {
		name string
		edit func(string) string
		want string
	}{
		{"path removed", func(s string) string {
			i := strings.Index(s, "  /items/{id}:")
			j := strings.Index(s, "components:")
			return s[:i] + s[j:]
		}, "path_removed"},
		{"operation removed", func(s string) string {
			i := strings.Index(s, "    post:")
			j := strings.Index(s, "  /items/{id}:")
			return s[:i] + s[j:]
		}, "operation_removed"},
		{"required parameter added", func(s string) string {
			return strings.Replace(s, `        - { name: limit, in: query, required: false, schema: { type: integer } }`,
				`        - { name: limit, in: query, required: false, schema: { type: integer } }
        - { name: tenant, in: query, required: true, schema: { type: string } }`, 1)
		}, "required_parameter_added"},
		{"parameter became required", func(s string) string {
			return strings.Replace(s, `name: limit, in: query, required: false`, `name: limit, in: query, required: true`, 1)
		}, "parameter_became_required"},
		{"parameter type changed", func(s string) string {
			return strings.Replace(s, `name: limit, in: query, required: false, schema: { type: integer }`, `name: limit, in: query, required: false, schema: { type: string }`, 1)
		}, "parameter_type_changed"},
		{"parameter removed", func(s string) string {
			return strings.Replace(s, "        - { name: limit, in: query, required: false, schema: { type: integer } }\n", "", 1)
		}, "parameter_removed"},
		{"path-level parameter removed", func(s string) string {
			return strings.Replace(s, "    parameters:\n      - { name: X-Trace, in: header, required: false, schema: { type: string } }\n", "", 1)
		}, "parameter_removed"},
		{"request body required", func(s string) string {
			return strings.Replace(s, "        required: false\n", "        required: true\n", 1)
		}, "request_body_required"},
		{"request body gained a required property", func(s string) string {
			return strings.Replace(s, "      required: [name]", "      required: [name, id]", 1)
		}, "required_property_added"},
		{"2xx response removed", func(s string) string {
			return strings.Replace(s, `        "201": { description: created }`, `        "202": { description: accepted }`, 1)
		}, "response_removed"},
		{"response type changed", func(s string) string {
			return strings.Replace(s, `    ItemList:
      type: array
      items: { $ref: "#/components/schemas/Item" }`, `    ItemList:
      type: object
      properties:
        items: { type: array, items: { $ref: "#/components/schemas/Item" } }`, 1)
		}, "response_type_changed"},
		{"response property removed", func(s string) string {
			return strings.Replace(s, "        id: { type: string }\n", "", 1)
		}, "response_property_removed"},
		{"response media type removed", func(s string) string {
			return strings.Replace(s, `            application/json:
              schema: { $ref: "#/components/schemas/Item" }
components:`, `            text/plain:
              schema: { type: string }
components:`, 1)
		}, "response_media_type_removed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			after := tc.edit(specBase)
			if after == specBase {
				t.Fatal("fixture edit did not apply")
			}
			b, _, changes := diffKinds(t, specBase, after)
			if !contains(b, tc.want) {
				t.Fatalf("want breaking %s, got breaking=%v (all=%+v)", tc.want, b, changes)
			}
			for _, c := range changes {
				if c.Breaking && c.Line == 0 {
					t.Errorf("breaking change %s has no line citation", c.Kind)
				}
			}
		})
	}
}

func TestDiffOpenAPIPathParameterIsAlwaysRequired(t *testing.T) {
	// The fixture's `id` path parameter omits `required: true`. Adding the
	// flag must not read as "parameter became required".
	after := strings.Replace(specBase, "{ name: id, in: path, schema", "{ name: id, in: path, required: true, schema", 1)
	b, _, _ := diffKinds(t, specBase, after)
	if len(b) != 0 {
		t.Fatalf("spelling out a path parameter's required flag reported %v", b)
	}
}

func TestDiffOpenAPIReadsJSON(t *testing.T) {
	before := `{"openapi":"3.1.0","paths":{"/a":{"get":{"responses":{"200":{"description":"x"}}}},"/b":{"get":{"responses":{"200":{"description":"x"}}}}}}`
	after := `{"openapi":"3.1.0","paths":{"/a":{"get":{"responses":{"200":{"description":"x"}}}}}}`
	b, _, _ := diffKinds(t, before, after)
	if !contains(b, "path_removed") {
		t.Fatalf("JSON spec diff missed the removed path: %v", b)
	}
	if !LooksLikeOpenAPI([]byte(after)) || LooksLikeOpenAPI([]byte(`swagger: "2.0"`)) || LooksLikeOpenAPI([]byte("a: [")) {
		t.Fatal("LooksLikeOpenAPI misclassified")
	}
}

func TestDiffOpenAPIRejectsUnparseable(t *testing.T) {
	if _, err := DiffOpenAPI("a", []byte("paths: ["), "a", []byte(specBase)); err == nil {
		t.Fatal("an unparseable before spec must be an error, not an empty diff")
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
