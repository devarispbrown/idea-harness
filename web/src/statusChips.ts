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
