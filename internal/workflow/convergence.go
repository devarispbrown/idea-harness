package workflow

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
)

// Review convergence (review.convergence, config.ConvergenceConfig).
//
// The PRD review did not converge in 2 of 2 measured real-model runs. Reading
// the loop, not the model, explained it -- each of these is a property of the
// code before this file existed, not of any one model:
//
//  1. The reviewer was memoryless. Every cycle it received the requirements
//     and nothing else: not the previous cycle's findings, not what the
//     revision did about them. So every cycle was a fresh adversarial review of
//     a (usually larger) PRD, and a fresh adversarial review always finds
//     something.
//  2. Convergence was "zero open critical/high findings in one fresh pass".
//     One new finding anywhere blocked -- including one about text the
//     reviewer had already seen, unchanged, the cycle before, and one that
//     cited nothing at all (scope expansion).
//  3. There was no cross-cycle identity. A rephrased re-raise was a brand-new
//     finding; inheritDispositionFromPrior only carries a HUMAN's disposition
//     forward, so inside the automated loop nothing recognized it. Earlier
//     cycles' findings also stayed open until convergence, so a parked
//     initiative showed every cycle's findings as if all were current.
//  4. The reviser was told about findings imprecisely: renderFindings dumped
//     every finding the initiative ever had (all cycles, plan and change-set
//     reviews included) as "[severity] text" with no evidence, recommendation
//     or requirement, and its policy only obliged it to act on findings
//     "dispositioned 'mitigate'" -- which no in-loop finding ever is.
//  5. Nothing measured progress. The loop ran to max_cycles whether the
//     blocking count was falling or flat, and recorded nothing either way.
//
// The rules below answer each. None lowers the bar for a genuine blocker: a
// finding that cites a requirement, is re-raised with a reason, and keeps
// blocking will park the initiative for a human -- sooner than before, via the
// stall detector, instead of after spending the remaining cycles.

// Demotion reasons, as recorded in ReviewTelemetry.Demoted and on the
// finding's Note.
const (
	demoteBelowGate          = "below_gate"
	demoteUncited            = "uncited"
	demoteUnjustifiedReRaise = "unjustified_reraise"
	demoteLaterUnchanged     = "later_unchanged_target"
	demoteLaterNotCritical   = "later_not_critical"
)

// Telemetry outcomes (domain.ReviewTelemetry.Outcome).
const (
	outcomeConverged = "converged"
	outcomeRevise    = "revise"
	outcomeStalled   = "stalled"
	outcomeMaxCycles = "max_cycles"
	outcomeBlocked   = "blocked"
)

// citeIdea is the citation a finding uses for a gap: something the idea asks
// for that no requirement covers. It cannot name a requirement (there is none
// to name), so it must quote the idea instead -- see quotesIdea.
const citeIdea = "IDEA"

// reRaiseSameCategory / reRaiseAnyCategory are the token-Jaccard similarities
// (dedup.go's own measure, on finding text with requirement codes stripped)
// at or above which a finding is treated as re-raising a prior-cycle one
// without saying so. Same-category uses findingDedupeThreshold, the value the
// human-disposition inheritance already settled on for reworded review prose;
// a different category needs more textual agreement, since a model is as
// likely to re-file a concern under a new category as it is to reword it.
const reRaiseAnyCategory = 0.7

// priorFinding is one gating adversarial finding from the previous cycle of
// the loop, as the next cycle's reviewer and rules see it.
type priorFinding struct {
	Label    string // "F1", "F2", ... -- stable within one hand-off
	ID       string // the persisted finding, dispositioned once the next cycle rules on it
	Severity domain.Severity
	Category string
	Text     string
	Evidence string
	Cites    string // requirement code (or "IDEA") it cited when raised, "" if none
	// Addressed is whether the revision between the two cycles did anything
	// about it: the reviser answered it by label, or the requirement it
	// cited was changed or superseded. Re-raising an addressed finding needs
	// a justification; re-raising an unaddressed one does not.
	Addressed bool
	Response  string // the reviser's own review_responses entry, if any
}

// classification is what classifyFindings decided about one cycle.
type classification struct {
	// reRaiseOf maps an index into the cycle's findings to the index of the
	// prior finding it re-raises.
	reRaiseOf map[int]int
	// resolved lists prior indices no finding this cycle re-raised.
	resolved []int
	demoted  map[string]int
	// cites is each finding's resolved citation (normalized code, citeIdea,
	// or ""), by index -- what the next cycle's prior list records.
	cites []string
}

