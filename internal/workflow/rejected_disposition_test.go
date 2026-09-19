package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// TestHumanRejectedReviewerFindingUnblocksAcceptFindings reproduces the ICP
// re-run's exact reported sequence for defect #2: a founder-journey run
// dispositioned a reviewer FALSE POSITIVE "rejected", after which
// `approve --accept-findings` kept failing with a message recommending
// --accept-findings, and only accepted_risk/fixed ever cleared the block.
//
// The fix (Finding.ResolvesBlocking, ICP re-run defect #2): a human's
// rejected on a reviewer's own judgment call (no system deny code) resolves
// it exactly like accepted_risk would -- "the human judged this finding
// wrong" is not a reason to keep denying forever.
func TestHumanRejectedReviewerFindingUnblocksAcceptFindings(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	eng.SetWorkRoot(t.TempDir())
	eng.SetWorkspaceRoot(t.TempDir())
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}

	// A real, in-diff, high-severity finding -- the adversarial reviewer's
	// own judgment, no system deny code -- that will later turn out to be a
	// false positive a human rejects.
	fp.SetResponse(ChangeSetReviewSchemaName, `{"findings":[{`+
		`"severity":"critical","confidence":"high","category":"x",`+
		`"finding":"looks like an injection risk","recommendation":"review it",`+
		`"path":"hello.txt"`+
		`}]}`)

	// First approve attempt: the fresh review's own critical finding blocks,
	// exactly as before this fix.
	if err := eng.ReviewChangeSet(ctx, ini.ID, cs.ID, "approved"); err == nil {
		t.Fatal("expected the first approve to be blocked by the reviewer's own critical finding")
	}
	got, err := db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ChangeSetReviewRequired {
		t.Fatalf("expected the change set to stay review_required after the blocked approve, got %q", got.Status)
	}

	findings, err := db.ListFindingsByTarget(ctx, "change_set", cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	var f *domain.Finding
	for i := range findings {
		if findings[i].Category == "x" {
			f = &findings[i]
		}
	}
	if f == nil {
		t.Fatalf("expected the scripted reviewer finding to be recorded among %+v", findings)
	}
	if f.Code != "" {
		t.Fatalf("setup: expected a reviewer judgment call with no deny code, got %q", f.Code)
	}

	// The human judges the finding a false positive and rejects it.
	if err := db.SetFindingDisposition(ctx, f.ID, domain.DispositionRejected, store.HumanActor); err != nil {
		t.Fatal(err)
	}

	// Before the fix: this still failed, with a message recommending
	// --accept-findings -- which is exactly the flag already in use here.
	if err := eng.ReviewChangeSetAccepting(ctx, ini.ID, cs.ID, "approved"); err != nil {
		t.Fatalf("approve --accept-findings should succeed once the reviewer's finding is human-rejected: %v", err)
	}

	got, err = db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ChangeSetApproved {
		t.Fatalf("expected the change set approved, got %q", got.Status)
	}
}

// TestSystemSetRejectedStillBlocksAcceptFindings is the other half: a SYSTEM-
// set rejected (a secret_sweep finding denied at execute time, which carries
// a deny code) must keep blocking even under --accept-findings -- "system
// deny beats human approve" is unaffected by this fix.
func TestSystemSetRejectedStillBlocksAcceptFindings(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()

	task := &domain.Task{InitiativeID: iniID, Title: "Add key AKIAIOSFODNN7EXAMPLE", Statement: "x", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Status != domain.ChangeSetRejected {
		t.Fatalf("setup: expected rejected (real credential), got %q", cs.Status)
	}

	// denyReviewOpenBlocking is the exact gate --accept-findings runs through
	// (reviewChangeSet's acceptFindings branch calls it after signing off
	// unclaimed, non-deny-coded findings); it must still refuse here even
	// though the finding's Disposition is "rejected", because it is a
	// SYSTEM-set rejected carrying a deny code (secret_sweep), not a human's
	// judgment call.
	if err := eng.denyReviewOpenBlocking(ctx, cs.ID); err == nil {
		t.Fatal("expected a system-deny-coded secret_sweep finding to still block")
	} else if !strings.Contains(err.Error(), "system deny") {
		t.Errorf("expected a system-deny error, got: %v", err)
	}
}
