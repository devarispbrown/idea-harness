package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A Settings save must keep what a person wrote into harness.yaml by hand:
// their comments, and the order of the keys they already had.
func TestWriteHarnessPreservesCommentsAndOrder(t *testing.T) {
	dir := t.TempDir()
	orig := `# Team config -- ask #platform before changing the executor.
network: disabled
executor:
  kind: internal # the placeholder until Docker is on CI
review:
  max_cycles: 2 # two is enough for this repo
integrations:
  linear:
    team_key: ENG
    # The label our triage bot applies.
    inbound_label: idea
`
	if err := os.WriteFile(filepath.Join(dir, "harness.yaml"), []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := cfg.Harness
	h.Integrations.Linear.PollIntervalSeconds = 120
	h.Budget.DefaultInitiativeUSD = 5
	if err := WriteHarness(dir, h); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "harness.yaml"))
	got := string(data)
	for _, want := range []string{
		"# Team config -- ask #platform before changing the executor.",
		"# the placeholder until Docker is on CI",
		"# two is enough for this repo",
		"# The label our triage bot applies.",
		"poll_interval_seconds: 120",
		"default_initiative_usd: 5",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("written harness.yaml lost %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "network:") > strings.Index(got, "executor:") || strings.Index(got, "executor:") > strings.Index(got, "review:") {
		t.Errorf("existing key order not kept:\n%s", got)
	}
	// And it round-trips: what was written loads to what was asked for.
	back, err := Load(dir)
	if err != nil {
		t.Fatalf("written file does not load: %v\n%s", err, got)
	}
	if back.Harness.Integrations.Linear.PollIntervalSeconds != 120 || back.Harness.Budget.DefaultInitiativeUSD != 5 || back.Harness.Review.MaxCycles != 2 {
		t.Fatalf("round trip = %+v", back.Harness)
	}
}

// The write path refuses what Load would refuse, before writing anything.
func TestWriteHarnessRefusesWhatLoadRefuses(t *testing.T) {
	cases := map[string]func(h *HarnessConfig){
		"slack without a channel": func(h *HarnessConfig) { h.Integrations.Slack = &SlackConfig{} },
		"bad slack user id": func(h *HarnessConfig) {
			h.Integrations.Slack = &SlackConfig{Channel: "C1", AllowedUsers: []string{"bob"}}
		},
		"linear poll too fast": func(h *HarnessConfig) { h.Integrations.Linear = &LinearConfig{TeamKey: "ENG", PollIntervalSeconds: 5} },
		"token value in env name": func(h *HarnessConfig) {
			h.Integrations.Linear = &LinearConfig{TeamKey: "ENG", APIKeyEnv: "lin_api_123 abc"}
		},
		"secret-looking mcp literal": func(h *HarnessConfig) {
			h.Executor.MCPServers = []MCPServerConfig{{Name: "gh", Command: "gh-mcp", Env: []MCPNameValue{{Name: "GITHUB_TOKEN", Value: "ghp_abc"}}}}
		},
		"unsafe openhands url": func(h *HarnessConfig) { h.Executor.OpenHands.URL = "http://agents.example.com" },
		"eval enabled without a cap": func(h *HarnessConfig) {
			h.Automations.Eval = &EvalAutomationConfig{Enabled: true, Model: "openai/gpt-4o-mini"}
		},
		"eval enabled without a model": func(h *HarnessConfig) {
			h.Automations.Eval = &EvalAutomationConfig{Enabled: true, MaxCostUSD: 1}
		},
		"negative budget": func(h *HarnessConfig) { h.Budget.DefaultInitiativeUSD = -1 },
		"bad event name": func(h *HarnessConfig) {
			h.Integrations.Slack = &SlackConfig{Channel: "C1", Events: []string{"change_set.exploded"}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			h := Default().Harness
			mutate(&h)
			if err := WriteHarness(dir, h); err == nil {
				t.Fatal("write accepted a config Load refuses")
			}
			if _, err := os.Stat(filepath.Join(dir, "harness.yaml")); !os.IsNotExist(err) {
				t.Fatal("a refused write still wrote the file")
			}
		})
	}
}

func TestAutomationsAreOffUnlessEnabled(t *testing.T) {
	dir := t.TempDir()
	yml := "automations:\n  eval:\n    model: openai/gpt-4o-mini\n    max_cost_usd: 2\n  metric_measurement: {}\n"
	if err := os.WriteFile(filepath.Join(dir, "harness.yaml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := cfg.Harness.Automations
	if a.Eval == nil || a.Eval.Enabled || a.MetricMeasurement == nil || a.MetricMeasurement.Enabled {
		t.Fatalf("a configured but not enabled job must be off: %+v", a)
	}
	if a.Eval.EffectiveIntervalHours() != 24 {
		t.Fatalf("default interval = %d", a.Eval.EffectiveIntervalHours())
	}
}
