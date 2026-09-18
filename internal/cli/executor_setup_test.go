package cli

import (
	"strings"
	"testing"
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

