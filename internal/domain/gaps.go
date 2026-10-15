package domain

// GapNode is one Product Graph node a gap is about. Code is the human handle
// (REQ-3, TASK-2, CHG-4, DEC-1, an artifact kind, an acceptance criterion as
// REQ-3/AC-2, an assertion's short id) -- what a person types; ID is the
// stable id a client links with.
type GapNode struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Code  string `json:"code"`
	Label string `json:"label,omitempty"`
}

// Gap is one hole `idea gaps` found in the Product Graph: what kind, how bad,
// which nodes, why in one line, and the command that closes it.
//
// Nodes[0] is always the node the gap is *about* (the requirement that is not
// implemented, the decision whose basis moved); any further nodes are the
// evidence for it (the stale dependents, the criteria with no evidence).
// Dependents counts the live nodes that rest on Nodes[0] -- the ranking key
// within a severity, so the assumption half the plan depends on sorts above
// one nothing cites.
type Gap struct {
	Kind       GapKind   `json:"kind"`
	State      GapState  `json:"state"`
	Severity   Severity  `json:"severity"`
	Nodes      []GapNode `json:"nodes"`
	Why        string    `json:"why"`
	Next       string    `json:"next"`
	Dependents int       `json:"dependents"`
	// InitiativeID is set on every gap, so a project-wide report stays
	// attributable after a client flattens it.
	InitiativeID string `json:"initiative_id"`
}

// GapSummary is the at-a-glance count the guidance projection and the
// Health page show: totals by severity and by kind. Every known kind and
// severity appears, zero included, so a client never has to tell "absent"
// from "zero".
type GapSummary struct {
	Total      int            `json:"total"`
	BySeverity map[string]int `json:"by_severity"`
	ByKind     map[string]int `json:"by_kind"`
}

// GapReport is the GET …/gaps response body.
type GapReport struct {
	InitiativeID string     `json:"initiative_id,omitempty"`
	Gaps         []Gap      `json:"gaps"`
	Summary      GapSummary `json:"summary"`
}

// SeverityRank orders severities most severe first (critical = 0). An
// unknown severity ranks last.
func SeverityRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 0
	case SeverityHigh:
		return 1
	case SeverityMedium:
		return 2
	case SeverityLow:
		return 3
	case SeverityObservation:
		return 4
	}
	return 5
}

// AtLeast reports whether s is at least as severe as min.
func (s Severity) AtLeast(min Severity) bool {
	return SeverityRank(s) <= SeverityRank(min)
}

// Summarize counts gaps by severity and kind.
func Summarize(gaps []Gap) GapSummary {
	sum := GapSummary{Total: len(gaps), BySeverity: map[string]int{}, ByKind: map[string]int{}}
	for _, s := range []Severity{SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow, SeverityObservation} {
		sum.BySeverity[string(s)] = 0
	}
	for _, k := range GapKinds {
		sum.ByKind[string(k)] = 0
	}
	for _, g := range gaps {
		sum.BySeverity[string(g.Severity)]++
		sum.ByKind[string(g.Kind)]++
	}
	return sum
}
