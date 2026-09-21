package daemon

import (
	"net/http"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// TestExecuteRefusesSynchronouslyWhenPriorChangeSetUnreviewed is ICP re-run
// D5, driven over the real HTTP surface the CLI uses (POST .../execute).
//
// Before workflow.Engine.ExecutePrecondition existed, every one of Execute's
// own guards -- including this one, a task whose only change set never
// finished its adversarial review -- ran only inside the async
// launchOperation goroutine, strictly AFTER this handler had already
// answered 202 Accepted. `idea execute` printed "✓ execute started" and only
// then, a moment later (a real founder-journey run measured under a
// millisecond, at zero model cost), the operation failed in the background.
//
// This pins the fix: the same refusal must now come back as part of the POST
// response itself (409, not 202), with no operation created at all -- so the
// CLI's own "if err != nil { return err }" suppresses the success line
// entirely, instead of printing it and then discovering the run never
// happened.
func TestExecuteRefusesSynchronouslyWhenPriorChangeSetUnreviewed(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}

	task := &domain.Task{InitiativeID: iniID, Title: "add /health", Statement: "x", Priority: domain.PriorityMust}
	if err := pr.db.CreateTask(t.Context(), task, "", nil); err != nil {
		t.Fatal(err)
	}
	setStatus(t, rt, projectID, iniID, domain.InitiativePlanApproved)

	// The exact persisted shape ICP re-run defect #1 leaves behind: a change
	// set that reached review_required without ever completing an
	// adversarial review (Execute's own review call was cancelled
	// mid-review), with its task left `proposed` so it looks re-pickable.
	cs := &domain.ChangeSet{InitiativeID: iniID, TaskID: task.ID, PatchDigest: "unused-digest", BaseSnapshotID: "unused-digest"}
	if err := pr.db.CreateChangeSet(t.Context(), cs); err != nil {
		t.Fatal(err)
	}
	if err := pr.db.TransitionChangeSet(t.Context(), cs.ID, domain.ChangeSetDraft, domain.ChangeSetChecksRunning, store.ActorOrchestrator); err != nil {
		t.Fatal(err)
	}
	if err := pr.db.TransitionChangeSet(t.Context(), cs.ID, domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired, store.ActorOrchestrator); err != nil {
		t.Fatal(err)
	}

	opCountBefore, err := pr.db.ListOperationsByInitiative(t.Context(), iniID)
	if err != nil {
		t.Fatal(err)
	}

	resp := doReq(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/execute", token, "", "")
	var out map[string]any
	decode(t, resp, &out)
	if resp.StatusCode == http.StatusAccepted {
		t.Fatalf("execute was accepted (202) over a task whose only change set never finished its adversarial review; want a synchronous refusal: %v", out)
	}
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("execute refusal status = %d, want 409: %v", resp.StatusCode, out)
	}
	msg, _ := out["error"].(string)
	if !strings.Contains(msg, "never finished its adversarial review") {
		t.Errorf("refusal message missing the reason: %v", out)
	}
	if !strings.Contains(msg, "rerun") {
		t.Errorf("refusal message does not name the way forward (rerun): %v", out)
	}

	opsAfter, err := pr.db.ListOperationsByInitiative(t.Context(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	if len(opsAfter) != len(opCountBefore) {
		t.Fatalf("a synchronously-refused execute still created an operation: %d before, %d after", len(opCountBefore), len(opsAfter))
	}

	ini, err := pr.db.GetInitiative(t.Context(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.Status != domain.InitiativePlanApproved {
		t.Fatalf("a synchronously-refused execute must not touch the initiative's status, got %q", ini.Status)
	}
}
