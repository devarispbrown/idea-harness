// Package projection computes the user-facing projections — Current
// Understanding (§5), Recommended Next Move (§6), and What Changed (§7) —
// deterministically from relational state. They are projections, not a source
// of truth, and every statement retains provenance.
package projection

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// Understanding is the Current Understanding projection.
type Understanding struct {
	Title         string                  `json:"title"`
	Description   string                  `json:"description"`
	Status        domain.InitiativeStatus `json:"status"`
	FailureReason string                  `json:"failure_reason,omitempty"`
	Summary       string                  `json:"summary"`
	NeedsJudgment []string                `json:"needs_judgment"`
	Assertions    []UnderstandingItem     `json:"assertions"`
	Requirements  []RequirementView       `json:"requirements"`
	Decisions     []DecisionView          `json:"decisions"`
	Questions     []QuestionView          `json:"questions"`
}

// UnderstandingItem is one provenance-backed statement.
type UnderstandingItem struct {
	ID            string                 `json:"id"`
	Type          domain.AssertionType   `json:"type"`
	Status        domain.AssertionStatus `json:"status"`
	Statement     string                 `json:"statement"`
	Confidence    domain.Confidence      `json:"confidence"`
	Source        string                 `json:"source"`
	Provenance    []domain.ProvenanceRef `json:"provenance"`
	ProducedByRun string                 `json:"produced_by_run"`
}

// RequirementView is a requirement in the projection.
type RequirementView struct {
	ID                 string                       `json:"id"`
	Code               string                       `json:"code"`
	Statement          string                       `json:"statement"`
	Priority           domain.RequirementPriority   `json:"priority"`
	Status             domain.RequirementStatus     `json:"status"`
	AcceptanceCriteria []domain.AcceptanceCriterion `json:"acceptance_criteria"`
}

// DecisionView is a decision in the projection (with dissent presence).
type DecisionView struct {
	ID             string                `json:"id"`
	Code           string                `json:"code"`
	Question       string                `json:"question"`
	Recommendation string                `json:"recommendation"`
	Options        []string              `json:"options"`
	Resolution     string                `json:"resolution,omitempty"`
	Status         domain.DecisionStatus `json:"status"`
	HasDissent     bool                  `json:"has_dissent"`
	Dissents       []DissentView         `json:"dissents"`
	Synthesis      string                `json:"synthesis,omitempty"`
}

// DissentView is one dissenting position on a decision (§8).
type DissentView struct {
	ProponentRole string `json:"proponent_role"`
	Position      string `json:"position"`
}

// QuestionView is an open question in the projection.
type QuestionView struct {
	ID       string                `json:"id"`
	Question string                `json:"question"`
	Status   domain.QuestionStatus `json:"status"`
}

// Guidance is the Recommended Next Move projection.
type Guidance struct {
	Action   string   `json:"action"`
	Why      string   `json:"why"`
	Affected []string `json:"affected"`
	// NextTask is the task Execute would run next, omitted when there is none
	// (the Build contract's `next_task`, reusing workflow's nextReadyTask).
	// Omitted rather than null: every other daemon endpoint's contract is
	// "never emit a null field" (TestNoEndpointEmitsANullArray), and this is
	// the same rule applied to a single optional object instead of a slice.
	// BuildGuidance leaves this nil: it stays a pure function of Understanding
	// with no engine/task-store access, so the daemon handler populates it
	// after calling BuildGuidance (via Engine.NextReadyTask).
	NextTask *NextTaskRef `json:"next_task,omitempty"`
	// RunBlockedReason is empty (and so omitted) when Execute could start
	// right now for this initiative, otherwise a user-facing sentence naming
	// the fix (the Build contract's `run_blocked_reason`): another change set
	// still open, an operation already running, the plan not approved yet, or
	// -- composed in by the daemon handler, not here -- the executor not
	// being ready. Same population note as NextTask.
	RunBlockedReason string `json:"run_blocked_reason,omitempty"`
}

// NextTaskRef is the Build contract's `next_task` shape: enough to name and
// link the task without exposing the full domain.Task.
type NextTaskRef struct {
	ID      string `json:"id"`
	HumanID string `json:"human_id"`
	Title   string `json:"title"`
}

