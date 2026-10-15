import { describe, expect, it } from "vitest";
import type { Gap } from "./gaps";
import { countBy, filterGaps, gapAction, gapKindTitle, groupGapsByKind } from "./gapsView";
import { formatValue, isMeasurable, metricProgress, observationRows, sparkline } from "./outcomesView";
import { blockingTrend, demotedSummary, groupLoops, outcomeTone, sevSummary, type ReviewTelemetry } from "./convergence";
import { citationText, historyByDimension, sortedChecks } from "./qualityHistory";
import type { MetricObservation } from "./api";
import { graphLayers } from "./GraphDiagram";

describe("graphLayers", () => {
  it("counts each layer of the chain in order, excluding dead requirements", () => {
    const l = graphLayers("i", {
      objectives: 2,
      onTrack: 1,
      reqs: [{ status: "verified" }, { status: "approved" }, { status: "superseded" }],
      tasks: [{ status: "done" }],
      changeSets: [{ status: "applied" }, { status: "rejected" }],
      evidence: 4,
    });
    expect(l.map((x) => [x.key, x.count, x.done])).toEqual([
      ["objective", 2, 1],
      ["requirement", 2, 1],
      ["task", 1, 1],
      ["change_set", 2, 1],
      ["evidence", 4, undefined],
    ]);
    expect(l[1].href).toBe("#/i/i/define");
  });
});

const gap = (kind: string, severity: string, nodes: Gap["nodes"] = [], next = "idea x"): Gap => ({
  kind,
  severity,
  state: "s",
  nodes,
  why: "why",
  next,
  dependents: 0,
  initiative_id: "i",
});

describe("gaps grouping", () => {
  const gaps = [
    gap("unvalidated_assumption", "low"),
    gap("untested_acceptance_criterion", "medium"),
    gap("unvalidated_assumption", "low"),
    gap("objective_without_metric", "high"),
  ];
  it("groups by kind, most severe group first, with a label for unknown kinds", () => {
    const g = groupGapsByKind(gaps);
    expect(g.map((x) => x.kind)).toEqual(["objective_without_metric", "untested_acceptance_criterion", "unvalidated_assumption"]);
    expect(g[0].title).toBe("Objective without metric");
    expect(g[2].gaps).toHaveLength(2);
    expect(gapKindTitle("orphan_task")).toBe("Tasks that serve no requirement");
  });
  it("filters by kind and severity, and counts for the options", () => {
    expect(filterGaps(gaps, { severity: "low" })).toHaveLength(2);
    expect(filterGaps(gaps, { kind: "unvalidated_assumption", severity: "high" })).toHaveLength(0);
    expect(filterGaps(gaps, {})).toHaveLength(4);
    expect(countBy(gaps, "severity")).toEqual({ low: 2, medium: 1, high: 1 });
  });
  it("picks a UI action where one exists and the CLI otherwise", () => {
    expect(gapAction(gap("unvalidated_assumption", "low", [{ type: "assertion", id: "a1", code: "a1" }]), "i")).toEqual({ kind: "assertion", assertionId: "a1" });
    expect(gapAction(gap("unimplemented_requirement", "high"), "i")).toMatchObject({ kind: "link", href: "#/i/i/plan" });
    expect(gapAction(gap("invalidated_decision", "high", [{ type: "decision", id: "d1", code: "DEC-1" }]), "i")).toMatchObject({ href: "#/i/i/decision/d1" });
    expect(gapAction(gap("brand_new_kind", "low", [{ type: "metric", id: "m", code: "MET-1" }]), "i")).toMatchObject({ href: "#/i/i/outcomes" });
    expect(gapAction(gap("brand_new_kind", "low", [], "idea fix it"), "i")).toEqual({ kind: "cli", command: "idea fix it" });
    // A known kind missing the node its action needs falls back too.
    expect(gapAction(gap("unvalidated_assumption", "low", [], "idea assumption x accept"), "i").kind).toBe("cli");
  });
});

describe("sparkline", () => {
  it("scales into the box with the target and baseline in the domain", () => {
    const sp = sparkline([5, 10], 100, 20, { pad: 0, target: 20, baseline: 0 });
    expect(sp.min).toBe(0);
    expect(sp.max).toBe(20);
    expect(sp.points).toEqual([
      { x: 0, y: 15, value: 5 },
      { x: 100, y: 10, value: 10 },
    ]);
    expect(sp.targetY).toBe(0);
    expect(sp.baselineY).toBe(20);
    expect(sp.path).toBe("0,15 100,10");
  });
  it("centres a flat series and a single point instead of dividing by zero", () => {
    const flat = sparkline([3, 3, 3], 100, 20, { pad: 0 });
    expect(flat.points.every((p) => p.y === 10)).toBe(true);
    const one = sparkline([7], 100, 20, { pad: 0 });
    expect(one.points).toEqual([{ x: 50, y: 10, value: 7 }]);
  });
  it("handles an empty series", () => {
    expect(sparkline([], 100, 20).points).toEqual([]);
  });
});

