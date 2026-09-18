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
  // Anything that doesn't match a known shape. Falling back to "home" here
  // used to send a mistyped or stale link silently to the initiative list --
  // indistinguishable from the user having clicked home on purpose (K).
  | { name: "not-found"; path: string };

export const PHASES: PhaseName[] = ["explore", "define", "design", "plan", "build", "verify"];

export function phaseHref(id: string, phase: PhaseName): string {
  return `#/i/${id}/${phase}`;
}

export function parseRoute(hash: string): Route {
  const path = hash.replace(/^#/, "");
  const segs = path.split("/").filter(Boolean);
  if (segs.length === 0) return { name: "home" };
  if (segs[0] === "settings" && segs.length === 1) return { name: "settings" };
  if (segs[0] === "i" && segs.length >= 3) {
    const id = segs[1];
    const rest = segs[2];
    if (rest === "decision" && segs.length === 4) return { name: "decision", id, decisionId: segs[3] };
    if (rest === "health" && segs.length === 3) return { name: "health", id };
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

export function currentRoute(): Route {
  return parseRoute(window.location.hash || "#/");
}

// navigate changes the hash (triggers the hashchange listener).
export function navigate(to: string): void {
  if (window.location.hash !== to) {
    window.location.hash = to;
  }
}
