# The third live-lock: a change set the reviewer will not pass

Measured on `HEAD` (19 commits after audit 7) with a real key,
`openai/gpt-4o-mini`, guided path, **0/3**, 84–94s each. The plan-review
live-lock is genuinely fixed — every run now gets past it, accepts the plan,
executes, and produces CHG-1 — and then stops:

```
$ idea next                    → Next: review CHG-1 — idea review CHG-1 approve
$ idea review CHG-1 approve    → rc=1  approval blocked: system deny
                                       review_open_blocking (open high finding)
$ idea apply CHG-1             → rc=1  needs approval first: `idea review CHG-1 approve`
```

Every other command refuses: `approve-plan` ("already approved"), `retry`
("requires a failed initiative"), and `revise-plan` — whose message, written in
this round, says *"`idea review <CHG-n>` then `idea apply` is the way on"* and
so points at the blocked route.

This is the third instance of one shape. Audit 6 found it on the PRD review,
audit 7 on the plan review, and this is the change-set review: **the command
offered as the escape re-runs a model reviewer, and a model reviewer is not a
fixed point.**

## Mechanism

`Engine.ReviewChangeSet`'s `approved` arm (`execute.go:1199-1226`) has two
gates:

1. `denyReviewOpenBlocking` (`:1169`) — refuses if any *prior* undisposed
   blocking finding targets the change set.
2. `EvaluateApproval` (`:1221`) — runs a **fresh** adversarial review and
   refuses on its findings, which are persisted first (`:1213-1219`).

So the first `idea review CHG-1 approve` runs the reviewer, gets a `high`
finding, persists it, and fails gate 2. Every later attempt fails gate 1 on the
finding the first attempt wrote. Retrying is not merely unreliable here — after
the first attempt it is *guaranteed* to fail, and it fails on a gate whose
comment says "non-overridable means non-overridable".

## What the plan review rejected

Reviewed before implementation and **rejected**. The central claim was false.

The draft said *"exactly one thing in the tree creates a change-set-targeted
finding … the system denies do not produce change-set findings."* There are
**five** producers, four of them system-originated, and `docs/security-model.md`
documents them in a published table I did not read:

| `execute.go` | producer | `Code` | severity | disposition |
|---|---|---|---|---|
| `:390` | Execute secret sweep | `secret_sweep`/`pii` | critical | `rejected` |
| `:765` | `checkPatchApplies` | `blob_digest_mismatch` | high | **none** |
| `:784`→`:830` | `checkCommand`, sandbox unavailable | `host_exec` | high | `rejected` |
| `:784`→`:840` | `checkCommand`, exit ≠ 0 | **none** | high | `rejected` |
| `:959` | apply-time re-sweep | `secret_sweep`/`pii` | critical | `rejected` |
| `:1215` | adversarial reviewer | **none** | model's | none |

The error was a search artifact: I grepped `TargetType: "change_set"` with one
space, and the struct literals are column-aligned, so four of five producers
were invisible and I reported the absence as a verified fact. `web/src/BuildPage.tsx:140`
already renders these findings — the UI knows about the thing the draft said
could not exist.

**Latent, not live.** Every system producer that sets a blocking severity also
drives the change set to `rejected`, and the `approved` arm refuses any status
but `review_required`. So no such finding is reachable there today. But the
invariant is undocumented, enforced 800 lines from the gate, and asserted by no
test — `grep denyReviewOpenBlocking **/*_test.go` returns nothing — so a fix
built on it would be one literal away from letting a human override the
credential sweep.

## The distinction the fix turns on

Two predicates, both required, because either alone is defeated by a real
producer:

- **`Code == ""`.** `review.ParseFindings` constructs findings without a `Code`;
  every system producer that carries a deny code sets one. A finding carrying a
  `DenyCode` is a system deny and is refused, naming the code.
- **`Disposition == ""`.** The `checkCommand` exit≠0 finding carries **no code**
  and is `high` — indistinguishable from a reviewer finding by code alone — but
  it is dispositioned `rejected`. A failing build is not a judgment a human may
  accept away.

