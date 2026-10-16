package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// ---------- classifyFindings: one rule per test ----------

func guardedRules() config.ConvergenceConfig { return config.ConvergenceConfig{}.Effective() }

func reqs(codes ...string) []domain.Requirement {
	out := make([]domain.Requirement, 0, len(codes))
	for _, c := range codes {
		out = append(out, domain.Requirement{ID: "id-" + c, Code: c, Statement: "statement of " + c})
	}
	return out
}

func high(text, cites string) domain.Finding {
	return domain.Finding{Severity: domain.SeverityHigh, Confidence: domain.ConfidenceHigh, Category: "acceptance", Finding: text, Cites: cites}
}

func TestClassifyCitedFindingBlocksAndIsRetargeted(t *testing.T) {
	fs := []domain.Finding{high("REQ-2 has no error case", "REQ-2")}
	cls := classifyFindings(classifyInput{rules: guardedRules(), active: reqs("REQ-1", "REQ-2")}, fs)
	if fs[0].Advisory {
		t.Fatalf("a cited high finding must still block: %+v", fs[0])
	}
	if fs[0].TargetType != "requirement" || fs[0].TargetID != "id-REQ-2" {
		t.Fatalf("finding not re-targeted at the requirement it cites: %q/%q", fs[0].TargetType, fs[0].TargetID)
	}
	if cls.cites[0] != "REQ-2" || len(cls.demoted) != 0 {
		t.Fatalf("cites=%v demoted=%v", cls.cites, cls.demoted)
	}
}

// A finding that names a requirement only in its own text is about that
// requirement whether or not it filled in the field -- an older prompt, a
// scripted response.
func TestClassifyCitationFallsBackToTheCodeInTheText(t *testing.T) {
	fs := []domain.Finding{high("req-1's target is not measurable", "")}
	cls := classifyFindings(classifyInput{rules: guardedRules(), active: reqs("REQ-1")}, fs)
	if fs[0].Advisory || cls.cites[0] != "REQ-1" {
		t.Fatalf("text citation not honoured: advisory=%v cites=%v", fs[0].Advisory, cls.cites)
	}
}

func TestClassifyUncitedFindingIsAdvisoryNotDropped(t *testing.T) {
	fs := []domain.Finding{
		high("the system should also support SSO and audit logging", ""),
		high("cites a superseded requirement", "REQ-9"),
	}
	cls := classifyFindings(classifyInput{rules: guardedRules(), active: reqs("REQ-1")}, fs)
	for i, f := range fs {
		if !f.Advisory || !strings.Contains(f.Note, "cites no active requirement") {
			t.Fatalf("finding %d: an uncited blocker must be advisory with the reason on it: %+v", i, f)
		}
	}
	if cls.demoted[demoteUncited] != 2 {
		t.Fatalf("demoted = %v", cls.demoted)
	}
}

func TestClassifyCitationRuleCanBeTurnedOff(t *testing.T) {
	off := false
	rules := config.ConvergenceConfig{RequireCitation: &off}.Effective()
	fs := []domain.Finding{high("uncited", "")}
	classifyFindings(classifyInput{rules: rules, active: reqs("REQ-1")}, fs)
	if fs[0].Advisory {
		t.Fatal("require_citation: false must let an uncited high finding block")
	}
}

// "idea" is a citation only with a quote: anything could claim the idea asks
// for it.
func TestClassifyIdeaCitationNeedsAQuote(t *testing.T) {
	idea := "A command-line todo list that syncs tasks to a plain markdown file"
	quoted := high("nothing covers syncing to markdown", "idea")
	quoted.Evidence = "\"syncs tasks to a plain markdown file\""
	unquoted := high("there is no multi-tenant billing", "idea")
	unquoted.Evidence = "enterprise customers will want invoices"
	fs := []domain.Finding{quoted, unquoted}
	classifyFindings(classifyInput{rules: guardedRules(), active: reqs("REQ-1"), idea: idea}, fs)
	if fs[0].Advisory {
		t.Fatalf("an idea citation with a quote must block: %+v", fs[0])
	}
	if !fs[1].Advisory {
		t.Fatalf("an idea citation without a quote is uncited: %+v", fs[1])
	}
}

