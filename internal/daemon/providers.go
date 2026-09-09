package daemon

import (
	"context"
	"sort"
	"time"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/model"
)

// providerSummary is the API/UI view of one provider (§40).
type providerSummary struct {
	Name    string `json:"name"`
	Label   string `json:"label"`
	Type    string `json:"type"`
	BaseURL string `json:"base_url"`
	KeyEnv  string `json:"key_env"`
	Ready   bool   `json:"ready"`
	// KeySet reports only whether the daemon can see this provider's key, in
	// its own environment or the project .env it loaded. Ready folds three
	// separate facts together -- a refused entry, a keyless provider, and a
	// missing key -- and a caller that needs to tell "no key" from "this entry
	// was rejected" cannot. `idea doctor` needs exactly that distinction: the
	// fix for one is `idea daemon stop` and the fix for the other is editing
	// harness.yaml, and telling a user to restart the daemon over a bad
	// base_url names a fix that changes nothing.
	KeySet     bool     `json:"key_set"`
	Configured bool     `json:"configured"`
	IsDefault  bool     `json:"is_default"`
	Models     []string `json:"models"`
	// Problem explains why a configured entry is not being used, and is empty
	// for a healthy one. An entry the registry refused (see
	// resolveProviderEntries) must not be shown as the live endpoint: BaseURL
	// below reports what requests will ACTUALLY reach, so the view cannot tell
	// the operator their key is going somewhere it is not.
	Problem string `json:"problem,omitempty"`
}

// providersView is the GET /providers response.
type providersView struct {
	DefaultModel string            `json:"default_model"`
	Models       map[string]string `json:"models,omitempty"`
	GitCommit    bool              `json:"git_commit"`
	Demo         bool              `json:"demo"`
	Providers    []providerSummary `json:"providers"`
}

// buildProvidersView builds the provider summary from config, presets, the
// model catalog, and resolved env/.env keys (§40, §47). For providers that are
// ready (have a key, or are local), the model list is fetched LIVE from the
// provider's API so the UI/CLI always shows the latest models; the catalog is
// only a fallback.
func buildProvidersView(ctx context.Context, cfg config.Config, dotEnv map[string]string, reg *model.Registry) providersView {
	defaultProv, _ := config.ParseModelSpec(cfg.Harness.DefaultModel)

	seen := map[string]bool{}
	out := make([]providerSummary, 0, len(config.Presets)+len(cfg.Harness.Providers))

	for name, pc := range cfg.Harness.Providers {
		typ, baseURL, keyEnv := config.ResolveProvider(name, pc)
		label := name
		preset := config.PresetFor(name)
		if preset != nil {
			label = preset.Label
		}
		// A refused entry is not what the registry uses, so report the endpoint
		// that requests actually reach: a preset falls back to its built-in
		// endpoint, a custom name resolves nowhere at all.
		problem := ""
		if err := config.ValidateProvider(name, pc); err != nil {
			problem = err.Error()
			if preset != nil {
				_, baseURL, keyEnv = config.ResolveProvider(name, config.ProviderConfig{})
			} else {
				baseURL = ""
			}
		}
		keySet := keyEnv == "" || lookupEnv(keyEnv, dotEnv) != ""
		ready := problem == "" && keySet
		out = append(out, providerSummary{
			Problem:    problem,
			Name:       name,
			Label:      label,
			Type:       string(typ),
			BaseURL:    baseURL,
			KeyEnv:     keyEnv,
			Ready:      ready,
			KeySet:     keySet,
			Configured: true,
			IsDefault:  name == defaultProv,
			Models:     providerModels(ctx, reg, name, model.DefaultModels[name], pc.Models, ready),
		})
		seen[name] = true
	}

	// Offer unconfigured presets so the UI can add them.
	for _, p := range config.Presets {
		if seen[p.Key] {
			continue
		}
		keySet := p.DefaultKeyEnv == "" || lookupEnv(p.DefaultKeyEnv, dotEnv) != ""
		ready := keySet
		out = append(out, providerSummary{
			Name:       p.Key,
			Label:      p.Label,
			Type:       string(p.Type),
			BaseURL:    p.BaseURL,
			KeyEnv:     p.DefaultKeyEnv,
			Ready:      ready,
			KeySet:     keySet,
			Configured: false,
			IsDefault:  p.Key == defaultProv,
			Models:     providerModels(ctx, reg, p.Key, model.DefaultModels[p.Key], nil, ready),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return providersView{DefaultModel: cfg.Harness.DefaultModel, Models: cfg.Harness.Models, GitCommit: cfg.Harness.GitCommit, Demo: isDemo(cfg, dotEnv), Providers: out}
}

// providerModels returns the live model list for a ready provider, falling back
// to the catalog. The user's configured models are always merged in so custom
// entries survive even when the live call fails.
func providerModels(ctx context.Context, reg *model.Registry, name string, catalog, configured []string, ready bool) []string {
	if ready && reg != nil {
		listCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		if live, err := reg.ListModels(listCtx, name); err == nil && len(live) > 0 {
			return mergeModels(live, configured)
		}
	}
	return mergeModels(catalog, configured)
}

// demoProviderName is the registry key of the built-in deterministic provider.
// It is the value defaultProviderName falls through to, and the one isDemo
// compares against, so the banner and the registry cannot disagree.
const demoProviderName = "idea-harness-default"

// isDemo reports whether output will actually be synthetic (§40).
//
// It asks the same question buildRegistry does — what provider will the default
// model resolve to — rather than scanning for key environment variables, which
// is a different question and was answering it wrongly in both directions:
//
//   - `idea model set gpt-4o` (no provider) with OPENAI_API_KEY exported:
//     defaultProviderName finds no slash and no configured provider, so
//     everything resolved to the demo provider, while this function saw the key
//     and suppressed the banner. `idea model set`, `idea config validate`,
//     `idea doctor`, `idea new` and `idea status` all reported success and the
//     user believed gpt-4o wrote their PRD. `model=gpt-4o actual=demo` appeared
//     only in the daemon log.
//   - the same with no default_model at all: a key alone does not make a
//     provider the default, but it did silence the banner.
//
// A configured provider whose key is missing is a different problem — the call
// fails 401 rather than going synthetic — and keyProblems reports that one.
func isDemo(cfg config.Config, dotEnv map[string]string) bool {
	return defaultProviderName(cfg) == demoProviderName
}

// mergeModels merges the catalog defaults with any config-provided models,
// deduplicating and preserving order (catalog first).
func mergeModels(catalog, configured []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(catalog)+len(configured))
	for _, m := range catalog {
		if m != "" && !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	for _, m := range configured {
		if m != "" && !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}
