import { describe, expect, it } from "vitest";
import { hasBuiltSomething, stripSourceTag, stripSourceTagsInText, verifiedRequirementCount } from "./verify";

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

// Reported defect: requirement titles rendered the model's "[Source: ...]"
// attribution tag verbatim, e.g. "[Source: human_approved task] The API
// returns a 429 after 100 requests/min".
describe("stripSourceTag", () => {
  it("strips a leading bracketed attribution tag", () => {
    expect(stripSourceTag("[Source: human_approved task] The requirement text")).toBe(
      "The requirement text",
    );
  });

  it("strips more than one leading tag", () => {
    expect(stripSourceTag("[Source: a][Source: b] The requirement text")).toBe(
      "The requirement text",
    );
  });

  it("leaves ordinary text (no leading bracket) unchanged", () => {
    expect(stripSourceTag("The requirement text")).toBe("The requirement text");
  });

  it("does not touch a bracket that isn't leading", () => {
    expect(stripSourceTag("The requirement [beta] text")).toBe("The requirement [beta] text");
  });

  it("leaves an unterminated leading bracket alone rather than eating the whole string", () => {
    expect(stripSourceTag("[unterminated text")).toBe("[unterminated text");
  });
});

describe("stripSourceTagsInText", () => {
  it("removes source attributions anywhere in a document, and nothing else", () => {
    const doc = "- [Source: human_approved task] Users can sign in\n- Keep [brackets] like this\n";
    expect(stripSourceTagsInText(doc)).toBe("- Users can sign in\n- Keep [brackets] like this\n");
  });
});
