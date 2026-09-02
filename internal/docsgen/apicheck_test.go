package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPatternOperation(t *testing.T) {
	cases := []struct {
		pattern string
		want    operation
		ok      bool
	}{
		{"GET /api/v1/health", operation{"GET", "/api/v1/health"}, true},
		{"POST /api/v1/projects/{projectId}/initiatives", operation{"POST", "/api/v1/projects/{projectId}/initiatives"}, true},
		{"DELETE /api/v1/x/{id}", operation{"DELETE", "/api/v1/x/{id}"}, true},
		// A pattern with no method matches every method; it is still a route.
		{"/api/v1/legacy", operation{"", "/api/v1/legacy"}, true},
		// The catch-all the embedded UI is mounted on is not an operation.
		{"/", operation{}, false},
		// Host-qualified patterns are not paths and must not be mis-split.
		{"GET example.com/x", operation{}, false},
	}
	for _, tc := range cases {
		got, ok := patternOperation(tc.pattern)
		if ok != tc.ok || got != tc.want {
			t.Errorf("patternOperation(%q) = %v, %v; want %v, %v", tc.pattern, got, ok, tc.want, tc.ok)
		}
	}
}

func TestParseRoutesReadsPatternsAndPositions(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "routes.go"), `package daemon

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /api/v1/health", s.handleHealth)
	m.HandleFunc("POST /api/v1/projects", s.handleCreateProject)
	m.Handle("/", uiHandler())
}
`)
	// A _test.go file in the same package must not contribute routes: test
	// fixtures register endpoints that the real daemon does not serve.
	writeFile(t, filepath.Join(dir, "fake_test.go"), `package daemon

func fixture(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/only-in-tests", nil)
}
`)

	routes, err := parseRoutes(dir)
	if err != nil {
		t.Fatalf("parseRoutes: %v", err)
	}
	if len(routes) != 2 {
		t.Fatalf("got %d routes, want 2: %v", len(routes), routes)
	}
	where, ok := routes[operation{"GET", "/api/v1/health"}]
	if !ok {
		t.Fatalf("GET /api/v1/health not found in %v", routes)
	}
	if !strings.HasSuffix(where, "routes.go:5") {
		t.Errorf("position = %q, want it to end in routes.go:5", where)
	}
	if _, ok := routes[operation{"GET", "/api/v1/only-in-tests"}]; ok {
		t.Error("a route registered in a _test.go file was counted")
	}
}

// A pattern the checker cannot read is a route that can drift out of the spec
// unnoticed, so it is a hard error rather than a silent skip.
func TestParseRoutesRejectsAComputedPattern(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "routes.go"), `package daemon

func register(m *http.ServeMux) {
	m.HandleFunc("GET "+pathHealth, handleHealth)
}
`)
	_, err := parseRoutes(dir)
	if err == nil {
		t.Fatal("parseRoutes accepted a HandleFunc pattern it cannot read")
	}
	if !strings.Contains(err.Error(), "not a string literal") {
		t.Errorf("error = %v, want it to name the unreadable pattern", err)
	}
}

// `Handle` is a common method name on other types, so a non-string first
// argument there means "not a route" rather than "unreadable route".
func TestParseRoutesIgnoresUnrelatedHandleCalls(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bus.go"), `package daemon

func wire(bus *Bus) {
	bus.Handle(onEvent)
}
`)
	routes, err := parseRoutes(dir)
	if err != nil {
		t.Fatalf("parseRoutes: %v", err)
	}
	if len(routes) != 0 {
		t.Errorf("got %v, want no routes", routes)
	}
}

func TestParseSpecOperations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "openapi.yaml")
	writeFile(t, path, `openapi: 3.1.0
paths:
  /api/v1/health:
    get:
      summary: up
  /api/v1/projects:
    parameters: []
    get:
      summary: list
    post:
      summary: create
`)
	ops, note, err := parseSpecOperations(path)
	if err != nil {
		t.Fatalf("parseSpecOperations: %v", err)
	}
	if note != "" {
		t.Errorf("note = %q, want none for a spec that parses", note)
	}
	want := []operation{
		{"GET", "/api/v1/health"},
		{"GET", "/api/v1/projects"},
		{"POST", "/api/v1/projects"},
	}
	if len(ops) != len(want) {
		t.Fatalf("got %d operations, want %d: %v", len(ops), len(want), ops)
	}
	for _, op := range want {
		if _, ok := ops[op]; !ok {
			t.Errorf("%v missing from %v", op, ops)
		}
	}
	// `parameters:` is a path-level key, not an operation.
	if _, ok := ops[operation{"PARAMETERS", "/api/v1/projects"}]; ok {
		t.Error("a non-method path-item key was read as an operation")
	}
}

// Before the hand-written spec lands there is nothing to compare against.
// Reporting every route as undocumented is more useful than refusing to run.
func TestParseSpecOperationsToleratesAMissingSpec(t *testing.T) {
	ops, note, err := parseSpecOperations(filepath.Join(t.TempDir(), "openapi.yaml"))
	if err != nil {
		t.Fatalf("parseSpecOperations: %v", err)
	}
	if len(ops) != 0 {
		t.Errorf("got %v, want no operations", ops)
	}
	if !strings.Contains(note, "does not exist") {
		t.Errorf("note = %q, want it to explain the missing spec", note)
	}
}

func TestParseSpecOperationsReportsUnparseableYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "openapi.yaml")
	// Duplicate top-level keys are the failure mode of a spec assembled by
	// appending to an existing file.
	writeFile(t, path, "paths:\n  /api/v1/a:\n    get: {}\npaths:\n  /api/v1/b:\n    get: {}\n")
	if _, _, err := parseSpecOperations(path); err == nil {
		t.Fatal("parseSpecOperations accepted a document with duplicate keys")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
