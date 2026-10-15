package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

func f64p(v float64) *float64 { return &v }

type outcomeFixture struct {
	db  *DB
	ini *domain.Initiative
	req *domain.Requirement
	obj *domain.Objective
	met *domain.OutcomeMetric
}

func newOutcomeFixture(t *testing.T) outcomeFixture {
	t.Helper()
	db := openTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{InitiativeID: ini.ID, Statement: "onboarding checklist", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}
	obj := &domain.Objective{InitiativeID: ini.ID, Statement: "More new users reach their first success", Rationale: "activation drives retention"}
	if err := db.CreateObjective(ctx, obj, HumanActor); err != nil {
		t.Fatal(err)
	}
	met := &domain.OutcomeMetric{InitiativeID: ini.ID, ObjectiveID: obj.ID, Name: "activation rate", Unit: "%",
		Direction: domain.DirectionIncrease, Baseline: f64p(20), Target: 35}
	if err := db.CreateMetric(ctx, met, HumanActor); err != nil {
		t.Fatal(err)
	}
	return outcomeFixture{db: db, ini: ini, req: req, obj: obj, met: met}
}

func edgesOfType(t *testing.T, db *DB, initiativeID string, typ domain.EdgeType) []domain.GraphEdge {
	t.Helper()
	all, err := db.ListGraphEdges(context.Background(), initiativeID)
	if err != nil {
		t.Fatal(err)
	}
	var out []domain.GraphEdge
	for _, e := range all {
		if e.EdgeType == typ {
			out = append(out, e)
		}
	}
	return out
}

func TestObjectiveAndMetricCodesAndEdges(t *testing.T) {
	fx := newOutcomeFixture(t)
	ctx := context.Background()
	if fx.obj.Code != "OBJ-1" || fx.met.Code != "MET-1" {
		t.Fatalf("codes = %s, %s; want OBJ-1, MET-1", fx.obj.Code, fx.met.Code)
	}
	if fx.obj.Status != domain.ObjectiveActive {
		t.Errorf("default status = %s, want active", fx.obj.Status)
	}
	measures := edgesOfType(t, fx.db, fx.ini.ID, domain.EdgeMeasures)
	if len(measures) != 1 || measures[0].FromID != fx.met.ID || measures[0].ToID != fx.obj.ID {
		t.Fatalf("measures edges = %+v", measures)
	}

	// Link, twice: one edge.
	for i := 0; i < 2; i++ {
		if err := fx.db.LinkRequirementObjective(ctx, fx.ini.ID, fx.req.ID, fx.obj.ID, HumanActor); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(edgesOfType(t, fx.db, fx.ini.ID, domain.EdgeServes)); n != 1 {
		t.Fatalf("linking twice should leave one serves edge, got %d", n)
	}
	reqs, _ := fx.db.ListRequirements(ctx, fx.ini.ID)
	if len(reqs[0].ObjectiveIDs) != 1 || reqs[0].ObjectiveIDs[0] != fx.obj.ID {
		t.Fatalf("requirement ObjectiveIDs = %v", reqs[0].ObjectiveIDs)
	}
	objs, _ := fx.db.ListObjectives(ctx, fx.ini.ID)
	if len(objs[0].RequirementIDs) != 1 || len(objs[0].MetricIDs) != 1 {
		t.Fatalf("objective links = reqs %v metrics %v", objs[0].RequirementIDs, objs[0].MetricIDs)
	}
	got, err := fx.db.GetObjectiveByRef(ctx, fx.ini.ID, "obj-1")
	if err != nil || got.ID != fx.obj.ID {
		t.Fatalf("GetObjectiveByRef(obj-1) = %v, %v", got, err)
	}

	if err := fx.db.UnlinkRequirementObjective(ctx, fx.ini.ID, fx.req.ID, fx.obj.ID, HumanActor); err != nil {
		t.Fatal(err)
	}
	reqs, _ = fx.db.ListRequirements(ctx, fx.ini.ID)
	if reqs[0].ObjectiveIDs == nil || len(reqs[0].ObjectiveIDs) != 0 {
		t.Fatalf("after unlink ObjectiveIDs = %#v, want empty non-nil", reqs[0].ObjectiveIDs)
	}
}

func TestLinkRefusesAnotherInitiativesObjective(t *testing.T) {
	fx := newOutcomeFixture(t)
	ctx := context.Background()
	other := &domain.Initiative{Title: "y", Description: "y"}
	if err := fx.db.CreateInitiative(ctx, other); err != nil {
		t.Fatal(err)
	}
	foreign := &domain.Objective{InitiativeID: other.ID, Statement: "elsewhere"}
	if err := fx.db.CreateObjective(ctx, foreign, HumanActor); err != nil {
		t.Fatal(err)
	}
	err := fx.db.LinkRequirementObjective(ctx, fx.ini.ID, fx.req.ID, foreign.ID, HumanActor)
	var nf *NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("cross-initiative link: got %v, want a NotFoundError", err)
	}
}

