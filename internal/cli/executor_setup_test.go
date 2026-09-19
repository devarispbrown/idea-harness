package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/daemon"
	"github.com/devarispbrown/idea-harness/internal/executor"
)

// An unknown provider must fail before the multi-gigabyte image pull, and the
// refusal must list what the user can choose from.
func TestExecutorSetupRejectsAnUnknownProviderBeforePulling(t *testing.T) {
	f := newFakeDaemon(t)
	f.providers.Providers = []cliProvider{
		{Name: "anthropic"}, {Name: "deepseek"}, {Name: "gemini"},
		{Name: "ollama"}, {Name: "openai"}, {Name: "openrouter"},
	}

	_, _, err := run(t, "executors", "setup", "--kind", "hermes", "--model", "openia/gpt-5.6-luna",
		"--image", "example/does-not-exist:never", "--yes")
	if err == nil {
		t.Fatal("a misspelled provider was accepted")
	}
	msg := err.Error()
	if !strings.Contains(msg, "openia") {
		t.Errorf("the refusal does not name the provider it rejected: %v", err)
	}
	for _, want := range []string{"openai", "anthropic", "gemini", "deepseek", "openrouter", "ollama"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not list %s: %v", want, err)
		}
	}
	if strings.Contains(msg, "pull") {
		t.Errorf("the image was pulled before the provider was checked: %v", err)
	}
}

func TestExecutorSetupRejectsABareModel(t *testing.T) {
	newFakeDaemon(t)
	_, _, err := run(t, "executors", "setup", "--kind", "hermes", "--model", "gpt-5.6-luna",
		"--image", "example/does-not-exist:never", "--yes")
	if err == nil || !strings.Contains(err.Error(), "provider/model") {
		t.Errorf("a bare model was not refused with the provider/model form: %v", err)
	}
}

// The positional kind (`executors setup omp`) used to be discarded --
// cmdExecutorSetup only ever read --kind, so kind silently defaulted to
// "hermes" and the command pulled, verified and configured a hermes executor
// while claiming (via the omp argument) to set up omp. Because omp has no
// canonical default image, a correctly-parsed "omp" must fail asking for
// --image, not proceed as hermes.
func TestExecutorSetupHonorsThePositionalKind(t *testing.T) {
	newFakeDaemon(t)
	_, _, err := run(t, "executors", "setup", "omp", "--yes")
	if err == nil {
		t.Fatal("setup with a positional omp kind and no --image succeeded (it silently used hermes)")
	}
	if !strings.Contains(err.Error(), "no canonical omp image") {
		t.Errorf("positional kind was not honored: %v", err)
	}
}

// An unknown positional kind must be refused clearly, the same as an unknown
// --kind would be.
func TestExecutorSetupRejectsAnUnknownPositionalKind(t *testing.T) {
	newFakeDaemon(t)
	_, _, err := run(t, "executors", "setup", "bogus", "--yes")
	if err == nil || !strings.Contains(err.Error(), `unsupported executor kind "bogus"`) {
		t.Errorf("an unknown positional kind was not rejected: %v", err)
	}
}

// A positional kind that disagrees with --kind must be refused rather than
// silently picking one of the two.
func TestExecutorSetupRejectsConflictingPositionalAndFlagKind(t *testing.T) {
	newFakeDaemon(t)
	_, _, err := run(t, "executors", "setup", "hermes", "--kind", "omp", "--yes")
	if err == nil || !strings.Contains(err.Error(), "conflicting executor kind") {
		t.Errorf("a conflicting positional kind and --kind were not rejected: %v", err)
	}
}

// gpt-4.1-mini was the ICP walkthrough's motivating example for the
// compatibility table (docs/agents.md, "Known executor/model
// compatibility"), but investigating it found a fixable root cause
// (hermescfg.go now declares model.context_length explicitly -- see its own
// doc comment) rather than a genuine incompatibility, so it is not refused.
// It is also not verified, so setup still prints an untested/--smoke
// advisory on its way to the (expected, unrelated) pull failure.
func TestExecutorSetupWarnsButProceedsForAnUntestedModel(t *testing.T) {
	f := newFakeDaemon(t)
	f.providers.Providers = []cliProvider{{Name: "openai", Type: "openai", BaseURL: "https://api.openai.com/v1"}}
	_, stderr, err := run(t, "executors", "setup", "--kind", "hermes", "--model", "openai/gpt-4.1-mini",
		"--image", "example/does-not-exist:never", "--yes")
	if err == nil || !strings.Contains(err.Error(), "pull failed") {
		t.Fatalf("expected the (unrelated) image pull to fail, got: %v", err)
	}
	if !strings.Contains(stderr, "gpt-4.1-mini") || !strings.Contains(stderr, "--smoke") {
		t.Errorf("stderr does not carry the untested-model advisory: %q", stderr)
	}
}

