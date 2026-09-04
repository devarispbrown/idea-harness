package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestUpdateProvidersReturnsAttributableWarnings pins the wire half of O2.
//
// The handler dropped everything that was not an error, so a model id absent
// from the bundled catalog produced a plain `{"status":"updated"}` and the CLI
// printed "✓ default model set to ...". The first call then came back 404,
// which is not retryable. Nine such ids shipped in the documentation.
//
// The attribution rule is the 400's rule: only warnings about a field this
// request wrote come back, so a pre-existing warning elsewhere cannot make an
// unrelated write look suspect.
func TestUpdateProvidersReturnsAttributableWarnings(t *testing.T) {
	for _, c := range []struct {
		name     string
		body     string
		wantWarn string // substring; "" means the response must carry no warnings
	}{
		{
			name:     "a model id absent from the catalog warns",
			body:     `{"default_model":"anthropic/claude-3-7-sonnet"}`,
			wantWarn: "bundled catalog",
		},
		{
			name: "a correct model id does not",
			body: `{"default_model":"anthropic/claude-sonnet-4-5"}`,
		},
		{
			name:     "a role override warns too",
			body:     `{"models":{"critic":"openai/gpt-4o-typo"}}`,
			wantWarn: "bundled catalog",
		},
		{
			name: "openrouter ids are upstream strings, not ours to police",
			body: `{"default_model":"openrouter/anthropic/claude-3.7-sonnet"}`,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, ts, token := newTestServer(t)
			projectID := newTestProject(t, ts.URL, token)

			resp := doReq(t, http.MethodPut, ts.URL+"/api/v1/projects/"+projectID+"/providers", token, "", c.body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status: got %d, want 200 — a catalog mismatch is a warning, not a rejection", resp.StatusCode)
			}

			var out struct {
				Status   string   `json:"status"`
				Warnings []string `json:"warnings"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				t.Fatal(err)
			}
			if out.Status != "updated" {
				t.Fatalf("status field: got %q, want %q", out.Status, "updated")
			}

			// Only the catalog warning is under test. A missing provider key also
			// produces one, and whether that fires depends on the ambient
			// environment -- this assertion was "no warnings at all", so it
			// passed on a developer machine with the keys exported and failed in
			// CI, which has none. The sibling test in internal/config had already
			// been fixed for exactly this and the lesson was not carried across.
			var catalog []string
			for _, w := range out.Warnings {
				if strings.Contains(w, "bundled catalog") {
					catalog = append(catalog, w)
				}
			}
			joined := strings.Join(catalog, "\n")
			if c.wantWarn == "" {
				if len(catalog) > 0 {
					t.Fatalf("unexpected catalog warning: %s", joined)
				}
				return
			}
			if !strings.Contains(joined, c.wantWarn) {
				t.Fatalf("catalog warnings %q do not mention %q (all warnings: %v)",
					joined, c.wantWarn, out.Warnings)
			}
		})
	}
}

// TestUpdateProvidersDoesNotReturnUnrelatedWarnings: the warning list follows
// the same attribution rule as the 400. A warning about a field this request
// did not write is logged, not returned — otherwise every save in the UI would
// grow a warning about some other part of the config.
func TestUpdateProvidersDoesNotReturnUnrelatedWarnings(t *testing.T) {
	_, ts, token := newTestServer(t)
	projectID := newTestProject(t, ts.URL, token)

	// Write a bad critic id first; it is now pre-existing.
	seed := doReq(t, http.MethodPut, ts.URL+"/api/v1/projects/"+projectID+"/providers", token, "",
		`{"models":{"critic":"openai/gpt-4o-typo"}}`)
	seed.Body.Close()

	// A request touching only default_model must not report the critic's warning.
	resp := doReq(t, http.MethodPut, ts.URL+"/api/v1/projects/"+projectID+"/providers", token, "",
		`{"default_model":"openai/gpt-4o-mini"}`)
	defer resp.Body.Close()

	var out struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	// The warning text names the model id, not the role, so that is what to look
	// for — a first version of this test grepped for "critic" and passed even
	// with the attribution check deleted.
	for _, w := range out.Warnings {
		if strings.Contains(w, "gpt-4o-typo") {
			t.Fatalf("a pre-existing warning about an untouched field came back: %s", w)
		}
	}
}

// TestListModelsEmitsAnEmptyArrayNotNull: the spec says an unreachable provider
// answers "200 with an empty models array rather than an error", and the
// handler drops the error to get that. It returned `"models":null` — a nil
// slice, not an empty array — so the single case the spec calls out was the one
// case it did not actually produce.
func TestListModelsEmitsAnEmptyArrayNotNull(t *testing.T) {
	_, ts, token := newTestServer(t)
	projectID := newTestProject(t, ts.URL, token)

	// A provider that is not configured and cannot be reached.
	resp := doReq(t, http.MethodGet,
		ts.URL+"/api/v1/projects/"+projectID+"/models?provider=nonexistent-provider", token, "", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"models":null`) {
		t.Fatalf("models came back null, not []: %s", body)
	}
	var out struct {
		Models []string `json:"models"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Models == nil {
		t.Fatal("models decoded to nil")
	}
}

// TestExecutorSetupWarnsOnAnUnknownModel: the same catalog warning as
// PUT /providers, on the path that matters most.
//
// `idea executors setup --model <dead id>` forwards that id into the agent
// container, so the 404 surfaces as an agent that produced nothing rather than
// as a config error. The first pass at O2 wired the warning into the providers
// endpoint and missed this one, and a review caught it.
func TestExecutorSetupWarnsOnAnUnknownModel(t *testing.T) {
	for _, c := range []struct {
		name, body, wantWarn string
	}{
		{"a dead model id warns", `{"kind":"omp","model":"anthropic/claude-3-7-sonnet"}`, "bundled catalog"},
		{"a correct one does not", `{"kind":"omp","model":"anthropic/claude-sonnet-4-5"}`, ""},
		{"no model, no warning", `{"kind":"internal"}`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, ts, token := newTestServer(t)
			projectID := newTestProject(t, ts.URL, token)

			resp := doReq(t, http.MethodPut, ts.URL+"/api/v1/projects/"+projectID+"/executors",
				token, "", c.body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				b, _ := io.ReadAll(resp.Body)
				t.Fatalf("status %d: %s", resp.StatusCode, b)
			}
			var out struct {
				Warnings []string `json:"warnings"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				t.Fatal(err)
			}
			// Only the catalog warning is under test. A missing provider key also
			// produces one, and whether that fires depends on the ambient
			// environment -- this assertion was "no warnings at all", so it
			// passed on a developer machine with the keys exported and failed in
			// CI, which has none. The sibling test in internal/config had already
			// been fixed for exactly this and the lesson was not carried across.
			var catalog []string
			for _, w := range out.Warnings {
				if strings.Contains(w, "bundled catalog") {
					catalog = append(catalog, w)
				}
			}
			joined := strings.Join(catalog, "\n")
			if c.wantWarn == "" {
				if len(catalog) > 0 {
					t.Fatalf("unexpected catalog warning: %s", joined)
				}
				return
			}
			if !strings.Contains(joined, c.wantWarn) {
				t.Fatalf("catalog warnings %q do not mention %q (all warnings: %v)",
					joined, c.wantWarn, out.Warnings)
			}
		})
	}
}
