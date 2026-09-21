// Stale-items banner text and per-item display (A4 #5 / JTBD 5: "nothing
// flagged what a changed decision made invalid"). Pure functions so the
// wording is unit-testable without a DOM. Rendered app-shell-wide in
// App.tsx, from GET .../guidance's `stale_items` (StaleItemRef[]):
//   { stale_id, type: "requirement"|"task"|"change_set", id, label?, reason }
// `reason` is a sentence like "DEC-9 changed" (see docs/openapi.yaml).
import { phaseHref, type PhaseName } from "./router";

export interface StaleItemRef {
  stale_id: string;
  type: string;
  id: string;
  label?: string;
  reason: string;
}

// decisionCodesFromReasons pulls the unique decision codes ("DEC-9") out of
// each item's `reason` sentence, in first-seen order. A reason that doesn't
// match the expected "<CODE> changed" shape (a future wording, or a reason
// about something other than a decision) contributes nothing rather than a
// garbled fragment.
export function decisionCodesFromReasons(items: StaleItemRef[]): string[] {
  const seen = new Set<string>();
  const codes: string[] = [];
  for (const item of items) {
    const m = /^(\S+)\s+changed\b/.exec(item.reason ?? "");
    if (m && !seen.has(m[1])) {
      seen.add(m[1]);
      codes.push(m[1]);
    }
  }
  return codes;
}

// joinWithAnd renders ["DEC-9"] -> "DEC-9", ["DEC-9","DEC-11"] ->
// "DEC-9 and DEC-11", and three or more with an Oxford comma before "and".
function joinWithAnd(items: string[]): string {
  if (items.length === 0) return "";
  if (items.length === 1) return items[0];
  if (items.length === 2) return `${items[0]} and ${items[1]}`;
  return `${items.slice(0, -1).join(", ")}, and ${items[items.length - 1]}`;
}

// staleBannerText is the app-shell banner: "N items may be invalid since
// DEC-9 changed". Falls back to a decision-free sentence when no reason
// parses (never silently shows nothing for a non-empty stale_items list).
export function staleBannerText(items: StaleItemRef[]): string {
  const n = items.length;
  const noun = n === 1 ? "item" : "items";
  const codes = decisionCodesFromReasons(items);
  if (codes.length === 0) return `${n} ${noun} may be invalid`;
  return `${n} ${noun} may be invalid since ${joinWithAnd(codes)} changed`;
}

// staleItemLabel is one row's display text in the expanded list: its human
// code when the graph node could be resolved, its bare type otherwise (a
// requirement/task/change set deleted since the mark was made), then why.
export function staleItemLabel(item: StaleItemRef): string {
  const who = item.label || item.type;
  return `${who} — ${item.reason}`;
}

const STALE_TYPE_PHASE: Record<string, PhaseName> = {
  requirement: "verify",
  task: "plan",
  change_set: "build",
};

// staleItemHref points at the phase page that owns this kind of node:
// requirements on Verify, tasks on Plan, change sets on Build. An
// unrecognized type (future graph node kind) has no known page, so this
// returns "" and the caller renders the row without a link rather than a
// broken one.
export function staleItemHref(initiativeId: string, type: string): string {
  const phase = STALE_TYPE_PHASE[type];
  return phase ? phaseHref(initiativeId, phase) : "";
}