func TestClassifyMediumAndBelowAreMarkedAdvisory(t *testing.T) {
	fs := []domain.Finding{{Severity: domain.SeverityMedium, Confidence: domain.ConfidenceLow, Finding: "nit", Cites: "REQ-1"}}
	cls := classifyFindings(classifyInput{rules: guardedRules(), active: reqs("REQ-1")}, fs)
	if !fs[0].Advisory || len(cls.demoted) != 0 {
		t.Fatalf("medium must read advisory without counting as a demotion: %+v %v", fs[0], cls.demoted)
	}
}

func TestClassifyGateSeverityCritical(t *testing.T) {
	rules := config.ConvergenceConfig{GateSeverity: "critical"}.Effective()
	crit := high("data loss on REQ-1", "REQ-1")
	crit.Severity = domain.SeverityCritical
	fs := []domain.Finding{high("REQ-1 unclear", "REQ-1"), crit}
	cls := classifyFindings(classifyInput{rules: rules, active: reqs("REQ-1")}, fs)
	if !fs[0].Advisory || fs[1].Advisory || cls.demoted[demoteBelowGate] != 1 {
		t.Fatalf("gate=critical: high must be advisory, critical must block: %+v / %+v", fs[0], fs[1])
	}
}

func priors(addressed bool) []priorFinding {
	return []priorFinding{{
		Label: "F1", ID: "prior-1", Severity: domain.SeverityHigh, Category: "acceptance",
		Text: "REQ-1 does not define behaviour for invalid input, so error handling cannot be verified", Cites: "REQ-1", Addressed: addressed,
	}}
}

// The reproduction of the non-convergence: the reviewer re-raises, reworded,
// a finding the revision addressed, and gives no reason.
func TestClassifyRephrasedReRaiseOfAnAddressedFindingIsAdvisory(t *testing.T) {
	fs := []domain.Finding{high("Error handling for invalid input is still undefined in REQ-4, so it cannot be verified", "REQ-4")}
	cls := classifyFindings(classifyInput{rules: guardedRules(), later: true, active: reqs("REQ-4"), prior: priors(true), changed: map[string]bool{"REQ-4": true}}, fs)
	if p, ok := cls.reRaiseOf[0]; !ok || p != 0 {
		t.Fatalf("a reworded re-raise was not recognized: %v", cls.reRaiseOf)
	}
	if !fs[0].Advisory || cls.demoted[demoteUnjustifiedReRaise] != 1 {
		t.Fatalf("an unjustified re-raise of an addressed finding must be advisory: %+v", fs[0])
	}
	if len(cls.resolved) != 0 {
		t.Fatalf("a re-raised prior is not resolved: %v", cls.resolved)
	}
}

func TestClassifyJustifiedReRaiseStillBlocks(t *testing.T) {
	f := high("Error handling for invalid input is still undefined in REQ-4, so it cannot be verified", "REQ-4")
	f.Justification = "the revision added a criterion for empty input but not malformed input"
	fs := []domain.Finding{f}
	classifyFindings(classifyInput{rules: guardedRules(), later: true, active: reqs("REQ-4"), prior: priors(true), changed: map[string]bool{"REQ-4": true}}, fs)
	if fs[0].Advisory {
		t.Fatalf("a justified re-raise must block: %+v", fs[0])
	}
	if !strings.Contains(fs[0].Note, "re-raises F1") || !strings.Contains(fs[0].Note, "malformed input") {
		t.Fatalf("the justification must be recorded on the finding: %q", fs[0].Note)
	}
}

// A finding the revision did nothing about needs no justification to persist.
func TestClassifyReRaiseOfAnUnaddressedFindingBlocks(t *testing.T) {
	fs := []domain.Finding{high("Error handling for invalid input is still undefined in REQ-1, so it cannot be verified", "REQ-1")}
	classifyFindings(classifyInput{rules: guardedRules(), later: true, active: reqs("REQ-1"), prior: priors(false), changed: map[string]bool{}}, fs)
	if fs[0].Advisory {
		t.Fatalf("an unaddressed finding re-raised must block: %+v", fs[0])
	}
}

func TestClassifyExplicitReRaiseLabelWins(t *testing.T) {
	f := high("completely different words", "REQ-1")
	f.ReRaises = "f1"
	fs := []domain.Finding{f}
	cls := classifyFindings(classifyInput{rules: guardedRules(), later: true, active: reqs("REQ-1"), prior: priors(true), changed: map[string]bool{}}, fs)
	if _, ok := cls.reRaiseOf[0]; !ok {
		t.Fatal("an explicit reraises label must link the finding whatever its wording")
	}
}

