// Package model provides the provider abstraction (§40) and the centralized
// structured-output pipeline (§39). Providers are config-driven; the fake
// provider is the deterministic CI path, while the HTTP adapters are exercised
// by the tagged real-provider smoke test.
package model

import (
	"context"
	"encoding/base64"
)

// dataURL renders an image as a base64 data URL for providers that accept
// inline image URLs.
func dataURL(img Image) string {
	return "data:" + img.MediaType + ";base64," + base64.StdEncoding.EncodeToString(img.Data)
}

// Capabilities describes what a provider/model supports (§40).
type Capabilities struct {
	StructuredOutput  bool
	Tools             bool
	Streaming         bool
	ReasoningControls bool
	ContextLimit      int
	MaxOutput         int
}

// Message is a single conversation turn.
type Message struct {
	Role    string `json:"role"` // system | user | assistant
	Content string `json:"content"`
}

// Image is one image attached to a request (for vision models).
type Image struct {
	Data      []byte
	MediaType string // e.g. "image/png", "image/jpeg"
}

// Request is a normalized model request. Images, when present, are attached to
// the (single) user message as vision content by providers that support it.
type Request struct {
	Model           string
	System          string
	Messages        []Message
	Images          []Image
	ReasoningEffort string
	MaxOutput       int
	JSONSchema      []byte // if set, structured output is requested
	JSONSchemaName  string
}

// Usage records token/cost usage (§36).
type Usage struct {
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
}

// Response is a normalized model response. RawJSON is the provider's raw
// (post-redaction) output; Text is the plain-text form when not structured.
type Response struct {
	Text        string
	RawJSON     string
	Usage       Usage
	ActualModel string
}

// Provider is a model provider adapter.
type Provider interface {
	Name() string
	Capabilities() Capabilities
	// Complete runs one request and returns a response. It must set
	// ActualModel to the model actually used (no silent fallback, §36).
	Complete(ctx context.Context, req Request) (Response, error)
}
