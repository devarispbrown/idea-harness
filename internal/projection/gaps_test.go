package projection

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// gapFixture builds a Product Graph through the store's own write paths --
// the same calls the workflow makes -- so every gap below is produced by the
// state a real run leaves, not by rows a test inserted around the API.
type gapFixture struct {
	t   *testing.T
	ctx context.Context
	db  *store.DB
	ini *domain.Initiative
}

func newGapFixture(t *testing.T) *gapFixture {
	t.Helper()
	f := &gapFixture{t: t, ctx: context.Background(), db: openProjTestDB(t)}
	f.ini = &domain.Initiative{Title: "billing", Description: "bill by usage", Status: domain.InitiativePlanApproved}
	if err := f.db.CreateInitiative(f.ctx, f.ini); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *gapFixture) req(code string, prio domain.RequirementPriority, criteria int) *domain.Requirement {
	f.t.Helper()
	r := &domain.Requirement{InitiativeID: f.ini.ID, Code: code, Statement: code + " statement",
		Priority: prio, Status: domain.RequirementApproved}
	for i := 0; i < criteria; i++ {
		r.AcceptanceCriteria = append(r.AcceptanceCriteria, domain.AcceptanceCriterion{Metric: "latency", Target: "100ms", Condition: "p99 <"})
	}
	if err := f.db.CreateRequirement(f.ctx, r); err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *gapFixture) task(title string, reqIDs ...string) *domain.Task {
	f.t.Helper()
	t := &domain.Task{InitiativeID: f.ini.ID, Title: title, Statement: title}
	if err := f.db.CreateTaskMulti(f.ctx, t, reqIDs, nil); err != nil {
		f.t.Fatal(err)
	}
	return t
}

func (f *gapFixture) changeSet(taskID string) *domain.ChangeSet {
	f.t.Helper()
	cs := &domain.ChangeSet{InitiativeID: f.ini.ID, TaskID: taskID, PatchDigest: "p"}
	if err := f.db.CreateChangeSet(f.ctx, cs); err != nil {
		f.t.Fatal(err)
	}
	return cs
}

func (f *gapFixture) applied(taskID string) *domain.ChangeSet {
	f.t.Helper()
	cs := f.changeSet(taskID)
	applyChangeSet(f.t, f.db, cs.ID)
	return cs
}

