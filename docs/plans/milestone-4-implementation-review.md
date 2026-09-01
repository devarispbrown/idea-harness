# Milestone 4 Implementation — Adversarial Review Record

Six reviewers evaluated the **completed M4 implementation** (code) against
[`milestone-4-requirement-to-verified-product.md`](./milestone-4-requirement-to-verified-product.md)
and the B1–B10 resolutions in
[`milestone-4-adversarial-review.md`](./milestone-4-adversarial-review.md).

## Verdict

| Perspective | Verdict |
|---|---|
| CTO / principal architect | **reject** |
| Staff Product | approve_with_changes |
| Staff Security | **reject** |
| Staff Quality | **reject** |
| Staff SRE / Platform | **reject** |
| Head of Design/UX | **reject** |

**Net: the M4 happy path works, but the implementation is not safe to ship as-is.** The
B1–B10 mechanics are largely present and the demo passes — but three data-integrity/crash-recovery
gaps and a redaction bug can corrupt the user's real source tree. All 19 blockers collapse into
~10 cross-cutting issues.

## Critical (data integrity — fix first)

**C1 — The redaction layer breaks the diff/apply/revert path (B1/B3).**
`fsdiff.Snapshot` hashes RAW bytes, but `blob.Store.Put` redacts content *before* hashing. So
`FileChange.Digest` (raw hash) never matches the stored key (redacted hash). Any produced file whose
content matches a credential pattern (`api_key=`, `password=`, JWT, PEM, `sk-…`) is rejected as
"content missing or corrupt" by `checkPatchApplies`, the secret sweep never fires, and — worst —
**revert restores the redacted bytes, silently replacing the user's real secrets with `[REDACTED]`**
in their source tree. Hidden in CI because every workflow test uses `blob.New(..., nil)` while
production wires `redactor.Redact`.
**Fix:** give the diff/apply/revert path a redaction-free (digest-transparent) content store, and move
redaction to display/artifact surfaces only. Add a test with a redacting store + a secret-bearing file.

**C2 — Revert is not byte-for-byte (B1).**
A direct consequence of C1: the pre-image is persisted through the redactor, so apply→revert does not
restore the original bytes. This is the exact crash-safety/integrity property B1 was written to
guarantee.

## High (correctness — must fix)

**H1 — Completion gate deadlocks on removed requirements (B4).**
`VerifyInitiative` requires *every* requirement row to reach `verified`, but superseded requirements
are `removed` and can never transition. Any initiative that went through a review-revise cycle can
never reach `complete`. (`Coverage` correctly skips `removed`; the gate loop does not — an internal
inconsistency that proves the bug.)

**H2 — B1 crash recovery is dead code.**
`ApplyChangeSet` never writes `prepared`/`workspace_mutated` journal rows, and `ReconcileJournal` is
never called in production. A crash between `fsdiff.Apply` and `MarkChangeSetApplied` leaves the
workspace half-mutated and the change set stuck in `applying` (no legal transition out), with no
journaled pre-image to recover from.

**H3 — Build/test run unsandboxed on the host, in the bare work root (B5).**
`checkCommand` runs `sh -c "<cmd>"` with full daemon authority in the near-empty isolated work root —
not in the `ContainerSandbox`, and not against a post-apply copy. So (a) the agent's untrusted code
runs with full host authority, and (b) the build/test can't actually validate the real project.

## Medium

- **Secret sweep is a whole-word marker scan** — real credentials (`sk_live_*`, PEM, JWT, `ghp_*`,
  `AKIA…`) are NOT detected. The strong patterns in `security/redact.go` are wired only as redaction,
  not as a rejection signal.
- **Verified is not cleared on revert** — apply→verify→complete→revert leaves the requirement
  `verified` and the initiative `complete` with the implementation rolled back.
- **B6 cancellation is half-wired** — no `idea cancel`/route, no persisted attempt PID, no startup
  orphan reaping; the execute goroutine runs on `context.Background()` so `ctx.Done()` never fires
  (only the hardcoded 15-minute deadline works).
- **B7 cost is not recorded/enforced** — no tokens/cost (not even "unknown"), `max_cost_usd`/`max_tokens`
  never enforced.
- **Stale blocking findings wedge the gate** — failed build/test findings are never dispositioned, so
  `hasOpenBlockingFindings` blocks completion forever after any rejected change set.
- **User's real billing keys are forwarded into a network-egress agent container.**

## Low

- UI Verify step is hardcoded `false`; no coverage/sign-off affordance (Track C acceptance unmet).
- `idea next` is a single-step dispatcher, not a loop (no autonomy presets).
- Work root is per-initiative, not per-task → diff contamination across tasks.
- No per-initiative execute mutex → concurrent `execute` races.
- `patch-applies` is a blob-retrievability check, not the promised re-apply-and-rehash.
- File permissions lost on apply (0600).
- `config.go` error text omits `hermes`; `RecordHumanPass` produces `human:human`.
- Timeouts not configurable; check commands have no timeout.

## Recommended fix order

1. **C1/C2 (redaction/digest) — P0.** Data corruption of the user's real tree.
2. **H1 (removed-requirement gate) — P1.** One-line filter; unblocks `complete` after any revise.
3. **H2 (crash-recovery journal) — P1.** Write `prepared`/`workspace_mutated` + startup reconcile.
4. **H3 (sandbox the checks) — P1.** Route `checkCommand` through `ContainerSandbox`.
5. **M (secret detector, revert-clears-verified, cancel route, cost enforcement) — P2.**
6. **L (UI verify step, `next` loop, mutex, patch-applies strength) — P3.**
