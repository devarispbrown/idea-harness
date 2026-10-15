package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// Objectives & outcomes in the PRD stage.
//
// The product manager returns objectives (each with metrics) alongside the
// requirements, and each requirement names the objectives it serves. This
// file turns that response into Objective/OutcomeMetric rows and serves
// edges, keeping codes stable across revisions the same way requirements do:
// a response objective whose key is an existing OBJ code -- or whose statement
// matches a live objective's -- continues it rather than minting a new one.
//
// Two ownership rules keep a model revision from trampling a human:
//
//   - a human-added objective or metric (no producing run) is never rewritten
//     or superseded by a revision; the model may still cite it, link
//     requirements to it, and add metrics under it;
//   - a revision that names no objectives for a requirement leaves that
//     requirement's existing links alone, so silence never unlinks.

// prdObjective is one parsed objectives[] entry.
type prdObjective struct {
	key, statement, rationale string
	metrics                   []prdMetric
}

type prdMetric struct {
	key, name, definition, unit, cadence, hypothesis string
	direction                                        domain.MetricDirection
	baseline, guardrail                              *float64
	target                                           float64
}

// num reads a JSON number the structured-output decoder produced. It decodes
// with UseNumber, so a number arrives as json.Number, not float64; a string
// holding a number is accepted too, since a model writes "40" as often as 40.
func num(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case float64:
		return n, true
	case int:
		return float64(n), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	}
	return 0, false
}

func numPtr(v any) *float64 {
	if f, ok := num(v); ok {
		return &f
	}
	return nil
}

func (e *Engine) parsePRDObjectives(obj map[string]any) []prdObjective {
	var out []prdObjective
	for _, o := range objSlice(obj["objectives"]) {
		po := prdObjective{
			key:       strings.TrimSpace(str(o["key"])),
			statement: strings.TrimSpace(e.redact(str(o["statement"]))),
			rationale: strings.TrimSpace(e.redact(str(o["rationale"]))),
		}
		if po.statement == "" {
			continue
		}
		for _, m := range objSlice(o["metrics"]) {
			target, ok := num(m["target"])
			if !ok {
				continue
			}
			pm := prdMetric{
				key:        strings.TrimSpace(str(m["key"])),
				name:       strings.TrimSpace(e.redact(str(m["name"]))),
				definition: strings.TrimSpace(e.redact(str(m["definition"]))),
				unit:       strings.TrimSpace(str(m["unit"])),
				cadence:    strings.TrimSpace(str(m["cadence"])),
				hypothesis: strings.TrimSpace(e.redact(str(m["hypothesis"]))),
				direction:  domain.MetricDirection(strings.ToLower(strings.TrimSpace(str(m["direction"])))),
				baseline:   numPtr(m["baseline"]),
				guardrail:  numPtr(m["guardrail"]),
				target:     target,
			}
			if pm.name == "" || !pm.direction.Valid() {
				continue
			}
			// A cadence the model wrote that ParseCadence cannot read is
			// dropped rather than failing the PRD: the metric is still
			// measurable, it just has no staleness expectation until a human
			// sets one.
			if _, err := domain.ParseCadence(pm.cadence); err != nil {
				pm.cadence = ""
			}
			po.metrics = append(po.metrics, pm)
		}
		out = append(out, po)
	}
	return out
}