This is the change-set analogue of `hasOpenStructuralPlanFinding`, which refuses
`approve-plan` over a `deterministic-plan-check` critical: the same shape, one
gate over, and the reason `approve-plan` needed two gates rather than one.

`denyReviewOpenBlocking` is **re-run after** the disposition rather than
skipped, so anything the flag declined to touch still blocks. That ordering is
what makes the predicates load-bearing instead of decorative — and the draft,
which listed three steps and never said what happened to that call, would have
produced a flag that does nothing, since the gate runs first.

## The fix

**`idea review <CHG-n> approve --accept-findings`**, mapping to
`{"verdict":"approved","accept_findings":true}`.

With the flag, `ReviewChangeSet`'s `approved` arm:

1. dispositions the findings targeting this change set that satisfy **both**
   predicates above as `accepted_risk` with `store.HumanActor` — the one
   disposition the reviewer is forbidden to set, so it is unambiguously a
   human's signature;
2. **re-runs `denyReviewOpenBlocking`**, so anything it declined to touch still
   refuses, naming the deny code;
3. **does not run a fresh adversarial review**, and does not call
   `EvaluateApproval`;
4. transitions `review_required → approved`.

Step 3 is the lesson of the two previous rounds: dispositioning and then
re-reviewing is a retry, not an accept. `ApprovePlan` made exactly that mistake.

Without the flag, behaviour is **unchanged**: run the reviewer, approve if it
passes. That is what `idea review CHG-1 approve` means, and the no-key path the
demo guides walk keeps working.

### A new store method is required

`DispositionOpenFindings` is `WHERE initiative_id = ?` — initiative-wide, no
target filter — so it cannot satisfy "findings targeting the initiative are
untouched". A loop of `SetFindingDisposition` is not atomic, and `ApprovePlan`'s
own comment reasons about which inconsistency survives a partial write; N events
makes that worse.

So: `DispositionFindingsByTarget(ctx, initiativeID, targetType, targetID,
disposition, actor)` — one event, target-scoped, carrying the
`accepted_risk ⇒ HumanActor` check that `SetFindingDisposition` already enforces
and `DispositionOpenFindings` does not. Without it the "human's signature" claim
is unenforced.

### `review_id` is deliberately left empty

Skipping the review means there is no run id to stamp, so `ChangeSet.ReviewID`
stays empty and the change set reads as never-reviewed. That is accurate: no
review passed. The provenance is the event log — a `change_set.transitioned`
carrying `HumanActor` — and that is what the audit trail rests on.

### The surface

A **boolean**, not a fourth verdict. `enum-check` compares each named spec enum
to the domain constants of the same name, and `verdict` is a documented past bug
(the spec listed `approve`/`reject` where the engine takes
`approved`/`rejected`). A fourth value would need a domain constant and would
make "approve" and "approve-and-accept" two verdicts, which is not what the
state machine does. An inline request-body boolean is invisible to `enum-check`;
`bodycheck` and `api-docs-check` are what bite.

`handleReviewChangeSet` decodes **leniently** today. Left that way, a new CLI
against an older daemon has the flag silently dropped and the user gets the
identical refusal while passing the documented escape — the failure mode
`decodeStrict` was added for on two other endpoints last round. So this handler
routes through `decodeStrict`, and an old daemon answers
`400 json: unknown field "accept_findings"`, which is diagnosable.

## Making it reachable

A hidden escape is not an escape, and the draft counted three sites. There are
more, because the CLI's flag table is machine-read:

| Site | Why |
|---|---|
| `internal/cli/cli.go:131` | `review` is declared `flags: flagSpec{}` — the parser **rejects** an unknown flag before `cmdReview` runs, so the flag is unreachable until it is declared |
| `internal/cli/change.go:189-214` | `cmdReview` reads only `a.positional` and posts a `map[string]string` |
| `docs/cli.md:48` | says `takes no flags`, and is **generated** — `make docs` |
| `internal/cli/next.go:236` | the `review <CHG-n>` arm is a single line; it gains the second, the way the judgment arms already offer two commands |
| `internal/workflow/execute.go:1180` | the refusal names no route today |
| `internal/daemon/runtime.go:1107` | `revise-plan`'s change-set refusal says `idea review <CHG-n>` then `idea apply` "is the way on" — true only when the reviewer passes |
| `docs/demo-greenfield.md`, `docs/demo-existing-project.md`, `README.md` | all print `idea review CHG-1 approve` as the guided step and need the "if the reviewer blocks" line |
| `docs/security-model.md` | its `review_open_blocking` row becomes conditionally overridable; it is a `claims-check` canonical document |
| `docs/configuration.md`, `internal/config/config.go`, `internal/daemon/runtime.go` | four places assert *"a blocking finding on a change set cannot be cleared by anything the CLI can do"* — the sentence this change falsifies |

