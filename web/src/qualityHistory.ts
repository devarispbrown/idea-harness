// Pure logic for the Quality page (#/i/<id>/quality), which expands the
// QualityScorecard card: per dimension, its mode and status, every check with
// its method and citations, the requirements it is evidence for, and its
// measurement history (GET .../quality/history). The scorecard rules
// themselves (what is a pass, what "not measured" looks like) stay in
// quality.ts; this adds only what the full page needs.

import type { QualityCheck, QualityCitation, QualityResult } from "./quality";

export interface HistoryResult extends QualityResult {
  dimension: string;
  mode?: string;
  measurement_id?: string;
  created_at?: string;
}

// historyByDimension groups the oldest-first history by dimension, keeping
// that order inside each group.
export function historyByDimension(results: HistoryResult[]): Record<string, HistoryResult[]> {
  const out: Record<string, HistoryResult[]> = {};
  for (const r of results ?? []) (out[r.dimension] ??= []).push(r);
  return out;
}

// citationText renders one citation as path:line (REQ-n), path alone, or the
// requirement alone.
export function citationText(c: QualityCitation): string {
  let s = c.path ?? "";
  if (c.path && c.line) s += `:${c.line}`;
  if (c.requirement) s += s ? ` (${c.requirement})` : c.requirement;
  return s;
}

// sortedChecks puts the checks needing attention first (fail, then warn and
// anything unknown, then pass, then not applicable), stable otherwise.
export function sortedChecks(checks: QualityCheck[] | undefined): QualityCheck[] {
  const rank = (s: string) => (s === "fail" ? 0 : s === "pass" ? 2 : s === "not_applicable" ? 3 : 1);
  return [...(checks ?? [])].map((c, i) => ({ c, i })).sort((a, b) => rank(a.c.status) - rank(b.c.status) || a.i - b.i).map((x) => x.c);
}

// checkTone maps one check's status to a chip tone.
export function checkTone(status: string): "ok" | "warn" | "bad" | "muted" {
  if (status === "pass") return "ok";
  if (status === "fail") return "bad";
  if (status === "not_applicable") return "muted";
  return "warn";
}

// historyStrip turns a dimension's history into one tone per measurement,
// oldest first, for the small status strip; at most `max` of the newest.
export function historyStrip(results: HistoryResult[], max = 12): { id: string; status: string; tone: string; at?: string }[] {
  return (results ?? []).slice(-max).map((r) => ({ id: r.id, status: r.status, tone: checkTone(r.status), at: r.created_at }));
}

export function dimensionTitle(d: string): string {
  const w = d.replace(/_/g, " ");
  return w.charAt(0).toUpperCase() + w.slice(1);
}
