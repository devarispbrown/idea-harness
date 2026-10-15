package projection

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/id"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// Gap analysis (`idea gaps`): a deterministic pass over the Product Graph that
// names what is missing or no longer holds -- a requirement nothing
// implements, a criterion nothing verified, evidence recorded against code
// that has since changed, an assumption the plan rests on that nobody
// validated, a decision whose basis moved, a requirement serving no objective,
// an objective nobody measures, a metric drifting, verified work whose
// objective is failing. No model call, no score, no fuzzy
// matching: every trigger is a comparison of recorded state, and
// docs/architecture.md ("Asking the graph: gaps") states each one exactly.
//
// It is a projection, not a check: it never writes, and it never blocks the
// workflow. `idea gaps --fail-on <severity>` is how a script turns it into a
// gate.

// GapOptions narrows a gap analysis.
type GapOptions struct {
	// Kinds limits the report to these kinds; empty means every kind.
	Kinds []domain.GapKind
	// EvidenceTests is ComputeEvidenceTests' result for the initiative, when
	// the caller has the blob stores to compute it. Optional: without it a
	// failing mapped test is not seen, and only recorded verification evidence
	// decides the verification kinds.
	EvidenceTests map[string][]domain.EvidenceTest
	// Now is when metric drift is evaluated (staleness is relative to it);
	// zero means time.Now().
	Now time.Time
}

// gapAnalyzer is one kind's rule. The table, not a switch, is the extension
// point: a new kind is one more row (see domain.GapKind).
type gapAnalyzer struct {
	kinds []domain.GapKind
	run   func(g *gapGraph) []domain.Gap
}

var gapAnalyzers = []gapAnalyzer{
	{[]domain.GapKind{domain.GapUnimplementedRequirement}, (*gapGraph).unimplementedRequirements},
	{[]domain.GapKind{domain.GapUnverifiedRequirement, domain.GapUntestedAcceptanceCriterion}, (*gapGraph).verificationGaps},
	{[]domain.GapKind{domain.GapUnvalidatedAssumption}, (*gapGraph).unvalidatedAssumptions},
	{[]domain.GapKind{domain.GapInvalidatedDecision}, (*gapGraph).invalidatedDecisions},
	{[]domain.GapKind{domain.GapOrphanTask, domain.GapOrphanChangeSet}, (*gapGraph).orphans},
	{[]domain.GapKind{domain.GapStaleArtifact}, (*gapGraph).staleArtifacts},
	{[]domain.GapKind{domain.GapOrphanRequirement, domain.GapUnmeasuredObjective, domain.GapUnservedObjective,
		domain.GapDriftingMetric, domain.GapIneffectiveRequirement}, (*gapGraph).outcomeGaps},
}

// ParseGapKinds parses a comma-separated --kind / ?kind= value. Empty is "all".
func ParseGapKinds(s string) ([]domain.GapKind, error) {
	var out []domain.GapKind
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k := domain.GapKind(part)
		if !k.Valid() {
			names := make([]string, len(domain.GapKinds))
			for i, v := range domain.GapKinds {
				names[i] = string(v)
			}
			return nil, fmt.Errorf("unknown gap kind %q; known kinds: %s", part, strings.Join(names, ", "))
		}
		out = append(out, k)
	}
	return out, nil
}

// BuildGaps runs every analyzer (or those opts.Kinds selects) over one
// initiative and returns its gaps, most severe first. Never nil.
func BuildGaps(ctx context.Context, db *store.DB, ini domain.Initiative, opts GapOptions) ([]domain.Gap, error) {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	g, err := loadGapGraph(ctx, db, ini, opts.EvidenceTests, now)
	if err != nil {
		return nil, err
	}
	want := map[domain.GapKind]bool{}
	for _, k := range opts.Kinds {
		want[k] = true
	}
	out := []domain.Gap{}
	for _, a := range gapAnalyzers {
		selected := len(want) == 0
		for _, k := range a.kinds {
			selected = selected || want[k]
		}
		if !selected {
			continue
		}
		for _, gap := range a.run(g) {
			if len(want) > 0 && !want[gap.Kind] {
				continue
			}
			gap.InitiativeID = ini.ID
			out = append(out, gap)
		}
	}
	SortGaps(out)
	return out, nil
}

// SortGaps orders gaps most severe first, then by kind (domain.GapKinds
// order), then most depended-on first, then by the first node's code -- a
// total order, so the same graph always prints the same report.
func SortGaps(gaps []domain.Gap) {
	kindRank := map[domain.GapKind]int{}
	for i, k := range domain.GapKinds {
		kindRank[k] = i
	}
	sort.SliceStable(gaps, func(i, j int) bool {
		a, b := gaps[i], gaps[j]
		if ra, rb := domain.SeverityRank(a.Severity), domain.SeverityRank(b.Severity); ra != rb {
			return ra < rb
		}
		if a.Kind != b.Kind {
			return kindRank[a.Kind] < kindRank[b.Kind]
		}
		if a.Dependents != b.Dependents {
			return a.Dependents > b.Dependents
		}
		if a.InitiativeID != b.InitiativeID {
			return a.InitiativeID < b.InitiativeID
		}
		return codeLess(firstCode(a), firstCode(b))
	})
}

func firstCode(g domain.Gap) string {
	if len(g.Nodes) == 0 {
		return ""
	}
	return g.Nodes[0].Code
}

// codeLess compares REQ-2 before REQ-10: by prefix, then numerically.
func codeLess(a, b string) bool {
	pa, na := splitCode(a)
	pb, nb := splitCode(b)
	if pa != pb {
		return pa < pb
	}
	if na != nb {
		return na < nb
	}
	return a < b
}

func splitCode(s string) (string, int) {
	i := strings.LastIndexByte(s, '-')
	if i < 0 {
		return s, 0
	}
	n, err := strconv.Atoi(s[i+1:])
	if err != nil {
		return s, 0
	}
	return s[:i], n
}

