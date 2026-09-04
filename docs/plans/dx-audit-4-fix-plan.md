# DX Audit 4 — Fix Plan

Branch: `worktree-dx-audit-4`, from `main` @ `39ca31a`.

Audit 4 verdict: **FAIL**, Overall DX **79/100** (from 74, from 70). Both hard
gates pass; six of nine areas now clear the world-class floor. Report:
`docs/plans/dx-audit-4-report.md`.

**P0 #1 is already fixed.** The initiative-stranding regression was closed during
the audit in `39ca31a` — cancellation releases the claim, `releaseOrphanedClaims`
is the backstop for a daemon killed mid-flight, and both halves are
mutation-checked. It is listed here for completeness, not as work.

Remaining: **2 P0, 8 P1, and a bookkeeping cluster.**

## The pattern, fourth time — and why this round's structural work is different

| Audit | Pattern |
|---|---|
| 2 | A fix made correctly in one place, not propagated to its siblings |
| 3 | The canonical source corrected, the derivative copies left stale |
| 4 | **The correction reaching two of three documents** |

`security.md` and `security-model.md` now describe redaction correctly;
`configuration.md` still promises it for logs and blobs. `ui.md` stopped claiming
an SSE feed; `api.md` and `cli.md` kept it. Each round the remediation gets
closer and stops one document short.

The guards now cover routes, request-body fields, 409 declarations, prose
commands, and `api.md` ↔ `openapi.yaml` row existence in both directions. **None
of them can see a claim that is false, or a state machine with a hole in it** —
which is exactly where all three of this round's P0s came from, including the one
the previous remediation introduced.

So the structural work is two checks aimed there:

1. **A security-claim lint** (G1). A small set of load-bearing security
   properties get one canonical statement each; any *other* document asserting
   the same property must link rather than restate. Mechanical, and it is the
   only thing that would have caught `configuration.md` — the third copy, in the
   one file the last two rounds did not open.
2. **A state-machine invariant test** (G2). Enumerate every terminal path out of
   every async entry point and assert the initiative claim is released on each.
   The claim was reviewed four times — three before implementation, one after —
   and two of those reviews specifically asked whether it was released on every
   exit. The answer was no, and nobody traced the cancellation path. Reading did
   not catch it; enumeration would have.

## Tracks

Ordered, not parallel. S defines the canonical statements that D links to, and C
changes behaviour that S and D describe.

| Order | Track | Scope | Files owned |
|---|---|---|---|
| 1 | **S** | Security docs + the claim lint | `docs/configuration.md`, `docs/security.md`, `docs/architecture.md`, `internal/docsgen/` |
| 2 | **O** | Onboarding (P0-3, model ids) | `README.md`, `docs/quickstart.md`, `docs/agents.md` |
| 3 | **C** | Code correctness | `internal/daemon/`, `internal/workflow/`, `internal/config/` |
| 4 | **R** | Reference docs | `docs/api.md`, `docs/cli.md`, `docs/openapi.yaml`, `CHANGELOG.md` |
| 5 | **B** | Bookkeeping | `docs/plans/` |

---

## Track S — the false redaction guarantee, and the guard for it

### S1 (P0). `configuration.md` promises redaction that does not happen

> *"Secret values are redacted before any persistence (`[REDACTED]`), including
> logs, events, blobs, and context bundles."*

Both halves are false, and both were the point of last round's rewrite:

- **Logs.** There is no redacting `slog` handler, and `GET /api/v1/logs` returns
  `daemon.log` verbatim.
- **Blobs.** The file-content store is constructed with a **nil redactor** —
  `blob.New(filepath.Join(projDir, "raw-blobs"), nil)`. `security-model.md`
  calls it "deliberately redaction-free".

**Fix:** state what is true — the store and event choke points redact; logs and
the raw blob store do not — and link to `security-model.md`, which is the
canonical statement, instead of restating the mechanism a fourth time.

### S2 (P1). "Review runs require a distinct actor" is enforced nowhere

`docs/security.md`'s threat row for agent self-approval asserts it. No code
compares two actors: `ReviewChangeSet` hardcodes the same actor constant for both
the review record and the transition, and nothing stops one model serving as
implementer and adversarial reviewer.

The row's *second* clause — `accepted_risk` requires a human actor — is real and
enforced in the store.

**Fix (documentation only this round).** Say what is enforced: a deterministic
selector→level mapping, a human actor required for `accepted_risk`, and a
*separate reviewer run* that is structural rather than actor-checked. Record the
actual enforcement as a deferred item rather than implying it exists. Making the
reviewer identity a real constraint is a design change, not a doc fix, and
inventing one under audit pressure is how the trust-level mapping got its current
shape.

