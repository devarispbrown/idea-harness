package daemon

import (
	"net/http"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestReRunAfterDenialSucceeds is the ICP round's A1/review#7 reproduction: the
// finding narrated a 409 when re-running a task whose change set the secret
// sweep had just denied. It does not reproduce through the ordinary path --
// wait for the denied execute's own operation to finish, then execute again --
// which this test drives end to end through the same HTTP surface the CLI and
// web UI use. It exists as a permanent regression guard either way: if a
// future change reintroduces a 409 here, this fails.
func TestReRunAfterDenialSucceeds(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	// A real credential value (an AWS access key shape), not a bare vocabulary
	// marker: the ICP-round fix makes markers non-blocking, so this is what
	// still denies a change set post-fix, the same as it did pre-fix.
	task := &domain.Task{InitiativeID: iniID, Title: "Add key AKIAIOSFODNN7EXAMPLE", Statement: "x", Priority: domain.PriorityMust}
	if err := pr.db.CreateTask(t.Context(), task, "", nil); err != nil {
		t.Fatal(err)
	}
	setStatus(t, rt, projectID, iniID, domain.InitiativePlanApproved)

	// First run: denied by the secret sweep.
	opID, err := rt.ExecuteInitiative(t.Context(), projectID, iniID)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	waitForOperation(t, ts, token, opID)

	sets, err := pr.db.ListChangeSets(t.Context(), iniID)
	if err != nil || len(sets) != 1 {
		t.Fatalf("execute produced %d change set(s) (err=%v), want 1", len(sets), err)
	}
	if sets[0].Status != domain.ChangeSetRejected {
		t.Fatalf("setup: expected the first change set denied, got %q", sets[0].Status)
	}
	ini, err := pr.db.GetInitiative(t.Context(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.Status != domain.InitiativePlanApproved {
		t.Fatalf("setup: initiative should be released back to plan_approved after a denied execute, got %q", ini.Status)
	}

	// Second run, over the same HTTP surface `idea execute` and the Build
	// page's Run button use: must be accepted, not refused with a 409.
	resp := doReq(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/execute", token, "", "")
	var out map[string]string
	decode(t, resp, &out)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("re-execute after a denial got %d, want 202: %v", resp.StatusCode, out)
	}
	waitForOperation(t, ts, token, out["operation_id"])

	sets, err = pr.db.ListChangeSets(t.Context(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 2 {
		t.Fatalf("re-execute after a denial produced %d change set(s), want 2 (CHG-1 denied, CHG-2 from the re-run)", len(sets))
	}
}
