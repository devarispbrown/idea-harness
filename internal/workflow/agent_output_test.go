package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
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
// set with no reason given.
func TestAgentThatChangedNothingSaysWhy(t *testing.T) {
	eng, iniID := newAgentExecuteFixture(t, "0")
	_, err := eng.Execute(context.Background(), iniID)
	if err == nil {
		t.Fatal("an agent run that changed nothing produced a change set")
	}
	if !strings.Contains(err.Error(), "without changing any file") || !strings.Contains(err.Error(), "HTTP 401") {
		t.Errorf("the error does not say why nothing changed: %v", err)
	}
}
