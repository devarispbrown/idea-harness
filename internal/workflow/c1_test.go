package workflow

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/blob"
	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
	"github.com/devarispbrown/idea-harness/internal/security"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// TestExecuteRejectsCredentialContent guards C1/C2 + M1 together: a secret-like
// file must be REJECTED by a real secret_sweep finding (the raw store preserves
// content so the detector sees it), not by a bogus "content missing or corrupt"
// patch-applies failure from the redaction/digest mismatch.
func TestExecuteRejectsCredentialContent(t *testing.T) {
	redacting, err := blob.New(t.TempDir(), security.NewRedactor().Redact)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := blob.New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}

	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	reg := model.NewRegistry("idea-harness-default")
	reg.Add("idea-harness-default", model.NewFakeProvider())
	eng := New(db, redacting, reg, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}}, nil)
	eng.SetRawBlobs(raw)
	eng.SetWorkRoot(t.TempDir())
	eng.SetWorkspaceRoot(t.TempDir())

	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	// An AWS-access-key-looking string is caught by the redactor AND the strong
	// credential detector, so the change set must be rejected via secret_sweep.
	task := &domain.Task{InitiativeID: ini.ID, Title: "Add key AKIAIOSFODNN7EXAMPLE", Statement: "x", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}

	cs, err := eng.Execute(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Status != domain.ChangeSetRejected {
		t.Fatalf("expected rejected (credential detected), got %q", cs.Status)
	}

	findings, err := db.ListFindingsByInitiative(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range findings {
		if f.TargetID == cs.ID && f.Category == "secret_sweep" {
			found = true
		}
		if f.TargetID == cs.ID && f.Category == "patch_applies" && f.Severity.Blocking() {
			t.Fatalf("credential content was rejected as a bogus patch-applies failure: %+v", f)
		}
	}
	if !found {
		t.Fatal("expected a secret_sweep finding, not a content-corrupt rejection")
	}
}
