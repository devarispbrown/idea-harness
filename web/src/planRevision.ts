// What a plan revision changed, in words. Pure so it is testable without a
// DOM: Plan takes a snapshot when the revision is asked for and compares it
// with the plan once the revision's operation settles.

export interface PlanSnapshot {
  version: number;
  codes: string[];
}

// revisionOutcome says what the revision did. The case that matters most is
// "nothing": the daemon keeps an identical plan at its version (and its task
// codes), so without this line a revision that changed nothing looked like a
// click that did nothing at all.
export function revisionOutcome(before: PlanSnapshot, after: PlanSnapshot): string {
  const kept = new Set(before.codes);
  const added = after.codes.filter((c) => !kept.has(c));
  const now = new Set(after.codes);
  const removed = before.codes.filter((c) => !now.has(c));
  if (after.version === before.version) {
    if (added.length === 0 && removed.length === 0) {
      return `The revision produced the same plan: still v${after.version}, tasks unchanged.`;
    }
    return `The plan text is unchanged (still v${after.version}), but its tasks changed${taskDelta(added, removed)}.`;
  }
  return `Plan revised: v${before.version} → v${after.version}${taskDelta(added, removed)}.`;
}

function taskDelta(added: string[], removed: string[]): string {
  const parts: string[] = [];
  if (added.length) parts.push(`added ${added.join(", ")}`);
  if (removed.length) parts.push(`removed ${removed.join(", ")}`);
  return parts.length ? ` (${parts.join("; ")})` : ", same tasks";
}
