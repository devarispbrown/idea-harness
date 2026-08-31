package model

import (
	"context"
	"testing"
)

func TestRegistryResolve(t *testing.T) {
	fp := NewFakeProvider()
	fp.SetResponse("test", `{"title":"t","summary":"s"}`)
	reg := NewRegistry("openai")
	reg.Add("openai", NewOpenAI("k", ""))
	reg.Add("gemini", NewGemini("k", ""))
	reg.Add("demo", fp)

	p, m, err := reg.Resolve("gemini/gemini-2.0-flash")
	if err != nil {
		t.Fatal(err)
	}
	if m != "gemini-2.0-flash" || p.Name() != "gemini" {
		t.Fatalf("got provider=%s model=%s", p.Name(), m)
	}

	// Bare model uses default provider.
	p, m, err = reg.Resolve("gpt-4o")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "openai" || m != "gpt-4o" {
		t.Fatalf("got provider=%s model=%s", p.Name(), m)
	}

	// OpenRouter slash model.
	reg.Add("openrouter", NewOpenAICompat("openrouter", "k", "https://openrouter.ai/api/v1"))
	p, m, err = reg.Resolve("openrouter/anthropic/claude-3.7-sonnet")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "openrouter" || m != "anthropic/claude-3.7-sonnet" {
		t.Fatalf("got provider=%s model=%s", p.Name(), m)
	}

	// Unknown provider errors.
	if _, _, err := reg.Resolve("nope/x"); err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestRegistryComplete(t *testing.T) {
	fp := NewFakeProvider()
	fp.SetResponse("idea_brief", `{"title":"t","summary":"s"}`)
	reg := NewRegistry("demo")
	reg.Add("demo", fp)
	resp, err := reg.Complete(context.Background(), "demo/any", Request{
		JSONSchemaName: "idea_brief",
		JSONSchema:     []byte(`{"type":"object"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.RawJSON == "" {
		t.Fatal("expected a response")
	}
}

func TestDefaultModelFor(t *testing.T) {
	if DefaultModelFor("openai") == "" {
		t.Fatal("expected a default model for openai")
	}
	if DefaultModelFor("unknown") != "model" {
		t.Fatal("expected fallback model name")
	}
}
