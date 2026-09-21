package workflow

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
)

// TestDerivedRowWritesSurviveCancellationAfterArtifactCommit is review H6's
// regression test.
//
// completedPhases (retry.go) treats an artifact's mere existence as proof
// that its whole phase -- model call AND persistence -- succeeded, and Retry
// skips a phase entirely once its artifact exists. But runFraming/
// runResearch/runProductDefinition each call CreateArtifact and THEN write a
// phase's real payload (assertions, requirements, decisions, questions) as a
// series of separate store calls on the SAME context as the artifact write.
// Stop (or any cancellation of the operation's context) between two of those
// calls -- proven reproducible: "Stop after 6 of 20 CreateRequirement
// calls" -- leaves the artifact durably committed and the rows after it
// wherever the cancellation caught them. Retry then sees "PRD done" and
// skips runProductDefinition entirely, so review/architecture/plan get built
// from a silently truncated requirement set, at real model cost already
// spent, with no error anywhere.
//
// This proves the exact mechanism the fix relies on, deterministically, with
// no goroutines or timing: a real, cancellable context; a real CreateArtifact
// call on it that succeeds (the phase's artifact really did commit, matching
// the proven repro); an explicit cancel(), exactly the moment a Stop would
// land between the artifact commit and the rows that follow it; then two
// writes on the SAME requirement -- one on the bare (now-cancelled) ctx, one
// on context.WithoutCancel(ctx) -- to show both that the vulnerability is
// real (the bare-ctx write fails, exactly as it would have before this fix)
// and that the fix closes it (the WithoutCancel write succeeds). engine.go's
// runFraming/runResearch/runProductDefinition each derive dctx :=
// context.WithoutCancel(ctx) immediately after their CreateArtifact call and
// use dctx, not ctx, for every write that follows -- this is that same
// derivation, applied at the same point in the same sequence.
func TestDerivedRowWritesSurviveCancellationAfterArtifactCommit(t *testing.T) {
	fp := model.NewFakeProvider()
	_, db := newTestEngine(t, fp, config.HarnessConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	ini := &domain.Initiative{Title: "t", Description: "d"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	// The phase's artifact commit -- e.g. the PRD -- must succeed before the
	// cancellation this test simulates, exactly as the proven repro
	// describes ("Stop after 6 of 20 CreateRequirement calls", i.e. after
	// the PRD artifact already exists).
	art := &domain.Artifact{InitiativeID: ini.ID, Kind: domain.ArtifactPRD}
	ver := &domain.ArtifactVersion{BodyDigest: "digest-1", Status: "draft"}
	if err := db.CreateArtifact(ctx, art, ver); err != nil {
		t.Fatalf("CreateArtifact must succeed before the simulated Stop: %v", err)
	}

	// Simulate a Stop landing right after the artifact commit -- the exact
	// window the proven repro exploits.
	cancel()

	// Sanity/negative control: the store genuinely honors context
	// cancellation, so the scenario this test guards against -- a phase's
	// derived rows silently truncated by a cancellation the artifact write
	// already survived -- is real, not something the store already
	// tolerates on its own.
	if err := db.CreateRequirement(ctx, &domain.Requirement{
		InitiativeID: ini.ID, Statement: "canary requirement", Priority: domain.PriorityMust,
	}); err == nil {
		t.Fatal("sanity check failed: CreateRequirement succeeded on an already-cancelled context -- " +
			"this test's premise (the store honors cancellation, so an unshielded write after the " +
			"artifact commit CAN be truncated) no longer holds")
	}

	// The fix (review H6): every requirement/decision/question write that
	// follows a phase's CreateArtifact uses context.WithoutCancel(ctx), not
	// ctx, so the cancellation above cannot reach it.
	dctx := context.WithoutCancel(ctx)
	if err := db.CreateRequirement(dctx, &domain.Requirement{
		InitiativeID: ini.ID, Statement: "Requirement written after the simulated Stop", Priority: domain.PriorityMust,
	}); err != nil {
		t.Fatalf("CreateRequirement over context.WithoutCancel(ctx) should have survived the cancellation "+
			"(review H6's fix in runProductDefinition/runFraming/runResearch), got: %v", err)
	}

	reqs, err := db.ListRequirements(context.Background(), ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 {
		t.Fatalf("expected exactly the one requirement written via WithoutCancel to persist, got %d", len(reqs))
	}
}
