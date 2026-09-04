package daemon

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestEveryAPIErrorIsJSON pins the contract openapi.yaml already claimed.
//
// The spec says "every error response in this API has this shape and nothing
// else". It did not: the catch-all registered for "/" — the web UI handler —
// matched every request no route pattern claimed, so a wrong verb or a typo'd
// path fell through to `404 page not found` as text/plain. A client with a
// token could not decode the response it is most likely to hit while
// integrating, and the CLI surfaced it as `idea: 404 Not Found`, the only raw
// HTTP status in the product.
func TestEveryAPIErrorIsJSON(t *testing.T) {
	_, ts, token := newTestServer(t)
	projectID := newTestProject(t, ts.URL, token)

	for _, c := range []struct{ name, method, path string }{
		{"wrong verb on a real collection", http.MethodDelete, "/api/v1/projects"},
		{"unknown path", http.MethodGet, "/api/v1/nope"},
		{"wrong verb on health", http.MethodPost, "/api/v1/health"},
		{"typo in a nested path", http.MethodGet, "/api/v1/projects/" + projectID + "/initiativez"},
		{"a v2 that does not exist yet", http.MethodGet, "/api/v2/projects"},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp := doReq(t, c.method, ts.URL+c.path, token, "", "")
			defer resp.Body.Close()

			if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
				t.Errorf("Content-Type %q — the spec says every error is JSON", ct)
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("body is not the documented {\"error\": …} envelope: %v", err)
			}
			if body.Error == "" {
				t.Error("the envelope carries no message")
			}
		})
	}
}

// TestTheWebUIStillServes is the other half: scoping the JSON handler to /api/
// must not take the UI's routes with it. The SPA is hash-routed, so the server
// only ever sees "/", but an over-broad guard would break it outright.
func TestTheWebUIStillServes(t *testing.T) {
	_, ts, token := newTestServer(t)
	for _, path := range []string{"/", "/index.html"} {
		resp := doReq(t, http.MethodGet, ts.URL+path, token, "", "")
		func() {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("GET %s = %d, want 200 — the UI is no longer served", path, resp.StatusCode)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
				t.Errorf("GET %s Content-Type %q, want html", path, ct)
			}
		}()
	}
}
