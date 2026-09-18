package model

import (
	"context"
	"strings"
	"testing"
)

// TestUserIdeaStripsTheAppendedSchemaInstruction is G2.
//
// runModel (internal/workflow/engine.go) appends
// "Return a single JSON object matching this schema (no markdown fences):\n<schema>"
// to the system prompt right after the compiled "--- task ... ---" section,
// with no closing "--- " delimiter of its own. taskSection's scan for the next
// section boundary ran straight through it, so the demo idea brief printed the
// user's idea immediately followed by that whole instruction a second time.
func TestUserIdeaStripsTheAppendedSchemaInstruction(t *testing.T) {
	system := "--- task (trust: trusted) ---\n" +
		"Build a recipe sharing app for home cooks" +
		"\n\nReturn a single JSON object matching this schema (no markdown fences):\n" +
		`{"type":"object","properties":{"title":{"type":"string"}}}`

	idea := userIdea(Request{System: system})
	if strings.Contains(idea, "Return a single JSON object matching this schema") {
		t.Fatalf("userIdea leaked the appended schema instruction: %q", idea)
	}
	if idea != "Build a recipe sharing app for home cooks" {
		t.Fatalf("userIdea = %q, want just the task text", idea)
	}
}

// TestFakeProviderCompleteDoesNotDoubleUpTheSchemaInstruction is the
// end-to-end version: the synthesized idea brief must not print the schema
// instruction at all, since it is not part of the user's idea.
func TestFakeProviderCompleteDoesNotDoubleUpTheSchemaInstruction(t *testing.T) {
	system := "--- task (trust: trusted) ---\n" +
		"Build a recipe sharing app for home cooks" +
		"\n\nReturn a single JSON object matching this schema (no markdown fences):\n" +
		`{"type":"object"}`

	fp := NewFakeProvider()
	resp, err := fp.Complete(context.Background(), Request{System: system, JSONSchemaName: "idea_brief"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(resp.RawJSON, "Return a single JSON object matching this schema") {
		t.Fatalf("demo idea brief contains the schema instruction: %s", resp.RawJSON)
	}
}
