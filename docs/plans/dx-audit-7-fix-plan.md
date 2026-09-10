# DX Audit 7 — fix plan (P0-1, P0-2)

Audit 7 returned **FAIL at 49/100** with the magic-path gate broken. This plan
covers the two P0s and the read-path defect that shares a function with P0-2.

## What the plan review rejected

This plan was reviewed adversarially before implementation and **rejected**. The
first draft would have failed every initiative it touched. Recording the
rejections rather than quietly editing, because they are the useful part:

| # | First draft said | Reality | Now |
|---|---|---|---|
| D1 | `ApprovePlan` CASes from `{needs_human_judgment, plan_approved}`, "mirroring `Engine.Approve` line for line" | **False, and catastrophic.** `Runtime.ApprovePlanInitiative` claims the initiative to `running` (`runtime.go:1166`) *before* `launchOperation` (`:1194`), so the engine always observes `running`. The CAS matches zero rows → `ErrInitiativeNotClaimable` → `failOperation(..., failInitiative: true)`. `approve-plan` would mark every initiative **`failed`**, findings undisposed. `Engine.Approve` escapes this only because `Runtime.ApproveInitiative` (`:525-543`) takes no claim and launches no operation — it runs on the request goroutine | The claim at `:1166` **already is** the CAS this asked for, with exactly that `from` set. The engine writes unconditionally |
| D2 | A `internal/workflow` acceptance test | **Vacuous against D1.** That path never claims, so the broken CAS would have matched and the test gone green while production failed 100% | A `internal/daemon` test through `ApprovePlanInitiative` is now the primary one |
| D5 | The reason gate "is the precondition that makes step 1 safe" | Silent about the **deterministic** plan checks the fix removes. `deterministicPlanChecks` (`advance.go:425`) runs inside `runExecutionPlanReview`, so a `critical` "task dependency cycle detected" finding (*"the plan cannot be scheduled"*) is on the record at the park, under reason `plan_review`, which the gate allows. One `approve-plan` would disposition it and set `plan_approved` — trading the live-lock for a Build that `nextReadyTask` can never satisfy | A deterministic-findings gate, below |
| D3 | Layer 2's CAS `from` = `{approved, plan_approved, needs_human_judgment, complete}` | Breaks **five** tests. `CreateInitiative` defaults `Status` to `running` (`store/repo.go:41-43`) and no `verify_test.go` fixture ever sets one — including `TestWaiveRequirementExcludedFromGate`, which the draft named three times as the untouched A2 regression guard | `running` is in the `from` set; only `failed` is excluded |
| D4 | Layer 1 refuses `running` | Breaks the documented sequence. `idea apply` does not wait (`cli/change.go:218`), and the initiative reads `running` until `release()` at `runtime.go:780` — after the change set reads `applied`. `smoke.sh:477` polls the *change set* and then runs `idea verify`, so it would 409 intermittently; `demo-greenfield.md:116-135` documents exactly that sequence and `:148` says verify "is usable in a script" | The `running` clause is dropped. Only `failed` is refused |
| D6 | "Guard the gate on `len(active) > 0`" and, four lines later, "`len(reqs) > 0`" | Self-contradictory, and neither closes the hole. `Engine.Revise` calls `SupersedeRequirements` (`engine.go:872`) — every requirement `removed` — *before* `runProductDefinition`. If that model call fails, `len(reqs) == N > 0`, the loop `continue`s all N as removed, and `complete` is written over `failed` one revise cycle later | One predicate: **at least one non-`removed` requirement** |
| D10 | Listed `docs/cli.md` | Missed `internal/cli/testdata/help.txt:18`, `docs/api.md:231`, the openapi verify description, `precondition.go:22-26` + `precondition_test.go:145` (which name `VerifyInitiative` as a path that must **not** carry the precondition type), and `cli/next.go:276-292`'s justification comment. It also **miscited `openapi.yaml:2876`** as evidence for the all-waived flow — that line is `status: {$ref: …}` in the `Initiative` schema. The real reference is `openapi.yaml:2946-2950`. A miscitation, in a plan whose Ledger section is about citations not matching claims | All enumerated below |
| D11 | `### Changed — BREAKING` for P0-1 only | Three breaking changes, not one | All three assigned |
| D12 | `Runtime.VerifyInitiative` | Collides with `Engine.VerifyInitiative`, and `conflictcheck.go:88-90` matches precondition methods by **bare selector name** | `Runtime.RunVerificationGate` |

