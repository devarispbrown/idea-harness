// Pure logic for the review convergence view on Define (the PRD review loop)
// and Plan (the execution-plan review), read from
// GET .../review-telemetry -- one record per review cycle, persisted on the
// adversarial reviewer's run manifest (domain.ReviewTelemetry).

export interface ReviewTelemetry {
  review: string; // "prd" | "execution_plan" (open-ended)
  cycle: number;
  mode: string;
  rigor?: string;
  raised?: Record<string, number>;
  new?: Record<string, number>;
  reraised?: Record<string, number>;
  resolved?: Record<string, number>;
  demoted?: Record<string, number>;
  deterministic: number;
  open_blocking: number;
  prev_open_blocking: number;
  outcome: string;
}

export interface ReviewLoop {
  review: string;
  cycles: ReviewTelemetry[];
  // The last cycle's outcome: how this loop ended (or where it stands).
  outcome: string;
}

// groupLoops splits the history into loops. A human revise starts a new loop
// at cycle 1, so a loop ends where the cycle number stops increasing (or the
// review changes); the input is oldest first, as the daemon sends it.
export function groupLoops(tel: ReviewTelemetry[], review?: string): ReviewLoop[] {
  const loops: ReviewLoop[] = [];
  let cur: ReviewLoop | null = null;
  for (const t of tel ?? []) {
    if (review && t.review !== review) continue;
    const last: ReviewTelemetry | undefined = cur?.cycles[cur.cycles.length - 1];
    if (!cur || cur.review !== t.review || !last || t.cycle <= last.cycle) {
      cur = { review: t.review, cycles: [], outcome: "" };
      loops.push(cur);
    }
    cur.cycles.push(t);
    cur.outcome = t.outcome;
  }
  return loops;
}

const SEV = ["critical", "high", "medium", "low", "observation"];

// sevEntries lists a per-severity map's non-zero counts, most severe first,
// with unknown severities after the known ones.
export function sevEntries(m: Record<string, number> | undefined): [string, number][] {
  const e = Object.entries(m ?? {}).filter(([, n]) => n > 0);
  const rank = (s: string) => (SEV.indexOf(s) === -1 ? SEV.length : SEV.indexOf(s));
  return e.sort((a, b) => rank(a[0]) - rank(b[0]) || a[0].localeCompare(b[0]));
}

export function total(m: Record<string, number> | undefined): number {
  return Object.values(m ?? {}).reduce((s, n) => s + n, 0);
}

// sevSummary renders "2 high · 1 low", or "none".
export function sevSummary(m: Record<string, number> | undefined): string {
  const e = sevEntries(m);
  return e.length ? e.map(([s, n]) => `${n} ${s}`).join(" · ") : "none";
}

// demotedSummary renders "uncited ×2 · reraise_without_justification ×1".
export function demotedSummary(m: Record<string, number> | undefined): string {
  const e = Object.entries(m ?? {}).filter(([, n]) => n > 0);
  return e.map(([rule, n]) => `${rule.replace(/_/g, " ")} ×${n}`).join(" · ");
}

const OUTCOME_LABELS: Record<string, string> = {
  converged: "converged",
  revise: "revised",
  stalled: "stalled",
  max_cycles: "hit max cycles",
  blocked: "blocked",
};

export function outcomeLabel(o: string): string {
  return OUTCOME_LABELS[o] ?? o.replace(/_/g, " ");
}

// outcomeTone: converged is good; another revise is in progress; anything
// that ends a loop without converging needs a human. Unknown outcomes get
// the neutral tone, never the good one.
export function outcomeTone(o: string): "ok" | "warn" | "bad" | "muted" {
  if (o === "converged") return "ok";
  if (o === "revise") return "warn";
  if (o === "stalled" || o === "max_cycles" || o === "blocked") return "bad";
  return "muted";
}

// blockingTrend says how the open blocking count moved this cycle, or "" on a
// loop's first cycle (prev_open_blocking is -1 there).
export function blockingTrend(t: Pick<ReviewTelemetry, "open_blocking" | "prev_open_blocking">): string {
  if (t.prev_open_blocking < 0) return "";
  const d = t.open_blocking - t.prev_open_blocking;
  if (d < 0) return `↓${-d}`;
  if (d > 0) return `↑${d}`;
  return "no change";
}

export function reviewTitle(review: string): string {
  if (review === "prd") return "PRD review";
  if (review === "execution_plan") return "Plan review";
  return review.replace(/_/g, " ");
}
