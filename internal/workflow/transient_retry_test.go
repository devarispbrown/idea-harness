package workflow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/blob"
	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// openAIChatBody mirrors just enough of the real OpenAI chat-completions
// response shape for the fake server below to answer a structured-output
// request.
type openAIChatBody struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Model string `json:"model"`
}

// TestTransientProviderErrorIsRetriedWithBackoff pins §51: a 503 from a real
// provider (Service Unavailable / overloaded, the shape every provider uses
// for "try again") must be retried with backoff inside the model call, not
// fail the phase outright. This is the model-call half of the ICP walkthrough
// finding "transient provider errors (503/429/overloaded) should be retried
// with backoff... check existing retry policy and make it cover 503" -- it
// goes through the real HTTP path (model.OpenAIProvider -> model.httpError ->
// workflow.isTransientModelError), not the fake provider, because that is the
// only way a 503 ever reaches isTransientModelError's string match on the
// message httpError actually produces.
func TestTransientProviderErrorIsRetriedWithBackoff(t *testing.T) {
	ideaBrief := `{"title":"x","summary":"x","assumptions":[],"open_questions":[]}`

	var requests int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			// The exact shape a real provider sends for a transient overload:
			// a 503 with a short prose body, no special error code field.
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"The server is temporarily overloaded. Please retry."}}`))
			return
		}
		body := openAIChatBody{Model: "gpt-test"}
		body.Choices = []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		}{{FinishReason: "stop"}}
		body.Choices[0].Message.Content = ideaBrief
		body.Usage.PromptTokens, body.Usage.CompletionTokens = 10, 10
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer ts.Close()

	db, err := store.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	blobs, err := blob.New(t.TempDir()+"/blobs", nil)
	if err != nil {
		t.Fatal(err)
	}

	reg := model.NewRegistry("openai")
	reg.Add("openai", model.NewOpenAI("test-key", ts.URL))
	eng := New(db, blobs, reg, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}}, nil)
	eng.SetRawBlobs(blobs)

	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "build it"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	if err := eng.runFraming(ctx, ini.ID, ini.Description); err != nil {
		t.Fatalf("framing failed despite the provider succeeding on retry: %v", err)
	}
	if requests != 2 {
		t.Fatalf("server saw %d requests, want 2 (one 503, one retried success)", requests)
	}

	versions, err := db.ListArtifacts(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 || versions[0].Kind != domain.ArtifactIdeaBrief {
		t.Fatalf("artifacts = %+v, want exactly one idea_brief", versions)
	}
}

// TestIsTransientModelErrorCoversTheCommonShapes locks in the substrings
// isTransientModelError depends on, using error strings shaped exactly like
// model.httpError actually produces them (provider name + "status <code>: "
// + body) for each provider this codebase calls.
func TestIsTransientModelErrorCoversTheCommonShapes(t *testing.T) {
	for _, msg := range []string{
		"openai: status 503: The server is temporarily overloaded. Please retry.",
		"openai: status 503 (no response body)",
		"anthropic: status 529: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}",
		"gemini: status 503: The model is overloaded. Please try again later.",
		"openai: status 429: Rate limit reached for requests",
		"gemini: status 500: internal error",
	} {
		if !isTransientModelError(errString(msg)) {
			t.Errorf("isTransientModelError(%q) = false, want true", msg)
		}
	}
	// A permanent error must not be retried into a wasted 3x cost.
	for _, msg := range []string{
		"openai: status 404: The model `gpt-9-ghost` does not exist",
		"anthropic: status 401: invalid x-api-key",
	} {
		if isTransientModelError(errString(msg)) {
			t.Errorf("isTransientModelError(%q) = true, want false (permanent error)", msg)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }
