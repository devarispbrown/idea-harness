package daemon

import (
	"errors"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/workflow"
)

// TestPlanCommandsRefuseOnceAChangeSetExists is change-set parity across the
// three plan-level commands, and it is table-driven for the reason the table
// exists: `ApprovePlanInitiative`'s change-set guard was introduced with the
// comment "Matching RevisePlan and ReopenPhase", and that was **false** of the
// Runtime sibling. RevisePlanInitiative had no such guard; the refusal lived
// only in Engine.RevisePlan, which runs inside the launched operation and
// returns InitiativeFailed -- so a healthy plan_approved initiative with one
// unreviewed change set became `failed` because of a refusal the API answers
// synchronously one function away.
//
// A per-command test would have let the next plan-level command diverge the
// same way. A fourth joins this table instead.
func TestPlanCommandsRefuseOnceAChangeSetExists(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*Runtime, string, string) (string, error)
	}{
		{"approve-plan", func(rt *Runtime, p, i string) (string, error) {
			return rt.ApprovePlanInitiative(t.Context(), p, i)
		}},
		{"revise-plan", func(rt *Runtime, p, i string) (string, error) {
			return rt.RevisePlanInitiative(t.Context(), p, i)
		}},
		{"reopen-phase", func(rt *Runtime, p, i string) (string, error) {
			return rt.ReopenPhaseInitiative(t.Context(), p, i, "architecture")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, ts, token := newTestServer(t)
			projectID, iniID, _ := appliedChangeSet(t, rt, ts, token)
			pr, err := rt.EnsureProject(t.Context(), projectID)
			if err != nil {
				t.Fatal(err)
			}
			// reopen-phase checks for a design artifact BEFORE it checks change
			// sets, so without this the subtest passed on "no design or plan to
			// reopen" and stayed green when the change-set guard was deleted --
			// a vacuous row in the table whose whole purpose is catching a
			// command that diverges.
			if err := pr.db.CreateArtifact(t.Context(),
				&domain.Artifact{InitiativeID: iniID, Kind: domain.ArtifactExecutionPlan},
				&domain.ArtifactVersion{BodyDigest: "d", Status: "draft"}); err != nil {
				t.Fatal(err)
			}
			if err := pr.db.NeedsHumanJudgment(t.Context(), iniID, domain.JudgmentPlanReview, "test"); err != nil {
				t.Fatal(err)
			}
			before, err := pr.db.GetInitiative(t.Context(), iniID)
			if err != nil {
				t.Fatal(err)
			}

			_, err = tc.call(rt, projectID, iniID)
			if err == nil {
				t.Fatalf("%s accepted an initiative that already has change sets", tc.name)
			}
			if !isPrecondition(err) {
				t.Errorf("%s refused with an error that does not carry the precondition type (%v); "+
					"the classification has to travel with the error, or the 409 depends on "+
					"whichever status the handler happened to pass", tc.name, err)
			}
			// The REASON, not just the refusal. Two of these commands have other
			// guards that fire earlier, so a row asserting only `err != nil`
			// passes while the change-set guard it exists for is gone.
			if !strings.Contains(err.Error(), "change set") &&
				!strings.Contains(err.Error(), "execution has begun") {
				t.Errorf("%s refused for a reason other than change sets: %v", tc.name, err)
			}

			after, err := pr.db.GetInitiative(t.Context(), iniID)
			if err != nil {
				t.Fatal(err)
			}
			if after.Status != before.Status {
				t.Fatalf("%s moved the status from %q to %q. A precondition must decline the "+
					"request; failing the initiative is the defect, not the fix",
					tc.name, before.Status, after.Status)
			}
			if after.Status == domain.InitiativeFailed {
				t.Fatalf("%s failed the initiative: %q", tc.name, after.FailureReason)
			}
		})
	}
}

