package workflow

import (
	"context"

	"github.com/idea-harness/idea-harness/internal/domain"
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
		return "execute", nil
	case domain.InitiativeNeedsHumanJudgment:
		return "resolve", nil
	case domain.InitiativeComplete:
		return "done", nil
	default: // running / failed
		return "wait", nil
	}
}
