# DX Audit 3 — Report

Third developer-experience audit, run against `main` @ `8fa037d` after the
audit-2 remediation merged.

- **Verdict:** FAIL · **Overall DX 74/100** (from 70) · all nine areas *Observed*
- **Install route:** the documented canonical one — `git clone` + `go install ./cmd/idea`
  into a private GOPATH. Not a prebuilt binary: the repo is private, so this is
  what README says to do.
- **Environment:** macOS darwin/arm64, Go 1.25.5; fresh clone, empty state root per run
- **Scope:** all 14 stages except `deploy`; 11 core + 7 optional, dry-run confirmed

Rendered: https://claude.ai/code/artifact/d556ab02-6d80-48ee-a6de-3f61c97b3766

## Hard gates — all pass

| Gate | Measured | Budget | Audit 2 |
|---|---|---|---|
| Magic path | 9.33s · 11 commands · 0 credentials · 1 switch | 900s · 8 · 2 · 4 | 4.67s |
| Local development | 14s · 3 commands | 600s · 4 | 15s |
| Time to recovery | 1.10s | 300s | — |

Slower than audit 2 by design: this run compiles from source rather than
unpacking a release tarball. 18/18 journey steps passed.

## Scores

| Area | Weight | Audit 2 | Audit 3 | Δ |
|---|---:|---:|---:|---:|
| Time to first value | 20 | 85 | 82 | −3 |
| API | 15 | 68 | 72 | +4 |
| SDK | 10 | 65 | 65 | 0 |
| CLI / config | 10 | 58 | 74 | **+16** |
| Errors / recovery | 12 | 64 | 74 | **+10** |
| Documentation | 10 | 58 | 55 | −3 |
| Local dev | 8 | 94 | 94 | 0 |
| Testing / quality | 8 | 70 | 78 | +8 |
| Release / compatibility | 7 | 62 | 70 | +8 |
| **Overall DX** | 100 | **70** | **74** | **+4** |

Documentation fell despite genuine fixes because this pass read the documents
against the code far more completely. Better measurement, not a regression.

## What the remediation fixed

All 13 audit-2 CLI findings re-probed against a binary built from `main`: every
one now exits non-zero with a message naming the problem. Five GETs on `/risk`
produced 0 new rows and 0 new events. `idea docs add -` works. All ten `--json`
commands return valid JSON. 409/404/401 semantics correct; `/health` still
unauthenticated by design.

## Named gates

| Gate | Result | Why |
|---|---|---|
| `STALE_PUBLIC_REFERENCE` | **FAIL** | Audit 2's six claims fixed; a deeper read found fifteen more, including three false security guarantees |
| `UNEXPLAINED_ERROR` | **FAIL** | `idea verify` prints ✓ and exits 0 for every gate state including a recorded failure; `idea watch` exits 0 on the fatal path |
| `BROKEN_CANONICAL_INSTALL` | PASS WITH DEBT | Route works; README's Install block still has no `git clone` (carried from audit 2) |
| `UNTESTED_SUPPORTED_VERSION` | PASS WITH DEBT | darwin/amd64 now in CI; `linux/arm64` built-but-untested, stated in README |
| `BROKEN_QUICKSTART` | PASS | Reproduced end to end in 9.33s |
| `NON_REPRODUCIBLE_BUILD` | PASS | 14s of 600s |
| `UNDOCUMENTED_BREAKING_API` | PASS | CHANGELOG carries `Fixed — BREAKING` with migration |
| `UNSAFE_EXAMPLES` | PASS | No literal credentials anywhere |
| `SDK_API_DRIFT` | PASS (vacuous) | No SDK released |

## Findings

### P0

1. **`docs/security-model.md` promises network isolation the coding agent does not
   get.** `ContainerSandbox` has one non-test caller (the deterministic check);
   agents use `SandboxedAgentExecutor`, which states `--network none` is
   deliberately omitted and the provider key forwarded in.
2. **`docs/ui.md` says uploaded docs are "never served or fed to a model
   unredacted".** Images skip redaction on ingest, on serve, and on egress to the
   vision provider.
3. **`docs/api.md` + `openapi.yaml` say contents are "redacted at write time".**
   The handler comment says the inverse: redaction is at the display surface,
   never at blob-write time.
4. **The quickstart's no-key path is broken by the step above it** — setting a
   model makes the demo fallback unreachable, and the run fails with a
   non-retryable 401.
5. **README's front page shows `idea trace task T-3`** — task codes are `TASK-<n>`,
   so it cannot resolve.
6. **`docs/ui.md` documents eleven tabs that do not exist.** The app has six phase
   pages and a seven-value route union; no activity view, no logs view.

### P1

7. **`idea watch` still exits 0** — audit 2's fix does not work: `Update` has a
   value receiver, so `m.fatal` is never written on the returned-from struct. The
   test written for it was vacuous.
8. **`idea verify` cannot fail** — prints ✓ and exits 0 for every gate state,
   including a recorded failure. Quickstart tells CI to use it.
9. **Cancelling an apply reports success while the goroutine keeps writing** to
   the source tree and git-committing; the terminal status error is discarded, as
   at eleven other sites.
10. **Four async operations check a running flag they never set**, so a
    double-click corrupts the execution plan and wedges further revises.
11. **Two reload-safety comments are false**; a test pins the unsafe shape.
12. **The engine mutex is held across model calls and a 15-minute container wait**,
    from synchronous handlers, on a server with no timeouts and body caps on 2 of
    ~22 handlers.
13. **`POST /findings/{id}/disposition` answers 200 for a finding that does not
    exist** and commits a provenance event saying a human accepted it.
14. **`CHANGELOG.md` contradicts the code** about `doctor`, three commits later.

### P2

`idea config validate` is the false-green `doctor` was fixed for twice; `--`
walks past the new subcommand check (`idea config -- validate`,
`idea daemon -- stop`); positional arity unenforced (`idea apply CHG-1 CHG-2`);
`GET /operations/{id}` reconciles every project as a side effect of a read;
`idea dev` clobbers the live daemon's pidfile; no `recover()` in
`launchOperation`; a cancelled execute strands the change set in
`checks_running`; `changed` is project-wide and picks the oldest initiative; a
413 writes two JSON bodies; README's Install block has no `git clone`.

Plus ~30 further documentation defects catalogued below the cut.

## The pattern

Audit 2: a fix made in one place and not propagated to its siblings. Audit 3, one
level out: **the remediation corrected each canonical source and left the
derivative copies stale.** `security.md` right / `ui.md` wrong; `openapi.yaml`
right / `api.md` wrong; `doctor` guarded / `config validate` not; a changelog
entry outliving its code by three commits.

The four new guards are structurally blind to this. They compare route sets,
request-body field names and 409 declarations — never prose, never response
shapes, never whether a documented command runs. Every P0 above passes all four.

## Delegated evidence

| Deep dive | Findings | Re-verified | Disagreements |
|---|---|---|---|
| CLI and API surface | 15 ranked + 6 below the cut | Yes — watch receiver bug confirmed by method-set analysis; `config validate` and `--` reproduced against a built binary | None. Its claim that my watch test was vacuous is correct, and I could not disprove it because Bubble Tea cannot open a TTY under `go test` — which is why the original test proved nothing. |
| Documentation | 15 ranked + ~30 below the cut | Yes — the security overclaim verified directly against `agent.go` and `ContainerSandbox`'s single caller | None |
