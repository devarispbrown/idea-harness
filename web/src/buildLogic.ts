// Pure label/state-decision helpers for the Build phase, extracted so they can
// be unit tested without a DOM or a fetch mock — the same reason statusChips.ts
// and providerStatus.ts exist. BuildPage, RunningCard, and FailedBanner import
// from here rather than deciding these things inline in JSX.

export interface NextTask {
  id: string;
  human_id: string;
  title: string;
}

export interface OperationLike {
  id: string;
  type: string;
  status: string;
  created_at: string;
  error?: string;
}

// isOperationInFlight is the one place "still running" is defined for an
// Operation. RunningCard, BuildPage, and FailedBanner all need this same
// question answered (is there a reason to keep polling / stay busy?), and
// three copies of the pending/running check is how they'd quietly drift.
export function isOperationInFlight(status: string): boolean {
  return status === "running" || status === "pending";
}

// errMessage unwraps an error thrown by api.ts into display text. api.ts
// throws `Error` whose `.message` is already the server's redacted `error`
// string, so this is just the one place that does `e instanceof Error`
// instead of every call site doing `String(e)` and leaking the "Error: "
// prefix into the banner.
export function errMessage(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

// PartialWork is what a stopped run left behind (C3, guidance's
// `partial_work`): the next execute of the SAME task carries these files in
// automatically, so the Build page's Run button and RunningCard's post-Stop
// state both read it to say so rather than silently resuming.
export interface PartialWork {
  task_id: string;
  task_human_id?: string;
  files: number;
  // Set instead of a positive `files` count when the stopped run's files
  // were not kept (e.g. they looked like they held a secret).
  note?: string;
}

export interface RunButtonState {
  // false when there's nothing to run, or something is already running --
  // the button should not render at all, not render disabled.
  visible: boolean;
  label: string;
  disabled: boolean;
  // True when this button will resume a stopped run rather than start a
  // fresh one -- BuildPage uses it to show "Start over" alongside it.
  startOver: boolean;
}

// runButtonState decides the Build page's primary "Run next task" action.
// It's hidden (not just disabled) once something is running, because a
// disabled "Run TASK-2" button next to a running card reads as a second,
// separate run available to start.
//
// C3: when a stopped run left files for exactly this next task, the button
// reads "Continue TASK-n (N files from the stopped run)" instead of "Run
// TASK-n: <title>" -- the plan's own words for it -- and `startOver` tells
// the caller to render "Start over" beside it. `partialWork` for a
// DIFFERENT task is not this task's to resume (the daemon discards it on a
// task change anyway), so it's ignored rather than trusted blindly.
export function runButtonState(
  nextTask: NextTask | null | undefined,
  runBlockedReason: string | undefined,
  isRunning: boolean,
  partialWork?: PartialWork | null,
): RunButtonState {
  if (isRunning || !nextTask) {
    return { visible: false, label: "", disabled: false, startOver: false };
  }
  const resuming = !!partialWork && partialWork.task_id === nextTask.id && partialWork.files > 0;
  const label = resuming
    ? `Continue ${nextTask.human_id} (${partialWork!.files} file${partialWork!.files === 1 ? "" : "s"} from the stopped run)`
    : `Run ${nextTask.human_id}: ${nextTask.title}`;
  return {
    visible: true,
    label,
    disabled: !!runBlockedReason,
    startOver: resuming,
  };
}

// latestOperationOfType picks the most recently created operation of a given
// type, newest first. Used to find "the latest execute" without assuming the
// operations list itself arrives newest-first (it doesn't promise to).
export function latestOperationOfType<T extends { type: string; created_at: string }>(
  ops: T[],
  type: string,
): T | undefined {
  return ops
    .filter((o) => o.type === type)
    .sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at))[0];
}

