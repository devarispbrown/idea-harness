package main

import (
	"strings"
	"testing"
)

func TestChangelogSections(t *testing.T) {
	const cl = `# Changelog

## [Unreleased]

### Added
- something new

## [0.2.0] - 2026-09-04

### Fixed
- a fix

## [v0.1.0] - 2026-09-03

### Changed
- the original
`
	got := changelogSections(cl)

	if _, ok := got["Unreleased"]; ok {
		t.Error("[Unreleased] must not be tracked — it is the section that is supposed to move")
	}
	if len(got) != 2 {
		t.Fatalf("got %d released sections, want 2: %v", len(got), keysOf(got))
	}
	if !strings.Contains(got["0.2.0"], "a fix") {
		t.Errorf("[0.2.0] body wrong: %q", got["0.2.0"])
	}
	// A heading renamed to `## [v0.5.0]` is the documented bypass of a
	// bare-number pattern; versioncheck's regex tolerates the prefix and this
	// rule reuses it rather than declaring a second, weaker one.
	if !strings.Contains(got["0.1.0"], "the original") {
		t.Errorf("a `v`-prefixed heading was not recognised, so its section would be unguarded: %v", keysOf(got))
	}
	// A section must not absorb the one after it.
	if strings.Contains(got["0.2.0"], "the original") {
		t.Error("[0.2.0] swallowed the next section, so a move between them would be invisible")
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestChangelogSectionsIgnoresFencedHeadings(t *testing.T) {
	const cl = "# Changelog\n\n" +
		"## [0.3.0] - 2026-09-04\n" +
		"- entry A\n" +
		"```text\n" +
		"## [Unreleased]\n" +
		"```\n" +
		"- entry B after the fence\n"
	got := changelogSections(cl)
	if !strings.Contains(got["0.3.0"], "entry B after the fence") {
		t.Errorf("a `## [` line inside a fence truncated the section, leaving everything "+
			"after it unguarded on both sides:\n%q", got["0.3.0"])
	}
}

func TestChangelogSectionsComparesTheReleaseDate(t *testing.T) {
	a := changelogSections("## [0.3.0] - 2026-09-04\n- same entry\n")
	b := changelogSections("## [0.3.0] - 2019-01-01\n- same entry\n")
	if a["0.3.0"] == b["0.3.0"] {
		t.Error("a rewritten release date compares equal; the date ships in every archive")
	}
}
