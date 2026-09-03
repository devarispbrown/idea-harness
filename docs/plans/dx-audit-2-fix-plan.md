# DX Audit 2 — Fix Plan

Branch: `worktree-dx-audit-2`, cut from `main` @ `69e9278` (v0.1.0 published).

Addresses every finding from the second DX audit: 3 P0, 5 P1, 13 P2, 4 P3.
Audit verdict was FAIL at Overall DX 70/100, forced by `STALE_PUBLIC_REFERENCE`
and `UNEXPLAINED_ERROR` with both hard gates passing.

## The shape of the problem

Nearly every finding is one instance of the same failure: **a fix was made
correctly in one place and not propagated to its siblings.**

- `handleApplyChangeSet` grew a synchronous precondition guard. `handleRevert`
  and `handleExecute` did not.
- `parseArgs` was written with an explicit carve-out for `-` as a positional.
  `cmdDocsAdd`, the one command that needs it, rolls its own loop and never
  calls it.
- Four handlers return 409 for a precondition failure. `advance` and `revise`
  return 500.
- `TestUnknownFlagFailsBeforeTheDaemon` has a hand-written table. The ten
  commands that still swallow flags are exactly the ones missing from it.
- `make api-check` compares `routes.go` to `openapi.yaml`. The spec is correct;
  nothing compares routes to the human-readable tables in `docs/api.md`, which
  is where ten endpoints are missing.
- `make docs-check` regenerates the CLI table from the same `summary` string the
  table is built from, so a flag absent from that string is invisible to the
  check in both places.

So the plan is not only "fix 25 things". Where a defect class exists, the fix is
**structural — make the guard check a second axis** so the next
correctly-made-but-half-applied fix cannot ship. Three structural changes carry
most of the weight:

1. The command table declares each command's `flagSpec`; dispatch enforces it
   centrally; `jsonSupport` is *derived* from the spec rather than hand-kept
   beside it (Track A).
2. Preconditions for async operations live in one place both `apply` and
   `revert`/`execute` call (Track C).
3. Drift checks compare code to the *human* documents, not only to the machine
   spec (Track D).

## Tracks and ordering

| Track | Scope | Depends on |
|---|---|---|
| A | CLI flag discipline (structural) | — |
| B | CLI correctness: silent success, wrong behavior | A |
| C | Daemon / API contract | — |
| E | Release, licensing, repo metadata | — |
| D | Documentation truth + drift guards | A, B, C |

A, C and E run in parallel. B follows A. D is last because it regenerates from
whatever A/B/C land.

---

## Track A — CLI flag discipline

### A1. Declare each command's flags in the command table

`internal/cli/cli.go`'s `command` struct gains a field:

```go
// flags declares what this command accepts. Dispatch enforces it before the
// command runs, so a command cannot silently swallow an unknown flag by
// forgetting to call parseArgs. nil means "not yet migrated" and is rejected
// by TestEveryCommandDeclaresItsFlags — every row must be explicit, including
// the empty spec flagSpec{} for commands that take no flags.
flags flagSpec
```

Every dispatchable row gets one. Documentation-only rows (`run == nil`) do not.

### A2. Enforce centrally in `Run`

`Run` calls `parseArgs(c.flags, rest)` before `c.run`, and passes the result
down. This changes the `run` signature:

```go
run func(a cmdArgs, stdout, stderr io.Writer) error

type cmdArgs struct {
    positional []string
    flags      parsedFlags
}
```

Commands that currently call `parseArgs` themselves drop that call and read from
`a`. Commands that use `wantJSON(args)` read `a.flags.has("--json")`.
`wantJSON` is deleted — it is the exact-match scan that made
`idea status --jsonn` print ANSI text to stdout with exit 0.

**Why the signature change rather than validating and discarding.** Validating
centrally while letting commands re-parse leaves two specs per command that can
disagree; the drift we are removing would come straight back in a new form. One
parse, one spec, one source.

### A3. Derive `jsonSupport` from the spec

`jsonSupport` stops being a hand-maintained field. It becomes a method:

