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

export interface RunButtonState {
  // false when there's nothing to run, or something is already running --
  // the button should not render at all, not render disabled.
  visible: boolean;
  label: string;
  disabled: boolean;
}

// runButtonState decides the Build page's primary "Run next task" action.
// It's hidden (not just disabled) once something is running, because a
// disabled "Run TASK-2" button next to a running card reads as a second,
// separate run available to start.
export function runButtonState(
  nextTask: NextTask | null | undefined,
  runBlockedReason: string | undefined,
  isRunning: boolean,
): RunButtonState {
  if (isRunning || !nextTask) {
    return { visible: false, label: "", disabled: false };
  }
  return {
    visible: true,
    label: `Run ${nextTask.human_id}: ${nextTask.title}`,
    disabled: !!runBlockedReason,
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
    if (line.startsWith("+++") || line.startsWith("---")) continue;
    if (line.startsWith("+") || line.startsWith("-")) {
      sawChangedLine = true;
    } else if (line.startsWith(" ")) {
      sawContext = true;
    }
  }
  return sawHunk && sawChangedLine && !sawContext;
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
export function appliedOutcomeLabel(executorKind: string, model: string | undefined): string {
  if (executorKind === "internal" || model === "demo") return "Placeholder change (demo)";
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
