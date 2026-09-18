package workflow

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// The agent's work root is a copy of the workspace, made fresh for every
// attempt.
//
// It used to start empty. The agent never saw the user's files, so asked to
// add a route to server.js it wrote a new server.js from nothing; the diff
// called the file "added", review passed it, and apply overwrote the real one,
// deleting the routes that were there. Fresh per attempt also means an earlier
// attempt's leftovers are never mistaken for the base of the next one.
//
// The copy goes into a container with network egress, so what is NOT copied
// matters as much as what is: environment files, the harness's own control
// plane, and any file the credential check flags. Everything skipped is
// reported on the change set rather than logged, because a build that fails
// for a missing file must be able to say why.

const (
	seedMaxFileBytes  = 1 << 20   // a file larger than this is skipped
	seedMaxTotalBytes = 200 << 20 // the copy stops with an error past this
)

// seedSkip is one path left out of the work root, and why.
type seedSkip struct {
	Path   string
	Reason string
}

// Reasons a path is not copied.
const (
	skipEnvFile   = "environment file"
	skipSecret    = "looks like it contains a credential"
	skipSymlink   = "symbolic link"
	skipOversized = "larger than 1 MiB"
	skipSubmodule = "git submodule"
	skipSpecial   = "not a regular file"
)

// seedDirsOutsideGit are skipped when the workspace is not a git repository and
// there is no ignore file to say what is generated.
var seedDirsOutsideGit = map[string]bool{
	"node_modules": true, "vendor": true, "dist": true, "build": true, "target": true,
}

// seedWorkRoot replaces workRoot with a copy of workspace. isSecret reports
// whether file content carries a credential. It returns what it skipped.
func seedWorkRoot(ctx context.Context, workspace, workRoot string, isSecret func(string) bool) ([]seedSkip, error) {
	if err := os.RemoveAll(workRoot); err != nil {
		return nil, fmt.Errorf("workflow: reset work root: %w", err)
	}
	if err := os.MkdirAll(workRoot, 0o755); err != nil {
		return nil, fmt.Errorf("workflow: create work root: %w", err)
	}
	if workspace == "" {
		return nil, nil
	}
	paths, err := workspaceFiles(ctx, workspace)
	if err != nil {
		return nil, err
	}

	var skipped []seedSkip
	var total int64
	for _, rel := range paths {
		if seedExcluded(rel) {
			continue
		}
		if isEnvFile(rel) {
			skipped = append(skipped, seedSkip{rel, skipEnvFile})
			continue
		}
		src := filepath.Join(workspace, filepath.FromSlash(rel))
		fi, err := os.Lstat(src)
		switch {
		case os.IsNotExist(err):
			continue // listed by git but deleted in the working tree
		case err != nil:
			return nil, fmt.Errorf("workflow: seed %s: %w", rel, err)
		case fi.Mode()&os.ModeSymlink != 0:
			skipped = append(skipped, seedSkip{rel, skipSymlink})
			continue
		case fi.IsDir():
			skipped = append(skipped, seedSkip{rel, skipSubmodule})
			continue
		case !fi.Mode().IsRegular():
			skipped = append(skipped, seedSkip{rel, skipSpecial})
			continue
		case fi.Size() > seedMaxFileBytes:
			skipped = append(skipped, seedSkip{rel, skipOversized})
			continue
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return nil, fmt.Errorf("workflow: seed %s: %w", rel, err)
		}
		if isSecret != nil && isSecret(string(data)) {
			skipped = append(skipped, seedSkip{rel, skipSecret})
			continue
		}
		total += int64(len(data))
		if total > seedMaxTotalBytes {
			return nil, fmt.Errorf("workflow: the project is larger than %d MiB of source files, the most the coding agent is given; "+
				"add generated or vendored directories to .gitignore", seedMaxTotalBytes>>20)
		}
		dest := filepath.Join(workRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return nil, fmt.Errorf("workflow: seed %s: %w", rel, err)
		}
		if err := os.WriteFile(dest, data, fi.Mode().Perm()|0o600); err != nil {
			return nil, fmt.Errorf("workflow: seed %s: %w", rel, err)
		}
	}
	return skipped, nil
}

// workspaceFiles lists the workspace's files, slash-separated and relative to
// it. In a git work tree that is what git would consider part of the project:
// tracked files and untracked files that are not ignored, scoped to the
// workspace directory rather than the repository's top level.
func workspaceFiles(ctx context.Context, workspace string) ([]string, error) {
	if err := exec.CommandContext(ctx, "git", "-C", workspace, "rev-parse", "--is-inside-work-tree").Run(); err == nil {
		out, err := exec.CommandContext(ctx, "git", "-C", workspace, "ls-files", "-co", "--exclude-standard", "-z").Output()
		if err != nil {
			return nil, fmt.Errorf("workflow: list project files: %w", err)
		}
		var paths []string
		seen := map[string]bool{}
		for _, p := range bytes.Split(out, []byte{0}) {
			if s := string(p); s != "" && !seen[s] {
				seen[s] = true
				paths = append(paths, s)
			}
		}
		sort.Strings(paths)
		return paths, nil
	}
	var paths []string
	err := filepath.WalkDir(workspace, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(workspace, p)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") || seedDirsOutsideGit[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("workflow: list project files: %w", err)
	}
	return paths, nil
}

// seedExcluded is the harness's own control plane, never given to the agent
// and never worth reporting as skipped.
func seedExcluded(rel string) bool {
	first := strings.ToLower(strings.SplitN(path.Clean(rel), "/", 2)[0])
	return first == ".git" || first == ".idea-harness"
}

// isEnvFile matches .env and its variants (.env.local, .env.production) at any
// depth, which hold secrets by convention whether or not they are tracked.
func isEnvFile(rel string) bool {
	base := strings.ToLower(path.Base(rel))
	return base == ".env" || strings.HasPrefix(base, ".env.")
}

// seedSkipSummary renders skipped paths for a finding, grouped by reason.
func seedSkipSummary(skipped []seedSkip) string {
	by := map[string][]string{}
	var reasons []string
	for _, s := range skipped {
		if _, ok := by[s.Reason]; !ok {
			reasons = append(reasons, s.Reason)
		}
		by[s.Reason] = append(by[s.Reason], s.Path)
	}
	sort.Strings(reasons)
	var b strings.Builder
	for _, r := range reasons {
		fmt.Fprintf(&b, "%s: %s\n", r, strings.Join(by[r], ", "))
	}
	return strings.TrimSpace(b.String())
}
