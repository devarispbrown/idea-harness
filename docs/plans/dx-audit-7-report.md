# DX Audit 7 — report

**Revision:** `213e186` (`main`, tree clean before and after, CI green)
**Date:** 2026-09-09
**Environment:** macOS 25.5.0 (darwin/arm64), Go toolchain from `go.mod`, cold
`GOPATH`/`GOCACHE` under a scratch dir, isolated `IDEA_HARNESS_STATE` and a
private port per run, provider keys unset except where a keyed pass says so.
**Journey scope:** all 14 stages — 7 core, 7 optional. Manifest dry-run first,
then `--execute --scope all` with an explicit `--cwd`.

---

## Verdict

**Release gate: FAIL. Overall DX = 49/100** (Observed), down from audit 6's 55.

**The score fell and the tree improved.** Every audit-6 item I re-tested that was
claimed fixed *is* fixed, and several are fixed well:

| Audit 6 item | Re-tested | Result |
|---|---|---|
| P0-1, the PRD review with no accept | keyed guided run ×3 | **Fixed.** `idea approve` clears it, first try, every time |
| P0-2, `idea next` calling a failed initiative "running" | fresh 401 failure | **Fixed.** `action: "failed"`, exit 1, reason rendered verbatim |
| D8, `doctor` reading the CLI's environment | all three key states | **Fixed.** Including the subtle no-daemon case |
| G7, stale-daemon detection | `-ldflags` v9.9.9 CLI vs `dev` daemon | **Fixed.** Names both builds, exit 1 |
| D3, D5a–c, D11–D15, the eight demo-guide blocks | both guides re-run end to end | **Fixed.** Byte-for-byte against real stdout |
| Every `[Unreleased]` breaking claim | reproduced live against a running daemon | **All true.** Zero false claims — a first for this repo |

The score moved because audit 7 probed four axes no previous round had: the
**plan half** of the loop with a real model, **non-GET** API operations,
**query-parameter and body ids**, and **state guards on synchronous writes**.
Two P0s and six P1s live in that space. This is the audit-5 → audit-6 pattern
repeating: measurement deepened, the number fell, and the number is honest.

---

## Hard gates

| Gate | Result | Evidence |
|---|---|---|
| Magic path (`MAGIC_PATH_MAX_MIN` = 15) | **FAIL** | keyed literal-README 0/4; keyed guided 0/3 (27 commands, 111–148s, never `complete`); no-key demo path 10/10 at ~2s; journey manifest span 3.65s. Observed |
| Local development (`LOCAL_DEV_MAX_MIN` = 10) | **PASS** | clean clone → `make build` 16s → `make test` 17s → **33s**, 22 packages `ok`. Observed |

