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

// OpenAIProvider calls an OpenAI-compatible chat-completions API (§41). It is
// used for OpenAI, OpenRouter, Ollama, and any custom OpenAI-compatible
// endpoint (Groq, Together, LM Studio, …).
type OpenAIProvider struct {
	name    string
	apiKey  string
	baseURL string
	client  *http.Client
	caps    Capabilities
}

// NewOpenAI builds an OpenAI provider.
func NewOpenAI(apiKey, baseURL string) *OpenAIProvider {
	return NewOpenAICompat("openai", apiKey, baseURL)
}

// NewOpenAICompat builds an OpenAI-compatible provider with a custom name.
func NewOpenAICompat(name, apiKey, baseURL string) *OpenAIProvider {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return &OpenAIProvider{
		name:    name,
		apiKey:  apiKey,
		baseURL: baseURL,
		client:  &http.Client{Timeout: 5 * time.Minute},
		caps: Capabilities{
			StructuredOutput:  true,
			Tools:             true,
			Streaming:         true,
			ReasoningControls: true,
			ContextLimit:      128000,
			MaxOutput:         16384,
		},
	}
}

func (p *OpenAIProvider) Name() string              { return p.name }
func (p *OpenAIProvider) Capabilities() Capabilities { return p.caps }

type openAIChatRequest struct {
	Model          string              `json:"model"`
	Messages       []Message           `json:"messages"`
	MaxTokens      int                 `json:"max_completion_tokens,omitempty"`
	ResponseFormat *openAIResponseFormat `json:"response_format,omitempty"`
}

type openAIResponseFormat struct {
	Type string `json:"type"`
}

type openAIChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Model string `json:"model"`
}

// Complete implements Provider.
func (p *OpenAIProvider) Complete(ctx context.Context, req Request) (Response, error) {
	system := req.System
	if len(req.JSONSchema) > 0 {
		if system != "" {
			system += "\n"
		}
		system += "Respond with a single JSON object matching the requested schema. Do not wrap in markdown."
	}
	body := openAIChatRequest{
		Model:     req.Model,
		Messages:  append([]Message{{Role: "system", Content: system}}, req.Messages...),
		MaxTokens: req.MaxOutput,
	}
	if len(req.JSONSchema) > 0 {
		body.ResponseFormat = &openAIResponseFormat{Type: "json_object"}
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return Response{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", &buf)
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("openai: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Response{}, fmt.Errorf("openai: status %d", resp.StatusCode)
	}
	var parsed openAIChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return Response{}, fmt.Errorf("openai: decode: %w", err)
	}
	content := ""
	if len(parsed.Choices) > 0 {
		content = parsed.Choices[0].Message.Content
	}
	if strings.TrimSpace(content) == "" {
		return Response{}, fmt.Errorf("openai: empty content (completion_tokens=%d)", parsed.Usage.CompletionTokens)
	}
	actual := parsed.Model
	if actual == "" {
		actual = req.Model
	}
	return Response{
		Text:        content,
		RawJSON:     content,
		Usage:       Usage{InputTokens: int64(parsed.Usage.PromptTokens), OutputTokens: int64(parsed.Usage.CompletionTokens)},
		ActualModel: actual,
	}, nil
}