// BuildUnderstanding assembles the Current Understanding projection.
func BuildUnderstanding(ctx context.Context, db *store.DB, ini domain.Initiative) (Understanding, error) {
	// Every slice starts non-nil so an initiative with nothing attached marshals
	// as [] rather than null. A bare initiative is the normal first state, not an
	// edge case, and a client should not have to null-check five fields to render
	// it -- nor should a generated client whose schema says `type: array` reject it.
	u := Understanding{
		Title: ini.Title, Description: ini.Description, Status: ini.Status, FailureReason: ini.FailureReason,
		NeedsJudgment: []string{},
		Assertions:    []UnderstandingItem{},
		Requirements:  []RequirementView{},
		Decisions:     []DecisionView{},
		Questions:     []QuestionView{},
	}

	assertions, err := db.ListAssertions(ctx, ini.ID)
	if err != nil {
		return u, err
	}
	for _, a := range assertions {
		u.Assertions = append(u.Assertions, UnderstandingItem{
			ID: a.ID, Type: a.Type, Status: a.Status, Statement: a.Statement,
			Confidence: a.Confidence, Source: a.Source, Provenance: a.Provenance, ProducedByRun: a.ProducedByRun,
		})
	}

	reqs, err := db.ListRequirements(ctx, ini.ID)
	if err != nil {
		return u, err
	}
	for _, r := range reqs {
		if r.Status == domain.RequirementRemoved {
			continue
		}
		// orEmpty on each nested slice, for the same reason as the top-level
		// ones: a requirement with no criteria yet is ordinary, and a client
		// should not have to tell null from [] to render it.
		u.Requirements = append(u.Requirements, RequirementView{
			ID: r.ID, Code: r.Code, Statement: r.Statement, Priority: r.Priority,
			Status: r.Status, AcceptanceCriteria: orEmpty(r.AcceptanceCriteria),
		})
	}

	decs, err := db.ListDecisions(ctx, ini.ID)
	if err != nil {
		return u, err
	}
	for _, d := range dedupDecisionsForDisplay(decs) {
		hasDissent, err := db.HasDissent(ctx, d.ID)
		if err != nil {
			return u, err
		}
		dv := DecisionView{
			ID: d.ID, Code: d.Code, Question: d.Question, Recommendation: d.Recommendation,
			Options: orEmpty(d.Options), Resolution: d.Resolution,
			Status: d.Status, HasDissent: hasDissent,
			Dissents: []DissentView{},
		}
		if hasDissent {
			ds, err := db.ListDissents(ctx, d.ID)
			if err != nil {
				return u, err
			}
			for _, x := range ds {
				dv.Dissents = append(dv.Dissents, DissentView{ProponentRole: x.ProponentRole, Position: x.Position})
			}
			// The orchestrator convergence synthesizes the dissents into the
			// decision's recommendation; surface that as the synthesis.
			dv.Synthesis = d.Recommendation
		}
		u.Decisions = append(u.Decisions, dv)
	}

	questions, err := db.ListQuestions(ctx, ini.ID)
	if err != nil {
		return u, err
	}
	for _, q := range questions {
		u.Questions = append(u.Questions, QuestionView{ID: q.ID, Question: q.Question, Status: q.Status})
	}
	u.Summary = summarize(&u)
	u.NeedsJudgment = judgmentItems(&u)
	return u, nil
}

// normalizeDecisionQuestion returns a canonical form for comparing decision
// questions across revise cycles (D3): a model re-listing a prior decision
// verbatim often prefixes it with "[Source: ...]", and casing/whitespace
// otherwise identical questions must still collapse onto one row.
//
// Mirrors workflow.normalizeJudgment's rule (same prefix-strip, same
// case/whitespace fold), kept as its own copy here rather than exported from
// workflow: the store stays the single source of truth and this is a
// display-only rule, so projection must not gain a dependency on workflow to
// get it.
// SameQuestion reports whether two decision questions are the same question
// for display: equal after normalizeDecisionQuestion.
func SameQuestion(a, b string) bool {
	return normalizeDecisionQuestion(a) == normalizeDecisionQuestion(b)
}

func normalizeDecisionQuestion(text string) string {
	s := strings.TrimSpace(text)
	for strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 {
			break
		}
		s = strings.TrimSpace(s[end+1:])
	}
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	return strings.TrimRight(s, "?!.;: ")
}

// dedupDecisionsForDisplay collapses decisions that ask the same normalized
// question down to one (D3).
//
// A revise cycle can re-list a prior decision verbatim or lightly reworded, so
// before this every re-listing was its own row and answering a question once
// still left it showing as open N more times.
//
// Precedence: the newest UNRESOLVED decision for a question always wins, even
// over a newer RESOLVED one for the same question -- an unresolved decision
// must never be hidden behind a resolved one asking the same thing. Only once
// every decision for a question is resolved does the newest resolved one
// show.
//
// decs must be ordered oldest-first, which is ListDecisions' own contract
// (`ORDER BY created_at ASC`); that ordering is what lets one forward pass
// track "newest" without a separate timestamp compare, and callers other than
// BuildUnderstanding must preserve it too.
func dedupDecisionsForDisplay(decs []domain.Decision) []domain.Decision {
	return DedupDecisions(decs)
}

