// "Since you were last here" (C1 / JTBD 6). The last-visit cursor is kept in
// localStorage, per initiative -- there is no server-side cursor for this
// (that's the separate, already-existing /changed + /seen mechanism used by
// DecisionPage, a different feature with its own project-wide cursor). Every
// localStorage access is wrapped in try/catch and degrades to "no cursor"
// rather than throwing: a private browsing window, a full quota, or a
// blocked accessor must not break the page around it.
import type { TimelineEntry } from "./timeline";

const KEY_PREFIX = "idea-harness:last-visit:";

export function getLastVisit(initiativeId: string): string | null {
  try {
    return window.localStorage.getItem(KEY_PREFIX + initiativeId);
  } catch {
    return null;
  }
}

export function setLastVisit(initiativeId: string, iso: string): void {
  try {
    window.localStorage.setItem(KEY_PREFIX + initiativeId, iso);
  } catch {
    // Storage unavailable or full -- "since you were last here" just won't
    // narrow next time. Nothing else on the page depends on this succeeding.
  }
}

// entriesSince is the pure filter behind "since you were last here": every
// entry strictly newer than `since`. A `null` cursor (first-ever visit, or
// storage was unavailable) means there is nothing to compare against, so it
// returns no entries rather than guessing "everything" -- a first-time view
// should not open with what would read as a huge, undifferentiated backlog.
export function entriesSince(entries: TimelineEntry[], since: string | null): TimelineEntry[] {
  if (!since) return [];
  const cursor = new Date(since).getTime();
  if (!Number.isFinite(cursor)) return [];
  return entries.filter((e) => {
    const at = new Date(e.at).getTime();
    return Number.isFinite(at) && at > cursor;
  });
}
