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

// TestFirstRequirementCodePrefersTheRequirementsSection is the low-priority
// review item: a requirement code mentioned in an earlier context section
// (e.g. "decisions", carrying history text that names a requirement since
// superseded) must not win over the live code the "requirements" section
// itself lists -- design/plan only ever sees live requirements, so citing
// the dead one produced demo output that referenced a requirement no longer
// in scope. Mutation-check: revert firstRequirementCode to the old
// scan-messages-then-whole-System order, and this fails.
func TestFirstRequirementCodePrefersTheRequirementsSection(t *testing.T) {
	system := "--- decisions (trust: trusted) ---\n" +
		"DEC-1: chose Postgres. REQ-1 was superseded because it assumed MySQL." +
		"\n\n--- requirements (trust: trusted) ---\n" +
		"- REQ-3: Persist data\n" +
		"  - metric target (condition)" +
		"\n\n--- task (trust: trusted) ---\n" +
		"Build the plan"

	if got := firstRequirementCode(Request{System: system}); got != "REQ-3" {
		t.Fatalf("firstRequirementCode = %q, want REQ-3 (the live requirements section), not the superseded REQ-1 mentioned earlier", got)
	}
}

// TestFirstRequirementCodeFallsBackWithoutARequirementsSection confirms the
// old behavior survives when there is no "requirements" section to prefer
// (e.g. a request that never includes one).
func TestFirstRequirementCodeFallsBackWithoutARequirementsSection(t *testing.T) {
	system := "--- task (trust: trusted) ---\nBuild REQ-2 out fully"
	if got := firstRequirementCode(Request{System: system}); got != "REQ-2" {
		t.Fatalf("firstRequirementCode = %q, want REQ-2", got)
	}

	if got := firstRequirementCode(Request{}); got != "" {
		t.Fatalf("firstRequirementCode = %q, want empty for a request naming no requirement", got)
	}
}
