package workflow

import (
	"context"
	"errors"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// NextAction returns the deterministic next step for an initiative (§52, Track
// C). It stops at human judgment points (review / apply / resolve / done) and
// otherwise names the next automatable step (advance / execute / verify).
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
		return "resolve", nil
	case domain.InitiativeComplete:
		return "done", nil
	default: // running / failed
		return "wait", nil
	}
}
