package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadHarness(t *testing.T, yaml string) (Config, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "harness.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(dir)
}

func TestIntegrationsConfigLoadsWithDefaults(t *testing.T) {
	cfg, err := loadHarness(t, `
integrations:
  slack:
    channel: C0123456
    allowed_users: [U0AAAA1]
    mention_on_judgment: [here, U0BBBB2]
  linear:
    team_key: ENG
`)
	if err != nil {
		t.Fatal(err)
	}
	s, l := cfg.Harness.Integrations.Slack, cfg.Harness.Integrations.Linear
	if s == nil || l == nil {
		t.Fatalf("sections not loaded: %+v", cfg.Harness.Integrations)
	}
	if !s.SocketModeEnabled() || s.BotTokenEnvName() != "SLACK_BOT_TOKEN" || s.AppTokenEnvName() != "SLACK_APP_TOKEN" {
		t.Fatalf("slack defaults wrong: %+v", s)
	}
	if len(s.EffectiveEvents()) != len(DefaultSlackEvents) {
		t.Fatal("unset events should mean the defaults")
	}
	if l.EffectiveInboundLabel() != "idea" || l.EffectivePollSeconds() != 60 || !l.InboundEnabled() || l.APIKeyEnvName() != "LINEAR_API_KEY" {
		t.Fatalf("linear defaults wrong: %+v", l)
	}
	if st := l.EffectiveStates(); st.InReview != "In Review" || st.Canceled != "Canceled" {
		t.Fatalf("state defaults wrong: %+v", st)
	}
}

func TestIntegrationsConfigRejects(t *testing.T) {
	cases := map[string]struct{ yaml, want string }{
		// The whole point of *_env: a token pasted into the committable file
		// is refused, the same rule every other secret-looking key follows.
		"inline token":       {"integrations:\n  slack:\n    channel: C1\n    bot_token: xoxb-123\n", "additional properties"},
		"unknown event":      {"integrations:\n  slack:\n    channel: C1\n    events: [nope]\n", "invalid"},
		"missing channel":    {"integrations:\n  slack:\n    events: [run.failed]\n", "channel"},
		"bad allowed user":   {"integrations:\n  slack:\n    channel: C1\n    allowed_users: [devaris]\n", "Slack user ID"},
		"bad mention":        {"integrations:\n  slack:\n    channel: C1\n    mention_on_judgment: [everyone]\n", "mention_on_judgment"},
		"missing team":       {"integrations:\n  linear:\n    project: X\n", "team_key"},
		"poll too fast":      {"integrations:\n  linear:\n    team_key: ENG\n    poll_interval_seconds: 5\n", "poll_interval_seconds"},
		"bad env name":       {"integrations:\n  linear:\n    team_key: ENG\n    api_key_env: \"lin api\"\n", "environment variable"},
		"unknown state name": {"integrations:\n  linear:\n    team_key: ENG\n    state_mapping:\n      backlog: B\n", "additional properties"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := loadHarness(t, tc.yaml)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v; want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestDefaultSlackEventsAreKnownKinds(t *testing.T) {
	known := map[string]bool{}
	for _, k := range IntegrationEventKinds {
		known[k] = true
	}
	for _, k := range DefaultSlackEvents {
		if !known[k] {
			t.Errorf("default slack event %q is not an IntegrationEventKind", k)
		}
	}
}

// WriteHarness is the settings endpoints' write path (model/executor
// changes). It re-marshals the whole struct, so an integrations section it
// failed to round-trip would be silently deleted by an unrelated UI change.
func TestWriteHarnessKeepsIntegrations(t *testing.T) {
	cfg, err := loadHarness(t, "integrations:\n  slack:\n    channel: C1\n    allowed_users: [U0AAAA1]\n  linear:\n    team_key: ENG\n    state_mapping:\n      done: Shipped\n")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := WriteHarness(dir, cfg.Harness); err != nil {
		t.Fatal(err)
	}
	back, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if back.Harness.Integrations.Slack == nil || back.Harness.Integrations.Slack.AllowedUsers[0] != "U0AAAA1" ||
		back.Harness.Integrations.Linear == nil || back.Harness.Integrations.Linear.EffectiveStates().Done != "Shipped" {
		t.Fatalf("integrations lost in round trip: %+v", back.Harness.Integrations)
	}
	// And an unconfigured harness writes no integrations key at all.
	dir2 := t.TempDir()
	if err := WriteHarness(dir2, Default().Harness); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir2, "harness.yaml"))
	if strings.Contains(string(data), "integrations") {
		t.Fatalf("empty integrations serialized:\n%s", data)
	}
}
