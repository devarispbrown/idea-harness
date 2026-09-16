# DX Audit 9 — report

**HEAD `4bacf47`.** Run 2026-09-15, 16 commits after audit 8 (`1f70f79`). Every metric
below is **Observed** unless labelled otherwise.

## Verdict

**PASS WITH DEBT. Overall DX = 75/100** (Observed), up from audit 8's 60.

Both hard gates pass. No named gate fires — the three that fired in audit 8 are closed, and
2.9.4's `UNVERIFIABLE_CI_PARITY` is closed with them. What remains is backlog: 17 recorded
deferred items, four audit-7 leftovers, and one P1 the keyed path still carries.

## A correction to audit 8 before anything else

Audit 8 recorded the keyed magic path as **3/3 PASS** and reported both hard gates passing
partly on that basis. Audit 9 ran the same block against the same model: **0/2 complete**,
at 235 s and 199 s, both stopped at a PRD review that did not converge.

That is model non-determinism, which means audit 8's keyed result was partly luck and was
presented as firmer than the evidence supported. The no-key path was and is genuinely solid:
10/10 then, 5/5 here. **The keyed path has never been observed reaching `✓ initiative
complete` in this audit** — see P1-1.

## Hard gates

| Gate | Result | Evidence |
|---|---|---|
| Magic path (`MAGIC_PATH_MAX_MIN` = 15) | **PASS** | No-key, README block verbatim: **5/5 at 2–4 s**, 7 commands. Keyed, following the guidance the tool prints: **442 s / 513 s**, 2/2 reaching an applied change set. Observed |
| Local development (`LOCAL_DEV_MAX_MIN` = 10) | **PASS** | Clean build + full suite: **57 s** (build 14 s, test 43 s, 23 packages). Observed |

`go install ./cmd/idea` works cold and reports `dev (4bacf47)`.

## Named gates

| Gate | Fires | Evidence |
|---|---|---|
| `UNDOCUMENTED_BREAKING_API` | No | All three `decodeStrict` endpoints named in CHANGELOG and the spec; the census is pinned by AST in `bodycheck` and fails on a fourth |
| `STALE_PUBLIC_REFERENCE` | No | `docs/versioning.md`'s dangling pointer gone and the rules inlined; `docs-lint` now checks reference existence and every documented `provider/model` against the catalog |
| `UNEXPLAINED_ERROR` | No | All three TTR scenarios inside `TTR_TARGET_MIN`; see the table below |
| `UNVERIFIABLE_CI_PARITY` (2.9.4) | No | `make build` was the last documented target CI never ran; a test asserts every documented target appears in a CI `run:` step |
| `SDK_API_DRIFT` | No | Cleared in audit 8 and still clear |
| `NON_REPRODUCIBLE_BUILD` | No | 57 s clean clone |
| `BROKEN_CANONICAL_INSTALL` | No | Cold `go install` verified |
| `BROKEN_QUICKSTART` | No | A reproducible end-to-end quickstart exists and runs inside the gate — the no-key one. The keyed variant's shortfall is recorded as P1-1 rather than as this gate, because the gate asks whether *a* reproducible route exists |

## Time to recovery — the three scenarios audit 8 failed

`TTR_TARGET_MIN` = 5:00. Audit 8: **0 of 3** inside target. Audit 9: **3 of 3**.

| Scenario | Audit 8 | Audit 9 | How |
|---|---|---|---|
| `idea init` with the port held by a non-daemon | **never** — 786 s observed, ~51 min code-bound, 0 of 6 diagnostic questions answered | **1.3 s** | The probe has its own 2 s timeout, the spawned child is waited on, and the failure quotes this run's log: `the daemon exited immediately (exit status 1): idea: listen tcp …: bind: address already in use; run \`idea logs\` for the full output` |
| Provider accepts and never answers | **~15:05** to the first user-visible error; `working…` was the only output | **31.6 s** to the first actionable signal | `still working… 20s elapsed (gives up at 15m0s; \`idea cancel <id>\` stops it)`, then every 30 s. The underlying provider bound is unchanged at 3 × 5 min; what changed is that the user can tell, and can stop it |
| `idea next` → ambiguous `CHG-1` | **no route existed** — the refusal demanded an id no human-readable command printed | **0.05 s** | `idea next` prints the full id when the human one is ambiguous, so its prescription runs first try. If a user types `CHG-1` anyway, the refusal now lists both candidates with their initiative titles |

The first of those was re-measured mid-audit and found to be 0.1 s but **without** the cause:
on a very first `idea init` the state root does not exist, so the redirect of the daemon's
stderr into `daemon.log` silently did not happen and the message pointed at an empty log.
Fixed in `4bacf47`; the 1.3 s above is after that fix.

## Scores

