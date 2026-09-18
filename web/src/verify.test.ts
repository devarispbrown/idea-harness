import { describe, expect, it } from "vitest";
import { hasBuiltSomething, verifiedRequirementCount } from "./verify";

describe("verifiedRequirementCount", () => {
  it("counts only verified, non-removed requirements", () => {
    expect(
      verifiedRequirementCount([
        { status: "verified" },
        { status: "verified" },
        { status: "pending" },
        { status: "removed" },
        { status: "waived" },
      ]),
    ).toBe(2);
  });

  it("is zero for an empty list", () => {
    expect(verifiedRequirementCount([])).toBe(0);
  });

  it("is zero when nothing is verified yet", () => {
    expect(verifiedRequirementCount([{ status: "pending" }, { status: "failed" }])).toBe(0);
  });
});

describe("hasBuiltSomething", () => {
  it("is true once any change set has been applied", () => {
    expect(hasBuiltSomething([{ status: "draft" }, { status: "applied" }])).toBe(true);
  });

  it("is false when nothing has been applied yet", () => {
    expect(hasBuiltSomething([{ status: "draft" }, { status: "review_required" }])).toBe(false);
  });

  it("is false for an empty list", () => {
    expect(hasBuiltSomething([])).toBe(false);
  });
});
