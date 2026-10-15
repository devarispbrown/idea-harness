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
	// EnvDependent marks a problem whose answer depends on which process asked.
	// Only the missing-key check is: it resolves the variable with os.Getenv, so
	// the CLI sees the shell you typed in while the run is made by the daemon,
	// which read its own environment once at start. `idea doctor` drops these
	// and asks the daemon instead.
	//
	// A field rather than matching the message text: the first version of this
	// matched a substring, and rewording the message silently un-marked it.
	EnvDependent bool `json:"-"`
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
	// Whether a bare model spec has anywhere to resolve to. This mirrors the
	// daemon's defaultProviderName: a provider named by a qualified
	// default_model, or any configured provider entry. With neither, a bare spec
	// resolves to the built-in demo provider.
	hasDefaultProvider := len(h.Providers) > 0
	if p, _ := ParseModelSpec(h.DefaultModel); p != "" {
		hasDefaultProvider = true
	}
	problems = append(problems, modelSpecProblems("harness.default_model", h.DefaultModel, known, h.Providers, dotEnv, hasDefaultProvider)...)
	problems = append(problems, modelSpecProblems("harness.executor.model", h.Executor.Model, known, h.Providers, dotEnv, hasDefaultProvider)...)
	for _, role := range sortedKeys(h.Models) {
		field := fmt.Sprintf("harness.models.%s", role)
		problems = append(problems, modelSpecProblems(field, h.Models[role], known, h.Providers, dotEnv, hasDefaultProvider)...)
	}
	problems = append(problems, executorProblems(h.Executor, dotEnv)...)
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

// modelSpecProblems checks one "provider/model" field.
//
// A bare model with no slash used to return nil here — "it inherits the
// default, so it is not checked". That is true only when a default provider
// exists. With none, `idea model set gpt-4o` routed the entire workflow to the
// built-in demo provider while this function, `idea config validate` and `idea
// doctor` all reported success at exit 0, and the user believed gpt-4o had
// written their PRD.
func modelSpecProblems(field, spec string, known map[string]bool, providers map[string]ProviderConfig, dotEnv map[string]string, hasDefaultProvider bool) []Problem {
	provider, _ := ParseModelSpec(spec)
	if provider == "" {
		if strings.TrimSpace(spec) == "" {
			return nil // unset is the ordinary case
		}
		// A bare spec resolves against whatever provider is default. With one
		// configured that is legitimate — `models.critic: gpt-4o` alongside
		// `default_model: openai/gpt-4o-mini` means "the default provider's
		// gpt-4o". With none it is silently synthetic, which is the defect.
		if hasDefaultProvider {
			return []Problem{{
				Severity: SeverityWarn,
				Field:    field,
				Message: fmt.Sprintf("%q names no provider; it resolves against the default provider. "+
					"Write it as <provider>/%s to be explicit.", spec, spec),
			}}
		}
		return []Problem{{
			Severity: SeverityError,
			Field:    field,
			Message: fmt.Sprintf("%q names no provider and none is configured, so it resolves to the "+
				"built-in demo provider and every artifact will be synthetic. Write it as "+
				"<provider>/%s — for example openai/%s.", spec, spec, spec),
		}}
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
	return append(out, UnknownModelProblems(field, spec)...)
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
func UnknownModelProblems(field, spec string) []Problem {
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
		Severity:     SeverityWarn,
		Field:        field,
		EnvDependent: true,
		// Deliberately process-neutral. This same text is returned by the daemon
		// through PUT /providers and printed by `idea model set`, so wording it
		// as "not set in this shell" made the daemon claim something about the
		// caller's shell that it cannot know -- and contradicted the very next
		// line `idea model set` prints. Which environment was read is the
		// caller's to say; EnvDependent is how a caller knows to say it.
		Message: fmt.Sprintf("provider %q needs %s, which is not set; the harness will produce demo output", provider, keyEnv),
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
	// An entry that names nothing to send requests to. This is NOT "declares no
	// fields": ResolveProvider fills every field from the preset when the name
	// matches one, so `openai: {}` is a complete, working configuration and
	// internal/daemon/providers.go relies on exactly that as the preset-fallback
	// path. The unusable shape is a name that resolves to no endpoint --
	// `mycustom: {}` -- which was accepted and failed at the first model call
	// instead of here, because of the BaseURL == "" early return below.
	//
	// Here rather than in the handler so it runs on the write path AND again
	// when the registry is built, which is what docs/security.md promises.
	// Consequence, stated rather than discovered: an entry already on disk that
	// resolves to no endpoint now reports `problem` on GET /providers.
	if _, base, _ := ResolveProvider(name, pc); base == "" {
		return fmt.Errorf("provider %q names no endpoint: it is not a built-in preset, so it needs "+
			"a base_url (and an api_key_env if the endpoint requires a key)", name)
	}
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
		if IsLoopbackHost(u.Hostname()) {
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
	return IsLoopbackHost(u.Hostname())
}

// IsLoopbackHost reports whether host names this machine: "localhost" in any
// case, or any loopback IP (all of 127.0.0.0/8, and ::1).
func IsLoopbackHost(host string) bool {
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

// ParseError is a malformed config file: the user's YAML, not the daemon's
// fault. Both shapes it can take — YAML that does not parse, and YAML that
// parses but the schema rejects — because the second is the more common one and
// they are the same problem to a caller: this file has to be edited before any
// request against this project can succeed.
//
// It used to travel as a bare fmt.Errorf, so every project-scoped handler
// mapped it to 404 — `config.Load` runs inside EnsureProject, and a failure
// there is indistinguishable from "no such project" to the caller. That
// violated the spec's own NotFound contract ("the message names the kind and
// the reference that was asked for") and leaked an absolute filesystem path
// into the response, which InternalError explicitly promises never happens.
//
// 500 is the wrong repair: internal/daemon/errors.go reserves it for "the
// daemon's problem", and telling a client to retry a syntax error in a file
// only the user can fix is worse than the 404 was. So this carries enough for a
// 4xx that names the file, the line, and nothing else.
type ParseError struct {
	// File, relative to the project root — never the absolute path.
	File string
	Msg  string
}

func (e *ParseError) Error() string {
	return "config: " + e.File + ": " + e.Msg
}