// gapGraph is one initiative's graph, loaded once and indexed for the
// analyzers. Everything here is read-only.
type gapGraph struct {
	ini domain.Initiative

	reqs     []domain.Requirement
	reqByID  map[string]domain.Requirement
	tasks    []domain.Task
	taskByID map[string]domain.Task
	css      []domain.ChangeSet
	cssByTsk map[string][]domain.ChangeSet
	asserts  []domain.Assertion
	assertBy map[string]domain.Assertion
	decs     []domain.Decision
	decByID  map[string]domain.Decision
	stale    []domain.StaleItem
	arts     []store.ArtifactView
	objs     []domain.Objective
	objByID  map[string]domain.Objective
	metrics  []domain.OutcomeMetric
	metByID  map[string]domain.OutcomeMetric
	// outcome is domain.AnalyzeOutcomeChain over the graph above: the same
	// rule the outcome report (GET …/outcomes) and the PRD review read.
	outcome domain.OutcomeChainGaps

	edgesFrom map[string][]domain.GraphEdge // "type:id" -> outgoing
	edgesTo   map[string][]domain.GraphEdge // "type:id" -> incoming

	evidence      map[string][]domain.VerificationEvidence // requirement id -> rows, oldest first
	evidenceTests map[string][]domain.EvidenceTest
	workspaceAt   map[string]time.Time // change set id -> last apply/revert
	eventSeq      map[string]int64     // "<id>|<event type>" -> max seq
}

func loadGapGraph(ctx context.Context, db *store.DB, ini domain.Initiative, evidenceTests map[string][]domain.EvidenceTest, now time.Time) (*gapGraph, error) {
	g := &gapGraph{
		ini:           ini,
		objByID:       map[string]domain.Objective{},
		metByID:       map[string]domain.OutcomeMetric{},
		reqByID:       map[string]domain.Requirement{},
		taskByID:      map[string]domain.Task{},
		cssByTsk:      map[string][]domain.ChangeSet{},
		assertBy:      map[string]domain.Assertion{},
		decByID:       map[string]domain.Decision{},
		edgesFrom:     map[string][]domain.GraphEdge{},
		edgesTo:       map[string][]domain.GraphEdge{},
		evidence:      map[string][]domain.VerificationEvidence{},
		evidenceTests: evidenceTests,
	}
	var err error
	if g.reqs, err = db.ListRequirements(ctx, ini.ID); err != nil {
		return nil, err
	}
	for _, r := range g.reqs {
		g.reqByID[r.ID] = r
		if g.evidence[r.ID], err = db.ListEvidenceByRequirement(ctx, r.ID); err != nil {
			return nil, err
		}
	}
	if g.tasks, err = db.ListTasks(ctx, ini.ID); err != nil {
		return nil, err
	}
	for _, t := range g.tasks {
		g.taskByID[t.ID] = t
	}
	if g.css, err = db.ListChangeSets(ctx, ini.ID); err != nil {
		return nil, err
	}
	for _, cs := range g.css {
		g.cssByTsk[cs.TaskID] = append(g.cssByTsk[cs.TaskID], cs)
	}
	if g.asserts, err = db.ListAssertions(ctx, ini.ID); err != nil {
		return nil, err
	}
	for _, a := range g.asserts {
		g.assertBy[a.ID] = a
	}
	if g.decs, err = db.ListDecisions(ctx, ini.ID); err != nil {
		return nil, err
	}
	for _, d := range g.decs {
		g.decByID[d.ID] = d
	}
	if g.stale, err = db.ListStaleItems(ctx, ini.ID); err != nil {
		return nil, err
	}
	if g.arts, err = db.ListArtifacts(ctx, ini.ID); err != nil {
		return nil, err
	}
	edges, err := db.ListGraphEdges(ctx, ini.ID)
	if err != nil {
		return nil, err
	}
	for _, e := range edges {
		g.edgesFrom[e.FromType+":"+e.FromID] = append(g.edgesFrom[e.FromType+":"+e.FromID], e)
		g.edgesTo[e.ToType+":"+e.ToID] = append(g.edgesTo[e.ToType+":"+e.ToID], e)
	}
	if g.workspaceAt, err = db.ChangeSetWorkspaceTimes(ctx, ini.ID); err != nil {
		return nil, err
	}
	if g.eventSeq, err = db.GraphEventSeqs(ctx, ini.ID); err != nil {
		return nil, err
	}
	if g.objs, err = db.ListObjectives(ctx, ini.ID); err != nil {
		return nil, err
	}
	for _, o := range g.objs {
		g.objByID[o.ID] = o
	}
	if g.metrics, err = db.ListMetrics(ctx, ini.ID, now); err != nil {
		return nil, err
	}
	for _, m := range g.metrics {
		g.metByID[m.ID] = m
	}
	g.outcome = domain.AnalyzeOutcomeChain(g.reqs, g.objs, g.metrics)
	return g, nil
}

// ---------- shared predicates ----------

// liveRequirement is the same "live" every graph reader uses
// (workflow.labelCache.isActiveRequirement, store.decisionDependents): not
// removed and not superseded by a later PRD revision.
func liveRequirement(r domain.Requirement) bool {
	return r.Status != domain.RequirementRemoved && r.SupersededBy == ""
}

// inScopeRequirement is a live requirement the completion gate counts:
// approved (or verified/failed after it) -- not a draft no one approved yet,
// and not one a human waived out of scope.
func inScopeRequirement(r domain.Requirement) bool {
	if !liveRequirement(r) {
		return false
	}
	switch r.Status {
	case domain.RequirementApproved, domain.RequirementVerified, domain.RequirementFailed:
		return true
	}
	return false
}

// liveTask is a task still in the plan: not cancelled, not superseded.
func liveTask(t domain.Task) bool {
	return t.Status != domain.TaskCancelled && t.SupersededBy == ""
}

// tasksFor returns the live tasks with an edge of one of edgeTypes to the
// requirement, in plan order.
func (g *gapGraph) tasksFor(reqID string, edgeTypes ...domain.EdgeType) []domain.Task {
	seen := map[string]bool{}
	for _, e := range g.edgesTo["requirement:"+reqID] {
		if e.FromType != "task" {
			continue
		}
		for _, et := range edgeTypes {
			if e.EdgeType == et {
				seen[e.FromID] = true
			}
		}
	}
	var out []domain.Task
	for _, t := range g.tasks {
		if seen[t.ID] && liveTask(t) {
			out = append(out, t)
		}
	}
	return out
}

