# DX Audit 3 — Fix Plan

Branch: `worktree-dx-audit-3`, from `main` @ `8fa037d`.

Audit 3 verdict: **FAIL**, Overall DX 74/100 (up from 70). Both hard gates pass.
Report: `docs/plans/dx-audit-3-report.md`. Findings: **6 P0, 7 P1, ~10 P2**, plus
~30 catalogued documentation defects below the cut.

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

So this round's structural work is not another route-level check. It is two
guards aimed at where the failures actually are:

1. **Executable documentation** (G1). Extract every fenced `idea …` command from
   README and the quickstart and run them in CI. This is the only thing that
   would have caught `idea trace task T-3` — a command on the README's front page
   that cannot resolve — or the quickstart's no-key path being broken by the step
   above it.
2. **Response-schema checking** (G2), the mirror of the request-body check added
   last round. Catches `/raw`, the `artifacts/diff` `files` key, and `promote`.

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

Tracks S, O and R touch disjoint file sets and run in parallel. Track C and D
touch `internal/` and run sequentially after, to avoid conflicting edits.

| Track | Scope | Files owned |
|---|---|---|
| S | Security documentation (P0) | `docs/security.md`, `docs/security-model.md`, `docs/architecture.md` |
| O | Onboarding documentation (P0) | `README.md`, `docs/quickstart.md`, `docs/ui.md` |
| R | Reference documentation (P1/P2) | `docs/api.md`, `docs/openapi.yaml`, `docs/configuration.md`, `docs/agents.md`, `CHANGELOG.md` |
| C | Code correctness (P0/P1) | `internal/daemon/`, `internal/workflow/` |
| D | CLI defects (P1/P2) | `internal/cli/` |
| G | New guards | `internal/docsgen/`, `scripts/`, `Makefile`, `.github/workflows/` |

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
precondition is checked. The selectors that would produce those levels are never
emitted, while selector `"task"` — the user's raw typed idea — maps straight to
`TrustHumanApproved`.

**This one is not only a doc fix.** Two options, and the plan takes the second:

- (a) Document what the function does. Honest, but leaves a trust ladder whose
  strongest level is assigned to unreviewed user input.
- (b) **Make the code match the claim** for the one case that matters: the `task`
  selector must not yield `TrustHumanApproved`. A typed idea is
  `external_untrusted` until something records a human approving it.

Take (b): change the mapping, correct the doc to describe what the switch
actually guarantees (a deterministic mapping from selector to level, never from
model output), and drop the two "requires a recorded …" sentences until the
records they describe exist. Add a test that no selector reachable from user
input yields `human_approved`.

*Risk:* changing a trust level changes prompt priority. Verified against callers
before landing; if any pipeline depends on `task` being `human_approved`, that
dependency is itself the finding and gets recorded.

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

Cancel during `fsdiff.Commit`: the API returns 200 and the operation reads
`cancelled`, while the commit completes, files land in the workspace, git commits
them, and the change set is marked applied. The terminal status write then fails
on the cancelled context and **the error is discarded** — as it is at eleven
other sites in `runtime.go`.

**Fix, three parts:**
- `CancelOperation` returns 409 once the change set has left `approved`, so apply
  is uninterruptible past the point where it starts mutating the tree.
- Terminal status writes use `context.Background()`, as `failOperation` already
  does, so a cancelled context cannot lose the record of what happened.
- Stop discarding those errors: log them with the operation id.

*Acceptance:* a test cancels mid-apply and asserts the operation's final status
matches what actually happened to the workspace.

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

### C4. Four async operations check a running flag they never set (P1)

`ReviseInitiative`, `RevisePlanInitiative`, `ApprovePlanInitiative` and
`SuggestAnswersInitiative` each guard on `status == InitiativeRunning` and none
of them sets it, unlike `Advance`, `Retry` and `ReopenPhase`. A double-click on
Revise plan lets both goroutines past: one deletes and recreates the task set
while the other writes dependency edges to task ids that no longer exist, after
which every further revise is refused.

**Fix:** one atomic conditional claim —
`UPDATE initiatives SET status='running' WHERE id=? AND status IN (…)` — 0 rows
affected → 409. Applied to **all seven** async entry points, not the four that
are broken, so the seventh cannot get it wrong.

### C5. Two reload-safety comments are false (P1)

`engine.go` says the swap is "guarded by e.mu, which ALL entry points now hold";
eleven entry points take no lock. `runtime.go` says `Reload` "builds a NEW
`*projectRuntime` and swaps the pointer"; it mutates in place, and a test pins
that shape.

**Fix:** make the comments true rather than correcting them downward — swap a
fresh `*projectRuntime` pointer, and load engine config through an
`atomic.Pointer` once per call (as `describeImage` already does correctly).
Update the test that pins the unsafe shape.

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

### C7. `POST /findings/{id}/disposition` answers 200 for a finding that does not exist (P1)

Bare `UPDATE … WHERE id = ?` with no `RowsAffected` check, while `db.Apply`
commits the `finding.dispositioned` event in the same transaction. So a stale ref
returns 200, nothing changes, and **the event log — the thing this product sells
as provable provenance — records that a human accepted the risk.**

**Fix:** check `RowsAffected`, return `*store.NotFoundError` (writeErr then gives
404), scope the UPDATE by initiative. Same for `/note`. Audit the other bare
UPDATEs in `repo.go` for the same shape.

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

- `GET /operations/{id}` opens every project and runs first-touch reconciliation,
  which can mark another project's initiatives failed and restore files into a
  working tree the request never named. **Scope the route under `{projectId}`, or
  record the owning project in the global registry.** Never reconcile as a side
  effect of a read.
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

### G1. Executable documentation

A script extracts fenced ```bash blocks from `README.md` and
`docs/quickstart.md`, filters to lines starting `idea `, and runs them against a
real daemon in a scratch project. Lines that cannot run unattended are marked in
the source with a trailing `# docs:skip <reason>` comment, and the script
**fails if a skip has no reason** — so skipping is visible rather than silent.

Wired as `make docs-run` and a CI job. This is the only guard that would have
caught O1 and O2, and it is the highest-value item in this plan after the
security docs.

*Scope, stated honestly:* it proves the commands execute, not that their output
means what the prose says.

### G2. Response-schema checking

Mirror of the request-body check: for each operation, compare the JSON keys the
handler writes against the properties the spec declares. Harder than the request
side, because responses are built from domain types rather than anonymous
structs — so the first version covers handlers that write a literal
`map[string]any{…}` or an inline struct, and **fails closed** on any routed
handler whose response shape it cannot read, exactly as the request check now
does.

---

## Verification

```
make test && make vet && make docs-check && make api-check \
  && make api-docs-check && make docs-run && make check-embed \
  && (cd web && pnpm build && pnpm test)
```

Then a full re-run of the audit journey, and direct re-probing of every
acceptance test against a live daemon.

**Coverage, stated honestly.** Mechanically checked: C1-C7, D1-D8, G1, G2, S5's
code half. **Not mechanically checkable** — the entire S track's prose, the O
track's prose beyond the commands G1 runs, and all of R. Those get a reviewer
pass against the code they describe, which is exactly how audit 3 found them, and
is the reason this plan does not claim a guard it does not have.

## Out of scope, recorded

- **A redacting `slog` handler** (S4 documents the gap instead).
- **No SDK.** Unchanged.
- **Releasing.** `v0.1.0` remains the only tag and still contains every audit-2
  defect; cutting `0.2.0` is a separate decision after this lands.
- **`linux/arm64` CI.** Unchanged from audit 2's reasoning.
