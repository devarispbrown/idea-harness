# DX Audit 6 — report

**Verdict: FAIL.** Overall DX **55/100**. The magic-path gate fails on the keyed
path (`BROKEN_QUICKSTART`); the local-dev gate passes comfortably.

Measured against `a3f79f5`, CI green, from a fresh clone. Binary built by the
README's own route (`go install ./cmd/idea`) — `dev (a3f79f5)`.

---

## The headline, and it is not a regression

The score moved 61 → 55, and as in audit 5 that is **measurement depth, not tree
regression**. Two things happened this round that no previous round did:

1. The README's Quick start block was run **with `idea model set` and a real
   provider key** — the keyed half of the block. Every prior audit ran only the
   no-key half.
2. Four specialist dives ran against a live daemon rather than against artifacts.

The no-key path, which audit 5 measured at **0/5**, is now **10/10**. That fix is
real and it is the largest single improvement in six rounds.

---

## Hard gates

| Gate | Result | Evidence |
|---|---|---|
| Magic path (`MAGIC_PATH_MAX_MIN` = 15) | **FAIL** | keyed path 0/4; no-key path 10/10 at ~2s. Observed |
| Local development (`LOCAL_DEV_MAX_MIN` = 10) | **PASS** | clean clone → `make build` 16s → `make test` 17s, green. Observed |

Named gates fired: `BROKEN_QUICKSTART`, `UNEXPLAINED_ERROR` (×6),
`UNDOCUMENTED_BREAKING_API`, `UNTESTED_SUPPORTED_VERSION` (narrow).
Not fired: `STALE_PUBLIC_REFERENCE` (0 broken links across README, CONTRIBUTING,
CHANGELOG and all 12 `docs/*.md`), `BROKEN_CANONICAL_INSTALL`,
`NON_REPRODUCIBLE_BUILD`, `UNSAFE_EXAMPLES`. `SDK_API_DRIFT` is N/A — there is no
SDK.

---

## P0-1 — the README's Quick start cannot complete with a real model

4/4 runs, deterministic, using `openai/gpt-4o-mini` — the model the README names.

```
idea new "Build usage-based billing"   → ✓ reviewed PRD ready
idea next --until-human                → Next: resolve open decisions/questions/findings
                                         — idea decisions            (exit 0)
idea decisions                         → No decisions yet.
idea review CHG-1 approve              → idea: no change set matching "CHG-1"
idea apply CHG-1                       → idea: no change set matching "CHG-1"
idea verify REQ-1 --pass               → idea: workflow: requirement "REQ-1" in state
                                         "removed" cannot take a human judgment
```

**Mechanism.** `runReviewLoop` (`internal/workflow/engine.go:747-812`) runs up to
`max_cycles` (default 3). Each blocked cycle calls `SupersedeRequirements` and
regenerates the PRD. Three blocked cycles → `needs_human_judgment`. That also
explains the third failure: the revise cycles supersede `REQ-1`, so the README's
hard-coded `REQ-1` and `CHG-1` are only valid when the review converges on cycle
one.

**There is no CLI route out of `needs_human_judgment`.** Verified by execution:

- `idea advance` refuses — `advance requires an approved PRD (status=needs_human_judgment)`
  (`internal/daemon/runtime.go:501`)
- dispositioning **all 13** findings through the API does not clear it; the gate
  is the initiative *status*, not the findings (`internal/workflow/next.go:61`)
- the escape is `POST …/initiatives/{id}/approve`, which has **no CLI command**.
  No file under `internal/cli/` calls `approve`, `revise`, `revise-plan`,
  `decisions/{id}/resolve`, `questions/{id}/answer` or `findings/{id}/disposition`
- `idea next` names `idea decisions`, which prints `No decisions yet.` The content
  is under `idea reviews` and `idea explain`; nothing points there

**The web UI has the escape** (`ui.md:173`) — and a binary from the README's own
canonical install serves `<h1>Web UI not built</h1>`. The loop is closed.

Three separate code paths reach this state: M1 non-convergence
(`engine.go:801`), M2 plan-review blocked (`advance.go:371`), and a
post-completion re-fail (`verify.go:222`).

---

## P0-2 — `idea next` reports a *failed* initiative as running, and exits 0

This is audit 5's **A3**. It never shipped, and it was never recorded in
`deferred.md`. `internal/workflow/next.go:65` is still
`default: // running / failed → return "wait"`, untouched since `dfa8c77`,
pre-`v0.1.0`.

