import { describe, expect, it } from "vitest";
import { openItemsNote, planTaskEditable, showsRevisePrd } from "./judgmentCopy";

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

  it("names the next version while still under review", () => {
    expect(openItemsNote("needs_human_judgment", 3)).toContain("v3");
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
