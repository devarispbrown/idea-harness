package cli

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveObjectIDContextDoc(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/projects/p/initiatives/i/docs" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"doc-1","kind":"prd","title":"My PRD"}]`))
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()
	c := &client{baseURL: ts.URL, token: "t", http: ts.Client()}

	if id, err := resolveObjectID(c, "p", "i", "context_doc", "My PRD"); err != nil || id != "doc-1" {
		t.Fatalf("resolve by title = %q, %v; want doc-1", id, err)
	}
	if id, err := resolveObjectID(c, "p", "i", "context_doc", "doc-1"); err != nil || id != "doc-1" {
		t.Fatalf("resolve by id = %q, %v; want doc-1", id, err)
	}
	if _, err := resolveObjectID(c, "p", "i", "context_doc", "nope"); err == nil {
		t.Fatal("expected an error for an unknown doc")
	}
}
