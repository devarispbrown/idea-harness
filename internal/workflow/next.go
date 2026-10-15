package workflow

import (
	"context"
	"errors"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// NextAction returns the deterministic next step for an initiative (§52, Track
// C). It stops at human judgment points (review / apply / resolve / done) and
// otherwise names the next automatable step (advance / execute / verify).
// `failed` and `wait` are terminal-for-now answers rather than steps: the first
// is an error the caller must surface, the second means an operation is in
// flight.
func (e *Engine) NextAction(ctx context.Context, initiativeID string) (string, error) {
	ini, err := e.db.GetInitiative(ctx, initiativeID)
	if err != nil {
		return "", err
	}
	switch ini.Status {
	case domain.InitiativeApproved:
		return "advance", nil
	case domain.InitiativePlanApproved:
		changes, err := e.db.ListChangeSets(ctx, initiativeID)
		if err != nil {
			return "", err
		}
		// Human judgment points first: review → apply → verify.
		for _, cs := range changes {
			if cs.Status == domain.ChangeSetReviewRequired {
				return "review " + e.changeSetRef(ctx, cs), nil
			}
		}
		for _, cs := range changes {
			if cs.Status == domain.ChangeSetApproved {
				return "apply " + e.changeSetRef(ctx, cs), nil
			}
		}
		for _, cs := range changes {
			// A NoOp applied change set touched nothing (ICP re-run defect
			// #2): it never counts as "there is something built to verify".
			if cs.ImplementsTask() {
				return "verify", nil
			}
		}
		// Only propose execute when there is something to execute. This used to
		// return "execute" unconditionally, so an initiative whose tasks were
		// all done -- or whose remaining tasks were blocked on unsatisfied
		// dependencies -- was told to execute and then failed in the background.
		// `idea next --until-human` drives itself from this string, so once the
		// execute handler gained a precondition (audit finding b) an
		// unconditional "execute" would have turned that background failure into
		// a hard error out of the loop.
		if _, err := e.nextReadyTask(ctx, initiativeID); err != nil {
			// Only "nothing runnable" means verify. Every other error
			// nextReadyTask can return is a storage failure, and swallowing one
			// here would answer 200 {"action":"verify"} on a broken database --
			// sending the operator to debug verification instead of storage.
			if !errors.Is(err, ErrNoReadyTask) {
				return "", err
			}
			return "verify", nil
		}
		return "execute", nil
	case domain.InitiativeNeedsHumanJudgment:
		// The reason is the whole token. Six code paths produce this status and
		// they want different things: a stalled PRD review is approved or
		// revised, a plan whose dependencies never resolved can only be
		// regenerated, and a requirement failed after completion is re-verified.
		// Before the reason was recorded, every client guessed from the presence
		// of the execution-plan artifact -- which runExecutionPlan writes
		// *before* the dependency check, so the guess said "approve the plan"
		// for a plan already known to be incomplete.
		switch ini.JudgmentReason {
		case domain.JudgmentPRDReview:
			return "resolve prd", nil
		case domain.JudgmentPlanDependencies:
			return "resolve plan-dependencies", nil
		case domain.JudgmentPlanReview:
			return "resolve plan", nil
		case domain.JudgmentVerification:
			return "resolve verification", nil
		case domain.JudgmentBudget:
			// Raising the cap is the only way on; it returns the initiative
			// to where it was, and the step is then run again from there.
			return "resolve budget", nil
		}
		// Recorded before this was introduced, so the reason is empty. The bare
		// token is what the CLI rendered for every case until now; keep it
		// rather than inventing a specific instruction we cannot support.
		return "resolve", nil
	case domain.InitiativeComplete:
		return "done", nil
	case domain.InitiativeFailed:
		// Split from `wait`. Collapsing the two told a failed initiative the
		// workflow was still running: `idea next` printed "workflow running --
		// try `idea watch`" and exited 0, forever, while `idea execute`
		// correctly pointed back at `idea next` and `idea watch` needed a TTY.
		// A closed recovery loop, reported as success. Found by audit 5, shipped
		// unfixed, found again by audit 6.
		return "failed", nil
	default: // running
		return "wait", nil
	}
}

// NextTask is the task Execute would run next, projected for an API response
// without exposing the full domain.Task shape (the Build contract's
// `next_task`).
type NextTask struct {
	ID      string `json:"id"`
	HumanID string `json:"human_id"`
	Title   string `json:"title"`
}

// NextReadyTask reports the task Execute would run next for initiativeID, or
// nil when there is none.
//
// Deliberately lock-free, unlike Execute itself: a poll-driven UI (the Build
// page) calling this on every refresh must not block for the whole duration
// of an in-flight Execute, which holds e.mu for as long as the agent
// container runs. A slightly stale answer is an acceptable cost for a
// read-only hint; Execute's own claim is still what actually enforces
// correctness when the button is clicked.
func (e *Engine) NextReadyTask(ctx context.Context, initiativeID string) (*NextTask, error) {
	t, err := e.nextReadyTask(ctx, initiativeID)
	if err != nil {
		if errors.Is(err, ErrNoReadyTask) {
			return nil, nil
		}
		return nil, err
	}
	return &NextTask{ID: t.ID, HumanID: t.Code, Title: t.Title}, nil
}

// PlanNotReadyForExecution returns why an initiative waiting on a human
// cannot have its tasks run yet, or "".
//
// needs_human_judgment covers parks before the plan is approved (the PRD or
// the plan under review) as well as after (verification). Execution was
// allowed from all of them, so from a plan-review park it ran tasks from an
// unapproved plan -- and left the initiative where approve-plan and
// revise-plan were both refused. The Build page then offered that one click
// away.
func PlanNotReadyForExecution(ini domain.Initiative) string {
	if ini.Status != domain.InitiativeNeedsHumanJudgment {
		return ""
	}
	switch ini.JudgmentReason {
	case domain.JudgmentPRDReview:
		return "The PRD is waiting for your review; approve it and the plan first"
	case domain.JudgmentPlanReview, domain.JudgmentPlanDependencies:
		return "The plan is waiting for your approval; approve it on the Plan page first"
	case domain.JudgmentBudget:
		return "This initiative stopped at its spending cap; raise the cap (`idea budget " + ini.ID + " --set <usd>`) first"
	}
	return ""
}

// RunBlockedReason reports why Execute would refuse to run right now for
// initiativeID, or "" when it would not (the Build contract's
// `run_blocked_reason`).
//
// It mirrors Execute's own guards read-only and lock-free (see NextReadyTask)
// so the Build page can grey out "Run next task" and say why, instead of
// letting the click land on a 409/400 it has to parse. It does NOT check
// executor readiness (image/key) -- that lives in the daemon package, which
// alone knows about Docker and the project's .env; the caller composes both.
func (e *Engine) RunBlockedReason(ctx context.Context, initiativeID string) (string, error) {
	ini, err := e.db.GetInitiative(ctx, initiativeID)
	if err != nil {
		return "", err
	}
	if ini.Status == domain.InitiativeRunning {
		return "An operation is already running", nil
	}
	if reason := PlanNotReadyForExecution(ini); reason != "" {
		return reason, nil
	}
	switch ini.Status {
	case domain.InitiativePlanApproved, domain.InitiativeNeedsHumanJudgment:
	case domain.InitiativeFailed:
		return "This initiative failed; retry it before running tasks", nil
	case domain.InitiativeComplete:
		return "This initiative is complete", nil
	default:
		return "The plan is not approved yet", nil
	}
	task, err := e.nextReadyTask(ctx, initiativeID)
	if err != nil {
		if errors.Is(err, ErrNoReadyTask) {
			// Nothing to run is not a blocked run: the Build page simply has
			// no button to show, which next_task == nil already conveys.
			return "", nil
		}
		return "", err
	}
	if err := e.refuseWhileAnotherChangeSetIsOpen(ctx, initiativeID, task.ID); err != nil {
		return strings.TrimPrefix(err.Error(), "workflow: execute: "), nil
	}
	return "", nil
}

// changeSetRef is the reference `idea next` should print for a change set.
//
// The human id is the readable one and is right almost always. But human ids
// are assigned per initiative while apply/review/change resolve project-wide,
// so once a project has two initiatives each holding a CHG-1, `idea apply
// CHG-1` refuses as ambiguous -- and `next` went on prescribing it, exiting 0,
// making no progress, with the full id printed by no human-readable command.
// The refusal now lists the candidates; this stops next from sending anyone
// there in the first place.
//
// A failed count falls back to the human id: a prescription that is usually
// right beats surfacing a database error from a hint.
func (e *Engine) changeSetRef(ctx context.Context, cs domain.ChangeSet) string {
	n, err := e.db.CountChangeSetsByHumanID(ctx, cs.HumanID)
	if err == nil && n > 1 {
		return cs.ID
	}
	return cs.HumanID
}