**The web UI.** `BuildPage.tsx` renders three verdict buttons and, when approve
400s on this deny, offers nothing — unlike the deny-code case, which it handles
with an explicit panel. Shipping the CLI flag alone recreates the asymmetry this
round has closed twice. It gets an "Approve, accepting the findings" affordance
behind the same predicates, or a `deferred.md` row with the reason. **In scope**:
the goal is shareable, and a person handed this is as likely to open the UI.

## Acceptance

| # | Assertion |
|---|---|
| 1 | With a reviewer that blocks every time, `idea review CHG-1 approve --accept-findings` reaches `approved`, and `idea apply` then succeeds |
| 2 | It makes **no** model call — asserted on `FakeProvider.Calls()`, not inferred. This is what separates an accept from a retry |
| 3 | Findings targeting the change set end `accepted_risk`/human; findings targeting the **initiative** are untouched |
| 4 | A finding carrying a `DenyCode` is **not** dispositioned, and the request is refused naming the code |
| 5 | A `rejected`-dispositioned blocking finding (a failing build check) is not dispositioned either, and still refuses |
| 6 | Without the flag, a blocking review still refuses — the flag is the only override |
| 7 | The refusal names the flag |
| 8 | `--accept-findings` on a change set that is not `review_required` is refused |
| 9 | After the accept and the apply, `hasOpenBlockingFindings(initiative) == false` — asserted on the store |
| 10 | The keyed guided path reaches `✓ initiative complete` **3/3** on `openai/gpt-5.6-luna` |

Assertion 9 is there because of the **fourth gate the draft never named**:
`verify.go` refuses `complete` while any initiative-wide undisposed blocking
finding exists, and the reviewer's change-set findings carry the initiative id,
so they are in scope. `accepted_risk` clears them — but only if the disposition
actually writes with the right scope. Mis-scope it and the initiative reaches
`approved` and then wedges at `complete`: a fifth live-lock one gate on, which
assertion 9 catches at the gate it breaks rather than three commands later.

Assertion 10 is the gate. It is the measurement that has failed every round, and
the only one that decides whether this is shareable. The measured script must
include the human verification step (`idea verify <REQ> --pass`), which is a
documented human decision, not a lock.

**The tests go through `Runtime`/HTTP, not `Engine` directly.** The plan-accept
work records why: a workflow-level test has a blind spot for exactly this class,
and a CAS bug that would have failed 100% of initiatives passed a direct engine
test. And the demo queue is sized **≥50, not 1** — on a converging queue a pass
is indistinguishable from the queue draining.

**Mutation checks.** Removing the disposition; re-adding the fresh review;
widening the disposition to initiative findings; dropping the `Code` predicate;
dropping the `Disposition` predicate; and skipping the re-run of
`denyReviewOpenBlocking` must each fail their assertion.

## Demo oracle

`demo.block_review_cycles` and `block_plan_review_cycles` exist because the demo
reviewer is a happy-path oracle. There is none for the **change-set** review,
which is why five smoke passes and every audit missed this.

The change-set review calls the model with schema name `"review"`
(`execute.go:1307`) — shared with the PRD review, the hazard the plan-review
queue was split to avoid. So it gets its own key, `"change_set_review"`, and a
`demo.block_change_set_review_cycles` knob.

