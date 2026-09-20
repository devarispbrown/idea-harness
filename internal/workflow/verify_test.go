package workflow

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
	"github.com/devarispbrown/idea-harness/internal/store"
)

func newVerifyFixture(t *testing.T) (*Engine, *store.DB, string) {
	t.Helper()
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	return eng, db, ini.ID
}

func TestVerifyInitiativeGate(t *testing.T) {
	eng, db, iniID := newVerifyFixture(t)
	ctx := context.Background()

	req := &domain.Requirement{
		InitiativeID: iniID,
		Statement:    "Be fast",
		Priority:     domain.PriorityMust,
		Status:       domain.RequirementApproved,
		AcceptanceCriteria: []domain.AcceptanceCriterion{
			{Metric: "latency", Target: "< 200ms", Condition: "reference workload"},
		},
	}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}

	// No evidence yet: the gate must not complete the initiative.
	if err := eng.VerifyInitiative(ctx, iniID); err != nil {
		t.Fatal(err)
	}
	ini, err := db.GetInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.Status == domain.InitiativeComplete {
		t.Fatal("initiative must not be complete without evidence")
	}

	// Human pass clears the criterion, then the gate verifies + completes.
	if err := eng.RecordHumanPass(ctx, iniID, req.Code, "manual check", "tester"); err != nil {
		t.Fatal(err)
	}
	if err := eng.VerifyInitiative(ctx, iniID); err != nil {
		t.Fatal(err)
	}

	reqs, err := db.ListRequirements(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if reqs[0].Status != domain.RequirementVerified {
		t.Fatalf("requirement status = %q, want verified", reqs[0].Status)
	}
	ini, err = db.GetInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.Status != domain.InitiativeComplete {
		t.Fatalf("initiative status = %q, want complete", ini.Status)
	}
}

