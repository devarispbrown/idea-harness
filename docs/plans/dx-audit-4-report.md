# DX Audit 4 — Report

Fourth developer-experience audit, against `main` @ `33d3e8b` after the audit-3
remediation merged.

- **Verdict:** FAIL · **Overall DX 79/100** (from 74, from 70) · all nine areas *Observed*
- **Install route:** the documented canonical one — `git clone` + `go install ./cmd/idea`
- **Scope:** all 14 stages except `deploy`; 11 core + 7 optional, dry-run confirmed
- Rendered: https://claude.ai/code/artifact/74af51bf-17e8-4d1c-aa80-48d65bb331a5

## Hard gates — all pass

| Gate | Measured | Budget | Audit 3 |
|---|---|---|---|
| Magic path | 17.12s · 11 commands · 0 credentials · 1 switch | 900s · 8 · 2 · 4 | 9.33s |
| Local development | 14s · 3 commands | 600s · 4 | 14s |
| Time to recovery | ~1s | 300s | 1.10s |

18/18 journey steps passed.

## Scores

| Area | Weight | Audit 3 | Audit 4 | Δ |
|---|---:|---:|---:|---:|
| Time to first value | 20 | 82 | 84 | +2 |
| API | 15 | 72 | 76 | +4 |
| SDK | 10 | 65 | 65 | 0 |
| CLI / config | 10 | 74 | 86 | **+12** |
| Errors / recovery | 12 | 74 | 84 | **+10** |
| Documentation | 10 | 55 | 62 | +7 |
| Local dev | 8 | 94 | 94 | 0 |
| Testing / quality | 8 | 78 | 86 | +8 |
| Release / compatibility | 7 | 70 | 70 | 0 |
| **Overall DX** | 100 | **74** | **79** | **+5** |

Six of nine areas now clear the world-class floor of 75, against three at audit 2.

## What the remediation fixed

Every audit-3 finding re-probed against a binary built from `main`: `idea verify`
can fail, arity is enforced on every bounded command, `--` no longer bypasses the
subcommand check, `config validate` is honest outside a project. The three P0
security-documentation defects are corrected, `docs/ui.md` was rewritten from
`web/src` and does **not** reproduce the fabricated-tabs defect, and two
exploitable bugs neither prior audit found — unredacted Anthropic and modern
OpenAI keys, and DNS rebinding in the SSRF guard — are fixed with tests.

## Findings

### P0

1. **The remediation's own fix stranded initiatives permanently.** The atomic
   claim released on `OperationFailed` and not `OperationCancelled`, so
   `idea cancel` left the initiative running forever with no recovery path.
   **Fixed during the audit in `39ca31a`**, mutation-checked, with an
   orphaned-claim sweep as backstop.
2. **The false redaction guarantee survives in a third document.**
   `docs/configuration.md` still promises redaction "including logs, events,
   blobs" — there is no redacting log handler, and the file-content store is
   constructed with a nil redactor.
3. **The documented with-key path dead-ends.** `idea init` starts the daemon,
   which inherits the environment at that instant; the key exported afterwards is
   invisible to it, and the run fails on a non-retryable 401.

### P1

4. `docs/security.md`'s "review runs require a distinct actor" is enforced
   nowhere.
5. `ApplyChangeSetInitiative` and `RevertChangeSetInitiative` bypass the claim
   entirely.
6. `failOperation` and `failOperationAfterPanic` write unconditionally, so a
   panic after a successful terminal write rewrites a completed run as failed.
7. The SSE feed `ui.md` stopped claiming is still asserted in `api.md` and
   `cli.md`.
8. `anthropic/claude-3-7-sonnet` appears eight times; the catalog spells it
   `-latest`, and nothing validates the model half of a spec.
9. "Configuration written but not applied → 409" is true for one of the two
   config endpoints.
10. A class of `required` arrays the handler emits as `null`.
11. The spec says operations are "newest last"; the store orders `DESC`.

### P2

Both ledgers introduced last round are inaccurate in opposite directions, and the
plan contradicts `deferred.md` about what was mechanically checked. The
capability broker has zero non-test callers while two documents list it as
shipped; `architecture.md` carries the inverted redaction claim a third time;
`container` is a second placeholder executor listed as a real kind in four
documents.

## The pattern

| Audit | Pattern |
|---|---|
| 2 | A fix made correctly in one place, not propagated to its siblings |
| 3 | The canonical source corrected, the derivative copies left stale |
| 4 | The correction reaching two of three documents |

Each round the remediation gets closer and stops one document short. And the P0
the remediation itself introduced is the sharper lesson: the claim was reviewed
four times, two of those reviews asked specifically whether it was released on
every exit path, and nobody traced the cancellation path. Reading did not catch
it; enumeration would have.

## Delegated evidence

| Deep dive | Findings | Re-verified | Disagreements |
|---|---|---|---|
| Code and API surface | 9 ranked | Yes — the P0 confirmed against `failOperation`'s status condition, the claim source-lists and `ListUnfinishedOperations`' two-status filter, then fixed and mutation-checked | None |
| Documentation | 15 ranked + ~10 below the cut | Yes — the `configuration.md` claim and the nil-redactor blob store confirmed directly | None. Its "what checked out" list matters as much as its findings: the `ui.md` rewrite does not reproduce the fabricated-tabs defect, the four-kind executor table is accurate line for line, and the no-key quickstart path runs end to end. |
