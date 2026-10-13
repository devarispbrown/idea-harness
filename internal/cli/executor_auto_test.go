package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/workflow"
)

const autoWarning = "NO REAL CODE WILL BE WRITTEN: executor.kind is unset (auto) and Docker is not running"

// `idea execute` prints the daemon's fallback warning, not the old soft
// placeholder line, when auto fell back.
func TestExecutePrintsTheAutoFallbackWarning(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	inProjectWithModel(t, f)
	f.json("POST /api/v1/projects/{projectId}/initiatives/{id}/execute", map[string]string{
		"operation_id": "op-1", "executor_kind": "auto", "executor_resolved": "internal", "executor_warning": autoWarning + " ...",
	})
	stdout, _, err := run(t, "execute")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, autoWarning) {
		t.Fatalf("execute did not print the fallback warning:\n%s", stdout)
	}
	if strings.Contains(stdout, "writes a placeholder (not real code)") {
		t.Fatalf("execute printed both warnings:\n%s", stdout)
	}
}

// Resolved to a real agent: nothing to warn about.
func TestExecuteIsQuietWhenAutoResolvesToARealAgent(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	inProjectWithModel(t, f)
	f.json("POST /api/v1/projects/{projectId}/initiatives/{id}/execute", map[string]string{
		"operation_id": "op-1", "executor_kind": "auto", "executor_resolved": "hermes",
	})
	stdout, _, err := run(t, "execute")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "⚠") {
		t.Fatalf("execute warned about a run that uses hermes:\n%s", stdout)
	}
}

// doctor reports the daemon's resolution, loudly, without failing.
func TestDoctorShowsTheDaemonsExecutorFallback(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	inProjectWithModel(t, f)
	f.providers = openaiProvider(true, "")
	f.json("GET /api/v1/projects/{projectId}/executors", []cliExecutor{
		{Name: "internal", Resolution: &workflow.ExecutorResolution{Requested: "auto", Resolved: "internal", Warning: autoWarning}},
		{Name: "hermes"},
	})
	stdout, _, err := run(t, "doctor")
	if err != nil {
		t.Fatalf("an executor fallback failed doctor; it is a warning: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "⚠ executor: "+autoWarning) {
		t.Fatalf("doctor did not show the fallback warning:\n%s", stdout)
	}
}

// With no daemon, doctor resolves in this shell -- and with no Docker probe
// in a test binary, auto falls back and says so.
func TestDoctorResolvesTheExecutorLocallyWithNoDaemon(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	state := t.TempDir()
	t.Setenv("IDEA_HARNESS_STATE", state)
	t.Setenv("IDEA_HARNESS_ADDR", "127.0.0.1:1")
	t.Setenv("OPENAI_API_KEY", "sk-test")
	if err := os.WriteFile(filepath.Join(state, "token"), []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cwd, ".idea-harness"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".idea-harness", "harness.yaml"),
		[]byte("network: disabled\ndefault_model: openai/gpt-4o-mini\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, _ := run(t, "doctor")
	if !strings.Contains(stdout, "⚠ executor: NO REAL CODE WILL BE WRITTEN") || !strings.Contains(stdout, "Docker is not running") {
		t.Fatalf("doctor did not report the local fallback:\n%s", stdout)
	}
}

func TestParseACPCommand(t *testing.T) {
	for in, want := range map[string][]string{
		"claude-code-acp":               {"claude-code-acp"},
		"gemini --experimental-acp":     {"gemini", "--experimental-acp"},
		`["sh", "-c", "exec my agent"]`: {"sh", "-c", "exec my agent"},
		"  openhands   acp ":            {"openhands", "acp"},
		"":                              nil,
	} {
		got, err := parseACPCommand(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("parseACPCommand(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := parseACPCommand(`["unterminated`); err == nil {
		t.Error("a malformed JSON array was accepted")
	}
}

func TestExecutorSetupACPNeedsACommandAndRejectsStrayFlags(t *testing.T) {
	_, _, err := run(t, "executors", "setup", "--kind", "acp", "--image", "img", "--yes")
	if err == nil || !strings.Contains(err.Error(), "--acp-command is required") {
		t.Fatalf("acp without --acp-command: %v", err)
	}
	_, _, err = run(t, "executors", "setup", "--kind", "acp", "--acp-command", "claude-code-acp", "--yes")
	if err == nil || !strings.Contains(err.Error(), "--image is required") {
		t.Fatalf("acp without --image: %v", err)
	}
	_, _, err = run(t, "executors", "setup", "--kind", "hermes", "--acp-command", "x", "--yes")
	if err == nil || !strings.Contains(err.Error(), "only apply to --kind acp") {
		t.Fatalf("--acp-command with hermes: %v", err)
	}
}
