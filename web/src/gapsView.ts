// Pure logic for the full Gaps page (#/i/<id>/gaps); GapsPage.tsx renders
// what these return. Like gaps.ts, nothing here switches exhaustively on
// `kind`: the kind list is open-ended (objective/outcome kinds are being
// added), so a kind these maps do not know gets the generic label and the CLI
// command the daemon already wrote in `next`.

import { gapKindLabel, type Gap } from "./gaps";

export const SEVERITIES = ["critical", "high", "medium", "low", "observation"];

// severityRank orders severities most severe first; an unknown one sorts last.
export function severityRank(s: string): number {
  const i = SEVERITIES.indexOf(s);
  return i === -1 ? SEVERITIES.length : i;
}

// Readable names for the kinds known today. Anything missing falls back to
// gapKindLabel's mechanical "Snake case → Sentence".
const KIND_TITLES: Record<string, string> = {
  unimplemented_requirement: "Requirements nothing implements",
  unverified_requirement: "Requirements without verification",
  untested_acceptance_criterion: "Acceptance criteria without evidence",
  unvalidated_assumption: "Unvalidated assumptions",
  invalidated_decision: "Decisions whose basis moved",
  orphan_task: "Tasks that serve no requirement",
  orphan_change_set: "Change sets with no task",
  stale_artifact: "Stale documents",
};

export function gapKindTitle(kind: string): string {
  return KIND_TITLES[kind] ?? gapKindLabel(kind);
}

export interface GapGroup {
  kind: string;
  title: string;
  gaps: Gap[];
  worst: string;
}

// groupGapsByKind buckets gaps by kind, most severe group first (then the
// larger group, then by kind for a stable order). Within a group the daemon's
// order -- most severe first -- is kept.
export function groupGapsByKind(gaps: Gap[]): GapGroup[] {
  const by = new Map<string, Gap[]>();
  for (const g of gaps ?? []) {
    const list = by.get(g.kind) ?? [];
    list.push(g);
    by.set(g.kind, list);
  }
  const groups: GapGroup[] = [];
  for (const [kind, list] of by) {
    const worst = list.map((g) => g.severity).sort((a, b) => severityRank(a) - severityRank(b))[0] ?? "";
    groups.push({ kind, title: gapKindTitle(kind), gaps: list, worst });
  }
  return groups.sort(
    (a, b) =>
      severityRank(a.worst) - severityRank(b.worst) || b.gaps.length - a.gaps.length || a.kind.localeCompare(b.kind),
  );
}

export interface GapFilter {
  kind?: string; // "" or undefined = every kind
  severity?: string; // "" or undefined = every severity
}

export function filterGaps(gaps: Gap[], f: GapFilter): Gap[] {
  return (gaps ?? []).filter((g) => (!f.kind || g.kind === f.kind) && (!f.severity || g.severity === f.severity));
}

// countBy tallies a field over the gaps, for the filter options' counts.
export function countBy(gaps: Gap[], field: "kind" | "severity"): Record<string, number> {
  const out: Record<string, number> = {};
  for (const g of gaps ?? []) out[g[field]] = (out[g[field]] ?? 0) + 1;
  return out;
}

// GapAction is how the page closes a gap: a link to the page where the UI can
// do it, an inline judgment it can record right there (assumptions), or --
// when the UI has no way -- the CLI command to copy.
export type GapAction =
  | { kind: "link"; label: string; href: string }
  | { kind: "assertion"; assertionId: string }
  | { kind: "cli"; command: string };

const firstNode = (g: Gap, type: string) => (g.nodes ?? []).find((n) => n.type === type);

// Kinds a page of this UI closes, by page. Lookups, not a switch: a kind not
// listed falls through to the CLI.
const KIND_PAGES: Record<string, { label: string; page: string }> = {
  unimplemented_requirement: { label: "Add a task on Plan", page: "plan" },
  orphan_task: { label: "Link it on Plan", page: "plan" },
  unverified_requirement: { label: "Record evidence on Verify", page: "verify" },
  untested_acceptance_criterion: { label: "Record evidence on Verify", page: "verify" },
  orphan_change_set: { label: "Open Build", page: "build" },
};

// gapAction picks the closing action for one gap. A listed kind missing the
// node it needs falls through as well, to the daemon's own `next` command.
export function gapAction(g: Gap, initiativeId: string): GapAction {
  const base = `#/i/${initiativeId}`;
  if (g.kind === "unvalidated_assumption") {
    const a = firstNode(g, "assertion");
    if (a) return { kind: "assertion", assertionId: a.id };
  }
  if (g.kind === "invalidated_decision") {
    const d = firstNode(g, "decision");
    if (d) return { kind: "link", label: `Open ${d.code}`, href: `${base}/decision/${d.id}` };
  }
  const hit = KIND_PAGES[g.kind];
  if (hit) return { kind: "link", label: hit.label, href: `${base}/${hit.page}` };
  // Kinds about the outcome end of the chain (objectives, metrics) close on
  // the Outcomes dashboard, whatever they end up being called.
  const t = g.nodes?.[0]?.type;
  if (t === "objective" || t === "metric") return { kind: "link", label: "Open Outcomes", href: `${base}/outcomes` };
  return { kind: "cli", command: g.next };
}
