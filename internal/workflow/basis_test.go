package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
	"github.com/devarispbrown/idea-harness/internal/projection"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// basisFixture runs framing and research on the demo responses, which record
// ASM-1 (framing assumption), ASM-2 (research assumption) and ASM-3 (research
// hypothesis).
func basisFixture(t *testing.T) (*Engine, *store.DB, *model.FakeProvider, *domain.Initiative) {
	t.Helper()
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()
	ini := &domain.Initiative{Title: "metering", Description: "usage-based billing"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if err := eng.runFraming(ctx, ini.ID, ini.Description); err != nil {
		t.Fatal(err)
	}
	if err := eng.runResearch(ctx, ini.ID, ini.Description); err != nil {
		t.Fatal(err)
	}
	as, err := db.ListAssertions(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, a := range as {
		codes = append(codes, a.Code+":"+string(a.Type))
	}
	if strings.Join(codes, ",") != "ASM-1:assumption,ASM-2:assumption,ASM-3:hypothesis" {
		t.Fatalf("framing+research assertions = %v", codes)
	}
	return eng, db, fp, ini
}

func assertionByCode(t *testing.T, db *store.DB, iniID, code string) domain.Assertion {
	t.Helper()
	as, err := db.ListAssertions(context.Background(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range as {
		if a.Code == code {
			return a
		}
	}
	t.Fatalf("no assertion %s", code)
	return domain.Assertion{}
}

// basisOf is the set of assertion codes a node rests on (derived_from edges).
func basisOf(t *testing.T, db *store.DB, iniID, fromType, fromID string) []string {
	t.Helper()
	ctx := context.Background()
	edges, err := db.ListGraphEdgesFrom(ctx, iniID, fromType, fromID)
	if err != nil {
		t.Fatal(err)
	}
	as, _ := db.ListAssertions(ctx, iniID)
	code := map[string]string{}
	for _, a := range as {
		code[a.ID] = a.Code
	}
	var out []string
	for _, e := range edges {
		if e.EdgeType == domain.EdgeDerivedFrom && e.ToType == "assertion" {
			out = append(out, code[e.ToID])
		}
	}
	return out
}

const basisPRD = `{"summary":"PRD","requirements":[{"statement":"Meter API calls per key","priority":"must",` +
	`"acceptance_criteria":[{"metric":"m","target":"t","condition":"c"}],"based_on":["ASM-2"]}],` +
	`"decisions":[{"question":"Bill monthly or per use?","options":["monthly","per use"],"recommendation":"per use","based_on":["asm-3","ASM-99"]}],` +
	`"open_questions":[],"risks":[]}`

// The PRD's based_on citations become derived_from edges, the prompt lists
// the citable assertions by code, and -- the point of it -- retracting the
// hypothesis a decided decision rests on now fires basis_refuted on the
// decision and counts its dependents on the assertion, on a real flow rather
// than a hand-built graph.
func TestPRDBasedOnWritesEdgesAndGapsFire(t *testing.T) {
	eng, db, fp, ini := basisFixture(t)
	ctx := context.Background()
	fp.SetResponse("prd", basisPRD)
	if err := eng.runProductDefinition(ctx, ini.ID, nil, ""); err != nil {
		t.Fatal(err)
	}

	var prdSystem string
	for _, c := range fp.Calls() {
		if c.JSONSchemaName == "prd" {
			prdSystem = c.System
		}
	}
	if !strings.Contains(prdSystem, "- ASM-3 (hypothesis, candidate") || !strings.Contains(prdSystem, "based_on") {
		t.Fatalf("PRD prompt does not list citable assertions:\n%s", prdSystem)
	}

	reqs, _ := db.ListRequirements(ctx, ini.ID)
	decs, _ := db.ListDecisions(ctx, ini.ID)
	if len(reqs) != 1 || len(decs) != 1 {
		t.Fatalf("reqs=%d decs=%d", len(reqs), len(decs))
	}
	if got := basisOf(t, db, ini.ID, "requirement", reqs[0].ID); strings.Join(got, ",") != "ASM-2" {
		t.Errorf("requirement basis = %v; want ASM-2", got)
	}
	// Case-insensitive, and the unknown ASM-99 is skipped, not fatal.
	if got := basisOf(t, db, ini.ID, "decision", decs[0].ID); strings.Join(got, ",") != "ASM-3" {
		t.Errorf("decision basis = %v; want ASM-3", got)
	}

	// Before any judgment: both research assertions are unvalidated with one
	// dependent each -- medium, no longer the dependents-free low.
	gaps, err := projection.BuildGaps(ctx, db, *ini, projection.GapOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"ASM-2", "ASM-3"} {
		g := findGap(gaps, domain.GapUnvalidatedAssumption, code)
		if g == nil || g.Dependents != 1 || g.Severity != domain.SeverityMedium {
			t.Errorf("%s gap = %+v; want unvalidated, medium, 1 dependent", code, g)
		}
	}

	if err := db.ResolveDecision(ctx, decs[0].ID, "per use", "human"); err != nil {
		t.Fatal(err)
	}
	// `idea assumption ASM-3 retract` is this status write (POST …/status).
	if err := db.SetAssertionStatus(ctx, assertionByCode(t, db, ini.ID, "ASM-3").ID, domain.AssertionRetracted, "human"); err != nil {
		t.Fatal(err)
	}
	gaps, err = projection.BuildGaps(ctx, db, *ini, projection.GapOptions{})
	if err != nil {
		t.Fatal(err)
	}
	g := findGap(gaps, domain.GapInvalidatedDecision, decs[0].Code)
	if g == nil || g.State != domain.GapStateBasisRefuted || g.Severity != domain.SeverityHigh || !strings.Contains(g.Why, "ASM-3 is retracted") {
		t.Errorf("decision gap = %+v; want basis_refuted naming ASM-3", g)
	}
	r := findGap(gaps, domain.GapUnvalidatedAssumption, "ASM-3")
	if r == nil || r.State != domain.GapStateRefuted || r.Dependents < 1 {
		t.Errorf("ASM-3 gap = %+v; want refuted with the decision resting on it", r)
	}
}

func findGap(gaps []domain.Gap, kind domain.GapKind, code string) *domain.Gap {
	for i, g := range gaps {
		if g.Kind == kind && len(g.Nodes) > 0 && g.Nodes[0].Code == code {
			return &gaps[i]
		}
	}
	return nil
}

// A requirement revised in place restates its basis: a citation the revision
// dropped leaves the graph; a decision re-listed by the revision keeps the
// basis it already had and gains the new one.
func TestRevisionReplacesRequirementBasis(t *testing.T) {
	eng, db, fp, ini := basisFixture(t)
	ctx := context.Background()
	fp.SetResponse("prd", basisPRD)
	if err := eng.runProductDefinition(ctx, ini.ID, nil, ""); err != nil {
		t.Fatal(err)
	}
	reqs, _ := db.ListRequirements(ctx, ini.ID)
	fp.SetResponse("prd", `{"summary":"PRD","requirements":[{"code":"`+reqs[0].Code+`","statement":"Meter API calls per key, hourly","priority":"must",`+
		`"acceptance_criteria":[{"metric":"m","target":"t","condition":"c"}],"based_on":["ASM-3"]}],`+
		`"decisions":[{"question":"Bill monthly or per use?","options":["monthly","per use"],"recommendation":"per use","based_on":["ASM-2"]}],`+
		`"open_questions":[],"risks":[]}`)
	if err := eng.runProductDefinition(ctx, ini.ID, activeRequirements(reqs), "review-cycle-1"); err != nil {
		t.Fatal(err)
	}
	after, _ := db.ListRequirements(ctx, ini.ID)
	live := activeRequirements(after)
	if len(live) != 1 || live[0].ID != reqs[0].ID {
		t.Fatalf("revision did not update in place: %+v", live)
	}
	if got := basisOf(t, db, ini.ID, "requirement", live[0].ID); strings.Join(got, ",") != "ASM-3" {
		t.Errorf("revised requirement basis = %v; want only ASM-3", got)
	}
	decs, _ := db.ListDecisions(ctx, ini.ID)
	if len(decs) != 1 {
		t.Fatalf("decisions = %d; the re-listed one should merge", len(decs))
	}
	if got := basisOf(t, db, ini.ID, "decision", decs[0].ID); strings.Join(got, ",") != "ASM-3,ASM-2" {
		t.Errorf("decision basis = %v; want ASM-3 then ASM-2", got)
	}
}

// Research findings can corroborate or contradict a framing assumption by
// code; the edge is written, and an unknown code is skipped.
func TestResearchRelatesFindingsToFramingAssumptions(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled})
	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if err := eng.runFraming(ctx, ini.ID, "x"); err != nil {
		t.Fatal(err)
	}
	fp.SetResponse("research_brief", `{"summary":"s","findings":[`+
		`{"statement":"Most customers meter several resources","type":"assumption","confidence":"medium","source":"interviews","contradicts":["ASM-1"]},`+
		`{"statement":"Usage pricing is common","type":"assumption","confidence":"low","source":"model","supports":["ASM-1","ASM-7"]}]}`)
	if err := eng.runResearch(ctx, ini.ID, "x"); err != nil {
		t.Fatal(err)
	}
	var research string
	for _, c := range fp.Calls() {
		if c.JSONSchemaName == "research_brief" {
			research = c.System
		}
	}
	if !strings.Contains(research, "- ASM-1 (assumption, candidate") {
		t.Errorf("research prompt does not list the framing assumptions:\n%s", research)
	}
	asm1 := assertionByCode(t, db, ini.ID, "ASM-1")
	edges, _ := db.ListGraphEdgesTo(ctx, ini.ID, "assertion", asm1.ID)
	got := map[domain.EdgeType]string{}
	for _, e := range edges {
		if e.FromType == "assertion" {
			got[e.EdgeType] = e.FromID
		}
	}
	if got[domain.EdgeContradicts] != assertionByCode(t, db, ini.ID, "ASM-2").ID || got[domain.EdgeSupports] != assertionByCode(t, db, ini.ID, "ASM-3").ID {
		t.Errorf("edges into ASM-1 = %+v", edges)
	}
}

// The demo provider cites the research findings, so a no-key demo's graph is
// not empty of basis edges.
func TestDemoPRDCitesResearchFindings(t *testing.T) {
	eng, db, _, ini := basisFixture(t)
	ctx := context.Background()
	if err := eng.runProductDefinition(ctx, ini.ID, nil, ""); err != nil {
		t.Fatal(err)
	}
	reqs, _ := db.ListRequirements(ctx, ini.ID)
	if len(reqs) == 0 {
		t.Fatal("no requirements")
	}
	if got := basisOf(t, db, ini.ID, "requirement", reqs[0].ID); strings.Join(got, ",") != "ASM-2,ASM-3" {
		t.Errorf("demo requirement basis = %v; want ASM-2,ASM-3", got)
	}
}

// The reviewer sees what requirements rest on, with status, and is told it may
// flag one resting on an unvalidated hypothesis -- medium and cited.
func TestReviewerSeesRequirementBasis(t *testing.T) {
	eng, db, fp, ini := basisFixture(t)
	ctx := context.Background()
	fp.SetResponse("prd", basisPRD)
	if err := eng.runProductDefinition(ctx, ini.ID, nil, ""); err != nil {
		t.Fatal(err)
	}
	reqs, _ := db.ListRequirements(ctx, ini.ID)
	if _, _, err := eng.runAdversarialReview(ctx, ini.ID, activeRequirements(reqs), RigorStandard, nil); err != nil {
		t.Fatal(err)
	}
	var sys string
	for _, c := range fp.Calls() {
		if c.JSONSchemaName == "review" {
			sys = c.System
		}
	}
	if !strings.Contains(sys, "- ASM-2 (assumption, candidate") || !strings.Contains(sys, "rested on by "+reqs[0].Code) {
		t.Errorf("review prompt lacks the basis:\n%s", sys)
	}
	if strings.Contains(sys, "- ASM-1 (") {
		t.Errorf("review prompt lists an assertion nothing rests on:\n%s", sys)
	}
	if !strings.Contains(sys, `category "basis"`) || !strings.Contains(sys, "severity medium") {
		t.Errorf("review policy lacks the basis instruction:\n%s", sys)
	}
}
