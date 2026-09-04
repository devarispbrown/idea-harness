# DX Audit 4 — Fix Plan (v2)

Branch: `worktree-dx-audit-4`, from `main` @ `39ca31a`.

Audit 4 verdict: **FAIL**, Overall DX **79/100** (from 74, from 70). Both hard
gates pass; six of nine areas clear the world-class floor. Report:
`docs/plans/dx-audit-4-report.md`.

**v2 supersedes v1 after three adversarial reviews** (security, Go correctness,
coverage/ledgers). They found **six blocking defects in the plan**, two of which
would have shipped new P0s — and one blocking defect in the *code* that neither
the audit nor the plan had found. Corrections are folded in and marked
**[v1 was wrong]**. Review record: `dx-audit-4-fix-plan-review.md`.

## Already fixed on this branch

Three items landed ahead of the rest because they are live defects, not planning
questions:

| Commit | What |
|---|---|
| `39ca31a` | **P0-1**: cancellation left the initiative claimed forever (fixed during the audit) |
| `0ffb976` | **A credential-egress bug neither audit found**: the raw idea text was sent to the model provider while only the stored copy was redacted |
| `39cb442` | **A P0 on `main` that no audit found**: a *successful* `idea execute` left the initiative claimed forever |
| `ebc5245` | O2's doc half — nine model ids that resolve to nothing |

### The Execute stranding, and why every gate was green

Surfaced by the Go review, not by the audit. `ExecuteInitiative` claimed the
initiative and never released it — on success *or* ordinary failure. Verified
against a live daemon: after a successful `idea execute`, status stayed
`running`, `idea next` answered "workflow running" indefinitely, and every later
command answered "already running".

Mine, from the audit-3 round. I added the claim to all seven guarded entry
points, which was right, without checking each had a release. Six do, as a side
effect — their engine calls reach a terminal status inside `internal/workflow`.
Execute's does not.

**`scripts/smoke.sh` and the audit journey both run
`execute → review → apply → revert`, and `RevertChangeSet` sets the initiative
status as a side effect** — so both harnesses reset the exact state the bug
corrupts, one step after corrupting it. Every gate passed. A real multi-task
initiative that applies without reverting is stuck, and neither harness has that
shape.

That is the fact this round's structural work has to answer, and it changes the
sequencing below.

## The pattern, and what actually catches it

| Audit | Pattern |
|---|---|
| 2 | A fix made correctly in one place, not propagated to its siblings |
| 3 | The canonical source corrected, the derivative copies left stale |
| 4 | The correction reaching two of three documents — and a claim added to seven call sites with a release checked on none |

**[v1 was wrong about the track record.]** v1 implied the guard-building has not
worked. Review corrected it: of audit 3's three guards, **two were built** —
`api-docs-check` and `conflictcheck` both run in CI — and only G1b was not. The
failure was the *claim line* asserting all three were checked, not build
capacity. That distinction matters, because it means the fix is honesty about
scope, not more ambition.

---

## Track C — code, and it goes first

**[v1 sequenced this last. It goes first now]**, because C1 as written would have
broken every `apply` in the tool, and the reason is a code fact v1 did not know.

### C1 (P1). Apply and revert do not claim the initiative

