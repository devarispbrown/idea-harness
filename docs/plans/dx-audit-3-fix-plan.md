# DX Audit 3 — Fix Plan (v2)

Branch: `worktree-dx-audit-3`, from `main` @ `8fa037d`.

**v2 supersedes v1 after three adversarial reviews** (security, Go correctness,
coverage/guards). They found **eleven blocking defects in the plan**, including
both guards not doing what the plan claimed, a track schedule that was not
disjoint, and a code-fix list that was wrong in both directions. Corrections are
folded in below and marked **[v1 was wrong]**. Review record:
`dx-audit-3-fix-plan-review.md`.

**Two exploitable security defects that neither audit found** were surfaced by
the security review and are already fixed on this branch (`e7ea561`, `f841023`):
Anthropic and modern OpenAI API keys were never redacted, and the SSRF guard
validated one address then dialled a hostname that resolves again.

Audit 3 verdict: **FAIL**, Overall DX 74/100 (up from 70). Both hard gates pass.
Report: `docs/plans/dx-audit-3-report.md`. Findings: **6 P0, 7 P1, ~10 P2**, plus
~30 documentation defects below the cut — now transcribed into
`dx-audit-3-doc-defects.md` rather than named and dropped, which is what happened
to audit 2's equivalent list. Deferrals go in `deferred.md`.

## The shape of the problem, third time

Audit 2's pattern was *a fix made correctly in one place and not propagated to its
siblings*. Audit 3 found the same failure one level out: **the remediation
corrected each canonical source and left the derivative copies stale.**

- `security.md` describes image handling correctly; `ui.md`, `api.md` and README
  still carry the pre-fix sentence.
- `openapi.yaml` describes `promote`, `/raw` and `artifacts/diff` correctly;
  `api.md` does not.
- `doctor` got a no-project guard; its sibling `config validate` did not.
- The CHANGELOG entry for `doctor` outlived the code it described by three commits.

And the guards built to stop exactly this are blind to it. They compare route
sets, request-body field names and 409 declarations — **never prose, never
response shapes, never whether a documented command actually runs.** Every P0 in
the audit passes all four checks.

So this round's structural work is not another route-level check. It is three
cheap checks aimed where the failures actually are — see Track G, which review
rewrote wholesale:

1. **A static doc-command lint** (G1a): every `idea …` command in the docs names
   a real verb and a resolvable reference. `scripts/smoke.sh` already said
   `TASK-1` while README said `T-3`; nothing compared them.
2. **One assertion in the existing smoke script** (G1b): the documented sequence
   must reach a terminal state that is not `failed`.
3. **`api.md` ↔ `openapi.yaml`** (G2): every R-track finding is prose drifting
   from a spec that is already correct, so that is the pair to compare.

Neither guard can check whether a *security claim* is true. That is a human
judgement, so S-track findings get a different remedy: the claims move to sit
immediately beside the code that implements them, with the scoping that makes
them true, and `docs/security-model.md` gains an explicit "what this does **not**
protect against" section — because the failure mode there was an unscoped
generalisation, not a typo.

## Priority

The audit's recommendation stands: **the three false security guarantees go
first**, ahead of everything else. Wrong docs are a nuisance; wrong security docs
are how somebody points this at a repository they should not.

## Tracks

