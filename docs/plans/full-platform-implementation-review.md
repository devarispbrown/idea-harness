# Full-Platform Implementation Review (M1/M2/M3)

A six-perspective adversarial review of the M1/M2/M3 **implementation** (not the
plans). The M4 implementation review earlier found real data-corruption bugs that
its plan review missed, so the same lens was applied across the whole platform
before any v1.0 claim.

**Verdict: 6/6 reviewers rejected.** The happy path works end-to-end, but the
implementation has latent correctness/security bugs that only manifest under
multiple initiatives, revise cycles, concurrent operations, or a crash mid-apply.

Perspectives: Security, Quality, Reliability, Product, Architecture, UX/API.

## Blockers found (and disposition)

| # | Severity | Finding | Disposition |
|---|----------|---------|-------------|
| 1 | blocker | `GetChangeSetByRef` resolved `CHG-N` with no initiative filter → a multi-initiative project's `idea apply CHG-1` could act on the wrong change set (wrong patch into the source tree). | **Fixed** — fails closed on ambiguity; tests added. |
| 2 | blocker | Review approval was non-monotonic: `runReviewLoop` evaluated only the current cycle, but the verify gate checks ALL findings, so stale critical/high findings from prior revise cycles wedge completion. | **Fixed** — pass now disposes open findings; verify gate no longer wedged. |
| 3 | blocker | `changes_requested`/`rejected` were terminal dead ends: `Execute` marked the task done before review and nothing produced `superseded`, so the human's verdict had no actionable path. | **Fixed** — verdict re-opens the task; re-execute supersedes the prior change set. Test added. |
| 4 | blocker | `ApplyChangeSet` was not crash/error-atomic: pre-image manifest persisted only AFTER `fsdiff.Apply` mutated the workspace; a mid-apply failure leaves partial mutation with no revert path. | **Deferred** — needs pre-image capture before mutation + startup recovery of a stuck `applying` change set. |
| 5 | blocker | `EnsureProject`/`ReloadProject` check-then-act race could open two `*store.DB` handles for one project and run stale reconciliation against a live goroutine (spurious "failed"). | **Deferred** — needs single-flight project construction + per-project (not per-engine) mutation lock. |

## High findings (and disposition)

| # | Severity | Finding | Disposition |
|---|----------|---------|-------------|
| 6 | high | Secret sweep + diff redaction use an incomplete credential catalog; preset provider keys (e.g. `GOOGLE_API_KEY=AIza…`) are not in the Redactor's known values; structured model-output fields (requirements/decisions/findings) are stored and re-served unredacted. | **Deferred** — collectSecrets must include presets; FindCredential must check known values; redact structured fields at write. |
| 7 | high | `checkCommand` runs agent-authored build/test code directly on the host, bypassing the executor sandbox and trust broker (the H3 "full sandbox" gap, now confirmed a real untrusted-code-execution hole). | **Deferred** — route build/test through the container sandbox (needs a toolchain image). |
| 8 | high | `Execute` claims the task only at the end; a crash/failure mid-pipeline leaves a change set while the task stays ready → duplicate change sets on retry. | **Deferred** — claim the task atomically with change-set creation; reconcile non-terminal change sets at startup. |
| 9 | high | Task dependency graph (`depends_on`) is ignored at execution; `TaskReady` is never computed, so dependent tasks can run out of order. | **Deferred** — compute readiness from the graph. |
| 10 | high | `handleListTasks` read the wrong initiative (fell back to the oldest). | **Fixed** — reads the `{id}` path parameter. |
| 11 | high | Adversarial review of a change set runs only AFTER the human approves, and can silently override the human's approve verdict (inverted gate). | **Deferred** — review before the human sees the diff; treat human approve as authoritative. |
| 12 | high | `ApplyChangeSet` never checks workspace drift vs the base snapshot; `conflicted` is never produced → clobbers concurrent user edits. | **Deferred** — snapshot-and-compare before apply. |
| 13 | high | `CancelOperation` unconditionally writes `cancelled`, so a concurrent success is overwritten and its `result_ref` cleared. | **Deferred** — conditional terminal write (`WHERE status IN (running,pending)`). |
| 14 | high | `runExecutionPlan` silently drops unresolved `depends_on` references. | **Deferred** — validate/hard-error on unresolved edges. |
| 15 | high | An apply failure strands the change set in `applying` (no retry/revert path; `conflicted` never written). | **Deferred** — recover `applying` → `conflicted`/`approved` on error + startup. |

## Conclusion

This is exactly why the platform was **not** v1.0 yet: a single-milestone
implementation review (M4) had already fixed real bugs, and the same review over
M1/M2/M3 surfaced five blockers and ten high-severity issues. Three blockers
(cross-initiative authorization, non-monotonic review, `changes_requested`
dead-end) plus the wrong-initiative tasks endpoint are fixed in this pass. The
remaining items are the honest v1.0 gate, with the four most important being
apply atomicity, the project-runtime race, secret-catalog completion, and host
execution of agent code (H3).