// liveRequirementsOf returns the live requirements a task implements or
// verifies.
func (g *gapGraph) liveRequirementsOf(taskID string) []domain.Requirement {
	var out []domain.Requirement
	for _, e := range g.edgesFrom["task:"+taskID] {
		if e.ToType != "requirement" || (e.EdgeType != domain.EdgeImplements && e.EdgeType != domain.EdgeVerifies) {
			continue
		}
		if r, ok := g.reqByID[e.ToID]; ok && liveRequirement(r) {
			out = append(out, r)
		}
	}
	return out
}

func (g *gapGraph) hasExecutionPlan() bool {
	for _, a := range g.arts {
		if a.Kind == domain.ArtifactExecutionPlan {
			return true
		}
	}
	return false
}

// workHeld reports whether any change set holds work -- the condition under
// which `idea revise-plan` and `idea reopen-phase` refuse.
func (g *gapGraph) workHeld() bool {
	for _, cs := range g.css {
		if cs.Status.HoldsWork() {
			return true
		}
	}
	return false
}

// canRevisePlan mirrors Engine.RevisePlan's own preconditions, so a gap never
// names a command that is guaranteed to refuse.
func (g *gapGraph) canRevisePlan() bool {
	if g.workHeld() {
		return false
	}
	return g.ini.Status == domain.InitiativePlanApproved || g.ini.Status == domain.InitiativeNeedsHumanJudgment
}

// relinkNext is the move for a node that should trace to a requirement and
// does not: regenerate the plan while that is still possible, edit the task
// once work is in flight.
func (g *gapGraph) relinkNext(taskCode string) string {
	if g.canRevisePlan() {
		return "idea revise-plan"
	}
	return "link " + taskCode + " to a requirement on the Plan page (PATCH …/tasks/{taskId} requirement_ids)"
}

// dependents counts the distinct live nodes that transitively rest on a
// node: incoming edges (what derives from, implements or depends on it), plus
// the targets of its own `supports` edges, plus -- since change sets reach
// their task by TaskID rather than an edge -- each reached task's change sets
// that still hold work. Removed/superseded requirements are not counted.
// Bounded like workflow.Impact (depth 8, 500 nodes), and the same answer
// `idea impact` would itemize for edge-linked nodes.
func (g *gapGraph) dependents(nodeType, nodeID string) []domain.GapNode {
	const maxDepth, maxNodes = 8, 500
	seen := map[string]bool{nodeType + ":" + nodeID: true}
	var out []domain.GapNode
	add := func(typ, id string) bool {
		k := typ + ":" + id
		if seen[k] {
			return false
		}
		if typ == "requirement" {
			if r, ok := g.reqByID[id]; !ok || !liveRequirement(r) {
				return false
			}
		}
		if typ == "task" {
			if t, ok := g.taskByID[id]; !ok || !liveTask(t) {
				return false
			}
		}
		seen[k] = true
		out = append(out, g.node(typ, id))
		return true
	}
	frontier := [][2]string{{nodeType, nodeID}}
	for depth := 0; depth < maxDepth && len(frontier) > 0 && len(out) < maxNodes; depth++ {
		var next [][2]string
		for _, n := range frontier {
			for _, e := range g.edgesTo[n[0]+":"+n[1]] {
				if e.EdgeType == domain.EdgeProducedBy || e.EdgeType == domain.EdgeConsumes {
					continue // provenance, not dependence
				}
				if add(e.FromType, e.FromID) {
					next = append(next, [2]string{e.FromType, e.FromID})
				}
			}
			for _, e := range g.edgesFrom[n[0]+":"+n[1]] {
				if e.EdgeType == domain.EdgeSupports && add(e.ToType, e.ToID) {
					next = append(next, [2]string{e.ToType, e.ToID})
				}
			}
			if n[0] == "task" {
				for _, cs := range g.cssByTsk[n[1]] {
					if cs.Status.HoldsWork() {
						add("change_set", cs.ID)
					}
				}
			}
		}
		frontier = next
	}
	return out
}

// node renders a GapNode with its human code and a short label.
func (g *gapGraph) node(typ, id string) domain.GapNode {
	n := domain.GapNode{Type: typ, ID: id, Code: shortID(id)}
	switch typ {
	case "requirement":
		if r, ok := g.reqByID[id]; ok {
			n.Code, n.Label = r.Code, trimLabel(r.Statement)
		}
	case "task":
		if t, ok := g.taskByID[id]; ok {
			n.Code, n.Label = t.Code, trimLabel(t.Title)
		}
	case "change_set":
		for _, cs := range g.css {
			if cs.ID == id {
				n.Code, n.Label = cs.HumanID, string(cs.Status)
			}
		}
	case "decision":
		if d, ok := g.decByID[id]; ok {
			n.Code, n.Label = d.Code, trimLabel(d.Question)
		}
	case "assertion":
		if a, ok := g.assertBy[id]; ok {
			n.Label = trimLabel(a.Statement)
		}
	case "artifact":
		for _, a := range g.arts {
			if a.ArtifactID == id {
				n.Code = string(a.Kind)
			}
		}
	case "objective":
		if o, ok := g.objByID[id]; ok {
			n.Code, n.Label = o.Code, trimLabel(o.Statement)
		}
	case "metric":
		if m, ok := g.metByID[id]; ok {
			n.Code, n.Label = m.Code, trimLabel(m.Name)
		}
	case "initiative":
		n.Label = trimLabel(g.ini.Title)
	}
	return n
}

// shortID is the 8-character prefix the CLI prints for code-less nodes
// (cli.shortRef) -- and accepts back as a reference.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func trimLabel(s string) string { return domain.TrimToWordBoundary(s, 60) }

func codes(nodes []domain.GapNode) string {
	cs := make([]string, len(nodes))
	for i, n := range nodes {
		cs[i] = n.Code
	}
	return strings.Join(cs, ", ")
}

// priorityTier maps a requirement's priority onto the severity a gap in it
// carries at full weight.
func priorityTier(p domain.RequirementPriority) domain.Severity {
	switch p {
	case domain.PriorityMust:
		return domain.SeverityHigh
	case domain.PriorityCould:
		return domain.SeverityLow
	}
	return domain.SeverityMedium
}