**[v1 was wrong.]** v1 claimed S, O and R touch disjoint files and can run in
parallel. Its own text then assigned S2 to `ui.md` and README (Track O's), S3 to
`api.md` (Track R's), R15 to `routes.go` (Track C's) and R10/R13 to Track O's
files — and S5 to `internal/security/`, which **no track owned**, in a track
declared documentation-only.

The real constraint is that S defines the canonical security statements that O
and R are told to link to, so S cannot run beside them.

| Order | Track | Scope | Files owned |
|---|---|---|---|
| 1 | **S** | Security documentation (P0) | `docs/security.md`, `docs/security-model.md`, `docs/architecture.md` |
| 2 | **O** | Onboarding (P0) | `README.md`, `docs/quickstart.md`, `docs/ui.md` |
| 2 | **R** | Reference (P1/P2) | `docs/api.md`, `docs/openapi.yaml`, `docs/configuration.md`, `docs/agents.md`, `CHANGELOG.md` |
| 3 | **C** | Code correctness (P0/P1) | `internal/daemon/`, `internal/workflow/`, `internal/store/`, `internal/security/`, `internal/context/` |
| 3 | **D** | CLI defects (P1/P2) | `internal/cli/` |
| 4 | **G** | Guards | `internal/docsgen/`, `scripts/`, `Makefile`, `.github/workflows/` |

S runs first and alone. O and R then run in parallel with each other. C and D are
sequential (both touch `internal/`). G is last, because G1a lints the docs the
earlier tracks rewrite — v1 never stated G's position at all.

Reassignments so every item has exactly one owner: **S2's `ui.md`/README edits →
O**; **S3's `api.md`/`openapi.yaml` edits → R**; **R15's `routes.go` comment →
C**; **S5's code change → C** (see below).

Track C and D must both end with `bash scripts/smoke.sh` passing: it is CI's only
end-to-end job, and C3 and D3 change commands it runs unguarded.

---

## Track S — Security documentation (P0)

### S1. The execution gateway describes isolation the coding agent does not get

`docs/security-model.md`'s "Execution gateway" claims the executor sandbox gives
*"no network, read-only root, `--cap-drop ALL`, non-root `--user`, bounded
memory/pids, writable `/tmp` caches, and a read-only `/repo` mount"*, and that
*"a sandboxed session cannot widen its own authority"*.

Verified: `ContainerSandbox` has exactly **one** non-test caller —
`internal/workflow/execute.go:821`, the deterministic build/test check. The
executors that run model-generated code (`omp`, `hermes`) use
`SandboxedAgentExecutor`, whose own doc comment states the opposite:

```
// NOT network isolation: the agent needs egress to reach its model API, so the
// provider keys are forwarded in and `--network none` is deliberately omitted.
//                                            internal/executor/agent.go:16-20
```

**Fix.** Split the section in two, because there are two boundaries with
different guarantees:

- **Deterministic checks** (`ContainerSandbox`) — keep the current list, scoped
  explicitly to `run_command` and the build/test check, naming its one caller.
- **Coding agents** (`SandboxedAgentExecutor`) — filesystem and process isolation
  only: read-only root, writable `/work` and `/tmp`, `--cap-drop ALL`, non-root.
  **Network egress is permitted and the provider API key is forwarded into the
  container**, because the agent must reach its model. State the consequence
  plainly: a compromised or prompt-injected agent can exfiltrate anything in
  `/work` and can use the forwarded key.

Add a **"What this does not protect against"** section covering that, the
redaction-free raw store (S3), and image egress (S2). The failure here was an
unscoped generalisation; a section that exists to hold the negative space is what
stops the next one.

*Acceptance:* every isolation claim names the type that implements it and the
caller that uses it; `grep -c "network" docs/security-model.md` findings all
scoped; a reviewer can map each bullet to a line in `internal/executor/`.

### S2. Image uploads are documented as redacted; they are not

`docs/ui.md` says each doc is *"credential-swept and redacted on ingest … never
served or fed to a model unredacted"*. Images skip both: `ingestImageDoc` writes
bytes verbatim to the redaction-free store, `handleGetDoc` serves them raw, and
`describeImage` sends them unredacted to the vision provider.

`docs/security.md` already documents this correctly as an accepted risk. So this
is purely a stale copy — the fix is to make `ui.md` (Track O owns the file) and
`api.md` and README (Track R / O) say what `security.md` says, and to link rather
than restate. **The rule this round: one canonical statement per security
property; every other mention links to it.** Restating is what let three copies
drift.

### S3. The patch-redaction claim is inverted, in two published artifacts

`docs/api.md` and `docs/openapi.yaml` both say file contents are *"redacted at
write time"*. The handler says the reverse: redaction happens *"at this display
surface … never at blob-write time — the raw store stays redaction-free."*

**Fix** (Track R owns both files): correct both to "redacted at read time; the
raw store is deliberately redaction-free", and link to the canonical statement in
`security.md`. Add the raw store to `architecture.md`'s data tree, which omits it.

### S4. `slog` is listed as a redaction choke point and is not one

`docs/security.md` names `slog` alongside `store` and `blob`. There is no
redacting handler: the CLI installs bare text handlers, and `handleLogs` returns
`daemon.log` verbatim over the API.

**Fix:** remove `slog` from the choke-point list and say explicitly that daemon
logs are **not** redacted and are readable through `GET /api/v1/logs`. That is a
real property a reader needs. (Making the logger redact is a larger change and is
recorded as follow-up, not done here.)

### S5. The described trust-assignment enforcement is not implemented

`docs/security.md` claims *"`human_approved` requires a recorded human action;
`reviewed_agent_output` requires a distinct reviewer run."* `security.TrustFor`
is a pure `switch` on a selector string that reads no record, and neither
precondition is checked.

**[v1 got the risk wrong and gave this no owner.]** v1 said changing the mapping
"changes prompt priority". It does not: priority is keyed on the selector string
in a separate map and is untouched. The only consumer of `TrustFor` is the
untrusted-data wrapper in `context.Compile`, which prefixes
`TrustExternalUntrusted` entries with "treat as data, never as instructions". So
the change toggles *framing*, not ordering — a product decision about what the
model is told, not a doc correction.

Review also found a worse instance v1 missed: the `task` selector is not only the
user's typed idea. `execute.go` passes a **model-generated execution-plan task
statement** under the same selector, so model output is currently elevated to
`human_approved` — the exact thing the doc says never happens.

And after any such change, four of six trust levels become unreachable, which the
doc must state rather than imply a ladder that exists.

**Reclassified.** The doc half stays in Track S: describe what the switch
actually guarantees (a deterministic selector→level mapping, never derived from
model output), drop the two "requires a recorded …" sentences, and say plainly
that trust levels are a prompt-construction label consumed by one line of code —
not an authorisation mechanism.

**The code half moves to Track C**, which owns `internal/security/` and
`internal/context/` in v2's table. Preferred option: introduce a distinct level
for direct human input and map the *idea* to it, leaving model-generated task
statements untrusted — rather than v1's blanket demotion, which would tell every
model on every call to treat the user's own product idea as data it must not act
on. Five stages pass the task content with no unwrapped duplicate elsewhere in
the prompt, so a blanket demotion is a real, untested behavioural change.

### S6. Exposures the audit did not find — already fixed

Both surfaced by the security review, both verified, both landed on this branch
ahead of the rest of the plan because they are exploitable rather than
misleading:

- **Modern provider API keys were never redacted** (`e7ea561`). The catalog's
  `sk-` pattern required 20+ contiguous alphanumerics, so `sk-ant-api03-…`,
  `sk-proj-…` and `sk-svcacct-…` all passed through in the clear. Measured: three
  of four current formats unredacted, including Anthropic's — the credential
  forwarded into the agent container that has unrestricted egress.
- **DNS rebinding in the SSRF guard** (`f841023`). It validated resolved
  addresses then dialled the hostname, which Go resolves again; a hostile
  resolver answers public first and `169.254.169.254` second. It now dials the
  address it checked.

Still open from that review, recorded rather than fixed here: the `--user` flag
passes the daemon's own UID, so a daemon running as root gives a root container;
the Hermes image is pinned by tag rather than digest, with no signature check;
and the control-plane deny covers `.idea-harness/` and `.git/` but not
auto-executing files like `.github/workflows/` — all three belong in S1's "what
this does not protect against" section.

---

## Track O — Onboarding documentation (P0)

### O1. The quickstart's no-key path is broken by the step above it

Line 20 instructs `idea model set openai/gpt-4o-mini`; lines 27-30 and 183-186
then promise *"No key yet? Then everything below is synthetic"* and *"a missing
key does not fail the run."* Once `default_model` is set, resolution returns it
before reaching the demo fallback, and the run fails with a non-retryable `401`.

**Fix:** restructure the opening so the two paths are explicit and mutually
exclusive — *"With a key: set a model. Without one: skip this step; everything
below produces synthetic output."* Verify by running both paths end to end (G1
makes this permanent).

