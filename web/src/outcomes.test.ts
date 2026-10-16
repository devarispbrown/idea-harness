import { describe, expect, it } from "vitest";
import {
  directionArrow,
  driftClass,
  driftLabel,
  driftingMetrics,
  isDrifting,
  linkableRequirements,
  metricDraft,
  metricEditBody,
  metricRange,
  metricsByObjective,
  objectiveDraft,
  objectiveEditBody,
  outcomeGapRows,
  parseNumber,
  requirementCodes,
  requirementsWithoutObjective,
} from "./outcomes";

const metric = (code: string, drift: string, objective_id = "o1") => ({
  code,
  objective_id,
  status: { drift } as never,
});

describe("driftLabel / driftClass", () => {
  it("labels every drift state", () => {
    expect(driftLabel("on_track")).toBe("on track");
    expect(driftLabel("off_track")).toBe("off track");
    expect(driftLabel("regressed")).toBe("regressed");
    expect(driftLabel("stale")).toBe("stale");
    expect(driftLabel("no_data")).toBe("no data");
    expect(driftLabel("something_new")).toBe("something new");
  });

  it("colours drift by severity", () => {
    expect(driftClass("on_track")).toBe("pill-verified");
    expect(driftClass("off_track")).toBe("chip-failing");
    expect(driftClass("regressed")).toBe("chip-failing");
    expect(driftClass("stale")).toBe("chip-needs-you");
    expect(driftClass("no_data")).toBe("chip-inactive");
    expect(driftClass("bogus")).toBe("chip-inactive");
  });
});

describe("isDrifting / driftingMetrics", () => {
  it("treats off_track, regressed and stale as drifting, not no_data or on_track", () => {
    expect(isDrifting(metric("MET-1", "off_track"))).toBe(true);
    expect(isDrifting(metric("MET-1", "regressed"))).toBe(true);
    expect(isDrifting(metric("MET-1", "stale"))).toBe(true);
    expect(isDrifting(metric("MET-1", "no_data"))).toBe(false);
    expect(isDrifting(metric("MET-1", "on_track"))).toBe(false);
    expect(isDrifting({ status: undefined as never })).toBe(false);
  });

  it("orders worst first, then by code numerically", () => {
    const out = driftingMetrics([
      metric("MET-10", "stale"),
      metric("MET-2", "off_track"),
      metric("MET-3", "on_track"),
      metric("MET-9", "stale"),
      metric("MET-4", "regressed"),
    ]);
    expect(out.map((m) => m.code)).toEqual(["MET-4", "MET-2", "MET-9", "MET-10"]);
  });
});

describe("metricsByObjective", () => {
  it("groups metrics under their objective id", () => {
    const g = metricsByObjective([metric("MET-1", "x", "a"), metric("MET-2", "x", "b"), metric("MET-3", "x", "a")]);
    expect(g.a.map((m) => m.code)).toEqual(["MET-1", "MET-3"]);
    expect(g.b.map((m) => m.code)).toEqual(["MET-2"]);
    expect(g.c).toBeUndefined();
  });
});

describe("requirement helpers", () => {
  const reqs = [
    { id: "r1", code: "REQ-1", status: "accepted", objective_ids: ["o1"] },
    { id: "r2", code: "REQ-2", status: "accepted", objective_ids: [] },
    { id: "r3", code: "REQ-3", status: "removed", objective_ids: [] },
    { id: "r4", code: "REQ-4", status: "draft" },
    { id: "r5", code: "REQ-5", status: "superseded", objective_ids: null },
  ];

  it("maps ids to codes, keeping unknown ids", () => {
    expect(requirementCodes(["r2", "r1", "zz"], reqs)).toEqual(["REQ-2", "REQ-1", "zz"]);
    expect(requirementCodes(undefined, reqs)).toEqual([]);
  });

  it("finds live requirements serving no objective", () => {
    expect(requirementsWithoutObjective(reqs).map((r) => r.code)).toEqual(["REQ-2", "REQ-4"]);
  });

  it("offers live requirements not already linked", () => {
    expect(linkableRequirements(reqs, ["r1"]).map((r) => r.code)).toEqual(["REQ-2", "REQ-4"]);
    expect(linkableRequirements(reqs, null).map((r) => r.code)).toEqual(["REQ-1", "REQ-2", "REQ-4"]);
  });
});