// DedupDecisions is dedupDecisionsForDisplay for callers outside the package:
// the decisions endpoint the UI reads applies it too, so the page and the
// guidance agree on how many decisions there are.
func DedupDecisions(decs []domain.Decision) []domain.Decision {
	chosen := map[string]domain.Decision{}
	order := make([]string, 0, len(decs))
	for _, d := range decs {
		key := normalizeDecisionQuestion(d.Question)
		existing, ok := chosen[key]
		if !ok {
			chosen[key] = d
			order = append(order, key)
			continue
		}
		if existing.Status == domain.DecisionOpen && d.Status != domain.DecisionOpen {
			// existing (older) is unresolved, d (newer) is resolved: keep the
			// unresolved one -- never hide it behind a resolved one.
			continue
		}
		// Either d is unresolved (always wins over whatever came before), or
		// both are resolved (d is newer, since decs is ascending, so it wins
		// the "newest resolved" case).
		chosen[key] = d
	}
	out := make([]domain.Decision, 0, len(order))
	for _, key := range order {
		out = append(out, chosen[key])
	}
	return out
}

// summarize renders a deterministic, provenance-backed prose summary (§5).
// It is assembled from entity state — never LLM narration, never a separate
// source of truth.
func summarize(u *Understanding) string {
	var b strings.Builder

	title := strings.TrimSpace(u.Title)
	if title == "" {
		title = "your idea"
	}
	// The title is the user's own idea statement — often already a verb phrase
	// like "Build a platform that…" — so avoid the awkward "We're building Build
	// a platform…" duplication.
	if verbPhrase(title) {
		fmt.Fprintf(&b, "%s.", title)
	} else {
		fmt.Fprintf(&b, "We're building %s.", title)
	}
	if d := strings.TrimSpace(u.Description); d != "" && d != title {
		fmt.Fprintf(&b, " %s.", strings.TrimSuffix(d, "."))
	}

	if n := len(u.Requirements); n > 0 {
		must := 0
		for _, r := range u.Requirements {
			if r.Priority == domain.PriorityMust {
				must++
			}
		}
		b.WriteString(fmt.Sprintf(" It has %d requirement(s)", n))
		if must > 0 {
			b.WriteString(fmt.Sprintf(" (%d mandatory)", must))
		}
		b.WriteString(".")
	}

	openDecisions := 0
	for _, d := range u.Decisions {
		if d.Status == domain.DecisionOpen {
			openDecisions++
		}
	}
	if openDecisions > 0 {
		b.WriteString(fmt.Sprintf(" %d decision(s) await your judgment.", openDecisions))
	}

	openQuestions := 0
	for _, q := range u.Questions {
		if q.Status == domain.QuestionOpen {
			openQuestions++
		}
	}
	if openQuestions > 0 {
		b.WriteString(fmt.Sprintf(" %d open question(s) remain.", openQuestions))
	}

	if n := len(u.Assertions); n > 0 {
		facts := 0
		assumptions := 0
		for _, a := range u.Assertions {
			switch a.Type {
			case domain.AssertionFact:
				facts++
			case domain.AssertionAssumption:
				assumptions++
			}
		}
		b.WriteString(fmt.Sprintf(" Research recorded %d assertion(s)", n))
		if facts > 0 || assumptions > 0 {
			b.WriteString(fmt.Sprintf(" (%d facts, %d assumptions)", facts, assumptions))
		}
		b.WriteString(".")
	}

	return b.String()
}

// verbPhrase reports whether the idea text already begins with a verb, so the
// summary avoids prefixing "We're building".
func verbPhrase(title string) bool {
	fields := strings.Fields(title)
	if len(fields) == 0 {
		return false
	}
	switch strings.ToLower(fields[0]) {
	case "build", "create", "make", "design", "add", "allow", "enable", "launch",
		"write", "develop", "let", "help", "give", "provide":
		return true
	}
	return false
}

// judgmentItems lists open decisions and open questions that need the user's
// attention (§8, §55).
func judgmentItems(u *Understanding) []string {
	out := []string{} // non-nil: the caller assigns it straight to a JSON field
	for _, d := range u.Decisions {
		if d.Status == domain.DecisionOpen {
			out = append(out, d.Code+": "+d.Question)
		}
	}
	for _, q := range u.Questions {
		if q.Status == domain.QuestionOpen {
			out = append(out, "Question: "+q.Question)
		}
	}
	return out
}

