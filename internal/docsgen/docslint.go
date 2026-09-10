package main

// Static linting of the `idea …` commands in the prose documentation.
//
// The third DX audit found `idea trace task T-3` on the README's front page.
// Task codes are TASK-<n>, so it could not resolve. `scripts/smoke.sh` had said
// `idea why task TASK-1` all along; nothing compared the two, and every existing
// guard -- route sets, request-body fields, 409 declarations -- was green.
//
// The plan's first draft proposed running every documented command against a
// live daemon. Review showed that would not have caught the finding it was
// justified by (`idea new` is fire-and-forget and exits 0 while the daemon fails
// later), needed fifteen skips out of forty-eight commands, and would have put
// CI markers in the README's front page.
//
// So this is static and narrow. It proves two things a reader depends on:
// the verb exists, and any TYPE-n reference uses a prefix the domain emits. It
// does not prove the command succeeds -- scripts/smoke.sh is where that lives.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/model"
)

// lintedDocs returns every published document whose commands a reader will
// copy: README plus docs/*.md.
//
// It used to be README and the quickstart alone, while the changelog entry
// announcing this guard said "across every Markdown file". Nine of the eleven
// published documents were unchecked, docs/cli.md and docs/api.md among them,
// and a planted `idea trace task T-3` in either passed.
//
// docs/plans/ is deliberately excluded: those are historical records of what
// was true at the time, and linting them would force the rewriting of history
// to satisfy a checker.
//
// RELEASING.md and CONTRIBUTING.md are in the set because they are read at the
// moments that matter -- tagging a release and opening a PR -- and were covered
// by nothing. RELEASING.md sent a maintainer to `docs/versioning.md` for the
// version-classification rules through four releases; that file has never
// existed in any commit.
func lintedDocs(root string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	if err != nil {
		return nil, err
	}
	out := []string{"README.md", "RELEASING.md", "CONTRIBUTING.md"}
	for _, m := range matches {
		rel, err := filepath.Rel(root, m)
		if err != nil {
			return nil, err
		}
		out = append(out, rel)
	}
	sort.Strings(out)
	return out, nil
}

var (
	// blockquoteRe strips a leading "> " so fenced blocks inside a blockquote are
	// seen. docs/quickstart.md has four of them, and without this the checker
	// reported clean on `idea trace task T-3` planted inside one -- the literal
	// defect it exists to catch, invisible in the same round that built it.
	blockquoteRe = regexp.MustCompile(`^\s*>\s?`)
	fenceRe      = regexp.MustCompile("^\\s*(?:```|~~~)")
	// inlineCodeRe pulls `...` spans out of prose. A front-page command is
	// usually written inline, not fenced -- README and the quickstart alone
	// carried 66 inline invocations that this check could not see, against 55
	// fenced ones that it could.
	inlineCodeRe = regexp.MustCompile("`([^`]+)`")
	ideaCmdRe    = regexp.MustCompile(`(?:^|[;&|]\s*)(?:\$ )?idea\s+([a-z-]+)`)
	// A reference like REQ-1 or TASK-12, as the docs write them.
	//
	// One letter is enough: the defect this check exists for was `idea trace
	// task T-3` on the README's front page, and a two-letter minimum missed it
	// entirely -- caught by mutation-testing this checker against the very
	// finding that motivated it.
	codeRefRe = regexp.MustCompile(`\b([A-Z][A-Z_]{0,15})-(\d+)\b`)
)

// docRefRe matches a documentation cross-reference: a markdown link target or
// an inline code span naming a .md file. Scoped to Markdown deliberately --
// prose also names runtime paths that do not exist in the repo
// (`.idea-harness/harness.yaml`, `~/.local/share/idea-harness/`), and a rule
// broad enough to include those would be noise on a correct tree.
var docRefRe = regexp.MustCompile(`\]\(([^)#\s:]+)(?:#[^)]*)?\)|` + "`" + `([A-Za-z0-9_./-]+\.md)` + "`")

