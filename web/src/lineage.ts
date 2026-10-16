// Pure logic for the Product Graph lineage surfaces: the LineageView page
// (#/i/<id>/lineage/<type>/<ref>) and the compact LineagePanel embedded on
// Define/Plan/Build/decision/objective rows. Both read the daemon's
// GET .../trace (the `why` + `impact` edge lists behind `idea why`, `idea
// impact` and `idea trace`); nothing here touches the DOM, so the tree
// shaping, label splitting and reference resolution are unit-tested in
// lineage.test.ts.

import { ARTIFACT_TITLES } from "./phases";

export interface TraceEdge {
  edge_type: string;
  from_type: string;
  from_id: string;
  from_label?: string;
  to_type: string;
  to_id: string;
  to_label?: string;
}

export interface ImpactEdge extends TraceEdge {
  // "stale" (an active stale mark), "revalidate" or "obsolete" -- the
  // daemon's classification (workflow.stalenessForNode). Open-ended: an
  // unknown value is rendered as-is, never dropped.
  staleness: string;
}

export interface TraceResponse {
  why: TraceEdge[];
  impact: ImpactEdge[];
}

// The node types the daemon's graph addresses (workflow.GraphNodeTypes). Only
// used to reject a mistyped route up front with a helpful list; the daemon
// remains the authority (it 400s an unknown type too).
export const LINEAGE_TYPES = [
  "requirement",
  "task",
  "decision",
  "question",
  "assertion",
  "artifact",
  "context_doc",
  "objective",
  "metric",
  "change_set",
  "quality_result",
  "run",
];

const TYPE_LABELS: Record<string, string> = {
  requirement: "Requirement",
  task: "Task",
  decision: "Decision",
  question: "Question",
  assertion: "Assumption",
  artifact: "Document",
  context_doc: "Context doc",
  objective: "Objective",
  metric: "Metric",
  change_set: "Change set",
  quality_result: "Quality result",
  run: "Run",
};

// nodeTypeLabel reads a graph type for a human, falling back to the type with
// underscores spaced so a type added later still renders.
export function nodeTypeLabel(type: string): string {
  if (TYPE_LABELS[type]) return TYPE_LABELS[type];
  const words = type.replace(/_/g, " ");
  return words.charAt(0).toUpperCase() + words.slice(1);
}

// Links built elsewhere (another agent's pages, a pasted URL) may say
// "change-set" or "requirements"; normalizeNodeType maps those spellings onto
// the graph's own so a near-miss still lands, and leaves anything else alone
// for isLineageType to reject.
export function normalizeNodeType(type: string): string {
  const t = type.trim().toLowerCase().replace(/-/g, "_");
  if (LINEAGE_TYPES.includes(t)) return t;
  if (t.endsWith("s") && LINEAGE_TYPES.includes(t.slice(0, -1))) return t.slice(0, -1);
  const aliases: Record<string, string> = {
    changeset: "change_set",
    chg: "change_set",
    req: "requirement",
    doc: "context_doc",
    quality: "quality_result",
    assumption: "assertion",
    hypothesis: "assertion",
  };
  return aliases[t] ?? t;
}

export function isLineageType(type: string): boolean {
  return LINEAGE_TYPES.includes(type);
}

export function lineageHref(initiativeId: string, type: string, ref: string): string {
  return `#/i/${encodeURIComponent(initiativeId)}/lineage/${encodeURIComponent(type)}/${encodeURIComponent(ref)}`;
}

// A human code at the front of a label: REQ-3, TASK-12, CHG-1, OBJ-2, MET-4,
// DEC-7, REQ-1/AC-2.
const CODE_RE = /^([A-Z][A-Z0-9]*-\d+(?:\/[A-Z]+-\d+)?)(?:\s+([\s\S]*))?$/;

export interface NodeDisplay {
  // The short handle shown first (REQ-3, or an 8-character id for nodes that
  // have no code). Never empty.
  code: string;
  // The readable part: a title, statement or kind. May be empty when the code
  // is all the daemon gave (a requirement's label is its code alone).
  text: string;
}

// nodeDisplay splits a daemon label into a code and the rest, so every node
// reads code-first ("REQ-3 · Users can save a recipe") rather than as a
// 32-character hex id. Labels are what workflow.labelCache produced; when it
// had nothing better it answers "<type> <id>", which is treated as no label.
export function nodeDisplay(type: string, id: string, label?: string): NodeDisplay {
  const short = id.length > 8 ? id.slice(0, 8) : id;
  const l = (label ?? "").trim();
  if (!l || l === `${type} ${id}`) return { code: short, text: nodeTypeLabel(type) };
  const m = CODE_RE.exec(l);
  if (m) return { code: m[1], text: (m[2] ?? "").trim() };
  if (type === "artifact") return { code: ARTIFACT_TITLES[l] ? l : short, text: ARTIFACT_TITLES[l] ?? l };
  if (type === "run") return { code: short, text: "Run" };
  return { code: short, text: l };
}