// BuildGuidance computes the Recommended Next Move with deterministic
// precedence: judgment items outrank stage advancement (§6, §9).
func BuildGuidance(ini domain.Initiative, understanding Understanding) Guidance {
	openQuestions := 0
	for _, q := range understanding.Questions {
		if q.Status == domain.QuestionOpen {
			openQuestions++
		}
	}
	openDecisions := 0
	for _, d := range understanding.Decisions {
		if d.Status == domain.DecisionOpen {
			openDecisions++
		}
	}

	switch ini.Status {
	case domain.InitiativeNeedsHumanJudgment:
		// One message for six causes said "the PRD" for four of them. `idea
		// status` is the first thing anyone runs on a stuck initiative, so it
		// naming the wrong artifact -- and no command at all -- is half the
		// dead end. The reason is recorded now; use it.
		switch ini.JudgmentReason {
		case domain.JudgmentPlanDependencies:
			return Guidance{Action: "Regenerate the execution plan",
				Why:      "The plan has tasks whose dependencies were never satisfied; approving it would lock in an incomplete graph. Run `idea revise-plan`.",
				Affected: []string{"Execution Plan"}}
		case domain.JudgmentPlanReview:
			return Guidance{Action: "Resolve blocking plan-review findings",
				Why:      "The plan review did not converge. `idea reviews` lists the findings; `idea revise-plan` regenerates the plan, and `idea approve-plan` accepts it as it stands.",
				Affected: []string{"Execution Plan", "Review"}}
		case domain.JudgmentVerification:
			return Guidance{Action: "Re-record verification evidence",
				Why:      "A requirement was failed after this initiative completed, so it is no longer complete. Run `idea verify`.",
				Affected: []string{"Verify"}}
		}
		return Guidance{Action: "Resolve blocking review findings",
			Why:      "Adversarial review did not converge; the PRD is paused for your judgment. `idea reviews` lists the findings; `idea approve` accepts the PRD, `idea revise` regenerates it.",
			Affected: []string{"Product Definition", "Review"}}
	case domain.InitiativeApproved:
		return Guidance{Action: "Proceed to design & execution plan", Why: "The reviewed PRD is ready. Advance to architecture, product design, verification, and an execution plan.", Affected: []string{"Design", "Execution Plan"}}
	case domain.InitiativePlanApproved:
		// B5: this used to say "Implementation and ChangeSets arrive in
		// Milestone 3" -- true when it was written, false since Milestone 3
		// shipped. Execute/apply/verify are live; say what to actually do.
		return Guidance{Action: "Execution plan approved", Why: "The reviewed plan is ready to build. Run the next task to produce a change set for review.", Affected: []string{"Execution Plan"}}
	case domain.InitiativeFailed:
		why := "Check the logs, fix the cause, then retry with `idea new`."
		if reason := strings.TrimSpace(ini.FailureReason); reason != "" {
			if len(reason) > 240 {
				reason = reason[:240] + "…"
			}
			why = reason
		}
		return Guidance{Action: "The workflow failed", Why: why, Affected: []string{"Workflow"}}
	case domain.InitiativeComplete:
		// Terminal state: the judgment-item rules below must not run. `idea
		// status` printed "● complete" and then "→ Resolve open questions",
		// which reads as unfinished work on an initiative that verified and
		// ended. Items left open on a complete initiative are part of the
		// record, not a next step, so they are described that way.
		return Guidance{Action: "Nothing to do — this initiative is complete",
			Why:      completeWhy(openDecisions, openQuestions),
			Affected: []string{"Workflow"}}
	}

	if openDecisions > 0 {
		return Guidance{Action: "Resolve open decisions", Why: fmt.Sprintf("%d decision(s) need your judgment before proceeding.", openDecisions), Affected: []string{"Product Definition"}}
	}
	if openQuestions > 0 {
		return Guidance{Action: "Resolve open questions", Why: fmt.Sprintf("%d open question(s) reduce uncertainty about the product.", openQuestions), Affected: []string{"Product Definition"}}
	}
	return Guidance{Action: "Workflow in progress", Why: "The workflow is running — follow it with `idea watch`.", Affected: []string{"Workflow"}}
}

