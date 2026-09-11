package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// `idea revise` has prescribed `idea reopen-phase` since audit 7 — twice — and
// the verb did not exist. The route was registered, docs/api.md documented it,
// and the CLI had no row, so following the advice answered `unknown command
// "reopen-phase" (try 'idea help')`. Half a prescription reads as though the
// user typed it wrong.
func TestReopenPhaseIsARealVerb(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("POST /api/v1/projects/{projectId}/initiatives/{id}/reopen-phase",
		map[string]string{"operation_id": ""})

	stdout, _, err := run(t, "reopen-phase", "architecture", "--no-wait")
	if err != nil {
		t.Fatalf("`idea reopen-phase architecture` failed: %v", err)
	}
	if !strings.Contains(stdout, "reopened") {
		t.Errorf("no confirmation printed:\n%s", stdout)
	}
}

// The daemon accepts exactly three phases and 400s anything else. Validating in
// the CLI means a typo names the alternatives instead of producing a bare
// `invalid body` from the other side of the wire.
func TestReopenPhaseRejectsAnUnknownPhaseLocally(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()

	_, stderr, err := run(t, "reopen-phase", "architectuer")
	if err == nil {
		t.Fatal("a misspelled phase was accepted")
	}
	msg := err.Error() + stderr
	if !strings.Contains(msg, "architecture") {
		t.Errorf("the refusal does not name the valid phases: %v", err)
	}
	for _, req := range f.requests {
		if strings.Contains(req, "reopen-phase") {
			t.Errorf("a phase the daemon rejects was sent anyway: %s", req)
		}
	}
}

// Every phase the daemon accepts must be one the CLI accepts — read from the
// daemon's own switch, not from a copy of the list in this file.
//
// A first version asserted reopenPhases against a hardcoded `want` here while
// its failure message said "handlers.go's switch is the authority". It never
// read handlers.go, so changing the daemon left it green. The values happened
// to match, which is exactly how that kind of test survives.
func TestReopenPhasesMatchTheDaemon(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join("..", "daemon", "handlers.go"), nil,
		parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}

	var daemonPhases []string
	ast.Inspect(file, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		sel, ok := sw.Tag.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Phase" {
			return true
		}
		for _, stmt := range sw.Body.List {
			cc, ok := stmt.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, e := range cc.List {
				lit, ok := e.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				if v, err := strconv.Unquote(lit.Value); err == nil {
					daemonPhases = append(daemonPhases, v)
				}
			}
		}
		return true
	})

	if len(daemonPhases) == 0 {
		t.Fatal("no `switch body.Phase` case values found in handlers.go — the matcher is broken, " +
			"so this check proves nothing")
	}
	sort.Strings(daemonPhases)
	got := append([]string(nil), reopenPhases...)
	sort.Strings(got)

	if !slices.Equal(got, daemonPhases) {
		t.Errorf("the CLI accepts %v; the daemon accepts %v.\n"+
			"    A phase the CLI allows and the daemon does not becomes a bare `400 invalid body`;\n"+
			"    one the daemon allows and the CLI does not is simply unreachable.",
			got, daemonPhases)
	}
}

// The wait path, which nothing else reaches: TestReopenPhaseIsARealVerb passes
// `--no-wait` AND seeds an empty operation_id, so it takes both bail-outs and
// exercises none of the code that waits.
//
// The point of waiting is that the command must not report a start as a result
// — the same defect `idea retry` had.
func TestReopenPhaseWaitsAndReportsTheOutcome(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("POST /api/v1/projects/{projectId}/initiatives/{id}/reopen-phase",
		map[string]string{"operation_id": "op-1"})
	f.json("GET /api/v1/operations/{id}",
		map[string]string{"status": "succeeded", "type": "reopen_phase"})
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/next",
		map[string]any{"action": "advance", "failure_reason": ""})

	stdout, _, err := run(t, "reopen-phase", "architecture")
	if err != nil {
		t.Fatalf("reopen-phase with a real operation: %v", err)
	}
	if !strings.Contains(stdout, "cancel with") {
		t.Errorf("the wait path was not taken — no cancel hint printed:\n%s", stdout)
	}
}

// And it must not exit 0 with the outcome unknown. `return nil` on a failed
// lookup after a wait is the same "reported success without checking" this
// command was written to avoid.
func TestReopenPhaseDoesNotExitZeroWhenTheOutcomeIsUnreadable(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("POST /api/v1/projects/{projectId}/initiatives/{id}/reopen-phase",
		map[string]string{"operation_id": "op-1"})
	f.json("GET /api/v1/operations/{id}",
		map[string]string{"status": "succeeded", "type": "reopen_phase"})
	f.fail("GET /api/v1/projects/{projectId}/initiatives/{id}/next",
		http.StatusInternalServerError, "internal error (correlation id: deadbeef)")

	if _, _, err := run(t, "reopen-phase", "architecture"); err == nil {
		t.Error("exited 0 although the outcome of the reopen could not be read")
	}
}
