import type { PhaseName } from "./router";

// Phase completion + summary derivation (§design rule 7: completed phases state
// what they produced). Client-side only, from status + artifact/task/change-set
// data.

export interface PhaseSource {
  status: string;
  artifacts: { kind: string; version: number }[];
  tasks: { status: string }[];
  changeSets: { status: string }[];
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

export const PHASE_PURPOSE: Record<PhaseName, string> = {
  explore: "Framing the idea, then researching what's known.",
  define: "Turning research into an agreed, reviewed PRD.",
  design: "How it will be built, and how we'll know it works.",
  plan: "The work, ordered. Approve it before anything runs.",
  build: "Nothing touches your repo until you approve it.",
  verify: "Every requirement, checked against what was built.",
};

export const ARTIFACT_TITLES: Record<string, string> = {
  idea_brief: "Idea brief",
  research_brief: "Research brief",
  prd: "Product requirements",
  technical_design: "Technical design",
  design_spec: "Design spec",
  verification_plan: "Verification plan",
  threat_model: "Threat model",
  execution_plan: "Execution plan",
  operational_readiness: "Operational readiness",
  verification_report: "Verification report",
};

// Which phase "Produced so far" each artifact belongs to.
export const ARTIFACT_PHASE: Record<string, PhaseName> = {
  idea_brief: "explore",
  research_brief: "explore",
  prd: "define",
  technical_design: "design",
  design_spec: "design",
  verification_plan: "design",
  threat_model: "design",
  execution_plan: "plan",
  operational_readiness: "verify",
  verification_report: "verify",
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

  const taskCount = src.tasks.length;
  const appliedChangeSets = src.changeSets.filter((c) => c.status === "applied").length;
  // Build is done only when the whole plan has been executed AND every change
  // set reached a terminal state (applied/reverted) — never on a single apply.
  const allTasksFinished =
    taskCount > 0 && src.tasks.every((t) => t.status === "done" || t.status === "cancelled");
  const allChangeSetsTerminal =
    src.changeSets.length > 0 &&
    src.changeSets.every((c) => c.status === "applied" || c.status === "reverted");

  // "Past define" = the PRD was approved. The execution-plan artifact is the
  // disambiguator between M1-review-blocked (no plan yet) and M2-plan-blocked
  // (plan exists), since both use the same `needs_human_judgment` status.
  const hasExecutionPlan = hasAll(artifacts, ["execution_plan"]);
  const pastDefine =
    status === "approved" || status === "plan_approved" || complete || hasExecutionPlan;

  const done: Record<PhaseName, boolean> = {
    explore: hasAll(artifacts, ["idea_brief", "research_brief"]),
    define: pastDefine,
    design: hasAll(artifacts, ["technical_design", "design_spec", "verification_plan"]),
    plan: status === "plan_approved" || complete,
    build: allTasksFinished && allChangeSetsTerminal,
    verify: complete,
  };

  const prdVersion = versionOf(artifacts, "prd");
  const summaries: Record<PhaseName, string> = {
    explore: "brief + research",
    define: `PRD v${prdVersion} approved`,
    design: "architecture + spec",
    plan: `${taskCount} task${taskCount === 1 ? "" : "s"} approved`,
    build: `${appliedChangeSets} change set${appliedChangeSets === 1 ? "" : "s"} applied`,
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
