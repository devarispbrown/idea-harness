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
}
