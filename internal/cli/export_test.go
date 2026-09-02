package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// seedTwoInitiativesWithArtifacts registers two initiatives ("ini-1", the
// older one, and "ini-2", the newest) each with their own artifact of kind
// "prd" and "execution_plan". The artifacts list endpoint answers according to
// the initiative_id query param — never all artifacts in the project — so a
// test can tell whether the CLI actually scoped its request or fell back to
// picking the last artifact across the whole project.
func seedTwoInitiativesWithArtifacts(f *fakeDaemon) {
	f.json("GET /api/v1/projects/{projectId}/initiatives", []map[string]any{
		{"id": "ini-1", "title": "Older initiative", "status": "plan_approved"},
		{"id": "ini-2", "title": "Newest initiative", "status": "plan_approved"},
	})

	artifactsByIni := map[string][]cliArtifact{
		"ini-1": {{ID: "art-old-prd", Kind: "prd", Version: 1}, {ID: "art-old-plan", Kind: "execution_plan", Version: 1}},
		"ini-2": {{ID: "art-new-prd", Kind: "prd", Version: 1}, {ID: "art-new-plan", Kind: "execution_plan", Version: 1}},
	}
	f.mux.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{initiativeId}/artifacts", func(w http.ResponseWriter, r *http.Request) {
		iniID := r.PathValue("initiativeId")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(artifactsByIni[iniID])
	})

	bodies := map[string]string{
		"art-old-prd":  "OLD PRD content",
		"art-new-prd":  "NEW PRD content",
		"art-old-plan": "OLD PLAN content",
		"art-new-plan": "NEW PLAN content",
	}
	f.mux.HandleFunc("GET /api/v1/projects/{projectId}/artifacts/{id}/raw", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(bodies[r.PathValue("id")]))
	})
	f.mux.HandleFunc("GET /api/v1/projects/{projectId}/artifacts/{id}/body", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cliArtifactBody{Kind: "prd", Version: 1, Body: bodies[r.PathValue("id")]})
	})
}

// TestExportScopesArtifactToResolvedInitiative is the regression test for the
// bug found in adversarial review: cmdExport ignored the initiative entirely,
// picking the last artifact of the requested kind in the whole project. With
// two initiatives that each have their own "prd" artifact, the unscoped
// version of cmdExport renders whichever one the fake project-wide list
// (there isn't one — the daemon requires initiative scoping) or map iteration
// happens to return; the fixed version must render exactly the artifact that
// belongs to the resolved initiative, and must ask the daemon with
// initiative_id set to that initiative.
func TestExportScopesArtifactToResolvedInitiative(t *testing.T) {
	t.Run("defaults to the newest initiative", func(t *testing.T) {
		f := newFakeDaemon(t)
		seedTwoInitiativesWithArtifacts(f)

		stdout, _, err := run(t, "export", "prd")
		if err != nil {
			t.Fatalf("idea export prd: %v", err)
		}
		if strings.TrimSpace(stdout) != "NEW PRD content" {
			t.Fatalf("idea export prd = %q, want the ini-2 (newest) artifact", stdout)
		}
		if !f.asked("/initiatives/ini-2/artifacts") {
			t.Fatalf("idea export prd did not scope the artifacts request to ini-2: %v", f.requests)
		}
	})

	t.Run("an explicit initiative overrides the default", func(t *testing.T) {
		f := newFakeDaemon(t)
		seedTwoInitiativesWithArtifacts(f)

		stdout, _, err := run(t, "export", "prd", "ini-1")
		if err != nil {
			t.Fatalf("idea export prd ini-1: %v", err)
		}
		if strings.TrimSpace(stdout) != "OLD PRD content" {
			t.Fatalf("idea export prd ini-1 = %q, want the ini-1 artifact", stdout)
		}
		if !f.asked("/initiatives/ini-1/artifacts") {
			t.Fatalf("idea export prd ini-1 did not scope the artifacts request to ini-1: %v", f.requests)
		}
		if f.asked("/initiatives/ini-2/artifacts") {
			t.Fatalf("idea export prd ini-1 also asked about ini-2: %v", f.requests)
		}
	})
}

// TestPlanScopesArtifactToResolvedInitiative covers cmdPlan, which delegates
// to cmdExport with kind "execution_plan" (advance.go). Before the fix, the
// initiative positional it forwards was silently discarded by cmdExport, so
// `idea plan ini-1` could still render ini-2's plan.
func TestPlanScopesArtifactToResolvedInitiative(t *testing.T) {
	f := newFakeDaemon(t)
	seedTwoInitiativesWithArtifacts(f)

	stdout, _, err := run(t, "plan", "ini-1")
	if err != nil {
		t.Fatalf("idea plan ini-1: %v", err)
	}
	if strings.TrimSpace(stdout) != "OLD PLAN content" {
		t.Fatalf("idea plan ini-1 = %q, want the ini-1 artifact", stdout)
	}
	if !f.asked("/initiatives/ini-1/artifacts") {
		t.Fatalf("idea plan ini-1 did not scope the artifacts request to ini-1: %v", f.requests)
	}
}

// TestPRDScopesArtifactToResolvedInitiative covers cmdPRD (prd.go), which had
// its own duplicate of the same unscoped-selection bug.
func TestPRDScopesArtifactToResolvedInitiative(t *testing.T) {
	f := newFakeDaemon(t)
	seedTwoInitiativesWithArtifacts(f)

	stdout, _, err := run(t, "prd", "prd", "ini-1")
	if err != nil {
		t.Fatalf("idea prd prd ini-1: %v", err)
	}
	if !strings.Contains(stdout, "OLD PRD content") {
		t.Fatalf("idea prd prd ini-1 = %q, want the ini-1 artifact", stdout)
	}
	if !f.asked("/initiatives/ini-1/artifacts") {
		t.Fatalf("idea prd prd ini-1 did not scope the artifacts request to ini-1: %v", f.requests)
	}
}

// TestExportRejectsFlagsInsteadOfTreatingThemAsKind is the second half of the
// bug: cmdExport used to index args[0] directly for the kind, so `idea export
// --json` silently treated the flag itself as the artifact kind (and then
// failed with a confusing "no \"--json\" artifact" error) instead of reporting
// an unknown flag.
func TestExportRejectsFlagsInsteadOfTreatingThemAsKind(t *testing.T) {
	f := newFakeDaemon(t)
	seedTwoInitiativesWithArtifacts(f)

	_, _, err := run(t, "export", "--json")
	if err == nil {
		t.Fatal("idea export --json: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "--json") {
		t.Fatalf("error should name the rejected flag, got: %v", err)
	}
	if strings.Contains(err.Error(), "no \"--json\" artifact") {
		t.Fatalf("flag was still treated as the artifact kind: %v", err)
	}
}