```
idea status → ● failed  → workflow: framing: openai: status 401: …
idea next   → workflow running — try `idea watch`        (exit 0, forever)
idea watch  → idea: could not open a new TTY             (exit 1)
```

`idea execute` correctly says *"`idea next` names the step this initiative is
actually ready for"*. `idea next` lies. `idea watch` needs a TTY. Reproduced from
two independent causes — a provider 401 and `idea cancel` — and TTR is unbounded.

The single highest-leverage fix in this report: return a `failed` action carrying
`failure_reason`, and give `reportNextAction` a `case "failed"` that prints it and
returns an error.

---

## Why six audits missed P0-1

`internal/model/fake.go:185`:

```go
case "review":
    m = map[string]any{"findings": []any{}}
```

The demo provider's adversarial reviewer returns zero findings **unconditionally**.
The review loop therefore always converges on cycle one, and
`needs_human_judgment` is **structurally unreachable** on the demo path.

CI's smoke step is labelled *"end-to-end, deterministic demo — no secrets"*.
`scripts/smoke.sh` never sets a model. No test under `internal/cli/` drives the
CLI out of `needs_human_judgment`. So every guard, every smoke run, and five
prior audits ran against a reviewer hard-coded to approve.

This is the same class of gap as audit 5's: the guards compare artifacts to
artifacts, and the one path that exercises real judgment is the one nothing runs.

---

## Five audit-5 items reported fixed that were not

This is the finding that matters most for process. `deferred.md` already calls out
the pattern three times — *"planned, undone, unrecorded, and it then survived a
round with no owner"* (rows 31–33). It happened five more times, including to a P0.

| Item | Claimed | Actual |
|---|---|---|
| **A3** (P0) | Track A of the audit-5 plan | Never implemented, never deferred, never recorded |
| **D5** (P1) | "Render the resolved config" | `idea config` is still byte-identical to `idea model`, and does not validate. Confirmed after normalising an unstable Ollama list ordering that masked the diff |
| **E1** (P1) | "Correct api.md" | Fixed in `openapi.yaml` only. `docs/api.md:316` still documents `approve`/`reject`, which `execute.go:1255` rejects with a 400. `CHANGELOG.md:313` announces the fix |
| **E4** (P1) | "Use the real enum" | Fixed in the spec only. `api.md:305` still shows `accepted, not applicable` — neither is one of the seven values in `enums.go:124-132` |
| **D1** (P1) | State-dir wedge closed | Covers token *rotation*, not token *deletion* — see below |

### D1, precisely

`newClient()` (`internal/cli/client.go:76`) returns an error when the token file
is missing, so `healthy()` returns false, so `daemonUp` is false, so
`tokenRejected = daemonUp && !authAccepted()` can never be true. The branch is
unreachable in exactly the scenario its own code comment cites. Observed:

```
$ idea init && rm -rf $IDEA_HARNESS_STATE
$ idea doctor                     # first command after the wipe
idea dev (a3f79f5)
• daemon: not running (auto-starts on first command)
✓ config valid
                                   (exit 0)
$ curl -s …/api/v1/health
{"status":"ok","version":"dev"}    # it is running
```

The `daemon stop` port fallback **is** fixed and its recovery action works (TTR 2s
Observed). Token *rotation* is also correctly caught. Only deletion is missed.

### Why nothing caught E1/E4

`ledgerGlob = "docs/plans/dx-audit-*-doc-defects.md"`
(`internal/docsgen/ledgercheck.go:20`). Audits 3 and 4 have that file. **Audit 5
never got one.** Two files matched, so `ledger-check` passed — `59 rows checked`,
none from audit 5. The guard was deliberately globbed so the next round's ledger
would be picked up automatically; that only works if the ledger is written.

---

## Delegated evidence

Four dives ran against a live daemon. Every claim quoted below was independently
re-verified against the tree or by execution before inclusion.

### API and contract design
API DX **62/100**, OpenAPI correctness **84/100**, PASS WITH DEBT.

- **F-1 (P1)** — `Engine.Approve` (`engine.go:847`) calls `SetInitiativeStatus`
  with **no state check**, releasing the claim `ClaimInitiativeRunning` exists to
  hold. `handleApprove` (`handlers.go:262`) returns a hardcoded
  `{"status":"approved"}` and maps every error to 500. Two concurrent `advance`
  operations were observed on one initiative. `claim_invariant_test.go` never
  touches `approve`. `POST /resume` regresses `plan_approved` → `approved`.
- **F-10** — bare `/api` escapes both the auth middleware and `apiAwareNotFound`
  (both guard `"/api/"`, `server.go:87` and `:146`): `text/plain`,
  `404 page not found`, unauthenticated.