// buildFailure finds the execute_initiative operation that should surface as
// "Build failed" on the Build page: the latest one, only when nothing is
// currently running (a running retry must not be shadowed by the failure it's
// retrying) and only when it actually failed. Today a failed execution leaves
// no trace in the UI at all (B2) -- this is the function that decides when
// the trace should appear.
export function buildFailure<T extends OperationLike>(ops: T[]): T | null {
  if (ops.some((o) => isOperationInFlight(o.status))) return null;
  const latest = latestOperationOfType(ops, "execute_initiative");
  return latest && latest.status === "failed" ? latest : null;
}

// shouldResetRetryBusy is FailedBanner's fix for F3: a retry button that says
// "Retrying..." forever once the retried run fails again. Busy must clear
// either when the specific operation we retried has settled (succeeded,
// failed, or was cancelled), or -- belt and suspenders, for the case a poll
// tick is missed and the operation is never directly observed -- when the
// failure text itself changes out from under the banner.
export function shouldResetRetryBusy(
  pendingOpStatus: string | undefined,
  prevReason: string | undefined,
  reason: string | undefined,
): boolean {
  if (pendingOpStatus !== undefined && !isOperationInFlight(pendingOpStatus)) return true;
  if (prevReason !== undefined && prevReason !== reason) return true;
  return false;
}

// runningTitle names the running card. For an execute run whose current task
// is known, it names the task (B3: "Running TASK-2: <title>"); every other
// operation type keeps its generic label.
export function runningTitle(
  opType: string,
  opLabels: Record<string, string>,
  task: NextTask | null | undefined,
): string {
  if (opType === "execute_initiative" && task) {
    return `Running ${task.human_id}: ${task.title}`;
  }
  return opLabels[opType] ?? opType;
}

export interface OperationTaskFields {
  task_id?: string;
  task_human_id?: string;
  task_title?: string;
}

// resolveRunningTask picks which task RunningCard names for a running
// execute_initiative operation. The daemon does not stamp task fields on
// operations today, so this resolves to guidance's next_task -- which is the
// running task, because a task stays ready until its change set is applied.
// Operation task fields, if the daemon ever adds them, take precedence.
export function resolveRunningTask(
  op: OperationTaskFields,
  guidanceNextTask: NextTask | null | undefined,
): NextTask | null {
  if (op.task_id && op.task_human_id) {
    return { id: op.task_id, human_id: op.task_human_id, title: op.task_title ?? "" };
  }
  return guidanceNextTask ?? null;
}

const DIFF_CHANGE_LABEL: Record<string, string> = {
  add: "added",
  modify: "modified",
  delete: "deleted",
};

// diffChangeTypeLabel turns a wire change_type into the badge word.
export function diffChangeTypeLabel(changeType: string): string {
  return DIFF_CHANGE_LABEL[changeType] ?? changeType;
}

// isWholeFileRewrite flags a modified file whose unified patch preserves no
// context lines at all -- every line is an addition or a removal. That's the
// shape of "delete the whole file, add a whole new file back" rather than an
// incremental edit, and it's exactly the shape that hid a deleted route
// behind an ADDED-looking whole-file dump (walkthrough finding on
// BuildPage.tsx:204-228): rendering the final content alone can't distinguish
// "this route never existed" from "this route was just removed". A patch
// with zero context lines is the caller's cue to show that warning.
//
// H1: the backend's patch is a plain unified diff with NO ---/+++ file
// headers -- just `@@ -oldStart,oldCount +newStart,newCount @@` hunk headers
// followed by lines each prefixed with exactly one of ' ' (context), '-', or
// '+'. This used to also skip lines starting with "+++"/"---" defensively;
// that defense matched invented fixtures the backend never actually
// produced, so it's dropped rather than carried forward as dead code.
export function isWholeFileRewrite(changeType: string, patch: string): boolean {
  if (changeType !== "modify" || !patch) return false;
  let sawHunk = false;
  let sawContext = false;
  let sawChangedLine = false;
  for (const line of patch.split("\n")) {
    if (line.startsWith("@@")) {
      sawHunk = true;
      continue;
    }
    if (!sawHunk) continue;
    if (line.startsWith("+") || line.startsWith("-")) {
      sawChangedLine = true;
    } else if (line.startsWith(" ")) {
      sawContext = true;
    }
  }
  return sawHunk && sawChangedLine && !sawContext;
}