// The negative rows. Sibling parity between approve-plan and revise-plan is
// FALSE by design in these three states -- revise-plan is the escape
// approve-plan's refusal names in two of them -- so a general parity test would
// either fail on day one or be written loosely enough to assert nothing. These
// pin that the change-set guard was not over-widened into one.
func TestRevisePlanIsStillAcceptedWithoutChangeSets(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setUp func(*testing.T, *Runtime, string, string)
	}{
		{"an already-approved plan", func(t *testing.T, rt *Runtime, p, i string) {
			setStatus(t, rt, p, i, domain.InitiativePlanApproved)
		}},
		{"a plan parked on unresolved dependencies", func(t *testing.T, rt *Runtime, p, i string) {
			pr, _ := rt.EnsureProject(t.Context(), p)
			if err := pr.db.NeedsHumanJudgment(t.Context(), i, domain.JudgmentPlanDependencies, "test"); err != nil {
				t.Fatal(err)
			}
		}},
		{"a plan carrying a critical structural finding", func(t *testing.T, rt *Runtime, p, i string) {
			pr, _ := rt.EnsureProject(t.Context(), p)
			if err := pr.db.CreateFinding(t.Context(), &domain.Finding{
				InitiativeID: i, TargetType: "initiative", TargetID: i,
				ReviewRunID: workflow.DeterministicPlanCheckRun,
				Severity:    domain.SeverityCritical, Confidence: domain.ConfidenceHigh,
				Category: "plan", Finding: "task dependency cycle detected: TASK-1",
				Recommendation: "break the cycle",
			}); err != nil {
				t.Fatal(err)
			}
			if err := pr.db.NeedsHumanJudgment(t.Context(), i, domain.JudgmentPlanReview, "test"); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, ts, token := newTestServer(t)
			projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
			tc.setUp(t, rt, projectID, iniID)

			opID, err := rt.RevisePlanInitiative(t.Context(), projectID, iniID)
			if err != nil {
				t.Fatalf("revise-plan was refused for %s, with no change sets present: %v\n"+
					"It is the escape `idea next` names from two of these states; refusing it "+
					"here would recreate the live-lock the change-set guard sits inside a fix for.",
					tc.name, err)
			}
			waitForOperation(t, ts, token, opID)
		})
	}
}

// isPrecondition reports whether the error carries the type that makes a
// refusal a 409 regardless of the status the handler passes.
func isPrecondition(err error) bool {
	var pre *PreconditionError
	return errors.As(err, &pre)
}

// TestPlanCommandsIgnoreTerminalChangeSets is the other half of the change-set
// predicate. `rejected` and `superseded` are absorbing states minted on ordinary
// paths -- every credential-sweep reject leaves one behind and reopens the task
// -- so an initiative carrying only those has no work in flight and is
// legitimately revisable.
//
// Counting them made all three commands refuse forever, and the refusal named
// `idea review` and `idea apply`, both of which are closed for a rejected change
// set. That is the predicate audit 5's A2 was rejected for proposing, one
// command over.
func TestPlanCommandsIgnoreTerminalChangeSets(t *testing.T) {
	for _, st := range []domain.ChangeSetStatus{domain.ChangeSetRejected, domain.ChangeSetSuperseded} {
		t.Run(string(st), func(t *testing.T) {
			rt, ts, token := newTestServer(t)
			projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
			pr, err := rt.EnsureProject(t.Context(), projectID)
			if err != nil {
				t.Fatal(err)
			}
			// Through the state machine, not straight into the terminal state:
			// CreateChangeSet forces `draft` on purpose, so a caller can never
			// persist a change set that skipped checks and review.
			cs := &domain.ChangeSet{InitiativeID: iniID, PatchDigest: "p"}
			if err := pr.db.CreateChangeSet(t.Context(), cs); err != nil {
				t.Fatal(err)
			}
			for _, step := range walkTo[st] {
				if err := pr.db.TransitionChangeSet(t.Context(), cs.ID, step.from, step.to, "test"); err != nil {
					t.Fatalf("setup: %s -> %s: %v", step.from, step.to, err)
				}
			}
			setStatus(t, rt, projectID, iniID, domain.InitiativePlanApproved)

			opID, err := rt.RevisePlanInitiative(t.Context(), projectID, iniID)
			if err != nil {
				t.Fatalf("revise-plan was refused over a %s change set, which holds no work: %v\n"+
					"The refusal names `idea review` and `idea apply`, both closed for it.", st, err)
			}
			waitForOperation(t, ts, token, opID)
		})
	}
}

// walkTo is the shortest legal route to each terminal state, per
// legalChangeSetTransition. `rejected` is what the credential sweep mints;
// `superseded` is what a re-execution leaves behind.
var walkTo = map[domain.ChangeSetStatus][]struct{ from, to domain.ChangeSetStatus }{
	domain.ChangeSetRejected: {
		{domain.ChangeSetDraft, domain.ChangeSetChecksRunning},
		{domain.ChangeSetChecksRunning, domain.ChangeSetRejected},
	},
	domain.ChangeSetSuperseded: {
		{domain.ChangeSetDraft, domain.ChangeSetChecksRunning},
		{domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired},
		{domain.ChangeSetReviewRequired, domain.ChangeSetChangesRequested},
		{domain.ChangeSetChangesRequested, domain.ChangeSetSuperseded},
	},
}
