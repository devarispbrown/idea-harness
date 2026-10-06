package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestFailureModesDocMessagesMatchCode guards docs/failure-modes.md (plan
// item A, v0.6.1): the page quotes real error/finding text from the code, and
// this proves neither side has drifted from the other.
//
// Several of the messages are %s/%d/%q/%v-templated (ReviewNeverCompletedCause,
// the revert refusal's three slots, the secret-sweep finding's path:line), so
// the page shows a RENDERED example (real-looking ids, not literal "%s") --
// the shape the Where-this-plan-was-wrong note says to match with wildcards,
// not a rendered example, means: the check against the DOC side wildcards the
// verbs (converts the Go format string's %s/%d/%q/%v into a ".+?" regex), and
// the check against the SOURCE side is a plain, exact substring -- so a
// reworded literal in either file breaks the match.
//
// Each fragment is drawn from a single Go string literal (never spanning a
// "+"-concatenation join), copied verbatim, so a rewording of that literal in
// code -- the failure mode this test exists to catch -- makes the source-side
// assertion fail. Whitespace is normalized (runs of whitespace, including the
// newlines Markdown wraps a long message across, collapse to one space) and
// Go's `\"` escape is undone before comparing, so line-wrapping and quoting
// style are not mistaken for a changed message.
func TestFailureModesDocMessagesMatchCode(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	docBytes, err := os.ReadFile(filepath.Join(root, "docs", "failure-modes.md"))
	if err != nil {
		t.Fatal(err)
	}
	docText := normalizeMessageText(string(docBytes))

	sourceCache := map[string]string{}
	source := func(rel string) string {
		if s, ok := sourceCache[rel]; ok {
			return s
		}
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		s := normalizeMessageText(string(b))
		sourceCache[rel] = s
		return s
	}

	for _, f := range failureModesFragments {
		srcText := source(f.source)
		frag := normalizeMessageText(f.text)

		if !strings.Contains(srcText, frag) {
			t.Errorf("%s: %q no longer appears verbatim in %s.\n"+
				"docs/failure-modes.md quotes this message; either the doc is stale or this rewording is intentional, "+
				"in which case update docs/failure-modes.md (and this test's fragment) to match.",
				f.desc, f.text, f.source)
			continue
		}

		pattern, err := wildcardPattern(frag)
		if err != nil {
			t.Fatalf("%s: building a wildcard pattern from %q: %v", f.desc, f.text, err)
		}
		if !pattern.MatchString(docText) {
			t.Errorf("%s: docs/failure-modes.md no longer shows a rendered form of %q (from %s); "+
				"the message still exists in code but the doc's example text has drifted from it.",
				f.desc, f.text, f.source)
		}
	}
}

// TestFailureModesDocCommandsAreReal anchors every `idea` command and the one
// HTTP route docs/failure-modes.md names against this branch's own generated
// reference, the same anchor internal/docsgen/docslint.go uses for every other
// published doc -- so a renamed or removed verb fails here instead of only
// being caught by a reader.
func TestFailureModesDocCommandsAreReal(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	help, err := os.ReadFile(filepath.Join(root, "internal", "cli", "testdata", "help.txt"))
	if err != nil {
		t.Fatal(err)
	}
	helpText := string(help)

	for _, verb := range []string{"status", "reviews", "review", "execute", "changes", "change", "revert", "apply"} {
		re := regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(verb) + `( |\[)`)
		if !re.MatchString(helpText) {
			t.Errorf("internal/cli/testdata/help.txt no longer documents `idea %s`, which docs/failure-modes.md relies on", verb)
		}
	}

	routes, err := os.ReadFile(filepath.Join(root, "internal", "daemon", "routes.go"))
	if err != nil {
		t.Fatal(err)
	}
	const dispositionRoute = "POST /api/v1/projects/{projectId}/findings/{id}/disposition"
	if !strings.Contains(string(routes), dispositionRoute) {
		t.Errorf("internal/daemon/routes.go no longer registers %q; "+
			"docs/failure-modes.md's not_a_secret recovery (there is still no CLI verb for it) cites this route directly", dispositionRoute)
	}
}

