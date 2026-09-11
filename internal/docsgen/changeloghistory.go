package main

// Released-changelog immutability.
//
// `e53bf2f` moved a whole `### Changed` block out of `[0.1.0]` when only the
// entry above it was misplaced, promoting 29 lines of released v0.1.0 history
// into `[Unreleased]` -- where the next release would have re-advertised them as
// new. Nothing was deleted, so a line-count or a lost-line check would not have
// seen it; the defect is that released history MOVED. goreleaser ships
// CHANGELOG.md inside every archive, so the wrong file reaches every consumer.
//
// The baseline is the NEWEST tag, not each section's own tag. That distinction
// is load-bearing: the `[0.1.0]` section was legitimately corrected between
// v0.1.0 and v0.2.0 (a stale operation count), so comparing each section to its
// own tag fails forever and would force re-introducing a false claim to satisfy
// a checker. Comparing to the newest tag ratifies corrections that shipped in a
// later release while still pinning everything since.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// changelogSections splits a changelog into heading -> body, keyed by version.
// `[Unreleased]` is deliberately absent: it is the section that is supposed to
// move.
// The heading line is part of the compared body: `## [0.3.0] - 2026-09-04`
// carries the release DATE, which ships in every archive and is as much a fact
// about the release as the entries under it. Excluding it let the date be
// rewritten silently.
//
// Fences are tracked because this is a changelog documenting changelog tooling:
// a `## [` line quoted inside a ```-block is an example, not a heading, and
// treating it as one truncates the real section and leaves everything after it
// unguarded on both sides.
func changelogSections(text string) map[string]string {
	out := map[string]string{}
	var cur string
	var body []string
	flush := func() {
		if cur != "" {
			out[cur] = strings.Join(body, "\n")
		}
	}
	inFence := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
		}
		if !inFence && strings.HasPrefix(line, "## [") {
			flush()
			cur, body = "", nil
			if m := releasedHeadingRe.FindStringSubmatch(line); m != nil {
				cur = m[1]
				body = append(body, line)
			}
			continue
		}
		if cur != "" {
			body = append(body, line)
		}
	}
	flush()
	return out
}

// newestTag returns the highest vX.Y.Z tag, or ok=false when the repository has
// none -- a shallow CI clone being the case that matters.
func newestTag(root string) (string, bool) {
	cmd := exec.Command("git", "tag", "--list", "v*.*.*", "--sort=-v:refname")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t, true
		}
	}
	return "", false
}

// runChangelogHistoryCheck compares every released section at HEAD against the
// same section at the newest tag.
func runChangelogHistoryCheck() (string, error) {
	root, err := repoRoot()
	if err != nil {
		return "", err
	}
	tag, ok := newestTag(root)
	if !ok {
		// Skipped, never silently passed. The guards run in a job whose
		// checkout has no fetch-depth, so without tags this rule verifies
		// nothing and must say so rather than print a green line.
		return "changelog-history: SKIPPED — no vX.Y.Z tag in this checkout, so released history " +
			"cannot be compared (a shallow clone needs fetch-depth: 0)", nil
	}

	headRaw, err := os.ReadFile(filepath.Join(root, changelogPath))
	if err != nil {
		return "", err
	}
	cmd := exec.Command("git", "show", tag+":"+changelogPath)
	cmd.Dir = root
	tagRaw, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("reading %s at %s: %w", changelogPath, tag, err)
	}

	head := changelogSections(string(headRaw))
	old := changelogSections(string(tagRaw))

	// Fail closed. An empty map means the file at the tag was empty, or its
	// heading format stopped matching -- either way nothing is compared, and
	// the loop below would return a confident green line having verified
	// nothing. versioncheck makes the same argument for the HEAD side.
	if len(old) == 0 {
		return "", fmt.Errorf("no released section found in %s at %s.\n"+
			"    Either the file moved or the `## [X.Y.Z]` heading format changed. This check\n"+
			"    cannot compare anything in that state and will not report success.",
			changelogPath, tag)
	}

	var problems []string
	for version, want := range old {
		got, present := head[version]
		if !present {
			problems = append(problems, fmt.Sprintf(
				"  [%s] shipped in %s and is missing at HEAD", version, tag))
			continue
		}
		if got != want {
			problems = append(problems, fmt.Sprintf(
				"  [%s] differs from its content at %s (%d lines then, %d now)",
				version, tag, len(strings.Split(want, "\n")), len(strings.Split(got, "\n"))))
		}
	}
	if len(problems) > 0 {
		return "", fmt.Errorf("released changelog history changed since %s:\n%s\n\n"+
			"A released section is immutable: goreleaser ships CHANGELOG.md in every archive, so\n"+
			"editing one rewrites what shipped, and moving entries out of one re-advertises old work\n"+
			"as new. Put new entries under [Unreleased]. A deliberate correction to released history\n"+
			"lands in a release, which moves this baseline forward.",
			tag, strings.Join(problems, "\n"))
	}
	return fmt.Sprintf("changelog-history: every released section matches its content at %s", tag), nil
}
