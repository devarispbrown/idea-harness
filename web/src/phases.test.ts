import { describe, expect, it } from "vitest";
import { derivePhases, rollupPhaseSource, type PhaseSource } from "./phases";

// The Define phase used to be derived from the presence of the execution-plan
// ARTIFACT, as the disambiguator between an M1-review park and an M2-plan park,
// since both share `needs_human_judgment`.
//
// The reason is the exact discriminator where the artifact is a proxy. The
// tightening that matters is at the other end: a park on `prd_review`, or one
// carrying no reason at all, no longer reads as past define merely because an
// artifact from an earlier cycle is lying around.
const base: PhaseSource = {
  status: "needs_human_judgment",
  artifacts: [
    { kind: "idea_brief", version: 1 },
    { kind: "research_brief", version: 1 },
    { kind: "prd", version: 1 },
    { kind: "execution_plan", version: 1 },
  ],
  tasks: [],
  changeSets: [],
  verifiedRequirements: 0,
};

const defineDone = (src: PhaseSource) =>
  derivePhases(src).find((p) => p.phase === "define")!.state === "done";

describe("derivePhases and the Define phase", () => {
  it("IS past define for a plan whose dependencies never resolved", () => {
    // The PRD was approved -- `advance` requires it -- so Define is done and the
    // work is parked in the Plan phase. What the artifact check got wrong was
    // never this; it was the CTA on PlanPage, fixed separately. Asserted here so
    // the reason-based predicate is not quietly narrower than the proxy it
    // replaced.
    expect(defineDone({ ...base, judgmentReason: "plan_dependencies" })).toBe(true);
  });

  it("is NOT past define for a PRD review parked on a human", () => {
    expect(defineDone({ ...base, judgmentReason: "prd_review" })).toBe(false);
  });

  it("is NOT past define when the reason is empty", () => {
    // Migration 18 backfills nothing, so every pre-migration park carries "" —
    // whichever of the four paths parked it, including plan_dependencies, whose
    // artifact IS present. Falling back to the artifact check here would re-run
    // the wrong inference on exactly the population it is wrong for
    // (deferred.md, audit 7 / K1). Not-past-define is the conservative reading.
    expect(defineDone({ ...base, judgmentReason: undefined })).toBe(false);
  });

  it("IS past define for a plan-review park", () => {
    expect(defineDone({ ...base, judgmentReason: "plan_review" })).toBe(true);
  });

  it("IS past define for a verification park", () => {
    expect(defineDone({ ...base, judgmentReason: "verification" })).toBe(true);
  });

  it("IS past define while an operation is running", () => {
    // The regression guard. The status is `running` for the whole of every M2
    // and M3 operation and records no reason, so dropping the artifact check
    // here would un-complete Define mid-build and jump the rail backwards from
    // Plan to Define during `idea execute`.
    expect(defineDone({ ...base, status: "running", judgmentReason: undefined })).toBe(true);
  });

  it("IS past define for a failed initiative that got as far as a plan", () => {
    expect(defineDone({ ...base, status: "failed", judgmentReason: undefined })).toBe(true);
  });

  it("is NOT past define while running before any plan exists", () => {
    expect(
      defineDone({
        ...base,
        status: "running",
        artifacts: [{ kind: "idea_brief", version: 1 }],
      }),
    ).toBe(false);
  });

  it("IS past define for the approved statuses", () => {
    for (const status of ["approved", "plan_approved", "complete"]) {
      expect(defineDone({ ...base, status, artifacts: [] })).toBe(true);
    }
  });
});

// ProductHome derives its rail from the rollup, not from App's literal, so a
// fix applied only to App leaves the bug on the page that renders it for every
// initiative at once.
describe("rollupPhaseSource", () => {
  it("carries the judgment reason through", () => {
    const src = rollupPhaseSource({
      id: "i1",
      title: "t",
      status: "needs_human_judgment",
      judgment_reason: "plan_dependencies",
      artifacts: [{ kind: "execution_plan", version: 1 }],
      task_statuses: [],
      change_set_statuses: [],
      open_decisions: 0,
      open_questions: 0,
      open_findings: 0,
      verified_requirements: 0,
    });
    expect(src.judgmentReason).toBe("plan_dependencies");
    expect(defineDone(src)).toBe(true);
  });
});