// classifyInput is everything classifyFindings needs that is not the
// findings themselves. Kept as a struct so the rule can be unit-tested
// without an Engine, a store, or a model.
type classifyInput struct {
	rules config.ConvergenceConfig // effective
	// later is true when the reviewer was shown prior findings: cycle 2+ of
	// a loop, or cycle 1 of a loop seeded from an earlier one (a human
	// revise or resume).
	later  bool
	active []domain.Requirement
	// taskCodes are additionally citable codes (normalized) that are not
	// requirements -- the plan review's TASK-n. A finding citing one is
	// cited, but not re-targeted (a plan finding always targets the plan).
	taskCodes map[string]bool
	// outcomeRefs are the live objectives' and metrics' codes (normalized,
	// OBJ-n / MET-n), each with the node it names. A finding citing one is
	// cited -- "objective has no measurable metric" is about an objective,
	// not a requirement -- and is re-targeted at that node, exactly as a
	// requirement citation is.
	outcomeRefs map[string]NodeRef
	idea        string
	prior       []priorFinding
	// changed is the set of active requirement codes the revision since the
	// previous cycle added or changed. nil means unknown (a seeded first
	// cycle), and the "changed" later-findings bar is then not applied --
	// unknown is not evidence a requirement was left alone.
	changed map[string]bool
}

// classifyFindings applies the convergence rules to one cycle's adversarial
// findings, in place: it resolves each finding's citation (setting
// TargetType/TargetID to the cited requirement), links re-raises to the prior
// finding they repeat, and marks Advisory -- with a Note naming the rule --
// every finding a rule says must not gate. It never drops a finding and never
// touches a deterministic one (callers pass adversarial findings only).
//
// A finding already Advisory on entry (quick rigor's one-pass acceptance) is
// still linked and counted, but no rule needs to demote it again.
func classifyFindings(in classifyInput, findings []domain.Finding) classification {
	out := classification{reRaiseOf: map[int]int{}, demoted: map[string]int{}, cites: make([]string, len(findings))}
	byCode := make(map[string]domain.Requirement, len(in.active))
	for _, r := range in.active {
		if r.Code != "" {
			byCode[NormalizeRequirementRef(r.Code)] = r
		}
	}
	matched := make([]bool, len(in.prior))
	demote := func(f *domain.Finding, reason, note string) {
		f.Advisory = true
		out.demoted[reason]++
		f.Note = joinNote(f.Note, note)
	}

	for i := range findings {
		f := &findings[i]
		cited := resolveCitation(f, byCode, in.taskCodes, in.outcomeRefs, in.idea)
		out.cites[i] = cited

		if p := matchPrior(f, in.prior); p >= 0 {
			out.reRaiseOf[i] = p
			matched[p] = true
		}

		if !f.Severity.Blocking() {
			// Medium and below never gated; saying so on the finding keeps a
			// reader from wondering why an open "medium" did not block.
			f.Advisory = true
			continue
		}
		if f.Advisory {
			continue
		}
		if in.rules.GateSeverity == "critical" && f.Severity != domain.SeverityCritical {
			demote(f, demoteBelowGate, "advisory: review.convergence.gate_severity is critical, so a high finding does not block")
			continue
		}
		if in.rules.CitationRequired() && cited == "" {
			demote(f, demoteUncited, "advisory: cites no active requirement (or the idea, with a quote), so it cannot block -- an uncited finding is scope expansion")
			continue
		}
		if p, ok := out.reRaiseOf[i]; ok {
			pf := in.prior[p]
			if pf.Addressed && f.Justification == "" {
				demote(f, demoteUnjustifiedReRaise, fmt.Sprintf("advisory: re-raises %s, which the revision addressed, without saying why the revision did not resolve it", pf.Label))
				continue
			}
			if f.Justification != "" {
				f.Note = joinNote(f.Note, fmt.Sprintf("re-raises %s: %s", pf.Label, f.Justification))
			} else {
				f.Note = joinNote(f.Note, fmt.Sprintf("re-raises %s, which the revision did not address", pf.Label))
			}
			continue
		}
		if !in.later {
			continue
		}
		switch in.rules.LaterFindings {
		case "critical":
			if f.Severity != domain.SeverityCritical {
				demote(f, demoteLaterNotCritical, "advisory: new after the first cycle, and review.convergence.later_findings requires a later-cycle blocker to be critical")
			}
		case "changed":
			if f.Severity == domain.SeverityCritical || in.changed == nil {
				continue
			}
			if !in.changed[cited] {
				demote(f, demoteLaterUnchanged, "advisory: new after the first cycle, about text the revision did not change -- it should have been raised when that text was first reviewed")
			}
		}
	}
	for p := range in.prior {
		if !matched[p] {
			out.resolved = append(out.resolved, p)
		}
	}
	return out
}

