package blob

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestStorePermissionsAreOwnerOnly: raw-blobs/ holds file content and image
// bytes unredacted by design, so the only thing standing between it and
// another local user is the filesystem mode. A store directory that already
// existed with a wider mode is tightened on open, not only a fresh one.
func TestStorePermissionsAreOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	root := filepath.Join(t.TempDir(), "raw-blobs")
	dirs := []string{root, filepath.Join(root, "sha256"), filepath.Join(root, "tmp")}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := s.Put([]byte("file content kept byte-for-byte"))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		fi, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != 0o700 {
			t.Errorf("%s mode = %o, want 700", d, got)
		}
	}
	fi, err := os.Stat(filepath.Join(root, "sha256", digest))
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("blob mode = %o, want 600", got)
	}
}
