package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// AnthropicProvider calls the Anthropic messages API natively (§41).
type AnthropicProvider struct {
	apiKey  string
	baseURL string
	client  *http.Client
	caps    Capabilities
}

// NewAnthropic builds an Anthropic provider.
func NewAnthropic(apiKey, baseURL string) *AnthropicProvider {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com/v1"
	}
	return &AnthropicProvider{
		apiKey:  apiKey,
		baseURL: baseURL,
		client:  &http.Client{Timeout: 5 * time.Minute},
		caps: Capabilities{
			StructuredOutput:  true,
			Tools:             true,
			Streaming:         true,
			ReasoningControls: false,
			ContextLimit:      200000,
			MaxOutput:         16384,
		},
	}
}

func (p *AnthropicProvider) Name() string               { return "anthropic" }
func (p *AnthropicProvider) Capabilities() Capabilities { return p.caps }

type anthropicRequest struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Messages  []Message `json:"messages"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Model string `json:"model"`
}

// Complete implements Provider.
func (p *AnthropicProvider) Complete(ctx context.Context, req Request) (Response, error) {
	system := req.System
	if len(req.JSONSchema) > 0 {
		if system != "" {
			system += "\n"
		}
		system += "Respond with a single JSON object matching the requested schema. Do not wrap in markdown."
	}
	body := anthropicRequest{
		Model:     req.Model,
		MaxTokens: req.MaxOutput,
		System:    system,
		Messages:  req.Messages,
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return Response{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/messages", &buf)
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("anthropic: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Response{}, fmt.Errorf("anthropic: status %d", resp.StatusCode)
	}
	var parsed anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return Response{}, fmt.Errorf("anthropic: decode: %w", err)
	}
	// Concatenate every text block: Anthropic can split a reply across multiple
	// content blocks, and reading only Content[0].Text drops the rest (which made
	// a model look like it returned empty output despite nonzero output tokens).
	var sb strings.Builder
	for _, block := range parsed.Content {
		if block.Type == "text" {
			sb.WriteString(block.Text)
		}
	}
	content := sb.String()
	if strings.TrimSpace(content) == "" {
		return Response{}, fmt.Errorf("anthropic: empty content (stop_reason=%s, output_tokens=%d, blocks=%d)", parsed.StopReason, parsed.Usage.OutputTokens, len(parsed.Content))
	}
	actual := parsed.Model
	if actual == "" {
		actual = req.Model
	}
	in, out := int64(parsed.Usage.InputTokens), int64(parsed.Usage.OutputTokens)
	return Response{
		Text:        content,
		RawJSON:     content,
		Usage:       Usage{InputTokens: in, OutputTokens: out, CostUSD: CostUSD(actual, in, out)},
		ActualModel: actual,
	}, nil
}