// checkDocRefs reports documentation cross-references whose target does not
// exist. RELEASING.md pointed at `docs/versioning.md` for the
// version-classification rules through four releases; the file has never
// existed in any commit, and no guard read RELEASING.md at all.
func checkDocRefs(root, rel string, lines []string) []string {
	var problems []string
	for i, line := range lines {
		for _, m := range docRefRe.FindAllStringSubmatch(line, -1) {
			target := m[1]
			if target == "" {
				target = m[2]
			}
			if target == "" || strings.Contains(target, "://") {
				continue
			}
			isLink := m[1] != ""
			// Relative to the referring document, as a reader would follow it.
			if _, err := os.Stat(filepath.Join(root, filepath.Dir(rel), target)); err == nil {
				continue
			}
			// A backticked name is prose and may be meant from the repo root;
			// `](target)` is not -- GitHub resolves it against the document's
			// own directory, so a root fallback there turns a link that 404s
			// for a reader into a pass.
			if !isLink {
				if _, err := os.Stat(filepath.Join(root, target)); err == nil {
					continue
				}
			}
			problems = append(problems, fmt.Sprintf(
				"  %s:%d references %s, which does not exist", rel, i+1, target))
		}
	}
	return problems
}

// runDocsLint checks every documented `idea` invocation.
func runDocsLint() error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	verbs, err := cliVerbs()
	if err != nil {
		return err
	}
	prefixes := domainCodePrefixes(root)

	docs, err := lintedDocs(root)
	if err != nil {
		return err
	}
	var problems []string
	for _, rel := range docs {
		lines, err := readLines(filepath.Join(root, rel))
		if err != nil {
			return err
		}
		problems = append(problems, checkDocRefs(root, rel, lines)...)
		problems = append(problems, checkModelSpecs(model.DefaultModels, rel, lines)...)
		inFence := false
		for i, raw := range lines {
			line := blockquoteRe.ReplaceAllString(raw, "")
			if fenceRe.MatchString(line) {
				inFence = !inFence
				continue
			}
			if !inFence {
				// Outside a fence only the code spans are commands; ordinary
				// prose saying "idea generates a plan" is not an invocation.
				//
				// Each span is scanned on its own. Concatenating them let a
				// command run past its own closing backtick: "`idea` command ...
				// `go install`" joined to "idea\ngo install", and \s in the
				// invocation pattern crosses a newline, so the checker invented
				// `idea go`.
				var spans []string
				for _, m := range inlineCodeRe.FindAllStringSubmatch(line, -1) {
					spans = append(spans, m[1])
				}
				if len(spans) == 0 {
					continue
				}
				checkLine(rel, i+1, spans, verbs, prefixes, &problems)
				continue
			}
			// Every invocation on the line, not just the first: commands chained
			// with && or ; would otherwise have their later verbs unchecked.
			checkLine(rel, i+1, []string{line}, verbs, prefixes, &problems)
		}
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("documented commands that cannot work:\n%s\n\n"+
		"These are copied by readers verbatim. This check proves the verb exists and the\n"+
		"reference prefix is one the domain emits; it does not prove the command succeeds --\n"+
		"scripts/smoke.sh covers that.", strings.Join(problems, "\n"))
}

