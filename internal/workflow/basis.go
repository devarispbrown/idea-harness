package workflow

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// The assertion basis: which facts, assumptions and hypotheses a requirement
// or decision rests on. Framing and research record assertions with stable
// ASM-n codes; the PRD cites them in `based_on`, and the engine writes each
// citation as requirement|decision --derived_from--> assertion. Without these
// edges an assertion had no dependents and a decision no basis, so the gap
// rules that read them (unvalidated_assumption's ranking and `refuted` state,
// invalidated_decision's `basis_refuted`) could never fire on a real run.

// liveAssertions is the initiative's assertions a model may cite: not
// superseded by a retry. Retracted and disputed ones are kept -- a model
// should see that a claim was refuted, and the reviewer flags requirements
// still resting on one.
func (e *Engine) liveAssertions(ctx context.Context, initiativeID string) []domain.Assertion {
	all, err := e.db.ListAssertions(ctx, initiativeID)
	if err != nil {
		e.log.Warn("could not list assertions for the prompt", "initiative", initiativeID, "error", err)
		return nil
	}
	out := make([]domain.Assertion, 0, len(all))
	for _, a := range all {
		if a.SupersededBy == "" && a.Status != domain.AssertionSuperseded && a.Code != "" {
			out = append(out, a)
		}
	}
	return out
}

// normalizeAssertionCode accepts "asm-3", " ASM-3 " and "ASM-3: …" alike.
func normalizeAssertionCode(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if i := strings.IndexAny(s, ": ("); i > 0 {
		s = s[:i]
	}
	return s
}

// assertionIDsByCode indexes assertions by normalized code.
func assertionIDsByCode(asserts []domain.Assertion) map[string]string {
	m := make(map[string]string, len(asserts))
	for _, a := range asserts {
		m[normalizeAssertionCode(a.Code)] = a.ID
	}
	return m
}

// resolveAssertionCodes maps cited codes to ids. An unknown code (a typo, a
// superseded assertion, an invented one) is returned separately and skipped:
// a citation is best-effort, like a requirement's `decision` reference, and
// never fails the stage that already paid for the model call.
func resolveAssertionCodes(refs []string, byCode map[string]string) (ids, unknown []string) {
	seen := map[string]bool{}
	for _, ref := range refs {
		code := normalizeAssertionCode(ref)
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		if id, ok := byCode[code]; ok {
			ids = append(ids, id)
		} else {
			unknown = append(unknown, ref)
		}
	}
	return ids, unknown
}

// renderAssertions lists assertions for a prompt, one per line, by code. When
// restedOn is non-nil, each line also names the requirements resting on it,
// and only assertions something rests on are listed -- the reviewer's view.
func renderAssertions(asserts []domain.Assertion, restedOn map[string][]string) string {
	var b strings.Builder
	for _, a := range asserts {
		var reqs []string
		if restedOn != nil {
			if reqs = restedOn[a.ID]; len(reqs) == 0 {
				continue
			}
		}
		fmt.Fprintf(&b, "- %s (%s, %s", a.Code, a.Type, a.Status)
		if a.Confidence != "" {
			fmt.Fprintf(&b, ", confidence %s", a.Confidence)
		}
		fmt.Fprintf(&b, "): %s", domain.TrimToWordBoundary(a.Statement, 200))
		if len(reqs) > 0 {
			fmt.Fprintf(&b, " -- rested on by %s", strings.Join(reqs, ", "))
		}
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		return "(none)"
	}
	return b.String()
}

