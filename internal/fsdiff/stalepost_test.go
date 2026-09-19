package fsdiff

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/blob"
	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestStalePost mirrors TestStaleBase at the opposite end of a change set's
// life: apply protects an edit made before it ran, revert protects one made
// after (review A3).
func TestStalePost(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "written.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	v2 := blob.Digest([]byte("v2"))
	for _, c := range []struct {
		name  string
		ch    domain.FileChange
		stale bool
	}{
		{"add still matches what apply wrote", domain.FileChange{Path: "written.txt", Digest: v2, ChangeType: domain.FileAdded}, false},
		{"add is now missing", domain.FileChange{Path: "missing.txt", Digest: "x", ChangeType: domain.FileAdded}, true},
		{"add was edited after apply", domain.FileChange{Path: "written.txt", Digest: "other", ChangeType: domain.FileAdded}, true},
		{"modify still matches what apply wrote", domain.FileChange{Path: "written.txt", Digest: v2, ChangeType: domain.FileModified, BaseDigest: "v1"}, false},
		{"modify was edited after apply", domain.FileChange{Path: "written.txt", Digest: "other", ChangeType: domain.FileModified, BaseDigest: "v1"}, true},
		{"modify is now missing", domain.FileChange{Path: "missing.txt", Digest: "x", ChangeType: domain.FileModified}, true},
		{"delete is still absent", domain.FileChange{Path: "gone.txt", ChangeType: domain.FileDeleted, BaseDigest: "v1"}, false},
		{"delete now exists again", domain.FileChange{Path: "written.txt", ChangeType: domain.FileDeleted, BaseDigest: "v1"}, true},
		// A change set made before this check has no ChangeType; it restores as
		// it always did.
		{"legacy change", domain.FileChange{Path: "written.txt", Digest: "anything"}, false},
	} {
		if got := len(StalePost(root, []domain.FileChange{c.ch})) > 0; got != c.stale {
			t.Errorf("%s: stale=%v, want %v", c.name, got, c.stale)
		}
	}
}

// TestStalePostRefusesWithoutMutating documents (and TestRevertRefusesAStalePost
// in internal/workflow exercises end-to-end) that StalePost is read-only: it
// never touches the filesystem, it only reports.
func TestStalePostRefusesWithoutMutating(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "written.txt")
	if err := os.WriteFile(path, []byte("edited-by-human"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	stale := StalePost(root, []domain.FileChange{
		{Path: "written.txt", Digest: blob.Digest([]byte("agent-wrote-this")), ChangeType: domain.FileModified, BaseDigest: "v1"},
	})
	if len(stale) == 0 {
		t.Fatal("want stale, got none")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("StalePost mutated the file: before %q, after %q", before, after)
	}
}
