package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// These run against a real Server and a real store, so they exercise the
// mapping itself rather than a canned response. An earlier revision of
// writeErr classified errors by searching their text, which turned every
// unknown-project 404 into a 500 -- a real store is what catches that.
func TestUnknownIDsAreNotFoundNotServerErrors(t *testing.T) {
	_, ts, token := newTestServer(t)

	// A project must exist before per-project routes can be reached.
	resp := doReq(t, http.MethodPost, ts.URL+"/api/v1/projects", token, "",
		`{"name":"p","root_path":"/tmp/errors-test"}`)
	var created struct {
		ProjectID string `json:"project_id"`
	}
	decode(t, resp, &created)
	if created.ProjectID == "" {
		t.Fatal("could not create a project to test against")
	}

	cases := []struct {
		name       string
		method     string
		path       string
		wantInBody string
	}{
		{
			name:       "unknown project",
			method:     http.MethodGet,
			path:       "/api/v1/projects/does-not-exist",
			wantInBody: "does-not-exist",
		},
		{
			name:       "unknown change set",
			method:     http.MethodGet,
			path:       "/api/v1/projects/" + created.ProjectID + "/change-sets/CHG-99",
			wantInBody: "CHG-99",
		},
		{
			name:       "unknown initiative",
			method:     http.MethodGet,
			path:       "/api/v1/projects/" + created.ProjectID + "/initiatives/ini-nope/tasks",
			wantInBody: "ini-nope",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := doReq(t, tc.method, ts.URL+tc.path, token, "", "")
			body := readBody(t, resp)

			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("status = %d, want 404 (body: %s)", resp.StatusCode, body)
			}
			assertNoStorageDetail(t, body)
			if !strings.Contains(body, tc.wantInBody) {
				t.Errorf("body should name the reference asked for (%q), got: %s", tc.wantInBody, body)
			}
		})
	}
}

// assertNoStorageDetail fails if a response body carries anything from below
// the domain boundary. Asserted as a shape rather than a blocklist of one
// token: an absolute state path leaks as much as constraint text does.
func assertNoStorageDetail(t *testing.T, body string) {
	t.Helper()
	for _, leak := range []string{
		"sql:", "SELECT ", "constraint failed", "no such table", "no such column",
		"database is locked", ".db", "/Users/", "goroutine",
	} {
		if strings.Contains(body, leak) {
			t.Fatalf("response leaks storage detail (%q): %s", leak, body)
		}
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func decode(t *testing.T, resp *http.Response, out any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}