// No goalpost moving: a new finding in a later cycle about text that did not
// change should have been raised the first time that text was reviewed.
func TestClassifyLaterNewFindingOnUnchangedTextIsAdvisory(t *testing.T) {
	fs := []domain.Finding{
		high("REQ-1 has no rate limit", "REQ-1"),
		high("REQ-2 now contradicts REQ-1", "REQ-2"),
	}
	cls := classifyFindings(classifyInput{rules: guardedRules(), later: true, active: reqs("REQ-1", "REQ-2"), changed: map[string]bool{"REQ-2": true}}, fs)
	if !fs[0].Advisory || cls.demoted[demoteLaterUnchanged] != 1 {
		t.Fatalf("new finding about unchanged REQ-1 must be advisory: %+v", fs[0])
	}
	if fs[1].Advisory {
		t.Fatalf("new finding about changed REQ-2 must block: %+v", fs[1])
	}
}

func TestClassifyLaterNewCriticalAlwaysBlocks(t *testing.T) {
	f := high("REQ-1 stores passwords in plain text", "REQ-1")
	f.Severity = domain.SeverityCritical
	fs := []domain.Finding{f}
	classifyFindings(classifyInput{rules: guardedRules(), later: true, active: reqs("REQ-1"), changed: map[string]bool{}}, fs)
	if fs[0].Advisory {
		t.Fatal("a critical finding must block in any cycle")
	}
}

func TestClassifyLaterFindingsAny(t *testing.T) {
	rules := config.ConvergenceConfig{LaterFindings: "any"}.Effective()
	fs := []domain.Finding{high("REQ-1 has no rate limit", "REQ-1")}
	classifyFindings(classifyInput{rules: rules, later: true, active: reqs("REQ-1"), changed: map[string]bool{}}, fs)
	if fs[0].Advisory {
		t.Fatal("later_findings: any must not demote a new later finding")
	}
}

func TestClassifyResolvedPriors(t *testing.T) {
	cls := classifyFindings(classifyInput{rules: guardedRules(), later: true, active: reqs("REQ-1"), prior: priors(true), changed: map[string]bool{}}, nil)
	if len(cls.resolved) != 1 || cls.resolved[0] != 0 {
		t.Fatalf("a prior nobody re-raised is resolved: %v", cls.resolved)
	}
}

func TestChangedRequirements(t *testing.T) {
	before := []domain.Requirement{
		{ID: "a", Code: "REQ-1", Statement: "one"},
		{ID: "b", Code: "REQ-2", Statement: "two"},
		{ID: "c", Code: "REQ-3", Statement: "three"},
	}
	after := []domain.Requirement{
		{ID: "a", Code: "REQ-1", Statement: "one"},
		{ID: "b", Code: "REQ-2", Statement: "two, now with a criterion", AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "m", Target: "1", Condition: "c"}}},
		{ID: "d", Code: "REQ-4", Statement: "four"},
	}
	changed, removed := changedRequirements(before, after)
	if changed["REQ-1"] || !changed["REQ-2"] || !changed["REQ-4"] || len(changed) != 2 {
		t.Fatalf("changed = %v", changed)
	}
	if !removed["REQ-3"] || len(removed) != 1 {
		t.Fatalf("removed = %v", removed)
	}
}

func TestMarkAddressed(t *testing.T) {
	ps := []priorFinding{
		{Label: "F1", Cites: "REQ-1"},
		{Label: "F2", Cites: "REQ-2"},
		{Label: "F3", Cites: ""},
		{Label: "F4", Cites: "REQ-9"},
	}
	markAddressed(ps, map[string]string{"F3": "added REQ-5 for the export gap"}, map[string]bool{"REQ-1": true}, map[string]bool{"REQ-9": true})
	want := []bool{true, false, true, true}
	for i, p := range ps {
		if p.Addressed != want[i] {
			t.Errorf("%s addressed = %v, want %v", p.Label, p.Addressed, want[i])
		}
	}
	if ps[2].Response == "" {
		t.Error("the reviser's response must be carried to the reviewer")
	}
}

// ---------- the loop, end to end on the engine ----------

