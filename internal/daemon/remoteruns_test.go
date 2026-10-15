package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// The listing serves an initiative's remote runs newest first with the
// task's code and the progress log's last line in place of the log itself,
// and the project-wide listing filters to the live ones with ?active=true.
func TestListRemoteRuns(t *testing.T) {
	rt, base, token, projectID, iniID, runID := runFixture(t)
	ctx := context.Background()
	pr, err := rt.EnsureProject(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := pr.db.ListTasks(ctx, iniID)
	if err != nil || len(tasks) == 0 {
		t.Fatalf("no tasks: %v", err)
	}
	task := tasks[0]

	first, _, err := pr.db.CreateRemoteRun(ctx, domain.RemoteRun{Executor: "openhands", IdempotencyKey: "k1", InitiativeID: iniID,
		TaskID: task.ID, Attempt: 1, ServerURL: "https://oh.example", RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	failed, msg, conv := domain.RemoteRunFailed, "server said 500", "conv-1"
	if err := pr.db.UpdateRemoteRun(ctx, first.ID, store.RemoteRunUpdate{Status: &failed, Error: &msg, ConversationID: &conv}); err != nil {
		t.Fatal(err)
	}
	if _, err := pr.db.AppendRemoteRunProgress(ctx, first.ID, "", "ev-2", 2, []string{"agent: reading files", "tool: npm test"}); err != nil {
		t.Fatal(err)
	}
	second, _, err := pr.db.CreateRemoteRun(ctx, domain.RemoteRun{Executor: "openhands", IdempotencyKey: "k2", InitiativeID: iniID,
		TaskID: task.ID, Attempt: 2, ServerURL: "https://oh.example"})
	if err != nil {
		t.Fatal(err)
	}

	var got []remoteRunView
	resp := httpDo(t, http.MethodGet, base+"/initiatives/"+iniID+"/remote-runs", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(got) != 2 {
		t.Fatalf("got %d runs: %+v", len(got), got)
	}
	byID := map[string]remoteRunView{got[0].ID: got[0], got[1].ID: got[1]}
	f := byID[first.ID]
	if f.Status != domain.RemoteRunFailed || f.Error != msg || f.ConversationID != conv || f.TaskCode != task.Code ||
		f.Attempt != 1 || f.ServerURL != "https://oh.example" || f.LastEvent != "tool: npm test" || f.EventCount != 2 ||
		f.LastEventID != "ev-2" || f.RunID != runID || f.FinishedAt == "" {
		t.Fatalf("failed run view = %+v", f)
	}
	if s := byID[second.ID]; s.Status != domain.RemoteRunDispatching || s.Attempt != 2 || s.FinishedAt != "" {
		t.Fatalf("live run view = %+v", s)
	}

	var active []remoteRunView
	resp = httpDo(t, http.MethodGet, base+"/remote-runs?active=true", token, "")
	if err := json.NewDecoder(resp.Body).Decode(&active); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(active) != 1 || active[0].ID != second.ID {
		t.Fatalf("active = %+v", active)
	}

	// An unknown initiative is a 404.
	if r := httpDo(t, http.MethodGet, base+"/initiatives/no-such/remote-runs", token, ""); r.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown initiative: %d", r.StatusCode)
	}
}

func TestLastProgressLine(t *testing.T) {
	for in, want := range map[string]string{"": "", "a\nb\n": "b", "a\n\n  \n": "a", "only": "only"} {
		if got := lastProgressLine(in); got != want {
			t.Errorf("lastProgressLine(%q) = %q, want %q", in, got, want)
		}
	}
}
