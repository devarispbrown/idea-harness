import { describe, expect, it } from "vitest";
import { gapKindLabel, gapSubject, gapSummaryLine, topGaps, type Gap } from "./gaps";

function gap(overrides: Partial<Gap> = {}): Gap {
  return {
    kind: "unverified_requirement",
    state: "evidence_stale",
    severity: "medium",
    nodes: [{ type: "requirement", id: "req-1", code: "REQ-1" }],
    why: "pass evidence for REQ-1/AC-1 predates CHG-2",
    next: "idea verify REQ-1 --pass",
    dependents: 0,
    initiative_id: "ini-1",
    ...overrides,
  };
}

describe("gapSummaryLine", () => {
  it("lists non-zero severities most severe first", () => {
    expect(gapSummaryLine({ low: 1, critical: 0, high: 2, medium: 0, observation: 0 })).toBe("2 high · 1 low");
  });
  it("is empty for no gaps or a missing summary", () => {
    expect(gapSummaryLine({ high: 0 })).toBe("");
    expect(gapSummaryLine(undefined)).toBe("");
  });
});

describe("gapKindLabel", () => {
  it("humanizes the kind without knowing it", () => {
    expect(gapKindLabel("untested_acceptance_criterion")).toBe("Untested acceptance criterion");
    expect(gapKindLabel("objective_unmeasured")).toBe("Objective unmeasured");
  });
});

describe("gapSubject", () => {
  it("leads with the first node's code", () => {
    expect(gapSubject(gap())).toBe("REQ-1");
  });
  it("falls back to the kind when there is no node", () => {
    expect(gapSubject(gap({ nodes: [] }))).toBe("unverified_requirement");
  });
});

describe("topGaps", () => {
  it("keeps the daemon's order and counts the rest", () => {
    const gaps = [gap({ why: "a" }), gap({ why: "b" }), gap({ why: "c" })];
    const { shown, hidden } = topGaps(gaps, 2);
    expect(shown.map((g) => g.why)).toEqual(["a", "b"]);
    expect(hidden).toBe(1);
  });
  it("tolerates fewer than n", () => {
    expect(topGaps([gap()], 5)).toEqual({ shown: [gap()], hidden: 0 });
  });
});
