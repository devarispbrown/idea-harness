package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestFailOperationRecordsARedactedError covers B2: a failed execute used to
// leave the initiative at plan_approved with the running card simply gone, and
// the reason lived only in daemon.log. failOperation now persists it on the
// operation itself, redacted so a key the agent's own output echoed never
// lands in the operations table.
func TestFailOperationRecordsARedactedError(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	opID, err := pr.db.CreateOperation(t.Context(), projectID, iniID, "execute_initiative")
	if err != nil {
		t.Fatal(err)
	}

	const secret = "sk-proj-abcdefghijklmnopqrstuvwxyz0123456789"
	runErr := errors.New("executor: agent exited 1: key was " + secret)
	rt.failOperation(opID, iniID, "execute failed (test)", context.Background(), pr, runErr, false)

	op, err := pr.db.GetOperation(t.Context(), opID)
	if err != nil {
		t.Fatal(err)
	}
	if op.Status != domain.OperationFailed {
		t.Fatalf("status = %q, want failed", op.Status)
	}
	if op.Error == "" {
		t.Fatal("failed operation carries no error; the Build page has nothing to show but a vanished running card")
	}
	if !strings.Contains(op.Error, "agent exited 1") {
		t.Errorf("error %q does not name the cause", op.Error)
	}
	if strings.Contains(op.Error, secret) {
		t.Errorf("error %q leaked the unredacted secret", op.Error)
	}

	// The API surface: GetOperation and the initiative's operation list both
	// carry it, since B2 requires "returned everywhere operations are
	// returned".
	resp := doReq(t, http.MethodGet, ts.URL+"/api/v1/operations/"+opID, token, "", "")
	defer resp.Body.Close()
	var got struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Error == "" || strings.Contains(got.Error, secret) {
		t.Errorf("GET /operations/%s error = %q, want a redacted non-empty reason", opID, got.Error)
	}
}

// TestFailOperationAfterPanicRecordsAnError is the panic-recovery twin: a
// panicked operation is also a failed one, and its record must carry a reason
// rather than an empty error field next to status=failed.
func TestFailOperationAfterPanicRecordsAnError(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	opID, err := pr.db.CreateOperation(t.Context(), projectID, iniID, "execute_initiative")
	if err != nil {
		t.Fatal(err)
	}

	rt.failOperationAfterPanic(opID, "nil pointer dereference")

	op, err := pr.db.GetOperation(t.Context(), opID)
	if err != nil {
		t.Fatal(err)
	}
	if op.Status != domain.OperationFailed {
		t.Fatalf("status = %q, want failed", op.Status)
	}
	if !strings.Contains(op.Error, "nil pointer dereference") {
		t.Errorf("error %q does not name the panic value", op.Error)
	}
}

// TestSucceededOperationOmitsError is the JSON-shape guard: a client reads
// `error` to decide whether to show the failed banner, so a succeeded
// operation must omit the field rather than send `"error":""`.
func TestSucceededOperationOmitsError(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	opID, err := pr.db.CreateOperation(t.Context(), projectID, iniID, "execute_initiative")
	if err != nil {
		t.Fatal(err)
	}
	if err := pr.db.SetOperationStatus(t.Context(), opID, domain.OperationSucceeded, "result"); err != nil {
		t.Fatal(err)
	}

	resp := doReq(t, http.MethodGet, ts.URL+"/api/v1/operations/"+opID, token, "", "")
	defer resp.Body.Close()
	body := map[string]any{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if _, present := body["error"]; present {
		t.Errorf("succeeded operation carries an error key: %v", body["error"])
	}
}
