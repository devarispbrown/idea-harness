// Product Graph gaps (GET .../gaps, `idea gaps`) for the Health page. Pure
// functions so the wording is unit-testable without a DOM. The report shape is
// docs/openapi.yaml's GapReport; `nodes[0]` is the node a gap is about and
// `next` the command that closes it. The kind list is open-ended (new kinds
// are added, never renamed), so nothing here switches on `kind`.

export interface GapNode {
  type: string;
  id: string;
  code: string;
  label?: string;
}

export interface Gap {
  kind: string;
  state: string;
  severity: string;
  nodes: GapNode[];
  why: string;
  next: string;
  dependents: number;
  initiative_id: string;
}

export interface GapReport {
  initiative_id?: string;
  gaps: Gap[];
  summary: { total: number; by_severity: Record<string, number>; by_kind: Record<string, number> };
}

const severities = ["critical", "high", "medium", "low", "observation"];

// gapSummaryLine renders "2 high · 1 low" (zeros skipped), or "" for none.
export function gapSummaryLine(by: Record<string, number> | undefined): string {
  return severities
    .filter((s) => (by?.[s] ?? 0) > 0)
    .map((s) => `${by![s]} ${s}`)
    .join(" · ");
}

// gapKindLabel turns `unverified_requirement` into "Unverified requirement".
export function gapKindLabel(kind: string): string {
  const words = kind.replace(/_/g, " ");
  return words.charAt(0).toUpperCase() + words.slice(1);
}

// gapSubject is the code a row leads with: the first node's, or the kind when
// a gap (never, today) names no node.
export function gapSubject(g: Gap): string {
  return g.nodes?.[0]?.code ?? g.kind;
}

// topGaps keeps the first n -- the daemon already sorts most severe first --
// and says how many were left out, so a long list stays a glance.
export function topGaps(gaps: Gap[], n: number): { shown: Gap[]; hidden: number } {
  const list = gaps ?? [];
  return { shown: list.slice(0, n), hidden: Math.max(0, list.length - n) };
}
