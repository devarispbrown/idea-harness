// Phase scoping for judgment cards (D1/C2), extracted as pure functions so
// "which page does this item belong on" is unit-testable without a DOM.
//
// Neither decisions nor findings are stored phase-scoped — both tables are
// initiative-wide — so the page has to work it out from the data:
//
//   - A Finding's target_type says which artifact the review that produced it
//     looked at (see internal/review.ParseFindings' callers): "prd" for the
//     PRD review, "execution_plan" for the plan review, "requirement" for the
//     deterministic PRD checks, "change_set" for Build's per-task review.
//     The plan-stage deterministic checks (traceability, dependency cycle,
//     unresolved dependency) now carry target_type "execution_plan", but a
//     row written before that landed may still carry "" or "initiative"
//     (backend contract), and a plan-scoped task finding carries "task".
//     Independent of target_type, review_run_id "deterministic-plan-check"
//     also marks a finding as Plan's.
//   - A Decision's type says which stage raised it: "product" is a PRD
//     decision (§19); "architecture"/"design"/"security"/"operations" are
//     raised during Design. DesignPage does not render decisions at all
//     (M6), so Define now shows every decision, labelled by type, rather
//     than silently dropping the non-"product" ones with nowhere else to go.
//
// Before this, Define fetched every finding for the initiative and rendered
// it unfiltered, so a plan-review finding like "No execution plan ... was
// provided" showed up on Define, and Plan — the phase where approval
// actually happens — showed no findings at all. Fixing THAT introduced a new
// failure mode (C2): a finding whose target_type matched neither page's set
// (e.g. a future target_type, or a data shape this UI doesn't know about yet)
// was simply unshown — including, in the reported incident, a CRITICAL
// finding that silently blocked plan approval with no visible reason. Every
// finding must now land somewhere: findingsForDefine + findingsForPlan +
// otherOpenFindings partitions the full set with no gap.
import type { DecisionItem, FindingItem } from "./JudgmentCard";

const DEFINE_FINDING_TARGETS = new Set(["prd", "requirement"]);
// "" and "initiative" are legacy target_types a plan-stage deterministic
// finding may still carry (backend contract); "task" is a plan-scoped task
// finding. All four are Plan's.
const PLAN_FINDING_TARGETS = new Set(["execution_plan", "", "initiative", "task"]);
const PLAN_REVIEW_RUN_ID = "deterministic-plan-check";

export function findingsForDefine(findings: FindingItem[]): FindingItem[] {
  return findings.filter((f) => DEFINE_FINDING_TARGETS.has(f.target_type ?? ""));
}

function isPlanFinding(f: FindingItem): boolean {
  return PLAN_FINDING_TARGETS.has(f.target_type ?? "") || f.review_run_id === PLAN_REVIEW_RUN_ID;
}

export function findingsForPlan(findings: FindingItem[]): FindingItem[] {
  return findings.filter(isPlanFinding);
}

// otherOpenFindings (C2's catch-all) is every finding that matches neither
// Define's nor Plan's set — a target_type this UI doesn't recognize, present
// or future. Rendered as Define's "Other open items" list, so nothing a
// reviewer needs to see can be silently dropped by an incomplete allowlist
// the way the reported CRITICAL finding was.
//
// change_set findings are excluded: the Build page shows every one of them
// on its change set, and listing them here too read as "didn't match a known
// phase", which was not true.
export function otherOpenFindings(findings: FindingItem[]): FindingItem[] {
  return findings.filter((f) => {
    const target = f.target_type ?? "";
    return target !== "change_set" && !DEFINE_FINDING_TARGETS.has(target) && !isPlanFinding(f);
  });
}

// decisionsForDefine (M6) used to keep PRD ("product") decisions only, which
// meant a design-stage decision (architecture/design/security/operations)
// appeared on no page at all: DesignPage does not render decisions. Define
// now shows every decision type — DecisionCard's caller labels each by type
// (see decisionTypeLabel) so a non-PRD decision doesn't read as if it were
// one.
export function decisionsForDefine(decisions: DecisionItem[]): DecisionItem[] {
  return decisions;
}

// decisionTypeLabel is the badge Define shows next to a decision now that it
// renders every type, not just "product" ones. A decision written before the
// `type` field existed has no type recorded, and is the same legacy case
// judgment_reason treats as PRD elsewhere in this app.
export function decisionTypeLabel(type: string | undefined): string {
  switch (type || "product") {
    case "product":
      return "PRD";
    case "architecture":
      return "Architecture";
    case "design":
      return "Design";
    case "security":
      return "Security";
    case "operations":
      return "Operations";
    default:
      return type as string;
  }
}
