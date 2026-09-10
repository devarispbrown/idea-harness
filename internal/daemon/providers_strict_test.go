package daemon

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// providerDemo reports GET /providers' `demo` flag.
func providerDemo(t *testing.T, ts, token, projectID string) bool {
	t.Helper()
	resp := doReq(t, http.MethodGet, ts+"/api/v1/projects/"+projectID+"/providers", token, "", "")
	var view struct {
		Demo bool `json:"demo"`
	}
	decode(t, resp, &view)
	return view.Demo
}

// TestUpdateProvidersRejectsAnInlineKey is the acceptance test for audit 7's
// P1-4.
//
// `api_key` is not a field of the decode target, so encoding/json dropped it in
// silence and the endpoint answered 200 "updated". What it wrote was
// `openai: {}` -- which is NOT an unusable entry, and getting that backwards is
// what made the first version of this fix wrong: ResolveProvider fills every
// field from the preset, so the write really did configure OpenAI, `demo`
// correctly went false, and real billed gpt-4o calls followed. The defect is
// exactly one thing: a field the caller sent to carry a credential was
// discarded, under a response that said the write succeeded.
//
// The `demo` assertion is the half that matters. A 400 that still flipped demo
// mode would be a worse defect wearing a better status code.
func TestUpdateProvidersRejectsAnInlineKey(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, _ := newTestProjectInitiative(t, rt, ts, token)
	before := providerDemo(t, ts.URL, token, projectID)

	resp := doReq(t, http.MethodPut, ts.URL+"/api/v1/projects/"+projectID+"/providers",
		token, "", `{"providers":{"openai":{"api_key":"sk-live-not-a-real-key"}}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("an inline api_key answered %d, want 400", resp.StatusCode)
	}
	var body struct {
		Error string `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	if !strings.Contains(body.Error, "api_key") {
		t.Errorf("the error does not name the field that was refused: %q", body.Error)
	}

	if after := providerDemo(t, ts.URL, token, projectID); after != before {
		t.Fatalf("a refused write still moved demo mode from %v to %v; the one signal that "+
			"output is synthetic was inverted by a request the API rejected", before, after)
	}
}

// A name that is not a preset and carries no base_url resolves to no endpoint.
// That is the entry that genuinely cannot serve a request.
func TestUpdateProvidersRejectsAnEndpointlessProvider(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, _ := newTestProjectInitiative(t, rt, ts, token)
	before := providerDemo(t, ts.URL, token, projectID)

	resp := doReq(t, http.MethodPut, ts.URL+"/api/v1/projects/"+projectID+"/providers",
		token, "", `{"providers":{"mycustom":{}}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a provider naming no endpoint answered %d, want 400", resp.StatusCode)
	}
	if after := providerDemo(t, ts.URL, token, projectID); after != before {
		t.Errorf("a refused write moved demo mode from %v to %v", before, after)
	}
}

// The row that stops this shipping a regression. `{}` for a PRESET name is a
// complete, working configuration -- ResolveProvider fills type, base_url and
// api_key_env from the preset table, and internal/daemon/providers.go calls
// exactly that as its fallback path. The first version of this fix proposed
// refusing it, with an acceptance test pinning 400, which would have broken
// "configure a preset by name alone" for every direct API caller.
func TestUpdateProvidersAcceptsAPresetByNameAlone(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, _ := newTestProjectInitiative(t, rt, ts, token)
	if !providerDemo(t, ts.URL, token, projectID) {
		t.Fatal("setup: expected demo mode before any provider is configured")
	}

	resp := doReq(t, http.MethodPut, ts.URL+"/api/v1/projects/"+projectID+"/providers",
		token, "", `{"providers":{"openai":{}}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("configuring a preset by name alone answered %d, want 200", resp.StatusCode)
	}
	if providerDemo(t, ts.URL, token, projectID) {
		t.Error("a real provider was configured and demo mode did not go false")
	}
}

// Every body a first-party client actually sends, as a literal lifted from the
// call site. DisallowUnknownFields turns a field added on one side into a 400
// for the other, so these are what make that a test failure here rather than a
// broken Settings page.
func TestStrictDecodeAcceptsEveryFirstPartyBody(t *testing.T) {
	for _, tc := range []struct{ name, route, body string }{
		// web/src/Settings.tsx
		{"Settings save model", "providers", `{"default_model":"openai/gpt-4o-mini"}`},
		{"Settings add provider", "providers",
			`{"providers":{"openai":{"type":"openai","base_url":"https://api.openai.com/v1","api_key_env":"OPENAI_API_KEY"}}}`},
		{"Settings git_commit", "providers", `{"git_commit":true}`},
		{"Settings executors", "executors", `{"kind":"internal","image":"alpine:3.20","model":"anthropic/claude-sonnet-4-5"}`},
		// internal/cli
		{"idea model set", "providers", `{"default_model":"openai/gpt-4o-mini"}`},
		{"idea model set role", "providers", `{"models":{"implementer":"openai/gpt-4o-mini"}}`},
		{"idea model add", "providers",
			`{"providers":{"groq":{"type":"openai","base_url":"https://api.groq.com/openai/v1","api_key_env":"GROQ_API_KEY"}}}`},
		{"idea executors setup", "executors", `{"kind":"internal","image":"alpine:3.20"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, ts, token := newTestServer(t)
			projectID, _ := newTestProjectInitiative(t, rt, ts, token)
			resp := doReq(t, http.MethodPut,
				ts.URL+"/api/v1/projects/"+projectID+"/"+tc.route, token, "", tc.body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				var body struct {
					Error string `json:"error"`
				}
				json.NewDecoder(resp.Body).Decode(&body)
				t.Fatalf("a first-party body was refused: %d %s\n%s", resp.StatusCode, body.Error, tc.body)
			}
		})
	}
}
