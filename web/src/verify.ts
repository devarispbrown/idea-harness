// Shared, pure Verify-phase decision logic. App.tsx's sidebar rail and
// VerifyPage's own header used to compute the "requirements verified" count
// independently -- App.tsx hard-coded it to 0 ("wired in the Verify page,
// step 9") and never actually wired it, so the sidebar read "0 requirements
// verified" while the page itself correctly showed "1 of 1" (K). Both now
// call this function on the same requirements list.

export interface RequirementStatus {
  status: string;
}

// Mirrors VerifyPage's own definition of "verified": active (not removed)
// requirements whose status is "verified". Waived requirements are out of
// scope for the count, same as the page's denominator.
export function verifiedRequirementCount(reqs: RequirementStatus[]): number {
  return reqs.filter((r) => r.status !== "removed" && r.status === "verified").length;
}

export interface ChangeSetStatus {
  status: string;
}

// Verify's Pass/Fail only mean something once a change set has actually
// landed in the workspace -- otherwise "pass" would be recording evidence
// against code that was never applied.
export function hasBuiltSomething(changeSets: ChangeSetStatus[]): boolean {
  return changeSets.some((c) => c.status === "applied");
}

// stripSourceTag removes a leading "[...]" attribution prefix -- e.g.
// "[Source: human_approved task]" -- that the model prepends when
// re-listing a requirement across revise cycles. internal/workflow/dedup.go's
// normalizeJudgment strips the same prefix, but only for COMPARISON
// (dedup), lower-casing and stripping punctuation along the way; the stored
// text itself keeps the tag, so it showed up verbatim in VerifyPage's
// requirement statement. This is the display-only equivalent: it strips
// every leading bracketed tag but leaves the rest of the text exactly as
// written, casing and punctuation included.
export function stripSourceTag(text: string): string {
  let s = text.trimStart();
  while (s.startsWith("[")) {
    const end = s.indexOf("]");
    if (end < 0) break;
    s = s.slice(end + 1).trimStart();
  }
  return s;
}
