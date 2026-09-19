import { describe, expect, it } from "vitest";
import { openItemsNote, planTaskActions, planTaskEditable, showsRevisePrd } from "./judgmentCopy";

describe("showsRevisePrd", () => {
  it("shows only for prd_review", () => {
    expect(showsRevisePrd("prd_review")).toBe(true);
  });

  // H3 — this is the reported defect: App.tsx was not threading
  // judgment_reason through to DefinePage at all, so every park (including a
  // plan-review one) arrived here as undefined and wrongly showed "Revise
  // PRD". A missing reason must fail closed, not fail open.
  it("fails closed for a missing reason instead of assuming prd_review", () => {
    expect(showsRevisePrd(undefined)).toBe(false);
    expect(showsRevisePrd("")).toBe(false);
  });

  it("hides for every other reason", () => {
    expect(showsRevisePrd("plan_review")).toBe(false);
    expect(showsRevisePrd("plan_dependencies")).toBe(false);
    expect(showsRevisePrd("verification")).toBe(false);
  });
});

describe("openItemsNote", () => {
  it("reads as optional once the PRD is approved, not as a contradiction of the pill", () => {
    const note = openItemsNote("approved", 2);
    expect(note).toContain("Optional");
    expect(note).not.toContain("revises");
  });

  it("names the next version only while the PRD itself is under review", () => {
    expect(openItemsNote("needs_human_judgment", 3, "prd_review")).toContain("v3");
  });

  it("never promises a PRD revision once the PRD is past review", () => {
    for (const [status, reason] of [
      ["plan_approved", undefined],
      ["complete", undefined],
      ["needs_human_judgment", "plan_review"],
      ["needs_human_judgment", "verification"],
    ] as const) {
      expect(openItemsNote(status, 2, reason)).not.toContain("revises");
    }
  });
});

describe("planTaskActions", () => {
  it("offers everything before approval, only cancel once approved, nothing when complete", () => {
    expect(planTaskActions("needs_human_judgment")).toEqual({
      reorder: true,
      edit: true,
      cancel: true,
      add: true,
      merge: true,
      split: true,
      dependsOn: true,
    });
    expect(planTaskActions("plan_approved")).toEqual({
      reorder: false,
      edit: false,
      cancel: true,
      add: false,
      merge: false,
      split: false,
      dependsOn: false,
    });
    expect(planTaskActions("complete")).toEqual({
      reorder: false,
      edit: false,
      cancel: false,
      add: false,
      merge: false,
      split: false,
      dependsOn: false,
    });
  });

  // Wave 3: merge/split/dependsOn must track reorder exactly (review B3 --
  // "merge, split and dependencies behave like reorder, i.e. only before
  // approval"), for every status this function distinguishes.
  it("keeps merge, split and dependsOn locked in lockstep with reorder", () => {
    for (const status of ["needs_human_judgment", "draft", "plan_approved", "complete"]) {
      const a = planTaskActions(status);
      expect(a.merge).toBe(a.reorder);
      expect(a.split).toBe(a.reorder);
      expect(a.dependsOn).toBe(a.reorder);
    }
  });
});

describe("planTaskEditable", () => {
  it("is editable before approval", () => {
    expect(planTaskEditable("needs_human_judgment")).toBe(true);
    expect(planTaskEditable("draft")).toBe(true);
  });

  it("is not editable once the plan is approved", () => {
    expect(planTaskEditable("plan_approved")).toBe(false);
  });

  // Reported defect: a completed initiative is unambiguously post-approval,
  // but only "plan_approved" was ever checked, so a shipped initiative's
  // plan still showed live Edit/Delete controls.
  it("is not editable once the initiative is complete", () => {
    expect(planTaskEditable("complete")).toBe(false);
  });
});
