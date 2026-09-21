package fsdiff

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// DefaultIgnoredDirNames are well-known cache, dependency, and toolchain
// directories that Snapshot always excludes, regardless of .gitignore or
// whether the workspace is even a git repository. A downloaded Go toolchain
// under .tools/, a node_modules/ tree, or a Python virtualenv is never part
// of a change set an agent should be judged on, and walking one is exactly
// what let a single agent's write balloon a snapshot to ~14,300 files
// (review defect #2). This list is deliberately conservative in NAME (not
// content): a project that genuinely versions a directory with one of these
// names is unusual enough that documenting the exclusion here is the right
// tradeoff over silently walking every agent-writable tree's cache dirs by
// default.
var DefaultIgnoredDirNames = map[string]bool{
	".git": true, ".hg": true, ".svn": true,
	".idea-harness": true, // the harness's own control plane (belt-and-suspenders; isControlPlane already denies it)
	"node_modules":  true,
	"vendor":        true,
	"dist":          true,
	"build":         true,
	"target":        true, // cargo, gradle, maven
	".venv":         true,
	"venv":          true,
	"__pycache__":   true,
	".pytest_cache": true,
	".mypy_cache":   true,
	".ruff_cache":   true,
	".cache":        true,
	".tools":        true, // the exact directory an ICP run's agent downloaded a Go toolchain into
	".next":         true,
	".nuxt":         true,
	".gradle":       true,
	".tox":          true,
	".terraform":    true,
}

// isDefaultIgnoredDir reports whether name (a bare directory name, not a
// path) is one of DefaultIgnoredDirNames.
func isDefaultIgnoredDir(name string) bool {
	return DefaultIgnoredDirNames[name]
}

// snapshotConfig is built from SnapshotOptions passed to Snapshot.
type snapshotConfig struct {
	workspaceRoot string
	maxFiles      int
	maxBytes      int64
}

// SnapshotOption configures Snapshot beyond its always-on defaults (control
// plane paths and DefaultIgnoredDirNames are excluded unconditionally).
type SnapshotOption func(*snapshotConfig)

// WithWorkspaceIgnore additionally excludes any path the WORKSPACE's own
// .gitignore would ignore. root (the directory Snapshot walks) is typically
// a fresh work-root copy with no .git of its own, so the ignore rules are
// read from workspaceRoot -- the canonical checkout -- and applied to root's
// workspace-relative paths, which mirror the same layout.
//
// When workspaceRoot is a git repository, `git check-ignore` is used (one
// batched subprocess call for every candidate path, not one per file --
// review defect #2 was 14,300 files). Otherwise a best-effort matcher reads
// workspaceRoot's own top-level .gitignore; it covers the common case (glob
// patterns, negation, anchoring, directory-only patterns) but not every git
// gitignore rule (e.g. per-directory nested .gitignore files), which is a
// documented limitation rather than a silent gap -- DefaultIgnoredDirNames
// and WithSanityCap still bound a non-git workspace regardless.
func WithWorkspaceIgnore(workspaceRoot string) SnapshotOption {
	return func(c *snapshotConfig) { c.workspaceRoot = workspaceRoot }
}

// WithSanityCap fails Snapshot with a *SnapshotTooLargeError once the walked
// tree exceeds maxFiles files or maxBytes of total content, whichever comes
// first (0 disables that particular bound). This is what turns "an agent
// wrote a downloaded toolchain into its work root" into a clear, immediate
// failure instead of a secret sweep with 79 false findings and a diff
// endpoint that never responds (review defect #2).
func WithSanityCap(maxFiles int, maxBytes int64) SnapshotOption {
	return func(c *snapshotConfig) { c.maxFiles = maxFiles; c.maxBytes = maxBytes }
}

// SnapshotTooLargeError is returned by Snapshot when WithSanityCap's bound is
// exceeded. It carries enough detail for a human to act on: how many files
// (or bytes), and a few example paths so "add them to .gitignore" is
// actionable rather than a guess.
type SnapshotTooLargeError struct {
	Root       string
	FileCount  int
	TotalBytes int64
	MaxFiles   int
	MaxBytes   int64
	Examples   []string
}