**Named gates fired:** `BROKEN_QUICKSTART`, `UNEXPLAINED_ERROR` (×6),
`STALE_PUBLIC_REFERENCE`, `SDK_API_DRIFT`, `UNTESTED_SUPPORTED_VERSION` (narrow).
**Not fired:** `BROKEN_CANONICAL_INSTALL` (`go install ./cmd/idea` in 0.7s,
`dev (213e186)` truthful; all three tags' checksums verify),
`NON_REPRODUCIBLE_BUILD`, `UNSAFE_EXAMPLES`, `UNDOCUMENTED_BREAKING_API`.

`MAGIC_PATH_MAX_COMMANDS` 7/8 PASS · `MAGIC_PATH_MAX_CREDENTIALS` 0/2 PASS ·
`MAGIC_PATH_MAX_CONTEXT_SWITCHES` 1/4 PASS — all on the no-key path, which is
the only path that completes.

---

## P0-1 — the plan review is a live-lock; `approve-plan` is a retry, not an accept

`internal/workflow/advance.go:388`. `runExecutionPlanReview` evaluates
`review.EvaluateApproval(all)` over the findings **this run** produced. Any
critical or high records `JudgmentPlanReview` and parks. There is no cycle
counter, no bound, and no accept.

Both commands the product offers are re-entries into the same function:

- `idea revise-plan` regenerates the plan, then re-reviews.
- `idea approve-plan` (`advance.go:78`) dispositions the open findings as
  `accepted_risk` and then **re-runs the review**, producing fresh findings.

Contrast the PRD half, which audit 6 genuinely fixed: `runReviewLoop`
(`engine.go:746`) bounds itself at `review.max_cycles`, and `Engine.Approve`
(`engine.go:898`) sets `approved` and dispositions **without re-running**. That
is a real terminal accept. The plan half has the park but not the accept.
`docs/configuration.md:142` states the root cause plainly and treats it as a
design fact: *"The M2 plan review is a single pass and does not consult this
field."*

Dispositioning is not an escape from any surface. The gate reads the current
run's findings, never the stored ones, so `POST …/findings/{id}/disposition`
cannot affect it — and `web/src/PlanPage.tsx:115` posts to the same
`approve-plan` endpoint the CLI does.

### Reproduction — deterministic, no API key

```bash
idea init && idea daemon stop
cat >> .idea-harness/harness.yaml <<'YML'
demo:
  block_plan_review_cycles: 50
YML
idea new "Build usage-based billing"
idea next --until-human      # → "the plan review raised blocking findings"
idea revise-plan  ; idea next --until-human   # same
idea approve-plan ; idea next --until-human   # same
```

Observed: 6 `next --until-human` iterations interleaved with `revise-plan`, then
3 bare `approve-plan` calls. Every one returned to the same state.
`plan_approved` was never reached.

**With a real model** (`openai/gpt-4o-mini`, guided path, 3/3 runs): PRD review
blocked once, `idea approve` cleared it first try, then the plan review blocked
**11 consecutive times** across `revise-plan` regenerations, plus 5 further bare
`approve-plan` calls. 27 commands, 111–148s, never `complete`.

### Supporting observations

| # | Observation | Evidence |
|---|---|---|
| a | Every escape a stuck user would try is refused correctly and exits 1: `idea approve` → *"approve accepts a PRD waiting on your judgment; this initiative is waiting on plan_review"*; `idea advance` → *"advance requires an approved PRD (status=needs_human_judgment)"*. The messages are good. No command works | Observed |
| b | `idea approve-plan` exits **0** printing `approving the plan…` while achieving nothing | Observed |
| c | `idea next --until-human` exits **0** in a state it cannot leave, so a script wrapping the documented loop spins at exit 0 | Observed |
| d | Findings accumulate without bound — one duplicate `high` per blocked cycle; `idea reviews` listed 11 after 11 cycles | Observed |
| e | The help text is honest — *"approve-plan  re-run the plan review (it can block again)"* — documenting the dead end rather than removing it | Observed |

### Why nothing caught it — the oracle blind spot, one stage later

`scripts/smoke.sh:462` says it outright:

> approve-plan re-runs the review; **on the demo provider the queue is drained by
> now so it converges** and sets plan_approved.

`demoProvider` (`runtime.go:1516`) primes a **finite** `EnqueueResponse` queue
from `demo.block_plan_review_cycles`. In every test, every smoke pass and every
audit, `approve-plan` succeeding is attributable to the queue running out — not
to `approve-plan` doing anything. Audit 6 fixed the oracle by teaching it to
block *N* times; a **converging** blocker proves the park state is reachable but
can never prove the escape exists, because the escape is indistinguishable from
the queue draining. The one test that drives `RevisePlan`
(`revise_plan_test.go:37`) accepts `InitiativePlanApproved` **or**
`InitiativeNeedsHumanJudgment`, so it cannot fail on the live-lock either.

### Acceptance test

`model.FakeProvider.SetResponse` is the **persistent** default, unlike the finite
`EnqueueResponse` queue. It already exists and has never been used this way:

```go
fp.SetResponse("plan_review", model.DemoBlockingReviewJSON) // never converges
```

Assert that a bounded sequence of CLI-reachable commands reaches
`domain.InitiativePlanApproved`. It fails on `213e186`. Mutation check:
reverting `ApprovePlan` to a re-run must fail the new test.

---

## P0-2 — a bare `idea verify` rewrites a failed initiative to `complete`

Reproduced from scratch, real 401, zero requirements, zero tasks, zero change
sets:

```
$ idea status --json   → [{"title":"a tiny url shortener","status":"failed"}]
$ idea verify          # no arguments, no flags
✓ verified status: complete
$ idea status --json   → [{"title":"a tiny url shortener","status":"complete"}]
$ idea status
a tiny url shortener  ● complete
  → Nothing to do — this initiative is complete
    Verification passed and the work is done.
```

One argumentless command on the obvious recovery path reports success, exits 0,
moves a terminal state to `complete`, and discards the recorded
`failure_reason`. `handleVerify` calls `RecordHumanEvidence` and
`VerifyInitiative` **synchronously with no running check, no claim and no state
precondition** — which is why `verifyInitiative` declares no 409 and the
conflict axis correctly agrees there is nothing to declare.
`POST …/requirements/{reqId}/verification-task` has the same shape: it minted
`TASK-2` on a failed initiative, 200.

This is audit 5's **A2**, rejected on review and recorded at
`deferred.md:37` with the rationale *"The false success this was aimed at is
fixed at its cause instead (audit 5 / A1)."* A1 made `idea new` wait for its
run. It did not close this. The specific gate A2 proposed was correctly
rejected — it would have broken the documented all-waived flow — but the class
it was aimed at is open, and the defect has acquired a strictly worse form: it
no longer merely completes an empty initiative, it **overwrites a failure**.

**Acceptance test.** `POST /verify` on a `failed` or `running` initiative answers
409 and leaves `status` unchanged; `idea verify` on a failed initiative exits
non-zero and names the failure. Mutation: removing the guard must fail it.

### A disagreement with the errors/TTR dive, reported not smoothed

That dive filed a P0 that a failed initiative reports `status: "complete"` to
`--json` and the API. **It does not reproduce.** On a fresh 401 failure,
`idea status --json`, `GET /initiatives/{id}` and `idea explain` all report
`failed`, and `idea next` exits 1. Its evidence compared two different
initiatives, and the `complete` it saw was produced by its own earlier verify
probe. The observation was real; the attribution was wrong. The defect is P0-2
above.

---

## P1 findings

| # | Finding | Class | Evidence |
|---|---|---|---|
| P1-1 | `idea new` prints `✓ reviewed PRD ready` while the initiative is `● needs your judgment`. `cmdNew` (`cli.go:526`) prints it unconditionally on operation success and never reads the terminal status. Reproduced with `demo.block_review_cycles: 3`. Same class audit 6 fixed one command over (`cli.go:505`), on the path README:179 tells you to expect with a real model. `smoke.sh:291` runs `idea new` in exactly this state and asserts nothing on its output | CLI/config | Observed |
| P1-2 | `POST /verify` and `/verification-task` have no state guard — see P0-2 | API | Observed |
| P1-3 | `POST /revise-plan` answers **202** for a state it refuses, then sets a healthy `plan_approved` initiative to `failed` asynchronously. `ApprovePlanInitiative`'s change-set guard carries the comment *"Matching RevisePlan and ReopenPhase"* — `RevisePlanInitiative` has no such guard. The identical request 409s on one endpoint and 202s-then-destroys on the other | API | Observed |
| P1-4 | `PUT /providers` silently drops an inline `api_key`, answers `200 {"status":"updated"}`, writes an all-empty provider entry, makes it the resolved default, and flips `demo` from true to false — after which real billed `gpt-4o` calls follow. No `DisallowUnknownFields` anywhere in `internal/daemon`; `ValidateProvider` returns `nil` immediately when `BaseURL == ""`. The spec says inline keys are *rejected*; they are ignored | API | Observed |
| P1-5 | `SDK_API_DRIFT`. `judgment_reason` appears nowhere in `web/src/`. `phases.ts:117` still disambiguates PRD-blocked from plan-blocked by execution-plan **artifact presence** — verbatim the inference `[Unreleased]` documents as wrong. `PlanPage.tsx:143` builds the commit bar unconditionally, so **"Approve plan →" renders in every status**; `approve` at `:114` has no `try/catch` while `api.ts:19` throws on non-2xx. For any initiative that has reached Build the Plan page's primary CTA is a silent dead click, and the 409 that made it dead was added this round | SDK quality | Observed |
| P1-6 | `docs/cli.md:100–108` documents that every initiative-scoped command takes an optional initiative reference and defaults to the newest, using `idea status` as its worked example. `cmdStatus` never calls `resolveInitiative` and never reads a positional: with two initiatives, `idea status`, `idea status <id>` and `idea status ZZZZnonexistent` all print both and exit 0 — contradicting `cli.md:110`'s promise that unknown arguments are an error. Ledger row **G3**, marked `fixed audit-6`, predicate green | Documentation | Observed |
| P1-7 | `docs/quickstart.md:278` is a fenced `idea doctor` output block whose third line the binary does not print, and whose invented remedy is unperformable: `idea model set` requires `<provider/model>` and **no CLI verb clears `default_model`**. Ledger row **D9**, marked `fixed audit-6`, predicate green. `STALE_PUBLIC_REFERENCE` | Documentation | Observed |
| P1-8 | `release.yml` gates a tag on `go vet && go test` on `ubuntu-latest` only: **none** of the eight guards, no `scripts/smoke.sh`, no macOS job, no web tests, no `needs:` on `ci`. A red `enum-check` or `check-embed` ships. **R-8, unfixed for a fourth round, with no `deferred.md` row.** Fix is `needs: [compile-no-node, backend, macos, web]` plus reusing the guard steps | Release | Observed |

---

## The audit-method finding: a green ledger row is not evidence of a correct fix

Audit 6 found that eight of audit 5's "fixed" items were not fixed, and the
remedy was this ledger with mechanically-checked predicates. `make ledger-check`
now reports **92 rows, 88 mechanical, 4 manual, exit 0**, and all eight guards
are green. Re-testing every `fixed audit-6` row against the binary rather than
the prose:

| Row | State | Reality |
|---|---|---|
| D3, D5a–c, D11–D15, D8, G7, G2, G4, D10, D1/D2, E1, E4, E5, G6 | green | **Genuinely fixed**, each re-executed |
| **D7** | green | Replacement text is a *different* false claim: `ui.md:266` says demo mode is *"`default_model` does not resolve to a registered provider"*; the predicate is `defaultProviderName(cfg) == demoProviderName` — no `provider/` prefix **and** no `providers:` entries. All six presets are always registered, so "registered provider" is not the axis; `default_model: bogus/x` errors rather than falling through |
| **G3** | green | P1-6 above |
| **D9** | green | P1-7 above |
| **A13** | green | Overstated. *"an unknown id is 404 on all of them"* — 13 of 15 probed endpoints 404; `POST …/retry` and `POST …/decisions/{id}/resolve` return 400 before checking the id |
| **D8d** | green | Predicate does not test its row's claim. `absent:web/src/Settings.tsx:providerStatusLabel` passes on the *import line*; the row's claim is about `docs/security.md:20`, which the predicate never touches. The behaviour is nonetheless fixed |
| **G5** | `manual:` | Honest but narrower than it reads: seven hard-coded `grep -qF` substrings in `smoke.sh:378`, typed into the script rather than captured from stdout — the same two-place drift the guard exists to prevent |

**The structural cause.** A `present:` predicate proves a specific false sentence
is *gone*. It cannot prove the sentence that replaced it is *true*. Three of ~19
`fixed audit-6` rows changed the words and kept the error. Audit 6's D8 row got
this right by pairing directions — `absent:README.md:asks the **daemon** whether
it has the key` asserts the *correct* claim is now stated. D7, G3 and D9 only
got the `present:` half.

**Remedies, in order of strength:**

1. A `fixed` row must carry **both** a `present:` (the false claim is gone) and
   an `absent:` (the correct claim is stated), or name a test/smoke assertion id
   that exists.
2. `ledger-check` should reject a predicate whose file is not the file the row's
   claim cites — closing D8d's class.
3. Tag documented output fences with the command that produces them and diff
   them in `smoke.sh` under a normaliser. About 15 fences repo-wide.

---

## Per-stage timing (journey manifest, `--scope all`, demo provider)

| Stage | Objective met? | Time | Commands | Credentials | Switches | Errors | Evidence |
|---|---|---|---|---|---|---|---|
| find | yes | 0.00s | 1 | 0 | 0 | 0 | Observed |
| understand | yes | 0.00s | 1 | 0 | 1 | 0 | Observed |
| install | yes | 0.02s | 1 | 0 | 0 | 0 | Observed |
| auth | yes | 0.02s | 1 | 0 | 0 | 0 | Observed |
| configure | yes | 0.13s | 1 | 0 | 0 | 0 | Observed |
| execute | yes | 1.56s | 1 | 0 | 0 | 0 | Observed |
| verify | yes | 1.90s | 1 | 0 | 0 | 0 | Observed |
| modify | yes | 1.05s | 1 | 0 | 0 | 0 | Observed |
| break | yes | 0.03s | 1 | 0 | 0 | 1 (expected) | Observed |
| diagnose | yes | 9.27s | 1 | 0 | 0 | 0 | Observed |
| recover | yes | 2.42s | 1 | 0 | 0 | 0 | Observed |
| test | yes | 15.35s | 1 | 0 | 0 | 0 | Observed |
| deploy | yes | 0.00s | 1 | 0 | 0 | 0 | Observed |
| upgrade | yes | 0.00s | 1 | 0 | 0 | 0 | Observed |

14/14 PASS. **This table measures the demo path only** and is not the magic-path
verdict — the keyed path never reaches `complete`, which is P0-1.

---

## Scores

| Area | Weight | Score | Evidence | Movement |
|---|---:|---:|---|---|
| Time to first value | 20 | 42 | Observed | 45 → 42 · PRD half fixed, plan half live-locks, `idea new` misreports |
| API | 15 | 48 | Observed | 64 → 48 · three P1s found on axes never probed before |
| SDK | 10 | 32 | Estimated (no SDK; scored on the first-party client) | 35 → 32 · `SDK_API_DRIFT` now fires |
| CLI/config | 10 | 44 | Observed | 52 → 44 · P0-2, argument discard, `idea status` positional, `idea config` |
| Errors/recovery | 12 | 42 | Observed | 45 → 42 · doctor's key/skew work is genuinely good; blind on its own trigger |
| Documentation | 10 | 64 | Observed | 58 → 64 · eight demo blocks byte-match; three P1s and three green-but-false rows |
| Local dev | 8 | 80 | Observed | 86 → 80 · 33s clean clone, but CONTRIBUTING omits eight CI-gating targets |
| Testing/quality | 8 | 58 | Observed + CI-observed | 66 → 58 · oracle cannot express a non-converging reviewer; `release.yml` runs no guards |
| Release/compatibility | 7 | 52 | Observed | 55 → 52 · best changelog yet; R-2 and R-8 unfixed for a fourth round |

**Overall DX = 49.**

## World-class checklist

| # | Criterion | Result |
|---|---|---|
| 1 | All hard gates pass | **No** — magic path fails |
| 2 | Overall DX ≥ 85 | No — 49 |
| 3 | Every area ≥ 75 | No — one area (local dev, 80) |
| 4 | No unresolved P0/P1 | No — 2 P0, 8 P1 |
| 5 | Magic-path timing Observed ≤ 15 min | Partially — no-key yes at ~2s, keyed never completes |

---

## Delegated evidence

| Dive | Skill role | Verdict | Re-verified? | Disagreement |
|---|---|---|---|---|
| Documentation | `developer-docs-auditor` (fallback checklist) | FAIL, 3 P1 | Yes — P1-1, P1-6 and P1-7 reproduced live | None |
| Errors / TTR | `error-experience` (fallback checklist) | FAIL, 3 P0 | Yes | **Its P0-2 does not reproduce** — see the disagreement note under P0-2. Its `idea verify REQ-99` and doctor-blindness findings both confirmed |
| API / contract | `api-design-reviewer` (fallback checklist) | FAIL, 3 P1 | Yes — P1-2, P1-3 and P1-4 confirmed by reproduction or code read | None |
| Release / compatibility | `release-guardian` (fallback checklist) | FAIL, 2 P1 | Yes — `SDK_API_DRIFT` confirmed in `PlanPage.tsx`/`api.ts`/`phases.ts` | None |
| `configuration.md` vs code | `developer-docs-auditor` | 1 P1, 5 P2 | Yes — the `revise` verb contradiction and the `max_cycles` clamp gap both confirmed | None |
| `ui.md` vs web source | `developer-docs-auditor` | 3 P2 | Partially — D7 confirmed by code read | None |

Plugin agent types (`developer-docs-skills:*`) are not installed; each dive ran
as a `general-purpose` agent carrying the specialist's embedded fallback
checklist, as the orchestration reference prescribes.

---

## Backlog by problem class

| Class | Items |
|---|---|
| **Onboarding and setup friction** | **P0-1** (plan-review live-lock); P1-1 (`idea new` misreports a parked review); `idea init` with the port held — 1/6 questions and `idea logs` returns an empty file |
| **CLI and configuration** | **P0-2** (`idea verify` overwrites a failure); `idea verify REQ-99` discards its argument on the read path; P1-6 (`idea status` positional); `idea config` shows the model list; `idea watch` has no non-TTY fallback though five commands recommend it; one exit code for everything and no `--json` error object; `idea revise` prescribes `idea reopen-phase`, which is not a CLI verb |
| **API and contract design** | P1-2, P1-3, P1-4; `PUT /providers` can answer 409 and declares none; `GET /api` escapes both auth and the JSON envelope; query-param ids answer `200 []` (5 endpoints); `Operation.type`'s three documented examples are never emitted; no pagination, idempotency keys or ETags; eight list envelope shapes; three incompatible identifier policies |
| **Error and recovery quality** | `doctor` blind to the state-dir wipe it was fixed for (first-command path); `doctor` green on a corrupt store; `idea retry` exits 0 with `✓` when the retry fails instantly; no provider-error classification and no retry-safety signal; a precondition refusal marks the initiative `failed` |
| **Documentation** | P1-6, P1-7; D7's replacement wording; `agents.md`'s three key/pull/digest claims; ten dead model ids across two providers; `configuration.md`'s `revise`-verb self-contradiction; no troubleshooting document; `idea explain` named in no prose |
| **Testing and quality story** | The demo oracle cannot express a non-converging reviewer; `revise_plan_test.go:37` accepts either outcome; `ci-test-summary.py` counts a failure only on a parsed test event, so a panic in the `continue-on-error` macOS job goes green; the route walker cannot reach query-param or body ids; the response-schema test reads only `Get` |
| **Release and compatibility** | P1-8 (R-8); R-2 (`info.version` frozen at `0.1.0` for a fourth release — one shared fix with R-8); `approve-plan`'s 202→409 filed under `### Fixed`; `judgment_reason` absent from `[Unreleased]`; no `deferred.md` row for any release finding |
| **SDK quality** | P1-5; 403/405 declared on zero of 84 operations; `Operation.type` untyped |
| **Local development** | CONTRIBUTING's build-and-test set omits the eight targets CI gates on, so following it exactly produces a red PR |
| **Discovery and findability** | No documented way to delete, archive or reset an initiative or the state directory, and no CLI verb for it |

---

## Recommended order

1. **P0-1**, with the `SetResponse("plan_review", …)` acceptance test written
   first, so the fix is provably not the queue draining.
2. **P0-2**, plus the `idea verify <req>` read-path discard in the same change —
   they are the same function and the same class of silent wrong answer.
3. **P1-8 with R-2** — one shared fix, the two cheapest and longest-lived
   findings in the project, and the reason P1-5 reached `HEAD`.
4. **P1-3, P1-4, P1-2** — the three API state/validation guards.
5. **P1-5**, then **P1-1**.
6. The ledger remedies, before the next round writes another row: paired
   predicates, the file-must-match-the-claim rule, and fence diffing.
7. **P1-6, P1-7** and the rest of the documentation backlog.

Each item: mutation-checked tests, adversarial review before merge, merge on
green, and a ledger row whose predicate asserts the *correct* claim as well as
the absence of the wrong one.