func TestCreateMetricValidation(t *testing.T) {
	fx := newOutcomeFixture(t)
	ctx := context.Background()
	bad := []domain.OutcomeMetric{
		{Name: "", Direction: domain.DirectionIncrease},
		{Name: "x", Direction: "up"},
		{Name: "x", Direction: domain.DirectionIncrease, Cadence: "sometimes"},
		{Name: "x", Direction: domain.DirectionIncrease, Source: domain.MetricSource{Kind: domain.MetricSourceCommand}},
		{Name: "x", Direction: domain.DirectionMaintain, Guardrail: f64p(-1)},
	}
	for _, m := range bad {
		m.InitiativeID, m.ObjectiveID = fx.ini.ID, fx.obj.ID
		if err := fx.db.CreateMetric(ctx, &m, HumanActor); err == nil {
			t.Errorf("%+v should be refused", m)
		}
	}
	missing := domain.OutcomeMetric{InitiativeID: fx.ini.ID, ObjectiveID: "nope", Name: "x", Direction: domain.DirectionIncrease}
	if err := fx.db.CreateMetric(ctx, &missing, HumanActor); err == nil {
		t.Error("a metric for an unknown objective should be refused")
	}
}

// applyTestChangeSet walks a change set through to applied with a committed
// journal entry, which is what RecordObservation's attribution reads.
func applyTestChangeSet(t *testing.T, db *DB, initiativeID string) *domain.ChangeSet {
	t.Helper()
	ctx := context.Background()
	cs := newTestChangeSet(t, db, initiativeID)
	for _, step := range [][2]domain.ChangeSetStatus{
		{domain.ChangeSetDraft, domain.ChangeSetChecksRunning},
		{domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired},
		{domain.ChangeSetReviewRequired, domain.ChangeSetApproved},
		{domain.ChangeSetApproved, domain.ChangeSetApplying},
	} {
		if err := db.TransitionChangeSet(ctx, cs.ID, step[0], step[1], ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.MarkChangeSetApplied(ctx, cs.ID, "digest", ""); err != nil {
		t.Fatal(err)
	}
	return cs
}

func countEventsOfType(t *testing.T, db *DB, eventType string) int {
	t.Helper()
	var n int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM events WHERE event_type = ?`, eventType).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRecordObservationAttributesChangeSetsAndEmitsDrift(t *testing.T) {
	fx := newOutcomeFixture(t)
	ctx := context.Background()

	// First observation: nothing has landed yet.
	o1 := &domain.MetricObservation{InitiativeID: fx.ini.ID, MetricID: fx.met.Code, Value: 25, Source: domain.MetricSourceManual}
	_, after, err := fx.db.RecordObservation(ctx, o1, HumanActor, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if after.Drift != domain.DriftOnTrack || len(o1.ChangeSetIDs) != 0 {
		t.Fatalf("first observation: drift %s, change sets %v", after.Drift, o1.ChangeSetIDs)
	}

	// A change set lands, then the metric drops below where it was AND
	// below the baseline: regressed (which outranks off_track).
	time.Sleep(5 * time.Millisecond)
	cs := applyTestChangeSet(t, fx.db, fx.ini.ID)
	time.Sleep(5 * time.Millisecond)
	o2 := &domain.MetricObservation{InitiativeID: fx.ini.ID, MetricID: fx.met.ID, Value: 18, Source: domain.MetricSourceManual, Note: "after onboarding v2"}
	before, after, err := fx.db.RecordObservation(ctx, o2, HumanActor, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(o2.ChangeSetIDs) != 1 || o2.ChangeSetIDs[0] != cs.ID {
		t.Fatalf("attribution = %v, want [%s]", o2.ChangeSetIDs, cs.ID)
	}
	if before.Drift != domain.DriftOnTrack || after.Drift != domain.DriftRegressed {
		t.Fatalf("drift %s -> %s, want on_track -> regressed", before.Drift, after.Drift)
	}
	attributed := edgesOfType(t, fx.db, fx.ini.ID, domain.EdgeAttributedTo)
	if len(attributed) != 1 || attributed[0].FromID != fx.met.ID || attributed[0].ToID != cs.ID {
		t.Fatalf("attributed_to edges = %+v", attributed)
	}
	if n := countEventsOfType(t, fx.db, "metric.drifted"); n != 1 {
		t.Fatalf("metric.drifted events = %d, want 1", n)
	}

	// Another bad reading with nothing new landed: still off track, no
	// second drift event (the status did not change into a drifting one),
	// and no new attribution.
	o3 := &domain.MetricObservation{InitiativeID: fx.ini.ID, MetricID: fx.met.ID, Value: 17, Source: domain.MetricSourceManual}
	if _, after, err = fx.db.RecordObservation(ctx, o3, HumanActor, time.Now()); err != nil {
		t.Fatal(err)
	}
	if after.Drift != domain.DriftOffTrack || len(o3.ChangeSetIDs) != 0 {
		t.Fatalf("third: drift %s attribution %v", after.Drift, o3.ChangeSetIDs)
	}
	if n := countEventsOfType(t, fx.db, "metric.drifted"); n != 2 {
		// regressed -> off_track is a change INTO a drifting status too.
		t.Fatalf("metric.drifted events = %d, want 2", n)
	}
	if n := countEventsOfType(t, fx.db, "metric.observed"); n != 3 {
		t.Fatalf("metric.observed events = %d, want 3", n)
	}

	obs, err := fx.db.ListObservations(ctx, fx.met.ID)
	if err != nil || len(obs) != 3 || obs[1].Note != "after onboarding v2" || obs[1].RecordedBy != HumanActor {
		t.Fatalf("observations = %+v, %v", obs, err)
	}

	// The metric's own events are visible in the initiative's event scope.
	evs, err := fx.db.ListEventsForInitiative(ctx, fx.ini.ID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range evs {
		seen[e.EventType] = true
	}
	for _, want := range []string{"objective.created", "metric.created", "metric.observed", "metric.drifted"} {
		if !seen[want] {
			t.Errorf("%s missing from the initiative's events", want)
		}
	}
}

func TestRecordObservationIgnoresRevertedAndNoOpChangeSets(t *testing.T) {
	fx := newOutcomeFixture(t)
	ctx := context.Background()
	reverted := applyTestChangeSet(t, fx.db, fx.ini.ID)
	if err := fx.db.MarkChangeSetReverted(ctx, reverted.ID, ""); err != nil {
		t.Fatal(err)
	}
	noop := applyTestChangeSet(t, fx.db, fx.ini.ID)
	if _, err := fx.db.SQL().Exec(`UPDATE change_sets SET no_op = 1 WHERE id = ?`, noop.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	o := &domain.MetricObservation{InitiativeID: fx.ini.ID, MetricID: fx.met.ID, Value: 30, Source: domain.MetricSourceManual}
	if _, _, err := fx.db.RecordObservation(ctx, o, HumanActor, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(o.ChangeSetIDs) != 0 {
		t.Fatalf("a reverted and a no-op change set changed nothing that is still there: got %v", o.ChangeSetIDs)
	}
}

func TestGapQueries(t *testing.T) {
	fx := newOutcomeFixture(t)
	ctx := context.Background()
	lonely := &domain.Objective{InitiativeID: fx.ini.ID, Statement: "Fewer support tickets"}
	if err := fx.db.CreateObjective(ctx, lonely, HumanActor); err != nil {
		t.Fatal(err)
	}
	abandoned := &domain.Objective{InitiativeID: fx.ini.ID, Statement: "Old goal", Status: domain.ObjectiveAbandoned}
	if err := fx.db.CreateObjective(ctx, abandoned, HumanActor); err != nil {
		t.Fatal(err)
	}

	noMetrics, err := fx.db.ObjectivesWithoutMetrics(ctx, fx.ini.ID)
	if err != nil || len(noMetrics) != 1 || noMetrics[0].ID != lonely.ID {
		t.Fatalf("ObjectivesWithoutMetrics = %+v, %v (an abandoned objective is not a gap)", noMetrics, err)
	}
	noReqs, _ := fx.db.ObjectivesWithoutRequirements(ctx, fx.ini.ID)
	if len(noReqs) != 2 {
		t.Fatalf("ObjectivesWithoutRequirements = %d, want 2 (nothing links yet)", len(noReqs))
	}
	orphans, _ := fx.db.RequirementsWithoutObjectives(ctx, fx.ini.ID)
	if len(orphans) != 1 || orphans[0].ID != fx.req.ID {
		t.Fatalf("RequirementsWithoutObjectives = %+v", orphans)
	}

	if err := fx.db.LinkRequirementObjective(ctx, fx.ini.ID, fx.req.ID, fx.obj.ID, HumanActor); err != nil {
		t.Fatal(err)
	}
	orphans, _ = fx.db.RequirementsWithoutObjectives(ctx, fx.ini.ID)
	noReqs, _ = fx.db.ObjectivesWithoutRequirements(ctx, fx.ini.ID)
	if len(orphans) != 0 || len(noReqs) != 1 || noReqs[0].ID != lonely.ID {
		t.Fatalf("after linking: orphans %d, objectives without requirements %+v", len(orphans), noReqs)
	}

	// Drifting + the "works but fails its objective" signal.
	drifting, _ := fx.db.DriftingMetrics(ctx, fx.ini.ID, time.Now())
	if len(drifting) != 0 {
		t.Fatalf("an unobserved metric with no cadence is no_data, not drifting: %+v", drifting)
	}
	o := &domain.MetricObservation{InitiativeID: fx.ini.ID, MetricID: fx.met.ID, Value: 10, Source: domain.MetricSourceManual}
	if _, _, err := fx.db.RecordObservation(ctx, o, HumanActor, time.Now()); err != nil {
		t.Fatal(err)
	}
	drifting, _ = fx.db.DriftingMetrics(ctx, fx.ini.ID, time.Now())
	if len(drifting) != 1 || drifting[0].Status.Drift != domain.DriftOffTrack {
		t.Fatalf("DriftingMetrics = %+v", drifting)
	}
	rd, _ := fx.db.RequirementOutcomeDrift(ctx, fx.ini.ID, time.Now())
	if len(rd) != 0 {
		t.Fatalf("an unverified requirement is not 'works but fails': %+v", rd)
	}
	if err := fx.db.ApproveRequirements(ctx, fx.ini.ID, HumanActor); err != nil {
		t.Fatal(err)
	}
	if err := fx.db.MarkRequirementVerified(ctx, fx.req.ID, HumanActor); err != nil {
		t.Fatal(err)
	}
	rd, _ = fx.db.RequirementOutcomeDrift(ctx, fx.ini.ID, time.Now())
	if len(rd) != 1 || rd[0].Signal != domain.SignalWorksButFailsObjective || rd[0].ObjectiveCode != "OBJ-1" || rd[0].MetricCodes[0] != "MET-1" {
		t.Fatalf("RequirementOutcomeDrift = %+v", rd)
	}
}

func TestSupersedeModelObjectivesKeepsHumansAndObservedMetrics(t *testing.T) {
	fx := newOutcomeFixture(t)
	ctx := context.Background()
	run := "run-1"
	modelObj := &domain.Objective{InitiativeID: fx.ini.ID, Statement: "model said", ProducedByRun: run}
	if err := fx.db.CreateObjective(ctx, modelObj, ""); err != nil {
		t.Fatal(err)
	}
	unobserved := &domain.OutcomeMetric{InitiativeID: fx.ini.ID, ObjectiveID: modelObj.ID, Name: "a", Direction: domain.DirectionIncrease, Target: 1, ProducedByRun: run}
	observed := &domain.OutcomeMetric{InitiativeID: fx.ini.ID, ObjectiveID: modelObj.ID, Name: "b", Direction: domain.DirectionIncrease, Target: 1, ProducedByRun: run}
	for _, m := range []*domain.OutcomeMetric{unobserved, observed} {
		if err := fx.db.CreateMetric(ctx, m, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := fx.db.RecordObservation(ctx, &domain.MetricObservation{InitiativeID: fx.ini.ID, MetricID: observed.ID, Value: 1, Source: domain.MetricSourceManual}, HumanActor, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := fx.db.SupersedeModelObjectivesExcept(ctx, fx.ini.ID, "review-cycle-1", nil); err != nil {
		t.Fatal(err)
	}
	objs, _ := fx.db.ListObjectives(ctx, fx.ini.ID)
	for _, o := range objs {
		switch o.ID {
		case fx.obj.ID:
			if o.SupersededBy != "" {
				t.Error("a human's objective must never be superseded by a model revision")
			}
		case modelObj.ID:
			if o.SupersededBy != "review-cycle-1" {
				t.Error("the model's dropped objective should be superseded")
			}
		}
	}
	ms, _ := fx.db.ListMetrics(ctx, fx.ini.ID, time.Now())
	live := map[string]bool{}
	for _, m := range ms {
		live[m.ID] = true
	}
	if live[unobserved.ID] || !live[observed.ID] || !live[fx.met.ID] {
		t.Fatalf("live metrics after supersede = %v (the observed one keeps its history)", live)
	}
}

func TestCreateMetricHypothesis(t *testing.T) {
	fx := newOutcomeFixture(t)
	ctx := context.Background()
	a, err := fx.db.CreateMetricHypothesis(ctx, fx.ini.ID, fx.met.ID, "We believe a checklist will move MET-1", "human", "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Type != domain.AssertionHypothesis {
		t.Fatalf("type = %s", a.Type)
	}
	hs, err := fx.db.MetricHypotheses(ctx, fx.ini.ID, fx.met.ID)
	if err != nil || len(hs) != 1 || hs[0].ID != a.ID {
		t.Fatalf("MetricHypotheses = %+v, %v", hs, err)
	}
	if n := len(edgesOfType(t, fx.db, fx.ini.ID, domain.EdgePredicts)); n != 1 {
		t.Fatalf("predicts edges = %d", n)
	}
}