### O2. README's front page shows task references that cannot resolve

`idea trace task T-3`, `idea why task T-1`. Codes are `TASK-<n>`. The
`REQ-`/`DEC-`/`CHG-` examples beside them are right.

**Fix:** correct to `TASK-1`. G1 then keeps every documented command honest.

### O3. `docs/ui.md` documents a UI that does not exist

An "Eleven tabs" section naming eleven views; the app has six phase pages and a
seven-value route union. There is no activity view and no logs view. A dozen more
claims fail the same way: stepper states, badges, counts, an SSE feed the UI does
not use (it polls on timers).

**Fix:** rewrite `ui.md` against `web/src/`, from the router and `phases.ts`
outward. Any claim that cannot be traced to a component is cut rather than
guessed. This is the largest single doc job in the round.

### O4. README's Install block has no way to obtain the source

Carried from audit 2, unfixed. The block opens at `cd idea-harness`; the only
`git clone` mention sits far below in Versioning.

**Fix:** one line. `git clone git@github.com:devarispbrown/idea-harness.git`
above the `cd`.

---

## Track R — Reference documentation (P1/P2)

Each row here is "the prose is present but wrong", which the existing guards
cannot see and G2 only partly covers.

| # | Finding | Fix |
|---|---|---|
| R1 | `api.md` impact-counts row omits the required `nodes` param — the documented call returns 400 | add the param |
| R2 | `api.md` says retry's `model` is optional; it is required | correct |
| R3 | `api.md` says promote yields "a requirement, decision or question"; it yields a Question. `openapi.yaml` is right | correct api.md |
| R4 | `api.md` says `/raw` is "the same body" as `/body`; `/body` is sanitized and `/raw` is not | correct |
| R5 | `api.md` cites `projection.BuildNextMove`, renamed to `BuildGuidance` | correct |
| R6 | `api.md` "Structured diff" describes a unified line diff | correct |
| R7 | `openapi.yaml` + `api.md` diff response schema omits the `files` key | add |
| R8 | `api.md` "Three cases" for `writeErr`; there are four | correct |
| R9 | `CHANGELOG.md` claims doctor exits non-zero outside a project; it exits 0 as of `8fa037d` | correct the bullet |
| R10 | `configuration.md` and `quickstart.md` describe review-budget semantics differently and both wrong | one correct statement, cross-linked |
| R11 | `configuration.md` `max_cycles` default condition wrong | correct |
| R12 | `agents.md` says the agent key is read from the project `.env`; it is not | correct |
| R13 | README lists five providers; DeepSeek is a sixth preset | correct |
| R14 | `api.md` doc-upload row omits `.gif`/`.webp` | add |
| R15 | `routes.go` comment says the flat routes are still live, contradicting `api.md` | correct the comment |

