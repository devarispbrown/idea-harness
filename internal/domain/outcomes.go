package domain

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The outcome chain: Objective -> Hypothesis -> Requirement -> ... -> Evidence
// -> product outcome.
//
// Before these types existed, the Product Graph ended at verification: a
// requirement could be proven implemented (tests pass, a human confirmed the
// criteria) and nothing in the graph could say whether building it did what
// it was for. An Objective is the "what it is for"; an OutcomeMetric is how
// anyone would know; a MetricObservation is a measured value with provenance.
// Requirements serve objectives, hypotheses predict metric movement, and an
// observation records which change sets landed since the previous one -- so
// `idea impact requirement REQ-1` reaches the outcome it moved, and "works but
// fails its objective" is a signal rather than a hunch.

// Objective is a product objective an initiative exists to move (OBJ-n).
type Objective struct {
	ID           string          `json:"id"`
	InitiativeID string          `json:"initiative_id"`
	Code         string          `json:"code"`
	Statement    string          `json:"statement"`
	Rationale    string          `json:"rationale,omitempty"`
	Status       ObjectiveStatus `json:"status"`
	Owner        string          `json:"owner,omitempty"`
	// ProducedByRun is the PRD run that authored this objective, empty for one
	// a human added. Only a model-authored objective is ever superseded by a
	// later PRD revision; a human's objective is the human's to change.
	ProducedByRun string `json:"produced_by_run,omitempty"`
	SupersededBy  string `json:"superseded_by,omitempty"`
	// RequirementIDs are the active requirements serving this objective, and
	// MetricIDs its active metrics. Computed at read time from graph edges and
	// the metric table -- never nil on the wire.
	RequirementIDs []string  `json:"requirement_ids"`
	MetricIDs      []string  `json:"metric_ids"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Active reports whether o is a live objective: not superseded by a PRD
// revision, and still being pursued.
func (o Objective) Active() bool {
	return o.SupersededBy == "" && o.Status == ObjectiveActive
}

// MetricSource says how an OutcomeMetric is measured. Command and URL run on
// request (`idea metric measure`), and on the metric's cadence only once a
// person enables the scheduled measurement (automations.metric_measurement).
type MetricSource struct {
	Kind MetricSourceKind `json:"kind"`
	// Command is a shell command run with `sh -c` in the no-network check
	// sandbox, with the workspace mounted read-only at /repo. Its stdout must
	// contain a number; the last number printed is the observation.
	Command string `json:"command,omitempty"`
	// URL is fetched through the SSRF-guarded fetcher (public http(s)
	// addresses only) and must return JSON; Pointer is the RFC 6901 JSON
	// pointer to the number in it ("" means the document itself is a number).
	URL     string `json:"url,omitempty"`
	Pointer string `json:"pointer,omitempty"`
}

// Validate reports why a source cannot be measured, or nil.
func (s MetricSource) Validate() error {
	switch s.Kind {
	case MetricSourceManual:
		return nil
	case MetricSourceCommand:
		if strings.TrimSpace(s.Command) == "" {
			return fmt.Errorf("a command source needs a command")
		}
		return nil
	case MetricSourceURL:
		if !strings.HasPrefix(s.URL, "https://") && !strings.HasPrefix(s.URL, "http://") {
			return fmt.Errorf("a url source needs an http(s) URL")
		}
		if s.Pointer != "" && !strings.HasPrefix(s.Pointer, "/") {
			return fmt.Errorf("pointer %q is not a JSON pointer (it must start with /)", s.Pointer)
		}
		return nil
	}
	return fmt.Errorf("unknown source kind %q (use manual, command or url)", s.Kind)
}

// OutcomeMetric is how an Objective is measured (MET-n).
type OutcomeMetric struct {
	ID           string          `json:"id"`
	InitiativeID string          `json:"initiative_id"`
	ObjectiveID  string          `json:"objective_id"`
	Code         string          `json:"code"`
	Name         string          `json:"name"`
	Definition   string          `json:"definition,omitempty"`
	Unit         string          `json:"unit,omitempty"`
	Direction    MetricDirection `json:"direction"`
	// Baseline is the value before the work, when known. Without one, the
	// first observation stands in for it (see EvaluateMetricDrift).
	Baseline *float64 `json:"baseline,omitempty"`
	Target   float64  `json:"target"`
	// Guardrail is a hard limit on the bad side of the metric: a floor for
	// increase, a ceiling for decrease, and the largest allowed distance from
	// the target for maintain. Crossing it is off_track whatever the baseline.
	Guardrail *float64     `json:"guardrail,omitempty"`
	Source    MetricSource `json:"source"`
	// Cadence is how often the metric is expected to be observed: hourly,
	// daily, weekly, monthly, quarterly, a Go duration ("36h"), or "<n>d" /
	// "<n>w". Empty means no expectation, so the metric is never stale.
	Cadence       string    `json:"cadence,omitempty"`
	ProducedByRun string    `json:"produced_by_run,omitempty"`
	SupersededBy  string    `json:"superseded_by,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	// ObjectiveCode and Status are computed at read time (the objective's
	// OBJ-n, and this metric's drift evaluation). Status is nil only on a
	// write response, before there is anything to evaluate.
	ObjectiveCode string        `json:"objective_code,omitempty"`
	Status        *MetricStatus `json:"status,omitempty"`
}

