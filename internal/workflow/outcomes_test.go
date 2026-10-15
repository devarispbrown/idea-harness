package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/store"
)

const prdWithObjectives = `{"summary":"PRD","requirements":[
	{"statement":"Meter usage events","priority":"must","objectives":["O1"],"acceptance_criteria":[{"metric":"events","target":">= 1","condition":"per request"}]},
	{"statement":"Show a usage dashboard","priority":"should","objectives":["O1","O2"],"acceptance_criteria":[{"metric":"dashboard loads","target":"< 2s","condition":"p95"}]}],
	"objectives":[
	 {"key":"O1","statement":"Customers are billed for what they use","rationale":"flat pricing loses heavy users",
	  "metrics":[{"name":"billing accuracy","unit":"%","direction":"increase","baseline":92,"target":99.5,"guardrail":90,"cadence":"weekly",
	              "hypothesis":"We believe metering every request will raise billing accuracy"}]},
	 {"key":"O2","statement":"Customers understand their bill","metrics":[{"name":"billing tickets","direction":"decrease","target":5,"cadence":"monthly"}]}],
	"decisions":[],"open_questions":[],"risks":[]}`

func runWithPRD(t *testing.T, prds ...string) (*Engine, *store.DB, *domain.Initiative, domain.InitiativeStatus) {
	t.Helper()
	fp := scriptedProvider()
	for i, p := range prds {
		if i == len(prds)-1 {
			fp.SetResponse("prd", p)
		} else {
			fp.EnqueueResponse("prd", p)
		}
	}
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()
	ini := &domain.Initiative{Title: "billing", Description: "usage-based billing"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	status, err := eng.Run(ctx, ini.ID, ini.Description)
	if err != nil {
		t.Fatal(err)
	}
	return eng, db, ini, status
}

// TestPRDStageRecordsObjectivesMetricsAndLinks is the scripted-provider stage
// test: the PRD's objectives become OBJ-n with MET-n metrics, a metric's
// hypothesis becomes a hypothesis assertion predicting it, every requirement
// is linked to the objectives it named, and the graph answers why/impact
// across the new edges.
func TestPRDStageRecordsObjectivesMetricsAndLinks(t *testing.T) {
	eng, db, ini, status := runWithPRD(t, prdWithObjectives)
	ctx := context.Background()
	if status != domain.InitiativeApproved {
		t.Fatalf("a fully linked PRD should approve, got %s", status)
	}

	objs, err := db.ListObjectives(ctx, ini.ID)
	if err != nil || len(objs) != 2 {
		t.Fatalf("objectives = %+v, %v", objs, err)
	}
	if objs[0].Code != "OBJ-1" || objs[1].Code != "OBJ-2" || objs[0].ProducedByRun == "" {
		t.Fatalf("codes/provenance: %+v", objs)
	}
	ms, err := db.ListMetrics(ctx, ini.ID, time.Now())
	if err != nil || len(ms) != 2 {
		t.Fatalf("metrics = %+v, %v", ms, err)
	}
	acc := ms[0]
	if acc.Code != "MET-1" || acc.ObjectiveCode != "OBJ-1" || acc.Baseline == nil || *acc.Baseline != 92 ||
		acc.Target != 99.5 || acc.Guardrail == nil || *acc.Guardrail != 90 || acc.Cadence != "weekly" ||
		acc.Source.Kind != domain.MetricSourceManual {
		t.Fatalf("MET-1 = %+v", acc)
	}
	if ms[1].Baseline != nil {
		t.Errorf("MET-2 had no baseline in the PRD and must not get one: %v", *ms[1].Baseline)
	}

	reqs, _ := db.ListRequirements(ctx, ini.ID)
	if len(reqs[0].ObjectiveIDs) != 1 || len(reqs[1].ObjectiveIDs) != 2 {
		t.Fatalf("serves links: REQ-1 %v, REQ-2 %v", reqs[0].ObjectiveIDs, reqs[1].ObjectiveIDs)
	}

	hyps, _ := db.MetricHypotheses(ctx, ini.ID, acc.ID)
	if len(hyps) != 1 || hyps[0].Type != domain.AssertionHypothesis {
		t.Fatalf("hypotheses on MET-1 = %+v", hyps)
	}

	why, err := eng.Why(ctx, ini.ID, "requirement", reqs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !traceHas(why, "serves", "OBJ-1") {
		t.Fatalf("why REQ-1 should reach OBJ-1 via serves: %+v", why)
	}
	impact, err := eng.Impact(ctx, ini.ID, "objective", objs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	var sawReq, sawMetric, sawHypothesis bool
	for _, it := range impact {
		switch {
		case it.EdgeType == "serves" && it.FromType == "requirement":
			sawReq = true
		case it.EdgeType == "measures" && it.FromType == "metric":
			sawMetric = true
		case it.EdgeType == "predicts" && it.FromType == "assertion":
			sawHypothesis = true
		}
	}
	if !sawReq || !sawMetric || !sawHypothesis {
		t.Fatalf("impact OBJ-1: requirement %v metric %v hypothesis %v: %+v", sawReq, sawMetric, sawHypothesis, impact)
	}

	// The PRD artifact body names the objectives.
	body := latestPRDBody(t, eng, db, ini.ID)
	if !strings.Contains(body, "## Objectives") || !strings.Contains(body, "billing accuracy") || !strings.Contains(body, "Serves: O1") {
		t.Fatalf("PRD body is missing the objectives:\n%s", body)
	}
}

func traceHas(items []TraceItem, edge, toLabelPrefix string) bool {
	for _, it := range items {
		if it.EdgeType == edge && strings.HasPrefix(it.ToLabel, toLabelPrefix) {
			return true
		}
	}
	return false
}

func latestPRDBody(t *testing.T, eng *Engine, db *store.DB, initiativeID string) string {
	t.Helper()
	arts, err := db.ListArtifacts(context.Background(), initiativeID)
	if err != nil {
		t.Fatal(err)
	}
	var digest string
	for _, a := range arts {
		if a.Kind == domain.ArtifactPRD {
			digest = a.BodyDigest
		}
	}
	b, err := eng.contentBlobs().Get(digest)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestPRDReviewBlocksOnOutcomeGapsThenConverges: a PRD with an objective but
// a requirement that serves none (and an objective with no metric the schema
// would have refused -- here, one whose only metric the store rejects) is
// blocked by the deterministic outcome check; the revision that links it
// converges, keeping OBJ-1's code.
func TestPRDReviewBlocksOnOutcomeGapsThenConverges(t *testing.T) {
	unlinked := `{"summary":"PRD","requirements":[
		{"statement":"Meter usage events","priority":"must","objectives":["O1"],"acceptance_criteria":[{"metric":"events","target":">= 1","condition":"per request"}]},
		{"statement":"Dark mode","priority":"could","acceptance_criteria":[{"metric":"theme switch","target":"1","condition":"settings"}]}],
		"objectives":[{"key":"O1","statement":"Customers are billed for what they use",
		  "metrics":[{"name":"billing accuracy","direction":"increase","target":99}]}],
		"decisions":[],"open_questions":[],"risks":[]}`
	linked := `{"summary":"PRD","requirements":[
		{"code":"REQ-1","statement":"Meter usage events","priority":"must","objectives":["OBJ-1"],"acceptance_criteria":[{"metric":"events","target":">= 1","condition":"per request"}]}],
		"objectives":[{"key":"OBJ-1","statement":"Customers are billed for what they use",
		  "metrics":[{"key":"MET-1","name":"billing accuracy","direction":"increase","target":99.5}]}],
		"decisions":[],"open_questions":[],"risks":[]}`
	_, db, ini, status := runWithPRD(t, unlinked, linked)
	ctx := context.Background()
	if status != domain.InitiativeApproved {
		t.Fatalf("the linked revision should converge, got %s", status)
	}
	fs, _ := db.ListFindingsByInitiative(ctx, ini.ID)
	var sawGap bool
	for _, f := range fs {
		if f.Category == "outcome" && strings.Contains(f.Finding, "REQ-2 serves no objective") {
			sawGap = true
			if f.TargetType != "requirement" {
				t.Errorf("the gap finding should target the requirement, got %s", f.TargetType)
			}
		}
	}
	if !sawGap {
		t.Fatalf("cycle 1 should have raised 'REQ-2 serves no objective': %+v", fs)
	}
	objs, _ := db.ListObjectives(ctx, ini.ID)
	if len(objs) != 1 || objs[0].Code != "OBJ-1" {
		t.Fatalf("the revision should continue OBJ-1, not mint another: %+v", objs)
	}
	ms, _ := db.ListMetrics(ctx, ini.ID, time.Now())
	if len(ms) != 1 || ms[0].Code != "MET-1" || ms[0].Target != 99.5 {
		t.Fatalf("the revision should update MET-1 in place: %+v", ms)
	}
}

// TestPRDWithoutObjectivesStillApproves pins back-compat: a PRD that defines
// no objectives gets one medium outcome finding and is not blocked by it.
func TestPRDWithoutObjectivesStillApproves(t *testing.T) {
	_, db, ini, status := runWithPRD(t, `{"summary":"PRD","requirements":[{"statement":"Meter usage events","priority":"must","acceptance_criteria":[{"metric":"events","target":">= 1","condition":"per request"}]}],"decisions":[],"open_questions":[],"risks":[]}`)
	if status != domain.InitiativeApproved {
		t.Fatalf("got %s", status)
	}
	fs, _ := db.ListFindingsByInitiative(context.Background(), ini.ID)
	n := 0
	for _, f := range fs {
		if f.Category == "outcome" {
			n++
			if f.Severity != domain.SeverityMedium {
				t.Errorf("no-objectives finding should be medium, got %s", f.Severity)
			}
		}
	}
	if n != 1 {
		t.Fatalf("outcome findings = %d, want 1", n)
	}
}

// TestPRDRevisionKeepsHumanObjectives: a human-added objective survives a PRD
// revision that does not mention it, and is not rewritten by one that does.
func TestPRDRevisionKeepsHumanObjectives(t *testing.T) {
	eng, db, ini, _ := runWithPRD(t, prdWithObjectives)
	ctx := context.Background()
	human := &domain.Objective{InitiativeID: ini.ID, Statement: "Finance closes the month in a day"}
	if err := db.CreateObjective(ctx, human, store.HumanActor); err != nil {
		t.Fatal(err)
	}
	reqs, _ := db.ListRequirements(ctx, ini.ID)
	if err := db.LinkRequirementObjective(ctx, ini.ID, reqs[0].ID, human.ID, store.HumanActor); err != nil {
		t.Fatal(err)
	}

	// A revision that drops O2 and says nothing about the human's objective,
	// and names no objectives for REQ-1.
	revision := `{"summary":"PRD v2","requirements":[
		{"code":"REQ-1","statement":"Meter usage events","priority":"must","acceptance_criteria":[{"metric":"events","target":">= 1","condition":"per request"}]},
		{"code":"REQ-2","statement":"Show a usage dashboard","priority":"should","objectives":["OBJ-1"],"acceptance_criteria":[{"metric":"dashboard loads","target":"< 2s","condition":"p95"}]}],
		"objectives":[{"key":"OBJ-1","statement":"Customers are billed for what they use","metrics":[{"key":"MET-1","name":"billing accuracy","direction":"increase","target":99.5}]}],
		"decisions":[],"open_questions":[],"risks":[]}`
	runID := "test-run"
	var obj map[string]any
	if err := jsonUnmarshal(revision, &obj); err != nil {
		t.Fatal(err)
	}
	refs := map[string][]string{reqs[0].ID: nil, reqs[1].ID: {"OBJ-1"}}
	if err := eng.recordPRDObjectives(ctx, ini.ID, runID, obj, "human-revise", refs); err != nil {
		t.Fatal(err)
	}
	objs, _ := db.ListObjectives(ctx, ini.ID)
	live := map[string]bool{}
	for _, o := range objs {
		if o.SupersededBy == "" {
			live[o.Code] = true
		}
	}
	if !live["OBJ-1"] || live["OBJ-2"] || !live[human.Code] {
		t.Fatalf("live after revision = %v (OBJ-2 dropped by the model; the human's %s kept)", live, human.Code)
	}
	reqs, _ = db.ListRequirements(ctx, ini.ID)
	if len(reqs[0].ObjectiveIDs) == 0 {
		t.Fatal("a revision naming no objectives for REQ-1 must not unlink it")
	}
	for _, id := range reqs[1].ObjectiveIDs {
		if id == human.ID {
			t.Fatal("REQ-2 was never linked to the human objective")
		}
	}
}

// TestConvergenceCitesObjectiveCodes: a reviewer finding citing OBJ-n is
// cited (so it can block) and re-targeted at the objective, under the same
// rule as a REQ citation; one citing an unknown OBJ is still uncited.
func TestConvergenceCitesObjectiveCodes(t *testing.T) {
	refs := map[string]NodeRef{"OBJ-1": {Type: "objective", ID: "o1"}, "MET-2": {Type: "metric", ID: "m2"}}
	fs := []domain.Finding{
		{Severity: domain.SeverityHigh, Confidence: domain.ConfidenceHigh, Category: "outcome", Finding: "OBJ-1 has no measurable metric", Cites: "OBJ-1"},
		{Severity: domain.SeverityHigh, Confidence: domain.ConfidenceHigh, Category: "outcome", Finding: "MET-2's target cannot be measured as defined"},
		{Severity: domain.SeverityHigh, Confidence: domain.ConfidenceHigh, Category: "outcome", Finding: "OBJ-9 is vague", Cites: "OBJ-9"},
	}
	cls := classifyFindings(classifyInput{rules: guardedRules(), active: reqs("REQ-1"), outcomeRefs: refs}, fs)
	if fs[0].Advisory || fs[0].TargetType != "objective" || fs[0].TargetID != "o1" || cls.cites[0] != "OBJ-1" {
		t.Errorf("OBJ-1 citation: advisory=%v target=%s/%s cite=%q", fs[0].Advisory, fs[0].TargetType, fs[0].TargetID, cls.cites[0])
	}
	if fs[1].Advisory || fs[1].TargetType != "metric" || cls.cites[1] != "MET-2" {
		t.Errorf("MET-2 in the text should count as a citation: advisory=%v target=%s cite=%q", fs[1].Advisory, fs[1].TargetType, cls.cites[1])
	}
	if !fs[2].Advisory {
		t.Error("an unknown OBJ code is not a citation")
	}
	if got := citationOf(domain.Finding{TargetType: "objective", TargetID: "o1"}, nil, map[string]string{"o1": "OBJ-1"}); got != "OBJ-1" {
		t.Errorf("citationOf(objective) = %q", got)
	}
}

// TestImpactReachesOutcomeThroughChangeSet: once an observation attributes a
// change set, `impact requirement` reaches the metric it moved
// (requirement <- task <- change set <- metric).
func TestImpactReachesOutcomeThroughChangeSet(t *testing.T) {
	eng, db, ini, _ := runWithPRD(t, prdWithObjectives)
	ctx := context.Background()
	reqs, _ := db.ListRequirements(ctx, ini.ID)
	task := &domain.Task{InitiativeID: ini.ID, Title: "meter", Statement: "meter", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, reqs[0].ID, nil); err != nil {
		t.Fatal(err)
	}
	cs := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID, PatchDigest: "p"}
	if err := db.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}
	for _, st := range [][2]domain.ChangeSetStatus{
		{domain.ChangeSetDraft, domain.ChangeSetChecksRunning}, {domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired},
		{domain.ChangeSetReviewRequired, domain.ChangeSetApproved}, {domain.ChangeSetApproved, domain.ChangeSetApplying},
	} {
		if err := db.TransitionChangeSet(ctx, cs.ID, st[0], st[1], ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.MarkChangeSetApplied(ctx, cs.ID, "d", ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, _, err := db.RecordObservation(ctx, &domain.MetricObservation{InitiativeID: ini.ID, MetricID: "MET-1", Value: 95, Source: domain.MetricSourceManual}, store.HumanActor, time.Now()); err != nil {
		t.Fatal(err)
	}
	impact, err := eng.Impact(ctx, ini.ID, "requirement", reqs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range impact {
		if it.EdgeType == "attributed_to" && strings.HasPrefix(it.FromLabel, "MET-1") && it.ToLabel == cs.HumanID {
			found = true
		}
	}
	if !found {
		t.Fatalf("impact REQ-1 should reach MET-1 through %s: %+v", cs.HumanID, impact)
	}
	if !ValidNodeType("objective") || !ValidNodeType("metric") || !ValidNodeType("change_set") {
		t.Error("objective, metric and change_set must be addressable node types")
	}
}

// jsonUnmarshal decodes like the structured-output pipeline does (numbers as
// json.Number), so a hand-built response exercises the same num() paths.
func jsonUnmarshal(raw string, v any) error {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(v)
}
