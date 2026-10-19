package store

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestRequirementChangeKindDerived pins the read-time delta: added on creation,
// modified after an in-place update, removed after supersede — derived from the
// row's own lifecycle, never a stored parallel column.
func TestRequirementChangeKindDerived(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	ini := &domain.Initiative{Title: "t"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	r := &domain.Requirement{InitiativeID: ini.ID, Statement: "Be fast", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, r); err != nil {
		t.Fatal(err)
	}

	reqs, err := db.ListRequirements(ctx, ini.ID)
	if err != nil || len(reqs) != 1 {
		t.Fatalf("expected 1 requirement, got %d (err=%v)", len(reqs), err)
	}
	if reqs[0].ChangeKind != domain.RequirementChangeAdded {
		t.Fatalf("change kind = %q, want added", reqs[0].ChangeKind)
	}

	upd := reqs[0]
	upd.Statement = "Be very fast"
	if err := db.UpdateRequirement(ctx, &upd); err != nil {
		t.Fatal(err)
	}
	reqs, _ = db.ListRequirements(ctx, ini.ID)
	if reqs[0].ChangeKind != domain.RequirementChangeModified {
		t.Fatalf("change kind = %q, want modified", reqs[0].ChangeKind)
	}

	if err := db.SupersedeRequirements(ctx, ini.ID, "rev-2"); err != nil {
		t.Fatal(err)
	}
	reqs, _ = db.ListRequirements(ctx, ini.ID)
	if reqs[0].ChangeKind != domain.RequirementChangeRemoved {
		t.Fatalf("change kind = %q, want removed", reqs[0].ChangeKind)
	}
}

// TestSeedRequirementsFromCopiesLiveWithCodes pins brownfield seeding: only live
// requirements are copied, with their codes preserved (so a subsequent PRD run
// treats them as continuing and emits a delta), and as approved baselines.
func TestSeedRequirementsFromCopiesLiveWithCodes(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	src := &domain.Initiative{Title: "src"}
	if err := db.CreateInitiative(ctx, src); err != nil {
		t.Fatal(err)
	}
	r1 := &domain.Requirement{InitiativeID: src.ID, Statement: "Be fast", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, r1); err != nil {
		t.Fatal(err)
	}
	r2 := &domain.Requirement{InitiativeID: src.ID, Statement: "Be secure", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, r2); err != nil {
		t.Fatal(err)
	}
	if err := db.SupersedeRequirementsExcept(ctx, src.ID, "dropped", []string{r1.ID}); err != nil {
		t.Fatal(err)
	}

	dest := &domain.Initiative{Title: "dest"}
	if err := db.CreateInitiative(ctx, dest); err != nil {
		t.Fatal(err)
	}
	n, err := db.SeedRequirementsFrom(ctx, dest.ID, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("seeded %d, want 1 (r2 removed)", n)
	}
	reqs, err := db.ListRequirements(ctx, dest.ID)
	if err != nil || len(reqs) != 1 {
		t.Fatalf("seeded requirements = %d (err=%v), want 1", len(reqs), err)
	}
	if reqs[0].Code != r1.Code || reqs[0].Statement != "Be fast" {
		t.Fatalf("seeded = %+v, want r1 with its code preserved", reqs[0])
	}
	if reqs[0].Status != domain.RequirementApproved {
		t.Fatalf("seeded status = %q, want approved", reqs[0].Status)
	}
}