---

## Track C — Code correctness (P0/P1)

### C1. Cancelling an apply reports success while the goroutine keeps writing (P0)

**[v1 named the wrong mechanism.]** v1 proposed three things: a 409 from
`CancelOperation` once the change set leaves `approved`, terminal writes with
`context.Background()`, and logging the discarded errors. Review established:

- `ApplyChangeSet` **already** writes its terminal mutation with
  `context.Background()`. That part of v1 described a fix that exists.
- The real mechanism is that **`SetOperationStatus` is not a compare-and-swap.**
  Verified against the store: it is an unconditional
  `UPDATE operations SET status=?,… WHERE id=?` with no `RowsAffected` check,
  and so is `SetInitiativeStatus`. `CancelOperation` unconditionally writes
  `cancelled`; the goroutine unconditionally writes its own terminal status.
  Whichever lands last in SQLite wins. That is a **stomped** write, and v1's
  fixes address a *lost* write — a real but different bug.
- The 409 gate is racy and mis-scoped. `CancelOperation` takes only an `opID`, so
  it must resolve the change set and read its status with no lock held against
  the `e.mu` that `ApplyChangeSet` holds for its whole duration. And the
  "change set left approved" boundary exists for 1 of 11 operation kinds.

**Fix, corrected:** give `SetOperationStatus` a real CAS —
`UPDATE operations SET … WHERE id=? AND status='running'` with a `RowsAffected`
check — mirroring `TransitionChangeSet`, which is already exactly this shape and
is the in-repo precedent. A cancel that arrives after the operation has gone
terminal then becomes a no-op, and the goroutine's own result always wins over a
stale cancel. Drop the 409 gate entirely.

