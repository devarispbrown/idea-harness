package projection

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/risk"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// nullArrayFields walks marshalled JSON and reports every key whose value is
// null but whose Go type is a slice — the shape that makes a client write
// `(x ?? []).map(...)` or crash.
func nullArrayFields(t *testing.T, v any) []string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	// Which keys are slices is known from the struct tags, so rather than
	// reflecting, take the simpler route: any key whose value is literally null
	// is suspect, and the caller's fixture populates every non-slice field it
	// cares about.
	var found []string
	for _, m := range regexp.MustCompile(`"([a-z_]+)":null`).FindAllStringSubmatch(string(raw), -1) {
		found = append(found, m[1])
	}
	sort.Strings(found)
	return found
}

// TestProjectionsNeverEmitNullArrays pins R3.
//
// An empty initiative's projections marshalled `"assertions":null`,
// `"requirements":null` and so on, because the builders appended to a nil
// slice. Every consumer then has to distinguish null from [] for no reason, and
// a generated client whose schema says `type: array` rejects the response
// outright. The fix is to initialise at the source, so this asserts on the
// wire shape of a genuinely empty initiative — the case that produces it.
func TestProjectionsNeverEmitNullArrays(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	// Deliberately bare: no requirements, decisions, questions or assertions.
	ini := &domain.Initiative{Title: "Bare", Description: "Nothing attached to it"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	// And then the same check with one of each, because the top-level slices
	// were only half the defect: a populated collection exposes the *nested*
	// views, whose own slices — a requirement's criteria, an assertion's
	// provenance, a decision's dissents — were still nil. The first version of
	// this test used a bare initiative only and reported the sweep complete
	// while nine nested fields still marshalled null.
	populated := &domain.Initiative{Title: "Populated", Description: "One of each"}
	if err := db.CreateInitiative(ctx, populated); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRequirement(ctx, &domain.Requirement{
		InitiativeID: populated.ID, Statement: "Do the thing", Priority: domain.PriorityMust,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateDecision(ctx, &domain.Decision{
		InitiativeID: populated.ID, Question: "Which way?", Type: domain.DecisionProduct,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateQuestion(ctx, &domain.Question{
		InitiativeID: populated.ID, Question: "Retention?",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateAssertion(ctx, &domain.Assertion{
		InitiativeID: populated.ID, Type: domain.AssertionFact,
		Statement: "It is so", Confidence: domain.ConfidenceHigh, Source: "test",
	}); err != nil {
		t.Fatal(err)
	}
	pu, err := BuildUnderstanding(ctx, db, *populated)
	if err != nil {
		t.Fatal(err)
	}
	if got := nullArrayFields(t, pu); len(got) > 0 {
		t.Errorf("a populated understanding emits null for %s — the nested views have "+
			"their own slices, and initialising only the top-level ones leaves these",
			strings.Join(got, ", "))
	}

	u, err := BuildUnderstanding(ctx, db, *ini)
	if err != nil {
		t.Fatal(err)
	}
	if got := nullArrayFields(t, u); len(got) > 0 {
		t.Errorf("understanding emits null for %s — initialise the slice at its source", strings.Join(got, ", "))
	}

	// Guidance across every status, not just the bare one: a review of the fix
	// plan claimed `/guidance`'s `affected` was a sixth null. It is not — every
	// branch of BuildGuidance sets it to a literal — and this loop is what
	// settles that rather than a reading of the function.
	for _, st := range []domain.InitiativeStatus{
		"", domain.InitiativeNeedsHumanJudgment, domain.InitiativeApproved,
		domain.InitiativePlanApproved, domain.InitiativeFailed, domain.InitiativeComplete,
	} {
		withStatus := *ini
		withStatus.Status = st
		g := BuildGuidance(withStatus, u, nil)
		if got := nullArrayFields(t, g); len(got) > 0 {
			t.Errorf("guidance for status %q emits null for %s", st, strings.Join(got, ", "))
		}
	}

	c, err := BuildChanged(ctx, db, ini.ID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if got := nullArrayFields(t, c); len(got) > 0 {
		t.Errorf("changed emits null for %s — initialise the slice at its source", strings.Join(got, ", "))
	}
}

// TestRiskProfileSignalsAreNeverNull covers the one endpoint the projection
// tests cannot reach: /risk builds its profile in internal/risk, and an
// initiative with no detected signals — the normal case — emitted
// `"signals":null`. Found by walking every GET endpoint on a live daemon
// rather than by reading, after a review pointed out the first sweep had
// stopped at the top-level projection slices.
func TestRiskProfileSignalsAreNeverNull(t *testing.T) {
	p := risk.ComputeRiskProfile("ini-1", nil)
	if p.Signals == nil {
		t.Fatal("RiskProfile.Signals is nil; /risk emits \"signals\":null")
	}
	if got := nullArrayFields(t, p); len(got) > 0 {
		t.Errorf("risk profile emits null for %s", strings.Join(got, ", "))
	}
}
