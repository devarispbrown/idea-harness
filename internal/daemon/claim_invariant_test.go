package daemon

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// asyncEntryPoints is the invariant table: every Runtime method that launches a
// background operation, and how its initiative claim is released.
//
// `releases` records the mechanism, and is documentation with teeth — a method
// listed as releasing must have a terminal-path test below or a deferred row
// naming why not.
var asyncEntryPoints = map[string]string{
	"StartInitiativeWithDocs":   "sets InitiativeRunning at creation; the engine reaches a terminal status",
	"AdvanceInitiative":         "workflow/advance.go sets a terminal status on success; failOperation on error",
	"ExecuteInitiative":         "explicit release on both exits — the engine sets nothing (audit 4 P0)",
	"ApplyChangeSetInitiative":  "explicit release on both exits",
	"RevertChangeSetInitiative": "workflow RevertChangeSet sets plan_approved; explicit release on error",
	"ReviseInitiative":          "the engine reaches a terminal status; failOperation on error",
	"RetryInitiative":           "the engine reaches a terminal status; failOperation on error",
	"ReopenPhaseInitiative":     "the engine reaches a terminal status; failOperation on error",
	"RevisePlanInitiative":      "the engine reaches a terminal status; failOperation on error",
	"ApprovePlanInitiative":     "the engine reaches a terminal status; failOperation on error",
	"SuggestAnswersInitiative":  "claims and releases explicitly",
}

// TestEveryAsyncEntryPointIsInTheClaimTable is C4a.
//
// It exists because of what the previous round shipped: the initiative claim was
// added to seven entry points, and six released only as a side effect of their
// engine call reaching a terminal status. Execute's did not, so a successful
// `idea execute` stranded the initiative forever — and every gate stayed green,
// because smoke.sh and the audit journey both revert afterwards and
// RevertChangeSet resets the status one step after the bug corrupts it.
//
// Reading did not catch that across four reviews. Enumeration does: a new
// background operation cannot join runtime.go without appearing here, and adding
// a row forces someone to write down how its claim is released.
//
// This is deliberately the cheap half. Driving every terminal path is C4b/C4c;
// the success path needs Runtime-level fixtures that do not exist yet and is
// recorded in docs/plans/deferred.md rather than implied as covered.
func TestEveryAsyncEntryPointIsInTheClaimTable(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "runtime.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}

	found := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "launchOperation" {
				found[fn.Name.Name] = true
			}
			return true
		})
	}

	if len(found) == 0 {
		t.Fatal("found no launchOperation call sites; this test is not reading runtime.go")
	}
	for name := range found {
		if _, listed := asyncEntryPoints[name]; !listed {
			t.Errorf("%s launches a background operation and is not in asyncEntryPoints.\n"+
				"Add it, and record how its initiative claim is released — a claim with no "+
				"release strands the initiative permanently, which is how a successful "+
				"`idea execute` shipped broken.", name)
		}
	}
	for name := range asyncEntryPoints {
		if !found[name] {
			t.Errorf("asyncEntryPoints lists %s, which no longer launches an operation; "+
				"remove the row so the table stays a census rather than a wish list", name)
		}
	}
	if t.Failed() {
		var names []string
		for n := range found {
			names = append(names, n)
		}
		sort.Strings(names)
		t.Logf("launchOperation sites in runtime.go: %v", names)
	}
}

// TestTerminalPathsReleaseTheClaim is C4b: cancel, ordinary failure, and panic,
// driven directly for each entry point that claims.
//
// Success is deliberately absent. It is the one terminal path that is not
// centralised — each closure inlines its own write, and for most entry points
// the release happens inside internal/workflow — so covering it needs
// Runtime-level fixtures (a fake executor as well as the fake provider) that do
// not exist in this tree. Building it the cheap way would produce a test that
// passes while a stranding is live, which is exactly what happened to the
// Execute path. Recorded in docs/plans/deferred.md as C4c rather than faked.
func TestTerminalPathsReleaseTheClaim(t *testing.T) {
	// The states each entry point legitimately claims from, mirroring runtime.go.
	claimants := []struct {
		name string
		from domain.InitiativeStatus
	}{
		{"advance", domain.InitiativeApproved},
		{"execute", domain.InitiativePlanApproved},
		{"revise", domain.InitiativePlanApproved},
		{"retry", domain.InitiativeFailed},
		{"reopen-phase", domain.InitiativePlanApproved},
		{"revise-plan", domain.InitiativePlanApproved},
		{"approve-plan", domain.InitiativeNeedsHumanJudgment},
	}

	for _, c := range claimants {
		for _, path := range []string{"cancelled", "failed", "panicked"} {
			t.Run(c.name+"/"+path, func(t *testing.T) {
				rt, ts, token := newTestServer(t)
				projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
				pr, err := rt.EnsureProject(t.Context(), projectID)
				if err != nil {
					t.Fatal(err)
				}
				ctx := t.Context()
				setStatus(t, rt, projectID, iniID, c.from)
				if err := pr.db.ClaimInitiativeRunning(ctx, iniID, c.from); err != nil {
					t.Fatalf("could not claim from %s: %v", c.from, err)
				}
				opID, err := pr.db.CreateOperation(ctx, projectID, iniID, c.name)
				if err != nil {
					t.Fatal(err)
				}

				switch path {
				case "cancelled":
					cancelled, cancel := context.WithCancel(ctx)
					cancel()
					rt.failOperation(opID, iniID, "cancelled", cancelled, pr, context.Canceled, true)
				case "failed":
					rt.failOperation(opID, iniID, "failed", ctx, pr, errBoom, true)
				case "panicked":
					rt.failOperationAfterPanic(opID, "boom")
				}

				ini, err := pr.db.GetInitiative(ctx, iniID)
				if err != nil {
					t.Fatal(err)
				}
				if ini.Status == domain.InitiativeRunning {
					t.Errorf("%s left the initiative claimed after %s; every later command "+
						"answers \"already running\" with no way back", c.name, path)
				}
			})
		}
	}
}

var errBoom = errors.New("boom")

// TestPanicAfterSuccessLeavesTheRecordAlone is C2's other direction, and the one
// the plan's first draft missed.
//
// A panic can happen after the goroutine's own terminal write — in a trailing
// log line. The obvious patch (gate the operation write, leave the initiative
// release alone) produces operation=succeeded with initiative=failed: a
// self-contradictory record that sends a user to `idea retry` on a run that
// actually worked, re-running the whole pipeline for nothing.
//
// Both writes are gated on the same outcome, so a late panic changes neither.
func TestPanicAfterSuccessLeavesTheRecordAlone(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()

	setStatus(t, rt, projectID, iniID, domain.InitiativePlanApproved)
	opID, err := pr.db.CreateOperation(ctx, projectID, iniID, "advance")
	if err != nil {
		t.Fatal(err)
	}
	// The work finished and recorded itself, as it does on the success path.
	if err := pr.db.SetOperationStatus(ctx, opID, domain.OperationSucceeded, "CHG-1"); err != nil {
		t.Fatal(err)
	}

	rt.failOperationAfterPanic(opID, "panic in a trailing log line")

	op, err := pr.db.GetOperation(ctx, opID)
	if err != nil {
		t.Fatal(err)
	}
	if op.Status != domain.OperationSucceeded {
		t.Errorf("a late panic rewrote a succeeded operation to %q", op.Status)
	}
	ini, err := pr.db.GetInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.Status == domain.InitiativeFailed {
		t.Error("a late panic failed an initiative whose operation succeeded; " +
			"the user is sent to `idea retry` on a run that worked")
	}
}