// ---------- unimplemented_requirement ----------

// unimplementedRequirements: an in-scope requirement other than a verified
// one (a human's verification outranks the graph's bookkeeping) that either
// no live task implements (no_task), or whose implementing tasks have no
// applied, non-empty change set between them (not_applied).
func (g *gapGraph) unimplementedRequirements() []domain.Gap {
	var out []domain.Gap
	planned := g.hasExecutionPlan()
	for _, r := range g.reqs {
		if !inScopeRequirement(r) || r.Status == domain.RequirementVerified {
			continue
		}
		reqNode := g.node("requirement", r.ID)
		tasks := g.tasksFor(r.ID, domain.EdgeImplements)
		if len(tasks) == 0 {
			gap := domain.Gap{Kind: domain.GapUnimplementedRequirement, State: domain.GapStateNoTask,
				Severity: priorityTier(r.Priority), Nodes: []domain.GapNode{reqNode}}
			switch {
			case !planned:
				// Expected before planning: every requirement is unimplemented
				// until the plan exists. Reported, but not at a weight that
				// fails a gate on an initiative that simply is not there yet.
				gap.Severity = domain.SeverityLow
				gap.Why = r.Code + " has no task yet: no execution plan has been generated"
				gap.Next = "idea advance"
			case g.canRevisePlan():
				gap.Why = r.Code + " is in scope but no task in the plan implements it"
				gap.Next = "idea revise-plan"
			default:
				gap.Why = r.Code + " is in scope but no task in the plan implements it, and change sets already hold work so the plan cannot be regenerated"
				gap.Next = "add a task implementing " + r.Code + " on the Plan page (POST …/tasks)"
			}
			out = append(out, gap)
			continue
		}
		applied := false
		var pending *domain.ChangeSet
		for _, t := range tasks {
			for i := range g.cssByTsk[t.ID] {
				cs := g.cssByTsk[t.ID][i]
				if cs.ImplementsTask() {
					applied = true
				}
				if cs.Status.HoldsWork() && cs.Status != domain.ChangeSetApplied &&
					(pending == nil || domain.NewerChangeSetInScanOrder(cs, *pending)) {
					pending = &g.cssByTsk[t.ID][i]
				}
			}
		}
		if applied {
			continue
		}
		nodes := []domain.GapNode{reqNode}
		for _, t := range tasks {
			nodes = append(nodes, g.node("task", t.ID))
		}
		gap := domain.Gap{Kind: domain.GapUnimplementedRequirement, State: domain.GapStateNotApplied,
			Severity: priorityTier(r.Priority), Nodes: nodes,
			Why: fmt.Sprintf("%s is planned (%s) but no change set implementing it has been applied", r.Code, codes(nodes[1:]))}
		gap.Next = "idea execute"
		if pending != nil {
			gap.Nodes = append(gap.Nodes, g.node("change_set", pending.ID))
			switch pending.Status {
			case domain.ChangeSetReviewRequired:
				gap.Next = "idea review " + pending.HumanID + " approve"
			case domain.ChangeSetApproved, domain.ChangeSetReverted:
				gap.Next = "idea apply " + pending.HumanID
			case domain.ChangeSetConflicted:
				gap.Next = "idea change " + pending.HumanID
			case domain.ChangeSetChangesRequested:
				gap.Next = "idea execute"
			default:
				gap.Next = "idea status (" + pending.HumanID + " is " + string(pending.Status) + ")"
			}
		}
		out = append(out, gap)
	}
	return out
}

// ---------- unverified_requirement / untested_acceptance_criterion ----------

