import { describe, expect, it } from "vitest";
import { changeStatusClass, dispositionClass, dispositionLabel } from "./statusChips";

describe("dispositionLabel", () => {
  it("replaces underscores with spaces", () => {
    expect(dispositionLabel("accepted_risk")).toBe("accepted risk");
    expect(dispositionLabel("needs_human_judgment")).toBe("needs human judgment");
  });

  it("leaves plain labels unchanged", () => {
    expect(dispositionLabel("mitigate")).toBe("mitigate");
  });
});

describe("dispositionClass", () => {
  it("maps known dispositions", () => {
    expect(dispositionClass("mitigate")).toBe("mitigate");
    expect(dispositionClass("accepted_risk")).toBe("fact");
    expect(dispositionClass("deferred")).toBe("medium");
  });

  it("defaults unknown dispositions to low", () => {
    expect(dispositionClass("whatever")).toBe("low");
  });
});

describe("changeStatusClass", () => {
  it("maps every known ChangeSet status to a class", () => {
    const cases: [string, string][] = [
      ["draft", "change-draft"],
      ["checks_running", "change-checks-running"],
      ["review_required", "change-review-required"],
      ["rejected", "change-rejected"],
      ["changes_requested", "change-changes-requested"],
      ["superseded", "change-superseded"],
      ["approved", "change-approved"],
      ["applying", "change-applying"],
      ["applied", "change-applied"],
      ["reverted", "change-reverted"],
      ["conflicted", "change-conflicted"],
    ];
    for (const [status, cls] of cases) {
      expect(changeStatusClass(status)).toBe(cls);
    }
  });

  it("returns empty for unknown statuses", () => {
    expect(changeStatusClass("bogus")).toBe("");
  });
});