### S3 (P1). `architecture.md` carries the inverted redaction claim a third time

`internal/blob   content-addressed BlobStore (**redaction before Put**)`, and a
data tree showing only `blobs/`. Two stores exist; `raw-blobs/` holds unredacted
file content and raw image bytes and appears in no published document.

Planned last round as part of S3 and never done — it is one of the three items
`deferred.md` omits.

### S4 (structural). A security-claim lint — `make security-claims-check`

A short table of load-bearing properties, each with one canonical location:

| Property | Canonical |
|---|---|
| What the executors isolate | `security-model.md` |
| Where redaction happens, and where it does not | `security-model.md` |
| Image handling | `security.md` |
| The token model | `security.md` |

The check scans every published `docs/*.md` plus `README.md` for sentences
asserting one of these properties, and fails when a file that is not the
canonical one asserts it without a link to the canonical one.

**Scope, stated honestly.** It cannot tell whether a claim is *true* — nothing
mechanical can. It enforces that each property is stated once, which is what
turns "three documents drifted apart" into "one document to check". That is the
whole of what the last three audits' documentation P0s had in common.

Keyword-driven and therefore approximate: it will want a small allowlist for
prose that mentions "redact" without asserting a guarantee. The allowlist is in
the check, not in the docs, so it cannot silently grow the way a `# docs:skip`
marker in a README would.

---

## Track O — the with-key path

### O1 (P0). The documented with-key path dead-ends

`idea init` calls `ensureDaemon()`, so the daemon starts with the environment as
it is *at that instant*. The quickstart and README then tell the user to export
their key. But keys are resolved inside the daemon, and `idea model set`'s reload
re-reads the daemon's environment — which still has none. The result is
`status 401`, which is not in the transient list, so the initiative fails.

`docs/agents.md` documents the correct sequence: export, then `idea daemon stop`
so the next command restarts it. The two entry-point documents omit it, and the
quickstart's warning box attributes the identical failure to a different cause.

**Fix, both halves:**

- **Documentation:** put the export before `idea init` in both files, and where
  the key arrives later, say `idea daemon stop` and why. Correct the warning box.
- **Product:** `idea model set` should notice this. When the selected provider's
  key env var is set in the *caller's* environment but absent in the daemon's,
  it can say so — "the daemon started before `ANTHROPIC_API_KEY` was set; run
  `idea daemon stop` and retry" — instead of reporting success and failing on the
  first call. That is a small, well-scoped check and it turns a documentation
  trap into an error message.

The product half is what stops this recurring, because the trap is a property of
the daemon's lifecycle rather than of the prose.

### O2 (P1). `anthropic/claude-3-7-sonnet` is not a valid model id

Eight occurrences across quickstart and agents.md; the catalog spells it
`claude-3-7-sonnet-latest`. Nothing validates the model half of a spec —
`modelSpecProblems` checks only the provider — so `idea model set` prints success
and the first call returns a non-retryable 404. Two occurrences are
`executors setup --model` lines, so the bad id is forwarded into the container.

**Fix, both halves again:** correct the eight ids, and have `modelSpecProblems`
warn when the model half matches no catalog entry for a provider whose catalog is
known. A warning rather than an error, because a valid model can postdate the
bundled catalog — but silence is what let eight copies of a dead id ship.

---

## Track C — code

### C1 (P1). Apply and revert bypass the claim

Every other async entry point claims the initiative before mutating task or graph
state. `ApplyChangeSetInitiative` and `RevertChangeSetInitiative` call
`CreateOperation` directly with no status check and no claim — verified, zero
`ClaimInitiativeRunning` calls in either.

So an apply can run concurrently with a revise that is regenerating the task
graph: the corruption the claim exists to prevent, reached through a different
door.

**Fix.** Both claim the initiative, from the states an apply or revert is
legitimate in. This needs care: apply and revert are reachable from more states
than the plan-mutating operations, and `complete` is one of them. Get the source
list wrong and a legitimate apply starts returning 409 — which is why the
acceptance test drives a real apply from each reachable state rather than
asserting the list by inspection.

### C2 (P1). `failOperation` and `failOperationAfterPanic` write unconditionally

Neither is guarded by the row's current status, unlike `CancelOperationStatus`
and `ClaimInitiativeRunning` in the same round. A panic *after* the goroutine's
own true terminal write — in a trailing log line, say — is still caught by the
recover and rewrites a completed, successful run as failed, with the initiative
failed alongside it.