// Measurable reports whether m can be judged at all: a name, a direction and
// a valid, parseable cadence. Target is a number by construction.
func (m OutcomeMetric) Measurable() bool {
	if strings.TrimSpace(m.Name) == "" || !m.Direction.Valid() {
		return false
	}
	_, err := ParseCadence(m.Cadence)
	return err == nil
}

// MetricObservation is one measured value of a metric.
type MetricObservation struct {
	ID           string           `json:"id"`
	InitiativeID string           `json:"initiative_id"`
	MetricID     string           `json:"metric_id"`
	Value        float64          `json:"value"`
	ObservedAt   time.Time        `json:"observed_at"`
	Source       MetricSourceKind `json:"source"`
	Note         string           `json:"note,omitempty"`
	RecordedBy   string           `json:"recorded_by"`
	// Provenance is what produced the value: the human who typed it, or the
	// digest of the command output / fetched document it was read from.
	Provenance []ProvenanceRef `json:"provenance"`
	// ChangeSetIDs are the change sets that were applied (and are still
	// applied, and changed something) between the previous observation and
	// this one -- the outcome attribution. Each is also a metric
	// --attributed_to--> change_set graph edge.
	ChangeSetIDs []string  `json:"change_set_ids"`
	CreatedAt    time.Time `json:"created_at"`
}

// MetricStatus is the deterministic drift evaluation of one metric.
type MetricStatus struct {
	Drift             MetricDrift `json:"drift"`
	OffTrack          bool        `json:"off_track"`
	Stale             bool        `json:"stale"`
	Regressed         bool        `json:"regressed"`
	GuardrailBreached bool        `json:"guardrail_breached"`
	Achieved          bool        `json:"achieved"`
	Observations      int         `json:"observations"`
	Latest            *float64    `json:"latest,omitempty"`
	LatestAt          *time.Time  `json:"latest_at,omitempty"`
	Previous          *float64    `json:"previous,omitempty"`
	// EffectiveBaseline is what off_track compares against, and
	// BaselineSource where it came from: "declared" (the metric's own
	// baseline) or "first_observation" (none declared). Both empty with no
	// observations and no declared baseline.
	EffectiveBaseline *float64 `json:"effective_baseline,omitempty"`
	BaselineSource    string   `json:"baseline_source,omitempty"`
	// RegressedBy is the change sets attributed to the observation that moved
	// the metric the wrong way (empty unless Regressed).
	RegressedBy []string   `json:"regressed_by"`
	NextDueAt   *time.Time `json:"next_due_at,omitempty"`
	Reasons     []string   `json:"reasons"`
}

// RequirementOutcomeDrift is the "works but fails its objective" signal: a
// requirement that is verified, serving an active objective whose metrics are
// off track or regressed.
type RequirementOutcomeDrift struct {
	RequirementID   string   `json:"requirement_id"`
	RequirementCode string   `json:"requirement_code"`
	ObjectiveID     string   `json:"objective_id"`
	ObjectiveCode   string   `json:"objective_code"`
	MetricCodes     []string `json:"metric_codes"`
	Signal          string   `json:"signal"`
	Detail          string   `json:"detail"`
}

