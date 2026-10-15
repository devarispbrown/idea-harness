package projection

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// OutcomeReport is the GET .../outcomes body: the outcome end of the Product
// Graph for one initiative -- live objectives, live metrics with their drift,
// the "works but fails its objective" signals, and the chain's gaps. It is the
// one shape `idea status`, `idea next`, the Health page and the MCP outcomes
// tool all read, so they cannot disagree about what is drifting.
type OutcomeReport struct {
	Objectives       []domain.Objective               `json:"objectives"`
	Metrics          []domain.OutcomeMetric           `json:"metrics"`
	RequirementDrift []domain.RequirementOutcomeDrift `json:"requirement_drift"`
	Gaps             OutcomeGaps                      `json:"gaps"`
	Summary          OutcomeSummary                   `json:"summary"`
}

// OutcomeGaps lists the codes of nodes the outcome chain is missing a link
// for: domain.AnalyzeOutcomeChain's answer, rendered for display.
type OutcomeGaps struct {
	ObjectivesWithoutMetrics      []string `json:"objectives_without_metrics"`
	ObjectivesWithoutRequirements []string `json:"objectives_without_requirements"`
	RequirementsWithoutObjectives []string `json:"requirements_without_objectives"`
}

// OutcomeSummary counts metrics by drift status.
type OutcomeSummary struct {
	Objectives int `json:"objectives"`
	Metrics    int `json:"metrics"`
	OnTrack    int `json:"on_track"`
	OffTrack   int `json:"off_track"`
	Regressed  int `json:"regressed"`
	Stale      int `json:"stale"`
	NoData     int `json:"no_data"`
	// Drifting is off_track + regressed + stale (MetricDrift.Drifting).
	Drifting int `json:"drifting"`
	// WorksButFails is len(RequirementDrift).
	WorksButFails int `json:"works_but_fails"`
}

// BuildOutcomes assembles the OutcomeReport at now. Its gaps and
// requirement drift are store.OutcomeChain (domain.AnalyzeOutcomeChain) --
// the same answer `idea gaps` turns into orphan_requirement,
// unmeasured_objective, unserved_objective and ineffective_requirement gaps.
func BuildOutcomes(ctx context.Context, db *store.DB, initiativeID string, now time.Time) (OutcomeReport, error) {
	rep := OutcomeReport{
		Objectives: []domain.Objective{}, Metrics: []domain.OutcomeMetric{}, RequirementDrift: []domain.RequirementOutcomeDrift{},
		Gaps: OutcomeGaps{ObjectivesWithoutMetrics: []string{}, ObjectivesWithoutRequirements: []string{}, RequirementsWithoutObjectives: []string{}},
	}
	objs, err := db.ListObjectives(ctx, initiativeID)
	if err != nil {
		return rep, err
	}
	for _, o := range objs {
		if o.SupersededBy == "" {
			rep.Objectives = append(rep.Objectives, o)
		}
	}
	ms, err := db.ListMetrics(ctx, initiativeID, now)
	if err != nil {
		return rep, err
	}
	rep.Metrics = ms
	for _, m := range ms {
		switch m.Status.Drift {
		case domain.DriftOnTrack:
			rep.Summary.OnTrack++
		case domain.DriftOffTrack:
			rep.Summary.OffTrack++
		case domain.DriftRegressed:
			rep.Summary.Regressed++
		case domain.DriftStale:
			rep.Summary.Stale++
		case domain.DriftNoData:
			rep.Summary.NoData++
		}
	}
	reqs, err := db.ListRequirements(ctx, initiativeID)
	if err != nil {
		return rep, err
	}
	chain := domain.AnalyzeOutcomeChain(reqs, objs, ms)
	rep.Gaps = outcomeGapsOf(chain)
	rep.RequirementDrift = chain.RequirementDrift
	rep.Summary.Drifting = len(chain.DriftingMetrics)
	rep.Summary.Objectives, rep.Summary.Metrics, rep.Summary.WorksButFails = len(rep.Objectives), len(ms), len(chain.RequirementDrift)
	return rep, nil
}

// outcomeGapsOf renders the chain's gaps as the report's code lists.
// Requirements serving nothing appear only once the initiative has an active
// objective; before that the whole chain is missing, which the PRD review
// (and `idea gaps`, as orphan_requirement / objectives_missing) reports once.
func outcomeGapsOf(c domain.OutcomeChainGaps) OutcomeGaps {
	g := OutcomeGaps{ObjectivesWithoutMetrics: []string{}, ObjectivesWithoutRequirements: []string{}, RequirementsWithoutObjectives: []string{}}
	for _, o := range c.ObjectivesWithoutMetrics {
		g.ObjectivesWithoutMetrics = append(g.ObjectivesWithoutMetrics, o.Code)
	}
	for _, o := range c.ObjectivesWithoutRequirements {
		g.ObjectivesWithoutRequirements = append(g.ObjectivesWithoutRequirements, o.Code)
	}
	for _, r := range c.RequirementsWithoutObjectives {
		g.RequirementsWithoutObjectives = append(g.RequirementsWithoutObjectives, r.Code)
	}
	return g
}

// OutcomeGuidance is the sentence `idea next` / the guidance projection
// appends about outcomes, and the codes it is about. "" when nothing needs
// attention.
func OutcomeGuidance(rep OutcomeReport) (string, []string) {
	var parts []string
	var affected []string
	if len(rep.RequirementDrift) > 0 {
		var codes []string
		for _, d := range rep.RequirementDrift {
			codes = append(codes, d.RequirementCode)
			affected = append(affected, d.RequirementCode)
		}
		parts = append(parts, fmt.Sprintf("%s %s verified but %s objective's metrics are off track (works, but fails its objective).",
			strings.Join(dedup(codes), ", "), isAre(len(dedup(codes))), theirIts(len(dedup(codes)))))
	}
	var regressed, off, stale []string
	for _, m := range rep.Metrics {
		switch m.Status.Drift {
		case domain.DriftRegressed:
			regressed = append(regressed, m.Code)
		case domain.DriftOffTrack:
			off = append(off, m.Code)
		case domain.DriftStale:
			stale = append(stale, m.Code)
		}
	}
	if len(regressed) > 0 {
		parts = append(parts, fmt.Sprintf("%s regressed after a change set landed -- `idea trace metric %s` shows which.", strings.Join(regressed, ", "), regressed[0]))
		affected = append(affected, regressed...)
	}
	if len(off) > 0 {
		parts = append(parts, fmt.Sprintf("%s %s off track.", strings.Join(off, ", "), isAre(len(off))))
		affected = append(affected, off...)
	}
	if len(stale) > 0 {
		parts = append(parts, fmt.Sprintf("%s %s overdue for a measurement -- `idea metric measure` or `idea metric record`.", strings.Join(stale, ", "), isAre(len(stale))))
		affected = append(affected, stale...)
	}
	return strings.Join(parts, " "), affected
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

func theirIts(n int) string {
	if n == 1 {
		return "its"
	}
	return "their"
}

func dedup(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
