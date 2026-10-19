package store

import (
	"context"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestResolveDecisionAppendsProjectMemory pins the auto-append side effect:
// resolving a decision records a bounded "DEC-x: question → resolution" entry in
// the project-wide memory, in the same transaction as the resolve.
func TestResolveDecisionAppendsProjectMemory(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	ini := &domain.Initiative{Title: "t"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	d := &domain.Decision{InitiativeID: ini.ID, Type: domain.DecisionProduct,
		Question: "Should sharing links expire?", Options: []string{"30 days", "never"}}
	if err := db.CreateDecision(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := db.ResolveDecision(ctx, d.ID, "30 days", HumanActor); err != nil {
		t.Fatal(err)
	}

	entries, err := db.ListProjectMemory(ctx, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected 1 memory entry, got %d (err=%v)", len(entries), err)
	}
	if entries[0].Kind != "decision" {
		t.Errorf("kind = %q, want decision", entries[0].Kind)
	}
	if !strings.Contains(entries[0].Body, d.Code) || !strings.Contains(entries[0].Body, "30 days") {
		t.Errorf("memory body does not carry the resolved fact: %q", entries[0].Body)
	}
}

// TestListProjectMemoryNewestFirst pins ordering and the limit.
func TestListProjectMemoryNewestFirst(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	ini := &domain.Initiative{Title: "t"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"one", "two", "three"} {
		d := &domain.Decision{InitiativeID: ini.ID, Question: q}
		if err := db.CreateDecision(ctx, d); err != nil {
			t.Fatal(err)
		}
		if err := db.ResolveDecision(ctx, d.ID, "yes", HumanActor); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := db.ListProjectMemory(ctx, 2)
	if err != nil || len(entries) != 2 {
		t.Fatalf("expected 2 (limit) entries, got %d (err=%v)", len(entries), err)
	}
	if !strings.Contains(entries[0].Body, "three") || !strings.Contains(entries[1].Body, "two") {
		t.Fatalf("expected newest first, got %q then %q", entries[0].Body, entries[1].Body)
	}
}