// SignalWorksButFailsObjective is RequirementOutcomeDrift.Signal's one value.
const SignalWorksButFailsObjective = "works_but_fails_objective"

// cadenceWords are the named cadences ParseCadence accepts. A month is 30
// days and a quarter 90: staleness is a nudge, not an accounting period.
var cadenceWords = map[string]time.Duration{
	"hourly":    time.Hour,
	"daily":     24 * time.Hour,
	"weekly":    7 * 24 * time.Hour,
	"biweekly":  14 * 24 * time.Hour,
	"monthly":   30 * 24 * time.Hour,
	"quarterly": 90 * 24 * time.Hour,
}

// ParseCadence turns a cadence into a duration. "" is 0 (no expectation).
func ParseCadence(s string) (time.Duration, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0, nil
	}
	if d, ok := cadenceWords[s]; ok {
		return d, nil
	}
	for suffix, unit := range map[string]time.Duration{"d": 24 * time.Hour, "w": 7 * 24 * time.Hour} {
		if n, ok := strings.CutSuffix(s, suffix); ok {
			if v, err := strconv.Atoi(n); err == nil && v > 0 {
				return time.Duration(v) * unit, nil
			}
		}
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d, nil
	}
	return 0, fmt.Errorf("cadence %q is not hourly, daily, weekly, biweekly, monthly, quarterly, <n>d, <n>w or a duration like 36h", s)
}

// worse reports whether value a is worse than reference value ref for a
// metric moving in direction dir toward target: lower for increase, higher
// for decrease, and farther from the target for maintain.
func worse(dir MetricDirection, target, a, ref float64) bool {
	switch dir {
	case DirectionIncrease:
		return a < ref
	case DirectionDecrease:
		return a > ref
	case DirectionMaintain:
		return math.Abs(a-target) > math.Abs(ref-target)
	}
	return false
}

