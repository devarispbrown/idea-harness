// Package fsdiff snapshots a directory tree and computes/applies/restores
// content-addressed file diffs (§36). It is the single implementation shared by
// the internal implementer (which writes files) and the engine (which applies
// and reverts the diff), so both sides agree on the representation. The diff is
// always computed by the orchestrator from a before/after snapshot — never
// trusted from an agent's self-declaration.
package fsdiff

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/idea-harness/idea-harness/internal/blob"
	"github.com/idea-harness/idea-harness/internal/domain"
	"github.com/idea-harness/idea-harness/internal/security"
)

// Entry is one file in a snapshot manifest: a workspace-relative path and the
// SHA-256 of its content.
type Entry struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

// Manifest is a path-sorted snapshot of a directory's regular files.
type Manifest []Entry

// Patch is the content-addressed diff manifest persisted as a change set's
// patch. BaseSnapshotDigest references the pre-image snapshot; Files is the
// ordered change list.
type Patch struct {
	BaseSnapshotDigest string              `json:"base_snapshot_digest,omitempty"`
	Files              []domain.FileChange `json:"files"`
}

// controlPlaneDirs are the directories a change set may never read or write.
//
// Containment (security.ResolveWithin) keeps a change set inside the workspace,
// but the workspace contains the machinery that governs the change set itself:
//
//   - .idea-harness/ holds harness.yaml — providers and their base_url, network
//     policy, executor kind, build_cmd/test_cmd, git_commit. A model-authored
//     diff editing it carries no credential and no path escape, so it trips
//     neither the secret sweep nor the path guard, yet it rewrites the governor.
//   - .git/ holds hooks. The harness runs its own commits with hooks disabled
//     (internal/git), but a pre-commit hook left in the workspace executes on
//     the developer's next manual commit.
//
// The deny is enforced in the walk, so an agent's writes there never enter a
// manifest, never become a change set, and never reach review — rather than
// relying on a reviewer to notice them in a diff. Apply and Prepare re-check,
// so a patch that names one of these paths is rejected even if it was built
// some other way. .gitignore is deliberately NOT covered: it sits outside .git/
// and is a legitimate thing for an agent to edit.
var controlPlaneDirs = []string{".idea-harness", ".git"}

// isControlPlane reports whether a slash-separated relative path lies inside a
// control-plane directory.
func isControlPlane(rel string) bool {
	rel = path.Clean(filepath.ToSlash(rel))
	for _, dir := range controlPlaneDirs {
		if rel == dir || strings.HasPrefix(rel, dir+"/") {
			return true
		}
	}
	return false
}

// ErrControlPlanePath is returned when a change set names a path that governs
// the harness itself.
var ErrControlPlanePath = errors.New("fsdiff: path is part of the harness control plane and cannot be changed by a change set")

// Snapshot walks root and returns a path-sorted manifest of its regular files
// (content-addressed). Directories, symlinks, and non-regular files are ignored.
// Symlinks are rejected via Lstat (not followed) so an untrusted executor cannot
// plant a symlink to a host file and induce the daemon to read it (review:
// v1 security boundary). A missing root yields an empty manifest.
func Snapshot(root string) (Manifest, error) {
	var m Manifest
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if rel, relErr := filepath.Rel(root, p); relErr == nil && rel != "." && isControlPlane(rel) {
				return fs.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			// A symlink reports a non-regular mode under Lstat, so it is skipped
			// here rather than followed.
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		// Re-Lstat before reading: never follow a swapped-in symlink.
		if fi, err := os.Lstat(p); err != nil {
			return err
		} else if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		m = append(m, Entry{Path: filepath.ToSlash(rel), Digest: blob.Digest(data)})
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("fsdiff: snapshot %s: %w", root, err)
	}
	sort.Slice(m, func(i, j int) bool { return m[i].Path < m[j].Path })
	return m, nil
}

// Digest returns the content digest of a manifest (its canonical JSON).
func (m Manifest) Digest() string {
	b, _ := json.Marshal(m)
	return blob.Digest(b)
}

// Digest returns the content digest of a patch manifest (its canonical JSON).
func (p Patch) Digest() string {
	b, _ := json.Marshal(p)
	return blob.Digest(b)
}