// resolveCitation returns the normalized code a finding validly cites --
// an active requirement's code, or citeIdea for a gap backed by a quote from
// the idea -- or "" when it cites nothing that holds up. A finding citing a
// requirement is re-targeted at it, which is also what the UI keys "which
// requirement is this about" on.
//
// The explicit field wins; without one (an older prompt, a scripted
// response) the first active REQ code the finding or its evidence mentions
// counts, since a finding that names REQ-3 in its own text is about REQ-3
// whether or not it filled in a field.
func resolveCitation(f *domain.Finding, byCode map[string]domain.Requirement, taskCodes map[string]bool, outcomeRefs map[string]NodeRef, idea string) string {
	cite := NormalizeRequirementRef(f.Cites)
	if cite == citeIdea {
		if quotesIdea(f.Evidence, idea) || quotesIdea(f.Finding, idea) {
			return citeIdea
		}
		return ""
	}
	try := func(c string) bool {
		if c == "" {
			return false
		}
		if r, ok := byCode[c]; ok {
			f.TargetType, f.TargetID = "requirement", r.ID
			return true
		}
		if ref, ok := outcomeRefs[c]; ok {
			f.TargetType, f.TargetID = ref.Type, ref.ID
			return true
		}
		return taskCodes[c]
	}
	if try(cite) {
		return cite
	}
	for _, c := range citableCodeRe.FindAllString(f.Finding+" "+f.Evidence, -1) {
		if c = NormalizeRequirementRef(c); try(c) {
			return c
		}
	}
	return ""
}

var citableCodeRe = regexp.MustCompile(`(?i)\b(?:REQ|TASK|OBJ|MET)-\d+\b`)

// codeRe strips requirement, task, objective and metric codes before two
// findings are compared:
// a revision that supersedes REQ-1 with REQ-4 re-raises the same concern
// under a different code, and the code must not be what tells them apart.
var codeRe = regexp.MustCompile(`(?i)\b(REQ|TASK|DEC|OBJ|MET|F|D)-?\d+\b`)

// quotesIdea reports whether text substantially quotes the idea: at least
// three significant tokens, at least 60% of them present in the idea. A
// "the idea asks for X" finding must show where it asks; otherwise "idea" is
// a citation anything could claim.
func quotesIdea(text, idea string) bool {
	tt := tokenize(text)
	if len(tt) < 3 {
		return false
	}
	ideaSet := map[string]bool{}
	for _, t := range tokenize(idea) {
		ideaSet[t] = true
	}
	seen := map[string]bool{}
	in, total := 0, 0
	for _, t := range tt {
		if seen[t] {
			continue
		}
		seen[t] = true
		total++
		if ideaSet[t] {
			in++
		}
	}
	return total >= 3 && float64(in)/float64(total) >= 0.6
}

// matchPrior returns the index of the prior finding f re-raises, or -1. An
// explicit `reraises` label wins; otherwise the most similar prior finding
// above the threshold (dedup.go's jaccardSimilarity, codes stripped).
func matchPrior(f *domain.Finding, prior []priorFinding) int {
	if len(prior) == 0 {
		return -1
	}
	if label := strings.ToUpper(strings.TrimSpace(f.ReRaises)); label != "" {
		for i, p := range prior {
			if strings.EqualFold(p.Label, label) {
				return i
			}
		}
	}
	best, bestSim := -1, 0.0
	text := stripCodes(f.Finding)
	for i, p := range prior {
		sim := jaccardSimilarity(text, stripCodes(p.Text))
		need := reRaiseAnyCategory
		if strings.EqualFold(strings.TrimSpace(p.Category), strings.TrimSpace(f.Category)) {
			need = findingDedupeThreshold
		}
		if sim >= need && sim > bestSim {
			best, bestSim = i, sim
		}
	}
	return best
}

func stripCodes(s string) string { return codeRe.ReplaceAllString(s, " ") }

func joinNote(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "; " + add
}

// requirementFingerprint is what "did the revision change this requirement"
// compares: its statement and every acceptance criterion, in order.
func requirementFingerprint(r domain.Requirement) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(r.Statement))
	for _, ac := range r.AcceptanceCriteria {
		fmt.Fprintf(&b, "\x00%s\x00%s\x00%s", ac.Metric, ac.Target, ac.Condition)
	}
	return b.String()
}

