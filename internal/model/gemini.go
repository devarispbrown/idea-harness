package model

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// GeminiProvider calls the Google generative-language API natively (§41).
type GeminiProvider struct {
	apiKey  string
	baseURL string
	client  *http.Client
	caps    Capabilities
}

// NewGemini builds a Google Gemini provider. baseURL defaults to the v1beta
// generative-language endpoint.
func NewGemini(apiKey, baseURL string) *GeminiProvider {
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com/v1beta"
	}
	return &GeminiProvider{
		apiKey:  apiKey,
		baseURL: baseURL,
		client:  &http.Client{Timeout: 5 * time.Minute},
		caps: Capabilities{
			StructuredOutput:  true,
			Tools:             true,
			Streaming:         true,
			ReasoningControls: false,
			ContextLimit:      1000000,
			MaxOutput:         8192,
		},
	}
}

func (p *GeminiProvider) Name() string              { return "gemini" }
func (p *GeminiProvider) Capabilities() Capabilities { return p.caps }

type geminiPart struct {
	Text       string           `json:"text,omitempty"`
	InlineData *geminiInlineData `json:"inline_data,omitempty"`
}

type geminiInlineData struct {
	MimeType string `json:"mime_type"`
	Data     string `json:"data"`
}

// geminiParts renders the user content parts, appending image inline_data
// parts when a request carries images.
func geminiParts(req Request) []geminiPart {
	parts := make([]geminiPart, 0, len(req.Images)+1)
	for _, img := range req.Images {
		parts = append(parts, geminiPart{InlineData: &geminiInlineData{
			MimeType: img.MediaType, Data: base64.StdEncoding.EncodeToString(img.Data),
		}})
	}
	if text := lastUserText(req); text != "" {
		parts = append(parts, geminiPart{Text: text})
	}
	return parts
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiRequest struct {
	SystemInstruction *geminiContent    `json:"systemInstruction,omitempty"`
	Contents          []geminiContent   `json:"contents"`
	GenerationConfig  geminiGenConfig   `json:"generationConfig,omitempty"`
}

type geminiGenConfig struct {
	ResponseMimeType string `json:"responseMimeType,omitempty"`
	MaxOutputTokens  int    `json:"maxOutputTokens,omitempty"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
		} `json:"content"`
		// "MAX_TOKENS" means the budget was hit, not that the model finished.
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
	ModelVersion string `json:"modelVersion"`
}

// Complete implements Provider.
func (p *GeminiProvider) Complete(ctx context.Context, req Request) (Response, error) {
	system := req.System
	if len(req.JSONSchema) > 0 {
		if system != "" {
			system += "\n"
		}
		system += "Respond with a single JSON object matching the requested schema. Do not wrap in markdown."
	}

	body := geminiRequest{
		Contents:         []geminiContent{{Role: "user", Parts: geminiParts(req)}},
		GenerationConfig: geminiGenConfig{MaxOutputTokens: capOutput(req.MaxOutput, p.caps)},
	}
	if system != "" {
		body.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: system}}}
	}
	if len(req.JSONSchema) > 0 {
		body.GenerationConfig.ResponseMimeType = "application/json"
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return Response{}, err
	}

	model := strings.TrimPrefix(req.Model, "models/")
	url := fmt.Sprintf("%s/models/%s:generateContent", p.baseURL, model)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &buf)
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("x-goog-api-key", p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("gemini: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Gemini was already the only provider that kept its error body -- which
		// is why its failures were diagnosable and the other two were not. It
		// now shares the same helper so all three behave identically, including
		// when the body is not JSON (the map decode used to drop it silently).
		return Response{}, httpError("gemini", req.Model, resp)
	}
	var parsed geminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return Response{}, fmt.Errorf("gemini: decode: %w", err)
	}

	// Concatenate every text part — Gemini can split a reply across multiple
	// parts, and reading only Parts[0].Text drops the rest.
	var sb strings.Builder
	if len(parsed.Candidates) > 0 {
		for _, part := range parsed.Candidates[0].Content.Parts {
			sb.WriteString(part.Text)
		}
	}
	content := sb.String()
	if strings.TrimSpace(content) == "" {
		return Response{}, fmt.Errorf("gemini: empty content (output_tokens=%d)", parsed.UsageMetadata.CandidatesTokenCount)
	}
	if len(parsed.Candidates) > 0 && parsed.Candidates[0].FinishReason == "MAX_TOKENS" {
		return Response{}, truncatedError("gemini", req.MaxOutput)
	}
	actual := parsed.ModelVersion
	if actual == "" {
		actual = req.Model
	}
	in, out := int64(parsed.UsageMetadata.PromptTokenCount), int64(parsed.UsageMetadata.CandidatesTokenCount)
	return Response{
		Text:        content,
		RawJSON:     content,
		Usage:       Usage{InputTokens: in, OutputTokens: out, CostUSD: CostUSD(actual, in, out)},
		ActualModel: actual,
	}, nil
}

// lastUserText returns the concatenated user message contents for Gemini's
// single-turn contents field.
func lastUserText(req Request) string {
	var b strings.Builder
	for _, m := range req.Messages {
		if m.Role == "user" {
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(m.Content)
		}
	}
	return b.String()
}
