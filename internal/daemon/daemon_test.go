package daemon

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

func newTestServer(t *testing.T) (*Runtime, *httptest.Server, string) {
	t.Helper()
	stateRoot := filepath.Join(t.TempDir(), "state")
	rt, err := NewRuntime(stateRoot, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close() })
	ts := httptest.NewServer(NewServer(rt).Handler())
	t.Cleanup(ts.Close)
	return rt, ts, rt.Token()
}

func doReq(t *testing.T, method, url, token, host string, body string) *http.Response {
	t.Helper()
	var r *http.Request
	var err error
	if body != "" {
		r, err = http.NewRequest(method, url, strings.NewReader(body))
	} else {
		r, err = http.NewRequest(method, url, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		r.Header.Set("X-Idea-Token", token)
	}
	if host != "" {
		r.Host = host
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestUnauthorizedRequestsRejected(t *testing.T) {
	_, ts, _ := newTestServer(t)
	resp := doReq(t, http.MethodGet, ts.URL+"/api/v1/projects", "", "", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestTokenQueryParamRejectedOutsideEvents(t *testing.T) {
	_, ts, token := newTestServer(t)
	// A query-param token on a non-events endpoint must be rejected (no header).
	resp := doReq(t, http.MethodGet, ts.URL+"/api/v1/projects?token="+token, "", "", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for query token on non-events endpoint, got %d", resp.StatusCode)
	}
	// The header token still works.
	resp2 := doReq(t, http.MethodGet, ts.URL+"/api/v1/projects", token, "", "")
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with header token, got %d", resp2.StatusCode)
	}
}

func TestIsSSEEventsPath(t *testing.T) {
	cases := map[string]bool{
		"/api/v1/projects/p/events":                true,
		"/api/v1/projects/p/initiatives/i/docs":    false,
		"/api/v1/projects":                         false,
		"/api/v1/logs":                             false,
		"/api/v1/projects/p/events/":               false,
	}
	for path, want := range cases {
		if got := isSSEEventsPath(path); got != want {
			t.Errorf("isSSEEventsPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestHostRebindingRejected(t *testing.T) {
	_, ts, token := newTestServer(t)
	resp := doReq(t, http.MethodGet, ts.URL+"/api/v1/projects", token, "evil.example.com", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for foreign host, got %d", resp.StatusCode)
	}
}

func TestProjectIsolation(t *testing.T) {
	rt, ts, token := newTestServer(t)

	// Register two projects.
	dirA := filepath.Join(t.TempDir(), "a")
	dirB := filepath.Join(t.TempDir(), "b")
	os.MkdirAll(dirA, 0o755)
	os.MkdirAll(dirB, 0o755)

	resp := doReq(t, http.MethodPost, ts.URL+"/api/v1/projects", token, "", `{"root_path":"`+dirA+`"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create project A: %d", resp.StatusCode)
	}
	var pa map[string]string
	json.NewDecoder(resp.Body).Decode(&pa)
	resp.Body.Close()

	resp = doReq(t, http.MethodPost, ts.URL+"/api/v1/projects", token, "", `{"root_path":"`+dirB+`"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create project B: %d", resp.StatusCode)
	}
	var pb map[string]string
	json.NewDecoder(resp.Body).Decode(&pb)
	resp.Body.Close()

	// Create an initiative in project A directly via its store.
	prA, err := rt.EnsureProject(t.Context(), pa["project_id"])
	if err != nil {
		t.Fatal(err)
	}
	ini := &domain.Initiative{Title: "secret in A", Description: "x"}
	if err := prA.db.CreateInitiative(t.Context(), ini); err != nil {
		t.Fatal(err)
	}

	// Project A sees it.
	resp = doReq(t, http.MethodGet, ts.URL+"/api/v1/projects/"+pa["project_id"]+"/initiatives", token, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list A: %d", resp.StatusCode)
	}
	var listA []map[string]any
	json.NewDecoder(resp.Body).Decode(&listA)
	resp.Body.Close()
	if len(listA) != 1 {
		t.Fatalf("project A should see 1 initiative, got %d", len(listA))
	}

	// Project B must NOT see project A's initiative.
	resp = doReq(t, http.MethodGet, ts.URL+"/api/v1/projects/"+pb["project_id"]+"/initiatives", token, "", "")
	var listB []map[string]any
	json.NewDecoder(resp.Body).Decode(&listB)
	resp.Body.Close()
	if len(listB) != 0 {
		t.Fatalf("project B leaked project A's initiative: %d items", len(listB))
	}
}