// changedRequirements compares the active requirements before and after a
// revision and returns the codes (normalized) of every active requirement
// the revision added or changed. It also returns the codes of requirements
// that were active before and are gone now -- a superseded requirement was
// certainly "done something about".
func changedRequirements(before, after []domain.Requirement) (changed map[string]bool, removed map[string]bool) {
	prev := make(map[string]string, len(before))
	for _, r := range before {
		prev[r.ID] = requirementFingerprint(r)
	}
	changed = map[string]bool{}
	still := map[string]bool{}
	for _, r := range after {
		still[r.ID] = true
		fp, existed := prev[r.ID]
		if !existed || fp != requirementFingerprint(r) {
			changed[NormalizeRequirementRef(r.Code)] = true
		}
	}
	removed = map[string]bool{}
	for _, r := range before {
		if !still[r.ID] {
			removed[NormalizeRequirementRef(r.Code)] = true
		}
	}
	return changed, removed
}

// gatingPriors turns one cycle's gating adversarial findings (persisted, so
// they carry IDs) into the prior list the next cycle sees, labelled F1..Fn in
// the order given.
func gatingPriors(findings []domain.Finding, cites []string) []priorFinding {
	out := make([]priorFinding, 0, len(findings))
	for i, f := range findings {
		cite := ""
		if i < len(cites) {
			cite = cites[i]
		}
		out = append(out, priorFinding{
			Label: fmt.Sprintf("F%d", i+1), ID: f.ID, Severity: f.Severity, Category: f.Category,
			Text: f.Finding, Evidence: f.Evidence, Cites: cite,
		})
	}
	return out
}

// markAddressed sets Addressed/Response on each prior finding from what the
// revision did: the reviser answered it by label, or the requirement it cited
// was changed or removed. A finding that cited nothing (or the idea) can only
// be addressed by an explicit response -- there is no requirement to diff.
func markAddressed(prior []priorFinding, responses map[string]string, changed, removed map[string]bool) {
	for i := range prior {
		p := &prior[i]
		if r := strings.TrimSpace(responses[strings.ToUpper(p.Label)]); r != "" {
			p.Addressed, p.Response = true, r
		}
		if p.Cites != "" && p.Cites != citeIdea && (changed[p.Cites] || removed[p.Cites]) {
			p.Addressed = true
		}
	}
}

// renderPriorFindings is the reviewer's view of the previous cycle (root
// cause 1): each gating finding by label, what it cited, and what the
// revision did about it, followed by the rules this cycle is judged under.
func renderPriorFindings(prior []priorFinding, changed map[string]bool, rules config.ConvergenceConfig) string {
	var b strings.Builder
	b.WriteString("These were the blocking findings of the previous review cycle, and what the revision since did about each:\n")
	for _, p := range prior {
		fmt.Fprintf(&b, "- %s [%s] (%s) %s", p.Label, p.Severity, citeLabel(p.Cites), p.Text)
		switch {
		case p.Response != "":
			fmt.Fprintf(&b, "\n  Revision's response: %s", p.Response)
		case p.Addressed:
			b.WriteString("\n  Revision: the cited requirement was changed or replaced.")
		default:
			b.WriteString("\n  Revision: not addressed.")
		}
		b.WriteString("\n")
	}
	if changed != nil {
		codes := sortedSet(changed)
		if len(codes) == 0 {
			b.WriteString("Requirements the revision added or changed: none.\n")
		} else {
			fmt.Fprintf(&b, "Requirements the revision added or changed: %s.\n", strings.Join(codes, ", "))
		}
	}
	b.WriteString("\nRules for this cycle:\n")
	b.WriteString("- Do not raise a prior finding again if the revision resolved it.\n")
	b.WriteString("- If one is still present, raise it again with \"reraises\" set to its label and, when the revision addressed it, a \"justification\" saying specifically why the revision did not resolve it. A re-raise of an addressed finding with no justification is recorded as advisory.\n")
	switch rules.LaterFindings {
	case "changed":
		b.WriteString("- A NEW critical/high finding blocks only if it is about a requirement the revision added or changed, or is critical. Anything about text you already reviewed unchanged should have been raised then, and is recorded as advisory.\n")
	case "critical":
		b.WriteString("- A NEW finding blocks only if it is critical; anything less is recorded as advisory.\n")
	}
	return b.String()
}

