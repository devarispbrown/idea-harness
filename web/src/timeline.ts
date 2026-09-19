// Timeline formatting (C1 / JTBD 6: "no useful 'what changed'"). Pure
// functions over the wave-3 wire contract so relative-time and grouping
// logic is unit-testable without a DOM.
//
// GET .../initiatives/{id}/timeline returns
//   [{at, kind, title, detail, ref:{type,id}}], newest first, where kind is
//   one of change_set_applied, change_set_rejected, change_set_denied,
//   change_set_reverted, run_failed, run_stopped, decision_changed, stale,
//   phase_completed.
export interface TimelineRef {
  type: string;
  id: string;
}

export interface TimelineEntry {
  at: string;
  kind: string;
  title: string;
  detail?: string;
  ref?: TimelineRef;
}

// KIND_LABEL is a short badge word per kind. `title`/`detail` from the
// backend are already human language (C1) -- this is only the small badge
// shown alongside them, so an entry never appears as a bare, unlabeled row.
// An unrecognized kind (a future addition) still renders, humanized from its
// snake_case form, rather than being dropped -- the same "nothing may be
// silently unshown" rule as judgmentScope.ts's otherOpenFindings.
const KIND_LABEL: Record<string, string> = {
  change_set_applied: "Applied",
  change_set_rejected: "Rejected",
  change_set_denied: "Denied",
  change_set_reverted: "Reverted",
  run_failed: "Run failed",
  run_stopped: "Run stopped",
  decision_changed: "Decision changed",
  stale: "Stale",
  phase_completed: "Phase completed",
};

export function timelineKindLabel(kind: string): string {
  if (KIND_LABEL[kind]) return KIND_LABEL[kind];
  if (!kind) return "Update";
  return kind
    .split("_")
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(" ");
}

// relativeTime renders a past ISO timestamp as "just now" / "5m ago" /
// "3h ago" / "2d ago", falling back to the plain date once it's old enough
// that a relative phrase stops being useful. `now` is threaded through
// (rather than read internally) so this is deterministic under test.
export function relativeTime(at: string, now: Date): string {
  const then = new Date(at);
  const ms = now.getTime() - then.getTime();
  if (!Number.isFinite(ms) || ms < 0) return "just now";
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return "just now";
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  if (days < 7) return `${days}d ago`;
  return then.toISOString().slice(0, 10);
}

// describeTimelineEntry is the one-line human sentence for an entry: its
// badge, its title, and its detail when the backend supplied one.
export function describeTimelineEntry(entry: TimelineEntry): string {
  const label = timelineKindLabel(entry.kind);
  const base = `${label}: ${entry.title}`;
  return entry.detail ? `${base} — ${entry.detail}` : base;
}

// dayKey buckets an entry's timestamp to its calendar day (UTC, "YYYY-MM-DD")
// for the "What changed" view's day-grouped list.
export function dayKey(at: string): string {
  return new Date(at).toISOString().slice(0, 10);
}

export interface TimelineDayGroup {
  day: string;
  entries: TimelineEntry[];
}

// groupByDay groups already-newest-first entries into day buckets, keeping
// each bucket's internal order and the days themselves in the order their
// first entry appeared (so a newest-first input stays newest-day-first).
export function groupByDay(entries: TimelineEntry[]): TimelineDayGroup[] {
  const order: string[] = [];
  const buckets = new Map<string, TimelineEntry[]>();
  for (const entry of entries) {
    const key = dayKey(entry.at);
    if (!buckets.has(key)) {
      buckets.set(key, []);
      order.push(key);
    }
    buckets.get(key)!.push(entry);
  }
  return order.map((day) => ({ day, entries: buckets.get(day)! }));
}