**Fix.** Make both conditional on the operation still being non-terminal, using
the CAS that `CancelOperationStatus` already established. A panic after success
then leaves the success standing, which is the honest record.

### C3 (P1). The provider config endpoint masks "written but not applied"

`writeReloadErr` exists precisely so the caller learns the file *was* written.
Only the executors endpoint calls it; the providers endpoint sends the same
failure to `writeErr`, whose storage branch masks it to a generic 500. And
`api.md` goes on to claim a handler cannot get this wrong.

**Fix.** Route the providers endpoint through `writeReloadErr` too, and add the
assertion to the existing leak test so both endpoints are covered.

### C4 (structural). The state-machine invariant test

Enumerate every async entry point and every terminal path — success, error,
cancellation, panic, and a create that fails after the claim — and assert the
initiative is not left `running`.

Table-driven from a list of entry points, so a new one joins the test by existing
rather than by being remembered. This is the check that would have caught the
regression the previous round shipped, and it is the reason this plan does not
add a fifth prose guard.

---

## Track R — reference docs

| # | Finding | Fix |
|---|---|---|
| R1 | `api.md` and `cli.md` still assert the SSE feed `ui.md` stopped claiming — `idea watch` polls at 1 Hz, there is no SSE client in `web/src`, and `GET /events` has zero in-tree consumers | correct both; keep the endpoint documented as existing and unused |
| R2 | `openapi.yaml` says operations are "newest last"; the store orders `DESC` and `api.md` says "newest first" | correct the spec |
| R3 | A class of `required` arrays the handler emits as `null` — `/why`, `/impact`, `/trace`, understanding's four arrays, `/models` | initialise the slices at the handler so the response matches the contract, which is the half that is actually wrong |
| R4 | `api.md` claims a handler "cannot accidentally report one as a 500" | true only after C3; sequence it after |
| R5 | `CHANGELOG` `[Unreleased]` records none of audit 3's code round, including two user-visible changes: `idea verify` now exits non-zero, and finding disposition now answers 404 where it answered 200 | add, with the second under **BREAKING** |
| R6 | `openapi.yaml`'s `Coverage` omits `status`, the field carrying the waived/failed distinction | add |

R3 is a code fix in a documentation track deliberately: the spec is right and the
handler is wrong, so correcting the spec would enshrine a `null` the client has
to special-case.

---

## Track B — bookkeeping

Both ledgers introduced last round are inaccurate, in opposite directions.

- `dx-audit-3-doc-defects.md` reads `open` on every row while at least 17 are
  fixed. A file built to make coverage falsifiable, with a uniformly stale
  column, no longer separates the six open rows from the seventeen closed ones.
- `deferred.md` omits three planned-and-undone items: `architecture.md`'s half of
  the redaction fix (S3 here), the trust-level code change, and the stale
  `routes.go` comment.
- The plan claims *"Mechanically checked: C1–C7"* while `deferred.md` records C5,
  C6 and part of C4 as not attempted. **The two files contradict each other.**

**Fix.** Reconcile all three against the tree, and add a `make ledger-check` that
fails when a row marked `open` names a file:line that no longer contains the
claimed text. A ledger nobody can trust is worse than no ledger, because it is
cited as evidence.

Also open and recorded rather than fixed: the capability broker has zero non-test
callers while two documents list it as shipped, and `container` is a second
placeholder executor listed as a real kind in four documents.

---

## Verification

```
make test && make vet && make docs-check && make api-check \
  && make api-docs-check && make docs-lint && make security-claims-check \
  && make ledger-check && make check-embed \
  && bash scripts/smoke.sh \
  && (cd web && pnpm build && pnpm test)
```

Then the audit journey, and direct re-probing of every acceptance test against a
live daemon.

**Coverage, stated honestly.** Mechanically checked: C1–C4, O1's product half,
O2's product half, R3, S4, B. **Not mechanically checkable:** S1, S2, S3 and R1,
R2, R5, R6 — prose, reviewed against the code it describes. S4 narrows that set
by making each property single-sourced, but it cannot judge truth.

This section is written last, against what actually landed, because last round's
version claimed two guards that did not exist.

## Out of scope, recorded in `deferred.md`

- **Enforcing a distinct reviewer actor** (S2's code half) — a design change.
- **A redacting `slog` handler** — documented instead, again.
- **Response-schema checking** — R3 fixes the instances; the axis stays unguarded.
- **The capability broker's missing callers**, and `container`'s placeholder
  status.
- **No SDK**, **`linux/arm64` CI**, **cutting `0.2.0`** — unchanged.