describe("metricRange", () => {
  it("renders baseline → target with direction and unit", () => {
    expect(metricRange({ direction: "increase", baseline: 12, target: 20, unit: "%" })).toBe("↑ 12% → 20%");
    expect(metricRange({ direction: "decrease", baseline: 900, target: 300, unit: "ms" })).toBe("↓ 900 ms → 300 ms");
    expect(metricRange({ direction: "maintain", target: 0 })).toBe("= → 0");
    expect(metricRange({ direction: "increase", baseline: 0, target: 5 })).toBe("↑ 0 → 5");
  });

  it("falls back to the raw direction when unknown", () => {
    expect(directionArrow("sideways")).toBe("sideways");
  });
});

describe("outcomeGapRows", () => {
  it("labels non-empty gaps in chain order", () => {
    expect(
      outcomeGapRows({
        objectives_without_metrics: ["OBJ-2"],
        objectives_without_requirements: [],
        requirements_without_objectives: ["REQ-3", "REQ-4"],
      }),
    ).toEqual([
      { label: "Objectives with no metric", codes: ["OBJ-2"] },
      { label: "Requirements serving no objective", codes: ["REQ-3", "REQ-4"] },
    ]);
  });

  it("handles a missing or empty gaps object", () => {
    expect(outcomeGapRows(undefined)).toEqual([]);
    expect(outcomeGapRows({})).toEqual([]);
  });
});

describe("parseNumber", () => {
  it("parses numbers and rejects blank or invalid input", () => {
    expect(parseNumber(" 4.5 ")).toBe(4.5);
    expect(parseNumber("0")).toBe(0);
    expect(parseNumber("")).toBeUndefined();
    expect(parseNumber("  ")).toBeUndefined();
    expect(parseNumber("abc")).toBeUndefined();
  });
});

describe("objective and metric edit bodies", () => {
  const o = { statement: "Cooks share recipes", rationale: "growth", owner: "", status: "active" as const };

  it("sends only what changed, nothing for an untouched form", () => {
    expect(objectiveEditBody(o, objectiveDraft(o))).toEqual({ body: {} });
    expect(objectiveEditBody(o, { ...objectiveDraft(o), owner: " sam ", status: "achieved" })).toEqual({
      body: { owner: "sam", status: "achieved" },
    });
    // Emptying the rationale is a change (to empty), not an omission.
    expect(objectiveEditBody(o, { ...objectiveDraft(o), rationale: "" })).toEqual({ body: { rationale: "" } });
  });

  it("refuses an empty statement", () => {
    expect(objectiveEditBody(o, { ...objectiveDraft(o), statement: "  " })).toEqual({ error: "An objective needs a statement." });
  });

  const m = { name: "Shares", direction: "increase" as const, target: 50, baseline: 10, unit: "" };

  it("clears a baseline emptied in the form, and sets a changed one", () => {
    expect(metricEditBody(m, metricDraft(m))).toEqual({ body: {} });
    expect(metricEditBody(m, { ...metricDraft(m), baseline: "" })).toEqual({ body: { clear_baseline: true } });
    expect(metricEditBody(m, { ...metricDraft(m), baseline: "12", target: "60", unit: "%" })).toEqual({
      body: { baseline: 12, target: 60, unit: "%" },
    });
    // No baseline to begin with: blank stays blank, nothing to clear.
    expect(metricEditBody({ ...m, baseline: undefined }, { ...metricDraft(m), baseline: "" })).toEqual({ body: {} });
  });

  it("refuses a non-numeric target or baseline and an empty name", () => {
    expect(metricEditBody(m, { ...metricDraft(m), target: "lots" })).toEqual({ error: "Target must be a number." });
    expect(metricEditBody(m, { ...metricDraft(m), baseline: "x" })).toEqual({ error: "Baseline must be a number (or blank)." });
    expect(metricEditBody(m, { ...metricDraft(m), name: "" })).toEqual({ error: "A metric needs a name." });
  });
});
