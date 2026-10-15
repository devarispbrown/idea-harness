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
	if err := os.WriteFile(filepath.Join(dir, "harness.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(dir)
}

// An unset kind is auto, not internal: internal writes a placeholder, and
// being the silent default is how projects ran it without knowing.
func TestUnsetExecutorKindIsAuto(t *testing.T) {
	cfg, err := loadHarness(t, "network: disabled\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Harness.Executor.Kind != ExecutorAuto {
		t.Fatalf("kind = %q, want auto", cfg.Harness.Executor.Kind)
	}
	if Default().Harness.Executor.Kind != ExecutorAuto {
		t.Fatal("Default() executor kind is not auto")
	}
	// Explicit internal is kept: it is the documented way to opt out.
	cfg, err = loadHarness(t, "executor:\n  kind: internal\n")
	if err != nil || cfg.Harness.Executor.Kind != ExecutorInternal {
		t.Fatalf("explicit internal = %q, %v", cfg.Harness.Executor.Kind, err)
	}
}

func TestACPAndMCPConfigLoads(t *testing.T) {
	cfg, err := loadHarness(t, `
executor:
  kind: acp
  image: ghcr.io/x/claude-acp:1
  model: anthropic/claude-sonnet-5
  acp:
    command: [claude-code-acp]
    key_env: ANTHROPIC_API_KEY
    timeout_seconds: 600
  mcp_servers:
    - name: gh
      command: github-mcp-server
      args: [stdio]
      env:
        - name: GITHUB_PERSONAL_ACCESS_TOKEN
          value_env: GITHUB_TOKEN
        - name: LOG_LEVEL
          value: debug
    - name: docs
      type: http
      url: https://docs.example.com/mcp
      headers:
        - name: Authorization
          value_env: DOCS_MCP_AUTH
`)
	if err != nil {
		t.Fatal(err)
	}
	e := cfg.Harness.Executor
	if e.Kind != ExecutorACP || len(e.ACP.Command) != 1 || e.ACP.TimeoutSeconds != 600 || len(e.MCPServers) != 2 {
		t.Fatalf("executor = %+v", e)
	}
	if e.MCPServers[1].EffectiveType() != "http" || e.MCPServers[0].EffectiveType() != "stdio" {
		t.Fatalf("types = %q %q", e.MCPServers[0].EffectiveType(), e.MCPServers[1].EffectiveType())
	}
}

func TestMCPConfigRefusesInlineSecretsAndBadShapes(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, want string }{
		"secret by name": {`
executor:
  mcp_servers:
    - name: gh
      command: x
      env:
        - name: GITHUB_TOKEN
          value: abc
`, "inline secret"},
		"authorization header": {`
executor:
  mcp_servers:
    - name: d
      type: http
      url: https://x
      headers:
        - name: Authorization
          value: Bearer abc
`, "inline secret"},
		"secret by content": {`
executor:
  mcp_servers:
    - name: gh
      command: x
      env:
        - name: SETTING
          value: ghp_0123456789abcdefghijklmnopqrstuvwxyzAB
`, "looks like it contains a credential"},
		"both value and value_env": {`
executor:
  mcp_servers:
    - name: gh
      command: x
      env:
        - name: A
          value: b
          value_env: C
`, "exactly one"},
		"stdio without command": {`
executor:
  mcp_servers:
    - name: gh
`, "needs command"},
		"http without url": {`
executor:
  mcp_servers:
    - name: d
      type: http
`, "needs url"},
		"http with command": {`
executor:
  mcp_servers:
    - name: d
      type: sse
      url: https://x
      command: y
`, "belong to a stdio server"},
		"duplicate names": {`
executor:
  mcp_servers:
    - name: d
      command: x
    - name: d
      command: y
`, "duplicate"},
		"unknown field": {`
executor:
  mcp_servers:
    - name: d
      command: x
      secret: y
`, "invalid"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadHarness(t, tc.yaml)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestExecutorProblems(t *testing.T) {
	has := func(ps []Problem, field, sub string) bool {
		for _, p := range ps {
			if p.Field == field && strings.Contains(p.Message, sub) {
				return true
			}
		}
		return false
	}
	acp := Config{Harness: HarnessConfig{Executor: ExecutorConfig{Kind: ExecutorACP}}}
	ps := acp.Problems()
	if !has(ps, "harness.executor.image", "no canonical ACP image") || !has(ps, "harness.executor.acp.command", "acp.command is empty") {
		t.Fatalf("acp without image/command: %+v", ps)
	}
	stray := Config{Harness: HarnessConfig{Executor: ExecutorConfig{Kind: ExecutorHermes,
		MCPServers: []MCPServerConfig{{Name: "gh", Command: "x", Env: []MCPNameValue{{Name: "T", ValueEnv: "UNSET_FOR_TEST_XYZ"}}}}}}}
	ps = stray.Problems()
	if !has(ps, "harness.executor.mcp_servers", "only passed to an acp agent") {
		t.Fatalf("mcp_servers on hermes was not flagged: %+v", ps)
	}
	if !has(ps, "harness.executor.mcp_servers[0]", "UNSET_FOR_TEST_XYZ") {
		t.Fatalf("an unresolvable value_env was not flagged: %+v", ps)
	}
	if ps := stray.ProblemsWithEnv(map[string]string{"UNSET_FOR_TEST_XYZ": "v"}); has(ps, "harness.executor.mcp_servers[0]", "UNSET_FOR_TEST_XYZ") {
		t.Fatal("a value_env set in .env was still flagged")
	}
}

// An unset acp block must not appear as `acp: {}` after a write.
func TestWriteHarnessOmitsAnEmptyACPBlock(t *testing.T) {
	dir := t.TempDir()
	if err := WriteHarness(dir, Default().Harness); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "harness.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "acp:") || strings.Contains(string(b), "mcp_servers") {
		t.Fatalf("harness.yaml gained empty executor blocks:\n%s", b)
	}
	if !strings.Contains(string(b), "kind: auto") {
		t.Fatalf("harness.yaml does not record kind: auto:\n%s", b)
	}
	if strings.Contains(string(b), "openhands") {
		t.Fatalf("harness.yaml gained an empty openhands block:\n%s", b)
	}
}

// The openhands block loads; the server URL must be https unless loopback and
// carry no credentials, and the session key is named, never inlined.
func TestOpenHandsConfigLoadsAndRefusesUnsafeShapes(t *testing.T) {
	cfg, err := loadHarness(t, `executor:
  kind: openhands
  model: anthropic/claude-sonnet-5
  openhands:
    url: https://agents.example.com
    api_key_env: OH_KEY
    agent: acp
    acp_command: [claude-agent-acp]
    workspace_dir: /srv/work
    withhold_provider_key: true
    max_iterations: 50
    timeout_seconds: 600
`)
	if err != nil {
		t.Fatal(err)
	}
	o := cfg.Harness.Executor.OpenHands
	if cfg.Harness.Executor.Kind != ExecutorOpenHands || o.URL != "https://agents.example.com" || o.EffectiveAPIKeyEnv() != "OH_KEY" ||
		o.EffectiveAgent() != "acp" || o.EffectiveWorkspaceDir() != "/srv/work" || !o.WithholdProviderKey {
		t.Fatalf("loaded %+v", o)
	}
	var zero OpenHandsConfig
	if zero.EffectiveAPIKeyEnv() != "OPENHANDS_API_KEY" || zero.EffectiveAgent() != "openhands" || zero.EffectiveWorkspaceDir() != "/workspace/idea-harness" {
		t.Fatal("defaults changed")
	}

	for _, tc := range []struct{ name, block, want string }{
		{"plain http to a remote host", "url: http://agents.example.com", "use https"},
		{"credentials in the url", "url: https://user:pw@agents.example.com", "must not carry credentials"},
		{"not http", "url: ftp://agents.example.com", "http(s) URL"},
		{"query string", "url: https://agents.example.com/?k=v", "base URL"},
		{"inline key", "api_key_env: sk-abc def", "NAME of an environment variable"},
		{"unknown field", "session_api_key: abcdefgh", ""},
		{"bad agent", "agent: hermes", ""},
		{"acp_command without acp", "acp_command: [x]", "only applies to agent: acp"},
		{"relative dir", "workspace_dir: work", "clean absolute directory"},
		{"root dir", "workspace_dir: /", "clean absolute directory"},
		{"unclean dir", "workspace_dir: /a/../b", "clean absolute directory"},
		{"shell chars in dir", "workspace_dir: /a;rm", "clean absolute directory"},
		{"negative timeout", "timeout_seconds: -1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadHarness(t, "executor:\n  kind: openhands\n  openhands:\n    "+tc.block+"\n")
			if err == nil {
				t.Fatal("loaded")
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
	for _, ok := range []string{"http://localhost:8000", "http://127.0.0.1:8000/", "http://[::1]:8000", "https://agents.example.com/oh"} {
		if err := ValidateOpenHandsURL(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
}

func TestOpenHandsProblems(t *testing.T) {
	c := Config{Harness: HarnessConfig{Executor: ExecutorConfig{Kind: ExecutorOpenHands, OpenHands: OpenHandsConfig{Agent: "acp"}}}}
	var url, cmd bool
	for _, p := range c.Problems() {
		url = url || (p.Field == "harness.executor.openhands.url" && p.Severity == SeverityError)
		cmd = cmd || (p.Field == "harness.executor.openhands.acp_command" && p.Severity == SeverityError)
	}
	if !url || !cmd {
		t.Fatalf("missing url / acp_command not flagged: %+v", c.Problems())
	}
}
