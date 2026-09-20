package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/store"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// readError pulls the message out of an error response.
func readError(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(b, &e)
	if e.Error == "" {
		return string(b)
	}
	return e.Error
}

// TestRevertRejectsANonAppliedChangeSet pins audit finding b. handleApply grew a
// synchronous status guard -- with a comment explaining that without it
// `idea apply CHG-1` on an already-applied change set answered "✓ apply started"
// -- and handleRevert never got the same treatment. Measured against the shipped
// v0.1.0 binary: `idea revert CHG-1` on a reverted change set printed
// "✓ revert started" and exited 0 while `idea apply CHG-1` correctly exited 1.
func TestRevertRejectsANonAppliedChangeSet(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	base := ts.URL + "/api/v1/projects/" + projectID

	// Drive a change set through the real state machine to reverted, rather
	// than hoping framing happens to produce one -- the first version of this
	// test skipped, which is no test at all.
	cs := &domain.ChangeSet{InitiativeID: iniID}
	if err := pr.db.CreateChangeSet(t.Context(), cs); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ from, to domain.ChangeSetStatus }{
		{domain.ChangeSetDraft, domain.ChangeSetChecksRunning},
		{domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired},
		{domain.ChangeSetReviewRequired, domain.ChangeSetApproved},
		{domain.ChangeSetApproved, domain.ChangeSetApplying},
	} {
		if err := pr.db.TransitionChangeSet(t.Context(), cs.ID, step.from, step.to, "test"); err != nil {
			t.Fatalf("transition %s -> %s: %v", step.from, step.to, err)
		}
	}
	if err := pr.db.MarkChangeSetApplied(t.Context(), cs.ID, "digest", "test"); err != nil {
		t.Fatal(err)
	}
	if err := pr.db.MarkChangeSetReverted(t.Context(), cs.ID, "test"); err != nil {
		t.Fatal(err)
	}

	resp := doReq(t, http.MethodPost, base+"/change-sets/"+cs.ID+"/revert", token, "", "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("revert on a reverted change set = %d, want 409 (got %q)",
			resp.StatusCode, readError(t, resp))
	}
	if msg := readError(t, resp); !strings.Contains(msg, "already reverted") {
		t.Errorf("409 message %q does not say the change set is already reverted", msg)
	}
}

// TestAdvanceTwiceIsAConflict pins audit finding l: handleAdvance mapped every
// error to 500, including "initiative is already running" and "advance requires
// an approved PRD", while retry/approve-plan/reopen-phase/revise-plan returned
// 409 for the identical class.
//
// The handler still passes StatusInternalServerError. It gets 409 anyway,
// because the classification now travels with the error -- which is the point:
// the seventh handler cannot get it wrong.
func TestAdvanceTwiceIsAConflict(t *testing.T) {
	_, ts, token := newTestServer(t)
	base, iniID := newSeededProject(t, ts, token, t.TempDir())

	// A freshly framed initiative is not in the approved state advance requires.
	resp := doReq(t, http.MethodPost, base+"/initiatives/"+iniID+"/advance", token, "", "")
	if resp.StatusCode == http.StatusAccepted {
		var out struct {
			OperationID string `json:"operation_id"`
		}
		decode(t, resp, &out)
		waitForOperation(t, ts, token, out.OperationID)
		resp = doReq(t, http.MethodPost, base+"/initiatives/"+iniID+"/advance", token, "", "")
	}
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("advance from a non-advanceable state = %d, want 409 (got %q)",
			resp.StatusCode, readError(t, resp))
	}
}

// TestExecuteRequiresAnApprovedPlan pins the execute half of finding b.
func TestExecuteRequiresAnApprovedPlan(t *testing.T) {
	_, ts, token := newTestServer(t)
	base, iniID := newSeededProject(t, ts, token, t.TempDir())

	resp := doReq(t, http.MethodPost, base+"/initiatives/"+iniID+"/execute", token, "", "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("execute without an approved plan = %d, want 409 (got %q)",
			resp.StatusCode, readError(t, resp))
	}
}

