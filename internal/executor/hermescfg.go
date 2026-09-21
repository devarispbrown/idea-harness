package executor

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/model"
)

// Hermes resolves a provider name through its own registry, and that registry
// does not agree with this project's. Read out of the pinned image
// (nousresearch/hermes-agent:v2026.5.16), `normalize_provider` maps:
//
//	anthropic  -> anthropic     gemini     -> gemini
//	deepseek   -> deepseek      openrouter -> openrouter
//	openai     -> OPENROUTER                 ollama -> "custom"
//
// So asking Hermes for `openai` silently bills OpenRouter and never touches the
// OpenAI key this project resolved, and `ollama` resolves to nothing at all.
// Hermes also defaults `model.base_url` to OpenRouter, so a provider it cannot
// place falls back there rather than failing.
//
// Rather than depend on that mapping, every provider is declared explicitly as
// a Hermes `custom_provider` with the base URL this project already holds in
// config.Presets. A custom provider is `transport: openai_chat` against a
// stated URL, which is what all six of these are. The cost is one generated
// config file; the benefit is that the endpoint the agent talks to is the one
// the operator configured, for every provider, with no silent reroute.
//
// Verified against the image: a custom provider pointing at api.openai.com with
// OPENAI_API_KEY forwarded returns a real completion. Hermes warns when
// `custom_providers` is present without a `model` section, so both are written.
const hermesConfigPath = "config.yaml"

// hermesProviderSlug is the custom-provider name written into the config and
// passed to `--provider`. Prefixed so it cannot collide with a Hermes built-in
// and it is obvious in a log where it came from.
func hermesProviderSlug(provider string) string {
	return "idea-" + provider
}

// slugSafe matches what Hermes's custom_provider_slug tolerates.
var slugSafe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// hermesConfig is the shape of the Hermes config file, in the order Hermes's
// docs present it.
type hermesConfig struct {
	CustomProviders []hermesCustomProvider `yaml:"custom_providers"`
	Model           hermesModel            `yaml:"model"`
}

type hermesCustomProvider struct {
	Name    string `yaml:"name"`
	BaseURL string `yaml:"base_url"`
	// KeyEnv names the env var holding the key. A custom provider has no key
	// env of its own, and without this Hermes sends the literal placeholder
	// "no-key-required" -- which is right for Ollama and a 401 for everything
	// else. OpenAI and OpenRouter only appeared to work because Hermes also
	// falls back to OPENAI_API_KEY and OPENROUTER_API_KEY globally; Anthropic,
	// Gemini and DeepSeek were rejected with a key that was valid on the host.
	KeyEnv string `yaml:"key_env,omitempty"`
}

type hermesModel struct {
	Default  string `yaml:"default"`
	Provider string `yaml:"provider"`
	BaseURL  string `yaml:"base_url"`
	// ContextLength declares the model's context window explicitly, in the
	// exact key Hermes's own config schema expects at the top level
	// (model.context_length -- see
	// https://hermes-agent.nousresearch.com/docs/integrations/providers).
	// Omitted (0) falls through to Hermes's own auto-detection chain, which
	// is what let hermes + openai/gpt-4.1-mini fail deterministically: every
	// provider here is an explicit custom_provider, for which that chain has
	// the least to go on, and its model-family guess for gpt-4.1-mini landed
	// under Hermes's own 64,000-token floor even though the model's real
	// window is roughly 1M. Set from model.ContextWindowFor, which is 0 for
	// ollama on purpose (see its own doc comment) -- there, and only there,
	// this is still left to Hermes's auto-detection.
	ContextLength int `yaml:"context_length,omitempty"`
}

