package model

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// ModelLister is implemented by providers that can list their available models.
// The catalog (DefaultModels) is only a fallback when a provider cannot (or
// does not need to) enumerate models live.
type ModelLister interface {
	ListModels(ctx context.Context) ([]string, error)
}

// ListModels lists models for an OpenAI-compatible provider (OpenAI, OpenRouter,
// Groq, Together, …) via GET /models.
func (p *OpenAIProvider) ListModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list models: status %d", resp.StatusCode)
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	return out, nil
}

// ListModels lists models for Anthropic via GET /models.
func (p *AnthropicProvider) ListModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list models: status %d", resp.StatusCode)
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	return out, nil
}

// ListModels lists models for Google Gemini via GET /models.
func (p *GeminiProvider) ListModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-goog-api-key", p.apiKey)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list models: status %d", resp.StatusCode)
	}
	var parsed struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(parsed.Models))
	for _, m := range parsed.Models {
		if name := strings.TrimPrefix(m.Name, "models/"); name != "" {
			out = append(out, name)
		}
	}
	return out, nil
}

// OllamaProvider is an OpenAI-compatible local provider that additionally
// enumerates installed models via GET /api/tags (at the server root, not /v1).
type OllamaProvider struct {
	*OpenAIProvider
	root string
}

// NewOllama builds an Ollama provider for the given OpenAI-compatible base URL
// (e.g. http://localhost:11434/v1).
func NewOllama(baseURL string) *OllamaProvider {
	if baseURL == "" {
		baseURL = "http://localhost:11434/v1"
	}
	return &OllamaProvider{
		OpenAIProvider: NewOpenAICompat("ollama", "", baseURL),
		root:           strings.TrimSuffix(baseURL, "/v1"),
	}
}

// ListModels lists installed Ollama models via GET /api/tags.
func (p *OllamaProvider) ListModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.root+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list models: status %d", resp.StatusCode)
	}
	var parsed struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(parsed.Models))
	for _, m := range parsed.Models {
		if m.Name != "" {
			out = append(out, m.Name)
		}
	}
	return out, nil
}

// ListModels returns live models for a provider, falling back to the catalog.
func (r *Registry) ListModels(ctx context.Context, name string) ([]string, error) {
	p, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("model: unknown provider %q", name)
	}
	if lister, ok := p.(ModelLister); ok {
		if models, err := lister.ListModels(ctx); err == nil && len(models) > 0 {
			return models, nil
		}
	}
	if models, ok := DefaultModels[name]; ok {
		return models, nil
	}
	return nil, fmt.Errorf("model: no models available for provider %q", name)
}