// rephrasedReview is cycle n's reviewer: the same concern about the first
// listed requirement, reworded each cycle, never justified -- the measured
// failure mode. {{first_requirement}} is resolved by the fake provider, since
// each revision mints a new code.
var rephrasedReview = []string{
	`{"findings":[{"severity":"high","confidence":"high","category":"acceptance","finding":"{{first_requirement}} does not define behaviour for invalid input, so error handling cannot be verified","recommendation":"add a criterion for invalid input","requirement":"{{first_requirement}}"}]}`,
	`{"findings":[{"severity":"high","confidence":"high","category":"acceptance","finding":"Error handling for invalid input is still undefined in {{first_requirement}}, so it cannot be verified","recommendation":"specify invalid-input handling","requirement":"{{first_requirement}}"}]}`,
	`{"findings":[{"severity":"high","confidence":"high","category":"acceptance","finding":"Invalid input error handling in {{first_requirement}} cannot be verified because behaviour is not defined","recommendation":"define it","requirement":"{{first_requirement}}"}]}`,
}

// blockerReview is a genuine blocker: a critical contradiction the reviser
// cannot resolve, re-raised every cycle with a reason.
const blockerReview = `{"findings":[{"severity":"critical","confidence":"high","category":"consistency",` +
	`"finding":"{{first_requirement}} requires all data to stay offline, but the idea requires real-time sync between devices","recommendation":"a human must choose offline-only or sync",` +
	`"requirement":"{{first_requirement}}","reraises":"F1","justification":"the revision restated both constraints; they still contradict"}]}`

func runLoop(t *testing.T, conv config.ConvergenceConfig, reviews ...string) (*Engine, *store.DB, string, domain.InitiativeStatus) {
	t.Helper()
	fp := scriptedProvider()
	for _, r := range reviews {
		fp.EnqueueResponse("review", r)
	}
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3, Rigor: "standard", Convergence: conv}})
	ctx := context.Background()
	ini := &domain.Initiative{Title: "billing", Description: "build usage-based billing"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	status, err := eng.Run(ctx, ini.ID, ini.Description)
	if err != nil {
		t.Fatal(err)
	}
	return eng, db, ini.ID, status
}

