package workflow

import (
	"context"
	"errors"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// NextAction returns the deterministic next step for an initiative (§52, Track
// C). It stops at human judgment points (review / apply / resolve / done) and
// otherwise names the next automatable step (advance / execute / verify).
// `failed` and `wait` are terminal-for-now answers rather than steps: the first
// is an error the caller must surface, the second means an operation is in
// flight.
func (e *Engine) NextAction(ctx context.Context, initiativeID string) (string, error) {
	ini, err := e.db.GetInitiative(ctx, initiativeID)
	if err != nil {
		return "", err
	}
	switch ini.Status {
	case domain.InitiativeApproved:
		return "advance", nil
	case domain.InitiativePlanApproved:
		changes, err := e.db.ListChangeSets(ctx, initiativeID)
		if err != nil {
			return "", err
		}
		// Human judgment points first: review → apply → verify.
		for _, cs := range changes {
			if cs.Status == domain.ChangeSetReviewRequired {
				return "review " + cs.HumanID, nil
			}
		}
		for _, cs := range changes {
			if cs.Status == domain.ChangeSetApproved {
				return "apply " + cs.HumanID, nil
			}
		}
		for _, cs := range changes {
			if cs.Status == domain.ChangeSetApplied {
				return "verify", nil
			}
		}
		// Only propose execute when there is something to execute. This used to
		// return "execute" unconditionally, so an initiative whose tasks were
		// all done -- or whose remaining tasks were blocked on unsatisfied
		// dependencies -- was told to execute and then failed in the background.
		// `idea next --until-human` drives itself from this string, so once the
		// execute handler gained a precondition (audit finding b) an
		// unconditional "execute" would have turned that background failure into
		// a hard error out of the loop.
		if _, err := e.nextReadyTask(ctx, initiativeID); err != nil {
			// Only "nothing runnable" means verify. Every other error
			// nextReadyTask can return is a storage failure, and swallowing one
			// here would answer 200 {"action":"verify"} on a broken database --
			// sending the operator to debug verification instead of storage.
			if !errors.Is(err, ErrNoReadyTask) {
				return "", err
			}
			return "verify", nil
		}
		return "execute", nil
	case domain.InitiativeNeedsHumanJudgment:
		// The reason is the whole token. Six code paths produce this status and
		// they want different things: a stalled PRD review is approved or
		// revised, a plan whose dependencies never resolved can only be
		// regenerated, and a requirement failed after completion is re-verified.
		// Before the reason was recorded, every client guessed from the presence
		// of the execution-plan artifact -- which runExecutionPlan writes
		// *before* the dependency check, so the guess said "approve the plan"
		// for a plan already known to be incomplete.
		switch ini.JudgmentReason {
		case domain.JudgmentPRDReview:
			return "resolve prd", nil
		case domain.JudgmentPlanDependencies:
			return "resolve plan-dependencies", nil
		case domain.JudgmentPlanReview:
			return "resolve plan", nil
		case domain.JudgmentVerification:
			return "resolve verification", nil
		}
		// Recorded before this was introduced, so the reason is empty. The bare
		// token is what the CLI rendered for every case until now; keep it
		// rather than inventing a specific instruction we cannot support.
		return "resolve", nil
	case domain.InitiativeComplete:
		return "done", nil
	case domain.InitiativeFailed:
		// Split from `wait`. Collapsing the two told a failed initiative the
		// workflow was still running: `idea next` printed "workflow running --
		// try `idea watch`" and exited 0, forever, while `idea execute`
		// correctly pointed back at `idea next` and `idea watch` needed a TTY.
		// A closed recovery loop, reported as success. Found by audit 5, shipped
		// unfixed, found again by audit 6.
		return "failed", nil
	default: // running
		return "wait", nil
	}
}
