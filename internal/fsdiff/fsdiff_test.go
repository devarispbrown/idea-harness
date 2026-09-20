package fsdiff

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/blob"
	"github.com/devarispbrown/idea-harness/internal/domain"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSnapshotAndDiff(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.txt", "one")
	writeFile(t, root, "b.txt", "two")
	before, err := Snapshot(root)
	if err != nil {
		t.Fatal(err)
	}

	// modify a, add c, delete b
	writeFile(t, root, "a.txt", "one-v2")
	writeFile(t, root, "c.txt", "three")
	if err := os.Remove(filepath.Join(root, "b.txt")); err != nil {
		t.Fatal(err)
	}
	after, err := Snapshot(root)
	if err != nil {
		t.Fatal(err)
	}

	diff := Diff(before, after)
	got := make(map[string]domain.FileChange, len(diff))
	for _, fc := range diff {
		got[fc.Path] = fc
	}
	if len(diff) != 3 {
		t.Fatalf("diff len = %d, want 3: %+v", len(diff), diff)
	}
	if got["a.txt"].Digest != blob.Digest([]byte("one-v2")) {
		t.Errorf("a.txt digest = %q, want digest of %q", got["a.txt"].Digest, "one-v2")
	}
	if !got["b.txt"].IsDelete() {
		t.Errorf("b.txt should be a deletion, got %+v", got["b.txt"])
	}
	if got["c.txt"].Digest != blob.Digest([]byte("three")) {
		t.Errorf("c.txt digest wrong")
	}
}

func TestApplyRestoreRoundTrip(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "mod.txt", "old")
	writeFile(t, root, "del.txt", "gone")

	content := map[string][]byte{}
	get := func(d string) ([]byte, error) { return content[d], nil }
	put := func(b []byte) (string, error) {
		d := blob.Digest(b)
		content[d] = b
		return d, nil
	}

	modContent := []byte("new")
	addContent := []byte("hello")
	content[blob.Digest(modContent)] = modContent
	content[blob.Digest(addContent)] = addContent

	changes := []domain.FileChange{
		{Path: "mod.txt", Digest: blob.Digest(modContent), Mode: "file"},
		{Path: "add.txt", Digest: blob.Digest(addContent), Mode: "file"},
		{Path: "del.txt", Mode: "file"}, // delete
	}

	pre, err := Apply(root, changes, get, put)
	if err != nil {
		t.Fatal(err)
	}

	if readFile(t, root, "mod.txt") != "new" {
		t.Errorf("mod.txt = %q, want %q", readFile(t, root, "mod.txt"), "new")
	}
	if readFile(t, root, "add.txt") != "hello" {
		t.Errorf("add.txt not applied")
	}
	if _, err := os.Stat(filepath.Join(root, "del.txt")); !os.IsNotExist(err) {
		t.Errorf("del.txt not removed")
	}

	if err := Restore(root, pre, get); err != nil {
		t.Fatal(err)
	}

	// Byte-for-byte restoration of the pre-image.
	if readFile(t, root, "mod.txt") != "old" {
		t.Errorf("mod.txt = %q after restore, want %q", readFile(t, root, "mod.txt"), "old")
	}
	if _, err := os.Stat(filepath.Join(root, "add.txt")); !os.IsNotExist(err) {
		t.Errorf("add.txt should be removed on restore")
	}
	if readFile(t, root, "del.txt") != "gone" {
		t.Errorf("del.txt = %q after restore, want %q", readFile(t, root, "del.txt"), "gone")
	}
}

