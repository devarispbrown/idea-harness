import { describe, expect, it } from "vitest";
import {
  advisoryBacklogLabel,
  appliedOutcomeLabel,
  approveErrorMessage,
  sortFindingsForCard,
  buildFailure,
  buildHeaderMeta,
  canOverrideSecret,
  conflictCause,
  diffChangeTypeLabel,
  errMessage,
  executeRequestBody,
  findingCategoryLabel,
  gitPrErrorMessage,
  hasNoFilesChanged,
  isOperationInFlight,
  isWholeFileRewrite,
  latestOperationOfType,
  noDiffMessage,
  notASecretConfirmCopy,
  partitionAdvisory,
  patchLines,
  prOverallLabel,
  prOverallState,
  resolveRunningTask,
  revertRefusalMessage,
  runButtonState,
  runningTitle,
  shouldResetRetryBusy,
  stageOrTitle,
  stoppedMessage,
  stripWorkflowPrefix,
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

  // C3: resuming a stopped run.
  it("reads Continue with the file count when the stopped run left files for this task", () => {
    const s = runButtonState(task, "", false, { task_id: "t1", files: 3 });
    expect(s.label).toBe("Continue TASK-2 (3 files from the stopped run)");
    expect(s.startOver).toBe(true);
  });

  it("singularizes one carried file", () => {
    const s = runButtonState(task, "", false, { task_id: "t1", files: 1 });
    expect(s.label).toBe("Continue TASK-2 (1 file from the stopped run)");
  });

  it("ignores partial work for a different task", () => {
    const s = runButtonState(task, "", false, { task_id: "some-other-task", files: 3 });
    expect(s.label).toBe("Run TASK-2: Add the retry button");
    expect(s.startOver).toBe(false);
  });

  // Mutation guard: files must be > 0 to resume -- a partial_work entry with
  // 0 files (everything was dropped by the credential sweep) has nothing to
  // continue from, so the button must stay a plain "Run", not "Continue (0
  // files from the stopped run)".
  it("does not treat a zero-file partial as something to resume", () => {
    const s = runButtonState(task, "", false, { task_id: "t1", files: 0, note: "dropped" });
    expect(s.label).toBe("Run TASK-2: Add the retry button");
    expect(s.startOver).toBe(false);
  });
});

