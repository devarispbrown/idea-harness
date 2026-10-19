package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
)

func TestStripNeedsClarification(t *testing.T) {
	cases := []struct {
		in   string
		out  string
		flag bool
	}{
		{"[NEEDS CLARIFICATION] How is billing structured?", "How is billing structured?", true},
		{"[needs clarification]: which payment provider", "which payment provider", true},
		{"[NEEDS CLARIFICATION]", "[NEEDS CLARIFICATION]", true},
		{"Plain question", "Plain question", false},
	}
	for _, c := range cases {
		out, flag := stripNeedsClarification(c.in)
		if out != c.out || flag != c.flag {
			t.Errorf("stripNeedsClarification(%q) = (%q, %v), want (%q, %v)", c.in, out, flag, c.out, c.flag)
		}
	}
}

// TestCreateQuestionFlagsClarification verifies the marker is stripped from the
// question text and recorded as the NeedsClarification flag, not matched on text.
func TestCreateQuestionFlagsClarification(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if err := eng.createQuestionIfNew(ctx, ini.ID, "[NEEDS CLARIFICATION] How is billing structured?"); err != nil {
		t.Fatal(err)
	}
	qs, err := db.ListQuestions(ctx, ini.ID)
	if err != nil || len(qs) != 1 {
		t.Fatalf("expected 1 question, got %d (err=%v)", len(qs), err)
	}
	if !qs[0].NeedsClarification {
		t.Errorf("question not flagged needs_clarification: %+v", qs[0])
	}
	if strings.Contains(qs[0].Question, "NEEDS CLARIFICATION") {
		t.Errorf("marker not stripped from question text: %q", qs[0].Question)
	}
	if qs[0].Question != "How is billing structured?" {
		t.Errorf("question = %q, want the stripped text", qs[0].Question)
	}
}

// TestDeterministicPlanChecksFlagsUnimplementedRequirement closes the
// requirement→task direction of coverage: a live requirement with no
// implementing task must produce a HIGH traceability finding.
func TestDeterministicPlanChecksFlagsUnimplementedRequirement(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{InitiativeID: ini.ID, Code: "REQ-1", Statement: "bill by usage", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}

	findings := eng.deterministicPlanChecks(ctx, ini.ID)
	for _, f := range findings {
		if f.Category == "traceability" && strings.Contains(f.Finding, "no implementing task") {
			return // found
		}
	}
	t.Fatalf("expected an unimplemented-requirement finding, got %+v", findings)
}
