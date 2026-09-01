package workflow

import (
	"context"
	"fmt"

	"github.com/idea-harness/idea-harness/internal/domain"
	"github.com/idea-harness/idea-harness/internal/store"
)

// RecordHumanPass records a human "pass" evidence for every acceptance criterion
// of a requirement (§70, review B10). This is the human sign-off path: UX/security
// criteria that the deterministic verifier marks needs_manual are cleared here by
// a human actor, never by a model.
func (e *Engine) RecordHumanPass(ctx context.Context, initiativeID, requirementRef, note, actor string) error {
	if actor == "" {
		actor = store.HumanActor
	}
	r, err := e.db.GetRequirementByRef(ctx, initiativeID, requirementRef)
	if err != nil {
		return err
	}
	if len(r.AcceptanceCriteria) == 0 {
		return fmt.Errorf("workflow: requirement %q has no acceptance criteria to verify", requirementRef)
	}
	for i := range r.AcceptanceCriteria {
		ev := &domain.VerificationEvidence{
			InitiativeID:  initiativeID,
			RequirementID: r.ID,
			CriterionKey:  domain.CriterionKey(r.ID, i),
			Kind:          domain.VerificationAcceptance,
			Status:        domain.VerificationPass,
			VerifiedBy:    "human:" + actor,
			Note:          note,
		}
		if err := e.db.CreateVerificationEvidence(ctx, ev); err != nil {
			return err
		}
	}
	return nil
}

// VerifyInitiative computes coverage across all approved requirements, marks a
// requirement verified when every mandatory criterion has a pass evidence, and
// completes the initiative when every requirement is verified and there are no
// open blocking findings (§70, review B4). It is idempotent and deterministic.
func (e *Engine) VerifyInitiative(ctx context.Context, initiativeID string) error {
	reqs, err := e.db.ListRequirements(ctx, initiativeID)
	if err != nil {
		return err
	}

	for _, r := range reqs {
		if r.Status != domain.RequirementApproved {
			continue
		}
		covered, err := e.requirementCovered(ctx, r)
		if err != nil {
			return err
		}
		if !covered {
			continue
		}
		if err := e.db.MarkRequirementVerified(ctx, r.ID, store.ActorOrchestrator); err != nil {
			return err
		}
	}

	// Every active requirement must now be verified before the initiative can
	// reach a terminal "complete" state.
	reqs, err = e.db.ListRequirements(ctx, initiativeID)
	if err != nil {
		return err
	}
	for _, r := range reqs {
		if r.Status != domain.RequirementVerified {
			return nil // not done yet; no error
		}
	}

	blocking, err := e.hasOpenBlockingFindings(ctx, initiativeID)
	if err != nil {
		return err
	}
	if blocking {
		return fmt.Errorf("workflow: initiative has open blocking findings; cannot complete")
	}
	return e.db.SetInitiativeStatus(ctx, initiativeID, domain.InitiativeComplete, store.ActorOrchestrator)
}

// requirementCovered reports whether every mandatory acceptance criterion of r
// has at least one pass evidence.
func (e *Engine) requirementCovered(ctx context.Context, r domain.Requirement) (bool, error) {
	total := len(r.AcceptanceCriteria)
	if total == 0 {
		return false, nil
	}
	passed, err := e.db.PassedCriterionKeys(ctx, r.ID)
	if err != nil {
		return false, err
	}
	count := 0
	for i := range r.AcceptanceCriteria {
		if passed[domain.CriterionKey(r.ID, i)] {
			count++
		}
	}
	return count == total, nil
}

// hasOpenBlockingFindings reports whether the initiative has any undisposed
// critical/high finding.
func (e *Engine) hasOpenBlockingFindings(ctx context.Context, initiativeID string) (bool, error) {
	findings, err := e.db.ListFindingsByInitiative(ctx, initiativeID)
	if err != nil {
		return false, err
	}
	for _, f := range findings {
		if f.Disposition == "" && f.Severity.Blocking() {
			return true, nil
		}
	}
	return false, nil
}