// recordPRDObjectives persists this PRD cycle's objectives and metrics and
// links the cycle's requirements to them. objectiveRefs maps a requirement
// id to the objective keys/codes the response listed for it.
func (e *Engine) recordPRDObjectives(ctx context.Context, initiativeID, runID string, obj map[string]any, reviseReason string, objectiveRefs map[string][]string) error {
	parsed := e.parsePRDObjectives(obj)

	existing, err := e.db.ListObjectives(ctx, initiativeID)
	if err != nil {
		return err
	}
	live := make([]domain.Objective, 0, len(existing))
	for _, o := range existing {
		if o.SupersededBy == "" {
			live = append(live, o)
		}
	}
	metrics, err := e.db.ListMetrics(ctx, initiativeID, time.Now())
	if err != nil {
		return err
	}

	// keyToID resolves every name a requirement may cite an objective by:
	// this response's own keys, and every live objective's code.
	keyToID := map[string]string{}
	for _, o := range live {
		keyToID[strings.ToUpper(o.Code)] = o.ID
	}
	used := map[string]bool{}
	keep := make([]string, 0, len(parsed))

	for _, po := range parsed {
		var match *domain.Objective
		if k := strings.ToUpper(po.key); k != "" {
			for i := range live {
				if strings.EqualFold(live[i].Code, k) && !used[live[i].ID] {
					match = &live[i]
					break
				}
			}
		}
		if match == nil {
			norm := normalizeJudgment(po.statement)
			for i := range live {
				if !used[live[i].ID] && normalizeJudgment(live[i].Statement) == norm {
					match = &live[i]
					break
				}
			}
		}
		var objID string
		if match != nil {
			objID = match.ID
			used[objID] = true
			if match.ProducedByRun != "" && (match.Statement != po.statement || match.Rationale != po.rationale) {
				upd := *match
				upd.Statement, upd.Rationale, upd.ProducedByRun = po.statement, po.rationale, runID
				if err := e.db.UpdateObjective(ctx, &upd, store.ActorOrchestrator); err != nil {
					return err
				}
			}
		} else {
			o := &domain.Objective{InitiativeID: initiativeID, Statement: po.statement, Rationale: po.rationale, ProducedByRun: runID}
			if err := e.db.CreateObjective(ctx, o, store.ActorOrchestrator); err != nil {
				return err
			}
			objID = o.ID
			used[objID] = true
			keyToID[strings.ToUpper(o.Code)] = objID
		}
		keep = append(keep, objID)
		if po.key != "" {
			keyToID[strings.ToUpper(po.key)] = objID
		}
		if err := e.recordPRDMetrics(ctx, initiativeID, runID, objID, po.metrics, metrics); err != nil {
			return err
		}
	}

	// A response that named objectives replaces the model's earlier set; one
	// that named none (an older prompt, a model that ignored the field) is
	// not read as "drop every objective".
	if len(parsed) > 0 {
		reason := reviseReason
		if reason == "" {
			reason = "prd:" + runID
		}
		if err := e.db.SupersedeModelObjectivesExcept(ctx, initiativeID, reason, keep); err != nil {
			return err
		}
	}

	reqIDs := make([]string, 0, len(objectiveRefs))
	for id := range objectiveRefs {
		reqIDs = append(reqIDs, id)
	}
	sort.Strings(reqIDs)
	for _, reqID := range reqIDs {
		var ids []string
		seen := map[string]bool{}
		for _, ref := range objectiveRefs[reqID] {
			id, ok := keyToID[strings.ToUpper(strings.TrimSpace(ref))]
			if !ok || seen[id] {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
		}
		if len(ids) == 0 {
			if len(objectiveRefs[reqID]) > 0 {
				e.log.Debug("requirement cites no known objective; existing links kept", "initiative", initiativeID, "requirement", reqID, "refs", objectiveRefs[reqID])
			}
			continue
		}
		if err := e.db.ReplaceRequirementObjectives(ctx, initiativeID, reqID, ids); err != nil {
			return err
		}
	}
	return nil
}

// recordPRDMetrics creates or updates one objective's metrics from the
// response. A model-authored metric keeps whatever measurement source a
// human gave it since: the PRD describes what to measure, not how this
// project's numbers are collected.
func (e *Engine) recordPRDMetrics(ctx context.Context, initiativeID, runID, objectiveID string, pms []prdMetric, existing []domain.OutcomeMetric) error {
	usedMetric := map[string]bool{}
	for _, pm := range pms {
		var match *domain.OutcomeMetric
		for i := range existing {
			m := &existing[i]
			if usedMetric[m.ID] {
				continue
			}
			if (pm.key != "" && strings.EqualFold(m.Code, pm.key)) ||
				(m.ObjectiveID == objectiveID && normalizeJudgment(m.Name) == normalizeJudgment(pm.name)) {
				match = m
				break
			}
		}
		var metricID string
		if match != nil {
			metricID = match.ID
			usedMetric[metricID] = true
			if match.ProducedByRun != "" {
				upd := *match
				upd.ObjectiveID, upd.Name, upd.Definition, upd.Unit = objectiveID, pm.name, pm.definition, pm.unit
				upd.Direction, upd.Baseline, upd.Target, upd.Guardrail, upd.Cadence = pm.direction, pm.baseline, pm.target, pm.guardrail, pm.cadence
				upd.ProducedByRun, upd.Status = runID, nil
				if err := e.db.UpdateMetric(ctx, &upd, store.ActorOrchestrator); err != nil {
					e.log.Warn("PRD metric revision rejected; previous definition kept", "initiative", initiativeID, "metric", match.Code, "error", err)
				}
			}
		} else {
			m := &domain.OutcomeMetric{
				InitiativeID: initiativeID, ObjectiveID: objectiveID, Name: pm.name, Definition: pm.definition, Unit: pm.unit,
				Direction: pm.direction, Baseline: pm.baseline, Target: pm.target, Guardrail: pm.guardrail, Cadence: pm.cadence,
				Source: domain.MetricSource{Kind: domain.MetricSourceManual}, ProducedByRun: runID,
			}
			if err := e.db.CreateMetric(ctx, m, store.ActorOrchestrator); err != nil {
				// One malformed metric must not fail a PRD the model already
				// paid for; the objective is then reported metric-less by
				// the deterministic outcome check, which is the right signal.
				e.log.Warn("PRD metric rejected", "initiative", initiativeID, "metric", pm.name, "error", err)
				continue
			}
			metricID = m.ID
		}
		if pm.hypothesis != "" {
			if err := e.recordHypothesisOnce(ctx, initiativeID, metricID, pm.hypothesis, runID); err != nil {
				return err
			}
		}
	}
	return nil
}

// recordHypothesisOnce links a "we believe X will move this metric"
// hypothesis to a metric unless an equivalent one already predicts it -- a
// revise cycle restating the same belief must not stack duplicates.
func (e *Engine) recordHypothesisOnce(ctx context.Context, initiativeID, metricID, statement, runID string) error {
	prior, err := e.db.MetricHypotheses(ctx, initiativeID, metricID)
	if err != nil {
		return err
	}
	norm := normalizeJudgment(statement)
	for _, a := range prior {
		an := normalizeJudgment(a.Statement)
		if an == norm || similarJudgment(an, norm) {
			return nil
		}
	}
	_, err = e.db.CreateMetricHypothesis(ctx, initiativeID, metricID, statement, "product_definition", runID)
	return err
}

// liveOutcomes returns the live objectives (not superseded; any status) and
// live metrics of an initiative.
func (e *Engine) liveOutcomes(ctx context.Context, initiativeID string) ([]domain.Objective, []domain.OutcomeMetric) {
	objs, _ := e.db.ListObjectives(ctx, initiativeID)
	live := make([]domain.Objective, 0, len(objs))
	for _, o := range objs {
		if o.SupersededBy == "" {
			live = append(live, o)
		}
	}
	ms, _ := e.db.ListMetrics(ctx, initiativeID, time.Now())
	return live, ms
}

// outcomeCitables returns what a review finding may cite beyond requirements:
// each live objective's and metric's normalized code with the node it names,
// and the reverse id -> code map citationOf uses on persisted findings.
func (e *Engine) outcomeCitables(ctx context.Context, initiativeID string) (map[string]NodeRef, map[string]string) {
	objs, metrics := e.liveOutcomes(ctx, initiativeID)
	refs := make(map[string]NodeRef, len(objs)+len(metrics))
	codes := make(map[string]string, len(objs)+len(metrics))
	for _, o := range objs {
		refs[strings.ToUpper(o.Code)] = NodeRef{Type: "objective", ID: o.ID}
		codes[o.ID] = o.Code
	}
	for _, m := range metrics {
		refs[strings.ToUpper(m.Code)] = NodeRef{Type: "metric", ID: m.ID}
		codes[m.ID] = m.Code
	}
	return refs, codes
}

// renderObjectives is the objectives context the PRD author and the PRD
// reviewer see: each objective with its metrics and the requirements serving
// it, followed by the requirements that serve none.
func renderObjectives(objs []domain.Objective, metrics []domain.OutcomeMetric, reqs []domain.Requirement) string {
	if len(objs) == 0 {
		return "(none)"
	}
	codeByReq := map[string]string{}
	for _, r := range reqs {
		codeByReq[r.ID] = r.Code
	}
	var b strings.Builder
	for _, o := range objs {
		fmt.Fprintf(&b, "- %s: %s", o.Code, o.Statement)
		if o.Status != domain.ObjectiveActive {
			fmt.Fprintf(&b, " (%s)", o.Status)
		}
		b.WriteString("\n")
		if o.Rationale != "" {
			fmt.Fprintf(&b, "  rationale: %s\n", o.Rationale)
		}
		n := 0
		for _, m := range metrics {
			if m.ObjectiveID != o.ID {
				continue
			}
			n++
			fmt.Fprintf(&b, "  metric %s: %s\n", m.Code, describeMetric(m))
		}
		if n == 0 {
			b.WriteString("  metric: (none)\n")
		}
		var served []string
		for _, rid := range o.RequirementIDs {
			if c := codeByReq[rid]; c != "" {
				served = append(served, c)
			}
		}
		if len(served) == 0 {
			b.WriteString("  served by: (no requirement)\n")
		} else {
			fmt.Fprintf(&b, "  served by: %s\n", strings.Join(served, ", "))
		}
	}
	var orphans []string
	for _, r := range reqs {
		if len(r.ObjectiveIDs) == 0 {
			orphans = append(orphans, r.Code)
		}
	}
	if len(orphans) > 0 {
		fmt.Fprintf(&b, "Requirements serving no objective: %s\n", strings.Join(orphans, ", "))
	}
	return b.String()
}

// describeMetric is a one-line human description of a metric's definition.
func describeMetric(m domain.OutcomeMetric) string {
	var b strings.Builder
	b.WriteString(m.Name)
	if m.Unit != "" {
		fmt.Fprintf(&b, " (%s)", m.Unit)
	}
	fmt.Fprintf(&b, ", %s", m.Direction)
	if m.Baseline != nil {
		fmt.Fprintf(&b, " from %s", domain.FormatMetricValue(*m.Baseline))
	}
	fmt.Fprintf(&b, " to %s", domain.FormatMetricValue(m.Target))
	if m.Guardrail != nil {
		fmt.Fprintf(&b, ", guardrail %s", domain.FormatMetricValue(*m.Guardrail))
	}
	if m.Cadence != "" {
		fmt.Fprintf(&b, ", observed %s", m.Cadence)
	}
	if m.Definition != "" {
		fmt.Fprintf(&b, " -- %s", m.Definition)
	}
	return b.String()
}

// outcomeFingerprints is what "did the revision change this objective or
// metric" compares, keyed by normalized code -- the outcome half of
// changedRequirements, so a later-cycle finding citing OBJ-2 is judged
// against whether OBJ-2 actually changed.
func outcomeFingerprints(objs []domain.Objective, metrics []domain.OutcomeMetric) map[string]string {
	out := map[string]string{}
	for _, o := range objs {
		fp := o.Statement + "\x00" + o.Rationale + "\x00" + string(o.Status)
		for _, m := range metrics {
			if m.ObjectiveID == o.ID {
				fp += "\x00" + m.Code
			}
		}
		out[strings.ToUpper(o.Code)] = fp
	}
	for _, m := range metrics {
		out[strings.ToUpper(m.Code)] = describeMetric(m) + "\x00" + m.ObjectiveID
	}
	return out
}

// changedOutcomes adds to changed (and removed) the objective and metric
// codes whose fingerprint differs between before and after.
func changedOutcomes(before, after map[string]string, changed, removed map[string]bool) {
	for code, fp := range after {
		if prev, ok := before[code]; !ok || prev != fp {
			changed[code] = true
		}
	}
	for code := range before {
		if _, ok := after[code]; !ok {
			removed[code] = true
		}
	}
}
