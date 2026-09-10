# DX Audit 7 — the remainder (P1-1, P1-5, P1-6, P1-7, ledger remedies)

The last four items of the report's recommended order.

## What the plan review rejected

Reviewed before implementation and **rejected**. Three of five items rested on a
claim about code I had not read to the bottom. Recording the rejections, because
they are the useful part.

| # | Draft said | Reality | Now |
|---|---|---|---|
| D1 | `idea new` renders its final line "through the same `reportNextAction` path `idea next` uses" | **`reportNextAction` is not a renderer — it executes.** `next.go:211` is `case action == "advance": return cmdAdvance(...)`, which POSTs `/advance` and starts the M2 pipeline. On the *converging* path `/next` returns exactly `advance`, so the draft would have made `idea new` spend model budget through design — violating a decision documented ten lines above the function I proposed reusing (`next.go:32`: *"`idea new` is deliberately NOT made to drive on its own"*). It would also have made `idea new` exit non-zero where it exited 0, and my own acceptance criterion (*"with the review converging, it still says the PRD is ready"*) is unsatisfiable by my own mechanism — no branch of `reportNextAction` emits that line | The non-executing branches are extracted; `cmdNew` switches on the two tokens actually reachable |
| D2 | `pastDefine` = approved / plan_approved / complete / a post-approval judgment reason | Drops `running` and `failed`. The status is `running` for the whole of every M2 and M3 operation, so during `idea execute` the rail would show Define **un-done** and jump the current phase back from Plan to Define. A straight regression | Both are handled explicitly |
| D3 | "`PhaseSource` gains `judgmentReason`" | `derivePhases` has **two** callers. `ProductHome.tsx:66` goes through `rollupPhaseSource`, and `InitiativeRollup` has no `judgment_reason` in the Go struct, the projection, the OpenAPI schema or the TS mirror — so the home page keeps the exact bug, for every initiative at once | The rollup carries the reason too, and that is named as the larger half |
| D4 | The empty reason falls back to the artifact check, "the safe reading" | Backwards. Migration 18 backfills nothing — every pre-migration park carries `''` whatever parked it, including `plan_dependencies`, whose artifact **is** present. So the fallback re-runs the wrong inference on precisely the population it is wrong for. `deferred.md`'s `audit 7 / K1` already says so, and the draft cited the opposite conclusion without citing the row. (Also: there are **four** reasons plus the empty string, not "six") | Empty reason is not past define |
| D5 | R1: "the predicate cell accepts several predicates separated by ` + `… exactly the shape audit 6's `D8` row had" | `evalPredicate` takes **one** predicate — two `strings.Cut`s and a `Contains`, no split anywhere. Zero of 110 rows contain ` + `. `D8` carries a single `absent:`, which R1 would not flag. The motivating example does not exist | R1 is stated as new capability, with combination semantics and a separator that cannot collide with a literal |
| D6 | "Existing rows are migrated" | Measured: **50 non-`manual` rows have no path in their Claim/Truth cells at all** (audit 3 puts the file in the section *heading*), and **18 `fixed` rows carry only a `present:`**. That is ~68 hand-edits across five files, each re-deriving corrected text for a defect fixed up to four rounds ago | Prospective, with a grandfather list |
| D7 | Silent on `manual:` | R2 must exempt it or six rows become parse errors. And R1 as worded left `fixed` + `manual:` exempt — a row asserting nothing, which is R1's own failure mode one form over | R1 keys on the row, not on what it "also" carries |
| D8 | A new `deferred.md` row for fence diffing | `audit 6 / G5b` already covers it and was *"rejected on cost"* twice, with a rationale this round disproves. A sibling row is the bookkeeping failure `runCoverageCheck` exists to prevent | G5b is amended with the new evidence |
| D9 | "one check, twenty call sites" | `cmdExplain` (`explain_export.go:27-37`) **hand-inlines** the resolution, so it would stay unrefused. And of the 19 sites four pass `nil`, so it is 15 + `cmdExplain` = 16 reference-taking commands | Both counted, and `cmdExplain` folded in |
| D10 | Silent | `cmdChanged` has the identical defect ten lines from `cmdStatus` — declared `maxPositional: 1`, reads no positional, and uses `inis[0]`, the **oldest**, against the doc's "most recently created" | Fixed with it |
| D11 | "it scopes to that initiative" | `idea status --json` is an array, and `docs/cli.md:93` says so under ledger row `G4`. Scoping without saying to what shape is an undeclared response change | The array is kept, filtered to one element |
| D12 | Silent | `✓ reviewed PRD ready` is quoted verbatim in `demo-greenfield.md:47` and `demo-existing-project.md:65`, both pinned by audit 6 ledger rows. Changing it without them ships the P1-7 defect class | Both guides are in scope |
| D13 | Silent | Three behaviour changes need `### Changed` entries | Listed |

