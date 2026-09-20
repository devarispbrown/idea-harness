package projection

import (
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestBuildGuidanceTerminalState covers the nudge on a finished initiative:
// `idea status` printed "● complete" and then "→ Resolve open questions", which
// reads as work still owed on an initiative that verified and ended. A terminal
// state never suggests a next step; anything left open on it is described as
// part of the record instead.
func TestBuildGuidanceTerminalState(t *testing.T) {
	open := func(decisions, questions int) Understanding {
		var u Understanding
		for i := 0; i < decisions; i++ {
			u.Decisions = append(u.Decisions, DecisionView{Status: domain.DecisionOpen})
		}
		for i := 0; i < questions; i++ {
			u.Questions = append(u.Questions, QuestionView{Status: domain.QuestionOpen})
		}
		return u
	}

	cases := []struct {
		name          string
		status        domain.InitiativeStatus
		understanding Understanding
		wantAction    string
		wantWhy       []string // substrings the reason must contain
		wantNotInWhy  []string
	}{
		{
			name:       "complete with nothing open",
			status:     domain.InitiativeComplete,
			wantAction: "Nothing to do — this initiative is complete",
			wantWhy:    []string{"the work is done"},
		},
		{
			name:          "complete with open questions reads as informational",
			status:        domain.InitiativeComplete,
			understanding: open(0, 2),
			wantAction:    "Nothing to do — this initiative is complete",
			wantWhy:       []string{"2 open question(s)", "on the record", "nothing is waiting on you"},
			wantNotInWhy:  []string{"before proceeding"},
		},
		{
			name:          "complete with open decisions and questions names both",
			status:        domain.InitiativeComplete,
			understanding: open(1, 1),
			wantAction:    "Nothing to do — this initiative is complete",
			wantWhy:       []string{"1 decision(s)", "1 open question(s)"},
		},
		{
			name:          "a running initiative still gets the nudge",
			status:        domain.InitiativeRunning,
			understanding: open(0, 1),
			wantAction:    "Resolve open questions",
		},
		{
			name:          "a running initiative with an open decision still gets the nudge",
			status:        domain.InitiativeRunning,
			understanding: open(1, 0),
			wantAction:    "Resolve open decisions",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			nm := BuildGuidance(domain.Initiative{Status: c.status}, c.understanding, nil, false)
			if nm.Action != c.wantAction {
				t.Fatalf("action: got %q, want %q", nm.Action, c.wantAction)
			}
			if c.status == domain.InitiativeComplete && strings.HasPrefix(nm.Action, "Resolve") {
				t.Fatalf("a terminal state must not ask for more work: %q", nm.Action)
			}
			for _, want := range c.wantWhy {
				if !strings.Contains(nm.Why, want) {
					t.Fatalf("why %q should contain %q", nm.Why, want)
				}
			}
			for _, notWant := range c.wantNotInWhy {
				if strings.Contains(nm.Why, notWant) {
					t.Fatalf("why %q should not contain %q", nm.Why, notWant)
				}
			}
		})
	}
}
