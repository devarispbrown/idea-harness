// Phase scoping for judgment cards (D1), extracted as pure functions so
// "which page does this item belong on" is unit-testable without a DOM.
//
// Neither decisions nor findings are stored phase-scoped — both tables are
// initiative-wide — so the page has to work it out from the data:
//
//   - A Finding's target_type says which artifact the review that produced it
//     looked at (see internal/review.ParseFindings' callers): "prd" for the
//     PRD review, "execution_plan" for the plan review, "requirement" for the
//     deterministic PRD checks, "change_set" for Build's per-task review.
//   - A Decision's type says which stage raised it: "product" is a PRD
//     decision (§19); "architecture"/"design"/"security"/"operations" are
//     raised during Design, and belong there, not on Define.
//
// Before this, Define fetched every finding for the initiative and rendered
// it unfiltered, so a plan-review finding like "No execution plan ... was
// provided" showed up on Define, and Plan — the phase where approval
// actually happens — showed no findings at all.
import type { DecisionItem, FindingItem } from "./JudgmentCard";

const DEFINE_FINDING_TARGETS = new Set(["prd", "requirement"]);
const PLAN_FINDING_TARGETS = new Set(["execution_plan"]);

export function findingsForDefine(findings: FindingItem[]): FindingItem[] {
  return findings.filter((f) => DEFINE_FINDING_TARGETS.has(f.target_type ?? ""));
}

export function findingsForPlan(findings: FindingItem[]): FindingItem[] {
  return findings.filter((f) => PLAN_FINDING_TARGETS.has(f.target_type ?? ""));
}

// decisionsForDefine keeps PRD ("product") decisions only. A decision written
// before the `type` field existed has no type recorded; treated as a PRD
// decision (the only kind that existed then), matching the "undefined is the
// legacy case" precedent used for judgment_reason elsewhere in this app.
export function decisionsForDefine(decisions: DecisionItem[]): DecisionItem[] {
  return decisions.filter((d) => (d.type || "product") === "product");
}
