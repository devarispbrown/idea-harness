package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// This file covers the five plan-editing CLI verbs added because the API and
// web UI could add/merge/split/depends-on/reorder a task and the CLI could
// not (ICP re-run 5, docs/plans/deferred.md): an evaluator editing a plan from
// the CLI had to drop to raw HTTP twice. See tasks.go's own section comment.

// seedTaskInitiative registers just enough for the plan-editing verbs -- an
// initiative list, so resolveInitiative succeeds -- without touching
// `GET .../tasks`: f.seedInitiative's own version of that route is static and
// cannot be overridden afterward (net/http.ServeMux panics on a duplicate
// pattern), and every test below needs its own task list.
func seedTaskInitiative(f *fakeDaemon) {
	f.initiatives = []map[string]any{
		{"id": "ini-1", "title": "Build usage-based billing", "status": "plan_approved"},
	}
	f.mux.HandleFunc("GET /api/v1/projects/{projectId}/initiatives",
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(f.initiatives)
		})
}

// ---------- add-task ----------

func TestAddTaskSendsRequirementsAndDependsOn(t *testing.T) {
	f := newFakeDaemon(t)
	seedTaskInitiative(f)

	var body string
	f.mux.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/tasks",
		func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			body = string(raw)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(cliTask{
				ID: "task-2", Code: "TASK-2", Title: "Wire up metering",
				RequirementIDs: []string{"REQ-1"}, DependsOn: []string{},
			})
		})

	stdout, _, err := run(t, "add-task", "Wire up metering", "--requirement", "REQ-1",
		"--depends-on", "TASK-1", "--priority", "must")
	if err != nil {
		t.Fatalf("idea add-task: %v", err)
	}
	if !strings.Contains(stdout, "TASK-2") {
		t.Errorf("expected the new task's code in output:\n%s", stdout)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("request body not JSON: %v", err)
	}
	if reqs, _ := sent["requirement_ids"].([]any); len(reqs) != 1 || reqs[0] != "REQ-1" {
		t.Errorf("requirement_ids not sent correctly: %v", sent)
	}
	if deps, _ := sent["depends_on"].([]any); len(deps) != 1 || deps[0] != "TASK-1" {
		t.Errorf("depends_on not sent correctly: %v", sent)
	}
	if sent["priority"] != "must" {
		t.Errorf("priority not sent: %v", sent)
	}
}

// TestAddTaskRequiresARequirementLocally is the main refusal: the server
// enforces "at least one requirement" synchronously (handleAddTask), and the
// CLI says so before the round trip rather than relaying the server's 400.
func TestAddTaskRequiresARequirementLocally(t *testing.T) {
	f := newFakeDaemon(t)
	seedTaskInitiative(f)
	before := len(f.requests)

	_, _, err := run(t, "add-task", "Wire up metering")
	if err == nil {
		t.Fatal("add-task with no --requirement: want an error")
	}
	if !strings.Contains(err.Error(), "--requirement") {
		t.Errorf("error should name the missing flag, got: %v", err)
	}
	if len(f.requests) != before {
		t.Errorf("add-task reached the daemon before validating locally: %v", f.requests[before:])
	}
}

// ---------- merge-tasks ----------

func seedTwoTasks(f *fakeDaemon) {
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/tasks", []map[string]any{
		{"id": "task-1", "code": "TASK-1", "title": "Implement billing", "priority": "must"},
		{"id": "task-2", "code": "TASK-2", "title": "Implement metering", "priority": "should"},
	})
}

func TestMergeTasksSendsBothIDsAndNeedsYes(t *testing.T) {
	f := newFakeDaemon(t)
	seedTaskInitiative(f)
	seedTwoTasks(f)

	var body string
	f.mux.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/tasks/merge",
		func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			body = string(raw)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(cliTask{ID: "task-1", Code: "TASK-1", Title: "Implement billing"})
		})

	// Main refusal: without --yes, nothing is sent to the merge endpoint.
	_, stderr, err := run(t, "merge-tasks", "TASK-1", "TASK-2")
	if err == nil {
		t.Fatal("merge-tasks without --yes: want an error")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("error should name --yes, got: %v", err)
	}
	if !strings.Contains(stderr, "TASK-2") || !strings.Contains(stderr, "deleted") {
		t.Errorf("stderr should warn what will be deleted:\n%s", stderr)
	}
	if body != "" {
		t.Errorf("merge endpoint was called before --yes was given")
	}

	// Success path, by code.
	stdout, _, err := run(t, "merge-tasks", "TASK-1", "TASK-2", "--yes")
	if err != nil {
		t.Fatalf("idea merge-tasks --yes: %v", err)
	}
	if !strings.Contains(stdout, "merged") || !strings.Contains(stdout, "TASK-2") {
		t.Errorf("expected a merge confirmation naming TASK-2:\n%s", stdout)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("request body not JSON: %v", err)
	}
	ids, _ := sent["task_ids"].([]any)
	if len(ids) != 2 || ids[0] != "task-1" || ids[1] != "task-2" {
		t.Errorf("task_ids not sent as resolved ids in order: %v", sent)
	}
}