// TestHasOpenBlockingFindingsSkipsAdvisory is review C1: an Advisory finding
// must never block completion, however severe and however undisposed --
// that's the whole point of marking a quick-accepted finding Advisory rather
// than leaving it a plain open blocker. Before the fix, a quick-accepted
// critical/high finding wedged the initiative: approval succeeded, and
// verify/complete then refused forever with no way to un-stick it.
func TestHasOpenBlockingFindingsSkipsAdvisory(t *testing.T) {
	eng, db, iniID := newVerifyFixture(t)
	ctx := context.Background()

	if err := db.CreateFinding(ctx, &domain.Finding{
		InitiativeID: iniID, TargetType: "prd", TargetID: iniID,
		Severity: domain.SeverityCritical, Confidence: domain.ConfidenceHigh,
		Category: "c", Finding: "quick-accepted", Recommendation: "r",
		Advisory: true,
	}); err != nil {
		t.Fatal(err)
	}

	blocking, err := eng.hasOpenBlockingFindings(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if blocking {
		t.Fatal("an Advisory finding must never be blocking, regardless of severity or disposition")
	}

	// A non-advisory finding of the same severity still blocks, proving the
	// skip is keyed on Advisory and not accidentally on something else.
	if err := db.CreateFinding(ctx, &domain.Finding{
		InitiativeID: iniID, TargetType: "prd", TargetID: iniID,
		Severity: domain.SeverityCritical, Confidence: domain.ConfidenceHigh,
		Category: "c", Finding: "real blocker", Recommendation: "r",
	}); err != nil {
		t.Fatal(err)
	}
	blocking2, err := eng.hasOpenBlockingFindings(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if !blocking2 {
		t.Fatal("a non-advisory undisposed critical finding must still block")
	}
}

func TestPartialCoverageNotVerified(t *testing.T) {
	eng, db, iniID := newVerifyFixture(t)
	ctx := context.Background()

	req := &domain.Requirement{
		InitiativeID: iniID,
		Statement:    "Be fast and cheap",
		Priority:     domain.PriorityMust,
		Status:       domain.RequirementApproved,
		AcceptanceCriteria: []domain.AcceptanceCriterion{
			{Metric: "latency", Target: "< 200ms", Condition: "reference workload"},
			{Metric: "cost", Target: "< $1", Condition: "per 1k requests"},
		},
	}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}

	// Pass only the first criterion directly (not via RecordHumanPass, which
	// passes all).
	if err := db.CreateVerificationEvidence(ctx, &domain.VerificationEvidence{
		InitiativeID:  iniID,
		RequirementID: req.ID,
		CriterionKey:  domain.CriterionKey(req.ID, 0),
		Kind:          domain.VerificationAcceptance,
		Status:        domain.VerificationPass,
		VerifiedBy:    "human:tester",
	}); err != nil {
		t.Fatal(err)
	}

	if err := eng.VerifyInitiative(ctx, iniID); err != nil {
		t.Fatal(err)
	}
	reqs, err := db.ListRequirements(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if reqs[0].Status != domain.RequirementApproved {
		t.Fatalf("requirement status = %q, want still approved (partial coverage)", reqs[0].Status)
	}
}

func TestRecordAutomatedEvidenceOnApply(t *testing.T) {
	eng, db, iniID := newVerifyFixture(t)
	ctx := context.Background()

	req := &domain.Requirement{
		InitiativeID: iniID, Statement: "Be fast", Priority: domain.PriorityMust, Status: domain.RequirementApproved,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "latency", Target: "<200ms", Condition: "x"}},
	}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: iniID, Title: "Implement", Statement: "build", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, req.ID, nil); err != nil {
		t.Fatal(err)
	}
	cs := &domain.ChangeSet{InitiativeID: iniID, TaskID: task.ID}
	if err := db.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}

	if err := eng.recordAutomatedEvidence(ctx, cs); err != nil {
		t.Fatal(err)
	}
	evs, err := db.ListEvidenceByRequirement(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("expected 1 evidence, got %d", len(evs))
	}
	if evs[0].Status != domain.VerificationNeedsManual {
		t.Fatalf("status = %q, want needs_manual", evs[0].Status)
	}
	if evs[0].VerifiedBy != "verification_engineer" {
		t.Fatalf("verified_by = %q, want verification_engineer", evs[0].VerifiedBy)
	}
}

func TestCompletionSkipsRemovedRequirements(t *testing.T) {
	eng, db, iniID := newVerifyFixture(t)
	ctx := context.Background()

	req1 := &domain.Requirement{
		InitiativeID: iniID, Statement: "Be fast", Priority: domain.PriorityMust, Status: domain.RequirementApproved,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "latency", Target: "<200ms", Condition: "x"}},
	}
	if err := db.CreateRequirement(ctx, req1); err != nil {
		t.Fatal(err)
	}
	// A superseded requirement can never reach verified; the gate must skip it.
	req2 := &domain.Requirement{
		InitiativeID: iniID, Statement: "Old", Priority: domain.PriorityMust, Status: domain.RequirementRemoved,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "m", Target: "t", Condition: "c"}},
	}
	if err := db.CreateRequirement(ctx, req2); err != nil {
		t.Fatal(err)
	}

	if err := eng.RecordHumanPass(ctx, iniID, req1.Code, "", "tester"); err != nil {
		t.Fatal(err)
	}
	if err := eng.VerifyInitiative(ctx, iniID); err != nil {
		t.Fatal(err)
	}
	ini, err := db.GetInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.Status != domain.InitiativeComplete {
		t.Fatalf("expected complete (removed skipped), got %q", ini.Status)
	}
}