```go
func (c command) jsonDoc() string {
    switch {
    case c.run == nil:            return ""        // documentation-only row
    case len(c.flags) == 0:       return "takes no flags"
    case c.rejectsJSON:           return "rejected — use `idea tasks --json`"
    case c.flags["--json"] == flagBool: return "yes"
    default:                      return "flags, no --json"
    }
}
```

The `json` field and the `jsonNone`/`jsonYes`/`jsonNoFlags`/`jsonRejected`
constants are deleted; only `rejectsJSON bool` survives for `plan`, whose
rejection is a deliberate pointer elsewhere. This kills finding P2-13: six rows
recorded as `—` actually behave as "takes no flags", and the same `—` also
covered ten commands doing the opposite.

### A4. A test that cannot be forgotten

Replaces the hand-written table in `internal/cli/json_flag_test.go`:

```go
// Iterates commands() itself, so a new command joins the test the moment it
// joins the table. The hand-written table is what let ten commands drift.
func TestEveryCommandRejectsAnUnknownFlag(t *testing.T)
func TestEveryCommandDeclaresItsFlags(t *testing.T)  // flags != nil
func TestJSONDocMatchesTheSpec(t *testing.T)
```

`TestEveryCommandRejectsAnUnknownFlag` must assert the rejection happens
**before any network call**, which is what the existing test's name promises.
Commands are invoked with `IDEA_HARNESS_ADDR` pointed at a closed port; an
unknown flag must produce the flag error, not a connection error.

**Migrated commands:** `status`, `changed`, `executors`, `logs`, `docs`,
`model`, `config`, `init`, `doctor`, `dev`, `daemon`.

**Risk.** This touches every command. Mitigation: it is mechanical, the compiler
finds every call site, and the three new tests fail loudly on an incomplete
migration. Commit A separately from B so a bisect can separate refactor from
behavior change.

---

## Track B — CLI correctness

### B1. `idea docs add -` (P0)

`cmdDocsAdd` replaces its hand-rolled loop with
`parseArgs(flagSpec{"--kind": flagValue, "--title": flagValue}, args)`. The
stdin branch below it already works; only the parser rejected `-`.

*Acceptance:* the literal line from `docs/quickstart.md:47` runs and creates the
document. A test pipes stdin through `cmdDocsAdd` with `-`.

### B2. `idea verify` records nothing but reports success (P1)

Validate before `connect()`:

- a disposition flag (`--pass`/`--fail`/`--waive`) with no requirement → error
  naming the requirement argument;
- more than one disposition → error;
- `--note` with no disposition → error.

*Acceptance:* `idea verify --pass --note x` exits non-zero; `--pass --fail REQ-1`
exits non-zero.

### B3. `idea model add` / `model set` swallow flags (P1)

Both route through `parseArgs`. `add` uses
`flagSpec{"--type": flagValue, "--base-url": flagValue, "--key-env": flagValue}`;
`set` uses `flagSpec{"--role": flagValue}`. The parser already errors on unknown
flags and missing values, so the silent no-ops disappear.

Additionally: `addProvider` rejects a provider with neither `--type` nor
`--base-url`, since `ValidateProvider` returns nil early when `BaseURL == ""`
and the daemon therefore accepts an endpoint-less provider.

*Acceptance:* `idea model add x --base_url u` exits non-zero naming the flag.

### B4. `idea daemon <typo>` starts a foreground daemon (P1)

`runDaemon`'s switch gains a `default` that errors naming the valid
subcommands. Bare `idea daemon` (no rest) keeps running the daemon.

### B5. `idea daemon status` exit code and writer (P2)

Returns a non-nil error when not running, and writes to the passed `stdout`
rather than `fmt.Println` to the process stdout, so `Run(args, stdout, stderr)`
can test it.

### B6. `idea config <typo>` (P2)

`cmdConfig` rejects an unknown subcommand non-zero instead of falling through to
`showConfig`. The table row already advertises "non-zero exit when invalid".

### B7. `idea init <path>` (P2)

`cmdInit` currently ignores all arguments and initializes `os.Getwd()`. Honor
the positional: `idea init <dir>` initializes that directory; reject more than
one positional. (Chosen over "reject any positional" because the argument reads
as intent and honoring it is what a user means.)

