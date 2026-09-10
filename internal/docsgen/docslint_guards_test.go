package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These encode the mutations that were run by hand when the two docs-lint
// reference rules landed. A guard whose only proof of non-vacuity is a
// transcript stops being proven the moment someone edits it.

func TestCheckDocRefs(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "docs", "real.md"), "x")
	mustWrite(t, filepath.Join(root, "scripts", "real.sh"), "x")
	mustWrite(t, filepath.Join(root, "README.md"), "x")

	for _, tc := range []struct {
		name  string
		rel   string
		line  string
		wantN int
		why   string
	}{
		{"backticked md that exists", "docs/a.md", "see `real.md` for more", 0,
			"resolved against the referring document's directory"},
		{"backticked md that does not exist", "docs/a.md", "`docs/versioning.md` has the rules", 1,
			"the exact defect: RELEASING.md pointed at a file that never existed"},
		{"link to an existing sibling", "docs/a.md", "see [real](real.md)", 0, ""},
		{"link to a missing sibling", "docs/a.md", "see [gone](tuning.md)", 1, ""},
		{"link to a non-md file that exists", "docs/a.md", "see [s](../scripts/real.sh)", 0,
			"link targets are checked whatever the extension"},
		{"link to a missing non-md file", "docs/a.md", "see [s](../scripts/nope.sh)", 1,
			"a renamed script is the same stale-reference class"},
		{"link resolving only from the root is still broken", "docs/a.md", "see [r](README.md)", 1,
			"GitHub resolves a link against the document's own directory, so this 404s"},
		{"backticked name may be meant from the root", "docs/a.md", "`README.md` explains", 0,
			"prose naming a repo file is not a link"},
		{"external url is not a path", "docs/a.md", "see [x](https://example.com/a.md)", 0, ""},
		{"anchor is stripped", "docs/a.md", "see [r](real.md#install)", 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := checkDocRefs(root, tc.rel, []string{tc.line})
			if len(got) != tc.wantN {
				t.Errorf("got %d problem(s), want %d (%s)\n  line: %s\n  %v",
					len(got), tc.wantN, tc.why, tc.line, got)
			}
		})
	}
}

func TestCheckModelSpecs(t *testing.T) {
	known := map[string][]string{
		"openai":    {"gpt-4o", "gpt-5.6-luna"},
		"anthropic": {"claude-sonnet-4-5-20250929"},
	}
	for _, tc := range []struct {
		name  string
		lines []string
		wantN int
		why   string
	}{
		{"a catalogued id", []string{"idea model set openai/gpt-5.6-luna"}, 0, ""},
		{"an uncatalogued id", []string{"idea model set openai/gpt-9-imaginary"}, 1,
			"this is what a documentation-only model fix looks like"},
		{"an uncatalogued anthropic id", []string{"`anthropic/claude-3-7-sonnet-latest`"}, 1, ""},
		{"an unknown provider is not ours to judge", []string{"groq/llama-3.3-70b"}, 0,
			"UnknownModelProblems returns nil for a provider with no catalog"},
		{"openrouter is skipped", []string{"openrouter/anthropic/claude-opus-5"}, 0,
			"openrouter ids are arbitrary upstream strings"},
		{"an openrouter tail quoted alone in the same file", []string{
			"`openrouter/anthropic/claude-opus-5` resolves to the",
			"`openrouter` provider with model `anthropic/claude-opus-5`.",
		}, 0, "the model half of an openrouter id is not an anthropic id"},
		{"a broken id on a line that merely says openrouter", []string{
			"Use `anthropic/claude-totally-bogus-9` unless you are on openrouter.",
		}, 1, "a line-level skip exempted seven live lines; the exemption is per-id, not per-line"},
		{"a base url is not a model spec", []string{"base_url: https://api.openai.com/v1"}, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := checkModelSpecs(known, "docs/x.md", tc.lines)
			if len(got) != tc.wantN {
				t.Errorf("got %d problem(s), want %d (%s)\n  %v", len(got), tc.wantN, tc.why, got)
			}
		})
	}
}

func TestStrictDecodeCensusMatchesTheTree(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	got, err := strictDecodeCallers(filepath.Join(root, "internal", "daemon"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, h := range got {
		seen[h] = true
		if !strictDecodeHandlers[h] {
			t.Errorf("%s calls decodeStrict but is not in strictDecodeHandlers; "+
				"closing a body is a breaking change and must be documented", h)
		}
	}
	for h := range strictDecodeHandlers {
		if !seen[h] {
			t.Errorf("strictDecodeHandlers lists %s, which no longer calls decodeStrict", h)
		}
	}
}

func TestStrictDecodeCallersFindsACallAndIgnoresAMention(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "x.go"), `package x

// decodeStrict is mentioned here but not called.
func mentionsOnly() {}

func callsIt(r int) { _ = decodeStrict(r, nil) }
`)
	got, err := strictDecodeCallers(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "callsIt" {
		t.Fatalf("got %v, want [callsIt] — a comment naming decodeStrict is not a call site, "+
			"which is why this is an AST walk and not a grep", got)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = strings.TrimSpace(body)
}
