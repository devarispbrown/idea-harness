package daemon

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestProject registers a project rooted at a fresh temp dir.
func newTestProject(t *testing.T, baseURL, token string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	resp := doReq(t, http.MethodPost, baseURL+"/api/v1/projects", token, "", `{"root_path":"`+dir+`"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create project: %d", resp.StatusCode)
	}
	var p map[string]string
	json.NewDecoder(resp.Body).Decode(&p)
	resp.Body.Close()
	return p["project_id"]
}

// TestUpdateProvidersValidatesBaseURL covers the write path: buildRegistry
// registers a configured provider over its preset default, so an unvalidated
// base_url would ship the preset's API key and the full prompt context to an
// operator-supplied host.
func TestUpdateProvidersValidatesBaseURL(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{"custom provider https", `{"providers":{"groq":{"type":"openai","api_key_env":"GROQ_API_KEY","base_url":"https://api.groq.com/openai/v1"}}}`, http.StatusOK},
		{"custom provider loopback http", `{"providers":{"lmstudio":{"type":"openai","base_url":"http://localhost:1234/v1"}}}`, http.StatusOK},
		{"custom provider remote http", `{"providers":{"groq":{"type":"openai","base_url":"http://api.groq.com/openai/v1"}}}`, http.StatusBadRequest},
		{"custom provider non-url", `{"providers":{"groq":{"type":"openai","base_url":"not a url"}}}`, http.StatusBadRequest},
		{"preset base_url override", `{"providers":{"openai":{"base_url":"https://attacker.tld/v1"}}}`, http.StatusBadRequest},
		{"preset with no override", `{"providers":{"openai":{"api_key_env":"OPENAI_API_KEY"}}}`, http.StatusOK},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rt, ts, token := newTestServer(t)
			projectID := newTestProject(t, ts.URL, token)

			resp := doReq(t, http.MethodPut, ts.URL+"/api/v1/projects/"+projectID+"/providers", token, "", c.body)
			defer resp.Body.Close()
			if resp.StatusCode != c.wantStatus {
				t.Fatalf("status: got %d, want %d", resp.StatusCode, c.wantStatus)
			}
			if c.wantStatus != http.StatusBadRequest {
				return
			}
			// A rejected write must leave nothing behind for buildRegistry to pick up.
			pr, err := rt.EnsureProject(t.Context(), projectID)
			if err != nil {
				t.Fatal(err)
			}
			for name, pc := range pr.cfg.Harness.Providers {
				if strings.Contains(pc.BaseURL, "attacker.tld") || pc.BaseURL == "not a url" {
					t.Fatalf("rejected provider %q was still persisted: %+v", name, pc)
				}
			}
		})
	}
}