// The fix, proven both ways on the same script: the legacy loop parks a
// reviewer that only ever rephrases one addressed finding; the guarded loop
// recognizes the re-raise, records it as advisory, and converges at cycle 2.
func TestRephrasingReviewerParksInLegacyAndConvergesGuarded(t *testing.T) {
	_, _, _, legacy := runLoop(t, config.ConvergenceConfig{Mode: "legacy"}, rephrasedReview...)
	if legacy != domain.InitiativeNeedsHumanJudgment {
		t.Fatalf("legacy: the rephrasing reviewer should reproduce non-convergence, got %q", legacy)
	}

	_, db, id, guarded := runLoop(t, config.ConvergenceConfig{}, rephrasedReview...)
	if guarded != domain.InitiativeApproved {
		t.Fatalf("guarded: the rephrasing reviewer should converge, got %q", guarded)
	}
	tel, err := db.ListReviewTelemetry(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(tel) != 2 {
		t.Fatalf("want 2 cycles of telemetry, got %d: %+v", len(tel), tel)
	}
	if tel[0].Outcome != outcomeRevise || tel[0].OpenBlocking != 1 || tel[0].PrevOpenBlocking != -1 {
		t.Fatalf("cycle 1 telemetry = %+v", tel[0])
	}
	c2 := tel[1]
	if c2.Outcome != outcomeConverged || c2.ReRaised[domain.SeverityHigh] != 1 || c2.Demoted[demoteUnjustifiedReRaise] != 1 || c2.OpenBlocking != 0 {
		t.Fatalf("cycle 2 telemetry = %+v", c2)
	}

	// Nothing was dropped: both findings are on record, the first superseded
	// by its re-raise, the re-raise advisory with the rule on it.
	fs, _ := db.ListFindingsByInitiative(context.Background(), id)
	var adv []domain.Finding
	for _, f := range fs {
		if f.Category == "acceptance" && f.ReviewRunID != "deterministic-check" {
			adv = append(adv, f)
		}
	}
	if len(adv) != 2 {
		t.Fatalf("want both cycles' findings persisted, got %d", len(adv))
	}
	if adv[0].Disposition != domain.DispositionDuplicate {
		t.Fatalf("cycle 1's finding should be superseded by its re-raise, got %q", adv[0].Disposition)
	}
	if !adv[1].Advisory || !strings.Contains(adv[1].Note, "re-raises F1") {
		t.Fatalf("cycle 2's re-raise should be advisory and say why: %+v", adv[1])
	}
}

// Human gating where it is warranted: a justified critical blocker that the
// revision cannot fix stops the loop -- and stops it at cycle 2, by the stall
// detector, rather than spending the third cycle.
func TestGenuineBlockerStillParksAndStallsEarly(t *testing.T) {
	_, db, id, status := runLoop(t, config.ConvergenceConfig{}, blockerReview, blockerReview, blockerReview)
	if status != domain.InitiativeNeedsHumanJudgment {
		t.Fatalf("a genuine blocker must park for a human, got %q", status)
	}
	ini, _ := db.GetInitiative(context.Background(), id)
	if ini.JudgmentReason != domain.JudgmentPRDReview {
		t.Fatalf("judgment reason = %q", ini.JudgmentReason)
	}
	tel, _ := db.ListReviewTelemetry(context.Background(), id)
	if len(tel) != 2 || tel[1].Outcome != outcomeStalled || tel[1].OpenBlocking != 1 || tel[1].PrevOpenBlocking != 1 {
		t.Fatalf("want a stall at cycle 2, got %+v", tel)
	}
	// The blocker is open for the human (one current finding, not one per cycle).
	open := 0
	fs, _ := db.ListFindingsByInitiative(context.Background(), id)
	for _, f := range fs {
		if f.Disposition == "" && !f.Advisory && f.Severity.Blocking() {
			open++
		}
	}
	if open != 1 {
		t.Fatalf("want exactly the current blocker open, got %d", open)
	}
}

func TestStallCyclesTwoSpendsTheBudget(t *testing.T) {
	_, db, id, status := runLoop(t, config.ConvergenceConfig{StallCycles: 2}, blockerReview, blockerReview, blockerReview)
	if status != domain.InitiativeNeedsHumanJudgment {
		t.Fatalf("got %q", status)
	}
	tel, _ := db.ListReviewTelemetry(context.Background(), id)
	if len(tel) != 3 || tel[2].Outcome != outcomeMaxCycles {
		t.Fatalf("stall_cycles: 2 with max_cycles 3 should reach max_cycles, got %+v", tel)
	}
}

// The reviewer is no longer memoryless, and the reviser is told precisely.
func TestLoopGivesTheReviewerPriorFindingsAndTheReviserALabelledBrief(t *testing.T) {
	fp := scriptedProvider()
	for _, r := range rephrasedReview {
		fp.EnqueueResponse("review", r)
	}
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3, Rigor: "standard"}})
	ctx := context.Background()
	ini := &domain.Initiative{Title: "billing", Description: "build usage-based billing"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Run(ctx, ini.ID, ini.Description); err != nil {
		t.Fatal(err)
	}
	var reviews, prds []model.Request
	for _, c := range fp.Calls() {
		switch c.JSONSchemaName {
		case "review":
			reviews = append(reviews, c)
		case "prd":
			prds = append(prds, c)
		}
	}
	if len(reviews) != 2 || len(prds) != 2 {
		t.Fatalf("want 2 reviews and 2 PRD calls (author + one revision), got %d/%d", len(reviews), len(prds))
	}
	if strings.Contains(reviews[0].System, "--- prior_findings") {
		t.Fatal("cycle 1 has no prior findings to show")
	}
	if !strings.Contains(reviews[0].System, "--- convergence_policy") {
		t.Fatal("every guarded review states the citation policy")
	}
	if !strings.Contains(reviews[1].System, "--- prior_findings") || !strings.Contains(reviews[1].System, "F1 [high]") {
		t.Fatalf("cycle 2's reviewer must see cycle 1's findings by label")
	}
	if !strings.Contains(prds[1].System, "--- review_findings") || !strings.Contains(prds[1].System, "F1 [high]") ||
		!strings.Contains(prds[1].System, "Recommendation: add a criterion for invalid input") {
		t.Fatal("the reviser must get the blocking findings by label, with their recommendation")
	}
	if strings.Contains(prds[0].System, "--- review_findings") {
		t.Fatal("the initial authoring has no review to answer")
	}
}

