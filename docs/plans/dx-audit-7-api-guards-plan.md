# DX Audit 7 — the two remaining API state guards (P1-3, P1-4)

P1-2 (`POST /verify` had no state guard) shipped in `3ce902a`. These are the
other two the API dive found, and both are the same shape: a check that exists
somewhere, applied to one caller and not to the path that matters.

---

## P1-3 — `POST /revise-plan` answers 202 for a state it refuses, then fails the initiative

`Runtime.ApprovePlanInitiative` carries a change-set guard introduced with the
comment *"Matching RevisePlan and ReopenPhase"*. **That comment is false.**
`Runtime.RevisePlanInitiative` (`runtime.go:1065`) checks running and status and
nothing else. The change-set refusal exists only in `Engine.RevisePlan`
(`advance.go:198-204`), which runs inside the launched operation and returns
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

### Acceptance

- `POST /revise-plan` on an initiative with ≥1 change set answers **409**, and
  `GET /initiatives/{id}.status` is **unchanged** afterwards. Mutation: removing
  the guard must fail it — and the failure must be that the status became
  `failed`, not merely that the code was 202, because the status is the damage.
- A sibling-parity test: `approve-plan` and `revise-plan` answer the **same
  status** for the same state. That is what would have caught the false comment,
  and it is what keeps the two from diverging again.

---

## P1-4 — `PUT /providers` ignores an inline `api_key`, answers 200, and turns demo mode off

Observed end to end by the dive:

```
PUT …/providers  {"providers":{"openai":{"api_key":"sk-live-123"}}}
→ 200 {"status":"updated"}
```

`api_key` is not a field of the decode target, so `encoding/json` drops it
silently. What lands in `harness.yaml` is
`openai: {type: "", api_key_env: "", base_url: "", models: []}` — an unusable
entry. `defaultProviderName` then picks it (first configured provider,
alphabetically), so `isDemo` flips **false**, and the dive observed real billed
`gpt-4o` calls follow. The one signal that output is synthetic was inverted by a
write that configured nothing.

`config.ValidateProvider` cannot catch it: it returns `nil` immediately when
`BaseURL == ""` (`problems.go:266-268`). And `docs/openapi.yaml`'s
`ProviderConfig` says *"Inline key values are rejected"* — they are **ignored**,
which is the failure mode that costs money.

**The check already exists, client-side, with the root cause written on it.**
`internal/cli/model_cmd.go:259-263`:

> `// ValidateProvider returns nil early when base_url is empty, so the daemon`
> `// accepts an endpoint-less provider and the failure surfaces at the first`
> `// model call instead of here.`
> `if pc["type"] == nil && pc["base_url"] == nil { … }`

So `idea model add` is safe and the web UI and any direct API caller are not.
This is the same shape as audit 6's D8 — a fix landed on one client and never
moved to the daemon that serves all of them.

### Fix, two parts

1. **`DisallowUnknownFields()` on the decoder**, answering 400 naming the
   field. `encoding/json` applies it to nested objects too, so
   `providers.openai.api_key` is rejected rather than dropped. Verified safe for
   both first-party clients: the web UI sends `{type, base_url, api_key_env}`
   (`Settings.tsx:175-183`) and `{default_model}` / `{git_commit}`; the CLI
   sends `{providers:{name:{…}}}` built from known keys.
2. **Refuse a provider entry with no `type`, no `api_key_env` and no
   `base_url`** — the daemon-side form of the CLI's check. An entry that
   configures nothing cannot serve a request, and its only observable effect is
   to suppress the demo banner.

`handleUpdateExecutors` shares the decoder pattern and gets `DisallowUnknownFields`
with it, or the next round finds the same defect on the sibling — which is how
this one got here.

### Acceptance

- `PUT /providers {"providers":{"openai":{"api_key":"x"}}}` → **400** naming
  `api_key`, and `GET /providers`'s `demo` is **unchanged**. The second half is
  the assertion that matters: a 400 that still flipped demo mode would be a
  worse defect wearing a better status code.
- `PUT /providers {"providers":{"openai":{}}}` → **400**, `demo` unchanged.
- The web UI's exact Add-provider body and the CLI's exact `model add` body both
  still succeed. Pinned as literals from `Settings.tsx` and `model_cmd.go`, so a
  future field addition that would 400 a first-party client fails here first.
- Mutation: removing either half must fail its test.

### Docs

`docs/openapi.yaml`'s `ProviderConfig` says inline keys are *rejected*; after
this they are, so the sentence becomes true rather than aspirational. Add the
400 to `updateProviders`' declared responses if it is not already there.

Also from the dive, same endpoint, and cheap to do here: `updateProviders` can
answer **409** through `writeReloadErr` and declares none, while its sibling
`updateExecutors` — which reaches 409 by the identical call — does declare it.
`make api-check` passes because `conflictcheck.go` only propagates through
functions containing a literal `preconditionf`, not through `writeReloadErr`.
Declaring the 409 is one line; making `conflictSources` follow any callee that
writes `StatusConflict` is the guard fix, and its acceptance test is that the
conflict axis fails when the declaration is removed.

---

## Order

1. P1-3, with the sibling-parity test.
2. P1-4, with the two body literals pinned.
3. The `conflictcheck` propagation fix and the `updateProviders` 409.

Each: mutation-checked tests, adversarial review before merge, merge on green,
and ledger rows carrying both a `present:` and an `absent:` predicate.