Two things the review tried to break and could not, so they stand as written:
`writeErr` promotes a `*PreconditionError` to 409 regardless of the status
argument (`errors.go:52-60`, pinned by `TestPreconditionErrorMapsToConflict`),
so `handleVerify`'s existing `http.StatusBadRequest` is harmless; and
`FakeProvider.Calls()` + `Request.JSONSchemaName` (`fake.go:105,132`) already
supports the per-schema call count, with no new helper.

---

## P0-1 — `approve-plan` is a retry, not an accept

### The defect

`internal/workflow/advance.go:78-90`. `Engine.ApprovePlan` dispositions the open
findings as `accepted_risk` and then calls `runExecutionPlanReview`, which runs
a fresh adversarial review and parks again on any critical/high. A model
reviewer is not a fixed point, so this is a retry. `revise-plan` re-enters the
same function after regenerating. There is no third command, and the web UI
posts to the same endpoint.

### The fix

**1. `Engine.ApprovePlan` becomes a terminal accept.**

```go
if err := e.db.DispositionOpenFindings(ctx, initiativeID, domain.DispositionAcceptedRisk, store.HumanActor); err != nil {
    return "", err
}
if err := e.db.SetInitiativeStatus(ctx, initiativeID, domain.InitiativePlanApproved, store.HumanActor); err != nil {
    return "", err
}
return domain.InitiativePlanApproved, nil
```

Unconditional, not a CAS — per D1, the claim at `runtime.go:1166` is the CAS,
and this runs while the status is `running`. Disposition **before** the status
write, so a failure between them leaves the initiative claimed and parked rather
than `plan_approved` with open findings wedging the completion gate. This is the
same order `runExecutionPlanReview:412-419` already uses.

**2. A deterministic-findings gate in `Runtime.ApprovePlanInitiative`** (D5),
synchronous, alongside the three that are already there:

```go
// A human can accept an adversarial reviewer's judgment. A human cannot accept
// a dependency cycle: `detectCycle`'s own finding says "the plan cannot be
// scheduled", and nextReadyTask (execute.go:107) will never yield a task whose
// predecessors can never apply. Approving one would trade the live-lock for a
// Build that no command can advance. Deterministic findings are machine-checked
// facts, not judgment calls, and only `revise-plan` clears them.
```

Refuse with `preconditionf` when any **undisposed** finding on the initiative
has `ReviewRunID == "deterministic-plan-check"` and severity critical or high,
naming `idea revise-plan` as the resolution. `deterministicPlanChecks` emits
exactly two: the `critical` cycle and the `high` untraced-task finding
(`advance.go:444-462`).

**3. Narrow the status gate to `needs_human_judgment`** (D8). Both the read
gate at `runtime.go:1137` and the claim at `:1166` currently accept
`plan_approved`. `TransitionInitiative` clears `judgment_reason`
(`store/repo.go:225-231`), so a `plan_approved` initiative has `""`, which the
reason gate allows — and the web UI's commit bar renders in **every** status
(audit 7 P1-5, `PlanPage.tsx:143`). Today a stray click there re-runs a review;
after step 1 it would silently disposition every open finding as a human's
`accepted_risk` with no review and no confirmation. Refusing `plan_approved`
with `preconditionf("this plan is already approved")` turns P1-5's dead click
into a clean 409 instead of a forged signature, and makes the idempotency
explicit rather than silent.

**What does not change.** The running, reason and change-set gates stay exactly
as they are. `revise-plan` is untouched — regenerating and re-reviewing is what
it is for.

**Known hole, stated rather than claimed away** (D9). The reason gate accepts
`""`, and migration 18 backfilled every pre-existing row with `''`
(`store/migrate.go:492`). An initiative parked on `plan_dependencies` before
that migration is exempt from the reason gate. This is pre-existing and the fix
does not worsen it — `runExecutionPlanReview` does not re-run `runExecutionPlan`
(`advance.go:363-380` creates the `plan_dependency` finding; `:391-421` does
not), so today it converges to `plan_approved` over the incomplete graph anyway.
The new deterministic gate does not cover it either, because that finding is not
tagged `deterministic-plan-check`. Recorded in `deferred.md`.

### Acceptance tests

**Primary, `internal/daemon`** (D2) — through `Runtime.ApprovePlanInitiative`,
which is the only path that reproduces the claim:

```go
fp.SetResponse("plan_review", model.DemoBlockingReviewJSON) // never converges
```

