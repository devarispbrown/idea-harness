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

// TestSimilarJudgment pins the token-Jaccard threshold (review B4, adopted
// #8: >= 0.85 after normalization), replacing the old overlap-over-shorter-
// side measure (>= 0.6), which wrongly merged genuinely distinct judgments
// that happened to share a couple of common words -- the ICP round's "53
// things need your judgment" traces partly to over-eager merging like this
// masking, then un-masking, decisions as wording drifted cycle to cycle.
func TestSimilarJudgment(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		// Same question, case/punctuation/whitespace only: identical token
		// sets, Jaccard 1.0.
		{"Which DNS provider should we use?", "which dns provider should we use", true},
		// The exact pair review B4's test requirement names: distinct
		// subjects (database vs cache) sharing only stop-adjacent words.
		// Tokens {which,database,use} vs {which,cache,use}: Jaccard 2/4=0.5.
		{"Which database should we use?", "Which cache should we use?", false},
		// Shares "many/mailboxes/domain/purchase" but one side adds
		// "included/default": Jaccard 4/6 = 0.667 -- below 0.85, correctly
		// kept apart even though a human would call these "about the same
		// thing" (the old 0.6-of-shorter-side rule merged this one).
		{"How many mailboxes are included by default per domain purchase?", "How many mailboxes per domain purchase?", false},
		{"How is billing structured?", "How is pricing structured across domain and email?", false},
		{"Which DNS provider should we use?", "Which DNS provider is best?", false},
		{"What is the primary user?", "What level of customization is in scope for v1?", false},
	}
	for _, c := range cases {
		if got := similarJudgment(normalizeJudgment(c.a), normalizeJudgment(c.b)); got != c.want {
			t.Errorf("similarJudgment(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// TestJaccardSimilarity pins the raw metric independent of the merge
// threshold, so a future threshold change cannot silently also change the
// arithmetic.
func TestJaccardSimilarity(t *testing.T) {
	cases := []struct {
		a, b string
		want float64
	}{
		{"which database use", "which cache use", 0.5},
		{"which dns provider use", "which dns provider use", 1},
		{"", "which dns provider use", 0},
	}
	for _, c := range cases {
		if got := jaccardSimilarity(c.a, c.b); got != c.want {
			t.Errorf("jaccardSimilarity(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
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

// TestCreateDecisionIfNewRequiresSameType proves the merge is scoped by
// DecisionType, not text alone (review B4, adopted #8): the same words asked
// about a different kind of decision are two distinct judgments, not one.
func TestCreateDecisionIfNewRequiresSameType(t *testing.T) {
	fp := scriptedProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	product := &domain.Decision{InitiativeID: ini.ID, Type: domain.DecisionProduct, Question: "Encrypt at rest?", Recommendation: "yes"}
	if err := eng.createDecisionIfNew(ctx, ini.ID, product); err != nil {
		t.Fatal(err)
	}
	ops := &domain.Decision{InitiativeID: ini.ID, Type: domain.DecisionOperations, Question: "Encrypt at rest?", Recommendation: "yes"}
	if err := eng.createDecisionIfNew(ctx, ini.ID, ops); err != nil {
		t.Fatal(err)
	}

	decs, err := db.ListDecisions(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(decs) != 2 {
		t.Fatalf("expected 2 decisions (different types), got %d", len(decs))
	}
}

// TestCreateQuestionIfNewLinksReraisedAnsweredQuestion proves review B4's "a
// question already answered is not re-created in a later cycle. If a new
// cycle raises it again, link it to the answered one": the duplicate is not
// created, and a question.reraised_answered event names the original.
func TestCreateQuestionIfNewLinksReraisedAnsweredQuestion(t *testing.T) {
	fp := scriptedProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if err := eng.createQuestionIfNew(ctx, ini.ID, "Which DNS provider should we use?"); err != nil {
		t.Fatal(err)
	}
	qs, err := db.ListQuestions(ctx, ini.ID)
	if err != nil || len(qs) != 1 {
		t.Fatalf("setup: expected 1 question, got %d (err=%v)", len(qs), err)
	}
	answered := qs[0]
	digest := eng.storeBlob("eNom")
	if err := db.AnswerQuestion(ctx, answered.ID, digest, "human"); err != nil {
		t.Fatal(err)
	}

	// A later cycle raises the same judgment again.
	if err := eng.createQuestionIfNew(ctx, ini.ID, "which dns provider should we use"); err != nil {
		t.Fatal(err)
	}

	qs, err = db.ListQuestions(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 1 {
		t.Fatalf("the answered question was re-created: got %d questions, want 1", len(qs))
	}

	events, err := db.ListEvents(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.EventType == "question.reraised_answered" && e.AggregateID == answered.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a question.reraised_answered event linking to the answered question")
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
