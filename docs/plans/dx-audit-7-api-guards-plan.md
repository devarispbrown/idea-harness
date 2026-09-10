# DX Audit 7 — the two remaining API state guards (P1-3, P1-4)

P1-2 (`POST /verify` had no state guard) shipped in `3ce902a`. These are the
other two the API dive found.

## What the plan review changed

Reviewed before implementation and returned **APPROVE WITH CHANGES** with two
critical defects. The two headline guards survived attack; three of the six
deliverables did not.

| # | Draft said | Reality | Now |
|---|---|---|---|
| D1 | Refuse a provider entry with no `type`, no `api_key_env` and no `base_url`; `PUT {"providers":{"openai":{}}}` → 400 | **Backwards, and the acceptance test would have pinned the regression.** `ResolveProvider("openai", {})` returns `type=openai base=https://api.openai.com/v1 keyEnv=OPENAI_API_KEY` — the complete preset (measured). `{}` for a preset name is a legitimate, working configuration, and `internal/daemon/providers.go:74` calls exactly that as the preset-fallback path. So the draft's premise — *"an unusable entry"* — was false, and so was its account of the damage: the `demo` flip was **correct**, because a real provider genuinely was configured. That is also why the dive saw *billed* calls rather than connection failures | The predicate is "resolves to no endpoint", not "declares no fields" |
| D2 | Make `conflictSources` follow any callee that writes `StatusConflict` | **Would flag 78 of 81 routes.** `writeErr` itself contains a literal `writeError(w, http.StatusConflict, …)` in its `*PreconditionError` branch (`errors.go:57`), and 75 functions call it — every read-only GET included. Measured: 15 → 78 conflict-capable operations, 57 newly requiring a spurious `409`, on a guard whose purpose is keeping the spec honest | "Every status this helper writes is 409", with `writeErr` excluded. Measured: 17, and exactly one newly requires a declaration — `updateProviders`, which is the target |
| D3 | Assert `approve-plan` and `revise-plan` answer the same status for the same state | False in three reachable states by design, and one of them is already pinned by a passing test (`TestRevisePlanClearsTheStructuralGate`), whose whole point is that revise-plan is the escape approve-plan's refusal names | Change-set parity across the three plan-level commands, plus negative rows |
| D4 | Silent | After the guard, an initiative with a change set and a plan-review park has **both** commands `idea next` names refused. That is deferred row `audit 6 / J1` | Named, with the refusal pointing at the change-set path |
| D5 | Silent | `PlanPage.tsx`'s `plan_approved` commit bar offers only "Ask for a revision", and `plan_approved` is exactly where change sets exist | Gated, and the comment that becomes false is rewritten |
| D6 | Silent | Both fixes change documented success responses | `### Changed — BREAKING`, table form |
| D7 | "400 naming the field" | Both handlers discard the decoder error and write a fixed `"invalid body"` | The decoder error is surfaced |
| D8 | Two `preconditionf` conversions | `ReopenPhaseInitiative` (`runtime.go:999`) has the same guard with the same plain `fmt.Errorf`, under a comment making this plan's own argument; `retry` (`:932`) too | The full set |

The review also reframed P1-4 usefully: the asymmetry is not client-vs-server.
`config.Load` already rejects unknown fields and inline secrets in the **YAML**
path (`config.go:181-183`, `rejectSecrets`). It is **YAML strict, JSON lenient**
— which is why `openapi.yaml`'s *"Inline key values are rejected"* is half-true
rather than aspirational, and why `rejectSecrets` is the model for the message.

---

## P1-3 — `POST /revise-plan` answers 202 for a state it refuses, then fails the initiative

`Runtime.ApprovePlanInitiative` carries a change-set guard introduced with the
comment *"Matching RevisePlan and ReopenPhase"*. **That comment is false.**
`Runtime.RevisePlanInitiative` (`runtime.go:1065`) checks running and status and
nothing else. The change-set refusal exists only in `Engine.RevisePlan`
(`advance.go:217-222`), which runs inside the launched operation and returns
`domain.InitiativeFailed, err` — so `failOperation(..., failInitiative: true)`
marks the initiative **failed**.

Observed by the dive: a healthy `plan_approved` initiative with one unreviewed
change set became `failed` because of a refusal the API answered synchronously
on the sibling endpoint one function away.

This is the class `ExecuteInitiative`'s own comment says was fixed: *"a state
that cannot execute has to be caught here or the caller gets '✓ execute started'
and exit 0 with the failure only in the log."*

### Fix

Move the guard to where the sibling has it — in `RevisePlanInitiative`, before
the claim:

```go
if cses, err := pr.db.ListChangeSets(ctx, initiativeID); err != nil {
    return "", err
} else if len(cses) > 0 {
    return "", preconditionf("cannot revise a plan that already has change sets")
}
```