// Diff computes the changes from before to after. Added/modified files carry
// their post-image digest; files present only in before are deletions (empty
// digest). The result is path-sorted.
func Diff(before, after Manifest) []domain.FileChange {
	beforeBy := make(map[string]string, len(before))
	for _, e := range before {
		beforeBy[e.Path] = e.Digest
	}
	afterBy := make(map[string]string, len(after))
	for _, e := range after {
		afterBy[e.Path] = e.Digest
	}

	paths := make(map[string]bool, len(before)+len(after))
	for _, e := range before {
		paths[e.Path] = true
	}
	for _, e := range after {
		paths[e.Path] = true
	}
	sorted := make([]string, 0, len(paths))
	for p := range paths {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)

	out := make([]domain.FileChange, 0, len(sorted))
	for _, p := range sorted {
		b, bok := beforeBy[p]
		a, aok := afterBy[p]
		switch {
		case bok && !aok:
			out = append(out, domain.FileChange{Path: p, Mode: "file"}) // delete
		case aok && (!bok || a != b):
			out = append(out, domain.FileChange{Path: p, Digest: a, Mode: "file"})
		}
	}
	return out
}

// Prepare captures the pre-image of every touched path (persisting each via put)
// WITHOUT mutating root. It returns the pre-image manifest so a caller can
// persist it BEFORE any byte is written, making an interrupted apply recoverable
// (review: full-platform apply atomicity).
func Prepare(root string, changes []domain.FileChange, put func(content []byte) (string, error)) (Manifest, error) {
	var pre Manifest
	for _, c := range changes {
		if isControlPlane(c.Path) {
			return nil, fmt.Errorf("%w: %q", ErrControlPlanePath, c.Path)
		}
		dest, err := security.ResolveWithin(root, c.Path)
		if err != nil {
			return nil, fmt.Errorf("fsdiff: prepare %q: %w", c.Path, err)
		}
		preDigest := ""
		if old, err := os.ReadFile(dest); err == nil {
			if put != nil {
				if preDigest, err = put(old); err != nil {
					return nil, fmt.Errorf("fsdiff: persist pre-image %q: %w", c.Path, err)
				}
			} else {
				preDigest = blob.Digest(old)
			}
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("fsdiff: read pre-image %q: %w", c.Path, err)
		}
		pre = append(pre, Entry{Path: c.Path, Digest: preDigest})
	}
	sort.Slice(pre, func(i, j int) bool { return pre[i].Path < pre[j].Path })
	return pre, nil
}

// Commit writes (and deletes) the changes into root, assuming the pre-image was
// already captured by Prepare. Every Path is containment-checked against root
// before any write, and writes are atomic (temp file + rename).
func Commit(root string, changes []domain.FileChange, get func(digest string) ([]byte, error)) error {
	for _, c := range changes {
		if isControlPlane(c.Path) {
			return fmt.Errorf("%w: %q", ErrControlPlanePath, c.Path)
		}
		dest, err := security.ResolveWithin(root, c.Path)
		if err != nil {
			return fmt.Errorf("fsdiff: commit %q: %w", c.Path, err)
		}
		if c.IsDelete() {
			if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("fsdiff: remove %q: %w", c.Path, err)
			}
			continue
		}
		content, err := get(c.Digest)
		if err != nil {
			return fmt.Errorf("fsdiff: get %q: %w", c.Digest, err)
		}
		if err := writeAtomic(dest, content); err != nil {
			return fmt.Errorf("fsdiff: write %q: %w", c.Path, err)
		}
	}
	return nil
}

// Apply writes changes into root and returns the pre-image manifest of every
// touched path (for revert). It is the two-phase Prepare+Commit composed for
// callers that do not need the crash-safety window between them.
func Apply(root string, changes []domain.FileChange, get func(digest string) ([]byte, error), put func(content []byte) (string, error)) (Manifest, error) {
	pre, err := Prepare(root, changes, put)
	if err != nil {
		return nil, err
	}
	if err := Commit(root, changes, get); err != nil {
		return nil, err
	}
	return pre, nil
}

// Restore restores a pre-image manifest. Entries with an empty digest did not
// exist before and are removed; others are rewritten to their prior content.
func Restore(root string, pre Manifest, get func(digest string) ([]byte, error)) error {
	for _, e := range pre {
		dest, err := security.ResolveWithin(root, e.Path)
		if err != nil {
			return fmt.Errorf("fsdiff: restore %q: %w", e.Path, err)
		}
		if e.Digest == "" {
			if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("fsdiff: restore remove %q: %w", e.Path, err)
			}
			continue
		}
		content, err := get(e.Digest)
		if err != nil {
			return fmt.Errorf("fsdiff: restore get %q: %w", e.Digest, err)
		}
		if err := writeAtomic(dest, content); err != nil {
			return fmt.Errorf("fsdiff: restore write %q: %w", e.Path, err)
		}
	}
	return nil
}

func writeAtomic(dest string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".idea-harness-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return err
	}
	return nil
}
