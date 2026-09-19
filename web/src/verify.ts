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

// stripSourceTagsInText removes "[Source: ...]" attributions wherever they
// appear in a document, for display. stripSourceTag handles a single leading
// tag on a requirement title; the PRD reader shows whole documents, where the
// tags sit at the start of list items. Only tags starting "[Source:" are
// touched, so ordinary bracketed text survives.
export function stripSourceTagsInText(text: string): string {
  return text.replace(/\[Source:[^\]\n]*\]\s*/gi, "");
}

// ---- task 7: evidence from tests (C2 wire contract) ----------------------
//
// A requirement's `evidence_tests` (docs/plans/icp-v06.md "C2 wire
// contract"): the test names a passing check maps to, so a passing check
// reads as evidence rather than a human having to re-derive it.

export interface EvidenceTest {
  name: string;
  status: "passed" | "failed";
  change_set?: string;
}

// allEvidencePassed is true only when there IS evidence and every one of it
// passed -- an empty list is "no automatic evidence", not "all passed",
// which matters for hasConfirmableEvidence below (an empty list must never
// make a requirement look confirmable).
export function allEvidencePassed(tests: EvidenceTest[] | undefined): boolean {
  return !!tests && tests.length > 0 && tests.every((t) => t.status === "passed");
}

// evidenceSummary (task 7's exact wording) is the line shown per requirement:
// "✓ 3 passing tests: test_a, test_b, test_c" when every mapped test passed,
// or a plainer count when some failed -- criteria with no passing evidence at
// all keep the manual Pass/Fail with nothing extra to show here.
export function evidenceSummary(tests: EvidenceTest[] | undefined): string {
  if (!tests || tests.length === 0) return "";
  const names = tests.map((t) => t.name);
  if (allEvidencePassed(tests)) {
    return `✓ ${tests.length} passing test${tests.length === 1 ? "" : "s"}: ${names.join(", ")}`;
  }
  const passed = tests.filter((t) => t.status === "passed").length;
  return `${passed} of ${tests.length} tests passing: ${names.join(", ")}`;
}

export interface EvidenceRequirementLike {
  status: string;
  evidence_tests?: EvidenceTest[];
}

// hasConfirmableEvidence gates the "Confirm all with passing tests" button
// (task 7): it only makes sense to offer when at least one active,
// not-yet-verified requirement actually has evidence that all passed --
// otherwise the button would post `confirm_passing_tests` and confirm
// nothing.
export function hasConfirmableEvidence(reqs: EvidenceRequirementLike[]): boolean {
  return reqs.some(
    (r) => r.status !== "removed" && r.status !== "verified" && r.status !== "waived" && allEvidencePassed(r.evidence_tests),
  );
}