The engine check **stays**. It is the invariant; this is the precondition. Same
reasoning the approve-plan gate carries.

While here, the two read-only checks above it use plain `fmt.Errorf`
(`runtime.go:1074`, `:1078`), so they reach 409 only because the handler happens
to pass `StatusConflict` — the classification does not travel with the error,
which is the exact property `precondition.go` says the type exists to guarantee.
`RevisePlanInitiative` and `ApprovePlanInitiative` are the last two read-only
running checks in the file still doing this. Both become `preconditionf`.

**Placement: before the claim**, matching the sibling. After it would close a
TOCTOU (a concurrent `execute` can create CHG-1 between the read and the claim)
but would need a compensating release, and `runtime.go` argues at length that a
failed release strands the initiative `running` with no operation for
`reconcileStaleOperations` — worse than the race. The race degrades to exactly
today's behaviour, with the engine check as the backstop, which is why that
check stays.

**The `preconditionf` sweep is the full set, not two of it.** `RevisePlanInitiative`
(`runtime.go:1075`, `:1078`), `ApprovePlanInitiative` (`:1238`, `:1252`),
`ReopenPhaseInitiative` (`:999` — which carries the change-set guard already,
with a comment making this plan's own argument, and still uses `fmt.Errorf`),
and `RetryInitiative` (`:932`). Converting four and leaving two is how the next
round finds the same defect.

### Acceptance

- `POST /revise-plan` on an initiative with ≥1 change set answers **409**, and
  `GET /initiatives/{id}.status` is **unchanged**. The status is the assertion
  that matters — a test checking only the code would pass while the initiative
  still ended `failed`.
- **Change-set parity, table-driven** over `approve-plan`, `revise-plan` and
  `reopen-phase`: with ≥1 change set all three answer 409 and none moves the
  status. That is the parity that actually holds, and it is what would have
  caught `ApprovePlanInitiative`'s false *"Matching RevisePlan and ReopenPhase"*
  comment. A fourth plan-level command joins the table rather than diverging.
- **Negative rows**, so the guard cannot be over-widened later: with **no**
  change sets, `revise-plan` still answers 202 from `plan_approved`, from a
  `plan_dependencies` park, and from a park carrying an open `critical`
  deterministic finding. Sibling parity is false in all three by design — the
  third is already pinned by `TestRevisePlanClearsTheStructuralGate`.

### The dead end this creates, named rather than shipped silently

With a change set present and the initiative parked on `plan_review`,
`reportJudgment`'s `"plan"` branch offers `revise-plan` and `approve-plan` —
and after this fix **both** are refused. `approve-plan` already was; this makes
the pair complete.

That state is reachable and recorded: `deferred.md`'s `audit 6 / J1` says
`NextAction`'s change-set walk sits under `case InitiativePlanApproved`, so a
plan-blocked initiative that ran `idea execute` still answers `resolve`. The
alternative today is worse — 202, then the initiative goes `failed`, then
`idea retry` — so the guard is right. But silence is how J1 survived a round.

So: the refusal message names the route that does exist (`idea changes`,
`idea review CHG-n`, `idea apply`), the way `approve-plan`'s cycle refusal names
`idea revise-plan`; and a `deferred.md` row ties the `idea next` half to J1.

---

## P1-4 — `PUT /providers` silently discards an inline `api_key` and answers 200

Observed end to end by the dive:

```
PUT …/providers  {"providers":{"openai":{"api_key":"sk-live-123"}}}
→ 200 {"status":"updated"}
```

`api_key` is not a field of the decode target, so `encoding/json` drops it
silently. What lands in `harness.yaml` is
`openai: {type: "", api_key_env: "", base_url: "", models: []}` — and that entry
is **not** unusable, which the draft got backwards. `ResolveProvider("openai",
{})` fills every field from the preset, so the write really does configure
OpenAI, `isDemo` really should flip false, and the billed `gpt-4o` calls the
dive observed followed correctly from a configuration the caller never intended
to make. The defect is exactly one thing: **a field the caller sent to carry a
credential was silently discarded**, and the response said `updated`.

`docs/openapi.yaml`'s `ProviderConfig` says *"Inline key values are rejected"*.
They are not — but the YAML path does reject them: `config.Load` validates the
raw document strictly, and `rejectSecrets` names the offending path. So this is
a **YAML-strict, JSON-lenient asymmetry**, and `rejectSecrets` is the model for
the message.

### Fix, two parts

1. **`DisallowUnknownFields()` on the decoder**, on `handleUpdateProviders` and
   `handleUpdateExecutors` together — the sibling is how this class propagates.
   `encoding/json` applies it to nested objects, so `providers.openai.api_key`
   is rejected rather than dropped. Verified safe: every first-party body was
   decoded against the real structs with the flag on, and all eight succeed
   (`Settings.tsx` ×4, `model_cmd.go` ×2, `huh.go`, `change.go`); the defect
   body fails with `json: unknown field "api_key"`. No spec-documented field is
   missing from either struct.

   **The 400 must name the field**, and today both handlers discard the decoder
   error for a fixed `"invalid body"`. The unknown-field error is safe to
   return verbatim — it names a key the caller sent, not a value. A syntax
   error stays `invalid body`, because its text can quote the body.

2. **Refuse a provider entry that resolves to no endpoint** — not one that
   declares no fields:

   ```go
   if _, base, _ := config.ResolveProvider(name, pc); base == "" {
   ```

   That refuses `{"providers":{"mycustom":{}}}`, which genuinely names nothing
   to send requests to, and accepts `{"providers":{"openai":{}}}`, which is a
   preset configured by name alone and works.

   It belongs in `config.ValidateProvider` rather than the handler, whose
   `BaseURL == ""` early return is the hole: that way it also runs at
   registry-build time, which is the *"enforced on the write path and again when
   the registry is built"* promise `docs/security.md` makes. Consequence to
   state rather than discover: it changes `problem` on `GET /providers` for
   entries already on disk.

### Acceptance

- `PUT /providers {"providers":{"openai":{"api_key":"x"}}}` → **400** naming
  `api_key`, and `GET /providers`'s `demo` is **unchanged**. The second half is
  the assertion that matters: a 400 that still flipped demo mode would be a
  worse defect wearing a better status code.
- `PUT /providers {"providers":{"mycustom":{}}}` → **400**, `demo` unchanged.
- **`PUT /providers {"providers":{"openai":{}}}` still SUCCEEDS**, and `demo`
  flips false, because that is a real configuration. Without this row the draft's
  predicate would have shipped a regression with a green test on top of it.
- Every first-party body still succeeds, pinned as literals lifted from
  `Settings.tsx`, `model_cmd.go`, `huh.go` and `change.go`, so a future field
  addition that would 400 a first-party client fails here first.
- Mutation: removing either half must fail its test.

### The conflict axis

`updateProviders` can answer 409 through `writeReloadErr` and declares none,
while `updateExecutors` reaches 409 by the identical call and does declare it.
`make api-check` passes because `conflictcheck.go` seeds only on functions
containing a literal `preconditionf`.

The rule is **"every status this helper writes is 409"** — true of
`writeReloadErr` (both branches), false of `writeErr` (four statuses, whose 409
branch is precondition-driven and already covered). `writeErr` is excluded
explicitly, with a comment: it is the reason the obvious rule is wrong.

Acceptance, both directions: removing `updateProviders`' 409 declaration fails
the conflict axis, **and** no GET is newly flagged. Measured, the narrow rule
takes conflict-capable operations 15 → 17 and newly requires exactly one
declaration.

### Docs

Both endpoints' 400 descriptions enumerate their error strings and gain the
unknown-field case; `openapi.yaml`'s *"Inline key values are rejected"* becomes
true; `make api-docs` regenerates `docs/api.md`, which `api-docs-check` gates.

---

## CHANGELOG

Both go under `### Changed — BREAKING`, in the Was/Now table form
`approve-plan`'s own 202→409 entry uses. Audit 7 faulted this round for filing
that one under `### Fixed`; repeating it here would be the same defect.

- `POST /revise-plan`: 202 → **409** for a state that previously succeeded (and
  then failed the initiative asynchronously).
- `PUT /providers` and `PUT /executors`: 200 → **400** for any body carrying an
  undeclared field. This breaks any third-party client sending an extra key, not
  only the two first-party ones.

## Ledger

Rows carry both directions. Note these are **code** defects, so most predicates
target `.go` files — permitted, and `R2c`/`V1` already do it. The one genuine
documentation claim in scope is `openapi.yaml`'s *"Inline key values are
rejected"*.

## Web UI

`PlanPage.tsx`'s `plan_approved` commit bar offers exactly one action, "Ask for
a revision" — and `plan_approved` is precisely where change sets exist, since
`execute` runs from there. After P1-3 that button 409s for any initiative that
has started Build. The error is at least rendered now, so it is not the silent
dead click P1-5 was, but the page still offers a button guaranteed to fail:
gate that branch on change-set presence, and rewrite the comment above it, which
says the daemon still accepts revise-plan from `plan_approved` and stops being
true on merge.

## Order

1. P1-3, with the sibling-parity test.
2. P1-4, with the two body literals pinned.
3. The `conflictcheck` propagation fix and the `updateProviders` 409.

Each: mutation-checked tests, adversarial review before merge, merge on green,
and ledger rows carrying both a `present:` and an `absent:` predicate.
