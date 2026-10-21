import { describe, expect, it } from "vitest";
import type { DecisionItem, FindingItem } from "./JudgmentCard";
import {
  blockingFindingsFirst,
  decisionsForDefine,
  decisionTypeLabel,
  findingsForDefine,
  findingsForPlan,
  isBlockingFinding,
  otherOpenFindings,
  severityRank,
  sortFindingsBlockingFirst,
} from "./judgmentScope";

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
  it("keeps execution_plan findings, plus the legacy/task target_types the backend contract calls out", () => {
    const all = [
      finding("prd"),
      finding("requirement"),
      finding("execution_plan"),
      finding("change_set"),
      finding("", {}),
      finding("initiative"),
      finding("task"),
    ];
    const kept = findingsForPlan(all);
    expect(kept.map((f) => f.target_type).sort()).toEqual(
      ["", "execution_plan", "initiative", "task"].sort(),
    );
  });

  it("includes a HIGH plan finding — Plan is where approval happens", () => {
    const all = [finding("execution_plan", { severity: "high", finding: "No execution plan ... was provided" })];
    const kept = findingsForPlan(all);
    expect(kept).toHaveLength(1);
    expect(kept[0].finding).toContain("No execution plan");
  });

  // C2: a plan-stage deterministic check is Plan's regardless of what
  // target_type its row happens to carry.
  it("keeps a finding with review_run_id deterministic-plan-check whatever its target_type", () => {
    const all = [finding("change_set", { review_run_id: "deterministic-plan-check" })];
    expect(findingsForPlan(all)).toHaveLength(1);
  });

  it("does not claim an unrelated review_run_id", () => {
    const all = [finding("change_set", { review_run_id: "some-other-check" })];
    expect(findingsForPlan(all)).toHaveLength(0);
  });
});

// C2: this is the reported incident — a CRITICAL finding whose target_type
// matched neither Define's nor Plan's allowlist was simply never shown,
// which silently blocked plan approval with no visible reason. No finding
// may fall through both functions.
describe("otherOpenFindings", () => {
  it("is empty when every finding matches Define or Plan", () => {
    const all = [finding("prd"), finding("requirement"), finding("execution_plan"), finding("task")];
    expect(otherOpenFindings(all)).toHaveLength(0);
  });

  it("leaves change-set findings to the Build page, which shows them", () => {
    expect(otherOpenFindings([finding("change_set", { severity: "critical" })])).toHaveLength(0);
  });

  it("catches a target_type neither page recognizes — the reported defect", () => {
    const all = [finding("some_unplaced_target", { severity: "critical" })];
    const other = otherOpenFindings(all);
    expect(other).toHaveLength(1);
    expect(other[0].severity).toBe("critical");
  });

  it("catches an unknown/future target_type", () => {
    expect(otherOpenFindings([finding("some_new_target")])).toHaveLength(1);
  });

  it("does not duplicate a finding that Plan already claims via review_run_id", () => {
    const all = [finding("change_set", { review_run_id: "deterministic-plan-check" })];
    expect(otherOpenFindings(all)).toHaveLength(0);
  });
});

describe("decisionsForDefine", () => {
  // M6 — this is the reported defect: decisionsForDefine used to keep only
  // "product" decisions, so a design-stage decision appeared on no page at
  // all (DesignPage does not render decisions). Define now shows every type.
  it("keeps every decision type, not just product", () => {
    const all = [decision("product"), decision("architecture"), decision("design"), decision("security"), decision("operations")];
    expect(decisionsForDefine(all)).toHaveLength(5);
  });

  it("keeps a legacy decision with no recorded type", () => {
    expect(decisionsForDefine([decision(undefined)])).toHaveLength(1);
  });
});

describe("decisionTypeLabel", () => {
  it("labels each known decision type", () => {
    expect(decisionTypeLabel("product")).toBe("PRD");
    expect(decisionTypeLabel("architecture")).toBe("Architecture");
    expect(decisionTypeLabel("design")).toBe("Design");
    expect(decisionTypeLabel("security")).toBe("Security");
    expect(decisionTypeLabel("operations")).toBe("Operations");
  });

  it("treats an undefined type as the legacy PRD case", () => {
    expect(decisionTypeLabel(undefined)).toBe("PRD");
  });
});

// P0.2 — blocking-first ordering.
describe("sortFindingsBlockingFirst / isBlockingFinding", () => {
  it("orders critical → high → medium → low → observation", () => {
    const all = [
      finding("prd", { id: "low", severity: "low" }),
      finding("prd", { id: "critical", severity: "critical" }),
      finding("prd", { id: "medium", severity: "medium" }),
      finding("prd", { id: "high", severity: "high" }),
      finding("prd", { id: "observation", severity: "observation" }),
    ];
    expect(sortFindingsBlockingFirst(all).map((f) => f.id)).toEqual([
      "critical",
      "high",
      "medium",
      "low",
      "observation",
    ]);
  });

  it("sorts an unknown severity last, never dropping it", () => {
    const all = [
      finding("prd", { id: "mystery", severity: "mystery" }),
      finding("prd", { id: "high", severity: "high" }),
    ];
    expect(sortFindingsBlockingFirst(all).map((f) => f.id)).toEqual(["high", "mystery"]);
  });

  it("treats critical and high as blocking, the rest as advisory", () => {
    expect(isBlockingFinding(finding("prd", { severity: "critical" }))).toBe(true);
    expect(isBlockingFinding(finding("prd", { severity: "high" }))).toBe(true);
    expect(isBlockingFinding(finding("prd", { severity: "medium" }))).toBe(false);
    expect(isBlockingFinding(finding("prd", { severity: "low" }))).toBe(false);
  });

  it("severityRank is monotonically increasing with severity", () => {
    expect(severityRank("critical")).toBeLessThan(severityRank("high"));
    expect(severityRank("high")).toBeLessThan(severityRank("medium"));
    expect(severityRank("medium")).toBeLessThan(severityRank("low"));
  });
});

describe("blockingFindingsFirst", () => {
  it("promotes a blocking finding triage ranked below the top-N", () => {
    const sorted = sortFindingsBlockingFirst([
      finding("prd", { id: "crit", severity: "critical" }),
      finding("prd", { id: "med", severity: "medium" }),
    ]);
    const split = { needsYou: [], optional: sorted }; // triage ranked neither priority
    const out = blockingFindingsFirst(sorted, split);
    expect(out.needsYou.map((f) => f.id)).toEqual(["crit"]);
    expect(out.optional.map((f) => f.id)).toEqual(["med"]);
  });

  it("keeps a non-blocking finding collapsed when triage did not rank it", () => {
    const sorted = sortFindingsBlockingFirst([finding("prd", { id: "med", severity: "medium" })]);
    const out = blockingFindingsFirst(sorted, { needsYou: [], optional: sorted });
    expect(out.needsYou).toHaveLength(0);
    expect(out.optional).toHaveLength(1);
  });
});
