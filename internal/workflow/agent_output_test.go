package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
	"github.com/devarispbrown/idea-harness/internal/security"
)

// fakeAgentDocker stands in for docker. The container exits with
// $FAKE_AGENT_EXIT and prints a provider error, the way Hermes does on a 401.
const fakeAgentDocker = `#!/usr/bin/env bash
case "$1" in
  run) echo "cid-agent" ;;
  inspect) echo "exited ${FAKE_AGENT_EXIT:-0}" ;;
  logs) echo "Error: HTTP 401: invalid x-api-key sk-test-leaked-value" ;;
  rm) exit 0 ;;
  *) exit 1 ;;
esac
`

func newAgentExecuteFixture(t *testing.T, exit string) (*Engine, string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(fakeAgentDocker), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_AGENT_EXIT", exit)
	t.Setenv("OPENAI_API_KEY", "sk-test-leaked-value")

	eng, db, iniID := newExecuteFixture(t)
	eng.cfg.Executor = config.ExecutorConfig{Kind: config.ExecutorHermes, Model: "openai/gpt-5.6-luna", Image: "img"}
	task := &domain.Task{InitiativeID: iniID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(context.Background(), task, "", nil); err != nil {
		t.Fatal(err)
	}
	return eng, iniID
}

// A failed attempt used to report only "attempt failed". The reason is in the
// agent's own output, which Collect/Cancel discard, so it is read first.
func TestFailedAgentRunCarriesItsOutput(t *testing.T) {
	eng, iniID := newAgentExecuteFixture(t, "1")
	eng.SetRedactor(security.NewRedactor("sk-test-leaked-value"))
	_, err := eng.Execute(context.Background(), iniID)
	if err == nil {
		t.Fatal("a failed agent run succeeded")
	}
	if !strings.Contains(err.Error(), "HTTP 401") {
		t.Errorf("the error does not carry the agent's output: %v", err)
	}
	if !strings.Contains(err.Error(), "exit 1") {
		t.Errorf("the error does not carry the exit status: %v", err)
	}
	// The output is the agent's, and an agent can echo its own key. The error
	// is shown and logged, so it goes through the same redactor as the rest.
	if strings.Contains(err.Error(), "sk-test-leaked-value") {
		t.Errorf("the provider key leaked into the error: %v", err)
	}
}

// Hermes exits 0 even when every model call failed, so a clean exit that
// changed nothing is the common failure. It must not become an empty change
// set with no reason given -- and it must not be a hard failure either: the
// work root carries earlier attempts, so a task already done has nothing to
// change, and failing it stranded the initiative.
func TestAgentThatChangedNothingSaysWhy(t *testing.T) {
	eng, iniID := newAgentExecuteFixture(t, "0")
	cs, err := eng.Execute(context.Background(), iniID)
	if err != nil {
		t.Fatalf("an agent that changed nothing failed the run, which strands the task: %v", err)
	}
	findings, err := eng.db.ListFindingsByTarget(context.Background(), "change_set", cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Category == "agent_output" {
			if !strings.Contains(f.Evidence, "HTTP 401") {
				t.Errorf("the finding does not carry the agent's output: %q", f.Evidence)
			}
			if f.Severity.Blocking() {
				t.Error("the finding blocks, so an already-done task could never be approved")
			}
			return
		}
	}
	t.Errorf("no finding says the agent changed nothing: %+v", findings)
}

// A refusal to launch the agent must come before the planner's model call, or
// every refused run is paid for.
func TestMissingKeyIsRefusedBeforeThePlannerSpends(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{
		Network:  config.NetworkDisabled,
		Review:   config.ReviewConfig{MaxCycles: 3},
		Executor: config.ExecutorConfig{Kind: config.ExecutorHermes, Model: "openai/gpt-5.6-luna", Image: "img"},
	})
	eng.SetWorkRoot(t.TempDir())
	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "t", Statement: "s", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Execute(ctx, ini.ID); err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatalf("the missing key was not refused: %v", err)
	}
	if n := len(fp.Calls()); n != 0 {
		t.Errorf("%d model call(s) were made before the refusal", n)
	}
}

// OMP has no default image, so a missing one is refused before anything is
// spent, not by docker after the planner ran.
func TestOMPWithoutAnImageIsRefusedUpFront(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	e := &Engine{cfg: config.HarnessConfig{
		Executor: config.ExecutorConfig{Kind: config.ExecutorOMP, Model: "openai/gpt-5.6-luna"},
	}}
	if _, err := e.selectExecutor(); err == nil || !strings.Contains(err.Error(), "--image") {
		t.Errorf("OMP without an image was accepted: %v", err)
	}
}
