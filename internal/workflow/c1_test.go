package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idea-harness/idea-harness/internal/blob"
	"github.com/idea-harness/idea-harness/internal/config"
	"github.com/idea-harness/idea-harness/internal/domain"
	"github.com/idea-harness/idea-harness/internal/model"
	"github.com/idea-harness/idea-harness/internal/security"
	"github.com/idea-harness/idea-harness/internal/store"
)

// TestExecutePreservesSecretLikeContent guards the C1/C2 fix: file content must
// round-trip through a REDACTION-FREE store so a secret-like file is neither
// rejected as "corrupt" nor redacted on apply/revert.
func TestExecutePreservesSecretLikeContent(t *testing.T) {
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
	workspace := t.TempDir()
	eng.SetWorkspaceRoot(workspace)

	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	// An AWS-access-key-looking string: caught by the redactor (AKIA…) but not by
	// the whole-word secret sweep, so it must survive raw and reach
	// review_required (not be rejected as "content missing or corrupt").
	task := &domain.Task{InitiativeID: ini.ID, Title: "Add key AKIAIOSFODNN7EXAMPLE", Statement: "x", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}

	cs, err := eng.Execute(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Status != domain.ChangeSetReviewRequired {
		t.Fatalf("expected review_required (content preserved raw), got %q", cs.Status)
	}

	if err := eng.ReviewChangeSet(ctx, ini.ID, cs.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	if err := eng.ApplyChangeSet(ctx, ini.ID, cs.ID); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(workspace, "hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "AKIAIOSFODNN7EXAMPLE") {
		t.Fatalf("content was redacted on apply: %q", string(data))
	}

	if err := eng.RevertChangeSet(ctx, ini.ID, cs.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "hello.txt")); !os.IsNotExist(err) {
		t.Fatal("hello.txt not removed on revert")
	}
}
