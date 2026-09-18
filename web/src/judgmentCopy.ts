// Judgment copy that branches on judgment_reason or status (D2), extracted
// as pure functions so the wording rules are unit-testable without a DOM.

// showsRevisePrd reports whether Define's "Revise PRD to vN →" action belongs
// on screen. All four judgment reasons (prd_review, plan_dependencies,
// plan_review, verification) share the same `needs_human_judgment` status, so
// status alone cannot tell a stalled PRD review from a stalled plan review;
// only the reason can.
//
// H3: this used to also treat undefined/"" as prd_review, on the theory that
// it was legacy data predating judgment_reason. In practice App.tsx was
// simply never threading `judgment_reason` through to this page at all, so
// EVERY park -- including a plan-review or plan-dependencies park -- arrived
// here as undefined and showed "Revise PRD", a wrong and destructive-looking
// action on a PRD that was not actually what was parked. Now that the reason
// is wired through, an absent reason fails closed: only an explicit
// "prd_review" shows the action, so a bug that drops the reason again hides
// the button instead of mis-showing it.
export function showsRevisePrd(reason: string | undefined): boolean {
  return reason === "prd_review";
}

// openItemsNote is the text under Define's "N things need your judgment"
// heading. Once the PRD is already approved, unresolved items (an unanswered
// question, say) are optional cleanup, not a blocker — restating "this will
// revise the PRD" there contradicts the APPROVED pill shown next to it.
export function openItemsNote(status: string, nextVersion: number): string {
  return status === "approved"
    ? "Optional: unanswered items use the recommendation"
    : `then the PRD revises to v${nextVersion} and Design unlocks`;
}

// PLAN_LOCKED_STATUSES are every status reachable only once a plan has been
// approved. Once approved it is the fixed input Build executes against;
// editing a task there would silently rewrite what was approved without
// another review cycle. This used to check only "plan_approved", so a
// completed initiative -- unambiguously post-approval -- still showed live
// Edit/Delete controls on a plan that had already been executed.
const PLAN_LOCKED_STATUSES = new Set(["plan_approved", "complete"]);

// planTaskEditable reports whether a plan task's Edit/Delete controls should
// render.
export function planTaskEditable(status: string): boolean {
  return !PLAN_LOCKED_STATUSES.has(status);
}