// failureModesFragment pins one message, or one literal piece of a templated
// message, that docs/failure-modes.md quotes to the single Go source file
// that emits it.
type failureModesFragment struct {
	desc   string // what this is, for a failing test's message
	source string // path relative to the repo root
	text   string // the literal fragment, exactly as it appears in a single Go string literal (never spanning a "+" join)
}

var failureModesFragments = []failureModesFragment{
	// Class 1/6 — execute.go's shared guard: a change set left in
	// review_required with no completed review refuses a re-run of its task.
	{
		desc:   "ExecutePrecondition/Execute re-pick refusal, opening clause",
		source: "internal/workflow/execute.go",
		text:   "never finished its adversarial review (%s) and is still awaiting one;",
	},
	{
		desc:   "ExecutePrecondition/Execute re-pick refusal, way-forward clause",
		source: "internal/workflow/execute.go",
		text:   "run `idea review %s rerun` to finish reviewing it before running this task again",
	},
	// Class 1/5 — the same guard's `idea status` guidance (projection.BuildGuidance).
	{
		desc:   "BuildGuidance never-completed-review message, opening clause",
		source: "internal/projection/projection.go",
		text:   "%s never finished its adversarial review (%s), so it has no findings to approve yet.",
	},
	{
		desc:   "BuildGuidance never-completed-review message, rerun pointer",
		source: "internal/projection/projection.go",
		text:   "`idea review %s rerun` runs it.",
	},
	// The two causes ReviewNeverCompletedCause can name.
	{
		desc:   "ReviewNeverCompletedCause fallback: cancelled",
		source: "internal/domain/types.go",
		text:   "likely cancelled mid-review",
	},
	{
		desc:   "ReviewNeverCompletedCause fallback: predates review tracking",
		source: "internal/domain/types.go",
		text:   "it predates review-completion tracking from before this project's daemon was upgraded, not because anything was cancelled",
	},
	// `idea changes`/`idea status` share this exact label for the same state.
	{
		desc:   "changeSetReviewStatusLabel's label",
		source: "internal/cli/change.go",
		text:   "never finished its adversarial review",
	},
	// The approve refusal for an unreviewed change set (class 1/5's recovery).
	{
		desc:   "approve refusal, opening clause",
		source: "internal/workflow/execute.go",
		text:   "workflow: cannot approve %s: its adversarial review never completed (%s),",
	},
	{
		desc:   "approve refusal, closing clause",
		source: "internal/workflow/execute.go",
		text:   "so it carries only mechanical check observations, no adversarial findings; %s",
	},
	{
		desc:   "approve refusal way-forward when a review was genuinely attempted",
		source: "internal/workflow/execute.go",
		text:   "run `idea review %s rerun` to try again, or `idea review %s approve --accept-findings` to proceed without a completed review",
	},
	// Class 5 — the truncation chain: ErrReviewUnavailable wraps
	// wrapTruncationGuidance wraps model.truncatedError.
	{
		desc:   "ErrReviewUnavailable.Error()",
		source: "internal/workflow/execute.go",
		text:   "the adversarial review could not complete:",
	},
	{
		desc:   "wrapTruncationGuidance, review.max_tokens pointer",
		source: "internal/workflow/engine.go",
		text:   "raise `review.max_tokens` in harness.yaml to ask for a larger reply and retry this stage,",
	},
	{
		desc:   "wrapTruncationGuidance, reduce-size alternative",
		source: "internal/workflow/engine.go",
		text:   "or reduce the size of what is being sent to it",
	},
	{
		desc:   "model.truncatedError, budget clause",
		source: "internal/model/httperr.go",
		text:   "model %q hit its %d-token output budget and was cut off mid-structure,",
	},
	{
		desc:   "model.truncatedError, unsafe-to-use clause",
		source: "internal/model/httperr.go",
		text:   "so the reply is not safe to use",
	},
	// Class 2 — the secret sweep's findings and recommendation text.
	{
		desc:   "secret_sweep finding, file-content shape",
		source: "internal/workflow/execute.go",
		text:   "%s:%d: looks like %s",
	},
	{
		desc:   "secret_sweep finding, check-output shape",
		source: "internal/workflow/execute.go",
		text:   "in the output of %s: looks like %s",
	},
	{
		desc:   "secretSweepRecommendation, overridable opening",
		source: "internal/workflow/execute.go",
		text:   "remove the sensitive content and re-execute, or, if this is not really a secret,",
	},
	{
		desc:   "secretSweepRecommendation, not_a_secret suffix",
		source: "internal/workflow/execute.go",
		text:   `disposition this finding "not a secret" to allow it from now on`,
	},
	// Class 3 — a zero-file change set's "agent changed nothing" finding.
	{
		desc:   "no-op agent_output finding text",
		source: "internal/workflow/execute.go",
		text:   "The coding agent finished without changing any file.",
	},
	{
		desc:   "no-op agent_output finding recommendation",
		source: "internal/workflow/execute.go",
		text:   "Read the agent output. A 401 means the provider key is wrong; a 404 means the model id is retired. Fix it and retry, or approve the empty change set if the task was already done.",
	},
	{
		desc:   "no-op re-pick refusal, opening clause",
		source: "internal/workflow/execute.go",
		text:   "%s is a no-op change set for %s and is still %s;",
	},
	{
		desc:   "no-op re-pick refusal, closing clause",
		source: "internal/workflow/execute.go",
		text:   "reject it, or approve and apply it, before running this task again",
	},
	// Class 4 — the revert refusal.
	{
		desc:   "stale_post finding text",
		source: "internal/workflow/execute.go",
		text:   "The workspace changed after this change set was applied, so reverting it now would erase that work.",
	},
	{
		desc:   "revertRefusedMessage shape (three slots)",
		source: "internal/workflow/execute.go",
		text:   "revert refused: the workspace changed since %s was applied: %s; %s",
	},
	{
		desc:   "revertAdvice, later-change-set clause",
		source: "internal/workflow/execute.go",
		text:   "revert %s first",
	},
	{
		desc:   "revertAdvice, discard-edit clause",
		source: "internal/workflow/execute.go",
		text:   "discard (or stash) your edits to %s",
	},
	{
		desc:   "revertAdvice trailing clause",
		source: "internal/workflow/execute.go",
		text:   ", then revert again",
	},
}