// verificationGaps classifies each acceptance criterion of an in-scope
// requirement by its latest *decisive* evidence row -- pass, fail, blocked or
// waived; a needs_manual row is the verifier's "a human must look" placeholder
// and decides nothing:
//
//   - none                       -> untested_acceptance_criterion / no_evidence
//   - fail or blocked            -> unverified_requirement / evidence_failed
//   - pass, recorded before the
//     last apply or revert of a
//     change set whose task
//     implements or verifies
//     the requirement            -> unverified_requirement / evidence_stale
//   - pass (fresh) or waived     -> covered
//
// A requirement whose status is `failed`, or one with a mapped test that last
// failed (ComputeEvidenceTests, when the caller supplied it), is
// evidence_failed whatever its rows say. A requirement with no acceptance
// criteria at all is unverified_requirement / no_criteria: the gate needs a
// pass per criterion, so it can never be verified, and `idea verify --waive`
// refuses it too. One requirement-level gap per requirement (failed outranks
// stale); one criterion-level gap per untested criterion.
func (g *gapGraph) verificationGaps() []domain.Gap {
	var out []domain.Gap
	for _, r := range g.reqs {
		if !inScopeRequirement(r) {
			continue
		}
		reqNode := g.node("requirement", r.ID)
		implemented := false
		for _, t := range g.tasksFor(r.ID, domain.EdgeImplements) {
			for _, cs := range g.cssByTsk[t.ID] {
				if cs.ImplementsTask() {
					implemented = true
				}
			}
		}

		if len(r.AcceptanceCriteria) == 0 {
			out = append(out, domain.Gap{Kind: domain.GapUnverifiedRequirement, State: domain.GapStateNoCriteria,
				Severity: domain.SeverityHigh, Nodes: []domain.GapNode{reqNode},
				Why:  r.Code + " has no acceptance criteria, so no evidence can ever verify it and the completion gate cannot pass",
				Next: "idea revise"})
			continue
		}

		// The last time the code this requirement's evidence speaks to
		// changed: the newest apply or revert of any non-empty change set
		// whose task implements or verifies it.
		var lastChange time.Time
		var lastChangeCS string
		for _, t := range g.tasksFor(r.ID, domain.EdgeImplements, domain.EdgeVerifies) {
			for _, cs := range g.cssByTsk[t.ID] {
				if cs.NoOp {
					continue
				}
				if at, ok := g.workspaceAt[cs.ID]; ok && at.After(lastChange) {
					lastChange, lastChangeCS = at, cs.HumanID
				}
			}
		}

		var failed, staleNodes []domain.GapNode
		var failedWhy []string
		var staleBefore time.Time
		for i, ac := range r.AcceptanceCriteria {
			key := domain.CriterionKey(r.ID, i)
			acNode := domain.GapNode{Type: "acceptance_criterion", ID: key,
				Code: r.Code + "/" + id.Code("AC", i+1), Label: trimLabel(criterionText(ac))}
			var decisive *domain.VerificationEvidence
			placeholder := false
			for j := range g.evidence[r.ID] {
				ev := g.evidence[r.ID][j]
				if ev.CriterionKey != key {
					continue
				}
				if ev.Status == domain.VerificationNeedsManual {
					placeholder = true
					continue
				}
				decisive = &g.evidence[r.ID][j]
			}
			switch {
			case decisive == nil:
				sev := domain.SeverityLow
				if implemented && r.Priority == domain.PriorityMust {
					sev = domain.SeverityMedium
				}
				why := acNode.Code + " has no pass or fail evidence"
				if placeholder {
					why += " (only the verifier's needs-manual placeholder)"
				}
				if !implemented {
					why += "; nothing implementing " + r.Code + " is applied yet"
				}
				next := "idea verify " + r.Code + " --pass|--fail|--waive"
				if AllTestsPassed(g.evidenceTests[r.ID]) {
					next = "idea verify --confirm-passing-tests"
				}
				out = append(out, domain.Gap{Kind: domain.GapUntestedAcceptanceCriterion, State: domain.GapStateNoEvidence,
					Severity: sev, Nodes: []domain.GapNode{acNode, reqNode}, Why: why, Next: next})
			case decisive.Status == domain.VerificationFail || decisive.Status == domain.VerificationBlocked:
				failed = append(failed, acNode)
				failedWhy = append(failedWhy, fmt.Sprintf("%s recorded %s by %s", acNode.Code, decisive.Status, orUnknown(decisive.VerifiedBy)))
			case decisive.Status == domain.VerificationPass && lastChange.After(decisive.CreatedAt):
				staleNodes = append(staleNodes, acNode)
				if staleBefore.IsZero() || decisive.CreatedAt.Before(staleBefore) {
					staleBefore = decisive.CreatedAt
				}
			}
		}
		var failingTests []string
		for _, t := range g.evidenceTests[r.ID] {
			if t.Status != "passed" {
				failingTests = append(failingTests, t.Name)
			}
		}

		verifyNext := "idea verify " + r.Code + " --pass|--fail|--waive"
		switch {
		case r.Status == domain.RequirementFailed || len(failed) > 0 || len(failingTests) > 0:
			var why []string
			if r.Status == domain.RequirementFailed {
				why = append(why, r.Code+" was failed by a human judgment")
			}
			why = append(why, failedWhy...)
			if len(failingTests) > 0 {
				why = append(why, "mapped tests failing: "+strings.Join(failingTests, ", "))
			}
			out = append(out, domain.Gap{Kind: domain.GapUnverifiedRequirement, State: domain.GapStateEvidenceFailed,
				Severity: domain.SeverityHigh, Nodes: append([]domain.GapNode{reqNode}, failed...),
				Why: strings.Join(why, "; "), Next: verifyNext})
		case len(staleNodes) > 0:
			out = append(out, domain.Gap{Kind: domain.GapUnverifiedRequirement, State: domain.GapStateEvidenceStale,
				Severity: domain.SeverityMedium, Nodes: append([]domain.GapNode{reqNode}, staleNodes...),
				Why: fmt.Sprintf("pass evidence for %s predates %s changing the code it verified (%s)",
					codes(staleNodes), lastChangeCS, lastChange.UTC().Format(time.RFC3339)),
				Next: "idea verify " + r.Code + " --pass"})
		}
	}
	return out
}

