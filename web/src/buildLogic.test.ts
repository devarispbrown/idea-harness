import { describe, expect, it } from "vitest";
import {
  appliedOutcomeLabel,
  buildFailure,
  buildHeaderMeta,
  conflictCause,
  diffChangeTypeLabel,
  errMessage,
  isOperationInFlight,
  isWholeFileRewrite,
  latestOperationOfType,
  runButtonState,
  runningTitle,
  shouldResetRetryBusy,
} from "./buildLogic";

describe("isOperationInFlight", () => {
  it("is in flight while running or pending", () => {
    expect(isOperationInFlight("running")).toBe(true);
    expect(isOperationInFlight("pending")).toBe(true);
  });

  it("is settled once succeeded, failed, or cancelled", () => {
    expect(isOperationInFlight("succeeded")).toBe(false);
    expect(isOperationInFlight("failed")).toBe(false);
    expect(isOperationInFlight("cancelled")).toBe(false);
  });
});

describe("errMessage", () => {
  it("unwraps an Error to its message, without the 'Error:' prefix", () => {
    expect(errMessage(new Error("provider refused the request"))).toBe(
      "provider refused the request",
    );
  });

  it("stringifies anything that isn't an Error", () => {
    expect(errMessage("plain string")).toBe("plain string");
  });
});

describe("runButtonState", () => {
  const task = { id: "t1", human_id: "TASK-2", title: "Add the retry button" };

  it("shows the run button named after the next task", () => {
    const s = runButtonState(task, "", false);
    expect(s.visible).toBe(true);
    expect(s.label).toBe("Run TASK-2: Add the retry button");
    expect(s.disabled).toBe(false);
  });

  it("disables the button and keeps it visible when blocked", () => {
    const s = runButtonState(task, "a checks_running change set is open", false);
    expect(s.visible).toBe(true);
    expect(s.disabled).toBe(true);
  });

  it("hides the button entirely while something is running, not just disables it", () => {
    const s = runButtonState(task, "", true);
    expect(s.visible).toBe(false);
  });

  it("hides the button when there is no next task", () => {
    expect(runButtonState(null, "", false).visible).toBe(false);
    expect(runButtonState(undefined, "", false).visible).toBe(false);
  });
});

describe("latestOperationOfType", () => {
  it("picks the newest by created_at, not array order", () => {
    const ops = [
      { type: "execute_initiative", created_at: "2026-01-01T00:00:00Z" },
      { type: "execute_initiative", created_at: "2026-01-03T00:00:00Z" },
      { type: "apply_change_set", created_at: "2026-01-05T00:00:00Z" },
      { type: "execute_initiative", created_at: "2026-01-02T00:00:00Z" },
    ];
    expect(latestOperationOfType(ops, "execute_initiative")?.created_at).toBe(
      "2026-01-03T00:00:00Z",
    );
  });

  it("returns undefined when no operation of that type exists", () => {
    expect(latestOperationOfType([], "execute_initiative")).toBeUndefined();
  });
});

describe("buildFailure", () => {
  const failed = { id: "op1", type: "execute_initiative", status: "failed", created_at: "2026-01-01T00:00:00Z", error: "boom" };

  it("surfaces the latest failed execute operation", () => {
    expect(buildFailure([failed])).toBe(failed);
  });

  it("stays quiet while anything is running, even a stale failure", () => {
    const running = { id: "op2", type: "retry_initiative", status: "running", created_at: "2026-01-02T00:00:00Z" };
    expect(buildFailure([failed, running])).toBeNull();
  });

  it("stays quiet when the latest execute succeeded", () => {
    const ok = { id: "op3", type: "execute_initiative", status: "succeeded", created_at: "2026-01-02T00:00:00Z" };
    expect(buildFailure([failed, ok])).toBeNull();
  });

  it("stays quiet with no execute operations at all", () => {
    expect(buildFailure([])).toBeNull();
  });
});

describe("shouldResetRetryBusy", () => {
  it("resets once the retried operation settles", () => {
    expect(shouldResetRetryBusy("failed", "x", "x")).toBe(true);
    expect(shouldResetRetryBusy("succeeded", "x", "x")).toBe(true);
  });

  it("stays busy while the retried operation is still running", () => {
    expect(shouldResetRetryBusy("running", "x", "x")).toBe(false);
  });

  it("resets when the failure text changes even with no operation status yet", () => {
    expect(shouldResetRetryBusy(undefined, "first failure", "second failure")).toBe(true);
  });

  it("does not reset on the initial mount (no previous reason to compare)", () => {
    expect(shouldResetRetryBusy(undefined, undefined, "first failure")).toBe(false);
  });

  it("does not fire on an unchanged reason with no settled operation", () => {
    expect(shouldResetRetryBusy(undefined, "same", "same")).toBe(false);
  });
});