// completeWhy explains a terminal initiative. Anything still open on it is
// informational — a record of what was not settled — never a prompt to act,
// which is what "Resolve open questions" read as on a finished initiative.
func completeWhy(openDecisions, openQuestions int) string {
	var left []string
	if openDecisions > 0 {
		left = append(left, fmt.Sprintf("%d decision(s)", openDecisions))
	}
	if openQuestions > 0 {
		left = append(left, fmt.Sprintf("%d open question(s)", openQuestions))
	}
	if len(left) == 0 {
		return "Verification passed and the work is done."
	}
	return fmt.Sprintf("Verification passed and the work is done. %s stayed unresolved and remain on the record for reference — nothing is waiting on you.",
		strings.Join(left, " and "))
}

// Changed is the grouped What Changed projection. Through is the highest event
// sequence actually consumed by this projection (or the caller's cursor when no
// events matched) — the client echoes it back to the read-cursor write so that
// exactly what was shown is marked seen, never events that arrived mid-read.
type Changed struct {
	Groups  []ChangeGroup `json:"groups"`
	Through int64         `json:"through"`
}

// ChangeGroup is one grouped category of changes.
type ChangeGroup struct {
	Category string   `json:"category"`
	Items    []string `json:"items"`
}

// BuildChanged summarizes events since sinceSeq into grouped, narrated items
// (§7). The raw event timeline is served separately via /activity.
func BuildChanged(ctx context.Context, db *store.DB, sinceSeq int64, limit int) (Changed, error) {
	events, err := db.ListEvents(ctx, sinceSeq, limit)
	if err != nil {
		return Changed{}, err
	}
	// Aggregate per category + event type into counts.
	type key struct{ category, eventType string }
	counts := map[key]int{}
	through := sinceSeq
	for _, e := range events {
		if e.Seq > through {
			through = e.Seq
		}
		if isMachinery(e.AggregateType) {
			continue // hide runs/operations/context-bundles (§79)
		}
		k := key{categoryLabel(e.AggregateType), e.EventType}
		counts[k]++
	}

	// Group counts into narrative items per category.
	byCategory := map[string][]string{}
	for k, n := range counts {
		byCategory[k.category] = append(byCategory[k.category], narrate(k.eventType, n))
	}

	out := Changed{Through: through}
	keys := make([]string, 0, len(byCategory))
	for k := range byCategory {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out.Groups = append(out.Groups, ChangeGroup{Category: k, Items: byCategory[k]})
	}
	return out, nil
}

// narrate turns an event type + count into a human phrase.
func narrate(eventType string, n int) string {
	s := func(singular, plural string) string {
		if n == 1 {
			return singular
		}
		return fmt.Sprintf("%d %s", n, plural)
	}
	switch eventType {
	case "initiative.created":
		return "Initiative created"
	case "initiative.status_changed":
		return "Workflow advanced"
	case "artifact.created":
		return s("1 artifact created", "artifacts created")
	case "assertion.created":
		return s("1 new assertion", "new assertions")
	case "requirement.created":
		return s("1 new requirement", "new requirements")
	case "requirements.superseded":
		return "Requirements revised after review"
	case "decision.created":
		return s("1 new decision", "new decisions")
	case "decision.resolved":
		return s("1 decision resolved", "decisions resolved")
	case "question.created":
		return s("1 new open question", "new open questions")
	case "question.answered":
		return s("1 question answered", "questions answered")
	case "finding.created":
		return s("1 review finding", "review findings")
	case "finding.dispositioned":
		return s("1 finding dispositioned", "findings dispositioned")
	case "run.status_changed":
		return "Model work completed"
	default:
		return eventType
	}
}

func categoryLabel(aggregate string) string {
	switch aggregate {
	case "assertion":
		return "Research"
	case "requirement":
		return "Requirements"
	case "decision":
		return "Decisions"
	case "artifact":
		return "Artifacts"
	case "finding":
		return "Review"
	case "initiative":
		return "Initiative"
	case "question":
		return "Open Questions"
	default:
		return strings.Title(aggregate)
	}
}

// isMachinery reports whether an aggregate is internal machinery (runs,
// operations, context bundles) that should be hidden from the user (§61, §79).
func isMachinery(aggregate string) bool {
	switch aggregate {
	case "run", "run_manifest", "operation", "context_bundle", "stage_run", "workflow_run":
		return true
	}
	return false
}

// orEmpty returns a non-nil slice, so an empty one marshals as [] rather than
// null.
//
// The first sweep initialised the top-level projection slices and stopped
// there, which left every nested view — a requirement's criteria, a decision's
// options and dissents — still emitting null the moment a collection had
// anything in it. The test that certified the sweep used an initiative with
// nothing attached, so it never reached them.
func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
