package daemon

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// setStatus forces an initiative into a status for guard testing.
func setStatus(t *testing.T, rt *Runtime, projectID, iniID string, status domain.InitiativeStatus) {
	t.Helper()
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if err := pr.db.SetInitiativeStatus(t.Context(), iniID, status, "test"); err != nil {
		t.Fatal(err)
	}
}

func TestTaskMutationsHandler(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	setStatus(t, rt, projectID, iniID, domain.InitiativePlanApproved)

	// Add a task.
	resp := doReq(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/tasks", token, "", `{"title":"T1","statement":"s","priority":"must"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add task: %d", resp.StatusCode)
	}
	var t1 domain.Task
	json.NewDecoder(resp.Body).Decode(&t1)
	resp.Body.Close()
	if t1.Code != "TASK-1" || t1.Status != domain.TaskProposed {
		t.Fatalf("added task = %+v", t1)
	}

	// Edit the task.
	resp = doReq(t, http.MethodPatch, ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/tasks/"+t1.ID, token, "", `{"title":"T1-edited"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("edit task: %d", resp.StatusCode)
	}
	pr, _ := rt.EnsureProject(t.Context(), projectID)
	got, _ := pr.db.GetTask(t.Context(), t1.ID)
	if got.Title != "T1-edited" {
		t.Fatalf("edit title = %q", got.Title)
	}

	// Cancel the task.
	resp = doReq(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/tasks/"+t1.ID+"/cancel", token, "", `{}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel task: %d", resp.StatusCode)
	}

	// Guard: not editable once initiative leaves plan_approved/needs_human_judgment.
	setStatus(t, rt, projectID, iniID, domain.InitiativeApproved)
	resp = doReq(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/tasks", token, "", `{"title":"T2"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 when not plan_approved, got %d", resp.StatusCode)
	}
}

func TestChecksHandler(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, _ := rt.EnsureProject(t.Context(), projectID)

	cs := &domain.ChangeSet{InitiativeID: iniID, TaskID: "t", PatchDigest: "p"}
	if err := pr.db.CreateChangeSet(t.Context(), cs); err != nil {
		t.Fatal(err)
	}
	if err := pr.db.CreateFinding(t.Context(), &domain.Finding{
		InitiativeID: iniID, TargetType: "change_set", TargetID: cs.ID,
		Severity: domain.SeverityObservation, Confidence: domain.ConfidenceHigh,
		Category: "build", Finding: "build passes", Evidence: "ok",
	}); err != nil {
		t.Fatal(err)
	}

	resp := doReq(t, http.MethodGet, ts.URL+"/api/v1/projects/"+projectID+"/change-sets/"+cs.ID+"/checks", token, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("checks: %d", resp.StatusCode)
	}
	var out struct {
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"checks"`
		Pass int `json:"pass"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if len(out.Checks) != 1 || out.Checks[0].Name != "build" || out.Checks[0].Status != "pass" || out.Pass != 1 {
		t.Fatalf("checks = %+v", out)
	}
}

func TestResolveConflictRejectHandler(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, _ := rt.EnsureProject(t.Context(), projectID)

	cs := &domain.ChangeSet{InitiativeID: iniID, TaskID: "t", PatchDigest: "p"}
	if err := pr.db.CreateChangeSet(t.Context(), cs); err != nil {
		t.Fatal(err)
	}
	if _, err := pr.db.SQL().Exec(`UPDATE change_sets SET status = ? WHERE id = ?`, domain.ChangeSetConflicted, cs.ID); err != nil {
		t.Fatal(err)
	}

	resp := doReq(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/change-sets/"+cs.ID+"/resolve-conflict", token, "", `{"resolution":"reject"}`)
	if resp.StatusCode != http.StatusOK {
		buf := make([]byte, 512)
		n, _ := resp.Body.Read(buf)
		t.Fatalf("resolve reject: %d: %s", resp.StatusCode, string(buf[:n]))
	}
	got, _ := pr.db.GetChangeSet(t.Context(), cs.ID)
	if got.Status != domain.ChangeSetRejected {
		t.Fatalf("status = %q, want rejected", got.Status)
	}
}

func TestMarkSeenHandler(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, _ := newTestProjectInitiative(t, rt, ts, token)

	resp := doReq(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/seen", token, "", `{"through":1}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mark seen: %d", resp.StatusCode)
	}
	var out map[string]int64
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	pr, _ := rt.EnsureProject(t.Context(), projectID)
	seq, _ := pr.db.GetReadCursor(t.Context(), "ui")
	if seq != 1 {
		t.Fatalf("read cursor = %d, want 1", seq)
	}
}
