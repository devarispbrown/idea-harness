// Judgment copy that branches on judgment_reason or status (D2), extracted
// as pure functions so the wording rules are unit-testable without a DOM.

// showsRevisePrd reports whether Define's "Revise PRD to vN →" action belongs
// on screen. All four judgment reasons (prd_review, plan_dependencies,
// plan_review, verification) share the same `needs_human_judgment` status, so
// status alone cannot tell a stalled PRD review from a stalled plan review;
// only the reason can. Undefined/"" is legacy data recorded before
// judgment_reason existed, and is treated as prd_review — the daemon itself
// falls back the same way (see PlanPage's `awaitingApproval`). Typed as a
// plain string, matching how App.tsx carries `initiative.judgment_reason`
// through to this page, rather than re-declaring the domain's enum here.
export function showsRevisePrd(reason: string | undefined): boolean {
  return !reason || reason === "prd_review";
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

// planTaskEditable reports whether a plan task's Edit/Delete controls should
// render. Once a plan is approved it is the fixed input Build executes
// against; editing a task there would silently rewrite what was approved
// without another review cycle.
export function planTaskEditable(status: string): boolean {
  return status !== "plan_approved";
}
