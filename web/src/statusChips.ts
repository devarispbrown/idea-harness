// Pure status-label/class helpers, extracted from App.tsx so they can be unit
// tested without a DOM. Kept dependency-free (string in → string out).

// dispositionLabel turns a snake_case disposition into a readable label for the
// finding chip (e.g. "accepted_risk" → "accepted risk").
export function dispositionLabel(d: string): string {
  return d.replace(/_/g, " ");
}

// dispositionClass maps a disposition to a chip color class.
export function dispositionClass(d: string): string {
  switch (d) {
    case "mitigate":
      return "mitigate";
    case "accepted_risk":
      return "fact";
    case "deferred":
      return "medium";
    default:
      return "low";
  }
}

// changeStatusClass maps a ChangeSet status to a chip color class.
export function changeStatusClass(status: string): string {
  switch (status) {
    case "draft":
      return "change-draft";
    case "checks_running":
      return "change-checks-running";
    case "review_required":
      return "change-review-required";
    case "rejected":
      return "change-rejected";
    case "changes_requested":
      return "change-changes-requested";
    case "superseded":
      return "change-superseded";
    case "approved":
      return "change-approved";
    case "applying":
      return "change-applying";
    case "applied":
      return "change-applied";
    case "reverted":
      return "change-reverted";
    case "conflicted":
      return "change-conflicted";
    default:
      return "";
  }
}

// ChangeSetChip is a change set's status pill: the status in words, the pill's
// colour state, and an optional secondary marker beside it.
export interface ChangeSetChip {
  label: string;
  state: string;
  marker?: string;
}

const CHANGE_SET_PILL: Record<string, { label: string; state: string }> = {
  draft: { label: "draft", state: "inactive" },
  checks_running: { label: "checking", state: "running" },
  review_required: { label: "review required", state: "needs-you" },
  rejected: { label: "rejected", state: "failing" },
  changes_requested: { label: "changes requested", state: "needs-you" },
  superseded: { label: "superseded", state: "inactive" },
  approved: { label: "approved", state: "verified" },
  applying: { label: "applying", state: "running" },
  applied: { label: "applied", state: "verified" },
  reverted: { label: "reverted", state: "inactive" },
  conflicted: { label: "conflicted", state: "needs-you" },
};

export interface ChangeSetChipInput {
  status: string;
  // The kind that actually produced this change set, and the model -- not
  // the page's currently-selected executor (a dropdown change must never
  // relabel history). Absent on a change set from an older daemon.
  executor?: string;
  model?: string;
  // The backend's own "touched zero files" signal (ChangeSet.no_op).
  no_op?: boolean;
}

// isPlaceholderChange is true for a change set the internal placeholder
// executor or the demo model produced: nothing real was written.
export function isPlaceholderChange(cs: Pick<ChangeSetChipInput, "executor" | "model">): boolean {
  return cs.executor === "internal" || cs.model === "demo";
}

// changeSetChip is the pill for one change set. The label is always the
// status -- an applied change set reads "applied" -- and what is ALSO true
// about it rides along as a marker: "demo" for placeholder output, "no file
// changes" for a no-op, "no changes" when an agent finding says the diff is
// empty (noFilesChanged, from hasNoFilesChanged).
//
// The pill used to replace the status with the caveat: an applied demo change
// set read "PLACEHOLDER CHANGE (DEMO)" and never said it had been applied, and
// a real one read "SHIPPED", a word for a deploy nothing here performs.
export function changeSetChip(cs: ChangeSetChipInput, noFilesChanged = false): ChangeSetChip {
  const base = CHANGE_SET_PILL[cs.status] ?? { label: cs.status.replace(/_/g, " "), state: "inactive" };
  let marker: string | undefined;
  if (cs.status === "applied" && cs.no_op) marker = "no file changes";
  else if (cs.status === "applied" && noFilesChanged) marker = "no changes";
  else if (isPlaceholderChange(cs)) marker = "demo";
  return marker ? { ...base, marker } : { ...base };
}