// EvaluateMetricDrift is the deterministic drift rule for one metric. obs may
// be in any order; now is the evaluation time (staleness is relative to it).
//
// The rules, exactly:
//
//   - no_data: the metric has never been observed. It is additionally stale
//     when it has a cadence and more than one cadence has passed since the
//     metric was defined.
//   - off_track: the latest observation is worse than the effective baseline
//     (lower for increase, higher for decrease, farther from the target for
//     maintain) -- i.e. on the wrong side of the baseline->target trajectory
//     -- or it breaches the guardrail. The effective baseline is the declared
//     one, or, when none was declared, the first observation; so a metric
//     with no baseline and a single observation cannot be off track except by
//     its guardrail.
//   - stale: a cadence is set and more than one cadence has passed since the
//     latest observation (strictly more: exactly one cadence is still due,
//     not late).
//   - regressed: the latest observation is worse than the one before it, by
//     the same direction rule, AND at least one change set landed between
//     the two (MetricObservation.ChangeSetIDs). A drop with no change set in
//     between is noise or the world, not this work; it can still be
//     off_track.
//
// Drift is the most urgent that holds: regressed > off_track > stale >
// no_data > on_track. Each signal is also reported on its own.
func EvaluateMetricDrift(m OutcomeMetric, obs []MetricObservation, now time.Time) MetricStatus {
	st := MetricStatus{RegressedBy: []string{}, Reasons: []string{}}
	cadence, cadenceErr := ParseCadence(m.Cadence)
	if cadenceErr != nil {
		st.Reasons = append(st.Reasons, cadenceErr.Error()+"; staleness is not checked")
		cadence = 0
	}

	sorted := make([]MetricObservation, len(obs))
	copy(sorted, obs)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].ObservedAt.Equal(sorted[j].ObservedAt) {
			return sorted[i].CreatedAt.Before(sorted[j].CreatedAt)
		}
		return sorted[i].ObservedAt.Before(sorted[j].ObservedAt)
	})
	st.Observations = len(sorted)

	if m.Baseline != nil {
		b := *m.Baseline
		st.EffectiveBaseline, st.BaselineSource = &b, "declared"
	}

	if len(sorted) == 0 {
		st.Drift = DriftNoData
		st.Reasons = append(st.Reasons, "never observed")
		if cadence > 0 {
			due := m.CreatedAt.Add(cadence)
			st.NextDueAt = &due
			if now.Sub(m.CreatedAt) > cadence {
				st.Stale = true
				st.Drift = DriftStale
				st.Reasons = append(st.Reasons, fmt.Sprintf("no observation in the %s since it was defined", m.Cadence))
			}
		}
		return st
	}

	latest := sorted[len(sorted)-1]
	lv, lat := latest.Value, latest.ObservedAt
	st.Latest, st.LatestAt = &lv, &lat
	if st.EffectiveBaseline == nil {
		b := sorted[0].Value
		st.EffectiveBaseline, st.BaselineSource = &b, "first_observation"
	}
	base := *st.EffectiveBaseline

	if worse(m.Direction, m.Target, lv, base) {
		st.OffTrack = true
		st.Reasons = append(st.Reasons, fmt.Sprintf("latest %s is worse than the %s baseline %s (direction %s, target %s)",
			fmtNum(lv), baselineWord(st.BaselineSource), fmtNum(base), m.Direction, fmtNum(m.Target)))
	}
	if m.Guardrail != nil {
		g := *m.Guardrail
		breached := false
		switch m.Direction {
		case DirectionIncrease:
			breached = lv < g
		case DirectionDecrease:
			breached = lv > g
		case DirectionMaintain:
			breached = math.Abs(lv-m.Target) > g
		}
		if breached {
			st.GuardrailBreached, st.OffTrack = true, true
			st.Reasons = append(st.Reasons, fmt.Sprintf("latest %s breaches the guardrail %s", fmtNum(lv), fmtNum(g)))
		}
	}
	switch m.Direction {
	case DirectionIncrease:
		st.Achieved = lv >= m.Target
	case DirectionDecrease:
		st.Achieved = lv <= m.Target
	case DirectionMaintain:
		tol := 0.0
		if m.Guardrail != nil {
			tol = *m.Guardrail
		}
		st.Achieved = math.Abs(lv-m.Target) <= tol
	}

	if cadence > 0 {
		due := lat.Add(cadence)
		st.NextDueAt = &due
		if now.Sub(lat) > cadence {
			st.Stale = true
			st.Reasons = append(st.Reasons, fmt.Sprintf("last observed %s ago, cadence is %s", now.Sub(lat).Round(time.Minute), m.Cadence))
		}
	}

	if len(sorted) >= 2 {
		prev := sorted[len(sorted)-2]
		pv := prev.Value
		st.Previous = &pv
		if worse(m.Direction, m.Target, lv, pv) && len(latest.ChangeSetIDs) > 0 {
			st.Regressed = true
			st.RegressedBy = append(st.RegressedBy, latest.ChangeSetIDs...)
			st.Reasons = append(st.Reasons, fmt.Sprintf("moved from %s to %s against its direction (%s) after %d change set(s) landed",
				fmtNum(pv), fmtNum(lv), m.Direction, len(latest.ChangeSetIDs)))
		}
	}

	switch {
	case st.Regressed:
		st.Drift = DriftRegressed
	case st.OffTrack:
		st.Drift = DriftOffTrack
	case st.Stale:
		st.Drift = DriftStale
	default:
		st.Drift = DriftOnTrack
	}
	return st
}

func baselineWord(source string) string {
	if source == "first_observation" {
		return "first-observation"
	}
	return source
}

