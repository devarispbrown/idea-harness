package projection

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// defaultTimelineLimit mirrors BuildChanged's default (200): generous enough
// for a normal session's worth of activity without an unbounded response.
const defaultTimelineLimit = 200

// BuildTimeline builds an initiative's "what changed" timeline (C1, wave 3
// contract) from the event log and the change-set, decision and stale-mark
// tables, scoped to initiativeID throughout by joining back to those tables'
// own initiative_id -- never by reading the events table unscoped, which is
// what let the old What Changed projection leak another initiative's counts
// (JTBD 6). Entries are newest first; since, if non-zero, excludes anything at
// or before it. limit<=0 uses defaultTimelineLimit.
func BuildTimeline(ctx context.Context, db *store.DB, initiativeID string, since int64, limit int) ([]domain.TimelineEntry, error) {
	if limit <= 0 {
		limit = defaultTimelineLimit
	}
	out := make([]domain.TimelineEntry, 0)

	csEvents, err := db.ListChangeSetEventsForInitiative(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	// Findings and task titles are looked up lazily, per change set, and
	// cached -- a busy initiative's timeline can carry many events for the
	// same handful of change sets.
	taskTitles := map[string]string{}
	taskTitle := func(taskID string) string {
		if taskID == "" {
			return ""
		}
		if t, ok := taskTitles[taskID]; ok {
			return t
		}
		title := ""
		if t, err := db.GetTask(ctx, taskID); err == nil {
			title = t.Title
		}
		taskTitles[taskID] = title
		return title
	}
	findingsByCS := map[string][]domain.Finding{}
	findingsFor := func(changeSetID string) []domain.Finding {
		if f, ok := findingsByCS[changeSetID]; ok {
			return f
		}
		f, _ := db.ListFindingsByTarget(ctx, "change_set", changeSetID)
		findingsByCS[changeSetID] = f
		return f
	}

	for _, ev := range csEvents {
		switch ev.EventType {
		case "change_set.applied":
			title := ev.HumanID + " applied"
			if t := taskTitle(ev.TaskID); t != "" {
				title += ": " + t
			}
			if n := patchFileCount(findingsFor(ev.ChangeSetID)); n > 0 {
				title += fmt.Sprintf(" (%d file%s)", n, plural(n))
			}
			out = append(out, domain.TimelineEntry{
				At: ev.At, Kind: "change_set_applied", Title: title,
				Ref: domain.TimelineRef{Type: "change_set", ID: ev.ChangeSetID},
			})
		case "change_set.reverted":
			out = append(out, domain.TimelineEntry{
				At: ev.At, Kind: "change_set_reverted", Title: ev.HumanID + " reverted",
				Ref: domain.TimelineRef{Type: "change_set", ID: ev.ChangeSetID},
			})
		case "change_set.status_changed":
			if !strings.HasSuffix(ev.Operation, "->"+string(domain.ChangeSetRejected)) {
				continue
			}
			findings := findingsFor(ev.ChangeSetID)
			detail := denialDetail(findings)
			// "denied" is reserved for a genuine system block (M3, review fix):
			// a plain check failure (build/tests) is orchestrator-actor too but
			// carries no DenyCode, so it now reads "rejected" like a human
			// reject would, not "denied" -- and the restart-reconcile sweep
			// (runtime.go, an interrupted run recovering with no findings of
			// its own) is named for what it is instead of reading as either.
			kind, verb := "change_set_rejected", "rejected"
			if hasSystemDenyCode(findings) {
				kind, verb = "change_set_denied", "denied"
			} else if detail == "" && ev.Actor == store.ActorOrchestrator {
				detail = "interrupted by a restart"
			}
			title := ev.HumanID + " " + verb
			if detail != "" {
				title += ": " + detail
			}
			out = append(out, domain.TimelineEntry{
				At: ev.At, Kind: kind, Title: title,
				Ref: domain.TimelineRef{Type: "change_set", ID: ev.ChangeSetID},
			})
		}
	}

	decEvents, err := db.ListDecisionEventsForInitiative(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	for _, ev := range decEvents {
		verb := "decided"
		if ev.EventType == "decision.reopened" {
			verb = "reopened"
		}
		title := ev.Code + " " + verb
		if ev.Question != "" {
			title += ": " + truncateWords(ev.Question, 80)
		}
		out = append(out, domain.TimelineEntry{
			At: ev.At, Kind: "decision_changed", Title: title,
			Ref: domain.TimelineRef{Type: "decision", ID: ev.DecisionID},
		})
	}

	ops, err := db.ListOperationsByInitiative(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	for _, op := range ops {
		var kind string
		switch op.Status {
		case domain.OperationFailed:
			kind = "run_failed"
		case domain.OperationCancelled:
			kind = "run_stopped"
		default:
			continue
		}
		at := op.CreatedAt
		if op.FinishedAt != nil {
			at = *op.FinishedAt
		}
		out = append(out, domain.TimelineEntry{
			At: at, Kind: kind, Title: humanizeOpType(op.Type), Detail: op.Error,
			Ref: domain.TimelineRef{Type: "operation", ID: op.ID},
		})
	}

	statusEvents, err := db.ListInitiativeStatusEvents(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	// lastPhase tracks the most recently emitted REAL phase (i.e. excluding
	// the running/failed noise skipped below), across statusEvents in
	// chronological order (ListInitiativeStatusEvents is seq ASC). Review #4,
	// ICP re-run: SetInitiativeStatus is an unconditional write with no
	// "did this actually change" guard, and the execute/apply/revert claim
	// mechanism (ClaimInitiativeRunning, then release back to priorStatus)
	// round-trips through it on EVERY build cycle -- plan_approved -> running
	// -> plan_approved -> running -> ... -- with the plan itself untouched.
	// Each release wrote its own initiative.status_changed(plan_approved)
	// event, and each one rendered its own "Plan approved" entry: the
	// timeline duplicated a milestone that happened exactly once. Only a
	// genuine CHANGE of phase is shown now; a release back to the phase the
	// initiative already occupied is not a new milestone.
	var lastPhase domain.InitiativeStatus
	for _, ev := range statusEvents {
		// M3, review fix: "->running" is a resume, not a completed phase (it
		// duplicated nothing, but read as a milestone it is not); "->failed"
		// is already reported once as a run_failed entry from the operations
		// loop above, and a second phase_completed entry for the same event
		// is a plain duplicate.
		to := ev.Operation
		if i := strings.LastIndex(ev.Operation, "->"); i >= 0 {
			to = ev.Operation[i+2:]
		}
		toStatus := domain.InitiativeStatus(to)
		switch toStatus {
		case domain.InitiativeRunning, domain.InitiativeFailed:
			continue
		}
		if toStatus == lastPhase {
			continue
		}
		lastPhase = toStatus
		out = append(out, domain.TimelineEntry{
			At: ev.At, Kind: "phase_completed", Title: humanizePhaseTransition(ev.Operation),
			Ref: domain.TimelineRef{Type: "initiative", ID: initiativeID},
		})
	}

	staleEntries, err := buildStaleTimelineEntries(ctx, db, initiativeID)
	if err != nil {
		return nil, err
	}
	out = append(out, staleEntries...)

	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })

	filtered := out[:0:0]
	for _, e := range out {
		if since > 0 && !e.At.After(time.UnixMilli(since)) {
			continue
		}
		filtered = append(filtered, e)
		if len(filtered) >= limit {
			break
		}
	}
	return filtered, nil
}

// buildStaleTimelineEntries groups an initiative's stale marks by the decision
// that caused them and the moment they were written (best-effort: markStaleTx
// calls nowMS() once per row, so rows from the same reopen normally share a
// millisecond; grouping by second tolerates the rare case where they do not,
// without merging two genuinely separate reopens of the same decision).
func buildStaleTimelineEntries(ctx context.Context, db *store.DB, initiativeID string) ([]domain.TimelineEntry, error) {
	marks, err := db.ListAllStaleMarks(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	type group struct {
		at         int64
		decisionID string
		byType     map[string]int
	}
	order := make([]string, 0)
	groups := map[string]*group{}
	for _, m := range marks {
		key := m.DecisionID + "|" + strconv.FormatInt(m.CreatedAt.Unix(), 10)
		g, ok := groups[key]
		if !ok {
			g = &group{at: m.CreatedAt.UnixMilli(), decisionID: m.DecisionID, byType: map[string]int{}}
			groups[key] = g
			order = append(order, key)
		}
		g.byType[m.TargetType]++
	}
	out := make([]domain.TimelineEntry, 0, len(order))
	for _, key := range order {
		g := groups[key]
		label := "a decision"
		if g.decisionID != "" {
			if d, err := db.GetDecision(ctx, g.decisionID); err == nil && d.Code != "" {
				label = d.Code
			}
		}
		total := 0
		parts := make([]string, 0, 3)
		for _, targetType := range []string{"requirement", "task", "change_set"} {
			if n := g.byType[targetType]; n > 0 {
				total += n
				parts = append(parts, fmt.Sprintf("%d %s%s", n, staleNoun(targetType), plural(n)))
			}
		}
		if total == 0 {
			continue
		}
		out = append(out, domain.TimelineEntry{
			At:     time.UnixMilli(g.at),
			Kind:   "stale",
			Title:  fmt.Sprintf("%s changed: %s may be invalid", label, strings.Join(parts, ", ")),
			Detail: fmt.Sprintf("%d item%s may be invalid", total, plural(total)),
			Ref:    domain.TimelineRef{Type: "decision", ID: g.decisionID},
		})
	}
	return out, nil
}

func staleNoun(targetType string) string {
	switch targetType {
	case "change_set":
		return "change set"
	default:
		return targetType
	}
}

// patchFileCount recovers the file count from a change set's patch_applies
// check finding ("N file(s) in patch", written by Engine.checkPatchApplies) --
// the count is not stored anywhere else, and reading it here keeps
// BuildTimeline store-only, with no dependency on the blob-backed patch body.
func patchFileCount(findings []domain.Finding) int {
	for _, f := range findings {
		if f.Category != "patch_applies" {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(f.Evidence, "%d file(s) in patch", &n); err == nil {
			return n
		}
	}
	return 0
}

// hasSystemDenyCode reports whether any of a change set's findings carries a
// machine-readable DenyCode (H3/M3, review fix): the signal that this
// rejection is a genuine system block (a secret, PII, a path escape, host
// exec, a blob mismatch, an unresolved blocking review finding) rather than
// a plain human reject or a plain check failure (build/tests findings never
// carry a Code, only a category and a severity) -- and rather than the
// restart-reconcile sweep's bare status transition, which writes no findings
// at all.
func hasSystemDenyCode(findings []domain.Finding) bool {
	for _, f := range findings {
		if f.Code != "" {
			return true
		}
	}
	return false
}

// denialDetail names why a change set was rejected or denied, from its own
// findings: a secret_sweep finding's path:line kind ("a credential at
// src/keys.js:14"), or else the first blocking finding's category. Returns ""
// when nothing explains it (e.g. a bare human reject with no findings).
func denialDetail(findings []domain.Finding) string {
	for _, f := range findings {
		if f.Code == domain.DenyCodeSecretSweep && f.SecretPath != "" {
			kind := f.SecretKind
			if kind == "" {
				kind = "a credential"
			} else {
				kind = "a " + kind
			}
			return kind + " at " + f.SecretPath
		}
	}
	for _, f := range findings {
		if f.Code != "" {
			if f.Path != "" {
				return f.Category + " at " + f.Path
			}
			return f.Category
		}
	}
	for _, f := range findings {
		if f.Severity == domain.SeverityCritical || f.Severity == domain.SeverityHigh {
			return f.Finding
		}
	}
	return ""
}

// humanizeOpType turns a stored operation type ("execute_initiative") into a
// human phrase ("Execute initiative") for a run_failed/run_stopped title.
func humanizeOpType(t string) string {
	words := strings.Split(t, "_")
	if len(words) == 0 || words[0] == "" {
		return t
	}
	words[0] = strings.ToUpper(words[0][:1]) + words[0][1:]
	return strings.Join(words, " ")
}

// phaseLabels names the initiative status an "->to" transition lands on, for a
// phase_completed timeline title. Unknown/unlisted targets fall back to the
// raw status name so a new status is still readable, just not polished.
var phaseLabels = map[domain.InitiativeStatus]string{
	domain.InitiativeApproved:           "PRD approved",
	domain.InitiativePlanApproved:       "Plan approved",
	domain.InitiativeComplete:           "Verification complete",
	domain.InitiativeNeedsHumanJudgment: "Ready for your judgment",
	domain.InitiativeRunning:            "Workflow resumed",
	domain.InitiativeFailed:             "Workflow failed",
}

// humanizePhaseTransition renders an initiative.status_changed event's
// "from->to" Operation as a phase_completed title.
func humanizePhaseTransition(op string) string {
	parts := strings.SplitN(op, "->", 2)
	to := op
	if len(parts) == 2 {
		to = parts[1]
	}
	if label, ok := phaseLabels[domain.InitiativeStatus(to)]; ok {
		return label
	}
	return "Phase completed: " + to
}

// plural returns "s" for n != 1, "" for n == 1.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// truncateWords trims s to at most n runes at a word boundary, appending an
// ellipsis when it had to cut.
func truncateWords(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	cut := n
	for cut > 0 && r[cut] != ' ' {
		cut--
	}
	if cut == 0 {
		cut = n
	}
	return strings.TrimSpace(string(r[:cut])) + "…"
}