func (e *SnapshotTooLargeError) Error() string {
	examples := strings.Join(e.Examples, ", ")
	switch {
	case e.MaxFiles > 0 && e.FileCount > e.MaxFiles:
		return fmt.Sprintf(
			"the agent wrote %d files (over the %d-file sanity cap), e.g. %s; "+
				"add them to .gitignore, or exclude them, or raise the check sanity cap if the change is genuinely that large",
			e.FileCount, e.MaxFiles, examples)
	default:
		return fmt.Sprintf(
			"the agent wrote %s of files (over the %s sanity cap), e.g. %s; "+
				"add them to .gitignore, or exclude them, or raise the check sanity cap if the change is genuinely that large",
			formatBytes(e.TotalBytes), formatBytes(e.MaxBytes), examples)
	}
}

func formatBytes(n int64) string {
	const mib = 1 << 20
	if n >= mib {
		return fmt.Sprintf("%.1f MiB", float64(n)/mib)
	}
	const kib = 1 << 10
	if n >= kib {
		return fmt.Sprintf("%.1f KiB", float64(n)/kib)
	}
	return fmt.Sprintf("%d B", n)
}

// workspaceIgnore reports whether a workspace-relative, slash-separated path
// is ignored by the workspace's own .gitignore. It is built once per
// Snapshot call (a batched git subprocess, or a parsed .gitignore), never
// once per file.
type workspaceIgnore struct {
	// git, when non-nil, holds the set of candidate paths git check-ignore
	// reported as ignored.
	git map[string]bool
	// patterns is the fallback matcher's compiled rules, used only when git
	// is unavailable (non-git workspace).
	patterns []ignorePattern
}

// loadWorkspaceIgnore prepares the matcher for workspaceRoot. candidates is
// every workspace-relative path Snapshot is considering (already filtered by
// DefaultIgnoredDirNames); when workspaceRoot is a git repository they are
// resolved in one batched `git check-ignore` call.
func loadWorkspaceIgnore(workspaceRoot string, candidates []string) (*workspaceIgnore, error) {
	if workspaceRoot == "" {
		return &workspaceIgnore{}, nil
	}
	if err := exec.Command("git", "-C", workspaceRoot, "rev-parse", "--is-inside-work-tree").Run(); err == nil {
		ignored, err := gitCheckIgnore(workspaceRoot, candidates)
		if err != nil {
			// Fail open on a git-invocation problem: this is an additional
			// exclusion on top of DefaultIgnoredDirNames and the sanity cap,
			// not the only line of defense, so a broken `git` must not turn
			// into a hard failure of every execute.
			return &workspaceIgnore{}, nil //nolint:nilerr
		}
		return &workspaceIgnore{git: ignored}, nil
	}
	patterns, err := parseGitignore(filepath.Join(workspaceRoot, ".gitignore"))
	if err != nil {
		return &workspaceIgnore{}, nil //nolint:nilerr // best-effort fallback
	}
	return &workspaceIgnore{patterns: patterns}, nil
}

// Ignore reports whether rel (workspace-relative, slash-separated) is
// ignored.
func (w *workspaceIgnore) Ignore(rel string) bool {
	if w == nil {
		return false
	}
	if w.git != nil {
		return w.git[rel]
	}
	for _, p := range w.patterns {
		if p.match(rel) {
			return !p.negate
		}
	}
	return false
}

// gitCheckIgnore batches every candidate through one `git check-ignore`
// invocation (review defect #2: the reported failure mode was 14,300 files,
// and a per-file subprocess would itself have been the next hang).
func gitCheckIgnore(workspaceRoot string, candidates []string) (map[string]bool, error) {
	ignored := map[string]bool{}
	if len(candidates) == 0 {
		return ignored, nil
	}
	var stdin bytes.Buffer
	for _, c := range candidates {
		stdin.WriteString(c)
		stdin.WriteByte(0)
	}
	// No --no-index: git check-ignore's default behavior is exactly what is
	// wanted here -- "tracked files are not shown at all since they are not
	// subject to exclude rules" (git-check-ignore(1)). --no-index turns that
	// off, so a file force-added (or added before its pattern existed) --
	// tracked in the workspace, and therefore copied into the before-snapshot
	// by seedWorkRoot's `git ls-files -co --exclude-standard` (-c always
	// includes tracked files) -- was reported ignored here and excluded from
	// the after-snapshot. fsdiff.Diff then read the asymmetry as a deletion:
	// a change set the agent never touched deleted the user's tracked file,
	// and with git_commit on, committed the deletion (C1). Dropping
	// --no-index still catches a genuinely untracked-and-ignored path (e.g.
	// a downloaded toolchain under .tools/ -- TestSnapshotAlwaysExcludesDefaultIgnoredDirs
	// and TestWorkspaceIgnoreExcludesTrackedFile below), since an untracked
	// path has nothing in the index to exempt it.
	cmd := exec.Command("git", "-C", workspaceRoot, "check-ignore", "-z", "--stdin")
	cmd.Stdin = &stdin
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			// Exit 1 means "none of the paths are ignored" -- not a failure.
			if ee.ExitCode() == 1 {
				return ignored, nil
			}
		} else {
			return nil, err
		}
	}
	for _, p := range bytes.Split(out, []byte{0}) {
		if s := string(p); s != "" {
			ignored[s] = true
		}
	}
	return ignored, nil
}

