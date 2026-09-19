package daemon

import (
	"encoding/json"
	"net/http"
	"strings"
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
	pr, _ := rt.EnsureProject(t.Context(), projectID)
	req := &domain.Requirement{InitiativeID: iniID, Statement: "r", Priority: domain.PriorityMust}
	if err := pr.db.CreateRequirement(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	setStatus(t, rt, projectID, iniID, domain.InitiativePlanApproved)

	// A task without a requirement is refused (review A4 #3): at least one is
	// required.
	resp := doReq(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/tasks", token, "", `{"title":"T0","statement":"s","priority":"must"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 adding a task with no requirement, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Add a task, naming the requirement via requirement_ids.
	resp = doReq(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/tasks", token, "",
		`{"title":"T1","statement":"s","priority":"must","requirement_ids":["`+req.Code+`"]}`)
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

// TestUpdateTaskReplacesRequirements covers the PATCH half of review A4 #3:
// requirement_ids, when present, replaces a task's requirement links
// entirely, requires at least one, and is left untouched when omitted.
func TestUpdateTaskReplacesRequirements(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, _ := rt.EnsureProject(t.Context(), projectID)
	req1 := &domain.Requirement{InitiativeID: iniID, Statement: "r1", Priority: domain.PriorityMust}
	if err := pr.db.CreateRequirement(t.Context(), req1); err != nil {
		t.Fatal(err)
	}
	req2 := &domain.Requirement{InitiativeID: iniID, Statement: "r2", Priority: domain.PriorityMust}
	if err := pr.db.CreateRequirement(t.Context(), req2); err != nil {
		t.Fatal(err)
	}
	setStatus(t, rt, projectID, iniID, domain.InitiativePlanApproved)

	resp := doReq(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/tasks", token, "",
		`{"title":"T1","statement":"s","priority":"must","requirement_ids":["`+req1.Code+`"]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add task: %d", resp.StatusCode)
	}
	var task domain.Task
	json.NewDecoder(resp.Body).Decode(&task)
	resp.Body.Close()

	// Omitting requirement_ids on PATCH leaves the link untouched.
	resp = doReq(t, http.MethodPatch, ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/tasks/"+task.ID, token, "", `{"title":"T1-renamed"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch with no requirement_ids: %d", resp.StatusCode)
	}
	resp.Body.Close()
	edges, err := pr.db.ListGraphEdgesFrom(t.Context(), iniID, "task", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range edges {
		if e.EdgeType == domain.EdgeImplements {
			got[e.ToID] = true
		}
	}
	if !got[req1.ID] || len(got) != 1 {
		t.Fatalf("expected requirement links untouched (just req1), got %+v", got)
	}

	// An empty requirement_ids array is refused (review A4 #3): clearing a
	// task's last requirement is not allowed.
	resp = doReq(t, http.MethodPatch, ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/tasks/"+task.ID, token, "", `{"requirement_ids":[]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 clearing all requirements, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// A non-empty requirement_ids REPLACES the link entirely.
	resp = doReq(t, http.MethodPatch, ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/tasks/"+task.ID, token, "", `{"requirement_ids":["`+req2.Code+`"]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch replacing requirement_ids: %d", resp.StatusCode)
	}
	resp.Body.Close()
	edges, err = pr.db.ListGraphEdgesFrom(t.Context(), iniID, "task", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	got = map[string]bool{}
	for _, e := range edges {
		if e.EdgeType == domain.EdgeImplements {
			got[e.ToID] = true
		}
	}
	if got[req1.ID] || !got[req2.ID] || len(got) != 1 {
		t.Fatalf("expected requirement links replaced with just req2, got %+v", got)
	}
}

// TestUpdateTaskValidatesBeforeReplacingRequirements is the low-priority
// review item: handleUpdateTask used to replace a task's requirement edges
// FIRST and only validate `priority` afterward, so an invalid priority in
// the same PATCH request left the edges replaced despite the 400. Every
// field must validate before either write runs.
func TestUpdateTaskValidatesBeforeReplacingRequirements(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, _ := rt.EnsureProject(t.Context(), projectID)
	req1 := &domain.Requirement{InitiativeID: iniID, Statement: "r1", Priority: domain.PriorityMust}
	if err := pr.db.CreateRequirement(t.Context(), req1); err != nil {
		t.Fatal(err)
	}
	req2 := &domain.Requirement{InitiativeID: iniID, Statement: "r2", Priority: domain.PriorityMust}
	if err := pr.db.CreateRequirement(t.Context(), req2); err != nil {
		t.Fatal(err)
	}
	setStatus(t, rt, projectID, iniID, domain.InitiativePlanApproved)

	resp := doReq(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/tasks", token, "",
		`{"title":"T1","statement":"s","priority":"must","requirement_ids":["`+req1.Code+`"]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add task: %d", resp.StatusCode)
	}
	var task domain.Task
	json.NewDecoder(resp.Body).Decode(&task)
	resp.Body.Close()

	// A PATCH naming req2 (a real, valid replacement) alongside an invalid
	// priority must fail the WHOLE request, leaving req1's link untouched.
	resp = doReq(t, http.MethodPatch, ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/tasks/"+task.ID, token, "",
		`{"priority":"not-a-real-priority","requirement_ids":["`+req2.Code+`"]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for an invalid priority, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	edges, err := pr.db.ListGraphEdgesFrom(t.Context(), iniID, "task", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range edges {
		if e.EdgeType == domain.EdgeImplements {
			got[e.ToID] = true
		}
	}
	if !got[req1.ID] || got[req2.ID] || len(got) != 1 {
		t.Fatalf("a rejected PATCH must not have replaced the requirement links, got %+v", got)
	}
}

// TestUpdateTaskRefusesASupersededRequirement is the low-priority review
// item: a requirement PATCH must refuse to wire a task to a superseded
// requirement rather than silently accepting it as if it were live.
func TestUpdateTaskRefusesASupersededRequirement(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, _ := rt.EnsureProject(t.Context(), projectID)
	req1 := &domain.Requirement{InitiativeID: iniID, Statement: "r1", Priority: domain.PriorityMust}
	if err := pr.db.CreateRequirement(t.Context(), req1); err != nil {
		t.Fatal(err)
	}
	setStatus(t, rt, projectID, iniID, domain.InitiativePlanApproved)

	resp := doReq(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/tasks", token, "",
		`{"title":"T1","statement":"s","priority":"must","requirement_ids":["`+req1.Code+`"]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add task: %d", resp.StatusCode)
	}
	var task domain.Task
	json.NewDecoder(resp.Body).Decode(&task)
	resp.Body.Close()

	// Supersede req1 (mirrors what a revise/retry does) and re-emit an active
	// requirement with the identical statement, so there IS a live
	// continuation to name.
	if err := pr.db.SupersedeRequirements(t.Context(), iniID, "human-revise"); err != nil {
		t.Fatal(err)
	}
	req1Live := &domain.Requirement{InitiativeID: iniID, Statement: "r1", Priority: domain.PriorityMust}
	if err := pr.db.CreateRequirement(t.Context(), req1Live); err != nil {
		t.Fatal(err)
	}

	resp = doReq(t, http.MethodPatch, ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/tasks/"+task.ID, token, "",
		`{"requirement_ids":["`+req1.Code+`"]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 referencing a superseded requirement, got %d", resp.StatusCode)
	}
	body := errorBody(t, resp)
	if !strings.Contains(body, "superseded") {
		t.Errorf("error does not say the requirement is superseded: %q", body)
	}
	if !strings.Contains(body, req1Live.Code) {
		t.Errorf("error does not name the live continuation %s: %q", req1Live.Code, body)
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
