package projection

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// Outcome-chain gaps: orphan_requirement, unmeasured_objective,
// unserved_objective, drifting_metric, ineffective_requirement. Built through
// the store's own write paths, like the rest of gaps_test.go.

func (f *gapFixture) objective(statement string, status domain.ObjectiveStatus) *domain.Objective {
	f.t.Helper()
	o := &domain.Objective{InitiativeID: f.ini.ID, Statement: statement, Status: status}
	if err := f.db.CreateObjective(f.ctx, o, store.HumanActor); err != nil {
		f.t.Fatal(err)
	}
	return o
}

func (f *gapFixture) link(reqID, objID string) {
	f.t.Helper()
	if err := f.db.LinkRequirementObjective(f.ctx, f.ini.ID, reqID, objID, store.HumanActor); err != nil {
		f.t.Fatal(err)
	}
}

func (f *gapFixture) metric(objID, name string, baseline float64, guardrail *float64, cadence string) *domain.OutcomeMetric {
	f.t.Helper()
	b := baseline
	m := &domain.OutcomeMetric{InitiativeID: f.ini.ID, ObjectiveID: objID, Name: name,
		Direction: domain.DirectionIncrease, Baseline: &b, Target: baseline + 10, Guardrail: guardrail, Cadence: cadence}
	if err := f.db.CreateMetric(f.ctx, m, store.HumanActor); err != nil {
		f.t.Fatal(err)
	}
	return m
}

func (f *gapFixture) observe(metricID string, v float64) {
	f.t.Helper()
	o := &domain.MetricObservation{InitiativeID: f.ini.ID, MetricID: metricID, Value: v, Source: domain.MetricSourceManual}
	if _, _, err := f.db.RecordObservation(f.ctx, o, store.HumanActor, time.Now()); err != nil {
		f.t.Fatal(err)
	}
}

func (f *gapFixture) verified(reqID string) {
	f.t.Helper()
	if err := f.db.MarkRequirementVerified(f.ctx, reqID, store.HumanActor); err != nil {
		f.t.Fatal(err)
	}
}