Keep the two genuinely separate improvements: terminal writes on
`context.Background()` where they are not already, and stop discarding those
errors at the eleven `launchOperation` closures.

*Acceptance:* a test drives a terminal write and a cancel against the same
operation and asserts the terminal status survives, in both orders.

### C2. `idea watch` still exits 0 — audit 2's fix does not work (P1)

`Update` has a **value receiver**, so Bubble Tea copies the model on first update
and thereafter holds the returned value; the outer `m` that `watchInitiative`
returns from is never written, so `m.fatal` is always nil. The program quits
correctly and still exits 0.

**And the test written for it was vacuous** — it called `poll()` on a pointer,
which is not the shape the program uses, so it passed either way.

**Fix:** take the final model from `p.Run()` and read `fatal` off that (or make
the receivers pointer receivers — the returned-model form is less subtle). The
test must drive `watchInitiative`, not `poll`. Because Bubble Tea cannot open a
TTY under `go test`, the test needs `tea.WithInput(nil)` + `tea.WithOutput` on a
pipe, or the program construction must be injectable. **If neither works, the
honest outcome is a test that is documented as not covering the exit path, not a
test that pretends to.**

### C3. `idea verify` cannot fail (P1)

Prints `✓ verified` and exits 0 for every gate state, including a recorded
failure: the status shown is the *initiative's*, not the gate's. Quickstart tells
readers to run this after apply, so `idea verify || exit 1` in CI can never fail.

**Fix:** return a non-nil error unless every coverage row is pass or waived, and
word the output from the actual gate result. Audit 2 fixed this command's
*recording* half; this is the *reporting* half.

### C4. Async operations check a running flag they never set (P1)

