// Minimal hash-based router (no dependency). Paths mirror the README route
// table, prefixed with "#": "#/", "#/i/:id/explore", "#/settings", etc.

export type PhaseName = "explore" | "define" | "design" | "plan" | "build" | "verify";

export type Route =
  | { name: "home" }
  | { name: "phase"; id: string; phase: PhaseName }
  | { name: "decision"; id: string; decisionId: string }
  | { name: "health"; id: string }
  | { name: "doc"; id: string; artifact: string; version: string }
  | { name: "diff"; id: string; from: string; to: string }
  | { name: "settings" }
  // C1: the per-initiative "What changed" view (wave 3) -- the timeline in
  // full, distinct from the compact panel shown in the app shell.
  | { name: "changed"; id: string }
  // D-b: the cross-initiative "Needs you" queue (wave 3). Project-scoped,
  // not initiative-scoped -- there is no initiative id in this route.
  | { name: "queue" }
  // Anything that doesn't match a known shape. Falling back to "home" here
  // used to send a mistyped or stale link silently to the initiative list --
  // indistinguishable from the user having clicked home on purpose (K).
  | { name: "not-found"; path: string }
  // The Product Graph views (docs/ui.md): one node's lineage, and the
  // initiative-wide gaps, outcomes and quality pages.
  | { name: "lineage"; id: string; nodeType: string; ref: string }
  | { name: "gaps"; id: string }
  | { name: "outcomes"; id: string }
  | { name: "quality"; id: string };

export const PHASES: PhaseName[] = ["explore", "define", "design", "plan", "build", "verify"];

export function phaseHref(id: string, phase: PhaseName): string {
  return `#/i/${id}/${phase}`;
}

// ---------- the project prefix ----------
//
// The daemon serves several projects, and a link has to say which one it is
// about or it opens in whichever project the reader happened to have selected
// last. Every route may carry a `#/p/<projectId>` prefix:
//
//   #/p/<pid>/i/<id>/build   -- a change set to review, in that project
//   #/i/<id>/build           -- the same, in the current project
//
// The prefix is optional so every existing `#/…` link keeps working;
// navigate() adds it for the current project, and the shell canonicalizes a
// bare hash to the prefixed form (replaceState), so the address bar -- what a
// person copies, and what a push notification links to -- always names one.

// splitProject separates a hash into its project prefix (null when absent)
// and the route underneath it, still `#`-prefixed.
export function splitProject(hash: string): { projectId: string | null; rest: string } {
  const m = /^#?\/p\/([^/]+)(\/.*)?$/.exec(hash);
  if (!m) return { projectId: null, rest: hash.startsWith("#") ? hash : "#" + hash };
  return { projectId: decodeSeg(m[1]), rest: "#" + (m[2] || "/") };
}

let activeProject = "";

// setActiveProject records the project navigate() prefixes links with. The
// shell calls it whenever the selected project changes.
export function setActiveProject(projectId: string): void {
  activeProject = projectId;
}

// withProject prefixes an in-app hash with a project. A hash that already
// names one, or anything that is not an in-app hash, is returned as is.
export function withProject(href: string, projectId: string = activeProject): string {
  if (!projectId || !href.startsWith("#/") || href.startsWith("#/p/")) return href;
  return "#/p/" + encodeURIComponent(projectId) + href.slice(1);
}

// currentProjectFromHash is the project the address bar names, if any.
export function currentProjectFromHash(): string | null {
  return splitProject(window.location.hash || "#/").projectId;
}

export function parseRoute(hash: string): Route {
  const path = splitProject(hash).rest.replace(/^#/, "");
  const segs = path.split("/").filter(Boolean);
  if (segs.length === 0) return { name: "home" };
  if (segs[0] === "settings" && segs.length === 1) return { name: "settings" };
  if (segs[0] === "queue" && segs.length === 1) return { name: "queue" };
  if (segs[0] === "i" && segs.length >= 3) {
    const id = segs[1];
    const rest = segs[2];
    if (rest === "decision" && segs.length === 4) return { name: "decision", id, decisionId: segs[3] };
    if (rest === "health" && segs.length === 3) return { name: "health", id };
    if (rest === "changed" && segs.length === 3) return { name: "changed", id };
    if ((rest === "gaps" || rest === "outcomes" || rest === "quality") && segs.length === 3) return { name: rest, id };
    // lineage/<type>/<ref>: the ref is URI-encoded by lineageHref, but a
    // hand-typed REQ-1/AC-1 arrives as two segments, so the tail is rejoined.
    if (rest === "lineage" && segs.length >= 5) {
      return { name: "lineage", id, nodeType: decodeSeg(segs[3]), ref: decodeSeg(segs.slice(4).join("/")) };
    }
    if (rest === "doc" && (segs.length === 4 || segs.length === 5)) {
      return { name: "doc", id, artifact: segs[3], version: segs[4] ?? "latest" };
    }
    if (rest === "diff" && segs.length === 5) {
      return { name: "diff", id, from: segs[3], to: segs[4] };
    }
    if (segs.length === 3 && (PHASES as string[]).includes(rest)) {
      return { name: "phase", id, phase: rest as PhaseName };
    }
  }
  // Normalized (no leading/trailing/doubled slashes) so App.tsx can show it
  // back to the user without echoing whatever stray punctuation was in the
  // original hash.
  return { name: "not-found", path: segs.join("/") };
}

// decodeSeg decodes one URI-encoded hash segment, keeping it as typed when it
// is not valid encoding (a stray "%") rather than throwing.
function decodeSeg(s: string): string {
  try {
    return decodeURIComponent(s);
  } catch {
    return s;
  }
}

export function currentRoute(): Route {
  return parseRoute(window.location.hash || "#/");
}

// navigate changes the hash (triggers the hashchange listener), naming the
// current project unless `to` already names one.
export function navigate(to: string): void {
  const target = withProject(to);
  if (window.location.hash !== target) {
    window.location.hash = target;
  }
}

// lineageHref is the one way to link to a node's lineage, so every caller
// encodes the type and ref the same way parseRoute decodes them. Defined in
// lineage.ts next to the view's own helpers; re-exported here for callers
// that already import their navigation from the router.
export { lineageHref } from "./lineage";
