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
