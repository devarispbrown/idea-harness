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
	"github.com/devarispbrown/idea-harness/internal/store"
)

// asyncEntryPoints is the invariant table: every Runtime method that launches a
// background operation, and how its initiative claim is released.
//
// `releases` records the mechanism, and is documentation with teeth — a method
// listed as releasing must have a terminal-path test below or a deferred row
// naming why not.
type entryPoint struct {
	// claims is checked against the AST, not trusted. The prose half of this
	// table said SuggestAnswersInitiative "claims and releases explicitly" when
	// it does neither -- the row nearest the reader was the wrong one, and
	// membership-only checking could not see it.
	claims   bool
	releases string
}

var asyncEntryPoints = map[string]entryPoint{
	"StartInitiativeWithDocs": {false,
		"does not claim: the initiative is created running, so there is no prior state to take. " +
			"It does write a terminal status on the error paths between CreateInitiative and " +
			"launchOperation — a half-created row is marked failed so `idea retry` can reach it — " +
			"but that is not a release of a claim it never took"},
	"AdvanceInitiative": {true,
		"workflow/advance.go sets a terminal status on success; failOperation on error"},
	"ExecuteInitiativeFreshWithNote": {true,
		"explicit release on both exits — the engine sets nothing (audit 4 P0). " +
			"ExecuteInitiativeFresh/ExecuteInitiative are thin wrappers around this " +
			"one (D-a, wave 3 contract: the optional rerun note), so this is the " +
			"row the AST scan finds"},
	"ApplyChangeSetInitiative": {true,
		"claims from every non-running state; explicit release on both exits"},
	"RevertChangeSetInitiative": {true,
		"claims from every non-running state; releases to plan_approved on success (the engine's own transition, since the revert unverifies a requirement) and to the prior status on error"},
	"ReviseInitiative": {true, "the engine reaches a terminal status; failOperation on error"},
	"RetryInitiativeWithNote": {true,
		"the engine reaches a terminal status; failOperation on error. " +
			"RetryInitiative is a thin wrapper around this one (D-a, wave 3 " +
			"contract: the optional rerun note), so this is the row the AST scan finds"},
	"ReopenPhaseInitiative": {true, "the engine reaches a terminal status; failOperation on error"},
	"RevisePlanInitiative":  {true, "the engine reaches a terminal status; failOperation on error"},
	"ApprovePlanInitiative": {true, "the engine reaches a terminal status; failOperation on error"},
	"RerunChangeSetReviewInitiative": {true,
		"claims from every non-running state (ICP re-run D2: the adversarial review this runs is a real model call, now async rather than synchronous inside the request); explicit release on both exits"},
	"SuggestAnswersInitiative": {false,
		"checks existence and refuses while running, but does not take the claim — it only reads open questions and writes suggestions, so a concurrent advance is a provenance race rather than a corrupted tree. The existence check landed after audit 5; the claim itself is still recorded in docs/plans/deferred.md as C4"},
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
	claims := map[string]bool{}
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
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "launchOperation":
				found[fn.Name.Name] = true
			case "ClaimInitiativeRunning":
				claims[fn.Name.Name] = true
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
	// The load-bearing half of each row, checked rather than read. The table
	// claimed SuggestAnswersInitiative "claims and releases explicitly" while
	// its body contains neither call; membership-only checking let that stand
	// through the round that added the table.
	for name := range found {
		row, listed := asyncEntryPoints[name]
		if !listed {
			continue // already reported above
		}
		if row.claims != claims[name] {
			t.Errorf("asyncEntryPoints[%q].claims = %v, but runtime.go %s call ClaimInitiativeRunning.\n"+
				"    row says: %s", name, row.claims,
				map[bool]string{true: "does", false: "does not"}[claims[name]], row.releases)
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
	// failInitiative mirrors the literal each call site passes in runtime.go.
	// It used to be hard-coded true for every row, so the six execute/apply/
	// revert subtests drove a call the production code never makes: those paths
	// pass false and rely on their own release() instead. They asserted nothing
	// about the real code, which is why deleting the apply release left them
	// green. They now drive what actually runs, and the release closure is
	// exercised alongside.
	claimants := []struct {
		name           string
		from           domain.InitiativeStatus
		failInitiative bool
		// releasesItself is true where the goroutine calls release() rather than
		// relying on failOperation to move the initiative off running.
		releasesItself bool
		// opType is the literal CreateOperation `type` string the real Runtime
		// method passes (review H7: failOperationAfterPanic now gates its
		// FailInitiative call on operationClaimsInitiative(op.Type), so this
		// must be the production string, not the test's own short label, or
		// every claimant here would be misclassified as non-claiming).
		opType string
	}{
		{"advance", domain.InitiativeApproved, true, false, "advance_initiative"},
		{"execute", domain.InitiativePlanApproved, false, true, "execute_initiative"},
		{"revise", domain.InitiativePlanApproved, true, false, "revise_initiative"},
		{"retry", domain.InitiativeFailed, true, false, "retry_initiative"},
		{"reopen-phase", domain.InitiativePlanApproved, true, false, "reopen_phase"},
		{"revise-plan", domain.InitiativePlanApproved, true, false, "revise_plan"},
		{"approve-plan", domain.InitiativeNeedsHumanJudgment, true, false, "approve_plan"},
		{"apply", domain.InitiativePlanApproved, false, true, "apply_change_set"},
		{"revert", domain.InitiativePlanApproved, false, true, "revert_change_set"},
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
				opID, err := pr.db.CreateOperation(ctx, projectID, iniID, c.opType)
				if err != nil {
					t.Fatal(err)
				}

				// The entry points that pass failInitiative=false release the
				// claim themselves before calling failOperation, so the test
				// must do the same to drive the real sequence.
				releaseLikeTheGoroutineDoes := func() {
					if !c.releasesItself {
						return
					}
					if err := pr.db.SetInitiativeStatus(context.WithoutCancel(ctx), iniID,
						c.from, store.ActorOrchestrator); err != nil {
						t.Fatal(err)
					}
				}

				switch path {
				case "cancelled":
					cancelled, cancel := context.WithCancel(ctx)
					cancel()
					releaseLikeTheGoroutineDoes()
					rt.failOperation(opID, iniID, "cancelled", cancelled, pr, context.Canceled, c.failInitiative)
				case "failed":
					releaseLikeTheGoroutineDoes()
					rt.failOperation(opID, iniID, "failed", ctx, pr, errBoom, c.failInitiative)
				case "panicked":
					// No release: a panic unwinds before the goroutine's own
					// release runs, which is exactly why failOperationAfterPanic
					// has to move the initiative off running by itself.
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

// TestApplyIsRefusedWhileTheInitiativeIsBusy pins what C1's claim is actually
// for.
//
// apply, revert and execute already shared the engine mutex, so they were
// mutually exclusive before this. The gap was Revise, which takes no mutex and
// can supersede requirements while an apply lands. The claim closes it by making
// "some operation holds this initiative" the thing apply checks.
func TestApplyIsRefusedWhileTheInitiativeIsBusy(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	setStatus(t, rt, projectID, iniID, domain.InitiativePlanApproved)

	// Something else holds it.
	if err := pr.db.ClaimInitiativeRunning(t.Context(), iniID, domain.InitiativePlanApproved); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.ApplyChangeSetInitiative(t.Context(), projectID, iniID, "cs-1"); err == nil {
		t.Error("apply started while another operation held the initiative")
	}
}

// TestApplyIsAllowedFromEveryRestingState keeps the claim from becoming a
// regression. apply had no guard at all, so it succeeded from every status; a
// list narrower than "everything not running" would refuse flows that work.
func TestApplyIsAllowedFromEveryRestingState(t *testing.T) {
	for _, st := range []domain.InitiativeStatus{
		domain.InitiativeApproved, domain.InitiativePlanApproved,
		domain.InitiativeNeedsHumanJudgment, domain.InitiativeComplete,
		domain.InitiativeFailed,
	} {
		t.Run(string(st), func(t *testing.T) {
			rt, ts, token := newTestServer(t)
			projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
			setStatus(t, rt, projectID, iniID, st)
			if _, err := rt.ApplyChangeSetInitiative(t.Context(), projectID, iniID, "cs-1"); err != nil {
				t.Errorf("apply refused from %s: %v", st, err)
			}
		})
	}
}

// compensatingWrites are the durable status writes that repair state after
// something else already failed.
//
// Every one of them must run on a context that outlives the request. ctx in a
// handler path is the HTTP request's: if the client disconnected -- `idea new
// --doc <big file>` then Ctrl-C -- the original call fails on ctx.Err() AND the
// repair sharing that context fails too, deterministically. The row is then
// left in the exact state the repair exists to prevent.
//
// runtime.go establishes the pattern in nineteen places and explains it at the
// release in ClaimInitiativeRunning. This is enumerated rather than read
// because the departure that prompted this test was written directly beneath
// that comment, by someone who had just read it.
//
// An end-to-end test cannot reach this: cancelling before the call makes the
// FIRST write fail, so no row is created and the compensating path never runs;
// cancelling during it is a race on a window microseconds wide. The property is
// syntactic, so it is checked syntactically.
var compensatingWrites = map[string]bool{
	"FailInitiative":      true,
	"SetInitiativeStatus": true,
	"CompleteInitiative":  true,
}

func TestCompensatingWritesDoNotShareTheRequestContext(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "runtime.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		// Only functions that take a context can pass the request's.
		if !takesContext(fn) {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !compensatingWrites[sel.Sel.Name] {
				return true
			}
			id, ok := call.Args[0].(*ast.Ident)
			if !ok || id.Name != "ctx" {
				return true // WithoutCancel, Background, sctx, bg — all fine
			}
			// A write on the happy path is not compensating; only those in an
			// error branch are. Distinguishing them syntactically is not worth
			// the machinery, so the rule is blunt on purpose: inside a function
			// that holds a request context, reach for a detached one.
			t.Errorf("%s:%d: %s is called with the request's ctx.\n"+
				"    A durable write here must survive client cancellation: use "+
				"context.WithoutCancel(ctx),\n    as the nineteen other compensating "+
				"writes in this file do. If this one is genuinely on the happy path,\n"+
				"    say so here and add it to the exemptions.",
				fset.Position(call.Pos()).Filename, fset.Position(call.Pos()).Line, sel.Sel.Name)
			return true
		})
	}
}

func takesContext(fn *ast.FuncDecl) bool {
	if fn.Type.Params == nil {
		return false
	}
	for _, p := range fn.Type.Params.List {
		sel, ok := p.Type.(*ast.SelectorExpr)
		if ok && sel.Sel.Name == "Context" {
			return true
		}
	}
	return false
}