**The rename is not free, and the draft treated it as a config addition.**
`demoResponse` switches on the same name and handles `case "review",
"plan_review"`, defaulting to `{}`. Rename the call site without extending that
switch and the demo provider answers the change-set review with `{}`,
`ParseFindings` returns `findings array missing`, and **`idea review CHG-1
approve` fails on the default no-key path** — the path both demo guides walk and
five smoke passes exercise. That is a P0 regression introduced by the fix's own
test scaffolding.

The rename is safe on the wire: `JSONSchemaName` is read by nothing but the fake
provider, and real providers never see it — stated at `advance.go:557`, and the
reason the plan-review key could be split at all.

Sites: `internal/model/fake.go` (the `demoResponse` switch),
`internal/config/config.go` (field + doc), `internal/config/schema.go` (the JSON
schema — `config.Load` validates strictly, so an undeclared field is a load
error), `internal/daemon/runtime.go` (the enqueue loop), `docs/configuration.md`
(the knob table).

Then a smoke pass that blocks the change-set review ≥50 times and asserts the
accept reaches `complete`. Without it the fix is verified only by a keyed run,
and the next round rediscovers this the same way.

## Model examples in the docs

The house model is `openai/gpt-5.6-luna`. The docs say `openai/gpt-4o-mini`, and
audit 7's documentation dive found ten sites carrying model ids that no longer
resolve — `anthropic/claude-3-7-sonnet-latest` is absent from the live catalog
and `gemini/gemini-2.5-flash` answers 404. Someone handed this repo copies those
lines.

In scope, because "shareable" is the goal: the examples move to ids verified
against `idea model ls` at the time of the change, and the ledger row carries the
verification date, since a model id is a fact with a shelf life.

## Out of scope, recorded

A general `idea findings <id> <disposition>` command would close the broader gap
— nothing in the CLI dispositions a finding. `deferred.md` **already carries
that row**: `audit 6 / D1`. It is updated in place, noting the flag closed the
change-set slice and what remains, rather than gaining a sibling — which is the
drift `runCoverageCheck` exists to prevent.

## Bookkeeping

- **CHANGELOG**: `### Added` for the flag and the knob — no-flag behaviour is
  unchanged. Plus a `### Changed` for `docs/security-model.md`, whose
  `review_open_blocking` row becomes conditionally overridable.
- **Ledger**: a row in `dx-audit-7-doc-defects.md` under the new R1/R2 rules —
  paired `present:`/`absent:` predicates, and the predicate must name a file the
  row cites. Not a `manual:`.
- **Migrations**: none. `findings.disposition`/`disposition_actor` already exist.

## Order

1. The demo knob and the schema-name split, including the `demoResponse` switch
   — so the fix can be tested without a key, and so the no-key path does not
   regress.
2. The store method.
3. The engine change and the two predicates.
4. The flag's CLI surface, the spec, and `decodeStrict`.
5. The message and doc sites, including the web UI.
6. Smoke pass, then the keyed re-measure on `gpt-5.6-luna`.

---

## Result

Measured on `HEAD`, `openai/gpt-5.6-luna`, guided path following the product's
own instruction at each stop:

| run | result | elapsed | commands |
|---|---|---|---|
| 3 | **PASS** `✓ initiative complete` | 369s | 37 |
| 4 | **PASS** | 844s | 37 |
| 6 | **PASS** | 398s | 33 |
| 5 | not counted — `read: operation timed out` to `api.openai.com` mid-run | 585s | 6 |

**3/3 on completed runs**, against 0/3 before. Run 5 died on a network read
timeout, twice seen this session and unrelated to the product; the error names
its cause and `idea retry` as the recovery, which is the error work behaving
correctly.

Two things the measurement surfaced that are not locks and are worth recording:

- **Command count.** `MAGIC_PATH_MAX_COMMANDS` is 8. These runs take 33–37,
  because `gpt-5.6-luna` produces ~38 requirements for one idea and
  `idea verify <REQ> --pass` is one command per requirement. The gate is time,
  which passes comfortably, but the P2 command-count target does not — and the
  first draft of the measurement script mistook this for a product stop, because
  it signed off only the first requirement and then ran out of loop turns.
- **Elapsed spread.** 369–844s on the same input. All well inside the 15-minute
  gate, but a reader told "about two minutes" by the demo guides is reading the
  no-key number.
