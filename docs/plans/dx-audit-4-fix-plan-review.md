# Adversarial review — DX Audit 4 fix plan

Three independent reviews of v1, per CONTRIBUTING §14.

| Reviewer | Perspective | Verdict |
|---|---|---|
| R1 | Security and documentation truth | **reject** — 3 blocking |
| R2 | Go correctness (tracks C, O, R) | **reject** — 3 blocking |
| R3 | Coverage, guards and ledgers | **reject** — 3 blocking |

All three rejected v1. Two of its fixes would have shipped new P0s, one of its
two new guards could not reach its own finding, and one review found a **live P0
in the code** that neither the audit nor the plan had identified.

## The code defect the plan did not know about

**R2, blocking.** `ExecuteInitiative` claimed the initiative and never released
it — on success *or* ordinary failure. Six of the seven claiming entry points
release as a side effect of their engine call reaching a terminal status inside
`internal/workflow`; Execute's does not.

Verified against a live daemon before fixing: after a successful `idea execute`,
status stayed `running`, `idea next` answered "workflow running" indefinitely,
and every later command answered "already running".

**Every gate was green.** `scripts/smoke.sh` and the audit journey both run
`execute → review → apply → revert`, and `RevertChangeSet` sets the initiative
status as a side effect — so both harnesses reset the exact state the bug
corrupts, one step after corrupting it. Fixed in `39cb442`, with a test that
deliberately never reverts.

## Blocking findings against the plan

### 1. C1 would have 409'd every apply (R2)

v1 had apply and revert take the claim. Because of the Execute stranding, the
ambient status at apply time *was* `running` — and no claim's source-list can
include the state it claims into. Every apply in the tool would have started
returning "already running". C1 now lands after that fix, and adds an explicit
release on both of apply's exits, which v1 omitted entirely.

R2 also narrowed the hazard: apply, revert and execute already share the engine
mutex, and revise-plan and reopen-phase refuse once a change set exists. The one
real gap is `Revise` — a provenance race, not filesystem corruption.

### 2. C2 would have re-opened P0-1 (R2, R3 independently)

v1: *"make both conditional on the operation still being non-terminal."*
`CancelOperation` writes `cancelled` **before** the goroutine unwinds, so gating
`failOperation` would skip the `FailInitiative` call that `39ca31a` added — the
stranding, one round later. v1 cited `CancelOperationStatus` as precedent while
proposing to invert the decision that precedent encodes, and contradicted a
comment written last round for that exact reason.

R2 added the direction v1 also missed: gating only the operation write leaves
`FailInitiative` unconditional, so a panic *after* a successful terminal write
produces operation `succeeded` + initiative `failed`, sending a user to
`idea retry` on a run that worked.

The two functions now get opposite treatment.

### 3. `ledger-check` could not reach its own finding (R3)

Specified as "fails when a row marked `open` names a file:line that no longer
contains the claimed text". The defects file contains **zero** `file:line`
references — the check would have been green on both ledgers, certifying the
stale-open state it exists to catch. Audit 3's G1b, repeated.

Replaced with a predicate column and a symmetric check, and the 27-row format
migration is stated as real work.

### 4. The security-claim lint was self-certifying (R1, R3)

Written in the same track, same round, by the same author as the doc edits it
judges — the allowlist would be tuned until the new wording passed. R1 measured
the noise: `token` is majority-noise across two files with three unrelated
meanings. And **assert-plus-link passes**, so a file could restate a false
guarantee and go green.

Redesigned around anchors in the canonical file, which needs no natural-language
classification.

### 5. S2 fixed one sentence of six (R1)

The "independent reviewer" claim lives in `security.md`, `architecture.md`,
`quickstart.md` (×2) and `README.md` (×2). Fixing one and calling it done
reproduces the pattern the plan is named after. R1 also rejected v1's replacement
wording: "structural" separation implies something real, and nothing prevents
implementer and reviewer resolving to the same model.

### 6. The coverage section was dishonest in a new way (R3)

v1 said the section would be "written last, against what actually landed" — a
promise to be honest later, inside the section it promises to replace. It also
listed S4, B and O1's product half as mechanically checked; none is. Now a
contract on the merge commit.

## Accepted with modification

- **C4 split three ways.** R2 showed cancel/fail/panic are cheap to test but
  **success is not centralised**, so a table-driven test built the obvious way
  would have passed while the Execute stranding was live. C4a (AST enumeration of
  `launchOperation` sites) is what makes deferring the success path safe.
- **O1's product half** is feasible and v1 hand-waved it: the CLI reads its own
  environment and compares against the daemon-side `Ready` that `GET /providers`
  already returns.
- **O2's product half would have been inert.** `handleUpdateProviders` drops
  every non-error problem before returning, so a warning would never reach
  `idea model set`'s output — the exact case the finding is about.
- **R3's null slices**: three of four sources are in `internal/workflow` and
  `internal/projection`, not the handler, and R2 found a fifth v1 missed
  (`/guidance`'s `affected`).
- **R5 overstated the changelog gap** — most of audit 3's code round is recorded;
  three specific items are not. An overstated finding in a bookkeeping round is
  the same defect class the round is about.

## Corrections to the plan's own framing

- **The guard track record is 2-of-3, not 0-of-2.** R3 established that audit 3
  built `api-docs-check` and `conflictcheck`; only G1b was missed. The failure was
  the claim line, not build capacity — which means the remedy is honesty about
  scope rather than more ambition.
- **v1 was stale on its own branch**: it treated O2's doc half as pending after
  `ebc5245` had landed it.
- **The ownership table asserted a disjointness it did not have** — four items
  landed in files no track owned. The tracks are serial, so it is a forgetting
  risk; the table is now informational and says so.

## Found by review, fixed ahead of the plan

- **A credential-egress bug** (`0ffb976`). `StartInitiativeWithDocs` redacted the
  description for storage and passed the raw value to the model provider. A
  credential pasted into an idea was scrubbed locally and egressed anyway. It hid
  because `retry.go` passes the redacted value, so only first runs leaked. R1
  found it while reading the code to check a documentation claim — which is the
  argument for reviewing prose against code rather than against itself.