func criterionText(ac domain.AcceptanceCriterion) string {
	return strings.TrimSpace(strings.Join([]string{ac.Metric, ac.Condition, ac.Target}, " "))
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// ---------- unvalidated_assumption ----------

// unvalidatedAssumptions: a live assumption or hypothesis (facts are not
// reported) that is a candidate nobody judged (unvalidated) or one a human
// disputed (disputed), with or without dependents -- ranked by how many live
// nodes rest on it -- plus a retracted one that live nodes still rest on
// (refuted). An accepted one, or a retracted one nothing cites, is not a gap.
func (g *gapGraph) unvalidatedAssumptions() []domain.Gap {
	var out []domain.Gap
	for _, a := range g.asserts {
		if a.Type == domain.AssertionFact || a.SupersededBy != "" || a.Status == domain.AssertionSuperseded {
			continue
		}
		deps := g.dependents("assertion", a.ID)
		node := g.node("assertion", a.ID)
		ref := node.Code
		gap := domain.Gap{Kind: domain.GapUnvalidatedAssumption, Dependents: len(deps), Nodes: append([]domain.GapNode{node}, deps...)}
		restsOn := ""
		if len(deps) > 0 {
			restsOn = fmt.Sprintf("; %d live node(s) rest on it (%s)", len(deps), codes(deps))
		}
		switch a.Status {
		case domain.AssertionCandidate:
			gap.State, gap.Severity = domain.GapStateUnvalidated, domain.SeverityLow
			if len(deps) > 0 {
				gap.Severity = domain.SeverityMedium
			}
			gap.Why = fmt.Sprintf("%s %s was never validated%s", a.Type, ref, restsOn)
			gap.Next = "idea assumption " + ref + " accept|dispute|retract"
		case domain.AssertionDisputed:
			gap.State, gap.Severity = domain.GapStateDisputed, domain.SeverityLow
			if len(deps) > 0 {
				gap.Severity = domain.SeverityMedium
			}
			gap.Why = fmt.Sprintf("%s %s is disputed and unresolved%s", a.Type, ref, restsOn)
			gap.Next = "idea assumption " + ref + " accept|retract"
		case domain.AssertionRetracted:
			if len(deps) == 0 {
				continue
			}
			gap.State, gap.Severity = domain.GapStateRefuted, domain.SeverityHigh
			gap.Why = fmt.Sprintf("%s %s was retracted, yet %d live node(s) still rest on it (%s)", a.Type, ref, len(deps), codes(deps))
			gap.Next = "idea impact " + deps[0].Type + " " + deps[0].Code
		default:
			continue
		}
		out = append(out, gap)
	}
	return out
}

// ---------- invalidated_decision ----------

// invalidatedDecisions reports a live (not superseded) decision whose basis
// moved, by four exact triggers:
//
//   - dependents_stale: the decision carries active stale marks -- it was
//     resolved or reopened after requirements, tasks or change sets were built
//     on it, and nobody has acknowledged or re-planned them. This is the same
//     mark `idea impact` reports as "stale".
//   - requirement_revised (decided only): a live requirement that derives from
//     it was revised (a requirement.revised event) after the decision's last
//     resolution, so it was decided against text that no longer exists.
//   - basis_refuted (decided only): an assertion it derives from or that
//     supports it was retracted or disputed, or a node `invalidates` or
//     `contradicts` it -- the edge `idea impact` calls "obsolete".
//   - requirements_removed (decided only): requirements cited it, and every
//     one of them has since been removed or superseded, so it serves nothing
//     live.
func (g *gapGraph) invalidatedDecisions() []domain.Gap {
	staleByDecision := map[string][]domain.StaleItem{}
	for _, s := range g.stale {
		if s.DecisionID != "" {
			staleByDecision[s.DecisionID] = append(staleByDecision[s.DecisionID], s)
		}
	}
	var out []domain.Gap
	for _, d := range g.decs {
		if d.SupersededBy != "" || d.Status == domain.DecisionSuperseded {
			continue
		}
		decNode := g.node("decision", d.ID)
		deps := len(g.dependents("decision", d.ID))
		decided := d.Status == domain.DecisionDecided

		if marks := staleByDecision[d.ID]; len(marks) > 0 {
			nodes := []domain.GapNode{decNode}
			builtOn := false // a task or change set, not only requirements
			for _, m := range marks {
				nodes = append(nodes, g.node(m.TargetType, m.TargetID))
				builtOn = builtOn || m.TargetType != "requirement"
			}
			// Built work resting on a changed decision is high; requirements
			// alone (the ordinary "resolved after the PRD cited it" case) are
			// a re-read, medium.
			sev := domain.SeverityMedium
			if builtOn {
				sev = domain.SeverityHigh
			}
			gap := domain.Gap{Kind: domain.GapInvalidatedDecision, State: domain.GapStateDependentsStale,
				Severity: sev, Nodes: nodes, Dependents: deps}
			switch {
			case !decided:
				gap.Why = fmt.Sprintf("%s was reopened after %s were built on it", d.Code, codes(nodes[1:]))
				gap.Next = "idea resolve " + d.Code + " <option#>"
			case builtOn && g.canRevisePlan():
				// revise-plan clears task and change-set marks, never a
				// requirement's (store.ClearStaleItemsForInitiative).
				gap.Why = fmt.Sprintf("%s changed after %s were built on it; they are marked stale", d.Code, codes(nodes[1:]))
				gap.Next = "idea revise-plan"
			default:
				gap.Why = fmt.Sprintf("%s changed after %s were built on it; they are marked stale until re-checked and acknowledged", d.Code, codes(nodes[1:]))
				gap.Next = "idea impact decision " + d.Code + ", then acknowledge each item (web UI stale banner, or POST …/stale/{staleId}/ack)"
			}
			out = append(out, gap)
		}
		if !decided {
			continue
		}

		decidedSeq := g.eventSeq[d.ID+"|decision.resolved"]
		if s := g.eventSeq[d.ID+"|decision.created"]; s > decidedSeq {
			decidedSeq = s
		}
		var revised, citing []domain.GapNode
		liveCiting := 0
		for _, e := range g.edgesTo["decision:"+d.ID] {
			if e.FromType != "requirement" || e.EdgeType != domain.EdgeDerivedFrom {
				continue
			}
			r, ok := g.reqByID[e.FromID]
			if !ok {
				continue
			}
			citing = append(citing, g.node("requirement", r.ID))
			if !liveRequirement(r) {
				continue
			}
			liveCiting++
			if decidedSeq > 0 && g.eventSeq[r.ID+"|requirement.revised"] > decidedSeq {
				revised = append(revised, g.node("requirement", r.ID))
			}
		}
		if len(revised) > 0 {
			out = append(out, domain.Gap{Kind: domain.GapInvalidatedDecision, State: domain.GapStateRequirementRevised,
				Severity: domain.SeverityMedium, Nodes: append([]domain.GapNode{decNode}, revised...), Dependents: deps,
				Why:  fmt.Sprintf("%s was revised after %s was decided; the decision was made against text that no longer exists", codes(revised), d.Code),
				Next: "idea reopen-decision " + d.Code})
		}

		var basis []domain.GapNode
		var basisWhy []string
		considerAssertion := func(id string) {
			a, ok := g.assertBy[id]
			if !ok || (a.Status != domain.AssertionRetracted && a.Status != domain.AssertionDisputed) {
				return
			}
			basis = append(basis, g.node("assertion", id))
			basisWhy = append(basisWhy, fmt.Sprintf("%s %s is %s", a.Type, shortID(id), a.Status))
		}
		for _, e := range g.edgesFrom["decision:"+d.ID] {
			if e.ToType == "assertion" && (e.EdgeType == domain.EdgeDerivedFrom || e.EdgeType == domain.EdgeDependsOn) {
				considerAssertion(e.ToID)
			}
		}
		for _, e := range g.edgesTo["decision:"+d.ID] {
			switch {
			case e.FromType == "assertion" && e.EdgeType == domain.EdgeSupports:
				considerAssertion(e.FromID)
			case e.EdgeType == domain.EdgeInvalidates || e.EdgeType == domain.EdgeContradicts:
				n := g.node(e.FromType, e.FromID)
				basis = append(basis, n)
				basisWhy = append(basisWhy, fmt.Sprintf("%s %s it", n.Code, e.EdgeType))
			}
		}
		if len(basis) > 0 {
			out = append(out, domain.Gap{Kind: domain.GapInvalidatedDecision, State: domain.GapStateBasisRefuted,
				Severity: domain.SeverityHigh, Nodes: append([]domain.GapNode{decNode}, basis...), Dependents: deps,
				Why:  d.Code + "'s basis no longer holds: " + strings.Join(basisWhy, "; "),
				Next: "idea reopen-decision " + d.Code})
		}

		if len(citing) > 0 && liveCiting == 0 {
			out = append(out, domain.Gap{Kind: domain.GapInvalidatedDecision, State: domain.GapStateRequirementsRemoved,
				Severity: domain.SeverityLow, Nodes: append([]domain.GapNode{decNode}, citing...),
				Why:  fmt.Sprintf("every requirement %s served (%s) has been removed or superseded; it no longer shapes anything live", d.Code, codes(citing)),
				Next: "idea impact decision " + d.Code})
		}
	}
	return out
}

// ---------- orphan_task / orphan_change_set ----------

// orphans: a live task with no implements/verifies edge to a live requirement
// (orphan_task / no_requirement), and a change set still holding work whose
// task no longer exists (orphan_change_set / task_missing) or is itself an
// orphan (task_orphaned). Rejected and superseded change sets are dead and
// never reported.
func (g *gapGraph) orphans() []domain.Gap {
	var out []domain.Gap
	orphanTask := map[string]bool{}
	for _, t := range g.tasks {
		if !liveTask(t) || len(g.liveRequirementsOf(t.ID)) > 0 {
			continue
		}
		orphanTask[t.ID] = true
		out = append(out, domain.Gap{Kind: domain.GapOrphanTask, State: domain.GapStateNoRequirement,
			Severity: domain.SeverityMedium, Nodes: []domain.GapNode{g.node("task", t.ID)},
			Why:  t.Code + " implements and verifies no live requirement, so nothing it builds traces back to the PRD",
			Next: g.relinkNext(t.Code)})
	}
	for _, cs := range g.css {
		if !cs.Status.HoldsWork() {
			continue
		}
		sev := domain.SeverityLow
		if cs.Status == domain.ChangeSetApplied {
			sev = domain.SeverityMedium
		}
		node := g.node("change_set", cs.ID)
		t, ok := g.taskByID[cs.TaskID]
		switch {
		case !ok:
			next := "idea change " + cs.HumanID
			switch cs.Status {
			case domain.ChangeSetApplied:
				next = "idea revert " + cs.HumanID
			case domain.ChangeSetReviewRequired:
				next = "idea review " + cs.HumanID + " reject"
			}
			out = append(out, domain.Gap{Kind: domain.GapOrphanChangeSet, State: domain.GapStateTaskMissing,
				Severity: sev, Nodes: []domain.GapNode{node},
				Why:  cs.HumanID + "'s task no longer exists, so it traces to no requirement",
				Next: next})
		case orphanTask[t.ID]:
			out = append(out, domain.Gap{Kind: domain.GapOrphanChangeSet, State: domain.GapStateTaskOrphaned,
				Severity: sev, Nodes: []domain.GapNode{node, g.node("task", t.ID)},
				Why:  cs.HumanID + " was built for " + t.Code + ", which is linked to no live requirement",
				Next: g.relinkNext(t.Code)})
		}
	}
	return out
}

// ---------- stale_artifact ----------

// artifactUpstreams is which artifacts each downstream one is generated from,
// and the command that regenerates it. A downstream artifact whose newest
// version is strictly older than an upstream's newest version was generated
// from a document that has since changed.
var artifactUpstreams = []struct {
	kind      domain.ArtifactKind
	upstreams []domain.ArtifactKind
	next      string
}{
	{domain.ArtifactTechnicalDesign, []domain.ArtifactKind{domain.ArtifactPRD}, "idea reopen-phase architecture"},
	{domain.ArtifactDesignSpec, []domain.ArtifactKind{domain.ArtifactPRD}, "idea reopen-phase product_design"},
	{domain.ArtifactVerificationPlan, []domain.ArtifactKind{domain.ArtifactPRD}, "idea reopen-phase verification_design"},
	{domain.ArtifactExecutionPlan, []domain.ArtifactKind{domain.ArtifactPRD, domain.ArtifactTechnicalDesign, domain.ArtifactDesignSpec, domain.ArtifactVerificationPlan}, "idea revise-plan"},
}

func (g *gapGraph) staleArtifacts() []domain.Gap {
	latest := map[domain.ArtifactKind]store.ArtifactView{}
	for _, a := range g.arts {
		if cur, ok := latest[a.Kind]; !ok || !a.CreatedAt.Before(cur.CreatedAt) {
			latest[a.Kind] = a
		}
	}
	var out []domain.Gap
	for _, rule := range artifactUpstreams {
		down, ok := latest[rule.kind]
		if !ok {
			continue
		}
		nodes := []domain.GapNode{g.node("artifact", down.ArtifactID)}
		var newer []string
		for _, up := range rule.upstreams {
			u, ok := latest[up]
			if !ok || !u.CreatedAt.After(down.CreatedAt) {
				continue
			}
			nodes = append(nodes, g.node("artifact", u.ArtifactID))
			newer = append(newer, fmt.Sprintf("%s v%d", up, u.Version))
		}
		if len(newer) == 0 {
			continue
		}
		next := rule.next
		if g.workHeld() {
			// Both regenerate commands refuse once a change set holds work.
			next = rule.next + " (refused while a change set holds work; revert or finish it first)"
		}
		out = append(out, domain.Gap{Kind: domain.GapStaleArtifact, State: domain.GapStateUpstreamNewer,
			Severity: domain.SeverityMedium, Nodes: nodes,
			Why:  fmt.Sprintf("%s v%d was generated before %s", rule.kind, down.Version, strings.Join(newer, ", ")),
			Next: next})
	}
	return out
}

// ---------- orphan_requirement / unmeasured_objective / unserved_objective /
// drifting_metric / ineffective_requirement ----------

// outcomeGaps turns the outcome chain's gaps (domain.AnalyzeOutcomeChain --
// the same answer the outcome report shows) into gaps, with severities that
// match the PRD review's outcome checks where the review has one:
//
//   - orphan_requirement / objectives_missing: live requirements exist and
//     no objective is active -- one gap about the PRD, medium (the review's
//     single medium finding), not one per requirement.
//   - orphan_requirement / no_objective: with at least one active objective,
//     a live requirement serves none of them -- high (the review blocks on
//     it).
//   - unmeasured_objective / no_metric: an active objective with no live,
//     measurable metric -- high (the review blocks on it).
//   - unserved_objective / no_requirement: an active objective no live
//     requirement serves -- medium: an outcome nothing in the plan aims at.
//   - drifting_metric / regressed (high), off_track (high when the guardrail
//     is breached, else medium), stale (low): a live metric's drift
//     (domain.EvaluateMetricDrift). no_data is not a gap: a metric never
//     observed with no cadence has no expectation yet to miss, and one whose
//     cadence has lapsed since it was defined already evaluates as stale.
//   - ineffective_requirement / works_but_fails_objective: a verified live
//     requirement serving an active objective with an off_track or regressed
//     metric -- high: it passed verification and is failing what it was for.
func (g *gapGraph) outcomeGaps() []domain.Gap {
	var out []domain.Gap
	c := g.outcome

	if c.ObjectivesMissing {
		subject := domain.GapNode{Type: "initiative", ID: g.ini.ID, Code: shortID(g.ini.ID), Label: trimLabel(g.ini.Title)}
		var prd *store.ArtifactView
		for i := range g.arts {
			if g.arts[i].Kind == domain.ArtifactPRD && (prd == nil || !g.arts[i].CreatedAt.Before(prd.CreatedAt)) {
				prd = &g.arts[i]
			}
		}
		if prd != nil {
			subject = g.node("artifact", prd.ArtifactID)
		}
		nodes := []domain.GapNode{subject}
		for _, r := range g.reqs {
			if liveRequirement(r) {
				nodes = append(nodes, g.node("requirement", r.ID))
			}
		}
		out = append(out, domain.Gap{Kind: domain.GapOrphanRequirement, State: domain.GapStateObjectivesMissing,
			Severity: domain.SeverityMedium, Nodes: nodes, Dependents: len(nodes) - 1,
			Why:  fmt.Sprintf("no active objective is defined, so none of the %d live requirement(s) can be traced to an outcome", len(nodes)-1),
			Next: `idea objective add "<statement>"`})
	}

	// With exactly one active objective the link command is complete;
	// otherwise the human picks which objective the requirement serves.
	var activeCodes []string
	for _, o := range g.objs {
		if o.Active() {
			activeCodes = append(activeCodes, o.Code)
		}
	}
	soleObjective := "<OBJ>"
	if len(activeCodes) == 1 {
		soleObjective = activeCodes[0]
	}
	for _, r := range c.RequirementsWithoutObjectives {
		out = append(out, domain.Gap{Kind: domain.GapOrphanRequirement, State: domain.GapStateNoObjective,
			Severity: domain.SeverityHigh, Nodes: []domain.GapNode{g.node("requirement", r.ID)},
			Why:  r.Code + " serves no active objective, so it can pass verification with nobody able to say what outcome it was for",
			Next: "idea objective link " + r.Code + " " + soleObjective})
	}

	for _, o := range c.ObjectivesWithoutMetrics {
		out = append(out, domain.Gap{Kind: domain.GapUnmeasuredObjective, State: domain.GapStateNoMetric,
			Severity: domain.SeverityHigh, Nodes: []domain.GapNode{g.node("objective", o.ID)}, Dependents: len(o.RequirementIDs),
			Why:  o.Code + " has no measurable metric, so nobody can tell whether it is being met and drift cannot be detected",
			Next: "idea metric add " + o.Code + ` "<name>" --target <n>`})
	}

	for _, o := range c.ObjectivesWithoutRequirements {
		out = append(out, domain.Gap{Kind: domain.GapUnservedObjective, State: domain.GapStateNoRequirement,
			Severity: domain.SeverityMedium, Nodes: []domain.GapNode{g.node("objective", o.ID)},
			Why:  o.Code + " is active but no live requirement serves it: nothing in the plan is aimed at it",
			Next: "idea objective link <REQ> " + o.Code})
	}

	for _, m := range c.DriftingMetrics {
		st := m.Status
		nodes := []domain.GapNode{g.node("metric", m.ID)}
		if _, ok := g.objByID[m.ObjectiveID]; ok {
			nodes = append(nodes, g.node("objective", m.ObjectiveID))
		}
		gap := domain.Gap{Kind: domain.GapDriftingMetric, Why: m.Code + ": " + strings.Join(st.Reasons, "; ")}
		if o, ok := g.objByID[m.ObjectiveID]; ok {
			gap.Dependents = len(o.RequirementIDs)
		}
		switch st.Drift {
		case domain.DriftRegressed:
			gap.State, gap.Severity = domain.GapStateRegressed, domain.SeverityHigh
			for _, csID := range st.RegressedBy {
				nodes = append(nodes, g.node("change_set", csID))
			}
			gap.Next = "idea trace metric " + m.Code
		case domain.DriftOffTrack:
			gap.State, gap.Severity = domain.GapStateOffTrack, domain.SeverityMedium
			if st.GuardrailBreached {
				gap.Severity = domain.SeverityHigh
			}
			gap.Next = "idea trace metric " + m.Code
		case domain.DriftStale:
			gap.State, gap.Severity = domain.GapStateStale, domain.SeverityLow
			gap.Next = "idea metric record " + m.Code + " <value>"
			if m.Source.Kind == domain.MetricSourceCommand || m.Source.Kind == domain.MetricSourceURL {
				gap.Next = "idea metric measure " + m.Code
			}
		default:
			continue
		}
		gap.Nodes = nodes
		out = append(out, gap)
	}

	for _, d := range c.RequirementDrift {
		nodes := []domain.GapNode{g.node("requirement", d.RequirementID), g.node("objective", d.ObjectiveID)}
		for _, mc := range d.MetricCodes {
			for _, m := range g.metrics {
				if m.Code == mc {
					nodes = append(nodes, g.node("metric", m.ID))
				}
			}
		}
		out = append(out, domain.Gap{Kind: domain.GapIneffectiveRequirement, State: domain.GapStateWorksButFailsObjective,
			Severity: domain.SeverityHigh, Nodes: nodes,
			Why:  d.Detail + " -- it works, but fails its objective",
			Next: "idea trace metric " + d.MetricCodes[0]})
	}
	return out
}
