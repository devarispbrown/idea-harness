// Triage grouping (B4 / JTBD 2: "53 things need your judgment", then 82.
// Repeated questions. No way to accept the recommendations."). Pure
// functions so "which items are 'Needs you' vs collapsed 'Optional'" is
// unit-testable without a DOM.
//
// GET .../triage ranks every open decision/question/blocking finding and
// labels the top 3 `priority`, the rest `optional` (docs/openapi.yaml's
// TriageItem). DefinePage cross-references this against its own
// phase-scoped open items (judgmentScope.ts) so the phase scoping the
// review changes fixed is not undone by triage.

export interface TriageItemLike {
  id: string;
  rank: "priority" | "optional";
}

// groupTriage partitions the raw triage list itself (e.g. for a summary
// count). Defensive against a triage response that marks more than 3 items
// `priority` -- a client must still cap "Needs you" at 3 and push the
// overflow into "optional" rather than trusting the label unconditionally,
// so a future backend change to the cap doesn't silently blow up the UI's
// own "top 3" promise.
export function groupTriage<T extends TriageItemLike>(
  items: T[],
): { needsYou: T[]; optional: T[] } {
  const priority = items.filter((i) => i.rank === "priority");
  const rest = items.filter((i) => i.rank !== "priority");
  const needsYou = priority.slice(0, 3);
  const overflow = priority.slice(3);
  return { needsYou, optional: [...overflow, ...rest] };
}

// partitionByTriage splits a phase-scoped list of items (DefinePage's own
// open decisions/findings/questions, already filtered by judgmentScope.ts)
// into the ones triage marked `priority` and everything else. Matching is by
// id, and an item triage never mentions at all (triage unreachable, or an
// item type triage doesn't rank) falls into `optional` -- fail open to
// "collapsed, not lost", never fail closed to "vanished".
export function partitionByTriage<T extends { id: string }>(
  items: T[],
  triage: TriageItemLike[],
): { needsYou: T[]; optional: T[] } {
  const priorityIds = new Set(triage.filter((t) => t.rank === "priority").map((t) => t.id));
  const needsYou: T[] = [];
  const optional: T[] = [];
  for (const item of items) {
    (priorityIds.has(item.id) ? needsYou : optional).push(item);
  }
  return { needsYou, optional };
}

// optionalLabel is the collapsed section's heading text.
export function optionalLabel(n: number): string {
  return `Optional — the recommendation will be used (${n})`;
}
