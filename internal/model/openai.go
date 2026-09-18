package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
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
	Messages       []openAIMessage     `json:"messages"`
	MaxTokens      int                 `json:"max_completion_tokens,omitempty"`
	ResponseFormat *openAIResponseFormat `json:"response_format,omitempty"`
}

// openAIMessage renders a message whose content is either a plain string or,
// for a vision request, an array of text/image_url parts.
type openAIMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type openAIContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *openAIImageURL `json:"image_url,omitempty"`
}

type openAIImageURL struct {
	URL string `json:"url"`
}

type openAIResponseFormat struct {
	Type string `json:"type"`
}

// openAIMessages prepends the system message and renders any user message with
// attached images as multimodal content parts.
func openAIMessages(req Request, system string) []openAIMessage {
	msgs := []openAIMessage{{Role: "system", Content: system}}
	for _, m := range req.Messages {
		if m.Role == "user" && len(req.Images) > 0 {
			parts := make([]openAIContentPart, 0, 1+len(req.Images))
			if m.Content != "" {
				parts = append(parts, openAIContentPart{Type: "text", Text: m.Content})
			}
			for _, img := range req.Images {
				parts = append(parts, openAIContentPart{Type: "image_url", ImageURL: &openAIImageURL{URL: dataURL(img)}})
			}
			msgs = append(msgs, openAIMessage{Role: "user", Content: parts})
			continue
		}
		msgs = append(msgs, openAIMessage{Role: m.Role, Content: m.Content})
	}
	return msgs
}

type openAIChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		// "length" means the budget was hit, not that the model finished.
		FinishReason string `json:"finish_reason"`
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
		Messages:  openAIMessages(req, system),
		MaxTokens: capOutput(req.MaxOutput, p.caps),
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
		return Response{}, httpError("openai", resp)
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
	if len(parsed.Choices) > 0 && parsed.Choices[0].FinishReason == "length" {
		return Response{}, truncatedError("openai", req.MaxOutput)
	}
	actual := parsed.Model
	if actual == "" {
		actual = req.Model
	}
	in, out := int64(parsed.Usage.PromptTokens), int64(parsed.Usage.CompletionTokens)
	cost := CostUSD(actual, in, out)
	if p.name == "ollama" || isLocalEndpoint(p.baseURL) {
		// A local model -- Ollama, or any server on this machine -- costs
		// nothing per token. At the default rate every
		// Ollama call spent max_cost_usd budget, and a long local run stopped
		// on a ceiling it had not reached.
		cost = 0
	}
	return Response{
		Text:        content,
		RawJSON:     content,
		Usage:       Usage{InputTokens: in, OutputTokens: out, CostUSD: cost},
		ActualModel: actual,
	}, nil
}

// isLocalEndpoint reports whether baseURL is on this machine.
func isLocalEndpoint(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	h := u.Hostname()
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
