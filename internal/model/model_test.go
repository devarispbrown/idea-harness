package model

import (
	"context"
	"encoding/json"
	"testing"
)

const testSchema = `{
  "type": "object",
  "properties": {
    "title": {"type": "string"},
    "summary": {"type": "string"}
  },
  "required": ["title", "summary"],
  "additionalProperties": false
}`

func TestDecodeStructuredValid(t *testing.T) {
	res, err := DecodeStructured([]byte(`{"title":"t","summary":"s"}`), []byte(testSchema))
	if err != nil {
		t.Fatal(err)
	}
	if res.WasRepaired {
		t.Fatal("valid JSON should not be repaired")
	}
	if res.Valid["title"] != "t" {
		t.Fatalf("unexpected object %v", res.Valid)
	}
}

func TestDecodeStructuredRepairsTruncation(t *testing.T) {
	// Truncated JSON (missing closing braces) must be repaired in one pass.
	raw := []byte(`{"title":"t","summary":"s"`)
	res, err := DecodeStructured(raw, []byte(testSchema))
	if err != nil {
		t.Fatal(err)
	}
	if !res.WasRepaired {
		t.Fatal("truncated JSON should be repaired")
	}
	if res.Valid["title"] != "t" {
		t.Fatalf("unexpected object %v", res.Valid)
	}
}

func TestDecodeStructuredRejectsInvalid(t *testing.T) {
	// Missing required field, unfixable by repair.
	if _, err := DecodeStructured([]byte(`{"title":"t"}`), []byte(testSchema)); err == nil {
		t.Fatal("expected invalid JSON to be rejected")
	}
}

func TestRepairJSONClosesNestedContainers(t *testing.T) {
	// Truncated mid-array with nested objects: the naive "all braces then all
	// brackets" closure order produces invalid JSON. LIFO closure must recover.
	raw := []byte(`{"answers":[{"question":"q","answer":"a`)
	repaired := string(repairJSON(raw))
	var v map[string]any
	if err := json.Unmarshal([]byte(repaired), &v); err != nil {
		t.Fatalf("repair produced invalid JSON %q: %v", repaired, err)
	}
	answers, ok := v["answers"].([]any)
	if !ok || len(answers) != 1 {
		t.Fatalf("expected one answer, got %v", v)
	}
}

func TestExtractJSONIgnoresBracesInStrings(t *testing.T) {
	// Answer text containing {} and [] must not confuse the object boundary.
	raw := []byte(`{"answers":[{"question":"q","answer":"use {placeholder} and [brackets]"}]} trailing`)
	got := string(extractJSON(raw))
	var v map[string]any
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("extractJSON produced invalid JSON %q: %v", got, err)
	}
	if _, ok := v["answers"]; !ok {
		t.Fatalf("expected answers key, got %v", v)
	}
}

func TestDecodeStructuredEmptyOutput(t *testing.T) {
	// A blank model reply must produce a clear, distinct error rather than the
	// generic "failed schema validation (output: )".
	errCases := [][]byte{{}, []byte(""), []byte("   "), []byte("```json\n```")}
	for i, raw := range errCases {
		if _, err := DecodeStructured(raw, []byte(testSchema)); err == nil {
			t.Fatalf("case %d: expected empty output to be rejected", i)
		}
	}
}

func TestDecodeStructuredStripsMarkdownFencesAndExtras(t *testing.T) {
	// Real models wrap in fences and add extra fields. A lenient schema
	// (additionalProperties: true) accepts the extras.
	lenient := `{"type":"object","properties":{"title":{"type":"string"},"summary":{"type":"string"}},"required":["title","summary"]}`
	raw := []byte("```json\n{\"title\":\"t\",\"summary\":\"s\",\"extra_field\":123}\n```")
	res, err := DecodeStructured(raw, []byte(lenient))
	if err != nil {
		t.Fatal(err)
	}
	if res.Valid["title"] != "t" || res.Valid["summary"] != "s" {
		t.Fatalf("unexpected object %v", res.Valid)
	}
}

func TestFakeProvider(t *testing.T) {
	fp := NewFakeProvider()
	fp.SetResponse("idea_brief", `{"title":"t","summary":"s"}`)
	resp, err := fp.Complete(context.Background(), Request{
		Model:          "fake-model",
		JSONSchemaName: "idea_brief",
		JSONSchema:     []byte(testSchema),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ActualModel != "demo" {
		t.Fatalf("expected actual model to be recorded as demo, got %q", resp.ActualModel)
	}
	if len(fp.Calls()) != 1 {
		t.Fatalf("expected 1 recorded call, got %d", len(fp.Calls()))
	}
}