`SetResponse` is the persistent default. `EnqueueResponse` — which
`demoProvider` uses — is a finite queue, and that finiteness is exactly why six
audits, CI and smoke could not see this: `smoke.sh:462` records in its own
comment that `approve-plan` succeeds there because "the queue is drained by
now". A test built on `EnqueueResponse` reproduces that blindness.

Drive to the plan-review park, call `ApprovePlanInitiative`, await the
operation, then assert the initiative status is **`plan_approved`** and not
`failed`. Mutation: restoring the first draft's CAS must fail this and only
this.

**Secondary, `internal/workflow`** — `ApprovePlan` leaves no undisposed finding,
and the `plan_review` schema call count from `fp.Calls()` is **unchanged**
across the call. The call-count assertion is what makes it non-vacuous: without
it a fix that got lucky on one re-run passes.

**Gate, `internal/daemon`** — an initiative parked with a deterministic
`critical` cycle finding answers **409** to `approve-plan` and names
`revise-plan`; status unchanged. Mutation: removing the gate must fail it.

**Gate, `internal/daemon`** — `approve-plan` on a `plan_approved` initiative
answers 409 and disposes nothing.

### Smoke

Add a second plan-judgment pass with `block_plan_review_cycles: 50` and
**`dangle_plan_dependency` off** (D12 — with it on, the initiative parks on
`plan_dependencies` and never reaches the review the pass exists to test),
asserting `approve-plan` reaches `plan_approved` and then `complete`. The
existing pass stays: it covers the disposition side-effect on the converging
path, a different property.

---

## P0-2 — `idea verify` overwrites a failed initiative with `complete`

### The defect

`internal/workflow/verify.go:167-238`. With zero requirements the loop never
executes, `allVerified` stays `true`, `hasOpenBlockingFindings` is false, and
`SetInitiativeStatus(..., InitiativeComplete, ...)` runs unconditionally — over
`failed`. `handleVerify` (`handlers.go:1904-1949`) calls the engine directly
with no state precondition, which is why `verifyInitiative` declares no 409.

### The fix, in three layers

**1. A synchronous `failed` gate, in a new `Runtime.RunVerificationGate`** (D12
naming). Only `failed` is refused (D4 — the `running` clause is dropped because
`idea apply` does not wait and the documented apply→verify sequence runs inside
that window):

```go
if ini.Status == domain.InitiativeFailed {
    return preconditionf("cannot verify a failed initiative; `idea retry` re-runs it")
}
```

`needs_human_judgment` stays allowed — `JudgmentVerification` parks there and
`idea verify` is its documented escape. `handleVerify` calls the Runtime and
stops calling the engine directly.

This inverts a pinned invariant and must say so (D10): `precondition.go:22-26`
names `VerifyInitiative` among the paths that must **not** carry the
precondition type, and `precondition_test.go:145` pins it. Both change, and the
comment must record why the enumeration moved rather than being widened.

Declare `409` on `verifyInitiative` in `docs/openapi.yaml`; `api-check`'s
conflict axis then requires it.

**2. A compare-and-swap on the completion write.** Replace the terminal
`SetInitiativeStatus(..., InitiativeComplete, ...)` with
`TransitionInitiative(..., InitiativeComplete, ..., InitiativeRunning,
InitiativeApproved, InitiativePlanApproved, InitiativeNeedsHumanJudgment,
InitiativeComplete)` — every state except `failed` (D3: `running` is in the set
because `CreateInitiative` defaults to it and five existing tests rely on that).
Layer 1 refuses the request; this refuses the *write*, from any future caller.

`ErrInitiativeNotClaimable` must be mapped, not leaked (D7): `handleVerify`
would otherwise answer `400: store: initiative is not in a claimable state`.
With layer 1 in place the only reachable trigger is a race, so map it to a
domain message and return it as a precondition.

**3. Zero *active* requirements is not "everything verified."** Guard the
completion conclusion on **at least one non-`removed` requirement** (D6). Not
`len(reqs)`, which the all-removed case defeats; not "active excluding waived",
which would kill the all-waived flow.

This is audit 5's **A2**, *rejected on review* and recorded at `deferred.md:37`.
That rejection was correct and this does not reopen it:

| A2 proposed | Why rejected | This plan |
|---|---|---|
| Require a change set before `complete` | Kills the documented all-waived flow (`verify_test.go:357`, `openapi.yaml:2946-2950`, `ui.md:227`) | Not proposed; change sets are not consulted |
| Predicate `{applied, reverted}` from `ui.md:101` | `rejected`/`superseded` are absorbing states minted on ordinary paths | Not used |
| — | `VerifyInitiative` has no channel to report a refusal | Layers 1 and 2 supply it: a 409, and a write refusal |

