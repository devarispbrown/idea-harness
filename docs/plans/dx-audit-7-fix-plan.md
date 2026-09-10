# DX Audit 7 — fix plan (P0-1, P0-2)

Audit 7 returned **FAIL at 49/100** with the magic-path gate broken. This plan
covers the two P0s and the read-path defect that shares a function with P0-2.

## What the plan reviews rejected

This plan was reviewed adversarially before implementation and rejected
**twice**. Recording the rejections rather than quietly editing, because they
are the useful part.

### Round 1 — the first draft would have failed every initiative it touched

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

### Round 2 — the revision's four new decisions, three of which were wrong

| # | Revision said | Reality | Now |
|---|---|---|---|
| R1 | Refuse `approve-plan` while an undisposed deterministic finding of severity **critical or high** is open | **Recreates the live-lock.** `deterministicPlanChecks` emits the traceability finding at `high`, once per untraced task (`advance.go:444-453`). A task is untraced whenever the model's `requirement` string misses: `reqByCode[str(t["requirement"])]` returns `""` (`advance.go:346`) and `createTask` then silently skips the edge (`store/graph.go:148`). `requirement` is a bare required string with no enum (`schema/schemas.go:235`), so `REQ-01`, `R1`, or a cross-cutting task all produce one. The gate would refuse `approve-plan` and name `revise-plan`, which re-runs the same unconstrained model call and can miss again. The `detectCycle` argument — *"the plan cannot be scheduled"* — is true only of the `critical` finding; an untraced task schedules and applies fine, since `nextReadyTask` never consults `implements` edges | **`critical` only.** Plus an acceptance test that a `high` traceability finding stays approvable — without it this ships green, because the demo plan emits `"requirement":"REQ-1"` (`fake.go:258`), which always resolves |
| R2 | The `plan_approved` refusal gives the web user "a clean 409" | False. `post` throws on non-2xx (`api.ts:17-19`) and `approve` has no `catch` (`PlanPage.tsx:114-117`), so the click is a silent no-op. Worse, `PlanPage.tsx:139-142` renders the pill **"needs approval"** for `plan_approved` — the UI would say the plan needs approval while the button silently fails. `PlanPage.tsx` was absent from the artifacts table | The pill and the commit bar are gated on `needs_human_judgment`, in this change |
| R3 | Layer 2's CAS `from` set enumerates the five non-`failed` states | A maintenance trap: `InitiativeStatus` has exactly six values (`domain/enums.go:245-250`), so the list is the exact complement of layer 1's single refusal. A seventh status added later is silently excluded and completion from it becomes a 409 with no test failing | A store helper expressing the invariant directly — `… WHERE id = ? AND status != 'failed'` |
| R4 | Layer 2 maps `ErrInitiativeNotClaimable` "to a precondition" | Not implementable there. `preconditionf` lives in `internal/daemon` (`precondition.go:33`), and `internal/daemon` imports `internal/workflow` — the engine cannot construct a `*PreconditionError` | The mapping happens in `Runtime.RunVerificationGate` |
| R5 | Three citations | All wrong. `TestWaiveRequirementExcludedFromGate` is at `verify_test.go:250`, not `:357` (`:357` is inside `TestFailOnVerifiedRequirementSticksAndCanBeWaived`, `:296`). `judgment_reason` is cleared by `SetInitiativeStatus` (`store/repo.go:288`), not `TransitionInitiative`. `runExecutionPlanReview:412-419` contains **no** disposition call, so it is not the precedent for disposition-before-status | Corrected below |
| R6 | Disposition-first "leaves the initiative claimed and parked" on a failure between the two writes | False. `ApprovePlan` runs inside `launchOperation`, so any error goes to `failOperation(..., failInitiative: true)` (`runtime.go:1197`) and the initiative ends **`failed`** in either order. `reconcileStaleOperations` never sees it — it only touches initiatives still reading `running` (`runtime.go:1296`) | Rationale rewritten to what the order actually buys |
| R7 | The `precondition.go` enumeration must change | Overstated. That comment forbids retyping **engine** errors surfaced at 400; layer 1's `preconditionf` lives in a new `Runtime.*` method, which it already permits. `precondition_test.go:145` is a comment line; the assertion drives `errPlain` and does not change | Comment update only |

Sound on re-review and unchanged: narrowing both the read gate and the claim
(no caller approves from `plan_approved` — `smoke.sh:467`, `next_failed_test.go:181,230`,
`claim_invariant_test.go:175` all pass `needs_human_judgment`); `ReviewRunID` as
a det/adv discriminator (`advance.go:397`, `review/review.go:136`, stored and
selected at `store/repo.go:953,987`, no collision with the PRD loop's
`"deterministic-check"`); dropping the `running` clause; and `fp.Calls()`.

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
and this runs while the status is `running`.

