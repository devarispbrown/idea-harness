package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
)

// inProjectWithModel gives the cwd a .idea-harness whose default_model routes to
// openai, so the key check has something to check.
func inProjectWithModel(t *testing.T, f *fakeDaemon) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cwd, ".idea-harness"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".idea-harness", "harness.yaml"),
		[]byte("network: disabled\ndefault_model: openai/gpt-4o-mini\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/doctor", []map[string]any{})
}

// openaiProvider is the daemon's view of one provider.
func openaiProvider(keySet bool, problem string) cliProvidersView {
	return cliProvidersView{
		DefaultModel: "openai/gpt-4o-mini",
		Providers: []cliProvider{{
			Name: "openai", Label: "OpenAI", KeyEnv: "OPENAI_API_KEY",
			KeySet: keySet, Ready: keySet && problem == "", Problem: problem,
		}},
	}
}

// The finding: keyProblems resolves the key with os.Getenv, so asking it from
// the CLI reports the CLI's environment. The daemon reads its own once, at
// start, so a key exported afterwards is invisible to it — and doctor said
// "all checks passed" in exactly the state four documents credit it with
// catching. `idea model set` has flagged this correctly all along.
func TestDoctorFailsWhenOnlyTheDaemonLacksTheKey(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	inProjectWithModel(t, f)
	f.providers = openaiProvider(false, "")
	t.Setenv("OPENAI_API_KEY", "sk-in-this-shell-only")

	stdout, _, err := run(t, "doctor")
	if err == nil {
		t.Fatal("doctor passed while the daemon was missing the configured model's key")
	}
	if !strings.Contains(stdout, "OPENAI_API_KEY") {
		t.Errorf("doctor did not name the variable:\n%s", stdout)
	}
	if !strings.Contains(stdout, "idea daemon stop") {
		t.Errorf("doctor did not name the fix:\n%s", stdout)
	}
	if strings.Contains(stdout, "all checks passed") {
		t.Errorf("doctor claimed all checks passed:\n%s", stdout)
	}
}

// The reverse setup was a false positive: doctor warned about a key the daemon
// had, because it was reading its own environment and not finding it there.
func TestDoctorIsSilentWhenTheDaemonHasTheKey(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	inProjectWithModel(t, f)
	f.providers = openaiProvider(true, "")
	// Explicitly cleared: a developer machine with the key exported would
	// otherwise pass this test for the wrong reason.
	t.Setenv("OPENAI_API_KEY", "")

	stdout, _, err := run(t, "doctor")
	if err != nil {
		t.Fatalf("doctor failed though the daemon has the key: %v\n%s", err, stdout)
	}
	if strings.Contains(stdout, "OPENAI_API_KEY") {
		t.Errorf("doctor reported a key the daemon has:\n%s", stdout)
	}
}

// A refused entry — an overridden base_url, say — is not a key problem, and
// `idea daemon stop` fixes nothing about it. Ready folds the two together, which
// is why the daemon reports key_set separately.
func TestDoctorDoesNotBlameTheKeyForARefusedProvider(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	inProjectWithModel(t, f)
	// KeySet false AND a problem: the only state where the Problem guard
	// matters. With KeySet true the earlier short-circuit fires first and this
	// test passes without reaching the branch it is named for.
	f.providers = openaiProvider(false, `provider "openai" is a built-in preset; its base_url may not be overridden`)
	t.Setenv("OPENAI_API_KEY", "sk-present")

	stdout, _, _ := run(t, "doctor")
	if strings.Contains(stdout, "idea daemon stop") {
		t.Errorf("doctor told the user to restart the daemon over a refused entry:\n%s", stdout)
	}
}