// ignorePattern is one compiled line of a .gitignore, for the non-git
// fallback matcher.
type ignorePattern struct {
	re       *regexp.Regexp
	dirOnly  bool
	negate   bool
	anchored bool
}

func (p ignorePattern) match(rel string) bool {
	return p.re.MatchString(rel)
}

// parseGitignore reads a single .gitignore file and compiles its patterns.
// It supports comments, blank lines, negation (!), directory-only patterns
// (trailing /), anchored patterns (leading /), and glob wildcards including
// "**". It does not walk nested .gitignore files -- a workspace without git
// available gets this one best-effort pass, not full git semantics; see the
// WithWorkspaceIgnore doc comment.
func parseGitignore(gitignorePath string) ([]ignorePattern, error) {
	data, err := os.ReadFile(gitignorePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var patterns []ignorePattern
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		p, ok := compileGitignoreLine(trimmed)
		if ok {
			patterns = append(patterns, p)
		}
	}
	return patterns, nil
}

func compileGitignoreLine(pattern string) (ignorePattern, bool) {
	negate := strings.HasPrefix(pattern, "!")
	if negate {
		pattern = pattern[1:]
	}
	dirOnly := strings.HasSuffix(pattern, "/")
	pattern = strings.TrimSuffix(pattern, "/")
	if pattern == "" {
		return ignorePattern{}, false
	}
	anchored := strings.HasPrefix(pattern, "/")
	pattern = strings.TrimPrefix(pattern, "/")

	reSrc := globToRegexp(pattern)
	if !anchored && !strings.Contains(pattern, "/") {
		// Unanchored, single-segment patterns match at any depth.
		reSrc = "(^|.*/)" + reSrc
	} else {
		reSrc = "^" + reSrc
	}
	if dirOnly {
		reSrc += "(/.*)?$"
	} else {
		reSrc += "(/.*)?$"
	}
	re, err := regexp.Compile(reSrc)
	if err != nil {
		return ignorePattern{}, false
	}
	return ignorePattern{re: re, dirOnly: dirOnly, negate: negate, anchored: anchored}, true
}

// globToRegexp translates a gitignore glob (*, ?, **, character classes) to
// a regexp source fragment (unanchored; the caller anchors it).
func globToRegexp(pat string) string {
	var b strings.Builder
	segs := strings.Split(pat, "/")
	for i, seg := range segs {
		if i > 0 {
			b.WriteString("/")
		}
		if seg == "**" {
			b.WriteString(".*")
			continue
		}
		for j := 0; j < len(seg); j++ {
			switch c := seg[j]; c {
			case '*':
				b.WriteString("[^/]*")
			case '?':
				b.WriteString("[^/]")
			case '.', '+', '(', ')', '|', '^', '$', '\\':
				b.WriteByte('\\')
				b.WriteByte(c)
			case '[':
				// Pass a character class through mostly verbatim.
				k := j
				for k < len(seg) && seg[k] != ']' {
					k++
				}
				if k < len(seg) {
					b.WriteString(seg[j : k+1])
					j = k
				} else {
					b.WriteString("\\[")
				}
			default:
				b.WriteByte(c)
			}
		}
	}
	return b.String()
}

// snapshotCandidate is one file Snapshot found before workspace-ignore
// filtering, kept just long enough to build the ignore matcher and the
// sanity-cap error's example list.
type snapshotCandidate struct {
	rel  string
	path string
	size int64
}

// exampleList returns up to n example paths for a SnapshotTooLargeError.
func exampleList(cands []snapshotCandidate, n int) []string {
	if n > len(cands) {
		n = len(cands)
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, cands[i].rel)
	}
	return out
}
