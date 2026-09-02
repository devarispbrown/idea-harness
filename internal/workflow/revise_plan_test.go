package workflow

import (
	"context"
	"testing"

	"github.com/idea-harness/idea-harness/internal/config"
	"github.com/idea-harness/idea-harness/internal/domain"
	"github.com/idea-harness/idea-harness/internal/model"
)

func TestRevisePlanRegeneratesTasks(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Run(ctx, ini.ID, ini.Description); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Advance(ctx, ini.ID); err != nil {
		t.Fatal(err)
	}

	before, err := db.ListTasks(ctx, ini.ID)
	if err != nil || len(before) == 0 {
		t.Fatalf("expected tasks before revision (err=%v)", err)
	}

	status, err := eng.RevisePlan(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status != domain.InitiativePlanApproved && status != domain.InitiativeNeedsHumanJudgment {
		t.Fatalf("status = %q", status)
	}

	after, err := db.ListTasks(ctx, ini.ID)
	if err != nil || len(after) == 0 {
		t.Fatalf("expected regenerated tasks (err=%v)", err)
	}
	oldIDs := map[string]bool{}
	for _, tk := range before {
		oldIDs[tk.ID] = true
	}
	for _, tk := range after {
		if oldIDs[tk.ID] {
			t.Fatal("an old task survived plan revision")
		}
	}
}

func TestRevisePlanRefusesWithChangeSets(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	// A change set means execution has begun; plan revision must refuse.
	if err := db.CreateChangeSet(ctx, &domain.ChangeSet{InitiativeID: ini.ID, PatchDigest: "p"}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.RevisePlan(ctx, ini.ID); err == nil {
		t.Fatal("expected RevisePlan to refuse when change sets exist")
	}
}
