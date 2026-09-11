package daemon

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// A refusal that reports the initiative's current state is telling the user
// they asked for the wrong thing. It has to say what the right thing is.
//
// Four of them did not: `advance requires an approved PRD (status=approved)`,
// `retry requires a failed initiative (status=...)`, `no plan to revise` and
// `no plan to approve`. Two were actively misleading as well — the bare
// `status=approved` in "no plan to approve (status=approved)" is the *PRD's*
// status, and reads as though the plan were approved.
//
// Enumerated rather than read: this is a message-shape rule across 36 call
// sites in one file, and the eight that already named a route did not stop four
// more from shipping without one.
func TestPreconditionRefusalsReportingStateNameARoute(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "runtime.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}

	checked := 0
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok || id.Name != "preconditionf" {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		msg, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		// "Reports state" is decided by what the call PASSES, not by how the
		// message is phrased. A first version matched the literal strings
		// "initiative is %s" and "status=%s" -- the two phrasings that had just
		// been adopted -- so rewording a refusal to "(currently %s)" dropped it
		// out of the checked set silently, and the count fell 6 to 5 with the
		// test still green.
		if !passesInitiativeStatus(call) {
			return true
		}
		checked++
		if !strings.Contains(msg, "`idea ") {
			t.Errorf("%s:%d: refusal reports the initiative's state but names no route:\n    %q\n"+
				"    Say what to run instead — `idea next` names the step the initiative is ready for.",
				fset.Position(lit.Pos()).Filename, fset.Position(lit.Pos()).Line, msg)
		}
		if strings.Contains(msg, "status=%s") {
			t.Errorf("%s:%d: %q says `status=` without saying whose.\n"+
				"    `no plan to approve (status=approved)` reports the PRD's status and reads as "+
				"though the plan were approved.",
				fset.Position(lit.Pos()).Filename, fset.Position(lit.Pos()).Line, msg)
		}
		return true
	})

	// A floor, not just non-zero: the failure mode this replaced was a refusal
	// quietly leaving the checked set, which a `> 0` tripwire cannot see. Raise
	// this when a refusal is added; lowering it means one stopped reporting
	// state, which is worth a deliberate edit.
	const knownStateReportingRefusals = 6
	if checked < knownStateReportingRefusals {
		t.Errorf("checked %d state-reporting refusals, expected at least %d.\n"+
			"    One has stopped passing ini.Status, or the matcher no longer sees it. If a refusal\n"+
			"    genuinely no longer reports state, lower this constant on purpose.",
			checked, knownStateReportingRefusals)
	}
	t.Logf("checked %d state-reporting precondition refusals", checked)
}

// passesInitiativeStatus reports whether a call hands the initiative's status to
// the message, whatever the surrounding phrasing.
func passesInitiativeStatus(call *ast.CallExpr) bool {
	for _, arg := range call.Args[1:] {
		sel, ok := arg.(*ast.SelectorExpr)
		if ok && sel.Sel.Name == "Status" {
			return true
		}
	}
	return false
}
