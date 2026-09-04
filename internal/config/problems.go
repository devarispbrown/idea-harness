package config

import (
	"fmt"
	"github.com/devarispbrown/idea-harness/internal/model"
	"net"
	"net/url"
	"os"
	"sort"
	"strings"
)

// Severity classifies a Problem. An error means the configuration cannot do
// what it says (a model spec that resolves to no provider, an endpoint that
// would ship credentials off-box); a warn means it will work but degrade —
// most commonly demo mode, which is a legitimate way to run the product.
type Severity string

const (
	SeverityError Severity = "error"
	SeverityWarn  Severity = "warn"
)

// Problem is one configuration finding. Field is the harness.yaml path it came
// from so the operator knows which line to edit.
type Problem struct {
	Severity Severity `json:"severity"`
	Field    string   `json:"field"`
	Message  string   `json:"message"`
}

// HasErrors reports whether any problem is error-severity.
func HasErrors(problems []Problem) bool {
	for _, p := range problems {
		if p.Severity == SeverityError {
			return true
		}
	}
	return false
}

// Problems reports configuration findings that Load deliberately does not turn
// into load errors (§D5): a config that is wrong must still open, or upgrading
// the binary would brick an existing project. Callers render these; nothing
// here mutates or rejects.
func (c Config) Problems() []Problem {
	return c.ProblemsWithEnv(nil)
}

// ProblemsWithEnv is Problems with the project's .env as a fallback for API-key
// resolution, matching how the daemon resolves keys (runtime.go lookupEnv).
func (c Config) ProblemsWithEnv(dotEnv map[string]string) []Problem {
	var problems []Problem
	h := c.Harness

	// Provider entries first: they define which names the model specs below may
	// legally reference.
	for _, name := range sortedKeys(h.Providers) {
		problems = append(problems, providerProblems(name, h.Providers[name], dotEnv)...)
	}

	known := knownProviders(h.Providers)
	problems = append(problems, modelSpecProblems("harness.default_model", h.DefaultModel, known, h.Providers, dotEnv)...)
	problems = append(problems, modelSpecProblems("harness.executor.model", h.Executor.Model, known, h.Providers, dotEnv)...)
	for _, role := range sortedKeys(h.Models) {
		field := fmt.Sprintf("harness.models.%s", role)
		problems = append(problems, modelSpecProblems(field, h.Models[role], known, h.Providers, dotEnv)...)
	}
	return problems
}

// knownProviders is the set of provider names a model spec may name: the
// built-in presets plus every explicitly configured provider. Configured names
// count even when they match no preset — ResolveProvider deliberately treats a
// custom provider as OpenAI-compatible, which is how Groq, Together and LM
// Studio are documented (docs/configuration.md). Scoping this to presets alone
// would reject documented, tested configurations.
func knownProviders(providers map[string]ProviderConfig) map[string]bool {
	known := map[string]bool{}
	for _, p := range Presets {
		known[p.Key] = true
	}
	for name := range providers {
		known[name] = true
	}
	return known
}

// modelSpecProblems checks one "provider/model" field. A bare model (no slash)
// names no provider and inherits the default, so it is not checked here.
func modelSpecProblems(field, spec string, known map[string]bool, providers map[string]ProviderConfig, dotEnv map[string]string) []Problem {
	provider, _ := ParseModelSpec(spec)
	if provider == "" {
		return nil
	}
	if !known[provider] {
		return []Problem{{
			Severity: SeverityError,
			Field:    field,
			Message: fmt.Sprintf("unknown provider %q; known providers are %s — add it under harness.providers or pick one of those",
				provider, strings.Join(sortedNames(known), ", ")),
		}}
	}
	// A referenced preset that was never configured still needs a key; the
	// configured ones are covered by providerProblems.
	var out []Problem
	if _, configured := providers[provider]; !configured {
		if p := PresetFor(provider); p != nil {
			out = append(out, keyProblems(field, provider, p.DefaultKeyEnv, p.NeedsKey, dotEnv)...)
		}
	}
	// Appended, not an alternative: a missing key and a mistyped model are
	// independent mistakes and a user can make both at once. Returning early on
	// the key problem hid the catalog warning in exactly the case that motivated
	// it -- someone setting a model they have not configured a key for yet.
	return append(out, unknownModelProblems(field, spec)...)
}

