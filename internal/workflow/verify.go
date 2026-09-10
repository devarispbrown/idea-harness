package workflow

import (
	"context"
	"fmt"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// recordAutomatedEvidence records the verification_engineer's deterministic
// output when a change set is applied: the checks passed (or it would have been
// rejected), but the acceptance criterion itself needs a human or a real
// measurement to verify (§70). This makes the pending state explicit, traceable,
// and surfaced by the verify command instead of silently absent.
func (e *Engine) recordAutomatedEvidence(ctx context.Context, cs *domain.ChangeSet) error {
	if cs.TaskID == "" {
		return nil
	}
	edges, err := e.db.ListGraphEdgesFrom(ctx, cs.InitiativeID, "task", cs.TaskID)
	if err != nil {
		return err
	}
	for _, edge := range edges {
		if edge.EdgeType != domain.EdgeImplements || edge.ToType != "requirement" {
			continue
		}
		req, err := e.db.GetRequirementByRef(ctx, cs.InitiativeID, edge.ToID)
		if err != nil {
			return err
		}
		for i := range req.AcceptanceCriteria {
			ev := &domain.VerificationEvidence{
				InitiativeID:  cs.InitiativeID,
				RequirementID: req.ID,
				CriterionKey:  domain.CriterionKey(req.ID, i),
				Kind:          domain.VerificationAcceptance,
				Status:        domain.VerificationNeedsManual,
				VerifiedBy:    "verification_engineer",
				Note:          "change set " + cs.HumanID + " applied; criterion needs manual verification",
			}
			if err := e.db.CreateVerificationEvidence(ctx, ev); err != nil {
				return err
			}
		}
	}
	return nil
}

// Coverage is a per-requirement verification-coverage summary (§70).
type Coverage struct {
	Code        string `json:"code"`
	Pass        int    `json:"pass"`
	Total       int    `json:"total"`
	NeedsManual int    `json:"needs_manual"`
	Status      string `json:"status"`
}

// Coverage computes the verification coverage for each requirement of an
// initiative (pass criteria / total criteria, plus how many still need a human).
// The requirement's status is surfaced so consumers can distinguish waived and
// failed (neither of which is "needs manual sign-off") from genuinely pending.
func (e *Engine) Coverage(ctx context.Context, initiativeID string) ([]Coverage, error) {
	reqs, err := e.db.ListRequirements(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	out := make([]Coverage, 0, len(reqs))
	for _, r := range reqs {
		if r.Status == domain.RequirementRemoved {
			continue
		}
		passed, err := e.db.PassedCriterionKeys(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		total := len(r.AcceptanceCriteria)
		pass := 0
		for i := range r.AcceptanceCriteria {
			if passed[domain.CriterionKey(r.ID, i)] {
				pass++
			}
		}
		needsManual := total - pass
		if r.Status == domain.RequirementWaived {
			needsManual = 0 // waived is out of scope, not waiting on a human
		}
		out = append(out, Coverage{
			Code:        r.Code,
			Pass:        pass,
			Total:       total,
			NeedsManual: needsManual,
			Status:      string(r.Status),
		})
	}
	return out, nil
}

// RecordHumanEvidence records a human judgment (pass/fail/waived) for every
// acceptance criterion of a requirement (§70, review B10), then applies the
// requirement-level disposition: pass leaves the status for VerifyInitiative to
// mark verified; fail marks it failed; waived marks it waived. This is the human
// sign-off path — UX/security criteria that the deterministic verifier marks
// needs_manual are cleared here by a human actor, never by a model.
func (e *Engine) RecordHumanEvidence(ctx context.Context, initiativeID, requirementRef string, status domain.VerificationStatus, note, actor string) error {
	if actor == "" {
		actor = store.HumanActor
	}
	switch status {
	case domain.VerificationPass, domain.VerificationFail, domain.VerificationWaived:
	default:
		return fmt.Errorf("workflow: invalid human verification status %q", status)
	}
	r, err := e.db.GetRequirementByRef(ctx, initiativeID, requirementRef)
	if err != nil {
		return err
	}
	if len(r.AcceptanceCriteria) == 0 {
		return fmt.Errorf("workflow: requirement %q has no acceptance criteria to verify", requirementRef)
	}
	// Reject invalid source states BEFORE writing any evidence, so a human
	// judgment can never orphan evidence rows on a requirement that is not
	// approvable (draft/removed).
	switch r.Status {
	case domain.RequirementApproved, domain.RequirementVerified, domain.RequirementFailed, domain.RequirementWaived:
	default:
		return fmt.Errorf("workflow: requirement %q in state %q cannot take a human judgment", requirementRef, r.Status)
	}
	// A new human judgment supersedes all prior evidence for this requirement's
	// criteria, so a stale pass cannot resurrect a later fail/waive.
	if err := e.db.ClearVerificationEvidence(ctx, r.ID, actor); err != nil {
		return err
	}
	for i := range r.AcceptanceCriteria {
		ev := &domain.VerificationEvidence{
			InitiativeID:  initiativeID,
			RequirementID: r.ID,
			CriterionKey:  domain.CriterionKey(r.ID, i),
			Kind:          domain.VerificationAcceptance,
			Status:        status,
			VerifiedBy:    actor,
			Note:          note,
		}
		if err := e.db.CreateVerificationEvidence(ctx, ev); err != nil {
			return err
		}
	}
	switch status {
	case domain.VerificationFail:
		return e.db.MarkRequirementFailed(ctx, r.ID, actor)
	case domain.VerificationWaived:
		return e.db.MarkRequirementWaived(ctx, r.ID, actor)
	}
	return nil
}

// RecordHumanPass records a human "pass" evidence for every acceptance criterion
// of a requirement (§70, review B10).
func (e *Engine) RecordHumanPass(ctx context.Context, initiativeID, requirementRef, note, actor string) error {
	return e.RecordHumanEvidence(ctx, initiativeID, requirementRef, domain.VerificationPass, note, actor)
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
		// A fully-covered requirement in an approvable state (approved, or a
		// prior fail/waive that a human has now re-passed) is marked verified.
		switch r.Status {
		case domain.RequirementApproved, domain.RequirementFailed, domain.RequirementWaived:
		default:
			continue // draft, removed, or already verified
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
	// Zero requirements is not "everything verified". The loop below concludes
	// allVerified vacuously when there is nothing to walk, and the write that
	// followed set `complete` over anything -- including a `failed` initiative
	// that died at framing before a single requirement existed (DX audit 7 P0-2).
	//
	// The predicate is "at least one requirement that is not removed", and each
	// half of that matters. len(reqs) alone does not close it: Engine.Revise
	// supersedes every requirement before regenerating, so a model failure there
	// leaves N removed requirements and zero active ones, and the loop `continue`s
	// all N. Excluding waived would go too far the other way and kill the
	// documented all-waived flow, which audit 5's A2 proposed and a plan review
	// rejected for exactly that reason -- waived is a human's judgment that the
	// requirement is out of scope, not an absence of work.
	inScope := 0
	for _, r := range reqs {
		if r.Status != domain.RequirementRemoved {
			inScope++
		}
	}
	if inScope == 0 {
		return nil // nothing was ever built; not done, and not an error
	}

	allVerified := true
	for _, r := range reqs {
		// Removed/superseded requirements are not part of the active set and can
		// never reach "verified"; skip them (review H1) so a revise cycle does not
		// permanently wedge the completion gate. Waived requirements are out of
		// scope, so they are excluded from the gate the same way.
		if r.Status == domain.RequirementRemoved || r.Status == domain.RequirementWaived {
			continue
		}
		if r.Status != domain.RequirementVerified {
			allVerified = false
			break
		}
	}
	if !allVerified {
		// A human can fail a previously-verified requirement after completion
		// (re-evaluation). That must not leave the initiative in a false
		// "complete" state, so downgrade it back to needing human judgment.
		ini, err := e.db.GetInitiative(ctx, initiativeID)
		if err != nil {
			return err
		}
		if ini.Status == domain.InitiativeComplete {
			// The plan is not the problem here and must not be offered as the
			// fix: revise-plan refuses outright once change sets exist, and
			// approve-plan burns an adversarial review to arrive back at the
			// same missing evidence. What this needs is `idea verify`.
			return e.db.NeedsHumanJudgment(ctx, initiativeID, domain.JudgmentVerification, store.ActorOrchestrator)
		}
		return nil // not done yet; no error
	}

	blocking, err := e.hasOpenBlockingFindings(ctx, initiativeID)
	if err != nil {
		return err
	}
	if blocking {
		return fmt.Errorf("workflow: initiative has open blocking findings; cannot complete")
	}
	// CompleteInitiative, not SetInitiativeStatus: the write refuses to run over
	// a `failed` initiative. See its doc comment.
	return e.db.CompleteInitiative(ctx, initiativeID, store.ActorOrchestrator)
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
