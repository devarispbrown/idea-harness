package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/security"
)

// The two executor kinds declared next to their siblings in config.go:
//
// ExecutorACP drives any Agent Client Protocol agent -- Claude Code via
// claude-code-acp, Codex via codex-acp, Gemini CLI (`gemini
// --experimental-acp`), OpenHands (`openhands acp`), OpenCode -- over JSON-RPC
// on the stdio of the same hardened container the hermes/omp kinds use
// (internal/executor/acp.go).
//
// ExecutorAuto is what an unset executor.kind means. It is resolved at run
// time (workflow.ResolveExecutor): hermes, on its pinned image, when Docker is
// reachable and the coding model's provider key is set; internal otherwise,
// with a loud warning everywhere a run is started or inspected, because the
// internal executor writes a placeholder, not code. It used to mean internal
// outright, which made "idea execute produced no real code" the silent default
// for everyone who never found docs/agents.md.

// ACPConfig configures the acp executor kind.
type ACPConfig struct {
	// Command is the agent's argv INSIDE the container, e.g.
	// ["claude-code-acp"] or ["gemini", "--experimental-acp"]. The tokens
	// {model} and {bare_model} are replaced with executor.model as
	// provider/model and as the bare model id: ACP has no standard way to
	// choose a model, and every agent takes it differently.
	Command []string `json:"command,omitempty" yaml:"command,omitempty"`
	// KeyEnv renames the provider key inside the container. The value is read
	// from the provider's own variable (OPENAI_API_KEY, GOOGLE_API_KEY, ...)
	// and exposed to the agent under this name instead -- Gemini CLI reads
	// GEMINI_API_KEY, not the GOOGLE_API_KEY the gemini preset uses. Empty
	// keeps the provider's own name.
	KeyEnv string `json:"key_env,omitempty" yaml:"key_env,omitempty"`
	// TimeoutSeconds bounds one prompt turn. When it expires the client sends
	// session/cancel, waits briefly, then kills the container. Zero uses the
	// default (840 s: just under the engine's own 15-minute ceiling, so the
	// agent gets a clean cancel rather than the engine's hard stop).
	TimeoutSeconds int `json:"timeout_seconds,omitempty" yaml:"timeout_seconds,omitempty"`
}

// IsZero lets yaml.v3's omitempty drop an unset acp: block, so a project that
// never chose acp does not gain an empty `acp: {}` on its next write.
func (a ACPConfig) IsZero() bool {
	return len(a.Command) == 0 && a.KeyEnv == "" && a.TimeoutSeconds == 0
}

// MCPServerConfig is one MCP server handed to an ACP agent in session/new, in
// ACP's own McpServer shape: a stdio server (Command, Args, Env) or an
// http/sse server (Type, URL, Headers).
//
// The trust tradeoff, stated once here and in docs/agents.md: a stdio server
// runs INSIDE the agent's container, as the agent's child, with the same
// isolation and the same network egress. An http/sse server is reached by the
// agent over the network, and whatever credential its headers carry is handed
// to the agent to send.
type MCPServerConfig struct {
	Name string `json:"name" yaml:"name"`
	// Type is "stdio" (the default), "http" or "sse".
	Type    string         `json:"type,omitempty" yaml:"type,omitempty"`
	Command string         `json:"command,omitempty" yaml:"command,omitempty"`
	Args    []string       `json:"args,omitempty" yaml:"args,omitempty"`
	Env     []MCPNameValue `json:"env,omitempty" yaml:"env,omitempty"`
	URL     string         `json:"url,omitempty" yaml:"url,omitempty"`
	Headers []MCPNameValue `json:"headers,omitempty" yaml:"headers,omitempty"`
}

// EffectiveType is the server's transport, defaulting to stdio.
func (m MCPServerConfig) EffectiveType() string {
	if m.Type == "" {
		return "stdio"
	}
	return m.Type
}

// MCPNameValue is one env variable or HTTP header for an MCP server. Exactly
// one of Value (a literal, for a non-secret setting) or ValueEnv (the NAME of a
// variable resolved from the daemon's environment, then the project's .env --
// the lookup provider keys use) is set. harness.yaml is committable, so a
// secret is never written inline: Load refuses a literal Value whose name or
// content looks like one.
type MCPNameValue struct {
	Name     string `json:"name" yaml:"name"`
	Value    string `json:"value,omitempty" yaml:"value,omitempty"`
	ValueEnv string `json:"value_env,omitempty" yaml:"value_env,omitempty"`
}

// secretNameRe matches env/header NAMES that carry a credential. It is wider
// than rejectSecrets' key check because these names are chosen by the MCP
// server's author, not by this schema: Authorization, X-Api-Key, GITHUB_TOKEN.
var secretNameRe = secretKeyRe

