package workflow

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
)

// countCalls returns how many of fp's recorded calls asked for schemaName.
func countCalls(fp *model.FakeProvider, schemaName string) int {
	n := 0
	for _, r := range fp.Calls() {
		if r.JSONSchemaName == schemaName {
			n++
		}
	}
	return n
}

// TestRetryResumesAfterTheLastCompletedPhase pins the ICP walkthrough finding:
// a provider failure mid-PRD-authoring, followed by `idea retry`, used to
// restart the whole framing→research→PRD slice from scratch -- re-asking
// framing's clarifying questions and re-running research that had already
// succeeded, which from the outside looked like the human's prior answers had
// been thrown away. Retry must resume after the last phase whose artifact
// already exists, not redo phases that already succeeded.
func TestRetryResumesAfterTheLastCompletedPhase(t *testing.T) {
	fp := scriptedProvider()
	// Break the PRD response so runModel exhausts its 3 attempts and the
	// initiative fails partway through product definition, after framing and
	// research have both already succeeded.
	fp.SetResponse("prd", `not valid json`)

	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "build it"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	status, err := eng.Run(ctx, ini.ID, ini.Description)
	if err == nil {
		t.Fatal("expected the broken PRD response to fail the run")
	}
	if status != domain.InitiativeFailed {
		t.Fatalf("status = %q, want failed", status)
	}

	// Framing and research each ran once; the PRD schema was attempted 3 times
	// (runModel's retry-on-decode-failure ceiling) before the run gave up.
	if n := countCalls(fp, "idea_brief"); n != 1 {
		t.Fatalf("idea_brief called %d times before the first failure, want 1", n)
	}
	if n := countCalls(fp, "research_brief"); n != 1 {
		t.Fatalf("research_brief called %d times before the first failure, want 1", n)
	}
	if n := countCalls(fp, "prd"); n != 3 {
		t.Fatalf("prd called %d times before the first failure, want 3", n)
	}

	versions, err := db.ListArtifacts(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[domain.ArtifactKind]int{}
	for _, v := range versions {
		kinds[v.Kind]++
	}
	if kinds[domain.ArtifactIdeaBrief] != 1 || kinds[domain.ArtifactResearchBrief] != 1 || kinds[domain.ArtifactPRD] != 0 {
		t.Fatalf("artifacts after the failure = %+v, want idea_brief=1 research_brief=1 prd=0", kinds)
	}

	// Fix the PRD response and retry. A correct resume authors the PRD without
	// touching framing or research again.
	fp.SetResponse("prd", `{"summary":"PRD","requirements":[{"statement":"Meter usage events","priority":"must","acceptance_criteria":[{"metric":"events","target":">= 1","condition":"per request"}]}],"decisions":[],"open_questions":[],"risks":[]}`)

	status, err = eng.Retry(ctx, ini.ID)
	if err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	if status != domain.InitiativeApproved {
		t.Fatalf("status after retry = %q, want approved", status)
	}

	if n := countCalls(fp, "idea_brief"); n != 1 {
		t.Errorf("idea_brief called %d times total, want 1 -- retry re-ran framing that had already succeeded", n)
	}
	if n := countCalls(fp, "research_brief"); n != 1 {
		t.Errorf("research_brief called %d times total, want 1 -- retry re-ran research that had already succeeded", n)
	}

	versions, err = db.ListArtifacts(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	kinds = map[domain.ArtifactKind]int{}
	for _, v := range versions {
		kinds[v.Kind]++
	}
	if kinds[domain.ArtifactIdeaBrief] != 1 || kinds[domain.ArtifactResearchBrief] != 1 || kinds[domain.ArtifactPRD] != 1 {
		t.Fatalf("artifacts after retry = %+v, want exactly one of each (no duplicate framing/research)", kinds)
	}
}

// TestRetryStillRunsEverythingWhenFramingItselfFailed is the other direction:
// when the very first phase never produced an artifact, retry must still run
// the whole slice, exactly as before this change.
func TestRetryStillRunsEverythingWhenFramingItselfFailed(t *testing.T) {
	fp := scriptedProvider()
	fp.SetResponse("idea_brief", `not valid json`)

	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "build it"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Run(ctx, ini.ID, ini.Description); err == nil {
		t.Fatal("expected the broken idea_brief response to fail the run")
	}

	fp.SetResponse("idea_brief", `{"title":"x","summary":"x","assumptions":[],"open_questions":[]}`)
	status, err := eng.Retry(ctx, ini.ID)
	if err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	if status != domain.InitiativeApproved {
		t.Fatalf("status after retry = %q, want approved", status)
	}
	if n := countCalls(fp, "idea_brief"); n != 4 { // 3 failed attempts + 1 retry success
		t.Errorf("idea_brief called %d times, want 4", n)
	}
	if n := countCalls(fp, "research_brief"); n != 1 {
		t.Errorf("research_brief called %d times, want 1 (only during the retry)", n)
	}
}

func TestModelOverrideResolution(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, _ := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})

	const iniID = "some-initiative"
	eng.SetModelOverride(iniID, "openai/gpt-test")
	if got := eng.modelSpecFor(iniID, "product_manager"); got != "openai/gpt-test" {
		t.Fatalf("override not applied: %q", got)
	}

	// A different initiative is not affected by the override.
	if got := eng.modelSpecFor("other-initiative", "product_manager"); got == "openai/gpt-test" {
		t.Fatalf("override bled into another initiative: %q", got)
	}

	eng.clearModelOverride(iniID)
	if got := eng.modelSpecFor(iniID, "product_manager"); got == "openai/gpt-test" {
		t.Fatal("override not cleared")
	}
}