All-waived has waived requirements, which are not `removed`, so it passes the
guard, reaches the loop, and still concludes `allVerified`. Zero-requirements
and all-removed are refused. The rejected predicate conflated these; this one
separates them.

### Acceptance tests

- `internal/daemon`: `POST /verify` on a `failed` initiative → **409**, status
  still `failed`. Mutation: removing the `preconditionf` must fail it.
- `internal/daemon`: `POST /verify` during an in-flight apply (status `running`)
  still succeeds — the D4 regression guard.
- `internal/workflow`: zero requirements → no `complete`. All requirements
  `removed` → no `complete`. Mutation: removing the guard must fail both.
- `internal/workflow`: `TestWaiveRequirementExcludedFromGate`
  (`verify_test.go:357`) stays green **untouched** — the A2 regression guard.
  If any layer requires editing it, that layer is wrong.
- `internal/workflow`: `TransitionInitiative` `failed` → `complete` is refused.
  Mutation: reverting to `SetInitiativeStatus` must fail it.

---

## P0-2b — `idea verify <req>` discards its argument on the read path

`internal/cli/change.go:432-440`. The switch has branches only for
`req != "" && (pass|fail|waive)`, so with a requirement and no disposition flag
`body` stays `{}` and the argument is dropped: `idea verify REQ-99` on a project
holding only REQ-1 reports `REQ-1 0/1 verified` and never mentions REQ-99. The
write path is correct — `idea verify REQ-99 --pass` exits 1 with `no requirement
matching "REQ-99"` — so the validation exists and is applied to the opposite
case from the one audit 6 reported.

**Fix.** Send `{"requirement": req}` with no disposition, and have the handler
resolve it and 400 with the same message the write path produces. Coverage stays
initiative-wide; the defect is the silent discard, not the breadth of the
report.

**Acceptance test.** `idea verify REQ-99` on a project with only REQ-1 exits
non-zero and names REQ-99. Mutation: restoring the empty body must fail it.

---

## Artifacts that change (D10)

| File | Why |
|---|---|
| `internal/cli/cli.go:117` | summary → `accept the execution plan as it stands` |
| `internal/cli/testdata/help.txt:18` | golden file, fails otherwise |
| `docs/cli.md` | generated from the summary; `make docs-check` |
| `internal/cli/next.go:276-293` | the advice **and** the ~17-line comment above it, which asserts "`ApprovePlan` does not approve anything" and "blocking findings stay undisposed" — both made false |
| `internal/projection/projection.go:308` | the `JudgmentPlanReview` guidance says the same thing |
| `docs/api.md:231` | "(re-runs its review)" becomes false — `STALE_PUBLIC_REFERENCE` class |
| `docs/openapi.yaml` | `approvePlan` description; `verifyInitiative` gains 409; the verify description's "`{}` just runs the gate" and its requirement/disposition wording, contradicted by P0-2b |
| `internal/daemon/precondition.go:22-26`, `precondition_test.go:145` | the enumeration that names `VerifyInitiative` |
| `docs/plans/deferred.md` | the empty-`judgment_reason` hole (D9) |

## CHANGELOG — three breaking changes, not one (D11)

All three under `### Changed — BREAKING`, each with Was/Now and consumer impact.
Audit 7 faulted this round for filing `approve-plan`'s prior 202→409 under
`### Fixed`; repeating that here would be the same defect.

1. `POST …/approve-plan` accepts instead of re-reviewing, and refuses
   `plan_approved` (was 202, now 409).
2. `POST …/verify` answers 409 on a `failed` initiative — an endpoint that has
   never returned one.
3. `idea verify REQ-99` exits 1 instead of 0.

## Ledger

Every row carries **both** directions: a `present:` predicate asserting the
false claim is gone **and** an `absent:` predicate asserting the correct claim
is now stated. Three of audit 6's rows were green over replacement text that was
itself false because they carried only the `present:` half.

## Order

1. **P0-1's daemon acceptance test**, merged red.
2. **P0-1**: engine accept, deterministic gate, status narrowing, then the six
   artifacts and the smoke pass.
3. **P0-2** layers 1 → 2 → 3, each with its own test and mutation check.
4. **P0-2b**.

Each step: mutation-checked tests, adversarial review before merge, merge on
green.