func TestMergeTasksRejectsTheSameTaskTwiceLocally(t *testing.T) {
	f := newFakeDaemon(t)
	seedTaskInitiative(f)
	seedTwoTasks(f)

	_, _, err := run(t, "merge-tasks", "TASK-1", "task-1", "--yes")
	if err == nil {
		t.Fatal("merge-tasks TASK-1 task-1: want an error")
	}
	if !strings.Contains(err.Error(), "distinct") {
		t.Errorf("error should say the two refs name the same task, got: %v", err)
	}
	if f.asked("/tasks/merge") {
		t.Error("a self-merge reached the daemon")
	}
}

// ---------- split-task ----------

func TestSplitTaskPairsTitleAndRequirementsInOrder(t *testing.T) {
	f := newFakeDaemon(t)
	seedTaskInitiative(f)
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/tasks", []map[string]any{
		{"id": "task-3", "code": "TASK-3", "title": "Build billing", "priority": "must"},
	})
	var body string
	f.mux.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/tasks/{taskId}/split",
		func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			body = string(raw)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]cliTask{
				{ID: "task-4", Code: "TASK-4", Title: "Build API"},
				{ID: "task-5", Code: "TASK-5", Title: "Build UI"},
			})
		})

	stdout, _, err := run(t, "split-task", "TASK-3",
		"--title", "Build API", "--requirements", "REQ-1,REQ-2",
		"--title", "Build UI", "--requirements", "REQ-3",
		"--yes")
	if err != nil {
		t.Fatalf("idea split-task: %v", err)
	}
	if !strings.Contains(stdout, "TASK-4") || !strings.Contains(stdout, "TASK-5") {
		t.Errorf("expected both replacement codes in output:\n%s", stdout)
	}
	var sent struct {
		Tasks []splitPart `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("request body not JSON: %v", err)
	}
	if len(sent.Tasks) != 2 {
		t.Fatalf("expected 2 replacement specs, got %d", len(sent.Tasks))
	}
	if sent.Tasks[0].Title != "Build API" || len(sent.Tasks[0].RequirementIDs) != 2 {
		t.Errorf("first replacement wrong: %+v", sent.Tasks[0])
	}
	if sent.Tasks[1].Title != "Build UI" || len(sent.Tasks[1].RequirementIDs) != 1 {
		t.Errorf("second replacement wrong: %+v", sent.Tasks[1])
	}
}

// TestSplitTaskRequiresTwoReplacementsLocally is the main refusal: the server
// 400s "tasks must name at least 2 replacement tasks" (handleSplitTask); the
// CLI says so before the round trip.
func TestSplitTaskRequiresTwoReplacementsLocally(t *testing.T) {
	f := newFakeDaemon(t)
	seedTaskInitiative(f)
	before := len(f.requests)

	_, _, err := run(t, "split-task", "TASK-3", "--title", "Build API", "--requirements", "REQ-1", "--yes")
	if err == nil {
		t.Fatal("split-task with one replacement: want an error")
	}
	if !strings.Contains(err.Error(), "at least 2") {
		t.Errorf("error should name the minimum, got: %v", err)
	}
	if len(f.requests) != before {
		t.Errorf("split-task reached the daemon before validating locally: %v", f.requests[before:])
	}
}

// ---------- depends-on ----------

func TestDependsOnSetsAndClears(t *testing.T) {
	f := newFakeDaemon(t)
	seedTaskInitiative(f)
	seedTwoTasks(f)

	var bodies []string
	f.mux.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/tasks/{taskId}/depends-on",
		func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			bodies = append(bodies, string(raw))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(cliTask{ID: "task-2", Code: "TASK-2", DependsOn: []string{"task-1"}})
		})

	stdout, _, err := run(t, "depends-on", "TASK-2", "--on", "TASK-1")
	if err != nil {
		t.Fatalf("idea depends-on --on: %v", err)
	}
	if !strings.Contains(stdout, "TASK-1") {
		t.Errorf("expected the dependency's code in output:\n%s", stdout)
	}
	var sent map[string]any
	_ = json.Unmarshal([]byte(bodies[0]), &sent)
	if deps, _ := sent["depends_on"].([]any); len(deps) != 1 || deps[0] != "TASK-1" {
		t.Errorf("depends_on not sent as the given ref: %v", sent)
	}

	// --clear must send depends_on: [] (not null): a nil slice JSON-encodes to
	// null, which the server reads as "omitted" and refuses with "depends_on
	// is required" -- the opposite of what --clear asks for.
	stdout, _, err = run(t, "depends-on", "TASK-2", "--clear")
	if err != nil {
		t.Fatalf("idea depends-on --clear: %v", err)
	}
	if !strings.Contains(stdout, "cleared") {
		t.Errorf("expected a clear confirmation:\n%s", stdout)
	}
	if !strings.Contains(bodies[1], `"depends_on":[]`) {
		t.Errorf("--clear must send an empty array, not null: %s", bodies[1])
	}
}

// TestDependsOnRefusesSelfDependencyLocally is one main refusal: the server
// also refuses this ("a task cannot depend on itself"), and the CLI pre-empts
// it when the ref resolves locally.
func TestDependsOnRefusesSelfDependencyLocally(t *testing.T) {
	f := newFakeDaemon(t)
	seedTaskInitiative(f)
	seedTwoTasks(f)

	_, _, err := run(t, "depends-on", "TASK-1", "--on", "TASK-1")
	if err == nil {
		t.Fatal("depends-on TASK-1 --on TASK-1: want an error")
	}
	if !strings.Contains(err.Error(), "cannot depend on itself") {
		t.Errorf("error should match the server's own wording, got: %v", err)
	}
	if f.asked("/depends-on") {
		t.Error("a self-dependency reached the daemon")
	}
}

// TestDependsOnSurfacesACycleRefusalVerbatim: the other main refusal
// (a cycle) is server-detected, and its message is already actionable
// (store.ErrDependsOnCycle carries no "store: " prefix) -- the CLI must not
// reword or swallow it.
func TestDependsOnSurfacesACycleRefusalVerbatim(t *testing.T) {
	f := newFakeDaemon(t)
	seedTaskInitiative(f)
	seedTwoTasks(f)
	f.fail("POST /api/v1/projects/{projectId}/initiatives/{id}/tasks/{taskId}/depends-on",
		http.StatusBadRequest, "that dependency would create a cycle: TASK-1 -> TASK-2 -> TASK-1")

	_, _, err := run(t, "depends-on", "TASK-1", "--on", "TASK-2")
	if err == nil {
		t.Fatal("want an error")
	}
	if err.Error() != "that dependency would create a cycle: TASK-1 -> TASK-2 -> TASK-1" {
		t.Errorf("cycle refusal was not surfaced verbatim, got: %v", err)
	}
}

// ---------- reorder ----------

func TestReorderSendsResolvedIDsInOrder(t *testing.T) {
	f := newFakeDaemon(t)
	seedTaskInitiative(f)
	seedTwoTasks(f)

	var body string
	f.mux.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/tasks/reorder",
		func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			body = string(raw)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]cliTask{
				{ID: "task-2", Code: "TASK-2", Title: "Implement metering"},
				{ID: "task-1", Code: "TASK-1", Title: "Implement billing"},
			})
		})

	stdout, _, err := run(t, "reorder", "TASK-2", "TASK-1")
	if err != nil {
		t.Fatalf("idea reorder: %v", err)
	}
	if !strings.Contains(stdout, "1. "+"TASK-2") && !strings.Contains(stdout, "TASK-2") {
		t.Errorf("expected the new order in output:\n%s", stdout)
	}
	var sent map[string]any
	_ = json.Unmarshal([]byte(body), &sent)
	ids, _ := sent["task_ids"].([]any)
	if len(ids) != 2 || ids[0] != "task-2" || ids[1] != "task-1" {
		t.Errorf("task_ids not sent as resolved ids in the given order: %v", sent)
	}
}

// TestReorderRequiresTheFullListLocally is the main refusal, and the one this
// verb exists to pre-empt: store.ReorderTasks answers a count mismatch with
// (until this round) a "store: "-prefixed message, and the CLI must give its
// own clear, actionable error before ever sending the request.
func TestReorderRequiresTheFullListLocally(t *testing.T) {
	f := newFakeDaemon(t)
	seedTaskInitiative(f)
	seedTwoTasks(f)

	_, _, err := run(t, "reorder", "TASK-1")
	if err == nil {
		t.Fatal("reorder with 1 of 2 tasks: want an error")
	}
	if !strings.Contains(err.Error(), "exactly 2") {
		t.Errorf("error should name the expected count, got: %v", err)
	}
	if strings.Contains(err.Error(), "store:") {
		t.Errorf("error leaks the internal package prefix: %v", err)
	}
	if f.asked("/tasks/reorder") {
		t.Error("an incomplete reorder reached the daemon")
	}
}

// ---------- the whole journey, no raw HTTP ----------

// TestPlanEditingJourneyNoRawHTTP runs add-task, merge-tasks, split-task,
// depends-on and reorder back to back against one stateful fake daemon -- the
// exact journey the ICP evaluator had to fall back to raw HTTP for twice,
// because the CLI had no verb for any of the five.
func TestPlanEditingJourneyNoRawHTTP(t *testing.T) {
	f := newFakeDaemon(t)
	seedTaskInitiative(f)

	tasks := []map[string]any{
		{"id": "task-1", "code": "TASK-1", "title": "Implement billing", "priority": "must",
			"requirement_ids": []string{"REQ-1"}, "depends_on": []string{}},
	}
	nextTaskNum := 2
	listTasks := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tasks)
	}
	f.mux.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/tasks", listTasks)

	// 1. add-task, with a requirement.
	f.mux.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/tasks",
		func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Title          string   `json:"title"`
				RequirementIDs []string `json:"requirement_ids"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			nt := map[string]any{
				"id": "task-2", "code": "TASK-2", "title": body.Title, "priority": "should",
				"requirement_ids": body.RequirementIDs, "depends_on": []string{},
			}
			tasks = append(tasks, nt)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(nt)
		})
	if _, _, err := run(t, "add-task", "Implement metering", "--requirement", "REQ-2"); err != nil {
		t.Fatalf("add-task: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("add-task did not create a task: %+v", tasks)
	}

	// 2. merge-tasks: fold TASK-2 into TASK-1.
	f.mux.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/tasks/merge",
		func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				TaskIDs []string `json:"task_ids"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			keep := body.TaskIDs[0]
			var merged map[string]any
			out := tasks[:0]
			for _, task := range tasks {
				if task["id"] == body.TaskIDs[1] {
					continue
				}
				if task["id"] == keep {
					merged = task
				}
				out = append(out, task)
			}
			tasks = out
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(merged)
		})
	if _, _, err := run(t, "merge-tasks", "TASK-1", "TASK-2", "--yes"); err != nil {
		t.Fatalf("merge-tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("merge-tasks did not reduce to 1 task: %+v", tasks)
	}

	// 3. split-task: split the survivor into two.
	f.mux.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/tasks/{taskId}/split",
		func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Tasks []splitPart `json:"tasks"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			tasks = tasks[:0]
			var created []map[string]any
			for _, p := range body.Tasks {
				code := "TASK-" + strconv.Itoa(nextTaskNum)
				nextTaskNum++
				nt := map[string]any{
					"id": "t-" + code, "code": code, "title": p.Title, "priority": "should",
					"requirement_ids": p.RequirementIDs, "depends_on": []string{},
				}
				tasks = append(tasks, nt)
				created = append(created, nt)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(created)
		})
	stdout, _, err := run(t, "split-task", "TASK-1",
		"--title", "Build API", "--requirements", "REQ-1",
		"--title", "Build UI", "--requirements", "REQ-2",
		"--yes")
	if err != nil {
		t.Fatalf("split-task: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("split-task did not produce 2 tasks: %+v", tasks)
	}
	firstCode, secondCode := tasks[0]["code"].(string), tasks[1]["code"].(string)
	if !strings.Contains(stdout, firstCode) || !strings.Contains(stdout, secondCode) {
		t.Errorf("split-task output missing a replacement code:\n%s", stdout)
	}

	// 4. depends-on: the second replacement depends on the first, named by id.
	f.mux.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/tasks/{taskId}/depends-on",
		func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				DependsOn []string `json:"depends_on"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			taskID := tasks[1]["id"].(string)
			for i, task := range tasks {
				if task["id"] == taskID {
					tasks[i]["depends_on"] = body.DependsOn
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(tasks[1])
		})
	firstID := tasks[0]["id"].(string)
	if _, _, err := run(t, "depends-on", secondCode, "--on", firstID); err != nil {
		t.Fatalf("depends-on: %v", err)
	}
	deps, _ := tasks[1]["depends_on"].([]string)
	if len(deps) != 1 || deps[0] != firstID {
		t.Fatalf("depends-on did not set the dependency by id: %+v", tasks[1])
	}

	// 5. reorder: swap the two.
	f.mux.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/tasks/reorder",
		func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				TaskIDs []string `json:"task_ids"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			byID := map[string]map[string]any{}
			for _, task := range tasks {
				byID[task["id"].(string)] = task
			}
			reordered := make([]map[string]any, len(body.TaskIDs))
			for i, id := range body.TaskIDs {
				reordered[i] = byID[id]
			}
			tasks = reordered
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(tasks)
		})
	if _, _, err := run(t, "reorder", secondCode, firstCode); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	if tasks[0]["code"] != secondCode || tasks[1]["code"] != firstCode {
		t.Fatalf("reorder did not apply the new order: %+v", tasks)
	}

	// Not one of these five went anywhere near raw HTTP: every request in the
	// log is a path this package's own client built.
	for _, reqLine := range f.requests {
		if strings.Contains(reqLine, "curl") {
			t.Fatalf("unexpected raw request: %s", reqLine)
		}
	}
}