export type PatchLineKind = "hunk" | "add" | "remove" | "context";

export interface PatchLine {
  kind: PatchLineKind;
  // The line's text with its one-character prefix stripped (for "add",
  // "remove", "context"); the hunk header is kept whole since it has no
  // per-character prefix to strip.
  text: string;
}

// patchLines (H1) turns the backend's unified-diff patch into a render-ready
// list, so BuildPage can style each line by what it is (+/- coloring) instead
// of dumping the raw text into a flat <pre> where an added and a removed line
// look identical. Pure and independent of the DOM so the parsing itself is
// unit-testable.
export function patchLines(patch: string): PatchLine[] {
  if (!patch) return [];
  return patch.split("\n").map((line) => {
    if (line.startsWith("@@")) return { kind: "hunk", text: line };
    if (line.startsWith("+")) return { kind: "add", text: line.slice(1) };
    if (line.startsWith("-")) return { kind: "remove", text: line.slice(1) };
    if (line.startsWith(" ")) return { kind: "context", text: line.slice(1) };
    // Malformed/blank line -- render as context rather than dropping it.
    return { kind: "context", text: line };
  });
}

const FINDING_CATEGORY_LABEL: Record<string, string> = {
  workspace_copy: "workspace copy",
  agent_output: "agent output",
  stale_base: "stale base",
  // C3: the finding a resumed run adds, listing what it carried in from the
  // run that was stopped before it produced a change set.
  carried_over: "carried over",
};

// findingCategoryLabel (H6) is the display label for a change-set finding's
// category. BuildPage used to hard-code a workspace_copy/agent_output-only
// ternary and only rendered it while the change set was "conflicted" --
// every other category (stale_base, and whatever else a reviewer emits) and
// every other status showed nothing, so an agent that hit a 401 and changed
// nothing rendered as a bare "(no diff)" next to an Approve button with no
// explanation at all. Falls back to the raw category, not a guess, for
// anything not named above.
export function findingCategoryLabel(category: string): string {
  return FINDING_CATEGORY_LABEL[category] ?? category;
}

// hasNoFilesChanged is true when the change set carries an agent_output
// finding -- H6's contract for that category is specifically "the agent
// changed nothing, and why" (e.g. it hit a 401 and produced no diff at all).
// Two things key off this: the pill must never call that outcome SHIPPED
// (appliedOutcomeLabel has no way to know this on its own, since executor
// and model both look perfectly real), and the diff area should say plainly
// that nothing changed instead of a bare, unexplained "(no diff)".
export function hasNoFilesChanged(findings: { category: string }[]): boolean {
  return findings.some((f) => f.category === "agent_output");
}

// noDiffMessage is the diff area's placeholder when there is nothing to
// render. An agent_output finding means the agent produced no diff for a
// known reason (already shown alongside it via findingCategoryLabel above),
// so the placeholder says that plainly instead of the generic "(no diff)",
// which read as if the UI just failed to load something.
export function noDiffMessage(noFilesChanged: boolean): string {
  return noFilesChanged ? "The agent changed no files." : "(no diff)";
}

// approveErrorMessage turns the engine's refusal from POST .../review into a
// sentence for the reviewer. The engine's text is written for the CLI --
// "workflow: approval blocked: system deny review_open_blocking (open high
// finding); `idea reviews` lists them, ..." -- and an earlier version of this
// matched a shape the engine never sent, so the raw text reached the page.
// The finding itself is listed on the card; this points at it.
const APPROVAL_BLOCKED = /approval blocked:.*?\(open (\w+) finding(?:, ([\w-]+))?\)/i;

