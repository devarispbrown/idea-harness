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
	if _, err := eng.createDecisionIfNew(ctx, ini.ID, first); err != nil {
		t.Fatal(err)
	}

	// A later revise cycle re-lists the same decision with a [Source: ...] prefix.
	relisted := &domain.Decision{
		InitiativeID: ini.ID, Type: domain.DecisionProduct,
		Question: "[Source: external_untrusted DEC-1] Which DNS provider?", Recommendation: "Use eNom",
	}
	if _, err := eng.createDecisionIfNew(ctx, ini.ID, relisted); err != nil {
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
	if _, err := eng.createDecisionIfNew(ctx, ini.ID, product); err != nil {
		t.Fatal(err)
	}
	ops := &domain.Decision{InitiativeID: ini.ID, Type: domain.DecisionOperations, Question: "Encrypt at rest?", Recommendation: "yes"}
	if _, err := eng.createDecisionIfNew(ctx, ini.ID, ops); err != nil {
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
	if _, err := eng.createDecisionIfNew(ctx, ini.ID, d); err != nil {
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

// TestCreateDecisionIfNewReturnsTheMatchedExistingID is review M1: on a
// near-duplicate merge, the return value must be the EXISTING decision's id
// (the one the near-duplicate merged into), not empty and not a new one --
// callers rely on this to wire something to "the decision this call just
// represents" without re-querying by (possibly now-mismatched) text.
func TestCreateDecisionIfNewReturnsTheMatchedExistingID(t *testing.T) {
	fp := scriptedProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	first := &domain.Decision{InitiativeID: ini.ID, Type: domain.DecisionProduct, Question: "Which DNS provider?", Recommendation: "Use eNom"}
	firstID, err := eng.createDecisionIfNew(ctx, ini.ID, first)
	if err != nil {
		t.Fatal(err)
	}
	if firstID == "" || firstID != first.ID {
		t.Fatalf("expected the first call to return the newly-created id %q, got %q", first.ID, firstID)
	}

	relisted := &domain.Decision{InitiativeID: ini.ID, Type: domain.DecisionProduct, Question: "[Source: external_untrusted DEC-1] Which DNS provider?", Recommendation: "Use eNom"}
	relistedID, err := eng.createDecisionIfNew(ctx, ini.ID, relisted)
	if err != nil {
		t.Fatal(err)
	}
	if relistedID != firstID {
		t.Fatalf("expected the merge to return the EXISTING decision's id %q, got %q", firstID, relistedID)
	}
}

// TestRequirementWiresToMergedDecisionByThisCyclesWording is review M1's
// end-to-end case. A decision already exists from an earlier cycle. This
// cycle's PRD re-lists a near-duplicate (not exact-text-identical) decision
// and a requirement that cites THIS CYCLE's exact wording via its `decision`
// field. The near-duplicate merges into the existing decision (by
// createDecisionIfNew's own rule), and the requirement must still be wired
// to it via derived_from -- re-fetching decisions and matching the ref
// against their STORED (earlier-cycle) wording would miss this, since the
// ref names wording that was never persisted.
func TestRequirementWiresToMergedDecisionByThisCyclesWording(t *testing.T) {
	fp := scriptedProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	existing := &domain.Decision{
		InitiativeID: ini.ID, Type: domain.DecisionProduct,
		Question: "Which DNS provider should we use for hosting?", Recommendation: "eNom",
	}
	if err := db.CreateDecision(ctx, existing); err != nil {
		t.Fatal(err)
	}

	// "will" replaces "should" -- a different exact/normalized string, but
	// both are stop words, so the token sets ({dns, provider, use, hosting})
	// are identical and this cycle's wording is a Jaccard-1.0 near-duplicate
	// that merges into `existing`.
	fp.SetResponse("prd", `{"summary":"PRD","requirements":[{"statement":"Register the domain","priority":"must",`+
		`"acceptance_criteria":[{"metric":"m","target":"t","condition":"c"}],`+
		`"decision":"Which DNS provider will we use for hosting?"}],`+
		`"decisions":[{"question":"Which DNS provider will we use for hosting?","options":["eNom"],"recommendation":"eNom"}],`+
		`"open_questions":[],"risks":[]}`)

	if err := eng.runProductDefinition(ctx, ini.ID, nil, ""); err != nil {
		t.Fatal(err)
	}

	decs, err := db.ListDecisions(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(decs) != 1 {
		t.Fatalf("expected the re-listed decision to merge into the existing one, got %d: %+v", len(decs), decs)
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
		if e.EdgeType == domain.EdgeDerivedFrom && e.ToID == existing.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the requirement to be wired (derived_from) to the merged-into decision %s, got edges %+v", existing.ID, edges)
	}
}

// TestSimilarDecisionMergesSameSubjectAcrossWordingDrift is review ICP
// re-run #2: a real run had the model re-ask essentially the same rate-limit
// algorithm choice each round with different phrasing, creating DEC-8/9/10
// as near-duplicates of DEC-5/6/7 instead of merging. Pure text similarity
// between these two phrasings falls short of nearDuplicateThreshold, but
// they offer the same options, so similarDecision must still merge them.
func TestSimilarDecisionMergesSameSubjectAcrossWordingDrift(t *testing.T) {
	a := "Which rate limiting algorithm should we use for the API?"
	b := "Which rate limiting algorithm should we use for the endpoints?"
	if jaccardSimilarity(normalizeJudgment(a), normalizeJudgment(b)) >= nearDuplicateThreshold {
		t.Fatal("test setup: these two phrasings must NOT already clear the pure-text threshold")
	}
	aOpts := []string{"token bucket", "sliding window", "fixed window"}
	bOpts := []string{"fixed window", "token bucket", "sliding window"}
	if !similarDecision(a, b, aOpts, bOpts) {
		t.Fatalf("expected same-subject decisions with matching options to merge:\n a=%q opts=%v\n b=%q opts=%v", a, aOpts, b, bOpts)
	}
}

// TestSimilarDecisionKeepsFalseMergeProtection is review ICP re-run #2's
// explicit guard: two decisions that merely offer the same generic options
// (a plain yes/no choice) but are about unrelated subjects must NOT merge
// just because their options match -- the text floor is what stops it.
func TestSimilarDecisionKeepsFalseMergeProtection(t *testing.T) {
	a := "Should offline mode be supported in v1?"
	b := "Should request bodies be logged for debugging?"
	opts := []string{"yes", "no"}
	if similarDecision(a, b, opts, opts) {
		t.Fatalf("unrelated decisions must not merge on identical generic options alone: %q vs %q", a, b)
	}

	// The review B4 pair ("which database?" vs "which cache?") must also
	// stay apart even when given plausible, non-identical options: distinct
	// subjects, low text similarity, and options that do not clear the
	// same-subject threshold either.
	if similarDecision("Which database should we use?", "Which cache should we use?",
		[]string{"postgres", "mysql"}, []string{"redis", "memcached"}) {
		t.Fatal("distinct subjects with distinct options must not merge")
	}
}

// TestSimilarDecisionKeepsDatabaseCachePairApartEvenWithMatchingOptions is
// review MEDIUM #5, proven: the database/cache pair's text similarity lands
// EXACTLY on sameSubjectTextFloor (2/4 = 0.5, per nearDuplicateThreshold's
// own worked example), and with a `< sameSubjectTextFloor` early-return that
// value fell through to the options check -- so a database/cache-style pair
// that also happened to offer near-identical, generic options (here, both a
// plain yes/no) merged anyway, contradicting the documented "correctly kept
// apart" claim (this comment, and CHANGELOG.md). The floor must be strict.
func TestSimilarDecisionKeepsDatabaseCachePairApartEvenWithMatchingOptions(t *testing.T) {
	a, b := "Which database should we use?", "Which cache should we use?"
	textSim := jaccardSimilarity(normalizeJudgment(a), normalizeJudgment(b))
	if textSim != 0.5 {
		t.Fatalf("test setup: text similarity = %v, want exactly 0.5 (the documented boundary case)", textSim)
	}
	opts := []string{"yes", "no"}
	if optionsSimilarity(opts, opts) != 1 {
		t.Fatal("test setup: identical options must have similarity 1")
	}
	if similarDecision(a, b, opts, opts) {
		t.Fatalf("%q vs %q merged despite being the documented kept-apart pair, "+
			"just because both happened to offer matching generic options (text=%v)", a, b, textSim)
	}
}

// TestOptionsSimilarity pins the raw options metric independent of the merge
// threshold, so a future threshold change cannot silently also change the
// arithmetic (mirrors TestJaccardSimilarity for options lists).
func TestOptionsSimilarity(t *testing.T) {
	cases := []struct {
		a, b []string
		want float64
	}{
		{[]string{"token bucket", "fixed window"}, []string{"fixed window", "token bucket"}, 1},
		{[]string{"yes"}, []string{"no"}, 0},
		{nil, []string{"yes"}, 0},
	}
	for _, c := range cases {
		if got := optionsSimilarity(c.a, c.b); got != c.want {
			t.Errorf("optionsSimilarity(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// TestCreateDecisionIfNewMergesRealisticReRunPair is the end-to-end version
// of TestSimilarDecisionMergesSameSubjectAcrossWordingDrift, through the
// actual dedup path createDecisionIfNew uses, proving DEC-8/9/10 would now
// merge into DEC-5/6/7 rather than being created as new rows (review ICP
// re-run #2).
func TestCreateDecisionIfNewMergesRealisticReRunPair(t *testing.T) {
	fp := scriptedProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	first := &domain.Decision{
		InitiativeID: ini.ID, Type: domain.DecisionProduct,
		Question:       "Which rate limiting algorithm should we use for the API?",
		Options:        []string{"token bucket", "sliding window", "fixed window"},
		Recommendation: "token bucket",
	}
	firstID, err := eng.createDecisionIfNew(ctx, ini.ID, first)
	if err != nil {
		t.Fatal(err)
	}

	// A later round re-asks it with drifted wording (below the pure-text
	// merge threshold on its own) but the same options, as a real ICP
	// re-run's DEC-8 did against DEC-5.
	relisted := &domain.Decision{
		InitiativeID: ini.ID, Type: domain.DecisionProduct,
		Question:       "Which rate limiting algorithm should we use for the endpoints?",
		Options:        []string{"fixed window", "token bucket", "sliding window"},
		Recommendation: "token bucket",
	}
	relistedID, err := eng.createDecisionIfNew(ctx, ini.ID, relisted)
	if err != nil {
		t.Fatal(err)
	}
	if relistedID != firstID {
		t.Fatalf("expected the re-worded, same-options decision to merge into %q, got %q", firstID, relistedID)
	}

	decs, err := db.ListDecisions(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(decs) != 1 {
		t.Fatalf("expected 1 decision after merge, got %d: %+v", len(decs), decs)
	}
}

// TestTokenizeKeepsNumericTokensRegardlessOfLength is review M2: a version
// number or a quantity must not be dropped just for being under the
// 3-character floor -- doing so collapsed "Support Python 3.11?" and
// "Support Python 3.12?" to the identical token set, and likewise "Max 10 or
// 50 items per page?" and "Max 20 or 25 items per page?", merging genuinely
// distinct questions.
func TestTokenizeKeepsNumericTokensRegardlessOfLength(t *testing.T) {
	cases := []struct {
		a, b string
	}{
		{"Support Python 3.11?", "Support Python 3.12?"},
		{"Max 10 or 50 items per page?", "Max 20 or 25 items per page?"},
	}
	for _, c := range cases {
		if similarJudgment(normalizeJudgment(c.a), normalizeJudgment(c.b)) {
			t.Errorf("similarJudgment(%q, %q) = true, want false: the numbers differ", c.a, c.b)
		}
	}

	// A short numeric token is kept even alone.
	got := tokenize("3.11")
	want := []string{"3", "11"}
	if len(got) != len(want) {
		t.Fatalf("tokenize(%q) = %v, want %v", "3.11", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tokenize(%q) = %v, want %v", "3.11", got, want)
		}
	}
}
