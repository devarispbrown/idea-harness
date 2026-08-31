package model

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Registry holds multiple named providers and routes requests by model spec
// ("provider/model"). It supports OpenAI, Anthropic, Gemini, OpenRouter,
// Ollama, and arbitrary OpenAI-compatible endpoints (§40).
type Registry struct {
	providers       map[string]Provider
	defaultProvider string
}

// NewRegistry builds an empty registry with a default provider name.
func NewRegistry(defaultProvider string) *Registry {
	return &Registry{providers: map[string]Provider{}, defaultProvider: defaultProvider}
}

// Add registers a provider under a name.
func (r *Registry) Add(name string, p Provider) {
	r.providers[name] = p
}

// SetDefault sets the provider used when a model spec has no "/" prefix.
func (r *Registry) SetDefault(name string) { r.defaultProvider = name }

// Default returns the default provider name.
func (r *Registry) Default() string { return r.defaultProvider }

// Get returns a provider by name.
func (r *Registry) Get(name string) (Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// Names returns the registered provider names, sorted.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.providers))
	for n := range r.providers {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Len returns the number of registered providers.
func (r *Registry) Len() int { return len(r.providers) }

// Resolve maps a model spec to a provider and model name. A spec of
// "provider/model" selects that provider; a bare model name uses the default
// provider (§40). Splitting is on the first slash.
func (r *Registry) Resolve(spec string) (Provider, string, error) {
	spec = strings.TrimSpace(spec)
	providerName, model := splitSpec(spec)
	if providerName == "" {
		providerName = r.defaultProvider
	}
	p, ok := r.providers[providerName]
	if !ok {
		return nil, "", fmt.Errorf("model: unknown provider %q (available: %s)", providerName, strings.Join(r.Names(), ", "))
	}
	if model == "" {
		return nil, "", fmt.Errorf("model: empty model name in %q", spec)
	}
	return p, model, nil
}

// Complete resolves a spec and runs it on the matching provider.
func (r *Registry) Complete(ctx context.Context, spec string, req Request) (Response, error) {
	p, model, err := r.Resolve(spec)
	if err != nil {
		return Response{}, err
	}
	req.Model = model
	return p.Complete(ctx, req)
}

func splitSpec(spec string) (provider, model string) {
	if i := strings.Index(spec, "/"); i >= 0 {
		return spec[:i], spec[i+1:]
	}
	return "", spec
}
