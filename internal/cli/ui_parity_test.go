package cli

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// uiParityPath is the CLI/UI parity ledger: one row per CLI verb naming the
// web UI surface that does the same thing, or why there is none.
const uiParityPath = "../../docs/ui-parity.md"

// cliVerbs derives the verb set from the command table -- the same rows
// `idea help --format=md` prints, so a verb added there is a verb this test
// asks about. A verb is the command word plus each following word that names
// one of the parent command's subcommands (`model set`, `daemon status|stop`);
// the first placeholder, flag or non-subcommand word ends it. A row that only
// lists alternatives which each have a row of their own (`objective
// add|edit|link`) is an index, not a verb.
func cliVerbs() map[string]bool {
	cmds := commands()
	subsOf := map[string]map[string]flagSpec{}
	for _, c := range cmds {
		if len(c.names) > 0 {
			subsOf[c.names[0]] = c.subs
		}
	}
	verbs := map[string]bool{}
	for _, c := range cmds {
		words := strings.Fields(c.usage)
		if len(words) == 0 {
			continue
		}
		verb := words[0]
		subs := subsOf[words[0]]
		for _, w := range words[1:] {
			isSub := subs != nil
			for _, alt := range strings.Split(w, "|") {
				if _, ok := subs[alt]; !ok {
					isSub = false
				}
			}
			if !isSub {
				break
			}
			verb += " " + w
		}
		verbs[verb] = true
	}
	// Drop the index rows: every alternative already has its own verb.
	for v := range verbs {
		head, tail, ok := strings.Cut(v, " ")
		if !ok || !strings.Contains(tail, "|") {
			continue
		}
		covered := true
		for _, alt := range strings.Split(tail, "|") {
			if !verbs[head+" "+alt] {
				covered = false
			}
		}
		if covered {
			delete(verbs, v)
		}
	}
	return verbs
}

type parityRow struct {
	line                  int
	verb, status, surface string
}

// splitMarkdownRow splits a table row on unescaped pipes; `\|` is a literal
// pipe inside a cell (`daemon status\|stop`).
func splitMarkdownRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	var cells []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		if line[i] == '\\' && i+1 < len(line) && line[i+1] == '|' {
			cur.WriteByte('|')
			i++
			continue
		}
		if line[i] == '|' {
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteByte(line[i])
	}
	return append(cells, strings.TrimSpace(cur.String()))
}

func readParityLedger(t *testing.T) []parityRow {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(uiParityPath))
	if err != nil {
		t.Fatalf("read %s: %v", uiParityPath, err)
	}
	var rows []parityRow
	in := false
	for i, line := range strings.Split(string(raw), "\n") {
		switch strings.TrimSpace(line) {
		case "<!-- ui-parity:begin -->":
			in = true
			continue
		case "<!-- ui-parity:end -->":
			in = false
			continue
		}
		if !in || !strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}
		cells := splitMarkdownRow(line)
		if len(cells) < 3 || cells[0] == "Verb" || strings.HasPrefix(cells[0], "---") {
			continue
		}
		rows = append(rows, parityRow{
			line:    i + 1,
			verb:    strings.Trim(cells[0], "`"),
			status:  cells[1],
			surface: cells[2],
		})
	}
	if len(rows) == 0 {
		t.Fatalf("%s has no rows between the <!-- ui-parity:begin --> and <!-- ui-parity:end --> markers", uiParityPath)
	}
	return rows
}

// TestUIParityLedger keeps docs/ui-parity.md complete and honest: every CLI
// verb has a row, no row outlives its verb, and each row either names the UI
// route that does the same thing or says why there is none. A new CLI verb
// cannot land without deciding where -- or whether -- the UI offers it.
func TestUIParityLedger(t *testing.T) {
	verbs := cliVerbs()
	rows := readParityLedger(t)
	seen := map[string]int{}
	for _, r := range rows {
		where := uiParityPath[len("../../"):] + ":" + strconv.Itoa(r.line)
		if prev, dup := seen[r.verb]; dup {
			t.Errorf("%s: verb %q already has a row at line %d", where, r.verb, prev)
		}
		seen[r.verb] = r.line
		if !verbs[r.verb] {
			t.Errorf("%s: %q is not a CLI verb (internal/cli/cli.go commands()); delete the row or fix its name", where, r.verb)
		}
		switch r.status {
		case "ui", "partial":
			if !strings.Contains(r.surface, "#/") {
				t.Errorf("%s: %q is %s but its UI surface names no #/ route: %q", where, r.verb, r.status, r.surface)
			}
		case "cli-only":
			if !strings.HasPrefix(r.surface, "CLI-only:") || strings.TrimSpace(strings.TrimPrefix(r.surface, "CLI-only:")) == "" {
				t.Errorf("%s: %q is cli-only, so its UI surface must read `CLI-only: <reason>`, got %q", where, r.verb, r.surface)
			}
		default:
			t.Errorf("%s: %q has status %q; use ui, partial or cli-only", where, r.verb, r.status)
		}
	}
	var missing []string
	for v := range verbs {
		if _, ok := seen[v]; !ok {
			missing = append(missing, v)
		}
	}
	sort.Strings(missing)
	for _, v := range missing {
		t.Errorf("CLI verb %q has no row in docs/ui-parity.md: add \"| `%s` | ui, partial or cli-only | <#/route — control> or CLI-only: <reason> | |\" between the ui-parity markers", v, v)
	}
}