describe("stoppedMessage", () => {
  it("names the file count and says running again continues from them", () => {
    expect(stoppedMessage({ task_id: "t1", files: 2 })).toBe(
      "Stopped. The agent had written 2 files; running the task again continues from them.",
    );
  });

  it("singularizes one file", () => {
    expect(stoppedMessage({ task_id: "t1", files: 1 })).toBe(
      "Stopped. The agent had written 1 file; running the task again continues from it.",
    );
  });

  it("shows the daemon's note when nothing was kept", () => {
    expect(
      stoppedMessage({
        task_id: "t1",
        files: 0,
        note: "the files it wrote looked like they contained a secret, so they were not kept",
      }),
    ).toBe(
      "Stopped. The files it wrote looked like they contained a secret, so they were not kept.",
    );
  });

  it("is a bare Stopped with no partial work at all", () => {
    expect(stoppedMessage(null)).toBe("Stopped.");
    expect(stoppedMessage(undefined)).toBe("Stopped.");
  });

  // Mutation guard: a zero-file partial with no note must not read as if
  // files were written.
  it("does not claim files were written when there are none and no note", () => {
    expect(stoppedMessage({ task_id: "t1", files: 0 })).toBe("Stopped.");
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

describe("resolveRunningTask", () => {
  it("prefers the operation's own task fields when it carries them", () => {
    const op = { task_id: "t2", task_human_id: "TASK-2", task_title: "Add the retry button" };
    const guidance = { id: "t9", human_id: "TASK-9", title: "A stale next task" };
    expect(resolveRunningTask(op, guidance)).toEqual({
      id: "t2",
      human_id: "TASK-2",
      title: "Add the retry button",
    });
  });

  it("falls back to guidance's next_task when the operation has no task fields", () => {
    const guidance = { id: "t9", human_id: "TASK-9", title: "A stale next task" };
    expect(resolveRunningTask({}, guidance)).toEqual(guidance);
  });

  it("is null when neither the operation nor guidance names a task", () => {
    expect(resolveRunningTask({}, null)).toBeNull();
    expect(resolveRunningTask({}, undefined)).toBeNull();
  });

  it("requires both task_id and task_human_id before trusting the operation", () => {
    const guidance = { id: "t9", human_id: "TASK-9", title: "fallback" };
    expect(resolveRunningTask({ task_id: "t2" }, guidance)).toEqual(guidance);
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

// H1: the backend's patch is a plain unified diff with NO ---/+++ file
// headers -- just `@@ -oldStart,oldCount +newStart,newCount @@` hunks
// followed by ' '/'-'/'+' prefixed lines (an added file is one hunk
// `@@ -0,0 +1,N @@` all '+'; a deleted file is one hunk `@@ -1,N +0,0 @@`
// all '-'). These fixtures use exactly that shape -- the tests they replaced
// fed invented git patches with ---/+++ headers the backend never produces.
describe("isWholeFileRewrite", () => {
  it("flags a modify patch with no preserved context lines", () => {
    const patch = [
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
      "@@ -10,3 +10,3 @@",
      " unchanged before",
      "-removed route",
      "+added route",
      " unchanged after",
    ].join("\n");
    expect(isWholeFileRewrite("modify", patch)).toBe(false);
  });

  it("never flags an add or a delete, only a modify", () => {
    const addPatch = "@@ -0,0 +1,1 @@\n+a";
    const deletePatch = "@@ -1,1 +0,0 @@\n-a";
    expect(isWholeFileRewrite("add", addPatch)).toBe(false);
    expect(isWholeFileRewrite("delete", deletePatch)).toBe(false);
  });

  it("is false with no patch text", () => {
    expect(isWholeFileRewrite("modify", "")).toBe(false);
  });
});

describe("patchLines", () => {
  it("parses a hunk header and context/removed/added lines from the exact backend format", () => {
    const patch = ["@@ -1,3 +1,3 @@", " a", "-b", "+B", " c"].join("\n");
    expect(patchLines(patch)).toEqual([
      { kind: "hunk", text: "@@ -1,3 +1,3 @@" },
      { kind: "context", text: "a" },
      { kind: "remove", text: "b" },
      { kind: "add", text: "B" },
      { kind: "context", text: "c" },
    ]);
  });

  it("parses an added file's single all-plus hunk", () => {
    const patch = "@@ -0,0 +1,2 @@\n+line one\n+line two";
    expect(patchLines(patch)).toEqual([
      { kind: "hunk", text: "@@ -0,0 +1,2 @@" },
      { kind: "add", text: "line one" },
      { kind: "add", text: "line two" },
    ]);
  });

  it("parses a deleted file's single all-minus hunk", () => {
    const patch = "@@ -1,2 +0,0 @@\n-line one\n-line two";
    expect(patchLines(patch)).toEqual([
      { kind: "hunk", text: "@@ -1,2 +0,0 @@" },
      { kind: "remove", text: "line one" },
      { kind: "remove", text: "line two" },
    ]);
  });

  it("is empty for an empty patch (the patch_note/content fallback case)", () => {
    expect(patchLines("")).toEqual([]);
  });
});

describe("hasNoFilesChanged", () => {
  it("is true when an agent_output finding is present", () => {
    expect(hasNoFilesChanged([{ category: "agent_output" }])).toBe(true);
  });

  it("is false without one — a stale_base or workspace_copy finding doesn't mean an empty diff", () => {
    expect(hasNoFilesChanged([{ category: "stale_base" }, { category: "workspace_copy" }])).toBe(false);
    expect(hasNoFilesChanged([])).toBe(false);
  });
});

describe("noDiffMessage", () => {
  it("explains an empty diff instead of a bare, unexplained placeholder", () => {
    expect(noDiffMessage(true)).toBe("The agent changed no files.");
  });

  it("keeps the generic placeholder when nothing explains the empty diff", () => {
    expect(noDiffMessage(false)).toBe("(no diff)");
  });
});

// Fixtures are the engine's exact format strings (internal/workflow/execute.go,
// reviewChangeSet): the first version of this was tested against a shape the
// engine never produced, and passed while the page showed the raw text.
describe("approveErrorMessage", () => {
  it("rewrites the engine's refusal for a reviewer finding", () => {
    const raw =
      "workflow: approval blocked: system deny review_open_blocking (open high finding); " +
      "`idea reviews` lists them, and `idea review <CHG-n> approve --accept-findings` accepts the change set findings and all";
    const msg = approveErrorMessage(raw);
    expect(msg).toContain("Can't approve");
    expect(msg).toContain("HIGH");
    expect(msg).not.toContain("workflow:");
    expect(msg).not.toContain("idea review");
  });

  it("says a system deny cannot be accepted", () => {
    const raw =
      "workflow: approval blocked: system deny review_open_blocking (open critical finding, secret_detected); " +
      "a system deny is not something a human can accept — `idea reviews` shows it";
    const msg = approveErrorMessage(raw);
    expect(msg).toContain("CRITICAL");
    expect(msg).toContain("secret_detected");
    expect(msg).toContain("cannot be accepted");
  });

  it("drops the internal prefix from any other error", () => {
    expect(approveErrorMessage("workflow: change set is not in review")).toBe("change set is not in review");
    expect(approveErrorMessage("network timeout")).toBe("network timeout");
  });
});

describe("findingCategoryLabel", () => {
  it("labels the known change-set finding categories", () => {
    expect(findingCategoryLabel("workspace_copy")).toBe("workspace copy");
    expect(findingCategoryLabel("agent_output")).toBe("agent output");
    expect(findingCategoryLabel("stale_base")).toBe("stale base");
    expect(findingCategoryLabel("carried_over")).toBe("carried over");
  });

  it("passes through an unrecognized category rather than hiding it", () => {
    expect(findingCategoryLabel("some_new_category")).toBe("some_new_category");
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

  // Low: this must be the CHANGE SET's own executor, not the page's
  // currently-selected one -- an older change set predating the `executor`
  // field carries `undefined` and falls back to the model check alone,
  // rather than assuming "internal" the way the page-level default used to.
  it("falls back to the model check for a change set with no recorded executor", () => {
    expect(appliedOutcomeLabel(undefined, "demo")).toBe("Placeholder change (demo)");
    expect(appliedOutcomeLabel(undefined, "anthropic/claude-sonnet-4")).toBe("SHIPPED");
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

describe("sortFindingsForCard", () => {
  it("leads with why the change set conflicted, then by severity", () => {
    const out = sortFindingsForCard([
      { category: "review", severity: "observation" },
      { category: "review", severity: "high" },
      { category: "stale_base", severity: "high" },
      { category: "review", severity: "critical" },
    ]);
    expect(out.map((f) => `${f.category}/${f.severity}`)).toEqual([
      "stale_base/high",
      "review/critical",
      "review/high",
      "review/observation",
    ]);
  });
});

describe("stripWorkflowPrefix", () => {
  it("drops a leading 'workflow: ' prefix", () => {
    expect(stripWorkflowPrefix("workflow: revert refused: web/src/a.ts changed since apply")).toBe(
      "revert refused: web/src/a.ts changed since apply",
    );
  });

  it("leaves text with no such prefix untouched", () => {
    expect(stripWorkflowPrefix("revert refused: web/src/a.ts changed since apply")).toBe(
      "revert refused: web/src/a.ts changed since apply",
    );
  });
});

describe("revertRefusalMessage (task 4)", () => {
  it("passes the backend's file-naming, advice-carrying message through readably", () => {
    const raw =
      "workflow: revert CHG-1 refused: web/src/App.tsx, web/src/api.ts changed since apply; " +
      "re-approve and re-apply a new change set instead of reverting this one";
    expect(revertRefusalMessage(raw)).toBe(
      "revert CHG-1 refused: web/src/App.tsx, web/src/api.ts changed since apply; " +
        "re-approve and re-apply a new change set instead of reverting this one",
    );
  });

  it("still names the files even with no workflow prefix", () => {
    const raw = "revert refused: web/src/App.tsx changed since apply";
    expect(revertRefusalMessage(raw)).toBe(raw);
  });
});

describe("canOverrideSecret (task 1)", () => {
  it("is true only for an overridable secret_sweep finding", () => {
    expect(canOverrideSecret({ category: "secret_sweep", secret_overridable: true })).toBe(true);
  });

  it("is false for a secret_sweep finding drawn from configured secrets", () => {
    expect(canOverrideSecret({ category: "secret_sweep", secret_overridable: false })).toBe(false);
  });

  it("is false for any other category, whatever secret_overridable says", () => {
    expect(canOverrideSecret({ category: "review", secret_overridable: true })).toBe(false);
  });

  it("is false when secret_overridable is absent", () => {
    expect(canOverrideSecret({ category: "secret_sweep" })).toBe(false);
  });
});

describe("notASecretConfirmCopy (task 1)", () => {
  it("names the file and mentions Settings as where to revoke it", () => {
    const copy = notASecretConfirmCopy("src/config.ts:12");
    expect(copy).toContain("src/config.ts:12");
    expect(copy).toContain("Settings");
    expect(copy).toContain("from now on");
  });

  it("still reads sensibly with no path", () => {
    const copy = notASecretConfirmCopy(undefined);
    expect(copy).toContain("Settings");
  });
});

describe("partitionAdvisory (task 2)", () => {
  it("splits advisory findings out from blocking ones", () => {
    const blocker = { advisory: false, id: "a" };
    const backlog1 = { advisory: true, id: "b" };
    const backlog2 = { advisory: true, id: "c" };
    const { blocking, advisory } = partitionAdvisory([blocker, backlog1, backlog2]);
    expect(blocking).toEqual([blocker]);
    expect(advisory).toEqual([backlog1, backlog2]);
  });

  it("treats an absent advisory flag as blocking", () => {
    const f: { id: string; advisory?: boolean } = { id: "a" };
    expect(partitionAdvisory([f]).blocking).toEqual([f]);
  });
});

describe("advisoryBacklogLabel (task 2)", () => {
  it("names the count and says it never blocks", () => {
    expect(advisoryBacklogLabel(3)).toBe("Backlog (3) — noted, not blocking");
  });

  it("still reads correctly at zero", () => {
    expect(advisoryBacklogLabel(0)).toBe("Backlog (0) — noted, not blocking");
  });
});

describe("prOverallLabel / prOverallState (task 3)", () => {
  it("labels every PRChecksResult.overall value", () => {
    expect(prOverallLabel("success")).toBe("All checks passed");
    expect(prOverallLabel("failure")).toBe("Checks failed");
    expect(prOverallLabel("pending")).toBe("Checks running");
    expect(prOverallLabel("none")).toBe("No checks reported");
  });

  it("falls back to the raw value for anything unrecognized", () => {
    expect(prOverallLabel("weird")).toBe("weird");
  });

  it("maps overall onto this app's pill states", () => {
    expect(prOverallState("success")).toBe("verified");
    expect(prOverallState("failure")).toBe("failing");
    expect(prOverallState("pending")).toBe("running");
    expect(prOverallState("none")).toBe("inactive");
  });
});

describe("gitPrErrorMessage (task 3)", () => {
  it("explains a missing gh CLI", () => {
    expect(gitPrErrorMessage("exec: \"gh\": executable file not found in $PATH")).toContain(
      "GitHub CLI (gh) isn't installed",
    );
  });

  it("explains a missing dedicated branch", () => {
    expect(gitPrErrorMessage("workflow: initiative has no dedicated branch yet")).toContain(
      "git.branch_per_initiative",
    );
  });

  it("explains no pull request yet, for pr-checks", () => {
    expect(gitPrErrorMessage("no pull request exists yet for this initiative")).toContain(
      "Create one first",
    );
  });

  it("passes an unrecognized 409 through, minus the workflow prefix", () => {
    expect(gitPrErrorMessage("workflow: something else entirely")).toBe("something else entirely");
  });
});

describe("executeRequestBody (task 5)", () => {
  it("is empty when neither fresh nor a note is given", () => {
    expect(executeRequestBody(false, "")).toEqual({});
  });

  it("trims and includes a note", () => {
    expect(executeRequestBody(false, "  please use the mock client  ")).toEqual({
      note: "please use the mock client",
    });
  });

  it("omits note entirely for a blank/whitespace-only note", () => {
    expect(executeRequestBody(false, "   ")).toEqual({});
  });

  it("includes fresh only when discarding partial work, alongside a note", () => {
    expect(executeRequestBody(true, "retry without the cache")).toEqual({
      fresh: true,
      note: "retry without the cache",
    });
  });
});

describe("stageOrTitle (task 8)", () => {
  const labels = { execute_initiative: "Executing the plan" };

  it("prefers the operation's own stage when present", () => {
    expect(stageOrTitle("Reviewing the plan, cycle 2", "execute_initiative", labels, null)).toBe(
      "Reviewing the plan, cycle 2",
    );
  });

  it("falls back to runningTitle when stage is absent", () => {
    expect(stageOrTitle(undefined, "execute_initiative", labels, { id: "t1", human_id: "TASK-2", title: "Add rate limiting" })).toBe(
      "Running TASK-2: Add rate limiting",
    );
  });

  it("falls back when stage is blank", () => {
    expect(stageOrTitle("   ", "execute_initiative", labels, null)).toBe("Executing the plan");
  });
});