// nodeRef is what a lineage link carries for a node: its code when it has
// one (human-first, and what the CLI accepts too), otherwise its id. The type
// is taken for symmetry with nodeDisplay; every type's code reads the same.
export function nodeRef(_type: string, id: string, label?: string): string {
  const m = CODE_RE.exec((label ?? "").trim());
  return m ? m[1] : id;
}

export interface LineageTreeNode {
  key: string; // "<type>:<id>"
  type: string;
  id: string;
  label?: string;
  edge: string; // the edge that reached this node from its parent
  staleness?: string;
  // true when the node was already placed elsewhere in the tree: it is shown
  // (so the edge is not lost) but not expanded again -- a diamond in the
  // graph would otherwise repeat a whole subtree, and a cycle never end.
  repeat: boolean;
  children: LineageTreeNode[];
}

const nodeKey = (type: string, id: string) => `${type}:${id}`;

// buildLineageTree turns the daemon's flat BFS edge list into a tree rooted
// at the queried node. For "why" a node's children are what it points to
// (its sources); for "impact" they are what points at it (its dependents).
// Each node expands once, at its shallowest position (the daemon's list is
// BFS-ordered, so the first time a node appears is its shortest path);
// later appearances are kept as non-expanding `repeat` leaves. Duplicate
// edges (the same pair reached twice) are dropped.
export function buildLineageTree(
  rootType: string,
  rootId: string,
  edges: (TraceEdge | ImpactEdge)[],
  direction: "why" | "impact",
): LineageTreeNode[] {
  const adj = new Map<string, (TraceEdge | ImpactEdge)[]>();
  const seenEdge = new Set<string>();
  for (const e of edges ?? []) {
    const ek = `${e.from_type}:${e.from_id}>${e.edge_type}>${e.to_type}:${e.to_id}`;
    if (seenEdge.has(ek)) continue;
    seenEdge.add(ek);
    const parent = direction === "why" ? nodeKey(e.from_type, e.from_id) : nodeKey(e.to_type, e.to_id);
    const list = adj.get(parent) ?? [];
    list.push(e);
    adj.set(parent, list);
  }
  const rootKey = nodeKey(rootType, rootId);
  const placed = new Set<string>([rootKey]);
  const roots: LineageTreeNode[] = [];
  // BFS so a node expands at its shallowest depth.
  const queue: { key: string; into: LineageTreeNode[] }[] = [{ key: rootKey, into: roots }];
  while (queue.length > 0) {
    const { key, into } = queue.shift()!;
    for (const e of adj.get(key) ?? []) {
      const type = direction === "why" ? e.to_type : e.from_type;
      const id = direction === "why" ? e.to_id : e.from_id;
      const label = direction === "why" ? e.to_label : e.from_label;
      const k = nodeKey(type, id);
      const repeat = placed.has(k);
      const node: LineageTreeNode = {
        key: k,
        type,
        id,
        label,
        edge: e.edge_type,
        staleness: "staleness" in e ? e.staleness : undefined,
        repeat,
        children: [],
      };
      into.push(node);
      if (!repeat) {
        placed.add(k);
        queue.push({ key: k, into: node.children });
      }
    }
  }
  return roots;
}

// uniqueNodes counts the distinct nodes a direction reaches (excluding the
// root itself), matching the daemon's impact-counts dedup rather than the
// edge count, which repeats a node reachable two ways.
export function uniqueNodes(edges: (TraceEdge | ImpactEdge)[], direction: "why" | "impact", rootKey?: string): number {
  const set = new Set<string>();
  for (const e of edges ?? []) {
    set.add(direction === "why" ? nodeKey(e.to_type, e.to_id) : nodeKey(e.from_type, e.from_id));
  }
  if (rootKey) set.delete(rootKey);
  return set.size;
}

// stalenessCounts tallies dependents by staleness, one vote per node (its
// worst classification: stale > obsolete > revalidate > anything else).
export function stalenessCounts(impact: ImpactEdge[]): Record<string, number> {
  const rank = (s: string) => (s === "stale" ? 3 : s === "obsolete" ? 2 : s === "revalidate" ? 1 : 0);
  const worst = new Map<string, string>();
  for (const e of impact ?? []) {
    const k = nodeKey(e.from_type, e.from_id);
    const prev = worst.get(k);
    if (prev === undefined || rank(e.staleness) > rank(prev)) worst.set(k, e.staleness);
  }
  const out: Record<string, number> = {};
  for (const s of worst.values()) out[s] = (out[s] ?? 0) + 1;
  return out;
}

