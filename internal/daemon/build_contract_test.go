package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// guidanceView is the subset of GET .../guidance this test cares about.
type guidanceView struct {
	Action   string `json:"action"`
	NextTask *struct {
		ID      string `json:"id"`
		HumanID string `json:"human_id"`
		Title   string `json:"title"`
	} `json:"next_task"`
	RunBlockedReason string `json:"run_blocked_reason"`
}

func getGuidance(t *testing.T, url, token string) (raw string, g guidanceView) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Idea-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET guidance: %d: %s", resp.StatusCode, body)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &g); err != nil {
		t.Fatal(err)
	}
	return string(body), g
}

// TestGuidanceNamesTheNextReadyTask is the Build contract's `next_task`:
// the Build page's "Run next task" button needs the task's id/human_id/title
// without a second round trip.
func TestGuidanceNamesTheNextReadyTask(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: iniID, Title: "Add /health endpoint", Statement: "s", Priority: domain.PriorityMust, Status: domain.TaskReady}
	if err := pr.db.CreateTask(t.Context(), task, "", nil); err != nil {
		t.Fatal(err)
	}
	setStatus(t, rt, projectID, iniID, domain.InitiativePlanApproved)

	url := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/guidance"
	_, g := getGuidance(t, url, token)

	if g.NextTask == nil {
		t.Fatal("next_task is absent, want the ready task")
	}
	if g.NextTask.ID != task.ID || g.NextTask.HumanID != task.Code || g.NextTask.Title != task.Title {
		t.Errorf("next_task = %+v, want id=%s human_id=%s title=%s", g.NextTask, task.ID, task.Code, task.Title)
	}
	if g.RunBlockedReason != "" {
		t.Errorf("run_blocked_reason = %q, want empty (a run can start)", g.RunBlockedReason)
	}
}

// TestGuidanceOmitsNextTaskWhenNothingIsReady: the field must be OMITTED, not
// null, when there is nothing to run -- every other daemon endpoint's
// contract is "never emit a null field" (TestNoEndpointEmitsANullArray), and
// this is that same rule for a single optional object.
func TestGuidanceOmitsNextTaskWhenNothingIsReady(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	setStatus(t, rt, projectID, iniID, domain.InitiativePlanApproved)

	url := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/guidance"
	raw, g := getGuidance(t, url, token)

	if g.NextTask != nil {
		t.Errorf("next_task = %+v, want nil", g.NextTask)
	}
	if strings.Contains(raw, `"next_task":null`) {
		t.Errorf("next_task is present as null instead of omitted: %s", raw)
	}
}

// TestGuidanceRunBlockedReasonNamesAnOpenChangeSet is A5 surfaced through the
// Build contract: a second task's run is blocked while an earlier task's
// change set is still open, and the reason must name it.
func TestGuidanceRunBlockedReasonNamesAnOpenChangeSet(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID, csID := reviewRequiredChangeSet(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := pr.db.GetChangeSet(t.Context(), csID)
	if err != nil {
		t.Fatal(err)
	}
	second := &domain.Task{InitiativeID: iniID, Title: "second task", Statement: "s", Priority: domain.PriorityMust, Status: domain.TaskReady}
	if err := pr.db.CreateTask(t.Context(), second, "", nil); err != nil {
		t.Fatal(err)
	}

	url := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/guidance"
	_, g := getGuidance(t, url, token)

	if g.RunBlockedReason == "" {
		t.Fatal("run_blocked_reason is empty, want it to name the open change set")
	}
	if !strings.Contains(g.RunBlockedReason, cs.HumanID) {
		t.Errorf("run_blocked_reason = %q, want it to name %s", g.RunBlockedReason, cs.HumanID)
	}
}

// TestGuidanceRunBlockedReasonNamesARunningOperation: the plan-approved,
// nothing-open case is not the only way a run is blocked.
func TestGuidanceRunBlockedReasonNamesARunningOperation(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	setStatus(t, rt, projectID, iniID, domain.InitiativeRunning)

	url := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/guidance"
	_, g := getGuidance(t, url, token)

	if !strings.Contains(g.RunBlockedReason, "already running") {
		t.Errorf("run_blocked_reason = %q, want it to name an operation already running", g.RunBlockedReason)
	}
}

// TestGuidanceRunBlockedReasonNamesTheExecutorProblem is C3's half of the
// Build contract: even when the workflow-level checks pass (plan approved, a
// task is ready, nothing else open), an unready executor -- here, hermes with
// Docker unavailable -- must still block the run and say why,
// reusing the same executorStatus logic GET /executors exposes (single
// source of truth for "is the executor ready").
func TestGuidanceRunBlockedReasonNamesTheExecutorProblem(t *testing.T) {
	rt, ts, token := newTestServer(t)
	rt.images = fakeImageCache(dockerUnavailable)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: iniID, Title: "t", Statement: "s", Priority: domain.PriorityMust, Status: domain.TaskReady}
	if err := pr.db.CreateTask(t.Context(), task, "", nil); err != nil {
		t.Fatal(err)
	}
	setStatus(t, rt, projectID, iniID, domain.InitiativePlanApproved)

	resp := doReq(t, http.MethodPut, ts.URL+"/api/v1/projects/"+projectID+"/executors", token, "",
		`{"kind":"hermes","model":"anthropic/claude-sonnet-4-5-20250929"}`)
	resp.Body.Close()

	url := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/guidance"
	_, g := getGuidance(t, url, token)

	if g.NextTask == nil {
		t.Fatal("next_task is absent; the executor problem should still let next_task through")
	}
	if g.RunBlockedReason == "" {
		t.Fatal("run_blocked_reason is empty, want the executor-not-ready problem")
	}
	if !strings.Contains(g.RunBlockedReason, "Docker is not running") {
		t.Errorf("run_blocked_reason = %q, want it to say Docker is not running", g.RunBlockedReason)
	}
}
