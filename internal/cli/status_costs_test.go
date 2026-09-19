package cli

import (
	"strings"
	"testing"
)

// TestStatusPrintsSpendAndTime pins C3: `idea status` shows the initiative's
// total spend and time when GET .../costs answers.
func TestStatusPrintsSpendAndTime(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/costs", map[string]any{
		"total_usd": 1.2345,
		"by_phase":  []map[string]any{{"phase": "product_definition", "usd": 1.2345, "seconds": 12.0, "calls": 1}},
		"agent":     map[string]any{"runs": 1, "seconds": 30.0, "usd": nil, "note": "agent spend not measured"},
	})

	stdout, _, err := run(t, "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "1.2345") {
		t.Errorf("status did not print the total spend:\n%s", stdout)
	}
	if !strings.Contains(stdout, "spend:") || !strings.Contains(stdout, "time:") {
		t.Errorf("status did not print the spend/time summary line:\n%s", stdout)
	}
}

// TestStatusToleratesMissingCosts: an older daemon with no .../costs route
// must not break `idea status`.
func TestStatusToleratesMissingCosts(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.fail("GET /api/v1/projects/{projectId}/initiatives/{id}/costs", 404, "not found")

	stdout, _, err := run(t, "status")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "spend:") {
		t.Errorf("status printed a spend line despite the costs read failing:\n%s", stdout)
	}
}
