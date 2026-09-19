package workflow

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
)

// TestRequirementWiresToStandingDecisionNotReRaisedThisCycle reproduces half
// of the ICP re-run's own defect #4: a decision (DEC-3, say) was decided in
// an EARLIER cycle and this cycle's PRD -- as instructed -- does not re-list
// it in `decisions[]` (only genuinely open choices go there), but a
// requirement regenerated this cycle still names it verbatim in its own
// `decision` field. Before this fix, the wiring loop only matched against
// decIDByNorm, built from THIS cycle's `decisions[]` alone, so the
// requirement was never wired at all -- leaving a live requirement with no
// derived_from edge to the decision it plainly still derives from, which is
// exactly what left the decision's reopen with "0 stale items" and an empty
// /impact for that requirement.
func TestRequirementWiresToStandingDecisionNotReRaisedThisCycle(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	standing := &domain.Decision{
		InitiativeID: ini.ID, Type: domain.DecisionProduct,
		Question: "Should rate limiting be per-key or per-IP?", Recommendation: "per-key",
	}
	if err := db.CreateDecision(ctx, standing); err != nil {
		t.Fatal(err)
	}
	if err := db.ResolveDecision(ctx, standing.ID, "per-key", "human"); err != nil {
		t.Fatal(err)
	}

	// This cycle's PRD regenerates a requirement that names the standing
	// decision verbatim, but (per the "do not re-list decisions already
	// marked DECIDED" instruction) does NOT re-raise it in decisions[].
	fp.SetResponse("prd", `{"summary":"PRD","requirements":[{"statement":"Enforce per-key rate limits","priority":"must",`+
		`"acceptance_criteria":[{"metric":"m","target":"t","condition":"c"}],`+
		`"decision":"Should rate limiting be per-key or per-IP?"}],`+
		`"decisions":[],"open_questions":[],"risks":[]}`)

	if err := eng.runProductDefinition(ctx, ini.ID, nil); err != nil {
		t.Fatal(err)
	}

	reqs, err := db.ListRequirements(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 {
		t.Fatalf("expected 1 requirement, got %d", len(reqs))
	}

	edges, err := db.ListGraphEdgesFrom(ctx, ini.ID, "requirement", reqs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range edges {
		if e.EdgeType == domain.EdgeDerivedFrom && e.ToID == standing.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the requirement wired (derived_from) to the standing decision %s, got edges %+v", standing.ID, edges)
	}
}
