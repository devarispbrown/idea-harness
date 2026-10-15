package workflow

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/projection"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// invalidatesOf lists "ASM-n>type" for every invalidates edge in the
// initiative, sorted.
func invalidatesOf(t *testing.T, db *store.DB, iniID string) []string {
	t.Helper()
	ctx := context.Background()
	edges, err := db.ListGraphEdges(ctx, iniID)
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
		if e.EdgeType == domain.EdgeInvalidates {
			out = append(out, code[e.FromID]+">"+e.ToType)
		}
	}
	sort.Strings(out)
	return out
}

// Retracting an assertion is what writes `invalidates`: framing and research
// record ASM-n, the PRD cites them in based_on, and `idea assumption ASM-n
// retract` (this status write) adds assertion --invalidates--> each live node
// resting on it. Gaps, why and impact all read the edge; a repeat is a no-op;
// a later accept removes it.
func TestRetractionWritesInvalidatesEdges(t *testing.T) {
	eng, db, fp, ini := basisFixture(t)
	ctx := context.Background()
	fp.SetResponse("prd", basisPRD) // REQ based_on ASM-2, DEC based_on ASM-3
	if err := eng.runProductDefinition(ctx, ini.ID, nil, ""); err != nil {
		t.Fatal(err)
	}
	if got := invalidatesOf(t, db, ini.ID); len(got) != 0 {
		t.Fatalf("invalidates before any retraction = %v", got)
	}
	decs, _ := db.ListDecisions(ctx, ini.ID)
	reqs, _ := db.ListRequirements(ctx, ini.ID)
	dec, req := decs[0], reqs[0]
	if err := db.ResolveDecision(ctx, dec.ID, "per use", "human"); err != nil {
		t.Fatal(err)
	}
	asm3 := assertionByCode(t, db, ini.ID, "ASM-3")
	asm2 := assertionByCode(t, db, ini.ID, "ASM-2")

	// Retract twice: one edge, one invalidates_set event.
	for i := 0; i < 2; i++ {
		if err := db.SetAssertionStatus(ctx, asm3.ID, domain.AssertionRetracted, "human"); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(invalidatesOf(t, db, ini.ID), ","); got != "ASM-3>decision" {
		t.Fatalf("invalidates after retracting ASM-3 = %q; want ASM-3>decision", got)
	}
	evs, err := db.ListEvents(ctx, 0, 10000)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if e.EventType == "assertion.invalidates_set" && e.AggregateID == asm3.ID {
			n++
			if e.Actor != "human" || e.Operation != "decision:"+dec.ID {
				t.Errorf("invalidates_set event = %+v", e)
			}
		}
	}
	if n != 1 {
		t.Errorf("invalidates_set events for ASM-3 = %d; want 1", n)
	}

	// Gaps: basis_refuted names ASM-3 once (edge and derived_from agree), and
	// the invalidator is not counted as the decision's dependent.
	gaps, err := projection.BuildGaps(ctx, db, *ini, projection.GapOptions{})
	if err != nil {
		t.Fatal(err)
	}
	g := findGap(gaps, domain.GapInvalidatedDecision, dec.Code)
	if g == nil || g.State != domain.GapStateBasisRefuted || len(g.Nodes) != 2 || g.Nodes[1].Code != "ASM-3" || g.Dependents != 0 {
		t.Fatalf("decision gap = %+v; want basis_refuted on ASM-3 alone, no dependents", g)
	}
	if r := findGap(gaps, domain.GapUnvalidatedAssumption, "ASM-3"); r == nil || r.State != domain.GapStateRefuted || r.Dependents != 1 {
		t.Errorf("ASM-3 gap = %+v; want refuted, 1 dependent", r)
	}

	// Lineage: why(decision) lists the invalidation; impact(ASM-3) classes
	// the decision obsolete; impact(decision) does not list ASM-3 as a dependent.
	why, err := eng.Why(ctx, ini.ID, "decision", dec.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range why {
		if it.EdgeType == string(domain.EdgeInvalidates) && it.FromID == asm3.ID && it.ToID == dec.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("why decision = %+v; want ASM-3 invalidates it", why)
	}
	imp, err := eng.Impact(ctx, ini.ID, "assertion", asm3.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(imp) != 1 || imp[0].FromID != dec.ID || imp[0].Staleness != "obsolete" {
		t.Errorf("impact ASM-3 = %+v; want the decision, obsolete", imp)
	}
	decImp, err := eng.Impact(ctx, ini.ID, "decision", dec.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range decImp {
		if it.FromID == asm3.ID {
			t.Errorf("impact decision lists its invalidator as a dependent: %+v", it)
		}
	}
	counts, err := eng.ImpactCounts(ctx, ini.ID, []NodeRef{{Type: "decision", ID: dec.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if counts["decision:"+dec.ID] != 0 {
		t.Errorf("decision blocks %d; the invalidator is not a dependent", counts["decision:"+dec.ID])
	}

	// The requirement side: retracting ASM-2 invalidates REQ-1.
	if err := db.SetAssertionStatus(ctx, asm2.ID, domain.AssertionRetracted, "human"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(invalidatesOf(t, db, ini.ID), ","); got != "ASM-2>requirement,ASM-3>decision" {
		t.Fatalf("invalidates = %q", got)
	}
	if imp, _ := eng.Impact(ctx, ini.ID, "assertion", asm2.ID); len(imp) == 0 || imp[0].FromID != req.ID || imp[0].Staleness != "obsolete" {
		t.Errorf("impact ASM-2 = %+v; want the requirement, obsolete", imp)
	}

	// Accepting ASM-3 after all removes its edge and the gap with it.
	if err := db.SetAssertionStatus(ctx, asm3.ID, domain.AssertionAccepted, "human"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(invalidatesOf(t, db, ini.ID), ","); got != "ASM-2>requirement" {
		t.Fatalf("invalidates after accepting ASM-3 = %q", got)
	}
	gaps, _ = projection.BuildGaps(ctx, db, *ini, projection.GapOptions{})
	if g := findGap(gaps, domain.GapInvalidatedDecision, dec.Code); g != nil && g.State == domain.GapStateBasisRefuted {
		t.Errorf("basis_refuted survived accepting ASM-3: %+v", g)
	}
}

// A PRD that cites an assertion already retracted gets the edge on the
// write, and a revision that drops the citation loses it.
func TestPRDCitingRetractedAssertionIsInvalidated(t *testing.T) {
	eng, db, fp, ini := basisFixture(t)
	ctx := context.Background()
	if err := db.SetAssertionStatus(ctx, assertionByCode(t, db, ini.ID, "ASM-2").ID, domain.AssertionRetracted, "human"); err != nil {
		t.Fatal(err)
	}
	if got := invalidatesOf(t, db, ini.ID); len(got) != 0 {
		t.Fatalf("invalidates with nothing resting on ASM-2 = %v", got)
	}
	fp.SetResponse("prd", basisPRD)
	if err := eng.runProductDefinition(ctx, ini.ID, nil, ""); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(invalidatesOf(t, db, ini.ID), ","); got != "ASM-2>requirement" {
		t.Fatalf("invalidates after the PRD = %q; want ASM-2>requirement", got)
	}

	reqs, _ := db.ListRequirements(ctx, ini.ID)
	fp.SetResponse("prd", `{"summary":"PRD","requirements":[{"code":"`+reqs[0].Code+`","statement":"Meter API calls per key, hourly","priority":"must",`+
		`"acceptance_criteria":[{"metric":"m","target":"t","condition":"c"}],"based_on":["ASM-3"]}],`+
		`"decisions":[],"open_questions":[],"risks":[]}`)
	if err := eng.runProductDefinition(ctx, ini.ID, activeRequirements(reqs), "review-cycle-1"); err != nil {
		t.Fatal(err)
	}
	if got := invalidatesOf(t, db, ini.ID); len(got) != 0 {
		t.Errorf("invalidates after the revision dropped ASM-2 = %v; want none", got)
	}
}