func TestFailRequirementBlocksThenPassRecovers(t *testing.T) {
	eng, db, iniID := newVerifyFixture(t)
	ctx := context.Background()

	req := &domain.Requirement{
		InitiativeID: iniID, Statement: "Be fast", Priority: domain.PriorityMust, Status: domain.RequirementApproved,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "latency", Target: "<200ms", Condition: "x"}},
	}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}

	if err := eng.RecordHumanEvidence(ctx, iniID, req.Code, domain.VerificationFail, "measured 500ms", "tester"); err != nil {
		t.Fatal(err)
	}
	reqs, err := db.ListRequirements(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if reqs[0].Status != domain.RequirementFailed {
		t.Fatalf("status = %q, want failed", reqs[0].Status)
	}
	if err := eng.VerifyInitiative(ctx, iniID); err != nil {
		t.Fatal(err)
	}
	ini, err := db.GetInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.Status == domain.InitiativeComplete {
		t.Fatal("initiative must not complete with a failed requirement")
	}

	// A human re-pass clears the fail and completes the initiative.
	if err := eng.RecordHumanEvidence(ctx, iniID, req.Code, domain.VerificationPass, "re-measured 150ms", "tester"); err != nil {
		t.Fatal(err)
	}
	if err := eng.VerifyInitiative(ctx, iniID); err != nil {
		t.Fatal(err)
	}
	reqs, err = db.ListRequirements(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if reqs[0].Status != domain.RequirementVerified {
		t.Fatalf("status = %q, want verified", reqs[0].Status)
	}
	ini, err = db.GetInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.Status != domain.InitiativeComplete {
		t.Fatalf("status = %q, want complete", ini.Status)
	}
}

func TestWaiveRequirementExcludedFromGate(t *testing.T) {
	eng, db, iniID := newVerifyFixture(t)
	ctx := context.Background()

	req1 := &domain.Requirement{
		InitiativeID: iniID, Statement: "Be fast", Priority: domain.PriorityMust, Status: domain.RequirementApproved,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "latency", Target: "<200ms", Condition: "x"}},
	}
	if err := db.CreateRequirement(ctx, req1); err != nil {
		t.Fatal(err)
	}
	req2 := &domain.Requirement{
		InitiativeID: iniID, Statement: "Be shiny (out of scope)", Priority: domain.PriorityCould, Status: domain.RequirementApproved,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "m", Target: "t", Condition: "c"}},
	}
	if err := db.CreateRequirement(ctx, req2); err != nil {
		t.Fatal(err)
	}

	if err := eng.RecordHumanEvidence(ctx, iniID, req2.Code, domain.VerificationWaived, "deferred", "tester"); err != nil {
		t.Fatal(err)
	}
	if err := eng.RecordHumanEvidence(ctx, iniID, req1.Code, domain.VerificationPass, "", "tester"); err != nil {
		t.Fatal(err)
	}
	if err := eng.VerifyInitiative(ctx, iniID); err != nil {
		t.Fatal(err)
	}
	ini, err := db.GetInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.Status != domain.InitiativeComplete {
		t.Fatalf("expected complete (waived skipped), got %q", ini.Status)
	}
	reqs, err := db.ListRequirements(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range reqs {
		if r.ID == req2.ID && r.Status != domain.RequirementWaived {
			t.Fatalf("waived requirement status = %q, want waived", r.Status)
		}
	}
}