| Area | Weight | Score | Evidence | Movement |
|---|---:|---:|---|---|
| Time to first value | 20 | 74 | Observed | 68 → 74 · no-key 5/5 at 2–4 s; keyed completes in ~7–9 min but needs 11+ commands against a target of 8, and the documented block had to be corrected rather than the flow simplified |
| API | 15 | 74 | Observed | 56 → 74 · 422 typed for both malformations with no path leak; the invalid schema fixed and guarded; 10 missing 500s, a 404, 422 on 74 operations and `default` on all 81 declared; `$ref` resolution and the operation count now checked |
| SDK | 10 | 60 | Estimated (no SDK; scored on the first-party client) | 55 → 60 · `default` gives a generated client somewhere to put 403/405; otherwise untouched this round |
| CLI/config | 10 | 70 | Observed | 48 → 70 · `retry` waits and reports the outcome, `reopen-phase` exists, six refusals name a route, `status` exits non-zero and `--json` carries the reason. No exit-code taxonomy, no delete verb, `idea config` still prints the model list |
| Errors/recovery | 12 | 76 | Observed | 40 → 76 · the largest move, and the one with the most evidence behind it: 3 of 3 TTR scenarios inside target, `doctor` no longer green over an unreadable store or blind to a live daemon |
| Documentation | 10 | 78 | Observed | 60 → 78 · `--accept-findings` where the magic path is, the keyed path described as it actually runs, ledger L1 closed and its class partly guarded. No troubleshooting doc, no exit-code table |
| Local dev | 8 | 88 | Observed | 85 → 88 · 57 s, and `make build` now runs in CI with a parity test |
| Testing/quality | 8 | 84 | Observed + CI-observed | 72 → 84 · 506 test functions (was 466), 11 guard steps in CI, F1 root-caused, and four vacuous tests found and fixed by review |
| Release/compatibility | 7 | 72 | Observed | 56 → 72 · released history restored and guarded against its own class; the breaking change marked as one per this changelog's precedent |

**Overall DX = 75.**

## P1 findings

### P1-1 — The keyed magic path has not been observed completing

Following the tool's own guidance the path reaches an applied change set in 442 s / 513 s,
2 of 2. It did not reach `✓ initiative complete` in either run, and the reason is structural
rather than a defect: the run ends with **12 live requirements carrying 3–5 acceptance
criteria each**, so completion needs roughly fifty `idea verify` sign-offs. The demo path
needs one.

Nothing here is broken — `idea next` reports the gate honestly (`0/5 verified (5 need manual
sign-off)`) and every refusal named a working route. But "a brand-new developer reaches a
verified end-to-end outcome" is only demonstrated on the no-key path, and this audit should
not imply otherwise.

### P1-2 — Four audit-7 leftovers, all still open

Verified against the tree: no `docs/troubleshooting.md`; no delete / archive / reset verb, so
an initiative is unremovable (A3 fixed the *recovery* case, not the feature); no exit-code
table in `docs/cli.md`; and `idea config` still prints the model list while its own summary
says "show or validate .idea-harness config".

## What the round fixed, briefly

Three named gates closed. All three TTR scenarios moved inside target. Seventeen Track B/C/D
findings, each mutation-checked. F1 — the intermittent that made every other guard's green
worth less — root-caused to `grep -q` closing the pipe, the Go binary taking EPIPE on its next
write, Go re-raising SIGPIPE, and `pipefail` adopting 141: measured at 7 failures in 150 runs
against the real binary, and 0 in 150 for the shape that replaced it.

## The pattern worth carrying forward

Adversarial review rejected or corrected something in every commit this round, and the
recurring shape was named precisely by one reviewer: **a guard written to the shape of the
instance just fixed, rather than to the class the commit message claimed.** Four were proven
by reintroducing a defect and watching the tree stay green — a test that supplied its own
inputs and so validated its own strings; a test that compared a constant to a copy of itself
while claiming another file was the authority; a matcher pinned to the phrasing just adopted;
and a CI check that matched its own comment.

Three separate guards this round matched prose instead of code. That is the failure mode to
design against next, and the cheapest defence is the one that caught all four: write the
mutation first, and watch it fail before believing the test.

## World-class checklist

| # | Criterion | Result |
|---|---|---|
| 1 | All hard gates pass | **Yes** |
| 2 | Overall DX ≥ 85 | No — 75 |
| 3 | Every area ≥ 75 | No — five are below: SDK 60, CLI 70, Release 72, TTFV 74, API 74. Four clear it: errors 76, docs 78, testing 84, local dev 88 |
| 4 | No unresolved P0/P1 | No — 0 P0, 2 P1 |
| 5 | Magic-path timing Observed ≤ 15 min | **Yes** — no-key 5/5 at 2–4 s; keyed 442/513 s to an applied change set |
