package review

import (
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

func TestOutcomeChecks(t *testing.T) {
	obj1 := domain.Objective{ID: "o1", Code: "OBJ-1", Statement: "activation up", Status: domain.ObjectiveActive}
	obj2 := domain.Objective{ID: "o2", Code: "OBJ-2", Statement: "support load down", Status: domain.ObjectiveActive}
	done := domain.Objective{ID: "o3", Code: "OBJ-3", Statement: "old", Status: domain.ObjectiveAchieved}
	met := domain.OutcomeMetric{ID: "m1", ObjectiveID: "o1", Code: "MET-1", Name: "activation", Direction: domain.DirectionIncrease, Target: 40}
	r1 := domain.Requirement{ID: "r1", Code: "REQ-1", Statement: "checklist", ObjectiveIDs: []string{"o1"}}
	r2 := domain.Requirement{ID: "r2", Code: "REQ-2", Statement: "dark mode", ObjectiveIDs: []string{}}
	r3 := domain.Requirement{ID: "r3", Code: "REQ-3", Statement: "only serves an achieved objective", ObjectiveIDs: []string{"o3"}}

	t.Run("no objectives is one medium, never a block", func(t *testing.T) {
		got := OutcomeChecks("ini", []domain.Requirement{r1, r2}, nil, nil)
		if len(got) != 1 || got[0].Severity != domain.SeverityMedium || got[0].Category != OutcomeCategory {
			t.Fatalf("got %+v", got)
		}
		if blocked, _ := EvaluateApproval(got); blocked {
			t.Fatal("a PRD with no objectives must not be blocked by the outcome check")
		}
		if got := OutcomeChecks("ini", nil, nil, nil); len(got) != 0 {
			t.Fatalf("no requirements and no objectives: nothing to say, got %+v", got)
		}
	})

	t.Run("requirement serving nothing live, objective with no metric", func(t *testing.T) {
		got := OutcomeChecks("ini", []domain.Requirement{r1, r2, r3}, []domain.Objective{obj1, obj2, done}, []domain.OutcomeMetric{met})
		var texts []string
		for _, f := range got {
			texts = append(texts, f.Finding)
			if f.Severity != domain.SeverityHigh {
				t.Errorf("%q should be high", f.Finding)
			}
		}
		joined := strings.Join(texts, " | ")
		for _, want := range []string{"REQ-2 serves no objective", "REQ-3 serves no objective", "OBJ-2 has no measurable metric"} {
			if !strings.Contains(joined, want) {
				t.Errorf("missing %q in %s", want, joined)
			}
		}
		if strings.Contains(joined, "REQ-1") || strings.Contains(joined, "OBJ-1") || strings.Contains(joined, "OBJ-3") {
			t.Errorf("REQ-1/OBJ-1 are fine and OBJ-3 is not active: %s", joined)
		}
		// Each finding targets the node it is about -- the requirement one
		// is therefore cited, which is what keeps it consistent with the
		// review loop's citation rule.
		for _, f := range got {
			switch {
			case strings.HasPrefix(f.Finding, "REQ-2"):
				if f.TargetType != "requirement" || f.TargetID != "r2" {
					t.Errorf("REQ-2 finding targets %s/%s", f.TargetType, f.TargetID)
				}
			case strings.HasPrefix(f.Finding, "OBJ-2"):
				if f.TargetType != "objective" || f.TargetID != "o2" {
					t.Errorf("OBJ-2 finding targets %s/%s", f.TargetType, f.TargetID)
				}
			}
		}
		if blocked, _ := EvaluateApproval(got); !blocked {
			t.Fatal("these gaps must block approval")
		}
	})

	t.Run("a superseded metric does not count", func(t *testing.T) {
		gone := met
		gone.SupersededBy = "review-cycle-1"
		got := OutcomeChecks("ini", []domain.Requirement{r1}, []domain.Objective{obj1}, []domain.OutcomeMetric{gone})
		if len(got) != 1 || !strings.Contains(got[0].Finding, "OBJ-1 has no measurable metric") {
			t.Fatalf("got %+v", got)
		}
	})
}
