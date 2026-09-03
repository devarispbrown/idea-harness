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
)

// docsLinted are the prose files whose commands a reader will copy.
var docsLinted = []string{"README.md", filepath.Join("docs", "quickstart.md")}

var (
	fenceRe   = regexp.MustCompile("^\\s*```")
	ideaCmdRe = regexp.MustCompile(`^\s*(?:\$ )?idea\s+([a-z-]+)((?:\s+\S+)*)`)
	// A reference like REQ-1 or TASK-12, as the docs write them.
	//
	// One letter is enough: the defect this check exists for was `idea trace
	// task T-3` on the README's front page, and a two-letter minimum missed it
	// entirely -- caught by mutation-testing this checker against the very
	// finding that motivated it.
	codeRefRe = regexp.MustCompile(`\b([A-Z][A-Z_]{0,15})-(\d+)\b`)
)

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

	var problems []string
	for _, rel := range docsLinted {
		lines, err := readLines(filepath.Join(root, rel))
		if err != nil {
			return err
		}
		inFence := false
		for i, line := range lines {
			if fenceRe.MatchString(line) {
				inFence = !inFence
				continue
			}
			if !inFence {
				continue
			}
			m := ideaCmdRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			where := fmt.Sprintf("%s:%d", rel, i+1)
			if verb := m[1]; !verbs[verb] {
				problems = append(problems, fmt.Sprintf(
					"  %s: `idea %s` — no such command", where, verb))
			}
			for _, ref := range codeRefRe.FindAllStringSubmatch(m[2], -1) {
				if prefix := ref[1]; len(prefixes) > 0 && !prefixes[prefix] {
					problems = append(problems, fmt.Sprintf(
						"  %s: reference %q uses prefix %q, which nothing emits (known: %s)",
						where, ref[0], prefix, strings.Join(sortedSet(prefixes), ", ")))
				}
			}
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
