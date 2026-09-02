import type { PhaseName } from "./router";

// Phase completion + summary derivation (§design rule 7: completed phases state
// what they produced). Client-side only, from status + artifact/change-set data.

export interface PhaseSource {
  status: string;
  artifacts: { kind: string; version: number }[];
  taskCount: number;
  appliedChangeSets: number;
  verifiedRequirements: number;
}

export interface PhaseState {
  phase: PhaseName;
  label: string;
  state: "done" | "current" | "future";
  summary: string; // done-phase summary ("PRD v4 approved")
}

export const PHASE_LABELS: Record<PhaseName, string> = {
  explore: "Explore",
  define: "Define",
  design: "Design",
  plan: "Plan",
  build: "Build",
  verify: "Verify",
};

function versionOf(artifacts: { kind: string; version: number }[], kind: string): number {
  return artifacts.find((a) => a.kind === kind)?.version ?? 0;
}

function hasAll(artifacts: { kind: string; version: number }[], kinds: string[]): boolean {
  return kinds.every((k) => artifacts.some((a) => a.kind === k));
}

export function derivePhases(src: PhaseSource): PhaseState[] {
  const { artifacts, status } = src;
  const complete = status === "complete";

  const done: Record<PhaseName, boolean> = {
    explore: hasAll(artifacts, ["idea_brief", "research_brief"]),
    define: hasAll(artifacts, ["prd"]),
    design: hasAll(artifacts, ["technical_design", "design_spec", "verification_plan"]),
    plan: status === "plan_approved" || complete,
    build: src.appliedChangeSets > 0,
    verify: complete,
  };

  const prdVersion = versionOf(artifacts, "prd");
  const summaries: Record<PhaseName, string> = {
    explore: "brief + research",
    define: `PRD v${prdVersion} approved`,
    design: "architecture + spec",
    plan: `${src.taskCount} task${src.taskCount === 1 ? "" : "s"} approved`,
    build: `${src.appliedChangeSets} change set${src.appliedChangeSets === 1 ? "" : "s"} applied`,
    verify: `${src.verifiedRequirements} requirement${src.verifiedRequirements === 1 ? "" : "s"} verified`,
  };

  const order: PhaseName[] = ["explore", "define", "design", "plan", "build", "verify"];
  const firstIncomplete = order.findIndex((p) => !done[p]);
  const currentIndex = firstIncomplete === -1 ? order.length - 1 : firstIncomplete;

  return order.map((phase, i) => {
    const state = done[phase] ? "done" : i === currentIndex ? "current" : "future";
    return { phase, label: PHASE_LABELS[phase], state, summary: summaries[phase] };
  });
}

export function currentPhase(src: PhaseSource): PhaseName {
  const states = derivePhases(src);
  return (states.find((s) => s.state === "current") ?? states[states.length - 1]).phase;
}