**[v1's list was wrong in both directions.]** Re-derived from the code by
counting, per `Runtime.*Initiative` method, whether it reads `InitiativeRunning`
and whether it ever sets it:

| Function | Reads | Sets | Shape |
|---|---|---|---|
| `ExecuteInitiative` | yes | **no** | the bug — **v1 omitted it** |
| `ReviseInitiative` | yes | no | the bug |
| `RevisePlanInitiative` | yes | no | the bug |
| `ApprovePlanInitiative` | yes | no | the bug |
| `AdvanceInitiative` | yes | yes | correct |
| `RetryInitiative` | yes | yes | correct |
| `ReopenPhaseInitiative` | yes | yes | correct |
| `SuggestAnswersInitiative` | **no check at all** | no | a different bug |

So v1 named `SuggestAnswers` as check-then-never-set when it has no check and no
existence check either, and omitted `Execute`, which is exactly that shape.

**Fix:** a new store method with genuinely conditional SQL —
`UPDATE initiatives SET status='running' WHERE id=? AND status IN (…)`,
`RowsAffected != 1` fails the transaction — mirroring `TransitionChangeSet`.
**This cannot be built from `SetInitiativeStatus`**, which is unconditional; a
Go-level `GetInitiative`-then-set is not atomic with the write and is the same
check-then-act the finding is about.

Applied to all seven guarded entry points, not the four that are broken, so the
eighth cannot get it wrong. `SuggestAnswersInitiative` gets an existence check
and its first guard, as separate work rather than a conversion.

The claim must be released on every exit path including panic — which is why
Track A's `recover()` in `launchOperation` lands with this, not after it.

### C5. Two reload-safety comments are false (P1)

`engine.go` says the swap is "guarded by e.mu, which ALL entry points now hold";
eleven entry points take no lock. `runtime.go` says `Reload` "builds a NEW
`*projectRuntime` and swaps the pointer"; it mutates in place, and a test pins
that shape.

**Fix:** make the comments true rather than correcting them downward.

**[v1 cited the wrong mechanism.]** `describeImage` does not use an
`atomic.Pointer` — it takes `e.mu`, snapshots the fields it needs, and unlocks.
The plan must say which mechanism it is adopting, because they differ in what
`e.mu` means afterwards. **Adopt the mutex-snapshot pattern**, matching the code
that already does it, at every entry point that currently reads `e.registry`,
`e.cfg`, `e.blobs` or `e.redactor` without the lock.

At the `runtime.go` layer, swap a fresh `*projectRuntime` pointer under `r.mu`.
This is sufficient because every handler re-fetches the pointer per request via
`EnsureProject` rather than holding one. **The fresh struct must keep the same
`*store.DB` and the same `*workflow.Engine`** — only `cfg`, `registry`,
`redactor` and `blobs` are reassigned — or the deliberate "no second DB handle"
invariant breaks and engine-mutex serialisation stops surviving a reload.

The test that pins the in-place mutation is rewritten by the same change, so it
cannot detect a wrong fix. Add an independent `-race` test that reloads while a
handler reads.

### C6. The engine mutex is held across model calls, on a server with no timeouts (P1)

`e.mu` is held through three 5-minute model calls and a 15-minute container wait,
from synchronous handlers, on an `http.Serve` with a zero-value `http.Server` —
no `ReadHeaderTimeout`, `ReadTimeout` or `WriteTimeout` — and `MaxBytesReader` on
2 of ~22 body-reading handlers.

**Fix:** three independent, each shippable alone:
- Shrink the critical section: claim the task under the lock, run the model
  unlocked, re-acquire to persist.
- Give `http.Server` explicit timeouts.
- A `MaxBytesReader` middleware over `/api/`.

The mutex change is the risky one and lands last, with its own review.

**It depends on C5 explicitly.** `Execute`'s unlocked middle section reads
`e.cfg.Check.Image` and the build/test commands; if C5 lands as mutex-snapshot
and C6 unlocks without snapshotting those first, C6 silently reintroduces C5's
race in the one place most exposed to it. The snapshot step is part of C6, not an
implication of it.

### C7. `POST /findings/{id}/disposition` answers 200 for a finding that does not exist (P1)

Bare `UPDATE … WHERE id = ?` with no `RowsAffected` check, while `db.Apply`
commits the `finding.dispositioned` event in the same transaction. So a stale ref
returns 200, nothing changes, and **the event log — the thing this product sells
as provable provenance — records that a human accepted the risk.**

**Fix:** check `RowsAffected`, return `*store.NotFoundError` (writeErr then gives
404), scope the UPDATE by initiative. Same for `/note`.

Then audit every bare `UPDATE` across **`internal/store/*.go`**, not just
`repo.go` as v1 said — the same unconditional shape is in `runrepo.go`'s
`SetRunStatus` and `SetOperationStatus`, and the latter is the single most
consequential instance in the tree, since it is the mechanism behind C1.

---

## Track D — CLI defects (P1/P2)

| # | Finding | Fix |
|---|---|---|
| D1 | `idea config validate` is the false-green `doctor` was fixed for twice — prints "config: valid" anywhere | stat `config.Dir` first; error with "no idea-harness project in \<dir\>" |
| D2 | `--` walks past `subsAreRequired`: `idea config -- validate` prints the model list, `idea daemon -- stop` starts a daemon | apply the subcommand check to parsed positionals, not `rest[0]` |
| D3 | Positional arity unenforced: `idea apply CHG-1 CHG-2` applies one and reports success | `maxPositional` on the command row, enforced in `parse`, rendered into the generated table |
| D4 | `idea dev` while a daemon is up overwrites then deletes the live pidfile, breaking `idea daemon stop` | probe `healthy()` first and exit with a message; write the pidfile only after a successful `Listen`; SIGTERM handler removes it; verify the pid is the daemon before killing |
| D5 | 413 on doc upload writes two JSON bodies | return a real error from `readMultipartFile`; caller trusts only the error |
| D6 | `changed` is project-wide not initiative-scoped; CLI picks the *oldest* initiative where everything else picks the newest; prints prose before the `--json` check | scope the projection; route through `resolveInitiative`; check `--json` before every early return |
| D7 | `idea init /elsewhere` prints a next step that fails from the current directory | tell the user to `cd` |
| D8 | `idea why requirment REQ-1` — invalid type prints "No provenance edges found", exit 0 | validate the type on both sides |

---

## Track A — API defects (P2), folded into Track C's commits

- **`GET /operations/{id}` — P1, not P2 [v1 under-ranked it].** It opens every
  registered project and runs first-touch reconciliation, which fails unfinished
  operations and can restore a mid-apply workspace. So one status poll for an
  operation in project A can reconcile away live operations in projects B and C
  that the request never named — and the CLI polls this endpoint in a loop.
  **Fix: record the owning project id in the global registry** so the lookup
  touches one project. v1 listed "scope the route under `{projectId}`" first;
  that is a breaking API change crossing four tracks plus `web/`, and it would
  flip the currently-passing `UNDOCUMENTED_BREAKING_API` gate unless shipped with
  a migration entry. Take the non-breaking option.
- `launchOperation` has no `recover()`; a panic in a detached goroutine kills the
  daemon, including one sitting between `fsdiff.Commit` and `MarkChangeSetApplied`.
- A cancelled execute strands the change set in `checks_running`; the only repair
  runs once per daemon lifetime, and the initiative refuses further revises until
  restart. **Defer a transition out on any error, and run reconciliation on a
  ticker.**
- `PATCH /tasks/{taskId}` erases `estimate` on any partial patch.
- `SuggestAnswersInitiative` is the only `*Initiative` method missing the
  existence check.
- Decision/question actions map a missing row to 400 "…is not open";
  `handleDecisionDiscussion` returns `200 []` for a decision that does not exist.
- `sse.go` writes into a stalled client with no deadline, pinning a goroutine
  against a `SetMaxOpenConns(1)` handle.

---

## Track G — Guards

**[v1 was wrong about both.]** Reviews established that neither guard did what
the plan claimed, so both are replaced by cheaper checks aimed where the failures
actually are.

### G1. A static doc-command lint, not a fenced-block executor

v1 proposed running every fenced `idea …` command against a live daemon, and
justified it as *"the only thing that would have caught"* the quickstart's broken
no-key path (O1). **It would not have.** `idea new` is fire-and-forget: it POSTs,
prints `✓ initiative started` and returns nil, while the 401 happens later in the
daemon's async workflow. A harness checking exit codes sees 0 and passes. v1 then
contradicted itself two paragraphs later, admitting it "proves the commands
execute, not that their output means what the prose says".

Review also enumerated the real cost: **48 `idea`-prefixed lines across 27 fenced
blocks**, of which **three** run unattended with no prior state and **15 need
hard skips** (TUI, Docker pulls, provider keys, fixture files, state mutation).
Three README bash blocks contain no `idea` line at all and would be invisible;
one quickstart block is inside a blockquote and would be missed by any extractor
keyed on `^```bash`. And the `# docs:skip <reason>` markers v1 proposed would put
CI machinery into the README's front page — a DX regression shipped by a DX
guard, in the round whose worst score is Documentation.

**Most importantly, the repository already knew the answer.** `scripts/smoke.sh`
has said `idea why task TASK-1` all along, while README said `T-3`. Nothing
compared the two.

**Replaced by two things:**

- **G1a — a static doc-command lint in `internal/docsgen`.** Extract every fenced
  `idea …` invocation from README and the quickstart, and assert (a) the verb
  exists in the CLI command table — already reachable, it is how `docs/cli.md` is
  generated — and (b) every `TYPE-n` reference matches a code format the domain
  actually emits. No daemon, no skips, cannot flake, runs beside `make docs-check`
  in the existing `compile-no-node` job. This catches `T-3`, `TASK-3` and `DEC-1`.
- **G1b — one assertion added to `scripts/smoke.sh`**, which is already the
  harness v1 proposed building: workspace-local GOPATH, unique port, daemon
  lifecycle with a trap, a `wait_for` helper, and a full drive of the documented
  loop, wired into two CI jobs. After the documented sequence it must poll the
  initiative to a terminal state and assert it is **not** `failed`. That is the
  ~10 lines that catch O1, and the difference between the guard mattering and
  not.

*Already applied by hand this round:* the README provenance block was fixed by
extracting its commands and running them, which is G1a done manually. All four
now exit 0.

### G2. `api.md` ↔ `openapi.yaml`, not code ↔ response

v1 proposed comparing handler-written JSON keys to spec properties, as the mirror
of the request-body check. Two independent reviews killed it:

- **It catches one of the three things v1 claimed.** `promote` (R3) and `/raw`
  (R4) are cases where `openapi.yaml` is already *correct* and `api.md` is wrong —
  a code↔spec check is green while the defect stands. `/raw` is not even JSON.
- **"Fail closed" would red most of the API on day one.** The request check is
  cheap because all twenty decoding handlers use one idiom; there is no such
  uniformity on the response side. Measured: of ~80 `writeJSON` calls, roughly
  half write a literal map and half write named domain types whose tags live in
  another package. v1's stated ordering — literal maps first, fail closed on the
  rest — is backwards, because the literal maps are the smaller half.

**Replaced by the axis where the R findings actually live:** a check that every
endpoint row in `docs/api.md` agrees with the same operation in
`docs/openapi.yaml` on method, path and declared status codes. `api-docs-check`
already parses both files, so this is an extension rather than a new analysis.

A live-response check — hitting each documented route on the smoke daemon and
diffing real JSON keys against the spec — is the right long-term answer and is
recorded as follow-up, not attempted here.

### G3. A deferral ledger

Audit 2 recorded a below-the-cut list in its plan and lost it; one item (README's
provider list omitting DeepSeek) is still unfixed and reappears in this plan as
R13, as new work. "Recorded" currently means "written in a plan file that the
next round supersedes".

`docs/plans/deferred.md`, appended to per round, one line per deferred item with
its origin. Cheap, and it closes the loop the audits keep re-finding.

## Verification

```
make test && make vet && make docs-check && make api-check \
  && make api-docs-check && make docs-lint && make check-embed \
  && bash scripts/smoke.sh \
  && (cd web && pnpm build && pnpm test)
```

`docs-lint` is **new** (G1a) and must be added to `.PHONY`. `scripts/smoke.sh` is
existing and is in the list because C3 and D3 change commands it runs unguarded —
v1 omitted both it and the web build's role here.

Then a full re-run of the audit journey, and direct re-probing of every
acceptance test against a live daemon.

**Coverage, stated honestly — corrected after the pre-merge review.** The first
version of this sentence claimed G1b and G2 were mechanically checked. **They
were not**, and the review caught the plan asserting work that did not exist —
the exact pattern this document exists to stop, in the document itself.

Mechanically checked: C1-C7, D1-D3, G1a, G2, S1's isolation table, S6.

**G1b was not implemented.** More importantly, it would not have worked as
specified: `scripts/smoke.sh` never runs `idea model set`, so it never reaches
O1's shape — a configured model with no key — and a bare "did not fail"
assertion on the synthetic path it already exercises would not have caught the
finding it was justified by. Recorded in `deferred.md` with that reasoning
rather than left as a claim.

**D4-D8 were not implemented** (the pidfile clobber, the 413 double body, the
`changed` scoping, the init-elsewhere hint, and trace-type validation). Recorded
in `deferred.md`.

**Not mechanically checkable:** the entire S track's prose, the O track's prose
beyond the commands G1a lints, and all of R. Those get a reviewer pass against
the code they describe — which is how audit 3 found them.

Three specific gaps review insisted be named rather than implied:

- **C2** may land with a test that cannot drive the exit path, if bubbletea
  cannot be run headlessly. The finding whose *previous* fix shipped with a
  vacuous test would then ship again with no regression coverage. If
  `tea.WithInput`/`WithoutRenderer` do not make `watchInitiative` testable, the
  honest outcome is a test documented as not covering the exit path — not a test
  that pretends to. That requires making the `tea.NewProgram` call accept
  injected options.
- **C5** rewrites the test that currently pins the unsafe shape, so `make test`
  cannot detect a wrong fix; the independent `-race` test is what verifies it.
- **The R track and the below-the-cut catalogue** are reviewer-pass only, against
  ~45 prose claims. That is not a verification step, it is a reading. Said
  plainly.

## Out of scope, recorded

- **A redacting `slog` handler** (S4 documents the gap instead).
- **No SDK.** Unchanged.
- **Releasing.** `v0.1.0` remains the only tag and still contains every audit-2
  defect; cutting `0.2.0` is a separate decision after this lands.
- **`linux/arm64` CI.** Unchanged from audit 2's reasoning.
