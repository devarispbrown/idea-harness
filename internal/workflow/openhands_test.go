package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/executor/ohfake"
	"github.com/devarispbrown/idea-harness/internal/model"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// newOpenHandsFixture is an engine configured for executor kind openhands
// against a fake Agent Server, with one task and a workspace holding app.txt.
func newOpenHandsFixture(t *testing.T, agent func(c *ohfake.Conv, dir string)) (*Engine, *store.DB, string, *ohfake.Server) {
	t.Helper()
	srv := ohfake.New(t, "session-key-for-tests-123")
	srv.Agent = agent
	eng, db := newTestEngine(t, model.NewFakeProvider(), config.HarnessConfig{
		Network: config.NetworkDisabled,
		Review:  config.ReviewConfig{MaxCycles: 3},
		Executor: config.ExecutorConfig{Kind: config.ExecutorOpenHands, Model: "anthropic/claude-sonnet-5",
			OpenHands: config.OpenHandsConfig{URL: srv.URL()}},
	})
	t.Cleanup(eng.DetachRemoteRuns)
	eng.SetDotEnv(map[string]string{"OPENHANDS_API_KEY": "session-key-for-tests-123", "ANTHROPIC_API_KEY": "sk-ant-test-0123456789abcdef"})
	eng.SetWorkRoot(t.TempDir())
	ws := t.TempDir()
	eng.SetWorkspaceRoot(ws)
	if err := os.WriteFile(filepath.Join(ws, "app.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "Greet", Statement: "change the greeting", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	return eng, db, ini.ID, srv
}

// End to end through Execute: the remote agent's edits come back into the
// work root and become an ordinary change set -- the engine's own diff of
// the files, awaiting review -- with the remote run collected and the
// server recorded on the run manifest.
func TestExecuteThroughOpenHandsProducesAChangeSet(t *testing.T) {
	eng, db, iniID, srv := newOpenHandsFixture(t, func(c *ohfake.Conv, dir string) {
		_ = os.WriteFile(filepath.Join(dir, "app.txt"), []byte("hello, remote\n"), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "greet.go"), []byte("package main\n"), 0o644)
		c.Say("changed the greeting")
	})
	ctx := context.Background()
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Status != domain.ChangeSetReviewRequired || cs.Executor != "openhands" {
		t.Fatalf("change set = %s by %s, want review_required by openhands", cs.Status, cs.Executor)
	}
	files := map[string]string{}
	for _, f := range eng.StructuredDiff(&cs) {
		files[f.Path] = f.Content
	}
	if files["app.txt"] != "hello, remote\n" || files["greet.go"] != "package main\n" {
		t.Fatalf("diff = %v", files)
	}
	if !strings.Contains(cs.AgentOutput, "agent: changed the greeting") {
		t.Fatalf("agent output = %q", cs.AgentOutput)
	}
	runs, err := db.ListRemoteRuns(ctx, false, 0)
	if err != nil || len(runs) != 1 || runs[0].Status != domain.RemoteRunCollected || runs[0].ID != cs.CreatedByRunID {
		t.Fatalf("remote runs = %+v (%v)", runs, err)
	}
	if n := len(srv.Creates()); n != 1 {
		t.Fatalf("conversations = %d", n)
	}
}

// The returned files go through the same secret sweep as a local agent's: a
// credential the remote agent wrote rejects the change set, and the raw bytes
// are never persisted.
func TestExecuteThroughOpenHandsSweepsReturnedFiles(t *testing.T) {
	eng, db, iniID, _ := newOpenHandsFixture(t, func(c *ohfake.Conv, dir string) {
		_ = os.WriteFile(filepath.Join(dir, "config.txt"), []byte("aws_key = AKIAIOSFODNN7EXAMPLE\n"), 0o644)
	})
	ctx := context.Background()
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Status != domain.ChangeSetRejected {
		t.Fatalf("change set = %s, want rejected by the secret sweep", cs.Status)
	}
	findings, err := db.ListFindingsByInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	var swept bool
	for _, f := range findings {
		swept = swept || (f.TargetID == cs.ID && f.Code == domain.DenyCodeSecretSweep)
	}
	if !swept {
		t.Fatal("no secret_sweep finding on the change set")
	}
}

// A failed remote run fails Execute with the agent's own reason, and Execute
// never selects openhands without a server.
func TestExecuteThroughOpenHandsReportsRemoteFailure(t *testing.T) {
	eng, _, iniID, _ := newOpenHandsFixture(t, func(c *ohfake.Conv, dir string) {
		c.Emit(map[string]any{"kind": "AgentErrorEvent", "source": "agent", "error": "model said no"})
		c.SetStatus("error")
	})
	_, err := eng.Execute(context.Background(), iniID)
	if err == nil || !strings.Contains(err.Error(), "model said no") {
		t.Fatalf("Execute = %v, want the agent's error", err)
	}

	eng.cfg.Executor.OpenHands.URL = ""
	if _, err := eng.selectExecutor(); err == nil || !strings.Contains(err.Error(), "executor.openhands.url") {
		t.Fatalf("selectExecutor without a URL = %v", err)
	}
}

// ReconcileRemoteRuns is the daemon-start hook: it re-attaches to what a
// previous daemon left, through the configured session-key variable, and a
// run whose conversation is gone from the server is failed with a reason.
func TestReconcileRemoteRunsFailsAVanishedRun(t *testing.T) {
	eng, db, _, srv := newOpenHandsFixture(t, nil)
	ctx := context.Background()
	rec, _, err := db.CreateRemoteRun(ctx, domain.RemoteRun{Executor: "openhands", IdempotencyKey: "k", TaskID: "t", Attempt: 1,
		ServerURL: srv.URL(), ConversationID: "00000000-0000-8000-8000-000000000000", Status: domain.RemoteRunRunning})
	if err != nil {
		t.Fatal(err)
	}
	n, err := eng.ReconcileRemoteRuns(ctx)
	if err != nil || n != 1 {
		t.Fatalf("reconcile = %d, %v", n, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, _ := db.GetRemoteRun(ctx, rec.ID)
		if got.Status == domain.RemoteRunFailed {
			if !strings.Contains(got.Error, "no longer exists") {
				t.Fatalf("reason = %q", got.Error)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run is %s, want failed", got.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Auto never resolves to openhands, and an explicit openhands names its
// server on the resolution.
func TestResolveExecutorOpenHandsIsExplicitOnly(t *testing.T) {
	h := config.HarnessConfig{Executor: config.ExecutorConfig{Kind: config.ExecutorOpenHands, OpenHands: config.OpenHandsConfig{URL: "https://oh.example"}}}
	res := ResolveExecutor(context.Background(), h, nil, nil)
	if res.Resolved != "openhands" || res.Server != "https://oh.example" || res.Warning != "" {
		t.Fatalf("resolution = %+v", res)
	}
	restore := SetAutoDockerProbeForTest(func(context.Context) bool { return true })
	defer restore()
	auto := ResolveExecutor(context.Background(), config.HarnessConfig{DefaultModel: "anthropic/claude-sonnet-5",
		Executor: config.ExecutorConfig{OpenHands: config.OpenHandsConfig{URL: "https://oh.example"}}}, nil,
		func(string) string { return "set" })
	if auto.Resolved == "openhands" {
		t.Fatal("auto picked openhands")
	}
}