// TestApplyLeavesNoStrayTempFileInTheWorkspace pins the review defect I fix:
// writeAtomic used to stage its temp file directly beside the destination
// (os.CreateTemp(filepath.Dir(dest), ".idea-harness-*")) -- a real run
// observed a stray ".idea-harness-<random>" entry sitting in the tracked
// workspace during apply, indistinguishable from a real file to `git status`
// if the process ever died between CreateTemp and the rename. The temp file
// must now be staged under root/.idea-harness/tmp/ instead, and none of the
// destination's own siblings (the top-level workspace directory this test
// applies into) may ever contain a ".idea-harness-" prefixed entry, whether
// the apply is still in flight or already finished.
func TestApplyLeavesNoStrayTempFileInTheWorkspace(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "existing/nested/mod.txt", "old")

	content := map[string][]byte{}
	get := func(d string) ([]byte, error) { return content[d], nil }
	put := func(b []byte) (string, error) {
		d := blob.Digest(b)
		content[d] = b
		return d, nil
	}
	modContent := []byte("new")
	addContent := []byte("hello")
	content[blob.Digest(modContent)] = modContent
	content[blob.Digest(addContent)] = addContent

	changes := []domain.FileChange{
		{Path: "existing/nested/mod.txt", Digest: blob.Digest(modContent), Mode: "file"},
		{Path: "brand/new/dir/add.txt", Digest: blob.Digest(addContent), Mode: "file"},
	}
	if _, err := Apply(root, changes, get, put); err != nil {
		t.Fatal(err)
	}

	assertNoStrayTempEntries(t, root)

	// The staging directory itself must be exactly .idea-harness/tmp (this is
	// the deterministic, mutation-provable half of this test: a crash between
	// CreateTemp and the rename is not reproducible in a test, but
	// MkdirAll(tmpDir, …) running at all is -- the old same-directory scheme
	// never created this path). It must be empty afterward: CreateTemp's file
	// is removed by the deferred cleanup once renamed away.
	tmpDir := filepath.Join(root, ".idea-harness", "tmp")
	info, err := os.Stat(tmpDir)
	if err != nil {
		t.Fatalf("stat %s: %v (writeAtomic should stage through this directory)", tmpDir, err)
	}
	if !info.IsDir() {
		t.Fatalf("%s exists but is not a directory", tmpDir)
	}
	if entries, err := os.ReadDir(tmpDir); err == nil && len(entries) != 0 {
		t.Errorf(".idea-harness/tmp/ has %d leftover entries after a successful apply, want 0: %v", len(entries), entries)
	}
}

// TestRestoreLeavesNoStrayTempFileInTheWorkspace is the revert-path
// counterpart: Restore uses the same writeAtomic, and must be just as clean.
func TestRestoreLeavesNoStrayTempFileInTheWorkspace(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "mod.txt", "old")
	content := map[string][]byte{blob.Digest([]byte("old")): []byte("old")}
	get := func(d string) ([]byte, error) { return content[d], nil }

	pre := Manifest{{Path: "mod.txt", Digest: blob.Digest([]byte("old"))}}
	if err := Restore(root, pre, get); err != nil {
		t.Fatal(err)
	}
	assertNoStrayTempEntries(t, root)
	if info, err := os.Stat(filepath.Join(root, ".idea-harness", "tmp")); err != nil || !info.IsDir() {
		t.Fatalf(".idea-harness/tmp should exist as a directory after Restore, got err=%v", err)
	}
}

// assertNoStrayTempEntries walks root (excluding .idea-harness/, the
// harness's own designated scratch location) and fails the test if any entry
// name starts with ".idea-harness-" -- the exact pattern writeAtomic used to
// leave behind directly in the user's tracked tree.
func assertNoStrayTempEntries(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != root && d.IsDir() && d.Name() == ".idea-harness" {
			return filepath.SkipDir
		}
		if strings.HasPrefix(d.Name(), ".idea-harness-") {
			t.Errorf("stray temp entry %q found outside .idea-harness/tmp", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestApplyRejectsPathEscape(t *testing.T) {
	root := t.TempDir()
	get := func(string) ([]byte, error) { return []byte("x"), nil }
	put := func(b []byte) (string, error) { return blob.Digest(b), nil }

	changes := []domain.FileChange{
		{Path: "../evil.txt", Digest: blob.Digest([]byte("x")), Mode: "file"},
	}
	if _, err := Apply(root, changes, get, put); err == nil {
		t.Fatal("Apply with a path-escape change = nil error, want error")
	}
}

func TestPrepareIsNonMutatingAndCommitApplies(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.txt", "old")

	content := map[string][]byte{}
	get := func(d string) ([]byte, error) { return content[d], nil }
	put := func(b []byte) (string, error) {
		d := blob.Digest(b)
		content[d] = b
		return d, nil
	}
	newContent := []byte("new")
	content[blob.Digest(newContent)] = newContent

	changes := []domain.FileChange{{Path: "a.txt", Digest: blob.Digest(newContent), Mode: "file"}}

	pre, err := Prepare(root, changes, put)
	if err != nil {
		t.Fatal(err)
	}
	// Prepare must NOT mutate: a.txt still holds the old content.
	if readFile(t, root, "a.txt") != "old" {
		t.Fatalf("Prepare mutated a.txt: %q", readFile(t, root, "a.txt"))
	}
	// But the pre-image must be persisted (recoverable).
	if len(pre) != 1 || pre[0].Digest != blob.Digest([]byte("old")) {
		t.Fatalf("unexpected pre-image: %+v", pre)
	}

	if err := Commit(root, changes, get); err != nil {
		t.Fatal(err)
	}
	if readFile(t, root, "a.txt") != "new" {
		t.Fatalf("Commit did not apply: %q", readFile(t, root, "a.txt"))
	}
}