// recordAssertionBasis writes one PRD cycle's based_on citations as edges.
//
// A requirement's basis is replaced: every requirement this cycle produced is
// in byReq (an empty list included), and a requirement revised in place
// restates what it rests on, so a citation the revision dropped is dropped
// from the graph too. A superseded requirement keeps its edges -- it is no
// longer live, so gap analysis already ignores it, and its history still says
// what it rested on. A decision's basis is only ever added to: a re-listed
// decision merges into the existing row, and the basis it was decided on is
// still its basis.
func (e *Engine) recordAssertionBasis(ctx context.Context, initiativeID string, asserts []domain.Assertion, byReq, byDec map[string][]string) error {
	byCode := assertionIDsByCode(asserts)
	// What each node rests on already, so an unchanged basis writes nothing:
	// no edge churn and no basis_set event per requirement per cycle.
	have := map[string]map[string]bool{}
	edges, err := e.db.ListGraphEdges(ctx, initiativeID)
	if err != nil {
		return err
	}
	for _, ed := range edges {
		if ed.EdgeType == domain.EdgeDerivedFrom && ed.ToType == "assertion" {
			if have[ed.FromID] == nil {
				have[ed.FromID] = map[string]bool{}
			}
			have[ed.FromID][ed.ToID] = true
		}
	}
	same := func(nodeID string, ids []string) bool {
		if len(have[nodeID]) != len(ids) {
			return false
		}
		for _, id := range ids {
			if !have[nodeID][id] {
				return false
			}
		}
		return true
	}
	for reqID, refs := range byReq {
		ids, unknown := resolveAssertionCodes(refs, byCode)
		if len(unknown) > 0 {
			e.log.Debug("requirement based_on cited unknown assertion codes; skipped", "initiative", initiativeID, "requirement", reqID, "codes", unknown)
		}
		if same(reqID, ids) {
			continue
		}
		if err := e.db.SetAssertionBasis(ctx, initiativeID, "requirement", reqID, ids, true); err != nil {
			return err
		}
	}
	for decID, refs := range byDec {
		ids, unknown := resolveAssertionCodes(refs, byCode)
		if len(unknown) > 0 {
			e.log.Debug("decision based_on cited unknown assertion codes; skipped", "initiative", initiativeID, "decision", decID, "codes", unknown)
		}
		var fresh []string
		for _, id := range ids {
			if !have[decID][id] {
				fresh = append(fresh, id)
			}
		}
		if len(fresh) == 0 {
			continue
		}
		ids = fresh
		if err := e.db.SetAssertionBasis(ctx, initiativeID, "decision", decID, ids, false); err != nil {
			return err
		}
	}
	// A node whose basis was unchanged skipped SetAssertionBasis, and with it
	// the invalidates reconcile; a new requirement resting on an already
	// retracted assertion must still be marked invalidated.
	return e.db.SyncAssertionInvalidations(ctx, initiativeID)
}

// requirementBasis returns, for each live assertion some active requirement
// rests on (requirement --derived_from--> assertion), those requirements'
// codes in order.
func (e *Engine) requirementBasis(ctx context.Context, initiativeID string, reqs []domain.Requirement) map[string][]string {
	codeByReq := make(map[string]string, len(reqs))
	for _, r := range reqs {
		codeByReq[r.ID] = r.Code
	}
	edges, err := e.db.ListGraphEdges(ctx, initiativeID)
	if err != nil {
		return nil
	}
	out := map[string][]string{}
	for _, ed := range edges {
		if ed.FromType != "requirement" || ed.EdgeType != domain.EdgeDerivedFrom || ed.ToType != "assertion" {
			continue
		}
		if code, ok := codeByReq[ed.FromID]; ok {
			out[ed.ToID] = append(out[ed.ToID], code)
		}
	}
	for k := range out {
		sort.Slice(out[k], func(i, j int) bool { return requirementCodeLess(out[k][i], out[k][j]) })
	}
	return out
}

// requirementCodeLess orders REQ-2 before REQ-10.
func requirementCodeLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

// reviewerBasisPolicy is the reviewer's instruction for the assertions input:
// a requirement resting on a hypothesis or assumption nobody validated is a
// legitimate concern, but a bounded one -- medium, and cited, so the
// convergence rules can track it like any other requirement finding.
const reviewerBasisPolicy = "Assertions below are the facts, assumptions and hypotheses requirements rest on, with their validation status. " +
	"A requirement resting on a hypothesis or assumption that is still candidate, disputed or retracted may be flagged: " +
	"severity medium, category \"basis\", the requirement field set to that requirement's code, evidence naming the ASM code and its status, " +
	"and a recommendation to validate the assertion or make the requirement hold without it. Do not flag a requirement for resting on an accepted assertion or a fact."
