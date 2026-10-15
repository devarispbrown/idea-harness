package cli

import (
	"strings"
	"testing"
)

func TestParseUSD(t *testing.T) {
	for in, want := range map[string]float64{"5": 5, "$2.50": 2.5, " 0 ": 0} {
		if got, err := parseUSD(in); err != nil || got != want {
			t.Errorf("parseUSD(%q) = %v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "five", "-1", "NaN", "Inf"} {
		if _, err := parseUSD(bad); err == nil {
			t.Errorf("parseUSD(%q) accepted", bad)
		}
	}
}

func TestBudgetLineSaysWhereTheCapComesFrom(t *testing.T) {
	five := 5.0
	if got := budgetLine(cliBudget{SpentUSD: 1.2}); !strings.Contains(got, "no cap") {
		t.Errorf("no cap: %q", got)
	}
	if got := budgetLine(cliBudget{SpentUSD: 1.2, CapUSD: &five, Source: "project"}); !strings.Contains(got, "of $5.00 (project default)") {
		t.Errorf("project: %q", got)
	}
	if got := budgetLine(cliBudget{SpentUSD: 5, CapUSD: &five, Source: "initiative", Exceeded: true}); !strings.Contains(got, "its own cap") || !strings.Contains(got, "at cap") {
		t.Errorf("initiative at cap: %q", got)
	}
}