// fmtNum renders a metric value without a float's noise: 12 not 12.000000,
// 0.25 not 2.5e-01.
func fmtNum(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// FormatMetricValue is fmtNum for callers outside the package (CLI, guidance).
func FormatMetricValue(v float64) string { return fmtNum(v) }

// OutcomeChainGaps is where the outcome end of the Product Graph is missing a
// link or failing: the one definition the outcome report (GET …/outcomes, the
// Health page), `idea gaps` and the PRD review's outcome checks all read, so
// "which requirement serves nothing" or "which objective is unmeasured" has
// exactly one answer. AnalyzeOutcomeChain computes it.
type OutcomeChainGaps struct {
	// ObjectivesMissing: there is at least one live requirement and no active
	// objective at all -- the whole chain is absent, reported once rather than
	// once per requirement.
	ObjectivesMissing bool
	// ObjectivesWithoutMetrics are the active objectives with no live,
	// measurable metric (OutcomeMetric.Measurable).
	ObjectivesWithoutMetrics []Objective
	// ObjectivesWithoutRequirements are the active objectives no live
	// requirement serves.
	ObjectivesWithoutRequirements []Objective
	// RequirementsWithoutObjectives are the live requirements that serve no
	// active objective. Empty when ObjectivesMissing (or when there are no
	// active objectives at all).
	RequirementsWithoutObjectives []Requirement
	// DriftingMetrics are the live metrics whose drift is off_track,
	// regressed or stale (MetricDrift.Drifting). no_data is not drifting.
	DriftingMetrics []OutcomeMetric
	// RequirementDrift is the "works but fails its objective" signal.
	RequirementDrift []RequirementOutcomeDrift
}

// AnalyzeOutcomeChain is the deterministic outcome-chain gap rule. Inputs may
// include dead nodes; it applies the liveness filters itself:
//
//   - a live requirement is not removed and not superseded;
//   - an active objective is Objective.Active (not superseded, status active);
//   - a live metric is not superseded, and metrics carry their drift Status
//     (store.ListMetrics evaluates it); one with no Status is not drifting.
//
// "Serves" is a requirement's ObjectiveIDs (its serves edges) naming an
// active objective; serving only an achieved or abandoned objective is
// serving none -- the same rule the PRD review applies.
//
// Output order follows input order (requirements and objectives as the store
// lists them; RequirementDrift by requirement, then by its objectives).
func AnalyzeOutcomeChain(reqs []Requirement, objectives []Objective, metrics []OutcomeMetric) OutcomeChainGaps {
	out := OutcomeChainGaps{
		ObjectivesWithoutMetrics: []Objective{}, ObjectivesWithoutRequirements: []Objective{},
		RequirementsWithoutObjectives: []Requirement{}, DriftingMetrics: []OutcomeMetric{},
		RequirementDrift: []RequirementOutcomeDrift{},
	}
	active := map[string]Objective{}
	for _, o := range objectives {
		if o.Active() {
			active[o.ID] = o
		}
	}
	measured := map[string]bool{}
	failing := map[string][]string{} // objective id -> off_track/regressed metric codes
	for _, m := range metrics {
		if m.SupersededBy != "" {
			continue
		}
		if m.Measurable() {
			measured[m.ObjectiveID] = true
		}
		if m.Status == nil {
			continue
		}
		if m.Status.Drift.Drifting() {
			out.DriftingMetrics = append(out.DriftingMetrics, m)
		}
		if m.Status.OffTrack || m.Status.Regressed {
			failing[m.ObjectiveID] = append(failing[m.ObjectiveID], m.Code)
		}
	}
	served := map[string]bool{}
	liveReqs := 0
	for _, r := range reqs {
		if r.Status == RequirementRemoved || r.SupersededBy != "" {
			continue
		}
		liveReqs++
		serves := false
		for _, oid := range r.ObjectiveIDs {
			if _, ok := active[oid]; ok {
				serves, served[oid] = true, true
			}
		}
		if !serves && len(active) > 0 {
			out.RequirementsWithoutObjectives = append(out.RequirementsWithoutObjectives, r)
		}
		if r.Status != RequirementVerified {
			continue
		}
		for _, oid := range r.ObjectiveIDs {
			o, ok := active[oid]
			codes := append([]string(nil), failing[oid]...)
			if !ok || len(codes) == 0 {
				continue
			}
			sort.Strings(codes)
			verb := "is"
			if len(codes) > 1 {
				verb = "are"
			}
			out.RequirementDrift = append(out.RequirementDrift, RequirementOutcomeDrift{
				RequirementID: r.ID, RequirementCode: r.Code, ObjectiveID: o.ID, ObjectiveCode: o.Code,
				MetricCodes: codes, Signal: SignalWorksButFailsObjective,
				Detail: fmt.Sprintf("%s is verified, but %s's %s %s off track", r.Code, o.Code, strings.Join(codes, ", "), verb),
			})
		}
	}
	out.ObjectivesMissing = len(active) == 0 && liveReqs > 0
	for _, o := range objectives {
		if !o.Active() {
			continue
		}
		if !measured[o.ID] {
			out.ObjectivesWithoutMetrics = append(out.ObjectivesWithoutMetrics, o)
		}
		if !served[o.ID] {
			out.ObjectivesWithoutRequirements = append(out.ObjectivesWithoutRequirements, o)
		}
	}
	return out
}
