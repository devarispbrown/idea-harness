package daemon

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
)

// TestUpdateProvidersPreservesHandEditedFields pins audit 5's C1.
//
// handleUpdateProviders assembled its candidate from pr.cfg.Harness — the
// daemon's cached copy — and then wrote the whole struct back. The daemon
// caches a project's config for its lifetime, so anything hand-edited since it
// started was invisible and got reverted, at exit 0, by a command the
// documentation recommends.
//
// Observed before the fix: max_cycles 9 -> 3, research.urls emptied,
// git_commit true -> false, all silently.
//
// The sibling handleUpdateExecutors has always re-read from disk and carries a
// comment saying why; the two live 1800 lines apart and only one was right.
func TestUpdateProvidersPreservesHandEditedFields(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID := newTestProject(t, ts.URL, token)

	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	cfgDir := filepath.Join(pr.rootPath, config.Dir)

	// Hand-edit three unrelated fields, the way a user does with an editor —
	// deliberately writing to disk WITHOUT telling the daemon, which is the
	// whole point.
	h := pr.cfg.Harness
	h.Review.MaxCycles = 9
	h.Research.URLs = []string{"https://example.com/research-i-spent-an-hour-on"}
	h.GitCommit = true
	if err := config.WriteHarness(cfgDir, h); err != nil {
		t.Fatal(err)
	}

	resp := doReq(t, http.MethodPut, ts.URL+"/api/v1/projects/"+projectID+"/providers",
		token, "", `{"default_model":"openai/gpt-4o-mini"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}

	after, err := config.Load(cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	if after.Harness.DefaultModel != "openai/gpt-4o-mini" {
		t.Errorf("the field the request actually wrote was lost: default_model = %q",
			after.Harness.DefaultModel)
	}
	if after.Harness.Review.MaxCycles != 9 {
		t.Errorf("review.max_cycles reverted to %d; the hand edit was silently destroyed",
			after.Harness.Review.MaxCycles)
	}
	if len(after.Harness.Research.URLs) != 1 {
		t.Errorf("research.urls emptied: %v", after.Harness.Research.URLs)
	}
	if !after.Harness.GitCommit {
		t.Error("git_commit reverted to false")
	}
}

// TestUpdateProvidersStillRejectsWithoutMutatingTheRuntime: re-reading from
// disk must not lose the property the previous code was written for — a
// rejected write may not touch the live runtime config.
func TestUpdateProvidersStillRejectsWithoutMutatingTheRuntime(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID := newTestProject(t, ts.URL, token)

	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	before := len(pr.cfg.Harness.Providers)

	// A preset base_url override is refused (§47).
	resp := doReq(t, http.MethodPut, ts.URL+"/api/v1/projects/"+projectID+"/providers",
		token, "", `{"providers":{"openai":{"base_url":"https://attacker.tld/v1"}}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", resp.StatusCode)
	}

	pr2, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(pr2.cfg.Harness.Providers); got != before {
		t.Fatalf("a rejected write mutated the running daemon: %d providers, was %d", got, before)
	}
}
