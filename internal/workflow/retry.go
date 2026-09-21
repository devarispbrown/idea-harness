package workflow

import (
	"context"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// Retry resumes a failed initiative's vertical slice after the last phase
// that actually completed, under the currently-set per-initiative model
// override (retry-with-a-different-model). Prior requirements and assertions
// are superseded immediately before product definition re-runs, so that
// re-run does not duplicate them; artifacts are immutable-versioned and
// decisions/questions are de-duplicated. The override is cleared when the run
// finishes.
//
// It used to re-run framing→research→PRD unconditionally, "from scratch",
// every time (ICP walkthrough finding: a 503 mid-PRD-authoring, followed by
// `idea retry`, re-asked framing's clarifying questions and re-ran research
// that had already succeeded). Framing does not consult existing
// decisions/questions at all, so redoing it after the human had already
// answered some of them looked, from the outside, like their answers were
// discarded -- even though the rows survive (SupersedeRequirements/
// SupersedeAssertions never touch decisions or questions, and
// runProductDefinition always feeds current decisions/questions back to the
// model). The real defect was doing the redundant work and re-asking at all.
// Resuming after the last completed phase, keyed off artifacts (a durable
// domain fact -- see the operation `stage` column's own doc comment for why
// that is NOT used here: it is a cosmetic progress hint, cleared on terminal
// status) fixes both: no wasted cost, and no re-asked framing questions.
func (e *Engine) Retry(ctx context.Context, initiativeID string) (domain.InitiativeStatus, error) {
	defer e.clearModelOverride(initiativeID)
	defer e.clearNoteOverride(initiativeID)
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

	done, err := e.completedPhases(ctx, initiativeID)
	if err != nil {
		return domain.InitiativeFailed, err
	}
	if !done[domain.ArtifactIdeaBrief] {
		if err := e.runFraming(ctx, initiativeID, ini.Description); err != nil {
			return domain.InitiativeFailed, err
		}
	}
	if !done[domain.ArtifactResearchBrief] {
		if err := e.runResearch(ctx, initiativeID, ini.Description); err != nil {
			return domain.InitiativeFailed, err
		}
	}
	if !done[domain.ArtifactPRD] {
		// Superseded only immediately before the re-run that would otherwise
		// duplicate them -- not unconditionally at the top, so a retry that
		// resumes past product definition (the failure was in the review loop)
		// leaves the already-approved requirement set alone.
		if err := e.db.SupersedeRequirements(ctx, initiativeID, "retry"); err != nil {
			return domain.InitiativeFailed, err
		}
		if err := e.db.SupersedeAssertions(ctx, initiativeID, "retry"); err != nil {
			return domain.InitiativeFailed, err
		}
		// reviseReason "": retry already superseded unconditionally above, a
		// clean rewrite, not a code-stable revision (review ICP re-run #1).
		if err := e.runProductDefinition(ctx, initiativeID, prior, ""); err != nil {
			return domain.InitiativeFailed, err
		}
	}
	return e.runReviewLoop(ctx, initiativeID)
}

// completedPhases reports which of the framing/research/PRD artifacts this
// initiative already has a version of, so Retry can resume after the last one
// that succeeded instead of redoing the whole pipeline. Artifacts are the
// right signal: they are created only once their phase's model call and
// persistence both succeeded, and -- unlike the operation's `stage` column --
// they are a durable domain fact rather than a cosmetic hint that gets
// cleared once the initiative reaches a terminal status.
func (e *Engine) completedPhases(ctx context.Context, initiativeID string) (map[domain.ArtifactKind]bool, error) {
	versions, err := e.db.ListArtifacts(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	done := make(map[domain.ArtifactKind]bool, len(versions))
	for _, v := range versions {
		done[v.Kind] = true
	}
	return done, nil
}
