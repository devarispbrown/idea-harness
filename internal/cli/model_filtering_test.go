package cli

import (
	"fmt"
	"strings"
	"testing"
)

// This file covers the `idea model` filtering added because the bare command
// printed every model of every configured provider (~8k tokens on a real
// run) with no way to see less: `--all`, `--provider`/a positional filter
// under `list`, and a compact default (see listModels's own doc comment).

func manyModels(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("model-%d", i)
	}
	return out
}

func TestModelDefaultIsCuratedAndHintsAtMore(t *testing.T) {
	f := newFakeDaemon(t)
	f.providers = cliProvidersView{
		DefaultModel: "openai/model-0",
		Providers: []cliProvider{
			{Name: "openai", Label: "OpenAI", Configured: true, Ready: true, IsDefault: true, Models: manyModels(20)},
		},
	}

	stdout, _, err := run(t, "model")
	if err != nil {
		t.Fatalf("idea model: %v", err)
	}
	if !strings.Contains(stdout, "model-0") || !strings.Contains(stdout, "model-4") {
		t.Errorf("expected the curated set (first 5) shown:\n%s", stdout)
	}
	if strings.Contains(stdout, "model-19") {
		t.Errorf("expected the tail of a 20-model catalog to be hidden by default:\n%s", stdout)
	}
	if !strings.Contains(stdout, "+15 more") {
		t.Errorf("expected a count of the hidden models:\n%s", stdout)
	}
	if !strings.Contains(stdout, "idea model list --all") {
		t.Errorf("expected a hint pointing at --all:\n%s", stdout)
	}
}

func TestModelAllShowsEveryModel(t *testing.T) {
	f := newFakeDaemon(t)
	f.providers = cliProvidersView{
		Providers: []cliProvider{
			{Name: "openai", Label: "OpenAI", Configured: true, Models: manyModels(20)},
		},
	}
	stdout, _, err := run(t, "model", "list", "--all")
	if err != nil {
		t.Fatalf("idea model list --all: %v", err)
	}
	if !strings.Contains(stdout, "model-19") {
		t.Errorf("expected --all to show the full catalog:\n%s", stdout)
	}
	if strings.Contains(stdout, "more)") {
		t.Errorf("--all should not truncate:\n%s", stdout)
	}
}

func TestModelFilterByProviderFlag(t *testing.T) {
	f := newFakeDaemon(t)
	f.providers = cliProvidersView{
		Providers: []cliProvider{
			{Name: "openai", Label: "OpenAI", Configured: true, Models: manyModels(20)},
			{Name: "gemini", Label: "Gemini", Configured: true, Models: []string{"gemini-pro"}},
		},
	}
	stdout, _, err := run(t, "model", "--provider", "openai")
	if err != nil {
		t.Fatalf("idea model --provider openai: %v", err)
	}
	if strings.Contains(stdout, "Gemini") {
		t.Errorf("filtered output should not include the other provider:\n%s", stdout)
	}
	if !strings.Contains(stdout, "model-19") {
		t.Errorf("a single filtered provider should show its full catalog:\n%s", stdout)
	}
}

func TestModelListFilterByPositional(t *testing.T) {
	f := newFakeDaemon(t)
	f.providers = cliProvidersView{
		Providers: []cliProvider{
			{Name: "openai", Label: "OpenAI", Configured: true, Models: manyModels(20)},
			{Name: "gemini", Label: "Gemini", Configured: true, Models: []string{"gemini-pro"}},
		},
	}
	stdout, _, err := run(t, "model", "list", "gemini")
	if err != nil {
		t.Fatalf("idea model list gemini: %v", err)
	}
	if strings.Contains(stdout, "OpenAI") {
		t.Errorf("filtered output should not include the other provider:\n%s", stdout)
	}
	if !strings.Contains(stdout, "gemini-pro") {
		t.Errorf("expected the filtered provider's model:\n%s", stdout)
	}
}

func TestModelFilterRejectsAnUnknownProvider(t *testing.T) {
	f := newFakeDaemon(t)
	f.providers = cliProvidersView{
		Providers: []cliProvider{{Name: "openai", Label: "OpenAI", Configured: true}},
	}
	_, _, err := run(t, "model", "list", "nope")
	if err == nil {
		t.Fatal("want an error for an unknown provider, got nil")
	}
	if !strings.Contains(err.Error(), "openai") {
		t.Errorf("error should name what is configured, got: %v", err)
	}
}

func TestModelSmallProviderIsNotTruncated(t *testing.T) {
	f := newFakeDaemon(t)
	f.providers = cliProvidersView{
		Providers: []cliProvider{
			{Name: "local", Label: "Local", Configured: true, Models: []string{"a", "b"}},
		},
	}
	stdout, _, err := run(t, "model")
	if err != nil {
		t.Fatalf("idea model: %v", err)
	}
	if strings.Contains(stdout, "more)") {
		t.Errorf("a provider with fewer models than the curated count should not be truncated:\n%s", stdout)
	}
	if !strings.Contains(stdout, "a, b") {
		t.Errorf("expected both models shown:\n%s", stdout)
	}
}