// Legacy mode sends exactly the prompts it always has.
func TestLegacyModeAddsNoConvergenceInputs(t *testing.T) {
	fp := scriptedProvider()
	for _, r := range rephrasedReview {
		fp.EnqueueResponse("review", r)
	}
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3, Rigor: "standard", Convergence: config.ConvergenceConfig{Mode: "legacy"}}})
	ctx := context.Background()
	ini := &domain.Initiative{Title: "billing", Description: "build usage-based billing"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Run(ctx, ini.ID, ini.Description); err != nil {
		t.Fatal(err)
	}
	for _, c := range fp.Calls() {
		for _, sel := range []string{"--- convergence_policy", "--- prior_findings", "--- review_findings"} {
			if strings.Contains(c.System, sel) {
				t.Fatalf("legacy mode sent %s on a %s call", sel, c.JSONSchemaName)
			}
		}
	}
	tel, _ := db.ListReviewTelemetry(ctx, ini.ID)
	if len(tel) != 3 || tel[2].Mode != "legacy" || tel[2].Outcome != outcomeMaxCycles {
		t.Fatalf("legacy still records telemetry; got %+v", tel)
	}
}

// A human revise after a park starts a new loop seeded with the open
// findings, so a reworded twin of what the human just revised away is a
// re-raise, not a brand-new blocker.
func TestReviseSeedsThePriorCycleFromOpenFindings(t *testing.T) {
	eng, db, id, status := runLoop(t, config.ConvergenceConfig{}, blockerReview, blockerReview)
	if status != domain.InitiativeNeedsHumanJudgment {
		t.Fatalf("setup: got %q", status)
	}
	ctx := context.Background()
	prior, err := eng.seedPriorFindings(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(prior) != 1 || prior[0].Label != "F1" || prior[0].Severity != domain.SeverityCritical {
		t.Fatalf("want the one open blocker seeded as F1, got %+v", prior)
	}
	if prior[0].Cites == "" {
		t.Fatal("the seeded finding should still know which requirement it cited")
	}
	// The review queue is drained: the next review returns no findings, so
	// the human revise converges, and the seeded blocker is retired as fixed
	// (the PRD was regenerated after it was raised).
	if _, err := db.GetInitiative(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := db.ClaimInitiativeRunning(ctx, id, domain.InitiativeNeedsHumanJudgment); err != nil {
		t.Fatal(err)
	}
	if st, err := eng.Revise(ctx, id); err != nil || st != domain.InitiativeApproved {
		t.Fatalf("revise: %q %v", st, err)
	}
	f, err := db.GetFinding(ctx, prior[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if f.Disposition != domain.DispositionFixed && f.Disposition != domain.DispositionNotApplicable {
		t.Fatalf("the seeded blocker should be retired by the next loop, got %q", f.Disposition)
	}
}

// The plan review is a single pass, but an uncited blocker there is no more
// actionable than on the PRD.
func TestPlanReviewCitationRule(t *testing.T) {
	for _, tc := range []struct {
		name, review string
		want         domain.InitiativeStatus
	}{
		{"uncited", `{"findings":[{"severity":"high","confidence":"high","category":"plan","finding":"consider a microservice split","recommendation":"split"}]}`, domain.InitiativePlanApproved},
		{"cites a task", `{"findings":[{"severity":"high","confidence":"high","category":"plan","finding":"TASK-1 has no test step","recommendation":"add one","requirement":"TASK-1"}]}`, domain.InitiativeNeedsHumanJudgment},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fp := scriptedProvider()
			fp.EnqueueResponse("plan_review", tc.review)
			eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3, Rigor: "standard"}})
			ctx := context.Background()
			ini := &domain.Initiative{Title: "billing", Description: "build usage-based billing"}
			if err := db.CreateInitiative(ctx, ini); err != nil {
				t.Fatal(err)
			}
			if _, err := eng.Run(ctx, ini.ID, ini.Description); err != nil {
				t.Fatal(err)
			}
			st, err := eng.Advance(ctx, ini.ID)
			if err != nil {
				t.Fatal(err)
			}
			if st != tc.want {
				t.Fatalf("got %q, want %q", st, tc.want)
			}
			tel, _ := db.ListReviewTelemetry(ctx, ini.ID)
			last := tel[len(tel)-1]
			if last.Review != "execution_plan" {
				t.Fatalf("plan review telemetry missing: %+v", tel)
			}
		})
	}
}
