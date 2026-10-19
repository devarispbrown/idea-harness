package daemon

import (
	"errors"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestApproveRefusesUnresolvedClarification pins the [NEEDS CLARIFICATION]
// gate: approving a PRD that still has an open flagged question is a 409
// precondition, not a success.
func TestApproveRefusesUnresolvedClarification(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}

	if err := pr.db.NeedsHumanJudgment(t.Context(), iniID, domain.JudgmentPRDReview, "test"); err != nil {
		t.Fatal(err)
	}
	if err := pr.db.CreateQuestion(t.Context(), &domain.Question{
		InitiativeID: iniID, Question: "How is billing structured?", NeedsClarification: true,
	}); err != nil {
		t.Fatal(err)
	}

	_, err = rt.ApproveInitiative(t.Context(), projectID, iniID)
	if err == nil {
		t.Fatal("approve accepted a PRD with an unresolved [NEEDS CLARIFICATION] question")
	}
	var pe *PreconditionError
	if !errors.As(err, &pe) {
		t.Fatalf("expected a PreconditionError, got %T: %v", err, err)
	}
}

// TestApproveIgnoresPlainOpenQuestions is the no-live-lock guard: an ordinary
// open question (not flagged) must NOT trip the clarification gate, otherwise
// approve would refuse every initiative with any open question.
func TestApproveIgnoresPlainOpenQuestions(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}

	if err := pr.db.NeedsHumanJudgment(t.Context(), iniID, domain.JudgmentPRDReview, "test"); err != nil {
		t.Fatal(err)
	}
	if err := pr.db.CreateQuestion(t.Context(), &domain.Question{
		InitiativeID: iniID, Question: "What should we call it?", NeedsClarification: false,
	}); err != nil {
		t.Fatal(err)
	}

	_, err = rt.ApproveInitiative(t.Context(), projectID, iniID)
	var pe *PreconditionError
	if errors.As(err, &pe) {
		t.Fatalf("a plain open question tripped the clarification gate: %v", err)
	}
}