Disposition **before** the status write, but not for the reason the last draft
gave (R6): an error in either position ends `failed` via
`failOperation(..., failInitiative: true)` (`runtime.go:1197`). What the order
buys is which inconsistency survives into that `failed` state. Disposition-first
leaves a human's `accepted_risk` signature on findings for a plan that was never
approved — recoverable, and visible in `idea reviews`. Status-first would write
`plan_approved` and then have `failed` overwrite it, which is a status the human
never got. The precedent is the convergence path at `engine.go:785-789`, not
`runExecutionPlanReview`, which has no disposition call at all. Note that
`Engine.Approve` uses the **reverse** order (`engine.go:924` then `:943`); it can
afford to, because it is not inside an operation that fails the initiative.

**2. A deterministic-findings gate in `Runtime.ApprovePlanInitiative`** (D5),
synchronous, alongside the three that are already there:

```go
// A human can accept an adversarial reviewer's judgment. A human cannot accept
// a dependency cycle: detectCycle's own finding says "the plan cannot be
// scheduled", and nextReadyTask (execute.go:107) will never yield a task whose
// predecessors can never apply. Approving one would trade the live-lock for a
// Build that no command can advance.
//
// critical ONLY, deliberately. deterministicPlanChecks also emits a `high`
// traceability finding per untraced task (advance.go:444), and a task is
// untraced whenever the model's `requirement` string does not resolve --
// reqByCode returns "" (advance.go:346) and createTask skips the edge
// (store/graph.go:148) with no error. `requirement` is a bare string with no
// enum (schema/schemas.go:235), so this is ordinary, not exceptional. Sweeping
// `high` in here would refuse approve-plan on the common case and send the user
// to revise-plan, which re-runs the same unconstrained call -- the live-lock
// this gate exists inside a fix for. Untraced work schedules and applies fine;
// it is a judgment call, and judgment calls are exactly what a human may accept.
```

Refuse with `preconditionf` when any **undisposed** finding has
`ReviewRunID == "deterministic-plan-check"` and `Severity == critical`, naming
`idea revise-plan`. Today that is exactly the cycle finding
(`advance.go:456-462`).

**3. Narrow the status gate to `needs_human_judgment`** (D8). Both the read
gate at `runtime.go:1137` and the claim at `:1166` currently accept
`plan_approved`. `TransitionInitiative` clears `judgment_reason`
(`store/repo.go:225-231`), so a `plan_approved` initiative has `""`, which the
reason gate allows — and the web UI's commit bar renders in **every** status
(audit 7 P1-5, `PlanPage.tsx:143`). Today a stray click there re-runs a review;
after step 1 it would silently disposition every open finding as a human's
`accepted_risk` with no review and no confirmation. Refusing `plan_approved`
with `preconditionf("this plan is already approved")` stops the forged
signature. It does **not** give the web user a visible error (R2): `post` throws
on non-2xx (`api.ts:17-19`) and `approve` has no `catch`, so the click becomes a
silent no-op. So this change also gates `PlanPage.tsx`'s commit bar **and** its
`"needs approval"` pill (`:139-142`, which today fires for `plan_approved` too)
on `needs_human_judgment`, so the button is not rendered in a state that refuses
it. That is a partial, contained fix for audit 7's P1-5; the rest of P1-5 — the
missing `catch` and `judgment_reason` never reaching the UI — stays in the
backlog.

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

**Anti-gate, `internal/daemon`** — an initiative parked with a deterministic
`high` **traceability** finding and no cycle **is** approvable and reaches
`plan_approved` (R1). This is the guard that keeps the gate from widening back
into a live-lock, and it cannot be written against the demo path as it stands:
`demoResponse`'s execution plan emits `"requirement":"REQ-1"`, which always
resolves (`fake.go:258`), so the test seeds the untraced task directly.

**Gate, `internal/daemon`** — `approve-plan` on a `plan_approved` initiative
answers 409 and disposes nothing.

### Smoke

Add a second plan-judgment pass with `block_plan_review_cycles: 50` and
**`dangle_plan_dependency` off** (with it on, the initiative parks on
`plan_dependencies` and never reaches the review the pass exists to test),
asserting `approve-plan` reaches `plan_approved` and then `complete`.

It needs its own project directory, its own config write, a **daemon restart as
the last write before `idea new`**, and a `wait_for` on the daemon — the
existing pass carries an explicit warning about exactly that ordering
(`smoke.sh:424-426`). Traced: the PRD converges (`block_review_cycles` unset),
`advance` drains one `plan_review` entry and parks, `approve-plan` sets
`plan_approved` with zero model calls, the 49 stranded entries are harmless
because the change-set review uses the separate `"review"` key
(`execute.go:1307`, queues separated at `runtime.go:1512-1518`), and the
disposition clears `hasOpenBlockingFindings` so verify completes.
`BlockPlanReviewCycles` is unclamped, so 50 is accepted (`runtime.go:1507`).

The existing pass stays — it covers the disposition side-effect on the
converging path, a different property — but its six-line comment at
`smoke.sh:460-466`, which explains that `approve-plan` works there *because the
queue is drained*, becomes false and must be rewritten.

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