**[v1's fix would have 409'd every apply.]** Review established the ambient
status at apply time *was* `running`, left there by the Execute stranding — so a
claim whose source-list excludes `Running` (as every claim's must) would have
refused the single most basic flow in the tool. That is fixed now (`39cb442`),
which is why C1 can proceed at all.

Two further corrections:

- **The hazard is narrower than v1 said.** `ApplyChangeSet`, `RevertChangeSet`
  and `Execute` already take the same engine mutex, so they are *already*
  mutually exclusive. `RevisePlan` and `ReopenPhase` refuse once any change set
  exists. **The one real gap is `Revise`**, which supersedes requirements
  concurrently with an apply — a provenance race, not filesystem corruption.
- **Apply's success path has no status write at all.** Adding a claim without
  adding a release would create a second instance of the exact P0 above, in the
  same round that fixed it.

**Fix.** Claim in both, from the states each is legitimately reachable from, and
**add an explicit release on every exit of both** — success and failure — to the
status held before the claim. `RevertChangeSet` currently forces `plan_approved`
regardless of the prior status; that is preserved but commented, since changing
it is a separate behavioural question.

*Acceptance:* a test drives apply from each reachable state and asserts neither a
spurious 409 nor a lingering `running`. The `-race` suite covers the concurrency.

### C2 (P1). `failOperation` and `failOperationAfterPanic` write unconditionally

**[v1's fix would have re-opened P0-1.]** v1 said "make both conditional on the
operation still being non-terminal". `CancelOperation` writes `cancelled` — a
terminal status — *before* the goroutine unwinds, so gating `failOperation` would
skip the `FailInitiative` call that `39ca31a` added, and cancellation would
strand the initiative again. v1 also cited `CancelOperationStatus` as precedent
while proposing to invert the design decision that precedent encodes, and
contradicted a comment written last round for exactly this reason.

**Fix, asymmetric, because the two functions are not the same case:**

- **`failOperationAfterPanic`**: gate the operation write *and* the initiative
  release on the same CAS outcome. A panic after a successful terminal write must
  leave both alone. Gating only the operation write — the obvious patch — would
  leave a self-contradictory record: operation `succeeded`, initiative `failed`,
  and a user running `idea retry` on a run that actually worked.
- **`failOperation`**: gate only the operation-status write. `FailInitiative`
  stays unconditional, because the cancel path reaches it with the row already
  terminal, and that is the path P0-1 was about.

*Acceptance:* the existing `TestCancellingAnOperationReleasesTheInitiative` must
still pass, plus a new case driving `CancelOperation` → goroutine-unwind in that
order, which the existing test does not do.

### C3 (P1). The providers endpoint masks "written but not applied"

Confirmed correct by review, no changes. `handleUpdateProviders` sends the reload
failure to `writeErr`, whose storage branch masks it to a generic 500; the
executors endpoint calls `writeReloadErr`. Route the providers endpoint through
`writeReloadErr` and extend the leak test to both.

### C4 (structural). Terminal-path enumeration — **split, and honestly scoped**

**[v1 over-promised.]** Review established that cancel, fail and panic are cheap
to test by direct state manipulation — the shape
`TestCancellingAnOperationReleasesTheInitiative` already uses — but **success is
not centralised**: each closure inlines its own terminal write, and for six of
seven entry points the release happens inside `internal/workflow`. A "table of
entry points" built the cheap way would have **passed while the Execute stranding
was live**, which is the same vacuity trap as last round.

- **C4a — buildable now.** An AST test enumerating every `launchOperation` call
  site in `internal/daemon/runtime.go` and asserting each appears in the
  invariant table. This catches the *forget-a-new-entry-point* class outright,
  and it is the half that would have flagged Execute as untested.
- **C4b — cancel, fail and panic per entry point**, driven by direct state
  manipulation. Real coverage for three of four terminal paths.
- **C4c — the success path — deferred, in the same commit.** It needs
  Runtime-level fixtures (a fake executor as well as the fake provider) that do
  not exist in the tree. Recorded in `deferred.md` with that reason, not implied
  as covered.

C4a is what makes the deferral safe: a new entry point cannot silently join
without appearing in the table.

### C5 (from review). `SuggestAnswersInitiative` — the third bypasser

Review found v1 named two claim-bypassing entry points; there are three.
`SuggestAnswersInitiative` has no existence check and no claim — already recorded
in `deferred.md` as audit-3 C4, and C4a will fail on it.

**Fix:** give it the existence check and the claim, so C4a ships green rather
than with an exemption. If that proves larger than it looks, C4a ships with a
one-entry exemption list naming the `deferred.md` row — never a silent skip.

---

## Track S — security documentation

### S1 (P0). `configuration.md` promises redaction that does not happen

> *"Secret values are redacted before any persistence (`[REDACTED]`), including
> logs, events, blobs, and context bundles."*

Review traced all four, which v1 had not:

- **Logs** — not redacted. No redacting handler; `GET /api/v1/logs` returns the
  log verbatim.
- **Blobs** — two stores. `blobs/` redacts; `raw-blobs/` is constructed with a
  nil redactor and holds unredacted file content and raw image bytes.
- **Events** — technically true, but only because event rows carry a digest into
  the already-redacted blob store. Not an independent choke point.
- **Context bundles** — same: the persisted record is a digest.

**[v1's replacement text was incomplete.]** The sentence's deeper problem is that
it invites the reader to assume redaction happens before the *model call*. It
does not, uniformly — it is applied field by field. That was not a wording
problem: `StartInitiativeWithDocs` was sending the raw idea to the provider,
fixed in `0ffb976`. The prose could not have been made honest while that line
existed.

**Fix:** state the boundary in both directions — what redacts before persistence,
what does not (logs, the raw store), and that redaction is not a uniform
pre-egress gate — and link to `security-model.md` rather than restating.

### S2 (P1). "Review runs require a distinct actor" is enforced nowhere

**[v1 scoped this to one sentence. There are six, across four files]** —
`security.md`, `architecture.md`, `quickstart.md` (×2) and `README.md` (×2).
Fixing one and calling S2 done reproduces the pattern this plan is named after.

Review also confirmed there is no actor identity to enforce against: two
constants exist system-wide, and every automated write uses the same one. So the
enforcement stays deferred — but **[v1's replacement wording was too generous]**.
"A separate reviewer run that is structural" implies a weaker real separation.
There is none: nothing prevents `implementer` and `adversarial_reviewer`
resolving to the same model.

**Fix:** say plainly that nothing prevents the two roles being the same model,
and that the only enforced separation is that a human, not the model, issues the
approval verdict — which is true and is enforced.

### S3 (P1). `architecture.md` carries the inverted redaction claim a third time

Planned last round and never done; one of the three items `deferred.md` omits.
Also add the raw store to its data tree, and — per review — correct its **SSE**
claims, which contradict the newly rewritten `ui.md`.

### S4 (P1, from review). The capability broker is documented as shipped

`security.md` lists it among five features that "all five shipped in 0.1.0".
`CapabilityBroker.Authorize` has **zero non-test callers**; the actual gate is
`TrustAllows`. Not an active vulnerability — `TrustAllows` is fail-closed — but a
false "shipped and active" claim about a named component in the canonical
security document. **[v1 filed this under bookkeeping]**; it belongs here.

### S5 (structural). The security-claim lint — **redesigned**

**[v1's design was not buildable and was self-certifying.]** Review's objections,
all accepted:

- Keyword scanning natural language is a different kind of problem from every
  other check in `internal/docsgen`, which parse structured artifacts. `token`
  is majority-noise: most occurrences are `review.max_tokens` or "command token".
- **Assert-plus-link passes.** A file could restate the same false guarantee and
  add a link and go green.
- **It is self-certifying**: written in the same track, same round, by the same
  author as the doc edits it judges. The allowlist would be tuned until the new
  wording passed. That is a snapshot test.

**Redesigned, judgement outside the checker.** Each canonical property sits in a
marked block in its canonical file:

```
<!-- claim:redaction -->…<!-- /claim -->
```

The check is then mechanical and needs no NL classification: **any other
published doc containing a keyword from a fixed list must link to that anchor.**
Deterministic, one-line fix on failure.

Properties this round: **executor isolation**, **redaction**, and
**reviewer/implementer separation** — the third added because review found it
falsely replicated across four files, and v1's table would have been blind to
exactly the claim this round is correcting. "Image handling" and "the token
model" are dropped: neither corresponds to an audit-4 finding, and each adds a
file-set to migrate.

*Scope, stated plainly:* it enforces that a property is stated once. It cannot
judge whether the statement is true.

---

## Track O — onboarding

### O1 (P0). The documented with-key path dead-ends

`idea init` starts the daemon, which inherits the environment at that instant.
The quickstart and README then say to export the key — but keys resolve inside
the daemon, and `idea model set`'s reload re-reads the daemon's environment.
Result: a non-retryable 401.

**Doc half:** put the export before `idea init` in both files; where the key
arrives later, say `idea daemon stop` and why; correct the warning box, which
attributes the failure to a different cause.

**Product half — [v1 hand-waved the mechanism].** Review showed it needs no
cross-process introspection: the CLI can read its *own* environment with
`os.Getenv(keyEnv)`, and `GET /providers` already reports the daemon-side
`Ready`/`KeyEnv`. The two facts just need comparing after the PUT. Today
`setModel` discards the response entirely, so that is the change: read the
updated provider back, and when the caller's environment has the key and the
daemon's does not, say so.

### O2 (P1). Model ids that resolve to nothing

Doc half landed in `ebc5245` — nine ids, including one the audit missed.

**Product half — [v1's version would have been inert].** Review found
`handleUpdateProviders` drops every non-error problem before it is returned, so a
`SeverityWarn` catalog mismatch would never reach `idea model set`'s output — the
exact case the finding opens with. So either the response carries warnings and
the CLI prints them, or this item does not fix its motivating scenario and the
plan says so. **Taking the first.** `openrouter` is excluded from the check: its
ids are arbitrary upstream strings and would misfire constantly.

---

## Track R — reference docs

| # | Finding | Fix |
|---|---|---|
| R1 | The SSE claim `ui.md` dropped survives in **four** files, not two — `api.md`, `cli.md`, and `architecture.md` twice. `security.md`'s mentions are correct (the endpoint exists) | correct all four; S3 owns the `architecture.md` edit |
| R2 | `openapi.yaml` says operations are "newest last"; the store orders `DESC` | correct the spec |
| R3 | `required` arrays emitted as `null`. **[v1 said "at the handler"; three of four are not]** — `Why`/`Impact` are in `internal/workflow/trace.go`, understanding's four in `internal/projection`, only `/models` is handler-local. Review also found a fifth v1 missed: `/guidance`'s `affected` | initialise at the source |
| R4 | `api.md` claims a handler "cannot accidentally report one as a 500" | true only after C3; sequence after |
| R5 | **[v1 overstated this.]** The changelog records most of audit 3's code round. Three specific gaps: `idea verify`'s exit code, disposition's 404 (BREAKING), and `docs-lint` missing from the guards list | add the three |
| R6 | `openapi.yaml`'s `Coverage` omits `status` | add |

---

## Track B — bookkeeping

### B1. The ledgers are wrong, and `ledger-check` as specified cannot see it

**[v1's guard was unimplementable.]** It was specified as "fails when a row marked
`open` names a file:line that no longer contains the claimed text". The defects
file contains **zero** `file:line` references — so the check would be **green on
both ledgers today, certifying the exact stale state it exists to catch**. That
is audit 3's G1b again: a guard whose specification cannot reach its finding.

**Fix, format first.** Every row gains a predicate column from a closed set:
`absent:<path>:<literal>`, `present:<path>:<literal>`, or `manual:<reason>`. The
check then runs symmetrically — an `open` row whose defect is gone fails
(stale-open), a `fixed` row whose defect returned fails (regression), a `manual`
row asserts only that its path exists, and the manual count is printed so the
un-checkable fraction is visible.

That is a migration of 27 rows, which is real work and is stated as such.

Review's counts, to be applied: **21 of 27 rows are fixed**, not "at least 17";
six survive (A2, A6, H1, H2, M1, I1), of which H1/H2 close under S3.

### B2. `deferred.md` omits three planned-and-undone items

`architecture.md`'s redaction half, the trust-level code change, and the stale
`routes.go` comment — which has now survived two rounds with no owner in either.

### B3. The plan contradicts `deferred.md`

Audit 3's plan says "Mechanically checked: C1–C7"; `deferred.md` records C5, C6
and part of C4 as not attempted. **Review's proposal, accepted:** generate that
block rather than reconcile it by hand, so the contradiction cannot recur. Cheaper
than `ledger-check` and higher value.

### B4 (from review). Create `dx-audit-4-doc-defects.md`

The dives produced ~10 unranked documentation findings. **v1 created no ledger for
them** — the identical failure `dx-audit-3-doc-defects.md` exists to stop, in the
round whose theme is bookkeeping. Seeded in the new predicate format before
anything else in B.

---

## Ownership

**[v1's table asserted a disjointness it did not have]** — review found four
items landing in files no track owned. The tracks are **serial**, so this is a
forgetting risk rather than a conflict risk, and the table is informational:

| Track | Primary files |
|---|---|
| C | `internal/daemon/`, `internal/workflow/`, `internal/store/` |
| S | `docs/security*.md`, `docs/configuration.md`, `docs/architecture.md`, `internal/docsgen/` |
| O | `README.md`, `docs/quickstart.md`, `internal/cli/`, `internal/config/` |
| R | `docs/api.md`, `docs/cli.md`, `docs/openapi.yaml`, `internal/projection/`, `CHANGELOG.md` |
| B | `docs/plans/`, `internal/docsgen/` |

---

## Verification

```
make test && make vet && make docs-check && make api-check \
  && make api-docs-check && make docs-lint && make claims-check \
  && make ledger-check && make check-embed \
  && go test -race ./internal/daemon/... ./internal/store/... \
  && bash scripts/smoke.sh \
  && (cd web && pnpm build && pnpm test)
```

`claims-check` and `ledger-check` are new and must be added to `.PHONY` and to
CI. **`-race` is in the list** because C1, C2 and C4 are concurrency properties
and no existing target runs it — review found audit 3 reasoned the same way and
then omitted it.

### Coverage — a contract, not a promise

**[v1's version was dishonest in a new way]**: it said the section would be
"written last, against what actually landed", which is a promise to be honest
later placed inside the section it promises to replace.

**The rule instead: the merge commit must replace this list with the measured
one, and anything that ships unchecked moves to `deferred.md` in the same
commit.**

Provisionally mechanical: C1, C2, C3, C4a, C4b, C5, O1's product half, O2's
product half, R3, S5, B1.

**Not mechanically checkable, and not claimed as such:** every prose item in S,
R and O's doc half; and **B's reconciliation itself** — `ledger-check` verifies
predicates, not whether the reconciliation was complete. v1 listed B under
"mechanically checked"; it is not.

## Out of scope, recorded in `deferred.md`

- **C4c**, the success-path terminal test — needs Runtime-level fixtures.
- **Enforcing a distinct reviewer actor** — no actor identity exists to compare.
- **A redacting `slog` handler.** Recorded with review's caveat: the redaction gap
  is broader than logs, and `0ffb976` showed the audit had not mapped it.
- **Response-schema checking** — R3 fixes the instances; the axis stays unguarded.
- **`container`'s placeholder status**, **no SDK**, **`linux/arm64` CI**,
  **cutting `0.2.0`** — unchanged.
