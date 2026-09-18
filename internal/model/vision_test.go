package model

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const pngBytes = "\x89PNG\r\n\x1a\nfake-image-bytes"

// captureBody returns a test server that records the raw request body and
// responds with the given JSON.
func captureBody(t *testing.T, responseJSON string, captured *[]byte) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		*captured = b
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responseJSON))
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestOpenAIVisionRendersImageURL(t *testing.T) {
	var body []byte
	ts := captureBody(t, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1},"model":"gpt-4o"}`, &body)
	p := NewOpenAICompat("openai", "k", ts.URL)
	_, err := p.Complete(t.Context(), Request{
		Model:     "gpt-4o",
		Messages:  []Message{{Role: "user", Content: "describe"}},
		Images:    []Image{{Data: []byte(pngBytes), MediaType: "image/png"}},
		MaxOutput: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if !strings.Contains(s, `"image_url"`) || !strings.Contains(s, `data:image/png;base64,`) {
		t.Fatalf("OpenAI request missing image_url part: %s", s)
	}
}

func TestAnthropicVisionRendersImageBlock(t *testing.T) {
	var body []byte
	ts := captureBody(t, `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1},"model":"claude"}`, &body)
	p := NewAnthropic("k", ts.URL)
	_, err := p.Complete(t.Context(), Request{
		Model:     "claude",
		Messages:  []Message{{Role: "user", Content: "describe"}},
		Images:    []Image{{Data: []byte(pngBytes), MediaType: "image/png"}},
		MaxOutput: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if !strings.Contains(s, `"type":"image"`) || !strings.Contains(s, `"media_type":"image/png"`) {
		t.Fatalf("Anthropic request missing image block: %s", s)
	}
}

func TestGeminiVisionRendersInlineData(t *testing.T) {
	var body []byte
	ts := captureBody(t, `{"candidates":[{"content":{"parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1},"modelVersion":"gemini-2.5-flash"}`, &body)
	p := NewGemini("k", ts.URL)
	_, err := p.Complete(t.Context(), Request{
		Model:     "gemini-2.5-flash",
		Messages:  []Message{{Role: "user", Content: "describe"}},
		Images:    []Image{{Data: []byte(pngBytes), MediaType: "image/png"}},
		MaxOutput: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if !strings.Contains(s, `"inline_data"`) || !strings.Contains(s, `"mime_type":"image/png"`) {
		t.Fatalf("Gemini request missing inline_data part: %s", s)
	}
}

// A local model costs nothing per token. At the default rate every Ollama call
// spent max_cost_usd budget, so a long local run stopped on a ceiling it had
// not reached. Local is decided by the endpoint, not only the name "ollama":
// a local server registered under another name is just as free.
func TestLocalModelCallsCostNothing(t *testing.T) {
	var body []byte
	resp := `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1000000,"completion_tokens":1000000},"model":"qwen3.8"}`
	for _, name := range []string{"ollama", "my-local-server"} {
		ts := captureBody(t, resp, &body) // listens on 127.0.0.1
		got, err := NewOpenAICompat(name, "k", ts.URL).Complete(t.Context(), Request{
			Model:    "qwen3.8",
			Messages: []Message{{Role: "user", Content: "hi"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if got.Usage.CostUSD != 0 {
			t.Errorf("%s on loopback cost %f", name, got.Usage.CostUSD)
		}
	}
	for url, want := range map[string]bool{
		"http://localhost:11434/v1":    true,
		"http://127.0.0.1:8080/v1":     true,
		"http://[::1]:11434/v1":        true,
		"https://api.openai.com/v1":    false,
		"https://openrouter.ai/api/v1": false,
	} {
		if got := isLocalEndpoint(url); got != want {
			t.Errorf("isLocalEndpoint(%s) = %v, want %v", url, got, want)
		}
	}
}
