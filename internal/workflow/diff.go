package workflow

import (
	"context"

	"github.com/idea-harness/idea-harness/internal/domain"
)

// DiffRequirement is one requirement in the semantic diff.
type DiffRequirement struct {
	Code      string `json:"code"`
	Statement string `json:"statement"`
	Priority  string `json:"priority"`
}

// DiffDecision is one decision in the semantic diff.
type DiffDecision struct {
	Code       string `json:"code"`
	Question   string `json:"question"`
	Resolution string `json:"resolution,omitempty"`
}

// SemanticDiff reports structured requirement/decision changes (added vs
// removed; open vs decided vs superseded) rather than raw markdown (§25).
type SemanticDiff struct {
	Requirements struct {
		Added   []DiffRequirement `json:"added"`
		Removed []DiffRequirement `json:"removed"`
	} `json:"requirements"`
	Decisions struct {
		Open       []DiffDecision `json:"open"`
		Decided    []DiffDecision `json:"decided"`
		Superseded []DiffDecision `json:"superseded"`
	} `json:"decisions"`
}

// SemanticDiff computes the structured diff for an initiative.
func (e *Engine) SemanticDiff(ctx context.Context, initiativeID string) (*SemanticDiff, error) {
	var out SemanticDiff
	reqs, err := e.db.ListRequirements(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	for _, r := range reqs {
		item := DiffRequirement{Code: r.Code, Statement: r.Statement, Priority: string(r.Priority)}
		if r.Status == domain.RequirementRemoved {
			out.Requirements.Removed = append(out.Requirements.Removed, item)
		} else {
			out.Requirements.Added = append(out.Requirements.Added, item)
		}
	}

	decs, err := e.db.ListDecisions(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	for _, d := range decs {
		item := DiffDecision{Code: d.Code, Question: d.Question, Resolution: d.Resolution}
		switch d.Status {
		case domain.DecisionOpen:
			out.Decisions.Open = append(out.Decisions.Open, item)
		case domain.DecisionDecided:
			out.Decisions.Decided = append(out.Decisions.Decided, item)
		case domain.DecisionSuperseded:
			out.Decisions.Superseded = append(out.Decisions.Superseded, item)
		}
	}
	return &out, nil
}