// unknownModelProblems warns when the model half of a spec matches no catalog
// entry for a provider whose catalog is known.
//
// Nothing validated the model half at all, so `idea model set
// anthropic/claude-3-7-sonnet` printed success and the first call came back a
// non-retryable 404 -- and nine such ids shipped in the documentation before an
// audit caught them by reading.
//
// A warning, not an error: a valid model can postdate the bundled catalog, and
// refusing it would make the tool unusable the week a provider ships something.
// openrouter and ollama are skipped entirely: openrouter's ids are arbitrary
// upstream strings, and ollama serves whatever the operator has pulled locally.
// For both, the bundled catalog cannot be authoritative by construction, so a
// warning there would be noise on correct configurations.
func unknownModelProblems(field, spec string) []Problem {
	provider, name := ParseModelSpec(spec)
	if name == "" || provider == "openrouter" || provider == "ollama" {
		return nil
	}
	known, ok := model.DefaultModels[provider]
	if !ok || len(known) == 0 {
		return nil
	}
	for _, m := range known {
		if m == name {
			return nil
		}
	}
	return []Problem{{
		Severity: SeverityWarn,
		Field:    field,
		Message: fmt.Sprintf("%q is not in the bundled catalog for %s (known: %s). "+
			"If the provider has since added it this is fine; if it is a typo, the first "+
			"model call will fail with a non-retryable 404.",
			name, provider, strings.Join(known, ", ")),
	}}
}

// providerProblems validates one configured provider entry.
func providerProblems(name string, pc ProviderConfig, dotEnv map[string]string) []Problem {
	field := fmt.Sprintf("harness.providers.%s", name)
	var problems []Problem
	if err := ValidateProvider(name, pc); err != nil {
		problems = append(problems, Problem{Severity: SeverityError, Field: field + ".base_url", Message: err.Error()})
	}
	if pc.Type != "" && !pc.Type.Valid() {
		problems = append(problems, Problem{
			Severity: SeverityError,
			Field:    field + ".type",
			Message:  fmt.Sprintf("unknown type %q; must be one of openai|anthropic|gemini", pc.Type),
		})
	}

	_, _, keyEnv := ResolveProvider(name, pc)
	needsKey := true
	if p := PresetFor(name); p != nil {
		needsKey = p.NeedsKey
	}
	return append(problems, keyProblems(field, name, keyEnv, needsKey, dotEnv)...)
}

// keyProblems reports a missing API key as a warn, never an error: running
// without a key is demo mode, which the product supports on purpose.
func keyProblems(field, provider, keyEnv string, needsKey bool, dotEnv map[string]string) []Problem {
	if !needsKey {
		return nil
	}
	if keyEnv == "" {
		return []Problem{{
			Severity: SeverityWarn,
			Field:    field,
			Message:  fmt.Sprintf("provider %q has no api_key_env; requests will run unauthenticated (demo output)", provider),
		}}
	}
	if lookupKey(keyEnv, dotEnv) != "" {
		return nil
	}
	return []Problem{{
		Severity: SeverityWarn,
		Field:    field,
		Message:  fmt.Sprintf("provider %q needs %s, which is not set; the harness will produce demo output", provider, keyEnv),
	}}
}

func lookupKey(name string, dotEnv map[string]string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return dotEnv[name]
}

// ValidateProvider checks one provider entry on the WRITE path (and, via
// Problems, on a hand-edited harness.yaml). A provider entry decides where
// prompts and an API key are sent, so it is validated before it is stored, not
// when it is first used.
func ValidateProvider(name string, pc ProviderConfig) error {
	if pc.BaseURL == "" {
		return nil
	}
	if err := ValidateBaseURL(pc.BaseURL); err != nil {
		return err
	}
	// Overriding a preset's endpoint is the exfiltration shape: a well-known
	// provider name keeps inheriting its well-known api_key_env, but the
	// credential and the full prompt context now go to the operator-supplied
	// host. Keyless local presets (ollama) may still move between loopback
	// ports, because nothing leaves the machine.
	p := PresetFor(name)
	if p == nil || pc.BaseURL == p.BaseURL {
		return nil
	}
	if !p.NeedsKey && p.DefaultKeyEnv == "" && isLoopbackURL(pc.BaseURL) {
		return nil
	}
	return fmt.Errorf("provider %q is a built-in preset; its base_url may not be overridden (it would send %s and full prompt context to %s). Configure a differently-named provider instead",
		name, presetKeyEnvLabel(p), pc.BaseURL)
}

func presetKeyEnvLabel(p *Preset) string {
	if p.DefaultKeyEnv == "" {
		return "requests"
	}
	return p.DefaultKeyEnv
}

// ValidateBaseURL requires https, except on loopback where http is the norm
// for local runtimes (ollama, LM Studio). Plain http to any other host puts the
// API key and the prompt on the wire in the clear.
func ValidateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.Opaque != "" {
		return fmt.Errorf("base_url %q is not an absolute http(s) URL", raw)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("base_url %q uses http; http is allowed only for loopback (localhost, 127.0.0.1, ::1)", raw)
	default:
		return fmt.Errorf("base_url %q must use https (or http on loopback), got scheme %q", raw, u.Scheme)
	}
}

func isLoopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return isLoopbackHost(u.Hostname())
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedNames(set map[string]bool) []string { return sortedKeys(set) }
