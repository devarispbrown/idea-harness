package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// apiDocsMux is a mux carrying only the documentation endpoints, so these tests
// do not depend on the rest of the route table.
func apiDocsMux() *http.ServeMux {
	mux := http.NewServeMux()
	registerAPIDocs(mux)
	return mux
}

func TestOpenAPIYAMLIsServedVerbatim(t *testing.T) {
	rec := httptest.NewRecorder()
	apiDocsMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /openapi.yaml = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/yaml") {
		t.Errorf("Content-Type = %q, want application/yaml", ct)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("served spec is not valid YAML: %v", err)
	}
	if _, ok := doc["openapi"]; !ok {
		t.Errorf("served spec has no `openapi` key; keys: %v", keysOf(doc))
	}
	if _, ok := doc["paths"]; !ok {
		t.Errorf("served spec has no `paths` key; keys: %v", keysOf(doc))
	}
}

func TestOpenAPIJSONIsTheSameDocument(t *testing.T) {
	rec := httptest.NewRecorder()
	apiDocsMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /openapi.json = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var fromJSON map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &fromJSON); err != nil {
		t.Fatalf("served spec is not valid JSON: %v", err)
	}

	yamlRec := httptest.NewRecorder()
	apiDocsMux().ServeHTTP(yamlRec, httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil))
	var fromYAML map[string]any
	if err := yaml.Unmarshal(yamlRec.Body.Bytes(), &fromYAML); err != nil {
		t.Fatalf("yaml spec: %v", err)
	}
	if len(fromJSON) != len(fromYAML) {
		t.Errorf("JSON has %d top-level keys (%v), YAML has %d (%v)",
			len(fromJSON), keysOf(fromJSON), len(fromYAML), keysOf(fromYAML))
	}
}

// Status codes are the reason the JSON conversion cannot be a plain
// yaml.Unmarshal + json.Marshal: unquoted `200:` is an integer key, so yaml.v3
// hands back a map[any]any that encoding/json refuses outright.
func TestJSONValueStringifiesNonStringMappingKeys(t *testing.T) {
	var doc any
	if err := yaml.Unmarshal([]byte("responses:\n  200:\n    description: ok\n  true:\n    description: odd\n"), &doc); err != nil {
		t.Fatal(err)
	}
	converted, err := jsonValue(doc)
	if err != nil {
		t.Fatalf("jsonValue: %v", err)
	}
	out, err := json.Marshal(converted)
	if err != nil {
		t.Fatalf("marshalling the converted document: %v", err)
	}
	for _, want := range []string{`"200"`, `"true"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("converted JSON %s is missing key %s", out, want)
		}
	}
}

// The reason the spec is not served under /api/v1/: Server.auth requires the
// token on everything under /api/ except health and the SSE token= param, so a
// Scalar page fetching the spec would render blank on a silent 401. This test
// pins the property the placement depends on.
func TestDocsPathsAreOutsideTheAuthBoundary(t *testing.T) {
	reached := false
	// s.rt is nil on purpose: if auth ever consults the runtime token for these
	// paths, this test panics rather than passing by accident.
	guarded := (&Server{}).auth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))

	for _, path := range []string{"/openapi.yaml", "/openapi.json", "/api-reference"} {
		if strings.HasPrefix(path, "/api/") {
			t.Fatalf("%s is under /api/ and would require a token", path)
		}
		reached = false
		rec := httptest.NewRecorder()
		guarded.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if !reached {
			t.Errorf("%s did not reach the handler: auth returned %d", path, rec.Code)
		}
	}
}

// A checkout that has never run `make build` has no Scalar bundle embedded. The
// page must say so and point at the spec rather than 404ing like a bad route —
// and the spec endpoints must keep working regardless.
func TestAPIReferenceDegradesWithoutABuiltBundle(t *testing.T) {
	rec := httptest.NewRecorder()
	apiDocsMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api-reference", nil))

	if rec.Code != http.StatusOK && rec.Code != http.StatusNotFound {
		t.Fatalf("GET /api-reference = %d, want 200 (built) or 404 (not built)", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "connect-src 'self'") {
		t.Errorf("Content-Security-Policy = %q, want connect-src 'self' so the page cannot reach proxy.scalar.com", csp)
	}
	if rec.Code == http.StatusNotFound && !strings.Contains(rec.Body.String(), "/openapi.yaml") {
		t.Errorf("the not-built page should point at the spec, which is always embedded; got:\n%s", rec.Body.String())
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