describe("runningTitle", () => {
  const labels = { execute_initiative: "Executing the plan", apply_change_set: "Applying change set" };
  const task = { id: "t1", human_id: "TASK-2", title: "Add the retry button" };

  it("names the task for a running execute", () => {
    expect(runningTitle("execute_initiative", labels, task)).toBe("Running TASK-2: Add the retry button");
  });

  it("falls back to the generic label when the task isn't known yet", () => {
    expect(runningTitle("execute_initiative", labels, null)).toBe("Executing the plan");
  });

  it("leaves every other operation type on its generic label", () => {
    expect(runningTitle("apply_change_set", labels, task)).toBe("Applying change set");
  });

  it("falls back to the raw type for an unlabeled operation", () => {
    expect(runningTitle("mystery_op", labels, task)).toBe("mystery_op");
  });
});

describe("diffChangeTypeLabel", () => {
  it("maps the three known change types", () => {
    expect(diffChangeTypeLabel("add")).toBe("added");
    expect(diffChangeTypeLabel("modify")).toBe("modified");
    expect(diffChangeTypeLabel("delete")).toBe("deleted");
  });

  it("passes through anything unrecognized rather than hiding it", () => {
    expect(diffChangeTypeLabel("rename")).toBe("rename");
  });
});

describe("isWholeFileRewrite", () => {
  it("flags a modify patch with no preserved context lines", () => {
    const patch = [
      "--- a/routes.go",
      "+++ b/routes.go",
      "@@ -1,3 +1,2 @@",
      "-old line one",
      "-old line two",
      "-old line three",
      "+new line one",
      "+new line two",
    ].join("\n");
    expect(isWholeFileRewrite("modify", patch)).toBe(true);
  });

  it("does not flag an incremental patch that keeps context lines", () => {
    const patch = [
      "--- a/routes.go",
      "+++ b/routes.go",
      "@@ -10,3 +10,3 @@",
      " unchanged before",
      "-removed route",
      "+added route",
      " unchanged after",
    ].join("\n");
    expect(isWholeFileRewrite("modify", patch)).toBe(false);
  });

  it("never flags an add or a delete, only a modify", () => {
    const patch = "@@ -1,1 +1,1 @@\n-a\n+b";
    expect(isWholeFileRewrite("add", patch)).toBe(false);
    expect(isWholeFileRewrite("delete", patch)).toBe(false);
  });

  it("is false with no patch text", () => {
    expect(isWholeFileRewrite("modify", "")).toBe(false);
  });
});

describe("conflictCause", () => {
  it("calls out stale_base so re-run can be foregrounded over retry", () => {
    expect(conflictCause([{ category: "stale_base" }])).toBe("stale_base");
    expect(conflictCause([{ category: "workspace_copy" }, { category: "stale_base" }])).toBe(
      "stale_base",
    );
  });

  it("defaults to other when nothing names a stale base", () => {
    expect(conflictCause([{ category: "workspace_copy" }])).toBe("other");
    expect(conflictCause([])).toBe("other");
  });
});

describe("appliedOutcomeLabel", () => {
  it("never calls a placeholder executor's output SHIPPED", () => {
    expect(appliedOutcomeLabel("internal", undefined)).toBe("Placeholder change (demo)");
  });

  it("never calls a demo-model change set SHIPPED, even on a real executor", () => {
    expect(appliedOutcomeLabel("container", "demo")).toBe("Placeholder change (demo)");
  });

  it("calls a real executor with a real model SHIPPED", () => {
    expect(appliedOutcomeLabel("container", "anthropic/claude-sonnet-4"))
      .toBe("SHIPPED");
  });
});

describe("buildHeaderMeta", () => {
  it("says placeholder code only for the internal executor", () => {
    expect(buildHeaderMeta("internal")).toBe("internal executor · placeholder code");
  });

  it("says nothing about placeholder code for a real executor", () => {
    expect(buildHeaderMeta("container")).toBe("container executor");
    expect(buildHeaderMeta("hermes")).toBe("hermes executor");
  });
});