`precondition.go:22-26` names `VerifyInitiative` among the paths that must not
carry the precondition type. That does **not** need inverting (R7): the comment
forbids retyping *engine* errors surfaced at 400, and layer 1's `preconditionf`
lives in a new `Runtime.*` method, which the enumeration already permits. The
comment is updated to say so; `precondition_test.go` does not change — its
assertion drives `errPlain`, not this path.

Declare `409` on `verifyInitiative` in `docs/openapi.yaml`; `api-check`'s
conflict axis then requires it.

**2. A guarded completion write.** Replace the terminal
`SetInitiativeStatus(..., InitiativeComplete, ...)` with a store helper that
expresses the invariant directly — `UPDATE … WHERE id = ? AND status != 'failed'`
— rather than a `TransitionInitiative` enumerating the other five states (R3).
`InitiativeStatus` has exactly six values (`domain/enums.go:245-250`), so an
enumeration is the exact complement of layer 1's single refusal, and a seventh
status added later would be silently excluded with no test failing. `running`
must remain permitted regardless: `CreateInitiative` defaults to it
(`store/repo.go:41-43`) and five `verify_test.go` fixtures rely on that (D3).

Layer 1 refuses the request; this refuses the *write*. What it buys beyond
layer 1 is narrow and should not be overstated (D4): a genuine TOCTOU, where a
concurrent `failOperation(..., failInitiative: true)` (`runtime.go:357`) lands
between layer 1's read and the terminal write — reachable precisely because
layer 1 permits `running` — plus any future caller.

The no-rows error must be mapped, not leaked (D7), and the mapping belongs in
`Runtime.RunVerificationGate`, not the engine (R4): `preconditionf` lives in
`internal/daemon` (`precondition.go:33`) and `internal/daemon` imports
`internal/workflow`, so the engine cannot construct a `*PreconditionError`.
Unmapped, `handleVerify` would answer `400: store: initiative is not in a
claimable state`.

**3. Zero *active* requirements is not "everything verified."** Guard the
completion conclusion on **at least one non-`removed` requirement** (D6). Not
`len(reqs)`, which the all-removed case defeats; not "active excluding waived",
which would kill the all-waived flow.

This is audit 5's **A2**, *rejected on review* and recorded at `deferred.md:37`.
That rejection was correct and this does not reopen it:

| A2 proposed | Why rejected | This plan |
|---|---|---|
| Require a change set before `complete` | Kills the documented all-waived flow (`verify_test.go:250`, `openapi.yaml:2946-2950`, `ui.md:227`) | Not proposed; change sets are not consulted |
| Predicate `{applied, reverted}` from `ui.md:101` | `rejected`/`superseded` are absorbing states minted on ordinary paths | Not used |
| — | `VerifyInitiative` has no channel to report a refusal | Layers 1 and 2 supply it: a 409, and a write refusal |

All-waived has waived requirements, which are not `removed`, so it passes the
guard, reaches the loop, and still concludes `allVerified`. Zero-requirements
and all-removed are refused. The rejected predicate conflated these; this one
separates them.

### A residual this does not close, stated rather than implied

With `running` permitted by layer 1 and by layer 2's guard, a `complete` written
by `idea verify` during an in-flight apply can be silently reverted:
`release()` writes `priorStatus` unconditionally (`runtime.go:768-772`), while
`handleVerify` has already re-read and reported `{"status":"complete"}`
(`handlers.go:1950-1959`). That is the documented sequence smoke runs
(`smoke.sh:472-477`). It is pre-existing, and excluding `running` would break
that flow the other way (D4), so it is deferred rather than fixed here. P0-2 is
not airtight; it is closed for the defect audit 7 reported.

### Acceptance tests

- `internal/daemon`: `POST /verify` on a `failed` initiative → **409**, status
  still `failed`. Mutation: removing the `preconditionf` must fail it.
- `internal/daemon`: `POST /verify` during an in-flight apply (status `running`)
  still succeeds — the D4 regression guard.
- `internal/workflow`: zero requirements → no `complete`. All requirements
  `removed` → no `complete`. Mutation: removing the guard must fail both.
- `internal/workflow`: `TestWaiveRequirementExcludedFromGate`
  (`verify_test.go:250`) and `TestFailOnVerifiedRequirementSticksAndCanBeWaived`
  (`:296`) both stay green **untouched** — the A2 regression guards. `waived` is
  not `removed`, so layer 3 passes them. If any layer requires editing either,
  that layer is wrong. (The previous draft cited `:357`, which is a line
  *inside* the second test — R5.)
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
| `internal/daemon/precondition.go:22-26` | comment only — the enumeration already permits a new `Runtime.*` method (R7). `precondition_test.go` does not change |
| `web/src/PlanPage.tsx:139-149` | the `"needs approval"` pill and the commit bar both gated on `needs_human_judgment` (R2) |
| `scripts/smoke.sh:460-466` | the comment explaining that `approve-plan` converges "because the queue is drained" becomes false |
| `docs/plans/deferred.md` | the empty-`judgment_reason` hole (D9); the apply-release residual; the rest of P1-5 |

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