Two things it tried to break and could not: every call site that passes a
positional really does mean an initiative reference (initiatives have no human
id and no prefix form, and `ListInitiatives` is unpaginated and project-scoped),
so validating against the fetched list is safe; and `verification`,
`plan_review` and `plan_dependencies` really are all post-approval.

---

## Ledger remedies — first, because the other items each want a row

**R1 — a row marked `fixed` or `not reproduced` must carry at least one
`absent:` predicate.** Keyed on the row, not on what else it carries, so it
covers the `present:`-only case *and* the `manual:`-only case — a `fixed` row
asserting nothing mechanically is the same "green over unverified" failure one
form over.

**R2 — a predicate must name a file the row cites**, where "cites" means a
path-shaped backtick in the Claim or Truth cell. `D8d` pointed at
`web/src/Settings.tsx` for a claim about `docs/security.md:20` and passed on an
import line. `manual:` is exempt by construction: it has no `<path>`.

**Both apply prospectively.** Retrofitting is ~68 hand-edits across five files
(measured: 50 rows cite no path, 18 `fixed` rows are `present:`-only), most of
them re-deriving corrected text for defects fixed up to four rounds ago. A
grandfather list — the ledger files existing before this change — is carried in
the checker with the count and the reason, so the exemption is visible and
shrinks rather than being forgotten. Audit 8's rows get both rules.

**Multiple predicates.** The cell gains a separator, split on
`\s+\+\s+(?=(?:absent|present|manual):)` — a lookahead on the known form
prefixes, so no literal can collide with it whatever it contains. Combination
semantics, which the draft omitted: for `fixed`/`not reproduced` **every**
predicate must report the defect absent; for `open`/`deferred` every predicate
must report it present. Otherwise a two-predicate `open` row is undefined.

**R3 is not a new row.** `audit 6 / G5b` already proposes fence diffing and was
rejected on cost twice, closing with *"the eight wrong blocks are fixed,
`absent:` predicates catch a re-edit"* — which D9 disproves, having been
`present:`-green over a *second* wrong block. G5b is amended with that evidence
rather than duplicated.

**Acceptance.** Parser unit tests for the separator and both rules; mutation:
a row violating each must fail. The grandfather list must be a literal set, so
a new file cannot join it by accident.

---

## P1-7 — a fabricated `idea doctor` block in the quickstart

`docs/quickstart.md:278`. Lines 276-277 match `internal/cli/doctor_keys.go:76-77`
verbatim; the third is invented, and its remedy is unperformable — `idea model
set` requires `<provider/model>` and no CLI verb clears `default_model`. Ledger
row **D9**, `fixed audit-6`, predicate green: that round replaced a wrong block
with a different wrong block.

**Fix.** Correct the line to the binary's. Do not invent a clearing verb.

**The guard's limit, stated.** The new row's `absent:` literal is the binary's
own string, so a future edit to the *doc* is caught — but nothing ties it to
`doctor_keys.go`, so an edit to the **Go** source re-opens D9 a third time
silently. That is the concrete instance G5b exists for, and it goes in G5b's
amendment as the evidence.

---

## P1-1 — `idea new` reports a reviewed PRD over a PRD parked for judgment

`internal/cli/cli.go:527` prints `✓ reviewed PRD ready` unconditionally on
operation success and never reads the terminal status.

Only two states are reachable there: `engine.Run` ends in `runReviewLoop`, which
returns `InitiativeApproved` or `InitiativeNeedsHumanJudgment` +
`JudgmentPRDReview`, and `NextAction` maps those to `advance` and `resolve prd`.

**Fix.** Extract `reportNextAction`'s non-executing branches into
`renderNextAction` (`review …`, `apply …`, `resolve …`, `done`, `failed`,
default); `reportNextAction` keeps its executing arms and delegates the rest, so
`idea next` is unchanged. `cmdNew` calls `nextAction()` — already in-package,
and `c`, `projectID` and the initiative id are all in scope — and switches:
`advance` keeps the ready-PRD line, `resolve prd` calls `reportJudgment("prd")`,
anything else delegates to `renderNextAction`. No state change, no new exit
code, and the judgment text has one source.

**Acceptance.** Against the fake daemon: parked → the final line names the
judgment, not a reviewed PRD; converging → still the ready-PRD line; and
`idea new` exits 0 in both. Mutation: restoring the unconditional line fails the
parked case. Smoke's HUMAN-JUDGMENT pass gains the stdout assertion it never
made.