// validateExecutorExtras checks the structure of executor.acp and
// executor.mcp_servers. Kind-specific requirements (acp needs an image and a
// command) are Problems, not load errors: a config that names acp before the
// image is chosen must still open.
func validateExecutorExtras(e ExecutorConfig) error {
	if e.ACP.TimeoutSeconds < 0 {
		return fmt.Errorf("config: harness.executor.acp.timeout_seconds must be >= 0")
	}
	seen := map[string]bool{}
	for i, s := range e.MCPServers {
		field := fmt.Sprintf("harness.executor.mcp_servers[%d]", i)
		if strings.TrimSpace(s.Name) == "" {
			return fmt.Errorf("config: %s.name is required", field)
		}
		if seen[s.Name] {
			return fmt.Errorf("config: %s: duplicate MCP server name %q", field, s.Name)
		}
		seen[s.Name] = true
		switch s.EffectiveType() {
		case "stdio":
			if s.Command == "" {
				return fmt.Errorf("config: %s (%s) is a stdio server and needs command", field, s.Name)
			}
			if s.URL != "" || len(s.Headers) > 0 {
				return fmt.Errorf("config: %s (%s) is a stdio server; url and headers belong to type: http or sse", field, s.Name)
			}
		case "http", "sse":
			if s.URL == "" {
				return fmt.Errorf("config: %s (%s) is an %s server and needs url", field, s.Name, s.Type)
			}
			if s.Command != "" || len(s.Args) > 0 || len(s.Env) > 0 {
				return fmt.Errorf("config: %s (%s) is an %s server; command, args and env belong to a stdio server", field, s.Name, s.Type)
			}
		default:
			return fmt.Errorf("config: %s.type must be stdio, http or sse, got %q", field, s.Type)
		}
		for j, nv := range s.Env {
			if err := validateNameValue(fmt.Sprintf("%s.env[%d]", field, j), nv); err != nil {
				return err
			}
		}
		for j, nv := range s.Headers {
			if err := validateNameValue(fmt.Sprintf("%s.headers[%d]", field, j), nv); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateNameValue(field string, nv MCPNameValue) error {
	if strings.TrimSpace(nv.Name) == "" {
		return fmt.Errorf("config: %s.name is required", field)
	}
	if nv.Value != "" && nv.ValueEnv != "" {
		return fmt.Errorf("config: %s (%s) sets both value and value_env; give exactly one", field, nv.Name)
	}
	if nv.Value == "" {
		return nil
	}
	// The same rule rejectSecrets applies to keys, applied to the entry's own
	// name -- `{name: GITHUB_TOKEN, value: ghp_...}` passes rejectSecrets,
	// whose key is just "value".
	if secretNameRe.MatchString(nv.Name) || strings.EqualFold(nv.Name, "Authorization") {
		return fmt.Errorf("config: %s (%s) must not contain an inline secret; use value_env to name the variable that holds it (§47)", field, nv.Name)
	}
	if security.FindCredential(nv.Value) != "" {
		return fmt.Errorf("config: %s (%s) looks like it contains a credential; use value_env to name the variable that holds it (§47)", field, nv.Name)
	}
	return nil
}

// ResolveMCPValue returns an entry's value: the literal, or the named
// variable from lookup. ok is false when ValueEnv names a variable that is set
// nowhere.
func ResolveMCPValue(nv MCPNameValue, lookup func(string) string) (string, bool) {
	if nv.ValueEnv == "" {
		return nv.Value, true
	}
	if lookup == nil {
		lookup = os.Getenv
	}
	v := lookup(nv.ValueEnv)
	return v, v != ""
}

// executorProblems are the executor findings Load deliberately does not turn
// into errors.
func executorProblems(e ExecutorConfig, dotEnv map[string]string) []Problem {
	var problems []Problem
	if e.Kind == ExecutorACP {
		if e.Image == "" {
			problems = append(problems, Problem{Severity: SeverityError, Field: "harness.executor.image",
				Message: "executor.kind is acp but executor.image is empty; there is no canonical ACP image -- run `idea executors setup --kind acp --image <image> --acp-command <argv>`"})
		}
		if len(e.ACP.Command) == 0 {
			problems = append(problems, Problem{Severity: SeverityError, Field: "harness.executor.acp.command",
				Message: "executor.kind is acp but executor.acp.command is empty; set the agent's argv inside the image, e.g. [claude-code-acp]"})
		}
	}
	if len(e.MCPServers) > 0 && e.Kind != ExecutorACP {
		problems = append(problems, Problem{Severity: SeverityWarn, Field: "harness.executor.mcp_servers",
			Message: fmt.Sprintf("executor.mcp_servers is only passed to an acp agent; executor.kind is %s, so they are not used", e.Kind)})
	}
	lookup := func(name string) string {
		if v := os.Getenv(name); v != "" {
			return v
		}
		return dotEnv[name]
	}
	for i, s := range e.MCPServers {
		for _, nv := range append(append([]MCPNameValue{}, s.Env...), s.Headers...) {
			if _, ok := ResolveMCPValue(nv, lookup); !ok {
				problems = append(problems, Problem{Severity: SeverityWarn, Field: fmt.Sprintf("harness.executor.mcp_servers[%d]", i),
					Message:      fmt.Sprintf("MCP server %q needs %s (for %s), which is set neither in the environment nor in .env; the run will stop before the agent starts", s.Name, nv.ValueEnv, nv.Name),
					EnvDependent: true})
			}
		}
	}
	return problems
}
