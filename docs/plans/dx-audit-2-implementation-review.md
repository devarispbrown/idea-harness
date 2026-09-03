# Implementation review — DX Audit 2 fixes

Pre-merge adversarial review of `worktree-dx-audit-2`, per CONTRIBUTING §75
(every change set is independently reviewed). Verdict: **reject** — 3 blocking,
9 should-fix/nit. All resolved in `58a7ffa`.

The reviewer independently verified the parts that matter most and found them
correct: all 24 flag-accessor call sites match their table rows, positional
semantics survive the Track A refactor in all six delegating commands, C5's
rename is right across all 42 routes with the non-initiative `{id}` handlers
correctly untouched, `writeErr`'s branch ordering is right, and all 16 CLI
acceptance criteria pass against a built binary.

## Blocking

### 1. A 32 MB compiled binary in the history

`go build ./internal/docsgen` in the repo root wrote `./docsgen`, and `git add -A`
committed it — twice, since the second commit carried a full second copy.
Nothing needs it; the Makefile uses `go run`.

**Resolved:** stripped from both commits with `filter-branch` while the branch
was unpushed, and `/docsgen` added to `.gitignore`. Verified: no blob over
200 KB remains in `main..HEAD`.

### 2. C1's fix shipped with the docs still describing the removed behaviour

`docs/api.md` said the risk read was "persisted as a side effect" and
`openapi.yaml` said the profile is "computed and stored" and "**persisted as a
side effect of this GET**".

This is the branch reproducing the exact gate it exists to clear. A reader would
still believe `idea risk --json` in a cron job writes to the database. Neither
new guard could catch it — `api-docs-check` proves row *existence* by design,
and `runBodyCheck` reads request bodies only.

**Resolved:** both claims corrected to say the read writes nothing, with a note
on what changed and why.

### 3. Four operations began returning 409 without declaring it

`executeInitiative`, `revertChangeSet`, `advanceInitiative` and
`reviseInitiative` all gained a 409 in this branch. `approvePlan` and
`revisePlan` — the siblings whose inconsistency this branch fixed — already had
one. So the same propagation failure, in the commit that fixed it elsewhere.
`api-check` compares paths, so it was invisible.

**Resolved with a guard rather than four lines.** `api-check` now derives which
operations *can* answer 409 — a handler writing `http.StatusConflict` directly,
or calling a Runtime method whose body contains `preconditionf` — and fails when
one does not declare it. Mutation-checked.

## Should-fix

### 4. `runBodyCheck` failed open

`bodyFieldsIn` only recognises `var body struct{…}`. The reviewer demonstrated
two evasions that passed green: a named struct type, and `body := struct{…}{}`.
In both, the handler was silently treated as reading no body while the check
announced that every handler's fields were documented.

**Resolved:** the decode call is detected independently of the shape decoded
into, and a shape the check cannot read is now an error naming the handler.
Verified against the reviewer's own evasion.

### 5. `NextAction` swallowed storage failures as `"verify"`

Every error from `nextReadyTask` became a successful `"verify"`. Its only
non-storage failure is "no ready tasks" — which is exactly why converting all of
them was wrong: a SQLite I/O error would answer `200 {"action":"verify"}`, and
`idea next --until-human` would send the operator to debug verification.

**Resolved:** `ErrNoReadyTask` sentinel; anything else propagates.

### 6. C6's escaping stopped at one resource

`initiativeResource` was applied everywhere for initiative paths, but change-set
and operation ids stayed raw — and the source guard's regex watched
`"/initiatives/"` alone, so it could never have noticed. Worse than the
initiative case, which only 404s: `idea change 'CHG-1/diff'` reached a real,
different endpoint and printed a blank change set with exit 0.

**Resolved:** `changeSetResource` and `operationResource`; the guard's regex
covers all three; lines that already escape are exempted rather than the file.
Verified live — both now exit 1.

### 7. Nothing checked code → spec for flags

A5 renders spec → docs, and the unknown-flag test proves argv → spec. Neither
notices a command calling `a.value("--foo")` where its row declares no `--foo`:
it reads `""` from a nil map, silently, with every check green.

**Resolved:** an AST test walks every `has`/`value`/`list` string literal in the
package and asserts membership in some row's spec. 38 reads against 21 declared
flags. Mutation-checked.

### 8. Subcommand names were still hand-maintained

`flagDoc` rendered subcommand *flags*, so a subcommand with none contributed
nothing — `daemon restart` could have been added, dispatched, and documented
nowhere with `docs-check` green.

**Resolved:** names render too.

### 9. A hand-maintained route count, reintroduced

`architecture.md` gained "81 operations" — two commits after the plan said to
delete exactly that from `api.md` because it "would go stale exactly as it just
did".

**Resolved:** deleted; "CI-checked against the routes" carries the meaning.

## Nits resolved

- `jsonDoc` returned `"no"` for `model`/`config`/`daemon`, whose prose definition
  says the command takes flags. They take none; the cell now says so, and the
  prose distinguishes a command's own flags from its subcommands'.
- `execute`'s precondition accepted only `plan_approved` where three siblings
  accept `needs_human_judgment` too; its 409 now names the remedy.
- `docRow.section` and `parseAPIDocs`'s returned lines were dead.
- README now mentions `executors setup --smoke`.

## Accepted as-is

- **C3's acceptance test differs from the plan.** The plan promised a test that
  all 14 `StatusBadRequest` sites still return 400; what landed tests the
  `writeErr` mechanism with synthetic errors. The reviewer judged this the
  better invariant, and it is — but the plan's claim was inaccurate and is
  recorded here rather than quietly left.
- **B9's test drives `watchModel.poll` rather than `watchInitiative`.** The
  plan's version would have been vacuous, since Bubble Tea exits immediately
  without a TTY; `watchInitiative`'s `return m.fatal` stays uncovered.
- **`risk_read_only_test.go` asserts the activity feed only**, not row counts.
  Non-vacuous (mutation-checked), but `handleActivity` caps at 200 events, so a
  much larger seeded project would make it tautological.
- **`SaveRiskProfile` has no callers.** Deliberate per the plan, documented in
  place, and will rot silently.
- **`macos-15-intel` is unverifiable from here.** With `fail-fast: false`, a bad
  runner label fails that job while the workflow's other jobs pass. Confirm on
  the first CI run.
