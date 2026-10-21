import type { PhaseName } from "./router";

// Phase completion + summary derivation (§design rule 7: completed phases state
// what they produced). Client-side only, from status + artifact/task/change-set
// data.

export interface PhaseSource {
  status: string;
  // Why the initiative is parked. All four reasons share
  // `needs_human_judgment`, so the status alone cannot tell a PRD-review park
  // from a plan-stage one.
  judgmentReason?: string;
  artifacts: { kind: string; version: number }[];
  tasks: { status: string }[];
  changeSets: { status: string }[];
  verifiedRequirements: number;
}

// InitiativeRollup is the cross-initiative summary served by GET /attention:
// phase-derivation inputs plus open decision/question/finding counts.
export interface InitiativeRollup {
  id: string;
  title: string;
  status: string;
  judgment_reason?: string;
  artifacts: { kind: string; version: number }[];
  task_statuses: string[];
  change_set_statuses: string[];
  open_decisions: number;
  open_questions: number;
  open_findings: number;
  verified_requirements: number;
}

// attentionCount is how many open items an initiative contributes to "Needs
// you elsewhere" (App.tsx's sidebar inbox). A COMPLETE initiative's
// unresolved decisions/questions/findings are optional cleanup, not
// something blocking anything -- the same "optional once approved" rule
// DefinePage's openItemsNote applies to a single approved PRD -- so they must
// not inflate a count that reads as "this still needs your judgment." Before
// this, a shipped initiative with a stray unanswered question showed up in
// the inbox exactly like one genuinely parked and waiting.
export function attentionCount(r: {
  status: string;
  open_decisions: number;
  open_questions: number;
  open_findings: number;
}): number {
  if (r.status === "complete") return 0;
  return r.open_decisions + r.open_questions + r.open_findings;
}

// rollupPhaseSource maps an InitiativeRollup to the shape derivePhases expects.
export function rollupPhaseSource(r: InitiativeRollup): PhaseSource {
  return {
    status: r.status,
    judgmentReason: r.judgment_reason,
    artifacts: r.artifacts,
    tasks: r.task_statuses.map((status) => ({ status })),
    changeSets: r.change_set_statuses.map((status) => ({ status })),
    verifiedRequirements: r.verified_requirements,
  };
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
  const versions = artifacts.filter((a) => a.kind === kind).map((a) => a.version);
  return versions.length ? Math.max(...versions) : 0;
}

function hasAll(artifacts: { kind: string; version: number }[], kinds: string[]): boolean {
  return kinds.every((k) => artifacts.some((a) => a.kind === k));
}