// With no daemon running, this shell IS the environment the next command will
// start one from, so the CLI-side answer is the authoritative one and doctor
// must keep it. Dropping it regressed exactly the state doctor's own remedy
// creates: it says `idea daemon stop`, and re-running doctor to confirm
// answered with a verdict-less silence.
func TestDoctorChecksThisShellWhenNoDaemonIsRunning(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	state := t.TempDir()
	t.Setenv("IDEA_HARNESS_STATE", state)
	t.Setenv("IDEA_HARNESS_ADDR", "127.0.0.1:1") // nothing listening
	t.Setenv("OPENAI_API_KEY", "")
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
	if !strings.Contains(stdout, "OPENAI_API_KEY") {
		t.Errorf("doctor lost the key answer with no daemon running, where the CLI is authoritative:\n%s", stdout)
	}
	if !strings.Contains(stdout, "this shell") {
		t.Errorf("doctor did not say which environment it read:\n%s", stdout)
	}
}

// A hardcoded string could satisfy the assertions above.
func TestDoctorAsksTheDaemonAboutKeys(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	inProjectWithModel(t, f)
	f.providers = openaiProvider(true, "")
	_, _, _ = run(t, "doctor")

	for _, req := range f.requests {
		if strings.Contains(req, "/providers") {
			return
		}
	}
	t.Fatalf("doctor never asked the daemon about providers; sent %v", f.requests)
}

// Only providers this project routes to. A machine with six keys exported has
// six ready presets and none of them is what the project uses.
func TestReferencedProvidersCoversEveryConfiguredRoute(t *testing.T) {
	got := referencedProvidersForTest(t)
	for _, want := range []string{"default_model", "models.implementer", "executor.model"} {
		found := false
		for _, field := range got {
			if field == want {
				found = true
			}
		}
		if !found {
			t.Errorf("referencedProviders missed %s; got %v", want, got)
		}
	}
}

func configWithRoles(t *testing.T) config.HarnessConfig {
	t.Helper()
	return config.HarnessConfig{
		DefaultModel: "openai/gpt-4o-mini",
		Models: map[string]string{
			"architect":   "anthropic/claude-sonnet-4-5",
			"implementer": "anthropic/claude-sonnet-4-5",
			"reviewer":    "anthropic/claude-sonnet-4-5",
			// A second route to the default provider, to prove it is not masked.
			"planner": "openai/gpt-4o",
		},
	}
}