// reviewerConvergencePolicy is the standing instruction every guarded-mode
// adversarial review gets, first cycle included: cite what you are judging,
// and know what severity means here. The citation rule is stated to the
// reviewer, not just enforced on it -- a reviewer that does not know a finding
// must cite a requirement will keep writing uncited ones and see them
// silently not block.
func reviewerConvergencePolicy(rules config.ConvergenceConfig, subject string) string {
	var b strings.Builder
	if rules.CitationRequired() {
		fmt.Fprintf(&b, "Set \"requirement\" on every finding to the code it is about (%s). "+
			"For something the idea asks for that nothing covers, set it to \"idea\" and quote the idea in evidence. "+
			"A critical/high finding that cites neither cannot block: review what is in front of you, do not expand scope.\n", subject)
	}
	if rules.GateSeverity == "critical" {
		b.WriteString("Only critical findings block approval in this project; use high for what should be fixed but need not stop the work.\n")
	} else {
		b.WriteString("Only critical and high findings block approval; use medium or lower for anything that should not stop the work.\n")
	}
	return b.String()
}

// renderReviewFindingsForReviser is the reviser's precise brief (root cause
// 4): only the findings gating THIS cycle, each with its label, citation,
// evidence and recommendation, so it can act on each and answer it by label.
func renderReviewFindingsForReviser(det []domain.Finding, prior []priorFinding, all []domain.Finding) string {
	var b strings.Builder
	b.WriteString("The review blocked on the findings below. Address each one by changing the requirement it cites ")
	b.WriteString("(or adding one, if it is a gap the idea asks for), and answer each in review_responses by its label. ")
	b.WriteString("Do not add requirements that neither the idea nor these findings call for.\n")
	for i, f := range det {
		fmt.Fprintf(&b, "- D%d [%s] %s\n  Evidence: %s\n  Recommendation: %s\n", i+1, f.Severity, f.Finding, f.Evidence, f.Recommendation)
	}
	byID := make(map[string]domain.Finding, len(all))
	for _, f := range all {
		byID[f.ID] = f
	}
	for _, p := range prior {
		f := byID[p.ID]
		fmt.Fprintf(&b, "- %s [%s] (%s) %s\n", p.Label, p.Severity, citeLabel(p.Cites), p.Text)
		if f.Evidence != "" {
			fmt.Fprintf(&b, "  Evidence: %s\n", f.Evidence)
		}
		if f.Recommendation != "" {
			fmt.Fprintf(&b, "  Recommendation: %s\n", f.Recommendation)
		}
	}
	return b.String()
}

func citeLabel(cite string) string {
	switch cite {
	case "":
		return "cites nothing"
	case citeIdea:
		return "cites the idea"
	default:
		return "cites " + cite
	}
}

// reviewResponses reads the reviser's review_responses into label -> text,
// labels upper-cased so "f1" and "F1" agree.
func reviewResponses(obj map[string]any) map[string]string {
	out := map[string]string{}
	for _, r := range objSlice(obj["review_responses"]) {
		label := strings.ToUpper(strings.TrimSpace(str(r["finding"])))
		if label != "" {
			out[label] = strings.TrimSpace(str(r["response"]))
		}
	}
	return out
}

// newReviewTelemetry starts one cycle's record; the caller fills the counts
// that need the persisted findings and the outcome.
func newReviewTelemetry(review string, cycle int, rules config.ConvergenceConfig, rigor RigorLevel) domain.ReviewTelemetry {
	return domain.ReviewTelemetry{
		Review: review, Cycle: cycle, Mode: rules.Mode, Rigor: string(rigor),
		Raised: map[domain.Severity]int{}, New: map[domain.Severity]int{},
		ReRaised: map[domain.Severity]int{}, Resolved: map[domain.Severity]int{},
		Demoted: map[string]int{}, PrevOpenBlocking: -1,
	}
}

// countCycle fills the per-severity counts of t from one cycle's adversarial
// findings and its classification.
func countCycle(t *domain.ReviewTelemetry, adv []domain.Finding, cls classification, prior []priorFinding) {
	for i, f := range adv {
		t.Raised[f.Severity]++
		if _, ok := cls.reRaiseOf[i]; ok {
			t.ReRaised[f.Severity]++
		} else {
			t.New[f.Severity]++
		}
	}
	for _, p := range cls.resolved {
		t.Resolved[prior[p].Severity]++
	}
	for k, v := range cls.demoted {
		t.Demoted[k] += v
	}
}

// countGating counts the findings that gate approval: non-advisory,
// blocking severity, not resolved by a disposition (EvaluateApproval's rule).
func countGating(findings []domain.Finding) int {
	n := 0
	for _, f := range findings {
		if !f.Advisory && f.Severity.Blocking() && !f.ResolvesBlocking() {
			n++
		}
	}
	return n
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
