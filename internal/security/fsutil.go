package security

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrPathEscape is returned when a path resolves outside its allowed root.
var ErrPathEscape = errors.New("security: path escapes allowed root")

// ResolveWithin joins rel to root, canonicalizes it, and guarantees the result
// (after symlink resolution of the deepest existing ancestor) stays within
// root. It is the single path guard for blob/config/cache/log access (§15).
func ResolveWithin(root, rel string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("security: abs root: %w", err)
	}
	rootAbs = filepath.Clean(rootAbs)

	joined := filepath.Join(rootAbs, rel)
	joined = filepath.Clean(joined)

	if !within(rootAbs, joined) {
		return "", ErrPathEscape
	}

	// Resolve symlinks on the root itself (e.g. macOS /var -> /private/var)
	// so the final containment check compares like-for-like paths.
	rootResolved, err := resolveExisting(rootAbs)
	if err != nil {
		return "", err
	}

	// Resolve symlinks on the deepest existing ancestor, then re-join the
	// non-existent remainder, and re-verify containment.
	resolved, err := resolveExisting(joined)
	if err != nil {
		return "", err
	}
	if !within(rootResolved, resolved) {
		return "", ErrPathEscape
	}
	return resolved, nil
}

func within(root, p string) bool {
	if p == root {
		return true
	}
	return strings.HasPrefix(p, root+string(os.PathSeparator))
}

// resolveExisting resolves symlinks on the deepest existing ancestor of p.
func resolveExisting(p string) (string, error) {
	// Walk up until an existing ancestor is found.
	cur := p
	var suffix []string
	for {
		if _, err := os.Lstat(cur); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("security: lstat %s: %w", cur, err)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			// Reached filesystem root without finding an existing ancestor.
			return p, nil
		}
		suffix = append([]string{filepath.Base(cur)}, suffix...)
		cur = parent
	}

	resolved, err := filepath.EvalSymlinks(cur)
	if err != nil {
		return "", fmt.Errorf("security: eval symlinks %s: %w", cur, err)
	}
	for _, s := range suffix {
		resolved = filepath.Join(resolved, s)
	}
	return filepath.Clean(resolved), nil
}
