package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// b3Fixture creates a plan_approved initiative, ready for task-graph tests.
func b3Fixture(t *testing.T) (ts *httptest.Server, token, projectID, iniID string, pr *projectRuntime) {
	t.Helper()
	rt, ts, tok := newTestServer(t)
	pid, iid := newTestProjectInitiative(t, rt, ts, tok)
	p, err := rt.EnsureProject(t.Context(), pid)
	if err != nil {
		t.Fatal(err)
	}
	setStatus(t, rt, pid, iid, domain.InitiativePlanApproved)
	return ts, tok, pid, iid, p
}

func mustCreateRequirement(t *testing.T, pr *projectRuntime, iniID string) *domain.Requirement {
	t.Helper()
	r := &domain.Requirement{InitiativeID: iniID, Statement: "do the thing", Priority: domain.PriorityMust}
	if err := pr.db.CreateRequirement(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	return r
}

func mustCreateTask(t *testing.T, pr *projectRuntime, iniID, title string, reqID string, dependsOn []string) *domain.Task {
	t.Helper()
	task := &domain.Task{InitiativeID: iniID, Title: title, Statement: "stmt for " + title, Priority: domain.PriorityMust}
	if err := pr.db.CreateTaskMulti(t.Context(), task, []string{reqID}, dependsOn); err != nil {
		t.Fatal(err)
	}
	return task
}

// TestDeleteTaskWithDependentsNamesThem proves review B3's readable 409: the
// error names the specific dependent task(s) instead of the old
// undifferentiated "another task depends on this task" message.
func TestDeleteTaskWithDependentsNamesThem(t *testing.T) {
	ts, token, projectID, iniID, pr := b3Fixture(t)
	req := mustCreateRequirement(t, pr, iniID)
	base := mustCreateTask(t, pr, iniID, "Base task", req.ID, nil)
	dependent := mustCreateTask(t, pr, iniID, "Dependent task", req.ID, []string{base.ID})

	url := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/tasks/" + base.ID
	resp := doReq(t, http.MethodDelete, url, token, "", "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
	body := errorBody(t, resp)
	if !strings.Contains(body, dependent.Code) {
		t.Fatalf("409 body %q does not name the dependent task %s", body, dependent.Code)
	}
	if !strings.Contains(body, base.Code) {
		t.Fatalf("409 body %q does not name the task being deleted %s", body, base.Code)
	}
}

// TestMergeTasksUnionsRequirementsAndConcatenatesStatements proves review
// B3's merge endpoint: requirements union, statements concatenate,
// dependencies rewire, and the merged task is gone.
func TestMergeTasksUnionsRequirementsAndConcatenatesStatements(t *testing.T) {
	ts, token, projectID, iniID, pr := b3Fixture(t)
	req1 := mustCreateRequirement(t, pr, iniID)
	req2 := mustCreateRequirement(t, pr, iniID)
	upstream := mustCreateTask(t, pr, iniID, "Upstream", req1.ID, nil)
	keep := mustCreateTask(t, pr, iniID, "Keep me", req1.ID, []string{upstream.ID})
	merge := mustCreateTask(t, pr, iniID, "Merge me", req2.ID, nil)
	downstream := mustCreateTask(t, pr, iniID, "Downstream", req1.ID, []string{merge.ID})

	url := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/tasks/merge"
	resp := doReq(t, http.MethodPost, url, token, "", `{"task_ids":["`+keep.ID+`","`+merge.ID+`"]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("merge: %d: %s", resp.StatusCode, errorBody(t, resp))
	}
	var merged domain.Task
	if err := json.NewDecoder(resp.Body).Decode(&merged); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if merged.Statement != "stmt for Keep me\n\nstmt for Merge me" {
		t.Fatalf("statements not concatenated: %q", merged.Statement)
	}

	// The merged task is gone.
	if _, err := pr.db.GetTask(t.Context(), merge.ID); err == nil {
		t.Fatal("merged task still exists")
	}

	// Requirements unioned onto the survivor.
	edges, err := pr.db.ListGraphEdgesFrom(t.Context(), iniID, "task", keep.ID)
	if err != nil {
		t.Fatal(err)
	}
	reqIDs := map[string]bool{}
	depIDs := map[string]bool{}
	for _, e := range edges {
		switch e.EdgeType {
		case domain.EdgeImplements:
			reqIDs[e.ToID] = true
		case domain.EdgeDependsOn:
			depIDs[e.ToID] = true
		}
	}
	if !reqIDs[req1.ID] || !reqIDs[req2.ID] {
		t.Fatalf("expected both requirements on survivor, got %v", reqIDs)
	}
	if !depIDs[upstream.ID] {
		t.Fatalf("expected survivor to keep its own upstream dependency, got %v", depIDs)
	}

	// The downstream task, which depended on the merged task, now depends on
	// the survivor instead.
	downstreamEdges, err := pr.db.ListGraphEdgesFrom(t.Context(), iniID, "task", downstream.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range downstreamEdges {
		if e.EdgeType == domain.EdgeDependsOn && e.ToID == keep.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("downstream task was not rewired to depend on the survivor: %+v", downstreamEdges)
	}
}

// TestSplitTaskCreatesReplacementsAndDeletesOriginal proves review B3's split
// endpoint.
func TestSplitTaskCreatesReplacementsAndDeletesOriginal(t *testing.T) {
	ts, token, projectID, iniID, pr := b3Fixture(t)
	req := mustCreateRequirement(t, pr, iniID)
	upstream := mustCreateTask(t, pr, iniID, "Upstream", req.ID, nil)
	original := mustCreateTask(t, pr, iniID, "Big task", req.ID, []string{upstream.ID})
	downstream := mustCreateTask(t, pr, iniID, "Downstream", req.ID, []string{original.ID})

	url := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/tasks/" + original.ID + "/split"
	body := `{"tasks":[{"title":"Part A","statement":"a"},{"title":"Part B","statement":"b"}]}`
	resp := doReq(t, http.MethodPost, url, token, "", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("split: %d: %s", resp.StatusCode, errorBody(t, resp))
	}
	var created []domain.Task
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(created) != 2 {
		t.Fatalf("expected 2 new tasks, got %d", len(created))
	}

	if _, err := pr.db.GetTask(t.Context(), original.ID); err == nil {
		t.Fatal("original task still exists after split")
	}

	for _, nt := range created {
		edges, err := pr.db.ListGraphEdgesFrom(t.Context(), iniID, "task", nt.ID)
		if err != nil {
			t.Fatal(err)
		}
		hasReq, hasUpstream := false, false
		for _, e := range edges {
			if e.EdgeType == domain.EdgeImplements && e.ToID == req.ID {
				hasReq = true
			}
			if e.EdgeType == domain.EdgeDependsOn && e.ToID == upstream.ID {
				hasUpstream = true
			}
		}
		if !hasReq {
			t.Errorf("new task %s missing the original's requirement", nt.Code)
		}
		if !hasUpstream {
			t.Errorf("new task %s missing the original's upstream dependency", nt.Code)
		}
	}

	downstreamEdges, err := pr.db.ListGraphEdgesFrom(t.Context(), iniID, "task", downstream.ID)
	if err != nil {
		t.Fatal(err)
	}
	newIDs := map[string]bool{created[0].ID: true, created[1].ID: true}
	seen := 0
	for _, e := range downstreamEdges {
		if e.EdgeType == domain.EdgeDependsOn && newIDs[e.ToID] {
			seen++
		}
	}
	if seen != 2 {
		t.Fatalf("expected downstream to depend on both new tasks, got %d matches", seen)
	}
}

// TestSplitTaskPartialFailureLeavesOriginalIntact is review M4: an invalid
// second (or later) replacement spec must refuse the WHOLE split before any
// write, not create the earlier replacements and leave the original
// undeleted -- the half-applied-write bug this branch had already fixed for
// PATCH.
func TestSplitTaskPartialFailureLeavesOriginalIntact(t *testing.T) {
	ts, token, projectID, iniID, pr := b3Fixture(t)
	req := mustCreateRequirement(t, pr, iniID)
	original := mustCreateTask(t, pr, iniID, "Big task", req.ID, nil)

	url := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/tasks/" + original.ID + "/split"
	// The second replacement names a requirement that does not exist.
	body := `{"tasks":[{"title":"Part A","statement":"a"},{"title":"Part B","statement":"b","requirement_ids":["REQ-999-does-not-exist"]}]}`
	resp := doReq(t, http.MethodPost, url, token, "", body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unresolvable requirement reference, got %d: %s", resp.StatusCode, errorBody(t, resp))
	}

	// Nothing was applied: the original still exists, and "Part A" was never
	// created.
	if _, err := pr.db.GetTask(t.Context(), original.ID); err != nil {
		t.Fatalf("original task must survive a refused split: %v", err)
	}
	tasks, err := pr.db.ListTasks(t.Context(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected only the original task to exist, got %d: %+v", len(tasks), tasks)
	}
}

// TestSplitTaskRejectsEmptyRequirementIDs is review M4: requirement_ids: []
// (explicitly empty, not omitted) used to produce a replacement task with no
// requirement at all, violating "Added tasks take a requirement".
func TestSplitTaskRejectsEmptyRequirementIDs(t *testing.T) {
	ts, token, projectID, iniID, pr := b3Fixture(t)
	req := mustCreateRequirement(t, pr, iniID)
	original := mustCreateTask(t, pr, iniID, "Big task", req.ID, nil)

	url := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/tasks/" + original.ID + "/split"
	body := `{"tasks":[{"title":"Part A","statement":"a","requirement_ids":[]},{"title":"Part B","statement":"b"}]}`
	resp := doReq(t, http.MethodPost, url, token, "", body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for an empty requirement_ids, got %d: %s", resp.StatusCode, errorBody(t, resp))
	}
	if _, err := pr.db.GetTask(t.Context(), original.ID); err != nil {
		t.Fatalf("original task must survive a refused split: %v", err)
	}
}

// TestSetTaskDependsOnRejectsCycle proves review B3's "set a task's
// depends_on (validate for cycles)".
func TestSetTaskDependsOnRejectsCycle(t *testing.T) {
	ts, token, projectID, iniID, pr := b3Fixture(t)
	req := mustCreateRequirement(t, pr, iniID)
	a := mustCreateTask(t, pr, iniID, "A", req.ID, nil)
	b := mustCreateTask(t, pr, iniID, "B", req.ID, []string{a.ID}) // B depends on A

	// Now try to make A depend on B, which would create A -> B -> A.
	// depends_on takes titles, codes, OR ids (docs/api.md) -- this leg uses the
	// code, TestSetTaskDependsOnAcceptsTaskID below covers the id form.
	url := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/tasks/" + a.ID + "/depends-on"
	resp := doReq(t, http.MethodPost, url, token, "", `{"depends_on":["`+b.Code+`"]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a cycle, got %d: %s", resp.StatusCode, errorBody(t, resp))
	}
	// This request is caught by the handler's own pre-check (wouldCycle),
	// never reaching store.ErrDependsOnCycle's defense-in-depth path -- see
	// TestErrDependsOnCycleHasNoInternalPrefix (store package) for that one.
	if body := errorBody(t, resp); strings.Contains(body, "store:") {
		t.Fatalf("error body = %q leaks an internal package prefix", body)
	}

	// A valid, acyclic change succeeds.
	c := mustCreateTask(t, pr, iniID, "C", req.ID, nil)
	resp2 := doReq(t, http.MethodPost, url, token, "", `{"depends_on":["`+c.Code+`"]}`)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected a valid depends_on to succeed, got %d: %s", resp2.StatusCode, errorBody(t, resp2))
	}
}

// TestSetTaskDependsOnAcceptsTaskID is defect #3 from the ICP re-run:
// docs/api.md documents depends-on as taking "titles, codes, or ids", but
// resolveTaskRefs's lookup table (byNorm) was built from title and code only
// -- a task's own id, the least ambiguous reference a caller could send, was
// refused with "unknown task". Also proves a cycle is still caught when the
// offending edge is named by id rather than code (review #3).
func TestSetTaskDependsOnAcceptsTaskID(t *testing.T) {
	ts, token, projectID, iniID, pr := b3Fixture(t)
	req := mustCreateRequirement(t, pr, iniID)
	a := mustCreateTask(t, pr, iniID, "A", req.ID, nil)
	b := mustCreateTask(t, pr, iniID, "B", req.ID, nil)

	url := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/tasks/" + a.ID + "/depends-on"
	resp := doReq(t, http.MethodPost, url, token, "", `{"depends_on":["`+b.ID+`"]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected a task id to resolve, got %d: %s", resp.StatusCode, errorBody(t, resp))
	}
	var updated struct {
		DependsOn []string `json:"depends_on"`
	}
	decode(t, resp, &updated)
	if len(updated.DependsOn) != 1 || updated.DependsOn[0] != b.ID {
		t.Fatalf("depends_on = %v, want [%s]", updated.DependsOn, b.ID)
	}

	// A cycle named by id (B -> A, closing A -> B -> A) is still refused.
	urlB := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/tasks/" + b.ID + "/depends-on"
	respCycle := doReq(t, http.MethodPost, urlB, token, "", `{"depends_on":["`+a.ID+`"]}`)
	if respCycle.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a cycle named by id, got %d: %s", respCycle.StatusCode, errorBody(t, respCycle))
	}
}

// TestMergeTasksRefusesSelfMerge is review H4: task_ids: [A, A] used to pass
// both existence guards, then DeleteTask(A) deleted the survivor and the
// response was null. It must be refused, and A must still exist.
func TestMergeTasksRefusesSelfMerge(t *testing.T) {
	ts, token, projectID, iniID, pr := b3Fixture(t)
	req := mustCreateRequirement(t, pr, iniID)
	a := mustCreateTask(t, pr, iniID, "A", req.ID, nil)

	url := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/tasks/merge"
	resp := doReq(t, http.MethodPost, url, token, "", `{"task_ids":["`+a.ID+`","`+a.ID+`"]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a self-merge, got %d: %s", resp.StatusCode, errorBody(t, resp))
	}
	// This request is caught by the handler's own pre-check (identical
	// task_ids), never reaching store.ErrSelfMerge's defense-in-depth path
	// -- see TestErrSelfMergeHasNoInternalPrefix (store package) for that one.
	if body := errorBody(t, resp); strings.Contains(body, "store:") {
		t.Fatalf("error body = %q leaks an internal package prefix", body)
	}
	if _, err := pr.db.GetTask(t.Context(), a.ID); err != nil {
		t.Fatalf("task must survive a refused self-merge: %v", err)
	}
}

// TestMergeTasksRejectsCycle is review H4's own example: A depends on B, B
// depends on C. Merging (A, C) would give A->B from the union of A's own
// outgoing dependency, and B->A from rewiring B (a dependent of C) onto the
// survivor -- a cycle. It must be refused, and NOTHING about the merge may
// be applied (one transaction): C must still exist, and A/B's edges must be
// exactly what they were before the attempt.
func TestMergeTasksRejectsCycle(t *testing.T) {
	ts, token, projectID, iniID, pr := b3Fixture(t)
	req := mustCreateRequirement(t, pr, iniID)
	c := mustCreateTask(t, pr, iniID, "C", req.ID, nil)
	b := mustCreateTask(t, pr, iniID, "B", req.ID, []string{c.ID}) // B depends on C
	a := mustCreateTask(t, pr, iniID, "A", req.ID, []string{b.ID}) // A depends on B

	url := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/tasks/merge"
	resp := doReq(t, http.MethodPost, url, token, "", `{"task_ids":["`+a.ID+`","`+c.ID+`"]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a merge that creates a cycle, got %d: %s", resp.StatusCode, errorBody(t, resp))
	}
	// review LOW #10: store.ErrMergeCycle's Error() text reaches the client
	// verbatim and used to carry the package's internal "store: " prefix.
	if body := errorBody(t, resp); !strings.HasPrefix(body, "merging would create a dependency cycle") {
		t.Fatalf("error body = %q, want the cycle message with no internal prefix", body)
	}

	// Nothing was applied: C still exists, unchanged.
	if _, err := pr.db.GetTask(t.Context(), c.ID); err != nil {
		t.Fatalf("C must still exist after a refused merge: %v", err)
	}
	bEdges, err := pr.db.ListGraphEdgesFrom(t.Context(), iniID, "task", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	bDepsOnC := false
	for _, e := range bEdges {
		if e.EdgeType == domain.EdgeDependsOn {
			if e.ToID != c.ID {
				t.Fatalf("B's depends_on must be unchanged (still only C), got edge to %s", e.ToID)
			}
			bDepsOnC = true
		}
	}
	if !bDepsOnC {
		t.Fatal("B must still depend on C after a refused merge")
	}
	aEdges, err := pr.db.ListGraphEdgesFrom(t.Context(), iniID, "task", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range aEdges {
		if e.EdgeType == domain.EdgeDependsOn && e.ToID != b.ID {
			t.Fatalf("A's depends_on must be unchanged (still only B), got edge to %s", e.ToID)
		}
	}
}

// TestMergeTasksDedupesDependentEdges is review H4: graph_edges has no
// unique constraint, so a task that depended on BOTH merged tasks used to
// get two identical depends_on edges to the survivor after a merge. It must
// get exactly one.
func TestMergeTasksDedupesDependentEdges(t *testing.T) {
	ts, token, projectID, iniID, pr := b3Fixture(t)
	req := mustCreateRequirement(t, pr, iniID)
	keep := mustCreateTask(t, pr, iniID, "Keep", req.ID, nil)
	merge := mustCreateTask(t, pr, iniID, "Merge", req.ID, nil)
	both := mustCreateTask(t, pr, iniID, "Both", req.ID, []string{keep.ID, merge.ID})

	url := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/tasks/merge"
	resp := doReq(t, http.MethodPost, url, token, "", `{"task_ids":["`+keep.ID+`","`+merge.ID+`"]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("merge: %d: %s", resp.StatusCode, errorBody(t, resp))
	}
	resp.Body.Close()

	edges, err := pr.db.ListGraphEdgesFrom(t.Context(), iniID, "task", both.ID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range edges {
		if e.EdgeType == domain.EdgeDependsOn && e.ToID == keep.ID {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 depends_on edge to the survivor, got %d", count)
	}
}

// concurrentGetOrPost fires an HTTP request and reports its status code and
// any transport error, WITHOUT calling t.Fatal -- safe to call from a
// goroutine other than the test's own (the testing package forbids FailNow
// there).
func concurrentGetOrPost(method, url, token, body string) (status int, err error) {
	var r *http.Request
	if body != "" {
		r, err = http.NewRequest(method, url, strings.NewReader(body))
	} else {
		r, err = http.NewRequest(method, url, nil)
	}
	if err != nil {
		return 0, err
	}
	if token != "" {
		r.Header.Set("X-Idea-Token", token)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

// TestSetTaskDependsOnConcurrentRequestsNeverCreateACycle is review M5's
// end-to-end guarantee: two real concurrent set-depends_on requests (A->B
// and B->A) must never both succeed. lockPlanEdit serializes plan edits per
// initiative, and SetTaskDependencies (store.TestSetTaskDependenciesRejectsCycleAgainstCurrentGraph
// covers that one deterministically -- goroutine timing alone cannot
// reliably force the narrow interleaving the original bug needed) re-checks
// for a cycle inside the write transaction. This test fires both requests
// with real goroutines as a best-effort end-to-end check that the observable
// outcome is always "exactly one wins, and the graph has no cycle either
// way", on top of that deterministic coverage.
func TestSetTaskDependsOnConcurrentRequestsNeverCreateACycle(t *testing.T) {
	ts, token, projectID, iniID, pr := b3Fixture(t)
	req := mustCreateRequirement(t, pr, iniID)
	a := mustCreateTask(t, pr, iniID, "A", req.ID, nil)
	b := mustCreateTask(t, pr, iniID, "B", req.ID, nil)

	urlA := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/tasks/" + a.ID + "/depends-on"
	urlB := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/tasks/" + b.ID + "/depends-on"

	var wg sync.WaitGroup
	statuses := make([]int, 2)
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		statuses[0], errs[0] = concurrentGetOrPost(http.MethodPost, urlA, token, `{"depends_on":["`+b.Code+`"]}`)
	}()
	go func() {
		defer wg.Done()
		statuses[1], errs[1] = concurrentGetOrPost(http.MethodPost, urlB, token, `{"depends_on":["`+a.Code+`"]}`)
	}()
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	oks, bads := 0, 0
	for _, status := range statuses {
		switch status {
		case http.StatusOK:
			oks++
		case http.StatusBadRequest:
			bads++
		default:
			t.Fatalf("unexpected status %d (want 200 or 400)", status)
		}
	}
	if oks != 1 || bads != 1 {
		t.Fatalf("expected exactly one request to succeed and one to be refused as a cycle, got statuses %v", statuses)
	}

	// Whatever the outcome, the final graph must have no cycle: exactly one
	// of the two dependency edges was written, never both.
	edgesA, err := pr.db.ListGraphEdgesFrom(t.Context(), iniID, "task", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	edgesB, err := pr.db.ListGraphEdgesFrom(t.Context(), iniID, "task", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	aDependsOnB, bDependsOnA := false, false
	for _, e := range edgesA {
		if e.EdgeType == domain.EdgeDependsOn && e.ToID == b.ID {
			aDependsOnB = true
		}
	}
	for _, e := range edgesB {
		if e.EdgeType == domain.EdgeDependsOn && e.ToID == a.ID {
			bDependsOnA = true
		}
	}
	if aDependsOnB && bDependsOnA {
		t.Fatal("both edges were written -- A depends on B AND B depends on A, a cycle")
	}
	if !aDependsOnB && !bDependsOnA {
		t.Fatal("expected exactly one of the two dependency edges to have been written")
	}
}

// TestSetTaskDependsOnRejectsMissingField is the review low-priority item: a
// body with no depends_on field at all (or a misspelled key) used to decode
// to a nil/zero slice and silently clear every existing dependency. It must
// be refused with 400 instead, and existing dependencies must survive.
func TestSetTaskDependsOnRejectsMissingField(t *testing.T) {
	ts, token, projectID, iniID, pr := b3Fixture(t)
	req := mustCreateRequirement(t, pr, iniID)
	a := mustCreateTask(t, pr, iniID, "A", req.ID, nil)
	b := mustCreateTask(t, pr, iniID, "B", req.ID, []string{a.ID}) // B depends on A

	url := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/tasks/" + b.ID + "/depends-on"

	for _, body := range []string{`{}`, `{"deppends_on":["` + a.Code + `"]}`} {
		resp := doReq(t, http.MethodPost, url, token, "", body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("body %s: expected 400 for a missing depends_on field, got %d: %s", body, resp.StatusCode, errorBody(t, resp))
		}
	}

	// B's existing dependency on A must survive untouched.
	edges, err := pr.db.ListGraphEdgesFrom(t.Context(), iniID, "task", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range edges {
		if e.EdgeType == domain.EdgeDependsOn && e.ToID == a.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("B's existing dependency on A was silently cleared by a request with no depends_on field")
	}

	// An EXPLICIT empty array is still the legitimate "clear all
	// dependencies" request.
	resp := doReq(t, http.MethodPost, url, token, "", `{"depends_on":[]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for an explicit empty depends_on, got %d: %s", resp.StatusCode, errorBody(t, resp))
	}
	edges2, err := pr.db.ListGraphEdgesFrom(t.Context(), iniID, "task", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range edges2 {
		if e.EdgeType == domain.EdgeDependsOn {
			t.Fatalf("expected all dependencies cleared by an explicit empty depends_on, got %+v", e)
		}
	}
}
