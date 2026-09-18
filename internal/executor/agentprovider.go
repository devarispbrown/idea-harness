package executor

import (
	"fmt"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/config"
)

// AgentProvider is the model endpoint a coding agent is pinned to.
type AgentProvider struct {
	Name    string // provider key, e.g. "openai"
	KeyEnv  string // env var holding its API key; "" for a keyless provider
	BaseURL string // its API root
}

// ResolveAgentProvider resolves the provider a coding-agent model spec names,
// the same way the model registry does: a `providers:` entry in harness.yaml
// first (which falls back to the preset for its missing fields), then the
// built-in preset.
//
// The entry is consulted BEFORE the preset. Checking the preset first handed
// the agent the preset's own endpoint and key variable, so an Ollama moved to
// another port, or an OpenAI key under a project-specific variable, reached
// every model call except the coding agent's.
//
// A bare model id (no `/`) takes the provider of defaultModel, which the
// operator chose explicitly. A spec WITH a provider prefix never falls back:
// `opneai/gpt-x` is a typo, and resolving it to the default provider would
// send a model id nobody chose to an endpoint the typo did not name.
func ResolveAgentProvider(model, defaultModel string, providers map[string]config.ProviderConfig) (AgentProvider, error) {
	name, hasPrefix := model, false
	if i := strings.Index(model, "/"); i > 0 {
		name, hasPrefix = model[:i], true
	}
	if pc, ok := providers[name]; ok {
		// Validated here as well as on the write path: harness.yaml can be
		// edited by hand, and this entry decides where a key is sent.
		if err := config.ValidateProvider(name, pc); err != nil {
			return AgentProvider{}, fmt.Errorf("executor: provider %q in harness.yaml is not usable: %w", name, err)
		}
		_, base, keyEnv := config.ResolveProvider(name, pc)
		return AgentProvider{Name: name, KeyEnv: keyEnv, BaseURL: base}, nil
	}
	if p := config.PresetFor(name); p != nil {
		return AgentProvider{Name: name, KeyEnv: p.DefaultKeyEnv, BaseURL: p.BaseURL}, nil
	}
	if !hasPrefix && defaultModel != "" && strings.Contains(defaultModel, "/") {
		return ResolveAgentProvider(defaultModel, "", providers)
	}
	// Unknown provider: fail closed.
	//
	// This used to scan config.Presets and return the first key that happened to
	// be set in the environment. A typo in executor.model therefore handed some
	// other provider's real API key to the coding agent -- which runs in a
	// container with deliberate network egress, so the key leaves the machine.
	return AgentProvider{}, fmt.Errorf("executor: cannot resolve a provider for model %q; "+
		"use provider/model with a built-in provider or one added under providers: in .idea-harness/harness.yaml", model)
}
