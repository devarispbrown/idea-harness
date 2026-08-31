package projection

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idea-harness/idea-harness/internal/domain"
	"github.com/idea-harness/idea-harness/internal/store"
)

func TestBuildUnderstandingSummaryAndJudgment(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	ini := &domain.Initiative{Title: "Recipe sharing", Description: "Share recipes with friends"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRequirement(ctx, &domain.Requirement{
		InitiativeID: ini.ID, Statement: "Share a recipe", Priority: domain.PriorityMust,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateDecision(ctx, &domain.Decision{
		InitiativeID: ini.ID, Question: "Public or private?", Type: domain.DecisionProduct,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateQuestion(ctx, &domain.Question{InitiativeID: ini.ID, Question: "Retention policy?"}); err != nil {
		t.Fatal(err)
	}

	u, err := BuildUnderstanding(ctx, db, *ini)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(u.Summary, "Recipe sharing") {
		t.Fatalf("summary should mention the title: %q", u.Summary)
	}
	if !strings.Contains(u.Summary, "decision") {
		t.Fatalf("summary should mention the open decision: %q", u.Summary)
	}
	if len(u.NeedsJudgment) != 2 {
		t.Fatalf("expected 2 judgment items (1 decision + 1 question), got %d", len(u.NeedsJudgment))
	}
}

func TestBuildNextMovePrecedence(t *testing.T) {
	// Judgment outranks stage advancement.
	ini := domain.Initiative{Status: domain.InitiativeApproved}
	u := Understanding{}
	if nm := BuildNextMove(ini, u); !strings.Contains(nm.Action, "design & execution plan") {
		t.Fatalf("approved should guide toward M2, got %q", nm.Action)
	}

	ini = domain.Initiative{Status: domain.InitiativePlanApproved}
	if nm := BuildNextMove(ini, u); !strings.Contains(nm.Action, "Execution plan approved") {
		t.Fatalf("plan_approved should report a done plan, got %q", nm.Action)
	}

	ini = domain.Initiative{Status: domain.InitiativeRunning}
	u = Understanding{Decisions: []DecisionView{{Status: domain.DecisionOpen}}}
	if nm := BuildNextMove(ini, u); !strings.Contains(nm.Action, "decision") {
		t.Fatalf("open decision should outrank, got %q", nm.Action)
	}
}