export function approveErrorMessage(raw: string): string {
  const m = APPROVAL_BLOCKED.exec(raw);
  if (!m) return raw.replace(/^workflow:\s*/i, "");
  const severity = m[1].toUpperCase();
  if (m[2]) {
    return `Can't approve: an open ${severity} finding (${m[2]}) is a system block that cannot be accepted. Reject this change set and run the task again.`;
  }
  return `Can't approve: an open ${severity} finding from the reviewer is listed on this change set. Resolve it, or request changes.`;
}

export type ConflictCause = "stale_base" | "other";

// conflictCause reads a conflicted change set's own findings to decide which
// action to foreground. A stale_base finding means retrying would fail
// identically (the workspace moved since the agent ran against it), so
// "Reject and re-run" must outrank "Retry" -- retrying is not a second option
// here, it's a trap.
export function conflictCause(findings: { category: string }[]): ConflictCause {
  return findings.some((f) => f.category === "stale_base") ? "stale_base" : "other";
}

// appliedOutcomeLabel is G3: an applied change set from a placeholder
// executor, or built under the demo model, never earned the word "SHIPPED" --
// nothing real happened. Labeling it "Placeholder change (demo)" instead is
// what stops a demo run from reading as a real deploy.
//
// `executor` must be the CHANGE SET's own executor (`cs.executor`, the kind
// that actually produced it -- a backend contract addition), not the page's
// currently-selected executor. The page-level value used to be passed here,
// which relabeled every already-applied change set's history the moment
// someone switched the dropdown, and read "internal" for everything before
// the page's own executor fetch resolved. A change set predating that field
// carries `executor: undefined`; the function falls back to the model check
// alone for it rather than assuming "internal" (which would mislabel a real
// SHIPPED change made under a real executor and a real model).
export function appliedOutcomeLabel(executor: string | undefined, model: string | undefined): string {
  if (executor === "internal") return "Placeholder change (demo)";
  if (model === "demo") return "Placeholder change (demo)";
  return "SHIPPED";
}

// buildHeaderMeta is B4: "placeholder code" only belongs next to the executor
// name when that executor IS the placeholder (internal). Every other executor
// kind does something real.
export function buildHeaderMeta(executorKind: string): string {
  return executorKind === "internal"
    ? `${executorKind} executor · placeholder code`
    : `${executorKind} executor`;
}

// sortFindingsForCard puts what explains the card's state first: why it
// conflicted (stale_base), then blocking severities, then the rest. The
// stale-base finding was the last of nine rows on the card it explained.
const SEVERITY_RANK: Record<string, number> = { critical: 0, high: 1, medium: 2, low: 3, observation: 4 };
export function sortFindingsForCard<T extends { category: string; severity: string }>(findings: T[]): T[] {
  return [...findings].sort((a, b) => {
    const sa = a.category === "stale_base" ? -1 : (SEVERITY_RANK[a.severity] ?? 5);
    const sb = b.category === "stale_base" ? -1 : (SEVERITY_RANK[b.severity] ?? 5);
    return sa - sb;
  });
}

// stoppedMessage is RunningCard's post-Stop line (C3). It reads straight off
// guidance's `partial_work` rather than guessing from the stopped operation
// alone -- the daemon is the one that knows whether the credential sweep
// actually kept anything. Three cases: files were kept (say how many, and
// that running again continues from them), nothing was kept but the daemon
// says why (the credential-sweep note), or there's nothing to report at all
// (no partial_work -- the run stopped before the agent wrote anything).
export function stoppedMessage(partial: PartialWork | null | undefined): string {
  if (!partial) return "Stopped.";
  if (partial.files > 0) {
    return `Stopped. The agent had written ${partial.files} file${partial.files === 1 ? "" : "s"}; running the task again continues from them.`;
  }
  if (partial.note) {
    return `Stopped. ${partial.note[0].toUpperCase()}${partial.note.slice(1)}.`;
  }
  return "Stopped.";
}
