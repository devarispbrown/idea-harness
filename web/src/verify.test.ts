import { describe, expect, it } from "vitest";
import {
  allEvidencePassed,
  evidenceSummary,
  hasBuiltSomething,
  hasConfirmableEvidence,
  stripSourceTag,
  stripSourceTagsInText,
  verifiedRequirementCount,
} from "./verify";

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

describe("allEvidencePassed (task 7)", () => {
  it("is true when every mapped test passed", () => {
    expect(
      allEvidencePassed([
        { name: "test_a", status: "passed" },
        { name: "test_b", status: "passed" },
      ]),
    ).toBe(true);
  });

  it("is false when any mapped test failed", () => {
    expect(
      allEvidencePassed([
        { name: "test_a", status: "passed" },
        { name: "test_b", status: "failed" },
      ]),
    ).toBe(false);
  });

  it("is false for an empty or absent list -- 'no evidence' is not 'all passed'", () => {
    expect(allEvidencePassed([])).toBe(false);
    expect(allEvidencePassed(undefined)).toBe(false);
  });
});

describe("evidenceSummary (task 7)", () => {
  it("reads '✓ N passing tests: ...' when every test passed", () => {
    expect(
      evidenceSummary([
        { name: "test_rate_limit_429", status: "passed" },
        { name: "test_rate_limit_headers", status: "passed" },
        { name: "test_rate_limit_reset", status: "passed" },
      ]),
    ).toBe("✓ 3 passing tests: test_rate_limit_429, test_rate_limit_headers, test_rate_limit_reset");
  });

  it("uses the singular for exactly one passing test", () => {
    expect(evidenceSummary([{ name: "test_a", status: "passed" }])).toBe("✓ 1 passing test: test_a");
  });

  it("shows a plain count when some tests failed", () => {
    expect(
      evidenceSummary([
        { name: "test_a", status: "passed" },
        { name: "test_b", status: "failed" },
      ]),
    ).toBe("1 of 2 tests passing: test_a, test_b");
  });

  it("is empty when there is no evidence at all", () => {
    expect(evidenceSummary(undefined)).toBe("");
    expect(evidenceSummary([])).toBe("");
  });
});

describe("hasConfirmableEvidence (task 7)", () => {
  it("is true when an active requirement has all-passing evidence", () => {
    expect(
      hasConfirmableEvidence([
        { status: "pending", evidence_tests: [{ name: "test_a", status: "passed" }] },
      ]),
    ).toBe(true);
  });

  it("is false when nothing has evidence", () => {
    expect(hasConfirmableEvidence([{ status: "pending" }])).toBe(false);
  });

  it("is false when the only confirmable requirement is already verified", () => {
    expect(
      hasConfirmableEvidence([
        { status: "verified", evidence_tests: [{ name: "test_a", status: "passed" }] },
      ]),
    ).toBe(false);
  });

  it("is false when the only confirmable requirement is waived or removed", () => {
    expect(
      hasConfirmableEvidence([
        { status: "waived", evidence_tests: [{ name: "test_a", status: "passed" }] },
        { status: "removed", evidence_tests: [{ name: "test_b", status: "passed" }] },
      ]),
    ).toBe(false);
  });

  it("is false when evidence exists but not all of it passed", () => {
    expect(
      hasConfirmableEvidence([
        {
          status: "pending",
          evidence_tests: [
            { name: "test_a", status: "passed" },
            { name: "test_b", status: "failed" },
          ],
        },
      ]),
    ).toBe(false);
  });
});