// cliVerbs returns every dispatchable command name, from the generated CLI
// reference — the same source docs/cli.md is built from.
func cliVerbs() (map[string]bool, error) {
	root, err := repoRoot()
	if err != nil {
		return nil, err
	}
	lines, err := readLines(filepath.Join(root, "internal", "cli", "cli.go"))
	if err != nil {
		return nil, err
	}
	nameRe := regexp.MustCompile(`\{names: \[\]string\{([^}]*)\}`)
	quoted := regexp.MustCompile(`"([^"]+)"`)
	out := map[string]bool{}
	for _, line := range lines {
		m := nameRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		for _, q := range quoted.FindAllStringSubmatch(m[1], -1) {
			out[q[1]] = true
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("docs-lint: found no commands in the CLI table; the parser is broken, not the docs")
	}
	return out, nil
}

// domainCodePrefixes collects the human-id prefixes the code actually emits.
//
// Two call shapes produce them: id.Code("TASK", n) directly, and
// nextCode(tx, table, column, "REQ", …) which allocates the next per-initiative
// sequence. A first version of this checker only knew the first, and reported
// every REQ- and CHG- reference in the docs as unresolvable -- a false positive
// that would have had someone "fix" correct documentation.
func domainCodePrefixes(root string) map[string]bool {
	out := map[string]bool{}
	callRe := regexp.MustCompile(`(?:id\.Code|nextCode)\([^)]*?"([A-Z][A-Z_]{1,15})"`)
	_ = filepath.Walk(filepath.Join(root, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, m := range callRe.FindAllStringSubmatch(string(data), -1) {
			out[m[1]] = true
		}
		return nil
	})
	return out
}

func readLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return strings.Split(string(data), "\n"), nil
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// checkLine reports unknown verbs and unresolvable references in each fragment.
// Fragments are scanned separately so a pattern cannot run from one inline code
// span into the next.
func checkLine(rel string, lineNo int, fragments []string, verbs, prefixes map[string]bool, problems *[]string) {
	where := fmt.Sprintf("%s:%d", rel, lineNo)
	for _, frag := range fragments {
		// Every invocation in the fragment, not just the first: commands chained
		// with && or ; would otherwise have their later verbs unchecked.
		for _, m := range ideaCmdRe.FindAllStringSubmatch(frag, -1) {
			if verb := m[1]; !verbs[verb] {
				*problems = append(*problems, fmt.Sprintf(
					"  %s: `idea %s` — no such command", where, verb))
			}
		}
		for _, ref := range codeRefRe.FindAllStringSubmatch(frag, -1) {
			if prefix := ref[1]; len(prefixes) > 0 && !prefixes[prefix] {
				*problems = append(*problems, fmt.Sprintf(
					"  %s: reference %q uses prefix %q, which nothing emits (known: %s)",
					where, ref[0], prefix, strings.Join(sortedSet(prefixes), ", ")))
			}
		}
	}
}

// modelSpecRe matches a provider-qualified model id in prose or a code span.
//
// The lookbehind-equivalent character class is load-bearing: `/` and `.` must
// not precede the provider, or the checker matches `anthropic/claude-opus-5`
// inside `openrouter/anthropic/claude-opus-5` and reports an OpenRouter id as a
// broken Anthropic one, and matches `openai/...` inside a base-URL like
// `https://api.openai.com/v1`.
// openRouterIDRe captures the model half of an `openrouter/<provider>/<model>`
// id, which is what docs/configuration.md then quotes on its own.
var openRouterIDRe = regexp.MustCompile(`openrouter/([A-Za-z0-9._-]+/[A-Za-z0-9._-]+)`)

var modelSpecRe = regexp.MustCompile(`(^|[^A-Za-z0-9._/-])(openai|anthropic|gemini|deepseek)/([A-Za-z0-9._-]+)`)

// checkModelSpecs reports a documented `provider/model` id that the bundled
// catalog does not know, which is what makes `idea model set <that id>` warn.
//
// This exists because audit 7 removed nine non-resolving ids from the docs and
// changed no code, so internal/model/catalog.go went on blessing the exact ids
// the docs had just deleted -- while `openai/gpt-5.6-luna`, the id on the
// README's front page and the one this project actually runs on, was absent
// from the catalog and warned. A documentation-only fix passed three audits
// because nothing compared the two.
//
// openrouter and ollama are skipped for the reason config.UnknownModelProblems
// already gives: their ids are arbitrary upstream strings and whatever the
// operator pulled locally, so the bundled catalog cannot be authoritative.
//
// OpenRouter ids contain a slash, so their model half looks exactly like a
// provider-qualified id: docs/configuration.md explains the split by quoting
// `anthropic/claude-opus-5` on its own, and that is an OpenRouter id, not an
// Anthropic one. The exemption is therefore the set of openrouter model halves
// named in the same file, not "any line mentioning openrouter" -- a line-level
// skip exempted seven live lines, and a genuinely broken id on any of them
// would have passed.
func checkModelSpecs(known map[string][]string, rel string, lines []string) []string {
	// Model halves this file documents as OpenRouter ids.
	orTails := map[string]bool{}
	for _, line := range lines {
		for _, m := range openRouterIDRe.FindAllStringSubmatch(line, -1) {
			orTails[m[1]] = true
		}
	}
	var problems []string
	for i, line := range lines {
		for _, m := range modelSpecRe.FindAllStringSubmatch(line, -1) {
			provider, name := m[2], m[3]
			models, ok := known[provider]
			if !ok {
				continue
			}
			if orTails[provider+"/"+name] {
				continue
			}
			found := false
			for _, k := range models {
				if k == name {
					found = true
					break
				}
			}
			if !found {
				problems = append(problems, fmt.Sprintf(
					"  %s:%d documents %s/%s, which internal/model/catalog.go does not list,\n"+
						"    so `idea model set %s/%s` warns. Add it to the catalog or correct the document",
					rel, i+1, provider, name, provider, name))
			}
		}
	}
	return problems
}