export function derivePhases(src: PhaseSource): PhaseState[] {
  const { artifacts, status, judgmentReason } = src;
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

  // "Past define" = the PRD was approved.
  //
  // This used to key on the execution-plan ARTIFACT, as the disambiguator
  // between an M1-review park and an M2-plan park, since both share
  // `needs_human_judgment`. The artifact is a proxy: `runExecutionPlan` writes
  // it before the dependency check, so its presence says "a plan was generated",
  // not "the plan stage is legitimately under way".
  //
  // The tightening that matters is at the other end -- a park on `prd_review`,
  // or one carrying no reason, no longer reads as past define because an
  // artifact from an earlier cycle is lying around.
  //
  // The reason is the real discriminator, and it is exact where the artifact is
  // a proxy. Three of the four reasons only occur after the PRD is approved --
  // verified at each write site: plan_review and plan_dependencies in
  // advance.go, verification in verify.go's completion path.
  const hasExecutionPlan = hasAll(artifacts, ["execution_plan"]);
  const pastDefinePark =
    judgmentReason === "plan_review" ||
    judgmentReason === "plan_dependencies" ||
    judgmentReason === "verification";
  const pastDefine =
    status === "approved" ||
    status === "plan_approved" ||
    complete ||
    // `running` and `failed` record no reason, and the status is `running` for
    // the whole of every M2 and M3 operation -- so during `idea execute` the
    // artifact is the only evidence available, and dropping it here would
    // un-complete Define mid-build and jump the rail backwards.
    ((status === "running" || status === "failed") && hasExecutionPlan) ||
    // A budget stop can happen in any phase, so its reason says nothing
    // about how far the initiative got: the artifact decides, as for running.
    (judgmentReason === "budget_exceeded" && hasExecutionPlan) ||
    pastDefinePark;

  const done: Record<PhaseName, boolean> = {
    explore: hasAll(artifacts, ["idea_brief", "research_brief"]),
    define: pastDefine,
    // Gated on `pastDefine`, not just the artifacts' presence (K): design
    // artifacts existing is not the same as Design having legitimately
    // started -- e.g. leftovers from an earlier cycle, the same class of
    // proxy-vs-reason mistake `pastDefine` itself was written to avoid for
    // Define. Without this, "Design ✓ approved" could show before the PRD
    // was even approved.
    design: pastDefine && hasAll(artifacts, ["technical_design", "design_spec", "verification_plan"]),
    // Guarded on hasExecutionPlan too, not status alone: `plan_approved` is
    // the status that means "a plan was approved", but the artifact is what
    // Plan actually PRODUCES, and the rail should not be able to say a
    // phase is done when its own output doesn't exist.
    plan: (status === "plan_approved" || complete) && hasExecutionPlan,
    build: allTasksFinished && allChangeSetsTerminal,
    verify: complete,
  };

  const prdVersion = versionOf(artifacts, "prd");
  const summaries: Record<PhaseName, string> = {
    explore: "brief + research",
    define: `PRD v${prdVersion} approved`,
    design: "architecture + spec",
    // The PLAN was approved -- individual tasks have no "approved" status of
    // their own (proposed/ready/blocked/done/cancelled), so a task added
    // after approval and still `proposed` used to be miscounted as one of
    // "N tasks approved", asserting an approval that never happened for it.
    plan: `plan approved · ${taskCount} task${taskCount === 1 ? "" : "s"}`,
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

// currentPill is the live badge/note PhaseNav shows next to whichever phase
// the rail currently considers "current". It must only describe a condition
// that is actually true of the CURRENT phase, not whatever the initiative's
// raw status happens to be.
//
// "approved" and "plan_approved" are announcements that a PREVIOUS phase
// (Define, Plan respectively) just finished -- that fact is already shown as
// that phase's own DONE summary ("PRD v4 approved", above). Both statuses
// persist in the initiative record well after the rail's "current" phase has
// moved on (Design generates automatically after PRD approval; Verify
// becomes current once Build finishes, all still under status
// "plan_approved"). Rendering them here too used to attach a stale
// "✓ approved"/"✓ plan approved" badge to whichever phase came next -- Design,
// or even Verify -- as if THAT phase had just been approved. Only
// "running" / "needs_human_judgment" / "failed" describe an actually-active
// condition of the current phase itself, so only those get a live badge.
export function currentPill(status: string): { pill: string; note: string } {
  switch (status) {
    case "running":
      return { pill: "▶", note: "working…" };
    case "needs_human_judgment":
      return { pill: "!", note: "waiting on you" };
    case "failed":
      return { pill: "✕", note: "failed" };
    default:
      return { pill: "", note: "" };
  }
}

// ---- goal-shaped stages (docs/plans/ui-ux-optimization-plan.md P0.1) ----
//
// The six engine phases (Explore/Define/Design/Plan/Build/Verify) are a
// backend shape, not a user's job. These four stages are the job: Shape the
// idea into a reviewed plan, Decide the calls that unblock it, Build the code,
// Ship the verified result. The six phases stay URL-addressable (and in the
// sidebar) as detail; Decide is a state, not a linear milestone.

export type StageName = "shape" | "decide" | "build" | "ship";

export const STAGE_LABELS: Record<StageName, string> = {
  shape: "Shape",
  decide: "Decide",
  build: "Build",
  ship: "Ship",
};

// STAGE_ORDER is the goal-shaped order the user is taught (Shape → Decide →
// Build → Ship). Decide is a state the initiative passes through, not a
// milestone to check off, but it is listed so the "waiting on you" moment has
// a name.
export const STAGE_ORDER: StageName[] = ["shape", "decide", "build", "ship"];

export const STAGE_PURPOSE: Record<StageName, string> = {
  shape: "Understand the idea and its reviewed plan.",
  decide: "The calls that unblock this.",
  build: "The code, written, reviewed, applied.",
  ship: "Verified — and what is not yet.",
};

// PHASE_TO_STAGE collapses the six phases into the four stages.
export const PHASE_TO_STAGE: Record<PhaseName, StageName> = {
  explore: "shape",
  define: "shape",
  design: "shape",
  plan: "build",
  build: "build",
  verify: "ship",
};

// currentStage is the stage a user thinks in: parked on a judgment → Decide;
// done → Ship. The definitive statuses map directly (plan_approved → Build,
// approved → Shape, the plan/PRD are the gates that move between stages); only
// running/failed/draft fall back to the rail's current phase, since those carry
// no stage of their own and the artifacts are the only evidence of how far the
// initiative got.
export function currentStage(src: PhaseSource): StageName {
  switch (src.status) {
    case "complete":
      return "ship";
    case "needs_human_judgment":
      return "decide";
    case "plan_approved":
      return "build";
    case "approved":
      return "shape";
    default:
      return PHASE_TO_STAGE[currentPhase(src)];
  }
}

// nextAction names the single next step as an imperative, with the CLI command
// that performs it. Coarse by design: the exact server-side step resolution
// (`idea next`) is the follow-up; this is legible from frontend state alone.
export function nextAction(src: PhaseSource): { action: string; command?: string } {
  switch (src.status) {
    case "complete":
      return { action: "Done — review what shipped." };
    case "failed":
      return { action: "Re-run the failed step.", command: "idea retry" };
    case "running":
      return { action: "Building — watch it live.", command: "idea watch" };
    case "needs_human_judgment":
      switch (src.judgmentReason) {
        case "plan_review":
        case "plan_dependencies":
          return { action: "Approve or revise the plan.", command: "idea approve-plan" };
        case "verification":
          return { action: "Record verification evidence.", command: "idea verify" };
        case "budget_exceeded":
          return { action: "Raise or clear the spending cap.", command: "idea budget --set <usd>" };
        default:
          return { action: "Approve or revise the PRD.", command: "idea approve" };
      }
    case "plan_approved":
      return { action: "Run the next task to build.", command: "idea execute" };
    case "approved":
      return { action: "Advance to the execution plan.", command: "idea advance" };
    default:
      return { action: "Keep shaping the idea.", command: "idea next" };
  }
}