*Acceptance:* `idea init ./sub` creates `./sub/.idea-harness/`; `idea init a b`
exits non-zero.

### B8. `idea doctor` false green outside a project (P2, carry-forward)

Unfixed since audit 1. `config.Load` on a directory with no `.idea-harness`
returns a usable default rather than an error, so doctor prints `✓ config valid`
and `all checks passed` with exit 0 anywhere on the filesystem.

Doctor gains an explicit project check first: when `.idea-harness/` is absent,
report `✗ no project here — run \`idea init\`` and exit non-zero. Daemon and
version lines still print, since they are useful outside a project.

### B9. `idea watch` hangs on a bad reference (P2)

`watchModel.poll` stores the error and returns, so `m.done` never sets and the
program spins at 1 Hz forever, then exits 0 on Ctrl-C. Fix: a poll error that is
a not-found (or persists beyond a small threshold) quits the Bubble Tea program
and `watchInitiative` returns it, so `main.go` sets exit 1.

### B10. `idea logs` argument handling (P2)

`parseArgs(flagSpec{"-f": flagBool, "--follow": flagBool, "-n": flagValue}, …)`.
A non-numeric positional is an error rather than silently discarded. `-n 200`
works, since it is the flag people reach for.

---

## Track C — Daemon and API contract

### C1. `GET …/risk` writes on every read (P0)