func (f *gapFixture) gapsAt(now time.Time, kinds ...domain.GapKind) []domain.Gap {
	f.t.Helper()
	ini, err := f.db.GetInitiative(f.ctx, f.ini.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	gaps, err := BuildGaps(f.ctx, f.db, ini, GapOptions{Kinds: kinds, Now: now})
	if err != nil {
		f.t.Fatal(err)
	}
	return gaps
}

func ofKind(gaps []domain.Gap, kind domain.GapKind) []domain.Gap {
	var out []domain.Gap
	for _, g := range gaps {
		if g.Kind == kind {
			out = append(out, g)
		}
	}
	return out
}

func TestGapsObjectivesMissing(t *testing.T) {
	f := newGapFixture(t)
	if got := ofKind(f.gaps(), domain.GapOrphanRequirement); len(got) != 0 {
		t.Fatalf("no requirements, no objectives: nothing to trace, want no gap, got %+v", got)
	}

	f.req("REQ-1", domain.PriorityMust, 1)
	f.req("REQ-2", domain.PriorityShould, 1)
	got := ofKind(f.gaps(), domain.GapOrphanRequirement)
	if len(got) != 1 {
		t.Fatalf("want ONE objectives_missing gap, not one per requirement; got %+v", got)
	}
	g := got[0]
	if g.State != domain.GapStateObjectivesMissing || g.Severity != domain.SeverityMedium ||
		g.Nodes[0].Type != "initiative" || len(g.Nodes) != 3 || g.Next != `idea objective add "<statement>"` {
		t.Fatalf("objectives_missing gap = %+v", g)
	}

	// With a PRD, the gap is about the PRD.
	f.artifact(domain.ArtifactPRD, time.Now())
	g = ofKind(f.gaps(), domain.GapOrphanRequirement)[0]
	if g.Nodes[0].Type != "artifact" || g.Nodes[0].Code != "prd" {
		t.Fatalf("subject = %+v, want the prd artifact", g.Nodes[0])
	}

	// An abandoned objective is not an objective: still missing.
	f.objective("Old goal", domain.ObjectiveAbandoned)
	if got := withState(f.gaps(), domain.GapStateObjectivesMissing); len(got) != 1 {
		t.Fatalf("an abandoned objective does not satisfy objectives_missing: %+v", got)
	}

	// One active objective: objectives_missing gives way to per-requirement gaps.
	f.objective("More users activate", domain.ObjectiveActive)
	gaps := f.gaps()
	if got := withState(gaps, domain.GapStateObjectivesMissing); len(got) != 0 {
		t.Fatalf("an active objective exists: want no objectives_missing, got %+v", got)
	}
	if got := withState(ofKind(gaps, domain.GapOrphanRequirement), domain.GapStateNoObjective); len(got) != 2 {
		t.Fatalf("want a no_objective gap per requirement, got %+v", got)
	}
}

func TestGapsOrphanRequirementNoObjective(t *testing.T) {
	f := newGapFixture(t)
	obj := f.objective("More users activate", domain.ObjectiveActive)
	old := f.objective("Old goal", domain.ObjectiveAbandoned)
	f.req("REQ-1", domain.PriorityMust, 1)
	r2 := f.req("REQ-2", domain.PriorityMust, 1)
	r3 := f.req("REQ-3", domain.PriorityCould, 1)
	f.link(r2.ID, obj.ID)
	f.link(r3.ID, old.ID)

	gaps := f.gaps(domain.GapOrphanRequirement)
	g := one(t, gaps, domain.GapOrphanRequirement, "REQ-1")
	if g.State != domain.GapStateNoObjective || g.Severity != domain.SeverityHigh || g.Next != "idea objective link REQ-1 OBJ-1" {
		t.Fatalf("REQ-1 gap = %+v", g)
	}
	none(t, gaps, domain.GapOrphanRequirement, "REQ-2")
	// Serving only an abandoned objective is serving none (the review's rule).
	one(t, gaps, domain.GapOrphanRequirement, "REQ-3")

	// Two active objectives: the command leaves the choice to the human.
	f.objective("Fewer tickets", domain.ObjectiveActive)
	g = one(t, f.gaps(domain.GapOrphanRequirement), domain.GapOrphanRequirement, "REQ-3")
	if g.Next != "idea objective link REQ-3 <OBJ>" {
		t.Fatalf("next = %q", g.Next)
	}

	// Superseded requirements are not live: no gap.
	if err := f.db.SupersedeRequirements(f.ctx, f.ini.ID, "revise"); err != nil {
		t.Fatal(err)
	}
	if got := f.gaps(domain.GapOrphanRequirement); len(got) != 0 {
		t.Fatalf("superseded requirements are not orphans: %+v", got)
	}
}

func TestGapsUnmeasuredAndUnservedObjective(t *testing.T) {
	f := newGapFixture(t)
	obj := f.objective("More users activate", domain.ObjectiveActive)
	f.objective("Old goal", domain.ObjectiveAbandoned)
	r := f.req("REQ-1", domain.PriorityMust, 1)

	gaps := f.gaps()
	g := one(t, gaps, domain.GapUnmeasuredObjective, "OBJ-1")
	if g.State != domain.GapStateNoMetric || g.Severity != domain.SeverityHigh || g.Next != `idea metric add OBJ-1 "<name>" --target <n>` {
		t.Fatalf("unmeasured gap = %+v", g)
	}
	g = one(t, gaps, domain.GapUnservedObjective, "OBJ-1")
	if g.State != domain.GapStateNoRequirement || g.Severity != domain.SeverityMedium || g.Next != "idea objective link <REQ> OBJ-1" {
		t.Fatalf("unserved gap = %+v", g)
	}
	// An abandoned objective is not a gap of either kind.
	none(t, gaps, domain.GapUnmeasuredObjective, "OBJ-2")
	none(t, gaps, domain.GapUnservedObjective, "OBJ-2")

	f.metric(obj.ID, "activation rate", 20, nil, "")
	f.link(r.ID, obj.ID)
	gaps = f.gaps()
	none(t, gaps, domain.GapUnmeasuredObjective, "OBJ-1")
	none(t, gaps, domain.GapUnservedObjective, "OBJ-1")
}

func TestGapsDriftingMetric(t *testing.T) {
	f := newGapFixture(t)
	obj := f.objective("More users activate", domain.ObjectiveActive)
	r := f.req("REQ-1", domain.PriorityMust, 1)
	f.link(r.ID, obj.ID)

	noData := f.metric(obj.ID, "never observed", 20, nil, "")       // MET-1
	onTrack := f.metric(obj.ID, "on track", 20, nil, "")            // MET-2
	offTrack := f.metric(obj.ID, "off track", 20, nil, "")          // MET-3
	breach := f.metric(obj.ID, "guardrail", 20, f64(15), "")        // MET-4
	regressed := f.metric(obj.ID, "regressed", 20, nil, "")         // MET-5
	stale := f.metric(obj.ID, "stale", 20, nil, "daily")            // MET-6
	cmdStale := f.metric(obj.ID, "measured", 20, nil, "daily")      // MET-7
	unobservedStale := f.metric(obj.ID, "lapsed", 20, nil, "daily") // MET-8
	_ = noData
	_ = unobservedStale
	cmd := *cmdStale
	cmd.Source = domain.MetricSource{Kind: domain.MetricSourceCommand, Command: "echo 25"}
	if err := f.db.UpdateMetric(f.ctx, &cmd, store.HumanActor); err != nil {
		t.Fatal(err)
	}

	f.observe(onTrack.ID, 25)
	f.observe(offTrack.ID, 18)
	f.observe(breach.ID, 10)
	f.observe(regressed.ID, 25)
	f.observe(stale.ID, 25)
	f.observe(cmdStale.ID, 25)
	time.Sleep(5 * time.Millisecond)
	task := f.task("onboarding v2", r.ID)
	cs := f.applied(task.ID)
	time.Sleep(5 * time.Millisecond)
	f.observe(regressed.ID, 22) // worse than 25, a change set landed between: regressed

	// Now: nothing is overdue.
	gaps := f.gaps(domain.GapDriftingMetric)
	none(t, gaps, domain.GapDriftingMetric, "MET-1") // no_data is not a gap
	none(t, gaps, domain.GapDriftingMetric, "MET-2") // on_track
	none(t, gaps, domain.GapDriftingMetric, "MET-6") // observed within its cadence
	none(t, gaps, domain.GapDriftingMetric, "MET-8") // defined within its cadence, never observed: no_data
	g := one(t, gaps, domain.GapDriftingMetric, "MET-3")
	if g.State != domain.GapStateOffTrack || g.Severity != domain.SeverityMedium || g.Next != "idea trace metric MET-3" ||
		len(g.Nodes) < 2 || g.Nodes[1].Code != "OBJ-1" {
		t.Fatalf("off_track gap = %+v", g)
	}
	g = one(t, gaps, domain.GapDriftingMetric, "MET-4")
	if g.State != domain.GapStateOffTrack || g.Severity != domain.SeverityHigh {
		t.Fatalf("guardrail-breach gap = %+v, want off_track/high", g)
	}
	g = one(t, gaps, domain.GapDriftingMetric, "MET-5")
	if g.State != domain.GapStateRegressed || g.Severity != domain.SeverityHigh || g.Next != "idea trace metric MET-5" ||
		g.Nodes[len(g.Nodes)-1].Code != cs.HumanID {
		t.Fatalf("regressed gap = %+v, want the landing change set %s as evidence", g, cs.HumanID)
	}

	// Two days on, the daily metrics are overdue.
	gaps = f.gapsAt(time.Now().Add(48*time.Hour), domain.GapDriftingMetric)
	g = one(t, gaps, domain.GapDriftingMetric, "MET-6")
	if g.State != domain.GapStateStale || g.Severity != domain.SeverityLow || g.Next != "idea metric record MET-6 <value>" {
		t.Fatalf("stale gap = %+v", g)
	}
	if g = one(t, gaps, domain.GapDriftingMetric, "MET-7"); g.Next != "idea metric measure MET-7" {
		t.Fatalf("a metric with a command source is measured, not typed in: %+v", g)
	}
	// Never observed AND past its cadence since it was defined: stale, not no_data.
	if g = one(t, gaps, domain.GapDriftingMetric, "MET-8"); g.State != domain.GapStateStale {
		t.Fatalf("lapsed no-data metric = %+v, want stale", g)
	}
	none(t, gaps, domain.GapDriftingMetric, "MET-1") // no cadence: never stale
}

func TestGapsIneffectiveRequirement(t *testing.T) {
	f := newGapFixture(t)
	obj := f.objective("More users activate", domain.ObjectiveActive)
	other := f.objective("Fewer tickets", domain.ObjectiveActive)
	r1 := f.req("REQ-1", domain.PriorityMust, 1)
	r2 := f.req("REQ-2", domain.PriorityMust, 1)
	r3 := f.req("REQ-3", domain.PriorityMust, 1)
	f.link(r1.ID, obj.ID)
	f.link(r2.ID, obj.ID)
	f.link(r3.ID, other.ID)
	bad := f.metric(obj.ID, "activation rate", 20, nil, "")
	good := f.metric(other.ID, "tickets", 20, nil, "")
	f.observe(bad.ID, 15)
	f.observe(good.ID, 25)
	f.verified(r1.ID)
	f.verified(r3.ID)

	gaps := f.gaps(domain.GapIneffectiveRequirement)
	g := one(t, gaps, domain.GapIneffectiveRequirement, "REQ-1")
	if g.State != domain.GapStateWorksButFailsObjective || g.Severity != domain.SeverityHigh ||
		g.Next != "idea trace metric MET-1" || len(g.Nodes) != 3 || g.Nodes[1].Code != "OBJ-1" || g.Nodes[2].Code != "MET-1" {
		t.Fatalf("ineffective gap = %+v", g)
	}
	none(t, gaps, domain.GapIneffectiveRequirement, "REQ-2") // not verified: it is not yet "working"
	none(t, gaps, domain.GapIneffectiveRequirement, "REQ-3") // its objective is on track

	// The objective achieved or abandoned: no longer an objective it fails.
	o := *obj
	o.Status = domain.ObjectiveAchieved
	if err := f.db.UpdateObjective(f.ctx, &o, store.HumanActor); err != nil {
		t.Fatal(err)
	}
	none(t, f.gaps(domain.GapIneffectiveRequirement), domain.GapIneffectiveRequirement, "REQ-1")
}

// TestOutcomeReportAndGapsAgree: the outcome report (GET …/outcomes, the
// Health page's outcome gaps) and `idea gaps` answer from one rule
// (domain.AnalyzeOutcomeChain), so on the same graph they name the same nodes.
func TestOutcomeReportAndGapsAgree(t *testing.T) {
	f := newGapFixture(t)
	obj := f.objective("More users activate", domain.ObjectiveActive) // OBJ-1: served, measured, off track
	f.objective("Fewer tickets", domain.ObjectiveActive)              // OBJ-2: unserved, unmeasured
	f.objective("Old goal", domain.ObjectiveAbandoned)                // OBJ-3: not a gap
	r1 := f.req("REQ-1", domain.PriorityMust, 1)
	f.req("REQ-2", domain.PriorityMust, 1) // serves nothing
	f.link(r1.ID, obj.ID)
	m := f.metric(obj.ID, "activation rate", 20, nil, "weekly")
	f.observe(m.ID, 15)
	f.verified(r1.ID)

	now := time.Now()
	rep, err := BuildOutcomes(f.ctx, f.db, f.ini.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	gaps := f.gapsAt(now)
	subjects := func(kind domain.GapKind) []string {
		out := []string{}
		for _, g := range ofKind(gaps, kind) {
			out = append(out, g.Nodes[0].Code)
		}
		sort.Strings(out)
		return out
	}
	sorted := func(in []string) []string {
		out := append([]string{}, in...)
		sort.Strings(out)
		return out
	}
	for _, c := range []struct {
		kind   domain.GapKind
		report []string
	}{
		{domain.GapUnmeasuredObjective, rep.Gaps.ObjectivesWithoutMetrics},
		{domain.GapUnservedObjective, rep.Gaps.ObjectivesWithoutRequirements},
		{domain.GapOrphanRequirement, rep.Gaps.RequirementsWithoutObjectives},
	} {
		if got, want := strings.Join(subjects(c.kind), ","), strings.Join(sorted(c.report), ","); got != want || got == "" {
			t.Errorf("%s: idea gaps names %q, the outcome report %q", c.kind, got, want)
		}
	}
	var reportDrift []string
	for _, d := range rep.RequirementDrift {
		reportDrift = append(reportDrift, d.RequirementCode)
	}
	if got, want := strings.Join(subjects(domain.GapIneffectiveRequirement), ","), strings.Join(sorted(reportDrift), ","); got != want || got == "" {
		t.Errorf("works-but-fails: idea gaps %q, outcome report %q", got, want)
	}
	if n := len(ofKind(gaps, domain.GapDriftingMetric)); n != rep.Summary.Drifting || n == 0 {
		t.Errorf("drifting_metric gaps = %d, outcome report drifting = %d", n, rep.Summary.Drifting)
	}

	// And both agree with the store's own gap queries.
	orphans, err := f.db.RequirementsWithoutObjectives(f.ctx, f.ini.ID)
	if err != nil || len(orphans) != 1 || orphans[0].Code != "REQ-2" {
		t.Fatalf("store RequirementsWithoutObjectives = %+v, %v", orphans, err)
	}
}

func f64(v float64) *float64 { return &v }