// verbRe matches a printf verb this page's templated messages use.
var verbRe = regexp.MustCompile(`%[sdqv]`)

// wsRe collapses any run of whitespace -- including the newlines Markdown
// wraps a long quoted message across -- to a single space, so line-wrapping
// is never mistaken for a changed message.
var wsRe = regexp.MustCompile(`\s+`)

// normalizeMessageText undoes Go's `\"` string escape (so a source file's
// literal and the doc's real quote character compare equal) and collapses
// whitespace, for both a Go source file's raw bytes and docs/failure-modes.md's
// Markdown.
func normalizeMessageText(s string) string {
	s = strings.ReplaceAll(s, `\"`, `"`)
	s = wsRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// wildcardPattern turns one Go string literal (already whitespace-normalized)
// into a regexp that matches a RENDERED instance of it: every printf verb
// (%s, %d, %q, %v) becomes a non-greedy ".+?", and everything else is matched
// literally (regexp.QuoteMeta escapes it first, which does not touch the
// verbs themselves -- '%' and the letters s/d/q/v are not regexp
// metacharacters).
func wildcardPattern(normalizedFragment string) (*regexp.Regexp, error) {
	escaped := regexp.QuoteMeta(normalizedFragment)
	pattern := verbRe.ReplaceAllString(escaped, `.+?`)
	return regexp.Compile(pattern)
}