// A row placed in the known-incompatible table is still refused before the
// multi-gigabyte image pull, the same as an unknown provider is -- the table
// exists for a combination that turns out to be genuinely, unfixably
// incompatible (none are known as of this fix; see compat.go).
func TestExecutorSetupRejectsAKnownIncompatibleCombination(t *testing.T) {
	f := newFakeDaemon(t)
	f.providers.Providers = []cliProvider{{Name: "openai", Type: "openai", BaseURL: "https://api.openai.com/v1"}}
	saved := executor.TestOnlySetKnownIncompatible("hermes", "openai/gpt-9-ghost", "a test fixture, not a real defect")
	t.Cleanup(saved)
	_, _, err := run(t, "executors", "setup", "--kind", "hermes", "--model", "openai/gpt-9-ghost",
		"--image", "example/does-not-exist:never", "--yes")
	if err == nil {
		t.Fatal("a known-incompatible combination was accepted")
	}
	if !strings.Contains(err.Error(), "gpt-9-ghost") {
		t.Errorf("the refusal does not name the model: %v", err)
	}
	if strings.Contains(err.Error(), "pull") {
		t.Errorf("the image was pulled before the compatibility check: %v", err)
	}
}

// A provider the daemon itself refuses (a preset whose base_url was
// overridden, say) must not be smoked, pulled for, or configured.
func TestExecutorSetupRejectsAProviderWithAProblem(t *testing.T) {
	f := newFakeDaemon(t)
	f.providers.Providers = []cliProvider{{Name: "openai", Problem: "its base_url may not be overridden"}}
	_, _, err := run(t, "executors", "setup", "--kind", "hermes", "--model", "openai/gpt-5.6-luna",
		"--image", "example/does-not-exist:never", "--yes")
	if err == nil || !strings.Contains(err.Error(), "base_url may not be overridden") {
		t.Errorf("a provider the daemon refuses was accepted: %v", err)
	}
}

// The provider's name and type are checked before the image pull, not at
// the agent's Start several gigabytes later.
func TestExecutorSetupRejectsAProviderTheAgentCannotUse(t *testing.T) {
	f := newFakeDaemon(t)
	f.providers.Providers = []cliProvider{{Name: "claude-proxy", Type: "anthropic", BaseURL: "https://proxy.example/v1"}}
	_, _, err := run(t, "executors", "setup", "--kind", "hermes", "--model", "claude-proxy/x",
		"--image", "example/does-not-exist:never", "--yes")
	if err == nil || !strings.Contains(err.Error(), "OpenAI chat completions") {
		t.Errorf("an anthropic-type proxy was accepted for the agent: %v", err)
	}
}

// models.implementer outranks executor.model, so setting (and smoking) one
// model while another runs must be said out loud.
func TestExecutorSetupWarnsWhenTheImplementerRoleOverrides(t *testing.T) {
	f := newFakeDaemon(t)
	f.providers.Providers = []cliProvider{{Name: "openai", Type: "openai", BaseURL: "https://api.openai.com/v1"}}
	f.providers.Models = map[string]string{"implementer": "anthropic/claude-sonnet-5"}
	_, stderr, _ := run(t, "executors", "setup", "--kind", "hermes", "--model", "openai/gpt-5.6-luna",
		"--image", "example/does-not-exist:never", "--yes")
	if !strings.Contains(stderr, "models.implementer") || !strings.Contains(stderr, "anthropic/claude-sonnet-5") {
		t.Errorf("no warning that models.implementer overrides --model:\n%s", stderr)
	}
}

// An auto-started daemon's stderr is daemon.log itself; the logger must not
// write each line to it twice.
func TestDaemonLogLinesAreWrittenOnce(t *testing.T) {
	t.Setenv("IDEA_HARNESS_STATE", t.TempDir())
	if err := os.MkdirAll(daemon.DefaultStateRoot(), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(logPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	newDaemonLogger(f).Info("hello once")
	data, _ := os.ReadFile(logPath())
	if n := strings.Count(string(data), "hello once"); n != 1 {
		t.Errorf("the line is in daemon.log %d times, want 1", n)
	}
}
