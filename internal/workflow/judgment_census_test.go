package workflow

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// The reason an initiative parks is now load-bearing: `idea next` and
// `idea status` both dispatch on it, and the wrong value names the wrong
// command. Recording `plan_review` where the cause is actually unresolved
// dependencies would tell the user to run `idea approve-plan`, which sets
// plan_approved over a dependency graph known to be incomplete and leaves the
// `plan_dependency` finding undisposed -- wedging the completion gate later.
//
// A behavioural test would have to drive a model into emitting a plan with a
// dangling depends_on. This is the same invariant, checked where it is written.
func TestUnresolvedDependenciesRecordTheDependencyReason(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(".", "advance.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	branches := 0
	ast.Inspect(file, func(n ast.Node) bool {
		ifs, ok := n.(*ast.IfStmt)
		if !ok || !mentions(ifs.Cond, "errUnresolvedDependencies") {
			return true
		}
		branches++
		body := render(fset, ifs.Body)
		if !strings.Contains(body, "NeedsHumanJudgment") {
			t.Errorf("%s: an unresolved-dependency branch does not record a judgment reason:\n%s",
				fset.Position(ifs.Pos()), body)
			return true
		}
		if !strings.Contains(body, "JudgmentPlanDependencies") {
			t.Errorf("%s: an unresolved-dependency branch records the wrong reason; "+
				"anything but JudgmentPlanDependencies makes `idea next` offer approve-plan "+
				"for a plan whose dependency graph is known to be incomplete:\n%s",
				fset.Position(ifs.Pos()), body)
		}
		return true
	})

	// The census is only as good as its reach: if the branches are refactored
	// away or renamed, this must fail rather than silently check nothing.
	if branches != 3 {
		t.Fatalf("found %d unresolved-dependency branches in advance.go, expected 3; "+
			"the census is no longer looking at what it was written for", branches)
	}
}

// No producer in this package may park an initiative without recording why:
// SetInitiativeStatus writes the status with no reason, which is what left every
// client guessing.
//
// The reach is deliberately stated rather than implied. This walks
// internal/workflow only and matches the literal identifier, so a park site
// added in internal/daemon, or one passing the status through a variable, is
// invisible to it. All six producers live here today; that is what makes it
// complete, not the check itself.
func TestNoProducerParksWithoutARecordedReason(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "SetInitiativeStatus" {
					return true
				}
				for _, arg := range call.Args {
					if mentions(arg, "InitiativeNeedsHumanJudgment") {
						t.Errorf("%s: SetInitiativeStatus parks an initiative without recording why; "+
							"use db.NeedsHumanJudgment so `idea next` can name a command",
							fset.Position(call.Pos()))
					}
				}
				return true
			})
			_ = name
		}
	}
}

func mentions(n ast.Node, ident string) bool {
	found := false
	ast.Inspect(n, func(x ast.Node) bool {
		if id, ok := x.(*ast.Ident); ok && id.Name == ident {
			found = true
		}
		return !found
	})
	return found
}

func render(fset *token.FileSet, n ast.Node) string {
	var b strings.Builder
	if err := printer.Fprint(&b, fset, n); err != nil {
		return err.Error()
	}
	return b.String()
}