**Docs.** `demo-greenfield.md:47` and `demo-existing-project.md:65` quote the
line; both are ledger-pinned and stay correct because the converging path — the
one those guides run — keeps it.

---

## P1-6 — `idea status` and `idea changed` ignore their documented argument

`docs/cli.md:100-108` promises an optional reference defaulting to the most
recently created, and uses `idea status` as its example. Neither command reads
one, and `cmdChanged` (`state_cmds.go:151`) uses `inis[0]` — the **oldest**,
since `ListInitiatives` is `ORDER BY created_at ASC` — which is the direct
opposite of the documented default.

`resolveInitiative` (`advance.go:19-30`) also returns `args[0]` unchecked, so all
16 reference-taking commands accept a garbage reference and produce whatever
downstream 404 that endpoint gives.

### The fix

1. **`resolveInitiative` validates** against the list it has already fetched.
2. **`cmdExplain` folds into it** — `explain_export.go:27-37` re-implements the
   resolution by hand and would otherwise stay unrefused.
3. **`cmdStatus` and `cmdChanged` take the reference.** `cmdChanged` also
   switches from oldest to newest.
4. **`idea status --json` keeps its array**, filtered to one element when
   scoped. `docs/cli.md:93` documents the array shape and ledger `G4` pins that
   sentence; `jq '.[0]'` keeps working and neither needs an edit.
5. `usage: "status"` → `status [<initiative>]`, and `make docs`.
6. `docs/cli.md` states both halves: the newest default for action commands, and
   that unscoped `status` is the overview.

**Acceptance.** `idea status ZZZZ` and `idea changed ZZZZ` exit non-zero naming
ZZZZ; scoped forms print exactly that initiative; bare forms unchanged;
`idea changed` acts on the newest. A table test over the 16 reference-taking
commands asserting an unknown reference is refused **before any state-changing
request** — not "before any request", which is unachievable, since
`resolveInitiative`'s first act is the `GET` it validates against.

---

## P1-5 (remainder) — `phases.ts` infers the phase from artifact presence

`web/src/phases.ts:117-121` uses execution-plan artifact presence as the
disambiguator — verbatim the inference `CHANGELOG` records as wrong, because
`runExecutionPlan` writes that artifact *before* the dependency check.

### The fix

`pastDefine` becomes, in order:

- `approved`, `plan_approved`, `complete` → true;
- `running` or `failed` → **the artifact check, as today** (D2: the status is
  `running` for the whole of every M2/M3 operation, and no reason is recorded
  there, so the artifact is the only evidence available and un-completing Define
  mid-build would be a regression);
- `needs_human_judgment` with `plan_review`, `plan_dependencies` or
  `verification` → true; with `prd_review` or **empty** → false. Empty is not a
  fallback to the artifact check (D4): migration 18 backfills nothing, so the
  pre-migration population carries `''` whatever parked it, and a legacy
  `plan_dependencies` park has the artifact — exactly where the artifact check
  is wrong. `deferred.md`'s `audit 7 / K1` records that hole; treating empty as
  not-past-define is the conservative reading and shows an earlier phase as
  current rather than claiming a later one is done.

**The larger half: the rollup.** `ProductHome.tsx:66` reaches `derivePhases`
through `rollupPhaseSource`, and `InitiativeRollup` carries no reason — so
fixing only `App.tsx` leaves the bug on the page that renders it for every
initiative at once, which is the "fixed in one place" pattern this round keeps
finding. `judgment_reason` is added to `store.InitiativeRollup`, the `/attention`
projection, the OpenAPI `InitiativeRollup` schema and the TS mirror.

**Acceptance.** `web/src/phases.test.ts` (new) over the four reasons plus empty,
plus `running` and `failed`, including: a `plan_dependencies` park **with** an
execution-plan artifact is not past define — the case the artifact check gets
wrong — and `running` with the artifact **is**, which is the D2 regression guard.
Mutation: restoring the artifact check must fail the first; dropping `running`
must fail the second. `deferred.md`'s `audit 7 / K3` is marked no longer
deferred.

---

## CHANGELOG

`### Changed`, three entries: `resolveInitiative` refuses an unknown reference
on 16 commands; `idea status` and `idea changed` take an argument and refuse an
unknown one (`idea status ZZZZ` goes 0 → non-zero), and `idea changed` acts on
the newest initiative rather than the oldest; `idea new`'s final line reflects
the terminal state.

## Order

R1/R2 → P1-7 (the first row written under them) → P1-1 → P1-6 → P1-5.

The draft put P1-6 second on a "touches twenty call sites" argument; the change
is a five-line guard in one function. What actually forces order is writing
rows: three rows under the old rules and then migrating them is avoidable work.

Each: mutation-checked tests, adversarial review before merge, merge on green.