func (f *gapFixture) evidence(reqID string, i int, status domain.VerificationStatus, at time.Time) {
	f.t.Helper()
	if err := f.db.CreateVerificationEvidence(f.ctx, &domain.VerificationEvidence{
		InitiativeID: f.ini.ID, RequirementID: reqID, CriterionKey: domain.CriterionKey(reqID, i),
		Kind: domain.VerificationAcceptance, Status: status, VerifiedBy: "human:test", CreatedAt: at,
	}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *gapFixture) artifact(kind domain.ArtifactKind, at time.Time) {
	f.t.Helper()
	if err := f.db.CreateArtifact(f.ctx, &domain.Artifact{InitiativeID: f.ini.ID, Kind: kind},
		&domain.ArtifactVersion{BodyDigest: "d", CreatedAt: at}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *gapFixture) gaps(kinds ...domain.GapKind) []domain.Gap {
	f.t.Helper()
	ini, err := f.db.GetInitiative(f.ctx, f.ini.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	gaps, err := BuildGaps(f.ctx, f.db, ini, GapOptions{Kinds: kinds})
	if err != nil {
		f.t.Fatal(err)
	}
	return gaps
}

// find returns the gaps of kind whose first node has code.
func find(gaps []domain.Gap, kind domain.GapKind, code string) []domain.Gap {
	var out []domain.Gap
	for _, g := range gaps {
		if g.Kind == kind && len(g.Nodes) > 0 && g.Nodes[0].Code == code {
			out = append(out, g)
		}
	}
	return out
}

func withState(gaps []domain.Gap, state domain.GapState) []domain.Gap {
	var out []domain.Gap
	for _, g := range gaps {
		if g.State == state {
			out = append(out, g)
		}
	}
	return out
}

func one(t *testing.T, gaps []domain.Gap, kind domain.GapKind, code string) domain.Gap {
	t.Helper()
	got := find(gaps, kind, code)
	if len(got) != 1 {
		t.Fatalf("want exactly one %s gap on %s, got %d: %+v", kind, code, len(got), gaps)
	}
	return got[0]
}

func none(t *testing.T, gaps []domain.Gap, kind domain.GapKind, code string) {
	t.Helper()
	if got := find(gaps, kind, code); len(got) != 0 {
		t.Fatalf("want no %s gap on %s, got %+v", kind, code, got)
	}
}

// ---------- unimplemented_requirement ----------

func TestGapsUnimplementedRequirement(t *testing.T) {
	f := newGapFixture(t)
	f.artifact(domain.ArtifactExecutionPlan, time.Now())
	noTask := f.req("REQ-1", domain.PriorityMust, 1)
	planned := f.req("REQ-2", domain.PriorityShould, 1)
	built := f.req("REQ-3", domain.PriorityMust, 1)
	verified := f.req("REQ-4", domain.PriorityMust, 1)
	waived := f.req("REQ-5", domain.PriorityMust, 1)
	plannedTask := f.task("plan it", planned.ID)
	cs := f.changeSet(plannedTask.ID)
	for _, s := range [][2]domain.ChangeSetStatus{{domain.ChangeSetDraft, domain.ChangeSetChecksRunning}, {domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired}} {
		if err := f.db.TransitionChangeSet(f.ctx, cs.ID, s[0], s[1], ""); err != nil {
			t.Fatal(err)
		}
	}
	f.applied(f.task("build it", built.ID).ID)
	if err := f.db.MarkRequirementVerified(f.ctx, verified.ID, "human"); err != nil {
		t.Fatal(err)
	}
	if err := f.db.MarkRequirementWaived(f.ctx, waived.ID, "human"); err != nil {
		t.Fatal(err)
	}

	gaps := f.gaps(domain.GapUnimplementedRequirement)
	g := one(t, gaps, domain.GapUnimplementedRequirement, "REQ-1")
	if g.State != domain.GapStateNoTask || g.Severity != domain.SeverityHigh {
		t.Errorf("REQ-1 = %+v; a must requirement with no task is high", g)
	}
	// A change set holds work, so revise-plan would refuse: the gap must not name it.
	if strings.Contains(g.Next, "revise-plan") {
		t.Errorf("REQ-1 next names a command that refuses while work is held: %q", g.Next)
	}
	g = one(t, gaps, domain.GapUnimplementedRequirement, "REQ-2")
	if g.State != domain.GapStateNotApplied || g.Severity != domain.SeverityMedium || g.Next != "idea review "+cs.HumanID+" approve" {
		t.Errorf("REQ-2 = %+v; want not_applied, medium (should), next = review the pending change set", g)
	}
	none(t, gaps, domain.GapUnimplementedRequirement, "REQ-3") // applied
	none(t, gaps, domain.GapUnimplementedRequirement, "REQ-4") // a human verified it
	none(t, gaps, domain.GapUnimplementedRequirement, "REQ-5") // waived: out of scope
	_ = noTask
}

// Before planning, every requirement is unimplemented -- expected, so it is
// low and points at `idea advance` instead of failing a high gate.
func TestGapsUnimplementedBeforePlanIsLow(t *testing.T) {
	f := newGapFixture(t)
	f.req("REQ-1", domain.PriorityMust, 1)
	g := one(t, f.gaps(domain.GapUnimplementedRequirement), domain.GapUnimplementedRequirement, "REQ-1")
	if g.Severity != domain.SeverityLow || g.Next != "idea advance" {
		t.Errorf("pre-plan gap = %+v", g)
	}
}

// A no-op change set is applied but changed nothing: it does not implement
// its task (ICP re-run defect #2), so the requirement stays unimplemented.
func TestGapsNoOpChangeSetDoesNotImplement(t *testing.T) {
	f := newGapFixture(t)
	f.artifact(domain.ArtifactExecutionPlan, time.Now())
	r := f.req("REQ-1", domain.PriorityMust, 1)
	task := f.task("t", r.ID)
	cs := &domain.ChangeSet{InitiativeID: f.ini.ID, TaskID: task.ID, PatchDigest: "p", NoOp: true}
	if err := f.db.CreateChangeSet(f.ctx, cs); err != nil {
		t.Fatal(err)
	}
	applyChangeSet(t, f.db, cs.ID)
	g := one(t, f.gaps(domain.GapUnimplementedRequirement), domain.GapUnimplementedRequirement, "REQ-1")
	if g.State != domain.GapStateNotApplied || g.Next != "idea execute" {
		t.Errorf("no-op gap = %+v", g)
	}
}

// ---------- unverified_requirement / untested_acceptance_criterion ----------

func TestGapsVerificationStates(t *testing.T) {
	f := newGapFixture(t)
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)

	noEv := f.req("REQ-1", domain.PriorityMust, 2) // implemented, no evidence at all
	f.applied(f.task("t1", noEv.ID).ID)
	f.evidence(noEv.ID, 0, domain.VerificationNeedsManual, future) // placeholders only
	f.evidence(noEv.ID, 1, domain.VerificationNeedsManual, future)

	failed := f.req("REQ-2", domain.PriorityMust, 1)
	f.applied(f.task("t2", failed.ID).ID)
	f.evidence(failed.ID, 0, domain.VerificationFail, future)

	stale := f.req("REQ-3", domain.PriorityMust, 1)
	f.evidence(stale.ID, 0, domain.VerificationPass, past) // recorded...
	staleCS := f.applied(f.task("t3", stale.ID).ID)        // ...then the code changed

	fresh := f.req("REQ-4", domain.PriorityMust, 1)
	f.applied(f.task("t4", fresh.ID).ID)
	f.evidence(fresh.ID, 0, domain.VerificationPass, future) // recorded after the apply

	noCriteria := f.req("REQ-5", domain.PriorityShould, 0)

	waivedEv := f.req("REQ-6", domain.PriorityMust, 1)
	f.evidence(waivedEv.ID, 0, domain.VerificationWaived, future)

	// A pass later overridden by a fail is failed: the latest decisive row wins.
	flipped := f.req("REQ-7", domain.PriorityMust, 1)
	f.evidence(flipped.ID, 0, domain.VerificationPass, past)
	f.evidence(flipped.ID, 0, domain.VerificationFail, future)

	gaps := f.gaps(domain.GapUnverifiedRequirement, domain.GapUntestedAcceptanceCriterion)

	// no evidence: one gap per criterion, needs_manual does not count.
	for _, ac := range []string{"REQ-1/AC-1", "REQ-1/AC-2"} {
		g := one(t, gaps, domain.GapUntestedAcceptanceCriterion, ac)
		if g.State != domain.GapStateNoEvidence || g.Severity != domain.SeverityMedium || !strings.Contains(g.Why, "needs-manual placeholder") {
			t.Errorf("%s = %+v", ac, g)
		}
	}
	none(t, gaps, domain.GapUnverifiedRequirement, "REQ-1")

	g := one(t, gaps, domain.GapUnverifiedRequirement, "REQ-2")
	if g.State != domain.GapStateEvidenceFailed || g.Severity != domain.SeverityHigh || !strings.Contains(g.Why, "REQ-2/AC-1 recorded fail") {
		t.Errorf("REQ-2 = %+v", g)
	}
	none(t, gaps, domain.GapUntestedAcceptanceCriterion, "REQ-2/AC-1")

	g = one(t, gaps, domain.GapUnverifiedRequirement, "REQ-3")
	if g.State != domain.GapStateEvidenceStale || g.Severity != domain.SeverityMedium ||
		!strings.Contains(g.Why, staleCS.HumanID) || g.Next != "idea verify REQ-3 --pass" {
		t.Errorf("REQ-3 = %+v", g)
	}

	none(t, gaps, domain.GapUnverifiedRequirement, "REQ-4")
	none(t, gaps, domain.GapUntestedAcceptanceCriterion, "REQ-4/AC-1")

	g = one(t, gaps, domain.GapUnverifiedRequirement, "REQ-5")
	if g.State != domain.GapStateNoCriteria || g.Severity != domain.SeverityHigh {
		t.Errorf("REQ-5 = %+v", g)
	}
	_ = noCriteria

	none(t, gaps, domain.GapUnverifiedRequirement, "REQ-6")
	none(t, gaps, domain.GapUntestedAcceptanceCriterion, "REQ-6/AC-1")

	if g := one(t, gaps, domain.GapUnverifiedRequirement, "REQ-7"); g.State != domain.GapStateEvidenceFailed {
		t.Errorf("REQ-7 = %+v", g)
	}
}

// A revert after the pass is a code change too: what was verified is gone.
func TestGapsEvidenceStaleAfterRevert(t *testing.T) {
	f := newGapFixture(t)
	r := f.req("REQ-1", domain.PriorityMust, 1)
	cs := f.changeSet(f.task("t", r.ID).ID)
	applyChangeSet(t, f.db, cs.ID)
	// Evidence recorded after the apply: fresh.
	f.evidence(r.ID, 0, domain.VerificationPass, time.Now().Add(time.Second))
	none(t, f.gaps(domain.GapUnverifiedRequirement), domain.GapUnverifiedRequirement, "REQ-1")

	time.Sleep(1100 * time.Millisecond) // the revert's journal row must be newer than the evidence
	if err := f.db.MarkChangeSetReverted(f.ctx, cs.ID, ""); err != nil {
		t.Fatal(err)
	}
	if g := one(t, f.gaps(domain.GapUnverifiedRequirement), domain.GapUnverifiedRequirement, "REQ-1"); g.State != domain.GapStateEvidenceStale {
		t.Errorf("after revert = %+v", g)
	}
}

// A failing mapped test makes the requirement evidence_failed even when its
// recorded evidence passed; all-passing mapped tests point an untested
// criterion at --confirm-passing-tests.
func TestGapsMappedTests(t *testing.T) {
	f := newGapFixture(t)
	failing := f.req("REQ-1", domain.PriorityMust, 1)
	f.evidence(failing.ID, 0, domain.VerificationPass, time.Now())
	passing := f.req("REQ-2", domain.PriorityMust, 1)

	ini, _ := f.db.GetInitiative(f.ctx, f.ini.ID)
	gaps, err := BuildGaps(f.ctx, f.db, ini, GapOptions{EvidenceTests: map[string][]domain.EvidenceTest{
		failing.ID: {{Name: "TestLimiter", Status: "failed", ChangeSet: "CHG-1"}},
		passing.ID: {{Name: "TestOK", Status: "passed", ChangeSet: "CHG-2"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if g := one(t, gaps, domain.GapUnverifiedRequirement, "REQ-1"); g.State != domain.GapStateEvidenceFailed || !strings.Contains(g.Why, "TestLimiter") {
		t.Errorf("REQ-1 = %+v", g)
	}
	if g := one(t, gaps, domain.GapUntestedAcceptanceCriterion, "REQ-2/AC-1"); g.Next != "idea verify --confirm-passing-tests" {
		t.Errorf("REQ-2/AC-1 = %+v", g)
	}
}

// ---------- unvalidated_assumption ----------

func TestGapsUnvalidatedAssumptionRankedByDependents(t *testing.T) {
	f := newGapFixture(t)
	mk := func(typ domain.AssertionType, statement string) *domain.Assertion {
		a := &domain.Assertion{InitiativeID: f.ini.ID, Type: typ, Statement: statement, Confidence: domain.ConfidenceLow}
		if err := f.db.CreateAssertion(f.ctx, a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	loadBearing := mk(domain.AssertionAssumption, "customers meter one resource")
	loose := mk(domain.AssertionHypothesis, "usage billing lifts revenue")
	fact := mk(domain.AssertionFact, "Stripe supports metered billing")
	accepted := mk(domain.AssertionAssumption, "accepted one")
	retracted := mk(domain.AssertionAssumption, "retracted and cited")
	retractedLoose := mk(domain.AssertionAssumption, "retracted and uncited")

	// Two requirements derive from the load-bearing assumption; one task
	// implements the first, so three live nodes rest on it.
	for _, code := range []string{"REQ-1", "REQ-2"} {
		r := &domain.Requirement{InitiativeID: f.ini.ID, Code: code, Statement: "s", Priority: domain.PriorityMust,
			Status: domain.RequirementApproved, SourceAssertionID: loadBearing.ID}
		if err := f.db.CreateRequirement(f.ctx, r); err != nil {
			t.Fatal(err)
		}
		if code == "REQ-1" {
			f.task("t", r.ID)
		}
	}
	r3 := &domain.Requirement{InitiativeID: f.ini.ID, Code: "REQ-3", Statement: "s", Priority: domain.PriorityMust,
		Status: domain.RequirementApproved, SourceAssertionID: retracted.ID}
	if err := f.db.CreateRequirement(f.ctx, r3); err != nil {
		t.Fatal(err)
	}
	for id, st := range map[string]domain.AssertionStatus{accepted.ID: domain.AssertionAccepted, retracted.ID: domain.AssertionRetracted, retractedLoose.ID: domain.AssertionRetracted} {
		if err := f.db.SetAssertionStatus(f.ctx, id, st, "human"); err != nil {
			t.Fatal(err)
		}
	}

	gaps := f.gaps(domain.GapUnvalidatedAssumption)
	g := one(t, gaps, domain.GapUnvalidatedAssumption, loadBearing.Code)
	if g.State != domain.GapStateUnvalidated || g.Severity != domain.SeverityMedium || g.Dependents != 3 {
		t.Errorf("load-bearing = %+v; want unvalidated, medium, 3 dependents", g)
	}
	if !strings.HasPrefix(g.Next, "idea assumption "+loadBearing.Code) {
		t.Errorf("next = %q", g.Next)
	}
	g = one(t, gaps, domain.GapUnvalidatedAssumption, loose.Code)
	if g.Severity != domain.SeverityLow || g.Dependents != 0 {
		t.Errorf("loose = %+v", g)
	}
	g = one(t, gaps, domain.GapUnvalidatedAssumption, retracted.Code)
	if g.State != domain.GapStateRefuted || g.Severity != domain.SeverityHigh {
		t.Errorf("retracted+cited = %+v", g)
	}
	none(t, gaps, domain.GapUnvalidatedAssumption, fact.Code)
	none(t, gaps, domain.GapUnvalidatedAssumption, accepted.Code)
	none(t, gaps, domain.GapUnvalidatedAssumption, retractedLoose.Code)

	// Ranked: the refuted one (high) first, then medium by dependents, then low.
	var order []string
	for _, g := range gaps {
		order = append(order, g.Nodes[0].Code)
	}
	want := []string{retracted.Code, loadBearing.Code, loose.Code}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", order, want)
	}

	// Closing it: accept removes the gap.
	if err := f.db.SetAssertionStatus(f.ctx, loadBearing.ID, domain.AssertionAccepted, "human"); err != nil {
		t.Fatal(err)
	}
	none(t, f.gaps(domain.GapUnvalidatedAssumption), domain.GapUnvalidatedAssumption, loadBearing.Code)
}

func TestSetAssertionStatusRefusesSuperseded(t *testing.T) {
	f := newGapFixture(t)
	a := &domain.Assertion{InitiativeID: f.ini.ID, Type: domain.AssertionAssumption, Statement: "s", Confidence: domain.ConfidenceLow}
	if err := f.db.CreateAssertion(f.ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := f.db.SupersedeAssertions(f.ctx, f.ini.ID, "retry"); err != nil {
		t.Fatal(err)
	}
	if err := f.db.SetAssertionStatus(f.ctx, a.ID, domain.AssertionAccepted, "human"); err == nil || !strings.Contains(err.Error(), "superseded") {
		t.Errorf("superseded: err = %v", err)
	}
	var nf *store.NotFoundError
	err := f.db.SetAssertionStatus(f.ctx, "nope", domain.AssertionAccepted, "human")
	if err == nil || !asNotFound(err, &nf) {
		t.Errorf("unknown id: err = %v, want NotFoundError", err)
	}
	if err := f.db.SetAssertionStatus(f.ctx, a.ID, domain.AssertionCandidate, "human"); err == nil {
		t.Error("candidate is not a judgment and must be refused")
	}
}

func asNotFound(err error, target **store.NotFoundError) bool {
	for e := err; e != nil; {
		if nf, ok := e.(*store.NotFoundError); ok {
			*target = nf
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}

// ---------- invalidated_decision ----------

func (f *gapFixture) decision(code string) *domain.Decision {
	f.t.Helper()
	d := &domain.Decision{InitiativeID: f.ini.ID, Code: code, Type: domain.DecisionProduct, Question: code + "?", Options: []string{"a", "b"}}
	if err := f.db.CreateDecision(f.ctx, d); err != nil {
		f.t.Fatal(err)
	}
	return d
}

func TestGapsInvalidatedDecisionDependentsStale(t *testing.T) {
	f := newGapFixture(t)
	d := f.decision("DEC-1")
	r := f.req("REQ-1", domain.PriorityMust, 1)
	if err := f.db.WireRequirementDerivedFromDecision(f.ctx, f.ini.ID, r.ID, d.ID); err != nil {
		t.Fatal(err)
	}
	f.applied(f.task("t", r.ID).ID)

	// Resolved after the requirement, task and change set were built on it:
	// ResolveDecision marks them stale -- the signal `idea impact` reports.
	if err := f.db.ResolveDecision(f.ctx, d.ID, "a", "human"); err != nil {
		t.Fatal(err)
	}
	g := one(t, f.gaps(domain.GapInvalidatedDecision), domain.GapInvalidatedDecision, "DEC-1")
	if g.State != domain.GapStateDependentsStale || g.Severity != domain.SeverityHigh || len(g.Nodes) != 4 {
		t.Errorf("resolved-after = %+v; want dependents_stale naming REQ-1, the task and the change set", g)
	}
	// Work is held, so revise-plan would refuse: point at impact + ack.
	if !strings.HasPrefix(g.Next, "idea impact decision DEC-1") {
		t.Errorf("next = %q", g.Next)
	}

	// Reopened: still stale, and the move is to resolve it.
	if err := f.db.ReopenDecision(f.ctx, d.ID, "human"); err != nil {
		t.Fatal(err)
	}
	g = one(t, f.gaps(domain.GapInvalidatedDecision), domain.GapInvalidatedDecision, "DEC-1")
	if g.Next != "idea resolve DEC-1 <option#>" {
		t.Errorf("reopened next = %q", g.Next)
	}

	// Acknowledged: the marks clear and so does the gap.
	items, _ := f.db.ListStaleItems(f.ctx, f.ini.ID)
	for _, it := range items {
		if err := f.db.ClearStaleItem(f.ctx, f.ini.ID, it.ID, "human"); err != nil {
			t.Fatal(err)
		}
	}
	none(t, f.gaps(domain.GapInvalidatedDecision), domain.GapInvalidatedDecision, "DEC-1")
}

func TestGapsInvalidatedDecisionRequirementRevised(t *testing.T) {
	f := newGapFixture(t)
	d := f.decision("DEC-1")
	revisedLater := f.req("REQ-1", domain.PriorityMust, 1)
	revisedBefore := f.req("REQ-2", domain.PriorityMust, 1)
	for _, r := range []*domain.Requirement{revisedLater, revisedBefore} {
		if err := f.db.WireRequirementDerivedFromDecision(f.ctx, f.ini.ID, r.ID, d.ID); err != nil {
			t.Fatal(err)
		}
	}
	revisedBefore.Statement = "reworded before the decision"
	if err := f.db.UpdateRequirement(f.ctx, revisedBefore); err != nil {
		t.Fatal(err)
	}
	if err := f.db.ResolveDecision(f.ctx, d.ID, "a", "human"); err != nil {
		t.Fatal(err)
	}
	if got := withState(find(f.gaps(domain.GapInvalidatedDecision), domain.GapInvalidatedDecision, "DEC-1"), domain.GapStateRequirementRevised); len(got) != 0 {
		t.Fatalf("a revision before the decision is not a gap: %+v", got)
	}
	// Resolving after the PRD cited it marked both requirements stale: the
	// ordinary case, medium (no built work rests on it yet).
	if got := withState(find(f.gaps(domain.GapInvalidatedDecision), domain.GapInvalidatedDecision, "DEC-1"), domain.GapStateDependentsStale); len(got) != 1 || got[0].Severity != domain.SeverityMedium {
		t.Fatalf("requirement-only stale marks = %+v; want one medium dependents_stale gap", got)
	}

	revisedLater.Statement = "reworded after the decision"
	if err := f.db.UpdateRequirement(f.ctx, revisedLater); err != nil {
		t.Fatal(err)
	}
	revised := withState(find(f.gaps(domain.GapInvalidatedDecision), domain.GapInvalidatedDecision, "DEC-1"), domain.GapStateRequirementRevised)
	if len(revised) != 1 {
		t.Fatalf("revised-after: %+v", revised)
	}
	g := revised[0]
	if len(g.Nodes) != 2 || g.Nodes[1].Code != "REQ-1" || g.Next != "idea reopen-decision DEC-1" {
		t.Errorf("revised-after = %+v", g)
	}
}

func TestGapsInvalidatedDecisionRequirementsRemoved(t *testing.T) {
	f := newGapFixture(t)
	d := f.decision("DEC-1")
	r := f.req("REQ-1", domain.PriorityMust, 1)
	if err := f.db.WireRequirementDerivedFromDecision(f.ctx, f.ini.ID, r.ID, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.ResolveDecision(f.ctx, d.ID, "a", "human"); err != nil {
		t.Fatal(err)
	}
	if got := withState(f.gaps(domain.GapInvalidatedDecision), domain.GapStateRequirementsRemoved); len(got) != 0 {
		t.Fatalf("a decision a live requirement cites is not orphaned: %+v", got)
	}
	// Superseding the requirement also clears its stale mark (store GC), so
	// requirements_removed is the only gap left.
	if err := f.db.SupersedeRequirements(f.ctx, f.ini.ID, "revise"); err != nil {
		t.Fatal(err)
	}
	g := one(t, f.gaps(domain.GapInvalidatedDecision), domain.GapInvalidatedDecision, "DEC-1")
	if g.State != domain.GapStateRequirementsRemoved || g.Severity != domain.SeverityLow {
		t.Errorf("removed = %+v", g)
	}
}

// ---------- orphan_task / orphan_change_set ----------

func TestGapsOrphans(t *testing.T) {
	f := newGapFixture(t)
	r := f.req("REQ-1", domain.PriorityMust, 1)
	linked := f.task("linked", r.ID)
	f.applied(linked.ID)
	orphan := f.task("orphan")
	orphanCS := f.applied(orphan.ID)
	cancelled := f.task("cancelled")
	if err := f.db.SetTaskStatus(f.ctx, cancelled.ID, domain.TaskCancelled); err != nil {
		t.Fatal(err)
	}
	// A change set whose task was deleted: rows only, as a crashed or older
	// daemon could leave behind.
	ghost := f.changeSet("no-such-task")
	// A rejected change set holds no work and is never reported.
	dead := f.changeSet(orphan.ID)
	for _, s := range [][2]domain.ChangeSetStatus{{domain.ChangeSetDraft, domain.ChangeSetChecksRunning}, {domain.ChangeSetChecksRunning, domain.ChangeSetRejected}} {
		if err := f.db.TransitionChangeSet(f.ctx, dead.ID, s[0], s[1], ""); err != nil {
			t.Fatal(err)
		}
	}

	gaps := f.gaps(domain.GapOrphanTask, domain.GapOrphanChangeSet)
	one(t, gaps, domain.GapOrphanTask, orphan.Code)
	none(t, gaps, domain.GapOrphanTask, linked.Code)
	none(t, gaps, domain.GapOrphanTask, cancelled.Code)
	g := one(t, gaps, domain.GapOrphanChangeSet, orphanCS.HumanID)
	if g.State != domain.GapStateTaskOrphaned || g.Severity != domain.SeverityMedium {
		t.Errorf("orphan cs = %+v", g)
	}
	g = one(t, gaps, domain.GapOrphanChangeSet, ghost.HumanID)
	if g.State != domain.GapStateTaskMissing || g.Severity != domain.SeverityLow || g.Next != "idea change "+ghost.HumanID {
		t.Errorf("ghost cs = %+v", g)
	}
	none(t, gaps, domain.GapOrphanChangeSet, dead.HumanID)

	// A task linked only to a removed requirement is an orphan too.
	if err := f.db.SupersedeRequirements(f.ctx, f.ini.ID, "revise"); err != nil {
		t.Fatal(err)
	}
	one(t, f.gaps(domain.GapOrphanTask), domain.GapOrphanTask, linked.Code)
}

// ---------- stale_artifact ----------

func TestGapsStaleArtifact(t *testing.T) {
	f := newGapFixture(t)
	t0 := time.Now().Add(-time.Hour)
	f.artifact(domain.ArtifactPRD, t0)
	f.artifact(domain.ArtifactTechnicalDesign, t0.Add(time.Minute))
	f.artifact(domain.ArtifactExecutionPlan, t0.Add(2*time.Minute))
	none(t, f.gaps(domain.GapStaleArtifact), domain.GapStaleArtifact, "execution_plan")

	// The technical design is regenerated after the plan was made from it.
	f.artifact(domain.ArtifactTechnicalDesign, t0.Add(3*time.Minute))
	gaps := f.gaps(domain.GapStaleArtifact)
	g := one(t, gaps, domain.GapStaleArtifact, "execution_plan")
	if g.Next != "idea revise-plan" || !strings.Contains(g.Why, "technical_design") {
		t.Errorf("stale plan = %+v", g)
	}
	none(t, gaps, domain.GapStaleArtifact, "technical_design")
}

// ---------- the report as a whole ----------

func TestGapsKindFilterAndOrder(t *testing.T) {
	f := newGapFixture(t)
	f.artifact(domain.ArtifactExecutionPlan, time.Now())
	f.req("REQ-10", domain.PriorityCould, 1)
	f.req("REQ-2", domain.PriorityMust, 1)
	f.task("orphan")

	only := f.gaps(domain.GapOrphanTask)
	if len(only) != 1 || only[0].Kind != domain.GapOrphanTask {
		t.Fatalf("--kind orphan_task = %+v", only)
	}

	all := f.gaps()
	for i := 1; i < len(all); i++ {
		if domain.SeverityRank(all[i-1].Severity) > domain.SeverityRank(all[i].Severity) {
			t.Fatalf("not most severe first at %d: %+v", i, all)
		}
	}
	if all[0].Nodes[0].Code != "REQ-2" {
		t.Errorf("first gap = %+v; want the must requirement", all[0])
	}
	for _, g := range all {
		if g.InitiativeID != f.ini.ID || g.Why == "" || g.Next == "" || !g.Kind.Valid() || !g.State.Valid() || !g.Severity.Valid() {
			t.Errorf("incomplete gap: %+v", g)
		}
	}
}

func TestParseGapKinds(t *testing.T) {
	got, err := ParseGapKinds(" orphan_task, stale_artifact ,")
	if err != nil || len(got) != 2 || got[0] != domain.GapOrphanTask || got[1] != domain.GapStaleArtifact {
		t.Errorf("parse = %v, %v", got, err)
	}
	if got, err := ParseGapKinds(""); err != nil || len(got) != 0 {
		t.Errorf("empty = %v, %v", got, err)
	}
	if _, err := ParseGapKinds("orphan"); err == nil || !strings.Contains(err.Error(), "unimplemented_requirement") {
		t.Errorf("unknown kind must list the known ones: %v", err)
	}
}

// The wire shape: every field present, nodes an array, never null.
func TestGapJSONShape(t *testing.T) {
	f := newGapFixture(t)
	f.req("REQ-1", domain.PriorityMust, 1)
	gaps := f.gaps()
	report := domain.GapReport{InitiativeID: f.ini.ID, Gaps: gaps, Summary: domain.Summarize(gaps)}
	b, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	g := m["gaps"].([]any)[0].(map[string]any)
	for _, k := range []string{"kind", "state", "severity", "nodes", "why", "next", "dependents", "initiative_id"} {
		if _, ok := g[k]; !ok {
			t.Errorf("gap lacks %q: %s", k, b)
		}
	}
	n := g["nodes"].([]any)[0].(map[string]any)
	for _, k := range []string{"type", "id", "code"} {
		if _, ok := n[k]; !ok {
			t.Errorf("node lacks %q: %s", k, b)
		}
	}
	sum := m["summary"].(map[string]any)
	if sum["by_kind"].(map[string]any)["stale_artifact"] != float64(0) {
		t.Errorf("summary must list every kind, zero included: %s", b)
	}

	empty, _ := json.Marshal(domain.GapReport{Gaps: []domain.Gap{}, Summary: domain.Summarize(nil)})
	if !strings.Contains(string(empty), `"gaps":[]`) {
		t.Errorf("empty report = %s", empty)
	}
}

// The rule over hand-inserted edges: nothing in production records an
// assertion supporting a decision, or an `invalidates` edge between two
// decisions. (Retraction's assertion --invalidates--> edge is covered on a
// real flow by workflow.TestRetractionWritesInvalidatesEdges.)
func TestGapsInvalidatedDecisionBasisRefuted(t *testing.T) {
	f := newGapFixture(t)
	d := f.decision("DEC-1")
	if err := f.db.ResolveDecision(f.ctx, d.ID, "a", "human"); err != nil {
		t.Fatal(err)
	}
	a := &domain.Assertion{InitiativeID: f.ini.ID, Type: domain.AssertionAssumption, Statement: "s", Confidence: domain.ConfidenceLow}
	if err := f.db.CreateAssertion(f.ctx, a); err != nil {
		t.Fatal(err)
	}
	edge := func(fromType, fromID string, et domain.EdgeType, toType, toID string) {
		t.Helper()
		if _, err := f.db.SQL().Exec(`INSERT INTO graph_edges (id, initiative_id, from_type, from_id, edge_type, to_type, to_id, metadata_json, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, '{}', 0)`, fromID+string(et), f.ini.ID, fromType, fromID, string(et), toType, toID); err != nil {
			t.Fatal(err)
		}
	}
	edge("assertion", a.ID, domain.EdgeSupports, "decision", d.ID)
	if got := withState(f.gaps(domain.GapInvalidatedDecision), domain.GapStateBasisRefuted); len(got) != 0 {
		t.Fatalf("a supporting candidate assumption is not a refuted basis: %+v", got)
	}
	if err := f.db.SetAssertionStatus(f.ctx, a.ID, domain.AssertionRetracted, "human"); err != nil {
		t.Fatal(err)
	}
	got := withState(f.gaps(domain.GapInvalidatedDecision), domain.GapStateBasisRefuted)
	if len(got) != 1 || got[0].Severity != domain.SeverityHigh || got[0].Next != "idea reopen-decision DEC-1" || !strings.Contains(got[0].Why, "retracted") {
		t.Fatalf("retracted support = %+v", got)
	}

	// An `invalidates` edge from another node is a refuted basis too.
	d2 := f.decision("DEC-2")
	if err := f.db.ResolveDecision(f.ctx, d2.ID, "b", "human"); err != nil {
		t.Fatal(err)
	}
	edge("decision", d.ID, domain.EdgeInvalidates, "decision", d2.ID)
	if g := withState(find(f.gaps(domain.GapInvalidatedDecision), domain.GapInvalidatedDecision, "DEC-2"), domain.GapStateBasisRefuted); len(g) != 1 {
		t.Fatalf("invalidated decision = %+v", g)
	}
}