describe("metric progress and history", () => {
  const status = (s: object) => ({ drift: "on_track", observations: 1, ...s }) as never;
  it("places the latest value between baseline and target, either direction", () => {
    expect(metricProgress({ direction: "decrease", baseline: 90, target: 60, status: status({ latest: 64, effective_baseline: 90 }) }).fraction).toBeCloseTo(26 / 30);
    expect(metricProgress({ direction: "increase", baseline: 100, target: 250, status: status({ latest: 50 }) }).fraction).toBe(0);
  });
  it("says nothing when progress is undefined, and trusts the daemon's achieved", () => {
    expect(metricProgress({ direction: "increase", target: 5, status: status({}) }).fraction).toBeNull();
    expect(metricProgress({ direction: "maintain", baseline: 1, target: 5, status: status({ latest: 3 }) }).fraction).toBeNull();
    expect(metricProgress({ direction: "increase", target: 1, status: status({ latest: 95, effective_baseline: 85, achieved: true }) })).toEqual({ fraction: 1, achieved: true });
  });
  it("pairs observations with deltas and the change sets landed before them, newest first", () => {
    const obs = [
      { id: "b", value: 7, observed_at: "2026-10-05T00:00:00Z", change_set_ids: ["cs1"] },
      { id: "a", value: 10, observed_at: "2026-10-01T00:00:00Z", change_set_ids: [] },
    ] as unknown as MetricObservation[];
    const rows = observationRows(obs, { cs1: "CHG-1" });
    expect(rows.map((r) => r.observation.id)).toEqual(["b", "a"]);
    expect(rows[0].delta).toBe(-3);
    expect(rows[0].changeSets).toEqual([{ id: "cs1", code: "CHG-1" }]);
    expect(rows[1].delta).toBeUndefined();
  });
  it("formats values and knows which sources can be measured", () => {
    expect(formatValue(64, "s")).toBe("64 s");
    expect(formatValue(12.345, "%")).toBe("12.35%");
    expect(formatValue(undefined)).toBe("—");
    expect(isMeasurable({ source: { kind: "command" } })).toBe(true);
    expect(isMeasurable({ source: { kind: "manual" } })).toBe(false);
  });
});

describe("review convergence", () => {
  const t = (review: string, cycle: number, outcome: string, extra: Partial<ReviewTelemetry> = {}): ReviewTelemetry => ({
    review,
    cycle,
    mode: "guarded",
    deterministic: 0,
    open_blocking: 0,
    prev_open_blocking: -1,
    outcome,
    ...extra,
  });
  it("splits loops where the cycle number resets, per review", () => {
    const tel = [t("prd", 1, "revise"), t("prd", 2, "converged"), t("execution_plan", 1, "converged"), t("prd", 1, "stalled")];
    const prd = groupLoops(tel, "prd");
    expect(prd.map((l) => [l.cycles.length, l.outcome])).toEqual([
      [2, "converged"],
      [1, "stalled"],
    ]);
    expect(groupLoops(tel)).toHaveLength(3);
  });
  it("summarizes severities, demotions and the blocking trend", () => {
    expect(sevSummary({ low: 1, high: 2, medium: 0 })).toBe("2 high · 1 low");
    expect(sevSummary(undefined)).toBe("none");
    expect(demotedSummary({ uncited: 2 })).toBe("uncited ×2");
    expect(blockingTrend({ open_blocking: 1, prev_open_blocking: 3 })).toBe("↓2");
    expect(blockingTrend({ open_blocking: 3, prev_open_blocking: 3 })).toBe("no change");
    expect(blockingTrend({ open_blocking: 3, prev_open_blocking: -1 })).toBe("");
    expect(outcomeTone("converged")).toBe("ok");
    expect(outcomeTone("max_cycles")).toBe("bad");
    expect(outcomeTone("something_new")).toBe("muted");
  });
});

describe("quality history", () => {
  it("groups by dimension in order and renders citations", () => {
    const h = historyByDimension([
      { id: "1", dimension: "security", status: "fail", summary: "", checks: [], requirements: [] },
      { id: "2", dimension: "docs_completeness", status: "pass", summary: "", checks: [], requirements: [] },
      { id: "3", dimension: "security", status: "pass", summary: "", checks: [], requirements: [] },
    ]);
    expect(h.security.map((r) => r.id)).toEqual(["1", "3"]);
    expect(citationText({ path: "README.md", line: 4, requirement: "REQ-1" })).toBe("README.md:4 (REQ-1)");
    expect(citationText({ requirement: "REQ-2" })).toBe("REQ-2");
  });
  it("puts failing checks first", () => {
    const c = (name: string, status: string) => ({ name, status, method: "deterministic" });
    expect(sortedChecks([c("a", "pass"), c("b", "fail"), c("c", "warn"), c("d", "not_applicable")]).map((x) => x.name)).toEqual(["b", "c", "a", "d"]);
  });
});