// TestExecuteRequiresAReadyTask is ICP re-run defect E: `idea execute` on a
// plan_approved initiative with no ready task (every task done, cancelled, or
// blocked on an unsatisfied dependency) used to print "✓ execute started" --
// launching the async operation, which then failed in the background with
// "no ready tasks ... (dependencies not satisfied)". The check is a cheap,
// lock-free read (engine.NextReadyTask), so it belongs before the operation
// launches, not only inside it.
func TestExecuteRequiresAReadyTask(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	// Jump straight to plan_approved with no tasks at all -- a plan that has
	// nothing left (or never had anything) to run.
	if err := pr.db.SetInitiativeStatus(t.Context(), iniID, domain.InitiativePlanApproved, store.ActorOrchestrator); err != nil {
		t.Fatal(err)
	}
	base := ts.URL + "/api/v1/projects/" + projectID

	resp := doReq(t, http.MethodPost, base+"/initiatives/"+iniID+"/execute", token, "", "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("execute with no ready task = %d, want 409 (got %q)",
			resp.StatusCode, readError(t, resp))
	}
	if msg := readError(t, resp); !strings.Contains(msg, "no ready tasks") {
		t.Errorf("409 message %q does not say there is no ready task", msg)
	}

	// The initiative must not have been claimed `running` by the refused
	// attempt -- a precondition refusal must leave it exactly as it was.
	ini, err := pr.db.GetInitiative(t.Context(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.Status != domain.InitiativePlanApproved {
		t.Errorf("a refused execute changed the initiative status to %q", ini.Status)
	}
}

// TestPreconditionErrorMapsToConflict tests the invariant rather than one
// handler: writeErr answers 409 for a PreconditionError regardless of the status
// the handler passed. Testing only `advance` would test the sixth handler.
func TestPreconditionErrorMapsToConflict(t *testing.T) {
	s := &Server{}
	for _, status := range []int{
		http.StatusInternalServerError,
		http.StatusBadRequest,
		http.StatusConflict,
	} {
		w := httptest.NewRecorder()
		s.writeErr(w, status, preconditionf("initiative is already running"))
		if w.Code != http.StatusConflict {
			t.Errorf("writeErr(%d, PreconditionError) = %d, want 409", status, w.Code)
		}
		if got := w.Body.String(); !strings.Contains(got, "already running") {
			t.Errorf("writeErr(%d) lost the reason: %q", status, got)
		}
	}
}

// TestNonPreconditionErrorsKeepTheirStatus is the other half, and the one that
// matters for regressions: fourteen handler call sites pass StatusBadRequest for
// engine errors (ReviewChangeSet and VerifyInitiative among them). If an error
// those paths surface were ever given the precondition type, the response would
// silently flip 400 -> 409.
func TestNonPreconditionErrorsKeepTheirStatus(t *testing.T) {
	s := &Server{}
	w := httptest.NewRecorder()
	s.writeErr(w, http.StatusBadRequest, errPlain("requirement REQ-9 is not in this initiative"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("a plain domain error passed at 400 became %d", w.Code)
	}

	// A store miss still wins over everything: 404, not the handler's status.
	w = httptest.NewRecorder()
	s.writeErr(w, http.StatusBadRequest, &store.NotFoundError{Kind: "requirement", Ref: "REQ-9"})
	if w.Code != http.StatusNotFound {
		t.Errorf("a store miss passed at 400 became %d, want 404", w.Code)
	}
}

type errPlain string

func (e errPlain) Error() string { return string(e) }

// TestReloadFailureNeverLeaksAStatePath covers the executor config reload path,
// which the DX audit found printing an absolute state path at the user and which
// no test exercised.
//
// Both assertions matter. Asserting only "no /Users/ in the body" would pass
// trivially on a 500 "internal error (correlation id)", which is exactly the
// regression the naive fix would have introduced: wrapping the cause with %w
// keeps store.IsStorageFailure able to detect it, so routing this through
// writeErr would mask the message the caller needs.
func TestReloadFailureNeverLeaksAStatePath(t *testing.T) {
	s := &Server{rt: &Runtime{log: slog.New(slog.NewTextHandler(io.Discard, nil))}}

	t.Run("storage failure", func(t *testing.T) {
		err := fmt.Errorf("daemon: state dir: %w", &fs.PathError{
			Op:   "mkdir",
			Path: "/Users/someone/.local/share/idea-harness/projects/abc",
			Err:  errors.New("permission denied"),
		})
		w := httptest.NewRecorder()
		s.writeReloadErr(w, err)

		if w.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409 — a 500 would hide that the file was written", w.Code)
		}
		body := w.Body.String()
		assertNoStorageDetail(t, body)
		if !strings.Contains(body, "config written but not applied") {
			t.Errorf("body must say the config was written but not applied, got: %s", body)
		}
		if !strings.Contains(body, "correlation id") {
			t.Errorf("a masked error must carry a correlation id, got: %s", body)
		}
	})

	t.Run("domain error keeps its message", func(t *testing.T) {
		w := httptest.NewRecorder()
		s.writeReloadErr(w, errors.New("daemon: cannot reload project while 2 operation(s) are running"))

		if w.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409", w.Code)
		}
		if body := w.Body.String(); !strings.Contains(body, "2 operation(s) are running") {
			t.Errorf("a domain error must survive verbatim, got: %s", body)
		}
	})
}

// TestBothConfigEndpointsReportWrittenButNotApplied pins C3.
//
// writeReloadErr exists so the caller learns their config file WAS written and
// the running daemon kept the old one. Only the executors endpoint used it; the
// providers endpoint sent the identical failure to writeErr, whose storage
// branch masks it to "internal error (correlation id)". So the same condition
// told the user two different things depending on which config they edited, and
// docs/api.md went on to claim a handler cannot get this wrong.
//
// Parsed, not string-scanned. A first version searched from the function name to
// the next "\n}\n", which truncates at the first nested closure -- so the body it
// examined never contained ReloadProject, the check skipped itself, and the test
// passed with the fix reverted. Vacuous in exactly the way this round keeps
// finding elsewhere.
func TestBothConfigEndpointsReportWrittenButNotApplied(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "handlers.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if fn.Name.Name != "handleUpdateProviders" && fn.Name.Name != "handleUpdateExecutors" {
			continue
		}
		var reloads, reportsHonestly bool
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				switch sel.Sel.Name {
				case "ReloadProject":
					reloads = true
				case "writeReloadErr":
					reportsHonestly = true
				}
			}
			return true
		})
		if !reloads {
			continue
		}
		checked++
		if !reportsHonestly {
			t.Errorf("%s handles a ReloadProject failure without writeReloadErr, so a caller "+
				"whose config was written but not applied gets a masked 500", fn.Name.Name)
		}
	}
	if checked != 2 {
		t.Fatalf("examined %d config endpoints, want 2; the test is not finding them", checked)
	}
}