// directEdges is the first ring only (edges touching the root), for the
// compact panel.
export function directEdges<T extends TraceEdge>(edges: T[], rootType: string, rootId: string, direction: "why" | "impact"): T[] {
  return (edges ?? []).filter((e) =>
    direction === "why"
      ? e.from_type === rootType && e.from_id === rootId
      : e.to_type === rootType && e.to_id === rootId,
  );
}

// ---- reference resolution --------------------------------------------------

// The list endpoint (resource name under the initiative) that holds each node
// type, mirroring the CLI's resolveObjectID so a ref resolves the same way in
// both. run has no list; its ref is taken as the id.
export const LIST_RESOURCE: Record<string, string> = {
  requirement: "requirements",
  task: "tasks",
  decision: "decisions",
  question: "questions",
  assertion: "assertions",
  artifact: "artifacts",
  context_doc: "docs",
  objective: "objectives",
  metric: "metrics",
  change_set: "change-sets",
  quality_result: "quality/history",
};

export interface ResolvedNode {
  id: string; // the graph id the trace endpoint takes
  code: string;
  title: string;
  status?: string;
  raw: Record<string, unknown>;
}

const str = (v: unknown): string | undefined => (typeof v === "string" && v ? v : undefined);

// toResolved normalizes one list row of any node type.
export function toResolved(type: string, raw: Record<string, unknown>): ResolvedNode {
  const id = (type === "artifact" ? str(raw.artifact_id) : undefined) ?? str(raw.id) ?? "";
  const kind = str(raw.kind);
  const code =
    str(raw.code) ??
    str(raw.human_id) ??
    (type === "quality_result" ? str(raw.dimension) : undefined) ??
    (type === "artifact" && kind ? kind : undefined) ??
    id.slice(0, 8);
  const title =
    str(raw.statement) ??
    str(raw.title) ??
    str(raw.question) ??
    str(raw.name) ??
    str(raw.summary) ??
    (kind ? ARTIFACT_TITLES[kind] ?? kind : undefined) ??
    "";
  return { id, code, title, status: str(raw.status), raw };
}

const DEAD = new Set(["superseded", "removed"]);

// resolveRef finds the row a lineage URL's ref names, matching the CLI's
// rules (id, code, human id, kind or title; codes case-insensitively) plus
// two conveniences the gap report needs: an artifact's artifact_id, and an
// unambiguous id prefix (gaps name assumptions by their first 8 characters).
// When several rows match (versions of one artifact, a superseded
// requirement and its replacement), a live one wins, then the newest.
export function resolveRef(type: string, ref: string, rows: Record<string, unknown>[]): ResolvedNode | null {
  const r = ref.trim();
  if (!r) return null;
  const lower = r.toLowerCase();
  const all = (rows ?? []).map((row) => toResolved(type, row));
  const exact = all.filter(({ raw, id, code }) =>
    [id, str(raw.id), str(raw.artifact_id), str(raw.kind), str(raw.title), str(raw.dimension)].includes(r) ||
    code.toLowerCase() === lower ||
    (str(raw.human_id) ?? "").toLowerCase() === lower,
  );
  let pool = exact;
  if (pool.length === 0 && r.length >= 6) {
    const pref = all.filter((n) => n.id.startsWith(r) || (str(n.raw.id) ?? "").startsWith(r));
    if (new Set(pref.map((n) => n.id)).size === 1) pool = pref;
  }
  if (pool.length === 0) return null;
  const live = pool.filter((n) => !DEAD.has(n.status ?? ""));
  const pick = live.length > 0 ? live : pool;
  return pick[pick.length - 1];
}

// pageHrefFor is where a node lives in the rest of the UI, for the lineage
// page's "Open" link. undefined when no page shows that node.
export function pageHrefFor(initiativeId: string, type: string, node: ResolvedNode | null): string | undefined {
  const base = `#/i/${initiativeId}`;
  switch (type) {
    case "requirement":
      return `${base}/define`;
    case "decision":
      return node ? `${base}/decision/${str(node.raw.id) ?? node.id}` : `${base}/define`;
    case "task":
      return `${base}/plan`;
    case "change_set":
      return `${base}/build`;
    case "objective":
    case "metric":
      return `${base}/outcomes`;
    case "quality_result":
      return `${base}/quality`;
    case "assertion":
    case "context_doc":
      return `${base}/explore`;
    case "artifact": {
      const rowId = node ? str(node.raw.id) : undefined;
      const version = node?.raw.version;
      return rowId ? `${base}/doc/${rowId}/${version ?? "latest"}` : undefined;
    }
  }
  return undefined;
}