func referencedProvidersForTest(t *testing.T) map[string]string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".idea-harness"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "network: disabled\n" +
		"default_model: openai/gpt-4o-mini\n" +
		"models:\n  implementer: anthropic/claude-sonnet-4-5\n" +
		"executor:\n  kind: hermes\n  model: gemini/gemini-2.5-flash\n"
	if err := os.WriteFile(filepath.Join(dir, ".idea-harness", "harness.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(dir, config.Dir))
	if err != nil {
		t.Fatal(err)
	}
	return referencedProviders(cfg.Harness)
}

// M8: the ./.env arm had no test, so deleting it built clean, passed everything,
// and reported the wrong cause and the wrong first step for a key that is in the
// project's .env rather than the shell.
func TestDoctorNamesTheEnvFileWhenTheKeyIsThere(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	inProjectWithModel(t, f)
	f.providers = openaiProvider(false, "")
	t.Setenv("OPENAI_API_KEY", "") // not in the shell
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".env"), []byte("OPENAI_API_KEY=sk-in-dotenv\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := run(t, "doctor")
	if err == nil {
		t.Fatal("doctor passed with the key only in .env and the daemon lacking it")
	}
	if !strings.Contains(stdout, ".env") {
		t.Errorf("doctor did not say the key is in ./.env, so it named the wrong cause:\n%s", stdout)
	}
	if !strings.Contains(stdout, "idea daemon stop") {
		t.Errorf("doctor did not name the fix that actually works here:\n%s", stdout)
	}
}

// M-demo: nothing asserted the demo banner, which is how it silently became
// conditional on the config loading when the providers fetch was hoisted.
func TestDoctorStillReportsDemoMode(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	inProjectWithModel(t, f)
	f.providers = cliProvidersView{Demo: true}

	stdout, _, _ := run(t, "doctor")
	if !strings.Contains(stdout, "demo mode") {
		t.Errorf("doctor stopped reporting demo mode:\n%s", stdout)
	}
}

// The attribution used to come from a randomised map iteration, so the same
// config produced a different "from …" on each run — and default_model, added
// first, was always overwritten by a later role.
func TestKeyFindingsAttributionIsStable(t *testing.T) {
	h := configWithRoles(t)
	first := referencedProviders(h)
	for i := 0; i < 20; i++ {
		if got := referencedProviders(h); got["anthropic"] != first["anthropic"] {
			t.Fatalf("attribution changed between runs: %q then %q", first["anthropic"], got["anthropic"])
		}
	}
	if first["openai"] != "default_model" {
		t.Errorf("default_model was masked by a later field: openai attributed to %q", first["openai"])
	}
}

// `idea model set` had the defect key_set was added to prevent, in the command
// README calls the detector that works: it branched on Ready, which folds a
// missing key together with a refused entry, so an overridden base_url with the
// key present told the user to restart the daemon — a fix that changes nothing.
func TestModelSetDoesNotBlameTheKeyForARefusedProvider(t *testing.T) {
	f := newFakeDaemon(t)
	f.json("PUT /api/v1/projects/{projectId}/providers", map[string]any{"status": "ok"})
	f.providers = openaiProvider(true, `provider "openai" is a built-in preset; its base_url may not be overridden`)
	t.Setenv("OPENAI_API_KEY", "sk-present-everywhere")

	stdout, _, _ := run(t, "model", "set", "openai/gpt-4o-mini")
	if strings.Contains(stdout, "daemon does not have") {
		t.Errorf("model set blamed the key for a refused entry:\n%s", stdout)
	}
	if strings.Contains(stdout, "idea daemon stop") {
		t.Errorf("model set told the user to restart the daemon over a base_url problem:\n%s", stdout)
	}
}

// The case it must still catch.
func TestModelSetStillWarnsWhenTheDaemonMissedTheKey(t *testing.T) {
	f := newFakeDaemon(t)
	f.json("PUT /api/v1/projects/{projectId}/providers", map[string]any{"status": "ok"})
	f.providers = openaiProvider(false, "")
	t.Setenv("OPENAI_API_KEY", "sk-in-this-shell")

	stdout, _, _ := run(t, "model", "set", "openai/gpt-4o-mini")
	if !strings.Contains(stdout, "daemon does not have") {
		t.Errorf("model set stopped catching a stale daemon:\n%s", stdout)
	}
}

// `idea model ls` had the same conflation doctor and `idea model set` were
// fixed for, and it reaches every user — including a `go install` one, who has
// no web UI at all. It reported a key as `unset` when the key was set and the
// entry was refused, which is a false statement about the operator's own
// environment at the one moment they most need to look at `base_url`.
func TestModelListSeparatesAMissingKeyFromARefusedEntry(t *testing.T) {
	const reason = `provider "openai" is a built-in preset; its base_url may not be overridden`
	f := newFakeDaemon(t)
	f.providers = cliProvidersView{Providers: []cliProvider{{
		Name: "openai", Label: "OpenAI", Type: "openai", KeyEnv: "OPENAI_API_KEY",
		Configured: true, KeySet: true, Ready: false, Problem: reason,
	}}}

	stdout, _, err := run(t, "model", "ls")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "OPENAI_API_KEY (unset)") {
		t.Errorf("model ls called a key that is set `unset`:\n%s", stdout)
	}
	if !strings.Contains(stdout, "OPENAI_API_KEY (set)") {
		t.Errorf("model ls did not report the key as set:\n%s", stdout)
	}
	if !strings.Contains(stdout, "base_url may not be overridden") {
		t.Errorf("model ls showed no reason for the refusal, so `!` says something is wrong "+
			"and not what:\n%s", stdout)
	}
}

// The genuinely-keyless case must still read `unset`.
func TestModelListStillReportsAMissingKey(t *testing.T) {
	f := newFakeDaemon(t)
	f.providers = cliProvidersView{Providers: []cliProvider{{
		Name: "anthropic", Label: "Anthropic", Type: "anthropic", KeyEnv: "ANTHROPIC_API_KEY",
		Configured: true, KeySet: false, Ready: false,
	}}}

	stdout, _, err := run(t, "model", "ls")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "ANTHROPIC_API_KEY (unset)") {
		t.Errorf("model ls stopped reporting a genuinely missing key:\n%s", stdout)
	}
}
