package workflow

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
)

func TestPromoteDocComment(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	c := &domain.DocComment{
		InitiativeID:      ini.ID,
		ArtifactVersionID: "some-version",
		Section:           "Pricing",
		Text:              "How is billing structured?",
	}
	if err := db.CreateDocComment(ctx, c); err != nil {
		t.Fatal(err)
	}

	// A blank comment cannot be promoted.
	blank := &domain.DocComment{InitiativeID: ini.ID, ArtifactVersionID: "v", Text: "   "}
	if err := db.CreateDocComment(ctx, blank); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.PromoteDocComment(ctx, blank.ID); err == nil {
		t.Fatal("expected a blank comment to be rejected")
	}

	q, err := eng.PromoteDocComment(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if q.Question != "[Pricing] How is billing structured?" {
		t.Fatalf("question = %q", q.Question)
	}

	// The promoted question is actually persisted for the initiative.
	qs, err := db.ListQuestions(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, x := range qs {
		if x.ID == q.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("promoted question not persisted")
	}

	got, err := db.GetDocComment(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PromotedQuestionID != q.ID {
		t.Fatalf("comment not linked to question: %q", got.PromotedQuestionID)
	}

	if _, err := eng.PromoteDocComment(ctx, c.ID); err == nil {
		t.Fatal("expected a second promote to fail")
	}
}
