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
	Name       string   `json:"name"`
	Label      string   `json:"label"`
	Type       string   `json:"type"`
	BaseURL    string   `json:"base_url"`
	KeyEnv     string   `json:"key_env"`
	Ready      bool     `json:"ready"`
	Configured bool     `json:"configured"`
	IsDefault  bool     `json:"is_default"`
	Models     []string `json:"models"`
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
		if p := config.PresetFor(name); p != nil {
			label = p.Label
		}
		ready := keyEnv == "" || lookupEnv(keyEnv, dotEnv) != ""
		out = append(out, providerSummary{
			Name:       name,
			Label:      label,
			Type:       string(typ),
			BaseURL:    baseURL,
			KeyEnv:     keyEnv,
			Ready:      ready,
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
		ready := p.DefaultKeyEnv == "" || lookupEnv(p.DefaultKeyEnv, dotEnv) != ""
		out = append(out, providerSummary{
			Name:       p.Key,
			Label:      p.Label,
			Type:       string(p.Type),
			BaseURL:    p.BaseURL,
			KeyEnv:     p.DefaultKeyEnv,
			Ready:      ready,
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

// isDemo reports whether no real provider is configured/available, so output
// will be synthetic (§40).
func isDemo(cfg config.Config, dotEnv map[string]string) bool {
	if len(cfg.Harness.Providers) > 0 {
		return false
	}
	for _, env := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GOOGLE_API_KEY", "OPENROUTER_API_KEY", "DEEPSEEK_API_KEY"} {
		if lookupEnv(env, dotEnv) != "" {
			return false
		}
	}
	return true
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
