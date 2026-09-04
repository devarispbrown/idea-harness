package model

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

var updateWire = flag.Bool("update-wire", false, "rewrite the recorded provider request bodies")

// TestProviderRequestWireFormat records the exact JSON each provider puts on
// the wire, for one representative request.
//
// This is the cheap mechanical stand-in for the real provider call no audit
// ever made. Four rounds of guards compared artifacts to artifacts — routes to
// specs, docs to code — and nothing compared the *request* to what a provider
// would accept. That is how `max_tokens: 32000` against a declared 16384
// ceiling shipped in v0.1.0 and survived until someone finally spent a real API
// key: every CI run was demo-mode, and the provider tests used httptest fakes
// at MaxOutput 100, so the shape at real budgets was never exercised.
//
// A golden file makes the wire shape reviewable in a diff. It cannot know what
// a provider will accept, so it is paired with the ceiling assertions below —
// but it does make a silent change to the request visible, which is the class
// that hurt here.
func TestProviderRequestWireFormat(t *testing.T) {
	for _, c := range []struct {
		name   string
		golden string
		build  func(u string) error
	}{
		{
			name:   "openai",
			golden: "openai_request.json",
			build: func(u string) error {
				_, err := NewOpenAI("k", u).Complete(context.Background(), goldenRequest())
				return err
			},
		},
		{
			name:   "anthropic",
			golden: "anthropic_request.json",
			build: func(u string) error {
				_, err := NewAnthropic("k", u).Complete(context.Background(), goldenRequest())
				return err
			},
		},
		{
			name:   "gemini",
			golden: "gemini_request.json",
			build: func(u string) error {
				_, err := NewGemini("k", u).Complete(context.Background(), goldenRequest())
				return err
			},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			var captured []byte
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured, _ = io.ReadAll(r.Body)
				w.WriteHeader(http.StatusInternalServerError) // the reply is not what is under test
			}))
			defer ts.Close()
			_ = c.build(ts.URL)

			var pretty bytes.Buffer
			if err := json.Indent(&pretty, captured, "", "  "); err != nil {
				t.Fatalf("provider sent invalid JSON: %v\n%s", err, captured)
			}
			got := append(pretty.Bytes(), '\n')

			path := filepath.Join("testdata", c.golden)
			if *updateWire {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				t.Logf("wrote %s", path)
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v — run `go test ./internal/model -update-wire` to record it", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s changed.\n--- want ---\n%s\n--- got ---\n%s\n"+
					"If this is intended, re-record with -update-wire and read the diff carefully: "+
					"this is the request a provider actually receives.", c.golden, want, got)
			}
		})
	}
}

// goldenRequest is one representative request at a realistic budget. The budget
// is the PRD stage's, because that is the one that was over every ceiling.
func goldenRequest() Request {
	return Request{
		Model:          "test-model",
		System:         "You are a product analyst.",
		Messages:       []Message{{Role: "user", Content: "Build usage-based billing"}},
		MaxOutput:      16384,
		JSONSchema:     []byte(`{"type":"object","properties":{"summary":{"type":"string"}},"required":["summary"]}`),
		JSONSchemaName: "prd",
	}
}

// TestRequestedTokensNeverExceedTheDeclaredCeiling is the assertion the golden
// cannot make: whatever a provider puts on the wire, the token budget in it must
// be one the provider said it can produce.
//
// It reads the emitted body rather than the Go struct, so a provider that
// renames or duplicates the field cannot slip past it.
func TestRequestedTokensNeverExceedTheDeclaredCeiling(t *testing.T) {
	for _, c := range []struct {
		name  string
		prov  Provider
		field string
		call  func(p Provider, u string, req Request) error
	}{
		{"openai", NewOpenAI("k", ""), "max_completion_tokens", callOpenAI},
		{"anthropic", NewAnthropic("k", ""), "max_tokens", callAnthropic},
		{"gemini", NewGemini("k", ""), "maxOutputTokens", callGemini},
	} {
		t.Run(c.name, func(t *testing.T) {
			ceiling := c.prov.Capabilities().MaxOutput
			var captured map[string]any
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(b, &captured)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer ts.Close()

			// Ask for more than the ceiling, as maxOutputForStage does for the PRD.
			req := goldenRequest()
			req.MaxOutput = ceiling * 2
			_ = c.call(c.prov, ts.URL, req)

			got := findNumber(captured, c.field)
			if got == 0 {
				t.Fatalf("no %s in the request body: %v", c.field, captured)
			}
			if int(got) > ceiling {
				t.Errorf("%s sent %s=%d against its own declared ceiling of %d; "+
					"the provider rejects this request outright. The caller must clamp — "+
					"see workflow.effectiveMaxOutput", c.name, c.field, int(got), ceiling)
			}
		})
	}
}

// findNumber finds a numeric field anywhere in a decoded body, because Gemini
// nests its budget under generationConfig.
func findNumber(m map[string]any, key string) float64 {
	for k, v := range m {
		if k == key {
			if f, ok := v.(float64); ok {
				return f
			}
		}
		if nested, ok := v.(map[string]any); ok {
			if f := findNumber(nested, key); f != 0 {
				return f
			}
		}
	}
	return 0
}

func callOpenAI(p Provider, u string, r Request) error {
	_, err := NewOpenAI("k", u).Complete(context.Background(), r)
	return err
}
func callAnthropic(p Provider, u string, r Request) error {
	_, err := NewAnthropic("k", u).Complete(context.Background(), r)
	return err
}
func callGemini(p Provider, u string, r Request) error {
	_, err := NewGemini("k", u).Complete(context.Background(), r)
	return err
}