`handleRisk` calls `SaveRiskProfile`, which is explicitly append-only ("every
computed profile is retained"). Measured: 3 GETs → +3 `risk_profiles` rows, +3
events, unbounded.

**Fix:** `handleRisk` becomes pure — compute and return, no persistence. The
durable tier floor the comment justifies is preserved by persisting from the
pipeline that *produces* change sets (where a profile corresponds to a real
state change), not from a read.

Persisting-on-read cannot be kept behind a dedupe either: the store's contract
is history, so a correct dedupe would contradict it. Removing the write from the
read path is the change; the write moves to the execute/apply path that already
has a natural once-per-change-set trigger.

*Acceptance:* N identical GETs leave `risk_profiles` and `events` counts
unchanged. Test asserts the counts directly.

### C2. `revert` and `execute` report success on impossible operations (P0)

`handleApplyChangeSet` has the guard and the comment explaining why. Extract the
shared shape:

- `handleRevertChangeSet` rejects a change set not in `applied` status with 409
  and a `revertStateMessage(cs)` written in the same voice as
  `applyStateMessage`.
- `handleExecute` / `ExecuteInitiative` gain a precondition (approved plan, a
  ready task) and return 409 when there is nothing to run.

*Acceptance:* `idea revert` on a `reverted` change set exits non-zero naming the
status, matching `apply`'s existing behavior. `idea execute` with nothing ready
exits non-zero.

### C3. Precondition errors returned as 500 (P1)

`handleAdvance` and `handleRevise` map every error to 500, including "initiative
is already running" and "advance requires an approved PRD". Their four siblings
(`retry`, `approve-plan`, `reopen-phase`, `revise-plan`) return 409.

Rather than hand-mapping a sixth handler, introduce a typed precondition error
in `internal/daemon` (or reuse the existing domain error convention) that
`writeErr` maps to 409, and return it from the runtime's precondition checks.
That way the seventh handler cannot get it wrong.

*Acceptance:* `advance` twice returns 409, not 500.

### C4. Raw error with absolute state path reaches the client (P2)

`handlers.go:2230` is the one surviving `writeError(w, …, "…"+err.Error())`.
`ReloadProject` wraps `*fs.PathError` carrying absolute state paths — exactly
what `store.IsStorageFailure` and `s.writeErr` exist to mask.

Route it through `s.writeErr(w, http.StatusConflict, fmt.Errorf("config written
but not applied (reload failed): %w", err))`, and extend the existing
"`/Users/` never appears in a user-facing message" test to cover this path,
which today nothing exercises.

### C5. Two wildcard names for one path segment (P2)

`{id}` on 53 route registrations, `{initiativeId}` on 10 (`routes.go:90-99`),
read by two helpers (`s.initiative` vs `s.initiativeFromPath`). Adding a route
under the wrong name makes `http.ServeMux.Handle` panic inside `NewServer`,
killing the daemon at startup without naming the offending route.

Unify on `{initiativeId}` — the explicit name, and the one the ten
list-endpoints already use — updating both helpers to one. A test asserts
`NewServer` does not panic and that no two registered patterns differ only by
wildcard name.

**Note:** this is internal only. Path *values* are unchanged, so it is not an
API break and needs no changelog migration entry.

### C6. Response envelope and query escaping (P2)

- `handleAttention` is the only list endpoint returning `{"items": [...]}`;
  every other returns a bare array. Make it a bare array. **This one is a
  breaking response-shape change** — `web/src/` consumes it, so the UI call site
  changes in the same commit, and it gets a CHANGELOG entry.
- `internal/cli/advance.go:252,291` and `internal/cli/model_cmd.go:82`
  concatenate query values unescaped, so `idea why assertion 'x&type=task'`
  rewrites the query. Replace with `url.Values{}.Encode()`.

---

## Track D — Documentation truth and drift guards

### D1. `docs/api.md` is ten endpoints behind (P1, the gate failure)

The document states "71 routes" and its tables contain exactly 71 rows;
`routes.go` registers 81. Missing: `retry`, `reopen-phase`,
`requirements/{reqId}/verification-task`, `attention`, `impact-counts`,
`operations`, `artifacts/diff`, `artifacts/{id}/comments` (GET and POST),
`comments/{id}/promote`. README advertises the document as covering "every
endpoint".

**Fix:** document all 81, and add the guard that would have caught it —
`make api-docs-check` compares `routes.go` registrations against the endpoint
table rows in `docs/api.md` and fails on either direction of mismatch. Wired
into CI beside `api-check`.

This is the second axis: `api-check` proves routes ↔ spec, the new check proves
routes ↔ prose.

### D2. Stale claims in reference docs (P1)

| File | Claim | Truth |
|---|---|---|
| `docs/architecture.md:73,90-91` | "No source mutation in M1: only `.idea-harness/` is written into the working tree" | `apply` writes patches into the workspace and can create a commit |
| `docs/architecture.md:25` | "REST + SSE (OpenAPI planned)" | shipped: 81 operations, served at `/openapi.yaml`, checked in CI |
| `docs/security.md:13` | token "required on every `/api/v1` request" | `GET /api/v1/health` is unconditionally exempt (`server.go:83-86`) |
| `docs/security.md:59-62` | sandbox, capability broker, executor isolation, apply journal "deferred to M3" | all shipped in v0.1.0 |
| `docs/cli.md:70-71` | `next` "takes no flags" | `next` takes `--until-human`; its own generated table 25 lines above says so |
| `docs/configuration.md:74` | `executor.image` default `—` | `alpine:3.20` (`container.go:44`), as `docs/agents.md:86` correctly says |

`docs/cli.md`'s prose is fixed by Track A: once `jsonDoc()` derives from the
spec, the generated table is authoritative and the contradicting prose paragraph
is deleted rather than corrected. Its unknown-flag list (which omits 12
commands and closes with the now-false "the remaining commands still ignore
flags they do not recognize") is deleted too — after Track A the correct
statement is "every command rejects unknown flags", which the generated table
already carries.

### D3. `idea verify --fail` / `--waive` documented nowhere (P1)

Implemented in the CLI and accepted by the daemon
(`VerificationFail`/`VerificationWaived`), but absent from the command summary,
the generated `docs/cli.md` row, README, quickstart, `docs/api.md` and the
`verifyInitiative` schema in `docs/openapi.yaml`.

Both existing guards are structurally blind here: `docs-check` regenerates from
the summary string, and `api-check` compares paths, not request schemas. Fix the
summary (which fixes the generated row), the prose docs, and the OpenAPI request
body. Widening `api-check` to compare request schemas is out of scope for this
round and recorded as follow-up.

### D4. `docs/api.md` 409 account (P2)

Names one source; the daemon writes 409 from ~14 sites, including the most
common one a client will meet (`apply` on a non-appliable change set). Replace
with the actual list, grouped by cause.

### D5. `docs/security-model.md` orphaned and inconsistent (P2)

Linked only from plan files, never from README or any user-facing doc, and calls
"trust tiers" what `docs/security.md` calls "trust levels". Link it from README
and from `docs/security.md`; settle on **trust tier** (the term the code uses)
and update `security.md`. Add the control-plane deny — the largest security
control in v0.1.0, and the one whose case-sensitivity bypass reached a draft
release — to `security.md`'s threat table, where it is currently absent.

### D6. P3 corrections

- `CHANGELOG.md:60` "70 operations" → 81.
- `README.md:229-231` links `docs/api.md` twice with different descriptions —
  keep one.
- `docs/api-reference-hosting.md:25` "120 KiB" → actual size, or drop the
  figure so it cannot go stale again (preferred).
- `docs/cli.md:135` "`idea new` returns an operation id" → it prints
  `initiative_id`.
- `README.md:166` provider list omits DeepSeek, a built-in preset that
  `docs/configuration.md` and `docs/ui.md` both list.
- `docs/agents.md:44-46` `--smoke` paragraph ends in a colon with no example.
- `executors setup --smoke` appears in quickstart and agents.md but in neither
  `docs/cli.md` nor README.

---

## Track E — Release, licensing, repo metadata

### E1. No license anywhere (P1)

No `LICENSE` file, no README license section, none in the release archive,
GitHub detects none. Under default copyright nobody the binary is shared with
may legally use, copy or modify it.

Add `LICENSE` at the repo root, a README license section, and `LICENSE` to
`.goreleaser.yaml`'s archive `files:` list beside `README.md` and
`CHANGELOG.md`.

**Open decision — requires the owner.** The license itself is the user's call,
not mine. Default proposal: **Apache-2.0** (permissive, explicit patent grant,
the common choice for a developer tool intended for adoption). MIT if brevity is
preferred; a source-available license if commercial control matters. I will
implement Apache-2.0 unless told otherwise, and will flag it in the summary
rather than bury the choice.

### E2. Repository metadata (P2)

`description`, `homepageUrl` and topics are all empty — the discovery stage has
nothing to work with. Populate via `gh repo edit`. **Requires the owner's
approval before I run it**, since it changes the public-facing repository, and
the repo being private means the value is realized at launch.

### E3. Untested shipped architectures (P2)

Four binaries ship; two architectures execute in CI (linux/amd64 via ubuntu,
darwin/arm64 via macos). darwin/amd64 and linux/arm64 are shipped untested.

Risk is genuinely low — pure Go, `CGO_ENABLED=0`, zero arch-specific source
files, one `//go:build unix` file, and both *operating systems* are tested.
Adding emulated arch jobs costs CI minutes for little signal.

**Chosen fix: state the matrix precisely** rather than expand CI. README
currently says "Both are built and tested in CI", which is true at OS
granularity and misleading at arch granularity. Replace with an explicit table
of what is built versus what is executed in CI. Revisit if any arch-specific
code ever lands.

---

## Verification

Every track ends green on:

```
make test && make vet && make docs-check && make api-check && make api-docs-check && make check-embed
```

Plus a re-run of the audit journey against a binary built from this branch, and
direct re-probing of each acceptance test above with a real daemon — the audit
findings were established by running the product, and they get closed the same
way, not by reading the diff.

## Out of scope, recorded

- **No SDK.** Scored 65/100 as a documented, deliberate gap. Generating one from
  the now-complete spec is a milestone, not a fix.
- **Public obtainability.** The repository is private; the audit's install
  measurements describe the access-granted population. Making it public is a
  launch decision, not a defect fix. E1 and E2 are its prerequisites.
- **Widening `api-check` to request/response schemas** (see D3).
- The `MAGIC_PATH_MAX_COMMANDS` overage (11 vs 8) was partly audit
  instrumentation; README's documented path is 8 commands, at target. No change.
