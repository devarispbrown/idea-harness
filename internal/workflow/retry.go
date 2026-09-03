package workflow

import (
	"context"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// Retry re-runs the full vertical slice from scratch under the currently-set
// per-initiative model override (retry-with-a-different-model). Prior
// requirements and assertions are superseded first so the re-run does not
// duplicate them; artifacts are immutable-versioned and decisions/questions are
// de-duplicated. The override is cleared when the run finishes.
func (e *Engine) Retry(ctx context.Context, initiativeID string) (domain.InitiativeStatus, error) {
	defer e.clearModelOverride(initiativeID)
	e.log.Info("retrying workflow with model override", "initiative", initiativeID, "model", e.currentModelOverride(initiativeID))

	ini, err := e.db.GetInitiative(ctx, initiativeID)
	if err != nil {
		return domain.InitiativeFailed, err
	}
	all, err := e.db.ListRequirements(ctx, initiativeID)
	if err != nil {
		return domain.InitiativeFailed, err
	}
	prior := activeRequirements(all)
	if err := e.db.SupersedeRequirements(ctx, initiativeID, "retry"); err != nil {
		return domain.InitiativeFailed, err
	}
	if err := e.db.SupersedeAssertions(ctx, initiativeID, "retry"); err != nil {
		return domain.InitiativeFailed, err
	}
	if err := e.runFraming(ctx, initiativeID, ini.Description); err != nil {
		return domain.InitiativeFailed, err
	}
	if err := e.runResearch(ctx, initiativeID, ini.Description); err != nil {
		return domain.InitiativeFailed, err
	}
	if err := e.runProductDefinition(ctx, initiativeID, prior); err != nil {
		return domain.InitiativeFailed, err
	}
	return e.runReviewLoop(ctx, initiativeID)
}