// hermesConfigYAML renders the Hermes config that pins the agent to one
// endpoint. baseURL is the provider's API root; modelID is the bare model id.
//
// Marshalled, not formatted by hand. The base URL and model come from
// harness.yaml, and a hand-formatted file let a ` #` truncate a URL into a
// comment, a `: ` in a model id break the parse, and a newline inject keys.
func hermesConfigYAML(provider, baseURL, modelID, keyEnv string) (string, error) {
	if !slugSafe.MatchString(provider) {
		return "", fmt.Errorf("executor: provider %q is not a usable Hermes provider name", provider)
	}
	if baseURL == "" {
		return "", fmt.Errorf("executor: provider %q has no base URL, so the agent has no endpoint to call", provider)
	}
	slug := hermesProviderSlug(provider)
	body, err := yaml.Marshal(hermesConfig{
		CustomProviders: []hermesCustomProvider{{Name: slug, BaseURL: baseURL, KeyEnv: keyEnv}},
		Model: hermesModel{
			Default:       modelID,
			Provider:      slug,
			BaseURL:       baseURL,
			ContextLength: model.ContextWindowFor(contextWindowProviderKey(provider, baseURL), modelID),
		},
	})
	if err != nil {
		return "", fmt.Errorf("executor: render agent config: %w", err)
	}
	return "# Generated by idea-harness. Pins the agent to the provider and\n" +
		"# endpoint configured in .idea-harness/harness.yaml.\n" + string(body), nil
}

// contextWindowProviderKey resolves the key to look up in
// model.ModelContextWindows/contextWindowFallback, whose tables are keyed on
// the six built-in provider names (review MEDIUM #6). provider is the
// harness.yaml map key, which is not necessarily one of those six: a renamed
// provider (`providers: openai-work:`, docs/configuration.md's own example
// shape) made ContextWindowFor(provider, ...) look up "openai-work" and
// return 0 for a real OpenAI endpoint, reproducing the exact
// Hermes-refuses-to-start failure the context_length fix exists to prevent.
//
// provider itself is tried first (the common case: an un-renamed provider
// key IS already one of the six). Failing that, baseURL is matched against
// the built-in presets' own base URLs (config.PresetForBaseURL): a renamed
// provider still points at the same real endpoint, so this recovers "openai"
// for "openai-work" the same way a human reading harness.yaml would. A
// genuinely custom endpoint (a private gateway, a self-hosted proxy) matches
// neither and falls through to "", for which ContextWindowFor already
// returns 0 -- Hermes's own auto-detection, the existing, safe behavior for
// an unrecognized provider.
func contextWindowProviderKey(provider, baseURL string) string {
	if config.PresetFor(provider) != nil {
		return provider
	}
	if p := config.PresetForBaseURL(baseURL); p != nil {
		return p.Key
	}
	return ""
}

// containerBaseURL rewrites a loopback endpoint so it resolves from inside the
// container.
//
// Ollama is the case this exists for: its preset base URL is
// http://localhost:11434/v1, and inside a container `localhost` is the
// container itself, so the agent would dial its own empty port instead of the
// operator's Ollama. Docker publishes the host as `host.docker.internal`; on
// Linux that name only exists if `--add-host` supplies it, which
// containerArgs does whenever this rewrite fires.
//
// A loopback host is recognised by config.IsLoopbackHost, the same test the
// config applies when it accepts a loopback base URL, so every URL the config
// lets through as local is also routed as local: `localhost` without a port,
// any case, all of 127.0.0.0/8, and [::1]. Only the host is examined; a
// loopback URL in a query string is not a loopback endpoint.
func containerBaseURL(raw string) (rewritten string, needsHostGateway bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || !config.IsLoopbackHost(u.Hostname()) {
		return raw, false
	}
	if port := u.Port(); port != "" {
		u.Host = net.JoinHostPort("host.docker.internal", port)
	} else {
		u.Host = "host.docker.internal"
	}
	return u.String(), true
}

// agentBaseURL is the OpenAI-compatible endpoint for a provider, which is what a
// Hermes custom provider speaks (transport openai_chat).
//
// It differs from the configured base URL for a Gemini-type provider, preset
// or custom: that base is the native API (…/v1beta), which idea-harness's own
// Gemini client uses, and the agent posts to {base}/chat/completions, which the
// native API does not serve. Google publishes the OpenAI-compatible surface
// under /openai. Anthropic's preset base already serves OpenAI-compatible chat
// completions, and the other presets are OpenAI-compatible natively.
func agentBaseURL(p AgentProvider, baseURL string) string {
	if p.Type == config.ProviderGemini && !strings.HasSuffix(strings.TrimRight(baseURL, "/"), "/openai") {
		return strings.TrimRight(baseURL, "/") + "/openai"
	}
	return baseURL
}
