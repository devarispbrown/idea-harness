import { describe, expect, it } from "vitest";
import { openItemsNote, planTaskEditable, showsRevisePrd } from "./judgmentCopy";

describe("showsRevisePrd", () => {
  it("shows for prd_review", () => {
    expect(showsRevisePrd("prd_review")).toBe(true);
  });

  it("shows for the legacy undefined/empty reason", () => {
    expect(showsRevisePrd(undefined)).toBe(true);
    expect(showsRevisePrd("")).toBe(true);
  });

  it("hides for every other reason — this is the reported defect", () => {
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
});
