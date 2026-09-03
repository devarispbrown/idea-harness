package workflow

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
)

func TestNormalizeJudgment(t *testing.T) {
	cases := map[string]string{
		"Which DNS provider?": "which dns provider",
		"[Source: external_untrusted DEC-1] Which DNS provider?":       "which dns provider",
		"[Source: external_untrusted DEC-1/DEC-4] Which DNS provider?": "which dns provider",
		"  Which   DNS   Provider?!  ":                                 "which dns provider",
		"How is billing structured?":                                   "how is billing structured",
		"How is billing structured":                                    "how is billing structured",
	}
	for in, want := range cases {
		if got := normalizeJudgment(in); got != want {
			t.Errorf("normalizeJudgment(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSimilarJudgment(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"How many mailboxes are included by default per domain purchase?", "How many mailboxes per domain purchase?", true},
		{"How is billing structured?", "How is pricing structured across domain and email?", false},
		{"Which DNS provider should we use?", "Which DNS provider is best?", true},
		{"What is the primary user?", "What level of customization is in scope for v1?", false},
	}
	for _, c := range cases {
		if got := similarJudgment(normalizeJudgment(c.a), normalizeJudgment(c.b)); got != c.want {
			t.Errorf("similarJudgment(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestCreateDecisionIfNewDedups(t *testing.T) {
	fp := scriptedProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	first := &domain.Decision{
		InitiativeID: ini.ID, Type: domain.DecisionProduct,
		Question: "Which DNS provider?", Recommendation: "Use eNom",
	}
	if err := eng.createDecisionIfNew(ctx, ini.ID, first); err != nil {
		t.Fatal(err)
	}

	// A later revise cycle re-lists the same decision with a [Source: ...] prefix.
	relisted := &domain.Decision{
		InitiativeID: ini.ID, Type: domain.DecisionProduct,
		Question: "[Source: external_untrusted DEC-1] Which DNS provider?", Recommendation: "Use eNom",
	}
	if err := eng.createDecisionIfNew(ctx, ini.ID, relisted); err != nil {
		t.Fatal(err)
	}

	decs, err := db.ListDecisions(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(decs) != 1 {
		t.Fatalf("expected 1 decision after dedup, got %d", len(decs))
	}
}

func TestCreateDecisionResolvesEquivalentQuestion(t *testing.T) {
	fp := scriptedProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	// Framing raises the judgment as an open question.
	if err := eng.createQuestionIfNew(ctx, ini.ID, "How is billing structured?"); err != nil {
		t.Fatal(err)
	}
	// Product definition raises the same judgment as a decision — it must win
	// and resolve the open question.
	d := &domain.Decision{
		InitiativeID: ini.ID, Type: domain.DecisionProduct,
		Question: "How is billing structured?", Recommendation: "Flat bundle",
	}
	if err := eng.createDecisionIfNew(ctx, ini.ID, d); err != nil {
		t.Fatal(err)
	}

	qs, err := db.ListQuestions(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 1 || qs[0].Status != domain.QuestionResolved {
		t.Fatalf("expected the overlapping question to be resolved, got %+v", qs)
	}
}
