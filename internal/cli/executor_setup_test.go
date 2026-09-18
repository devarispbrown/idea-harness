package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/daemon"
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