- **F-13 (P2)** — five endpoints answer a nonexistent id with something other
  than 404. Byte-identical error text yields 400 from `/verify` and 404 from
  `/verification-task`.
- **F-12 (P2)** — `store:` and `workflow:` layer prefixes reach clients; the
  envelope has no `code` field and no request id, and there are four phrasings of
  "already running".
- **F-3 (P2)** — `Operation.type` documents three values (`advance`, `execute`,
  `apply`); the real vocabulary is 11 and **none of the three is ever emitted**.
  Typed as bare `string`, so `enum-check` cannot see it.
- P2, structural: no pagination on 17 list endpoints (`/activity` has a monotonic
  `seq` and no `since`), no idempotency keys, no ETag/If-Match, no `deprecated:`
  anywhere despite ten operations having been moved.

What is genuinely strong: 81 routes ↔ 81 spec operations, exact both directions;
no enum drift across 18 enums; a full sweep of all 42 addressable GET 200
responses found exactly **one** undeclared field; the auth surface has no holes.

### Error experience and TTR
6 `UNEXPLAINED_ERROR` failures, 6 exit-0-while-failed cases.

- **P0-2** above (`idea next` on a failed initiative) — TTR unbounded.
- `idea init` **hangs silently** when the port is held by a non-HTTP listener —
  51s observed with no output at all; `ensureDaemon` loops 50× over a 60s client
  timeout, so worst case ≈ 50 min (Estimated). `idea daemon` is the only command
  that names the cause; `idea daemon stop` asserts nothing is listening, falsely.
- `idea doctor` is green during four separate hard failures: revoked key, corrupt
  `state.db` (every state command 500s), port conflict, and wiped state.
- `idea retry` prints `✓ retry started` and exits 0 when the retry fails
  instantly — but it **does** genuinely recover with a working provider
  (133s Observed).
- `idea verify REQ-99` in **read** mode silently reports on `REQ-1`; the write
  path validates correctly. On a 0-requirement failed initiative,
  `idea verify` prints `✓ verified status: complete`, exit 0.
- Corrupt `harness.yaml` is silently discarded by every command except `doctor`
  and `config validate`, which are both excellent.

Best error in the product: `no project registered for <abs path>; run \`idea init\` first`
— 5/6 questions answered, TTR ~10s.

### Documentation
0 broken links or anchors. All seven guards green — with every defect below
already present.

- **P0** — `api.md:316` documents review verdicts the engine 400s (E1 above).
- Both **new** demo guides contain output blocks a real run does not produce.
  `demo-existing-project.md:103-108` shows one line where the binary prints two
  (`→ advance …` then `→ execute …`), and the next sentence says "Identical to
  greenfield from here" while `demo-greenfield.md:78-79` shows both. Verified by
  running the sequence.
- `demo-greenfield.md:69` sends the reader to `idea decisions` for open
  questions; it prints `No decisions yet.` The command that works is
  `idea explain`, which no document names.