func TestFailOnVerifiedRequirementSticksAndCanBeWaived(t *testing.T) {
	eng, db, iniID := newVerifyFixture(t)
	ctx := context.Background()

	req := &domain.Requirement{
		InitiativeID: iniID, Statement: "Be fast", Priority: domain.PriorityMust, Status: domain.RequirementApproved,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "latency", Target: "<200ms", Condition: "x"}},
	}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}

	// Verify it first.
	if err := eng.RecordHumanEvidence(ctx, iniID, req.Code, domain.VerificationPass, "", "tester"); err != nil {
		t.Fatal(err)
	}
	if err := eng.VerifyInitiative(ctx, iniID); err != nil {
		t.Fatal(err)
	}

	// A human then fails the verified requirement. Stale pass evidence must not
	// resurrect it to verified on the next gate run.
	if err := eng.RecordHumanEvidence(ctx, iniID, req.Code, domain.VerificationFail, "regressed", "tester"); err != nil {
		t.Fatal(err)
	}
	if err := eng.VerifyInitiative(ctx, iniID); err != nil {
		t.Fatal(err)
	}
	reqs, err := db.ListRequirements(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if reqs[0].Status != domain.RequirementFailed {
		t.Fatalf("status = %q, want failed (must not resurrect to verified)", reqs[0].Status)
	}
	ini, err := db.GetInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.Status == domain.InitiativeComplete {
		t.Fatal("initiative must not be complete while a requirement is failed")
	}

	// Failed → waived is a supported transition and excludes it from the gate.
	if err := eng.RecordHumanEvidence(ctx, iniID, req.Code, domain.VerificationWaived, "defer", "tester"); err != nil {
		t.Fatal(err)
	}
	if err := eng.VerifyInitiative(ctx, iniID); err != nil {
		t.Fatal(err)
	}
	reqs, err = db.ListRequirements(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if reqs[0].Status != domain.RequirementWaived {
		t.Fatalf("status = %q, want waived", reqs[0].Status)
	}
	ini, err = db.GetInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.Status != domain.InitiativeComplete {
		t.Fatalf("status = %q, want complete (waived skipped)", ini.Status)
	}
}

// TestUnverifyThenBareVerifyDoesNotReverify is review H1, the exact sequence
// named in the review: human pass -> verify -> revert -> verify. Before the
// fix, UnverifyRequirement flipped status back to approved but left the pass
// evidence rows in place; PassedCriterionKeys is status-blind, so a bare
// `idea verify` with nothing new on disk re-marked the requirement (and the
// initiative) verified/complete from stale evidence.
//
// Mutation-check: remove the verification_evidence DELETE from
// UnverifyRequirement's transaction, and this fails at the final assertion.
func TestUnverifyThenBareVerifyDoesNotReverify(t *testing.T) {
	eng, db, iniID := newVerifyFixture(t)
	ctx := context.Background()

	req := &domain.Requirement{
		InitiativeID: iniID, Statement: "Retain audit logs for 90 days", Priority: domain.PriorityMust,
		Status: domain.RequirementApproved,
		AcceptanceCriteria: []domain.AcceptanceCriterion{
			{Metric: "retention", Target: "90d", Condition: "audit log store"},
		},
	}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}

	if err := eng.RecordHumanPass(ctx, iniID, req.Code, "looks good", "tester"); err != nil {
		t.Fatal(err)
	}
	if err := eng.VerifyInitiative(ctx, iniID); err != nil {
		t.Fatal(err)
	}
	reqs, err := db.ListRequirements(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if reqs[0].Status != domain.RequirementVerified {
		t.Fatalf("requirement status = %q, want verified", reqs[0].Status)
	}

	// Revert: the change set that verified it is reverted.
	if err := db.UnverifyRequirement(ctx, req.ID, "tester"); err != nil {
		t.Fatal(err)
	}
	reqs, err = db.ListRequirements(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if reqs[0].Status != domain.RequirementApproved {
		t.Fatalf("requirement status after revert = %q, want approved", reqs[0].Status)
	}
	passed, err := db.PassedCriterionKeys(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(passed) != 0 {
		t.Fatalf("pass evidence survived UnverifyRequirement: %v", passed)
	}

	// Bare re-verify: nothing new happened on disk.
	if err := eng.VerifyInitiative(ctx, iniID); err != nil {
		t.Fatal(err)
	}
	reqs, err = db.ListRequirements(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if reqs[0].Status == domain.RequirementVerified {
		t.Fatal("a bare re-verify (no new evidence) re-marked the requirement verified after a revert")
	}
	ini, err := db.GetInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.Status == domain.InitiativeComplete {
		t.Fatal("initiative reported complete on stale evidence after a revert")
	}
}
