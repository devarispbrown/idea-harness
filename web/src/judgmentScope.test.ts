import { describe, expect, it } from "vitest";
import type { DecisionItem, FindingItem } from "./JudgmentCard";
import { decisionsForDefine, findingsForDefine, findingsForPlan } from "./judgmentScope";

function finding(target_type: string, overrides: Partial<FindingItem> = {}): FindingItem {
  return {
    id: target_type + "-id",
    severity: "high",
    finding: `finding for ${target_type}`,
    recommendation: "",
    target_type,
    ...overrides,
  };
}

function decision(type: string | undefined, overrides: Partial<DecisionItem> = {}): DecisionItem {
  return {
    id: type ?? "legacy",
    code: "DEC-1",
    question: "q",
    recommendation: "r",
    options: [],
    status: "open",
    type,
    ...overrides,
  };
}

describe("findingsForDefine", () => {
  it("keeps prd and requirement findings", () => {
    const all = [finding("prd"), finding("requirement"), finding("execution_plan"), finding("change_set")];
    const kept = findingsForDefine(all);
    expect(kept.map((f) => f.target_type).sort()).toEqual(["prd", "requirement"]);
  });

  it("excludes plan-review findings — this is the reported defect", () => {
    const all = [finding("execution_plan", { severity: "high", finding: "No execution plan ... was provided" })];
    expect(findingsForDefine(all)).toHaveLength(0);
  });

  it("drops a finding with no target_type rather than guessing", () => {
    const all = [finding("", {})];
    expect(findingsForDefine(all)).toHaveLength(0);
  });
});

describe("findingsForPlan", () => {
  it("keeps only execution_plan findings", () => {
    const all = [finding("prd"), finding("requirement"), finding("execution_plan"), finding("change_set")];
    const kept = findingsForPlan(all);
    expect(kept.map((f) => f.target_type)).toEqual(["execution_plan"]);
  });

  it("includes a HIGH plan finding — Plan is where approval happens", () => {
    const all = [finding("execution_plan", { severity: "high", finding: "No execution plan ... was provided" })];
    const kept = findingsForPlan(all);
    expect(kept).toHaveLength(1);
    expect(kept[0].finding).toContain("No execution plan");
  });
});

describe("decisionsForDefine", () => {
  it("keeps product decisions", () => {
    expect(decisionsForDefine([decision("product")])).toHaveLength(1);
  });

  it("excludes design-stage decisions (architecture/design/security/operations)", () => {
    const all = [decision("architecture"), decision("design"), decision("security"), decision("operations")];
    expect(decisionsForDefine(all)).toHaveLength(0);
  });

  it("treats an undefined type as a legacy PRD decision", () => {
    expect(decisionsForDefine([decision(undefined)])).toHaveLength(1);
  });
});
