package fsdiff

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/blob"
	"github.com/devarispbrown/idea-harness/internal/domain"
)

func TestStaleBase(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "exists.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	v1 := blob.Digest([]byte("v1"))
	for _, c := range []struct {
		name  string
		ch    domain.FileChange
		stale bool
	}{
		// An add over a file that now exists is how an agent that never saw the
		// workspace deleted a user's code. An all-adds patch has no base
		// digests at all, so emptiness cannot be the test.
		{"add over an existing file", domain.FileChange{Path: "exists.txt", Digest: "x", ChangeType: domain.FileAdded}, true},
		{"add of a new file", domain.FileChange{Path: "new.txt", Digest: "x", ChangeType: domain.FileAdded}, false},
		{"modify at its base", domain.FileChange{Path: "exists.txt", Digest: "x", ChangeType: domain.FileModified, BaseDigest: v1}, false},
		{"modify after an edit", domain.FileChange{Path: "exists.txt", Digest: "x", ChangeType: domain.FileModified, BaseDigest: "other"}, true},
		{"modify with no base recorded", domain.FileChange{Path: "exists.txt", Digest: "x", ChangeType: domain.FileModified}, true},
		{"modify of a deleted file", domain.FileChange{Path: "gone.txt", Digest: "x", ChangeType: domain.FileModified, BaseDigest: v1}, true},
		{"delete at its base", domain.FileChange{Path: "exists.txt", ChangeType: domain.FileDeleted, BaseDigest: v1}, false},
		// A change set made before this check has no ChangeType; it applies as
		// it always did.
		{"legacy change", domain.FileChange{Path: "exists.txt", Digest: "x"}, false},
	} {
		if got := len(StaleBase(root, []domain.FileChange{c.ch})) > 0; got != c.stale {
			t.Errorf("%s: stale=%v, want %v", c.name, got, c.stale)
		}
	}
}

func TestDiffRecordsTheBase(t *testing.T) {
	before := Manifest{{Path: "a", Digest: "d1"}, {Path: "b", Digest: "d2"}}
	after := Manifest{{Path: "a", Digest: "d3"}}
	for _, c := range Diff(before, after) {
		want := map[string]string{"a": "d1", "b": "d2"}[c.Path]
		if c.BaseDigest != want {
			t.Errorf("%s: base %q, want %q", c.Path, c.BaseDigest, want)
		}
	}
}

// Applying a change keeps the replaced file's permissions: an executable
// script stays executable, a readable file stays readable.
func TestWriteKeepsTheFileMode(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "run.sh")
	if err := os.WriteFile(script, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(root, script, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(script); fi.Mode().Perm() != 0o755 {
		t.Errorf("mode %v after apply, want 0755", fi.Mode().Perm())
	}
	fresh := filepath.Join(root, "new.txt")
	if err := writeAtomic(root, fresh, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(fresh); fi.Mode().Perm() != 0o644 {
		t.Errorf("new file mode %v, want 0644", fi.Mode().Perm())
	}
}