- `ui.md:261` documents a demo-mode predicate that was deliberately removed;
  `README:159`, `quickstart:47` and `quickstart:258` all credit `idea doctor`
  with detecting a missing key, which it cannot (it reads the CLI's environment,
  not the daemon's).
- **Gap G1** — escaping `needs_human_judgment` is undocumented for anyone
  without a built web UI. `quickstart.md:150-155` covers the M1 path; the M2
  plan-review path and the verify-regression path appear in no document.
- **Gap G2** — `idea retry` is never explained. Both places that describe an
  initiative going to `failed` omit the recovery command.

The systemic blind spot: **not one guard executes a documented command and
compares its stdout to the fenced block beside it.** The full greenfield sequence
runs in 5 seconds on the demo provider — cheap enough for every push.

### Release and compatibility
FAIL for a release cut from `a3f79f5`.

- **R-5 (P0)** — `UNDOCUMENTED_BREAKING_API`. `[Unreleased]` lists three items;
  four user-visible contract changes from `df6d56d` and `a3f79f5` are absent:
  `why|impact|trace` unknown type 200→400, `suggest-answers` 202→404/409,
  `doctor` exit 0→non-zero, and the `daemon stop` message. Three are the exact
  classes `v0.2.0` explicitly labelled BREAKING.
- **R-4 (P1)** — upgrading the binary leaves the old daemon serving every
  command, and `idea doctor` affirms health. `authAccepted()`
  (`client.go:223`) already fetches `/api/v1/version` and discards the body;
  `cmdDoctor` prints the CLI's own `buildinfo.Get()`. No doc anywhere says to
  restart the daemon after an upgrade.
- **R-2 (P2)** — `openapi.yaml` `info.version` has been `0.1.0` across all three
  releases, spanning an enum change, ten path renames and ten null→[] changes.
  `docs/api-reference-hosting.md:143` instructs a bump at every tag; it has been
  missed three times.
- **R-8 (P2)** — `release.yml` has no `needs:` on `ci`, runs `go test` on
  `ubuntu-latest` only, and runs none of the seven guards. A red macOS job or a
  red guard would not stop artifacts being uploaded.
- **R-6/R-7 (P2)** — no documented way to install a release artifact, no
  checksum-verification instruction, no signing, no attestations, no SBOM.

Genuinely strong: version stamping is correct on all three build paths and the
`resolveVersion` fix is real; the `v0.3.0` artifact is reproducible from its tag
and its checksum matches; the CHANGELOG's claims for shipped tags survived
mechanical verification against the diffs; migrations are append-only and
byte-identical since `v0.1.0`; the platform-support table is exactly right.

---

## Per-stage timing (journey manifest, demo provider)

14/14 steps PASS. `find` 0.00s · `understand` 0.00s · `install` 0.02s ·
`auth` 0.02s · `configure` 0.14s · `execute` 1.79s · `verify` 1.52s ·
`modify` 1.04s · `break` 0.03s · `diagnose` 1.47s · `recover` 1.39s ·
`test` 24.27s · `deploy` 0.00s · `upgrade` 0.00s. Magic-path span 3.49s,
7 commands, 0 credentials, 1 context switch. Observed.

This is the manifest passing cleanly while P0-1 sits outside it — the manifest
runs the demo provider, like everything else.

---

## Scores

| Area | Weight | Score | Evidence |
|---|---:|---:|---|
| Time to first value | 20 | 45 | Observed |
| API | 15 | 64 | Observed |
| SDK | 10 | 35 | Estimated (no SDK; scored on generation-readiness) |
| CLI/config | 10 | 52 | Observed |
| Errors/recovery | 12 | 45 | Observed |
| Documentation | 10 | 58 | Observed |
| Local dev | 8 | 86 | Observed |
| Testing/quality | 8 | 66 | Observed + CI-observed |
| Release/compatibility | 7 | 55 | Observed |

**Overall DX = 55.**

## World-class checklist

| # | Criterion | Result |
|---|---|---|
| 1 | All hard gates pass | **No** — magic path fails |
| 2 | Overall DX ≥ 85 | No — 55 |
| 3 | Every area ≥ 75 | No — one area (local dev, 86) |
| 4 | No unresolved P0/P1 | No |
| 5 | Magic-path timing Observed ≤ 15 min | Partially — no-key yes, keyed never completes |

---

## Backlog by problem class

| Class | Items |
|---|---|
| Onboarding and setup friction | P0-1 (keyed quickstart), G1, `idea init` silent hang |
| Error and recovery quality | P0-2 (A3), doctor green during four failures, D1 deletion case, retry exit 0, verify read-path |
| API and contract design | F-1, F-13, F-12, F-3, F-10, pagination, idempotency, deprecation policy |
| CLI and configuration | D5, `idea why` usage string, `idea docs ad`, `--json` failure shapes, exit-code table |
| Discovery and findability | `idea explain` and `idea reviews` unreachable from any pointer |
| Testing and quality story | demo reviewer as happy-path oracle; no ledger for audit 5; no stdout-vs-doc guard |
| Release and compatibility | R-5, R-4, R-2, R-8, R-6, R-7 |
| Local development | no pre-push hook (local-vs-CI divergence) |
| SDK quality | none exists; 403/405 undeclared and `Operation.type` untyped block clean generation |

## Recommended order

1. **P0-2 / A3** — smallest fix, largest TTR change, and it is an unrecorded
   audit-5 P0.
2. **P0-1** — CLI verbs for the human-judgment surface (`approve`, `revise`,
   `revise-plan`, question/finding resolution), and correct the pointer in
   `reportNextAction` from `idea decisions` to what actually holds the content.
3. **The oracle** — give the demo provider a review path that can block, so
   `needs_human_judgment` becomes reachable by CI, `smoke.sh` and every future
   audit.
4. **F-1** — guard `approve`/`resume` behind the claim; they break the invariant
   an entire test file exists to protect.
5. **R-5 + the audit-5/6 doc-defects ledgers** — the changelog entries, and the
   ledger rows that would have caught E1/E4.
6. Everything else, by class above.
