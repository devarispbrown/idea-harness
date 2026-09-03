# DX Audit 2 — Fix Plan (v2)

Branch: `worktree-dx-audit-2`, cut from `main` @ `69e9278` (v0.1.0 published).

Addresses every finding from the second DX audit. Audit verdict was **FAIL** at
Overall DX 70/100, forced by `STALE_PUBLIC_REFERENCE` and `UNEXPLAINED_ERROR`
with both hard gates passing. The audit report is committed alongside this plan
at `docs/plans/dx-audit-2-report.md` so the finding list is checkable by someone
other than its author.

**v2 supersedes v1 after three adversarial reviews** (Go/API correctness;
coverage and refactor risk; guards, verification and release). The reviews found
**seven blocking defects in the plan itself**, four of which would have shipped
bugs worse than the ones being fixed. What changed is recorded in
`dx-audit-2-fix-plan-review.md`; the corrections are folded in below and the
material reversals are called out inline as **[v1 was wrong]**.

## The shape of the problem

Nearly every finding is one instance of the same failure: **a fix was made
correctly in one place and not propagated to its siblings.**

- `handleApplyChangeSet` grew a synchronous precondition guard. `handleRevert`
  and `handleExecute` did not.
- `parseArgs` was written with an explicit carve-out for `-` as a positional.
  `cmdDocsAdd`, the one command that needs it, rolls its own loop.
- Four handlers return 409 for a precondition failure; `advance` and `revise`
  return 500.
- `initiativeResource()` was added in audit 1 *specifically* to escape path
  segments. Eight call sites still concatenate raw.
- `make api-check` proves routes ↔ spec. Nothing proves routes ↔ the prose
  tables, which is where ten endpoints are missing.
- `make docs-check` regenerates the CLI table from the same `summary` string the
  table is built from, so a flag absent from that string is invisible on both
  sides of the diff.

So the fix is structural where a defect class exists: **make each guard check a
second axis.** Three structural changes carry the weight.

1. Every dispatch target — including subcommands — declares its `flagSpec`;
   dispatch enforces it; **the generated CLI table renders the flag names from
   that spec** (Track A).
2. Preconditions for async operations live in one typed place (Track C).
3. Drift checks compare code to the *human* documents and to *request bodies*,
   not only to spec paths (Track D).

### The second axis, stated precisely

Review 3 made the sharpest point in this round and it reshaped Track A:

> Track A centralizes *enforcement* while leaving *documentation* keyed to the
> same hand-written `summary` string, so the D3 class survives intact. Without
> rendering flag names into the table, Track A is a refactor, not a guard.

That is correct. `renderCommandTable` emits `usage`, `summary`, `jsonDoc()`.
Deriving only the third column changes nothing about where flag *names* come
from — so `--waive` could sit in `verify`'s spec, be enforced at runtime, and
stay absent from `docs/cli.md` with `make docs-check` green. That is finding (k)
verbatim, surviving its own fix. **A5 is therefore the load-bearing item of
Track A**, not A3.

## Traceability

| # | Finding | Item |
|---|---|---|
| a | `GET …/risk` mutates on read | C1 |
| b | `revert`/`execute` succeed on impossible ops | C2 |
| c | `docs add -` rejected by parser | B1 |
| d | No LICENSE | E1 ✅ |
| e | `verify` records nothing, reports success | B2 |
| f | `model add`/`set` swallow flags | B3 |
| g | api.md 71 vs 81 routes | D1 |
| h | architecture.md two false claims | D2 |
| i | security.md two false claims | D2 |
| j | cli.md prose contradicts its own table | D2, A5 |
| k | `verify --fail`/`--waive` documented nowhere | D3, A5 |
| l | advance/revise 500 not 409 | C3 |
| m | `idea daemon <typo>` starts a daemon | B4 |
| n | `doctor` false-green (carry-forward) | B8 |
| o | `idea init <path>` initializes cwd | B7 |
| p | `config valdiate` exits 0 | B6 |
| q | Ten commands swallow unknown flags | A1–A5 |
| r | `idea docs --jsn` | A1, B1 |
| s | `watch` spins forever, exits 0 | B9 |
| t | `daemon status` exit 0 + process stdout | B5 |
| u | State path leaks on reload failure | C4 |
| v | Two wildcard names | C5 |
| w | attention envelope + unescaped values | C6 |
| x | security-model.md orphaned + terminology | D5 |
| y | Repo metadata; 409 account; cli.md flag list | E2 ✅, D4, A5 |
| z | Two shipped architectures untested | E3 |
| P3 | changelog count, executor.image, README dupe, hosting size | D6 |

Counts: **3 P0** (a, b, c), **10 P1** (d–m), **13 P2** (n–z), **4 P3**.
(v1's header said "5 P1" while its body labelled eight; the audit report's P1
section groups related findings that the working list splits.)

## Tracks and ordering

| Track | Scope | Depends on |
|---|---|---|
| A | CLI flag discipline + the generated-flags guard | — |
| B | CLI correctness: silent success, wrong behavior | A |
| C | Daemon / API contract | — |
| E | Release, licensing, repo metadata | — |
| D | Documentation truth + drift guards | A, B, C |

A, C and E run in parallel. B follows A. D is last: it regenerates from what
A/B/C land.

---

## Track A — CLI flag discipline

### A1. Declare flags per **dispatch target**, not per top-level command

**[v1 was wrong]** v1 put one `flagSpec` on each top-level row. Five commands
multiplex subcommands with disjoint flag sets:

| Command | Subcommand flags |
|---|---|
| `model` | `set --role`; `add --type --base-url --key-env`; `ls` none |
| `docs` | `docs --json`; `docs add --kind --title` |
| `executors` | `executors --json`; `setup --kind --image --model --yes --smoke` |
| `config` | `validate` (none) |
| `daemon` | `status` / `stop` (none) |

A union spec would make `idea model ls --key-env X`, `idea docs --kind x` and
`idea executors --smoke` *accepted* — reintroducing the silent-swallow class one
level down, blessed by the structural fix. It also contradicted B3, which gives
`add` and `set` different specs.

```go
type command struct {
    ...
    // flags is the spec for the bare command. subs holds one spec per
    // subcommand token; a command with subcommands declares both, and Run
    // selects by the first positional. Declared per dispatch target because a
    // union spec would accept `idea model ls --key-env X`.
    flags flagSpec
    subs  map[string]flagSpec
}
```

`Run` peeks `rest[0]`; if it names a key in `subs`, it parses `rest[1:]` against
that spec and passes the subcommand through in `cmdArgs`; otherwise it parses
`rest` against `flags`. **B4 and B6 (reject unknown subcommands) then fall out
of the table** instead of needing hand-written `default:` arms.

`nil` vs empty is load-bearing and gets an explicit branch everywhere, never
`len() == 0`: a dispatchable row with a nil spec is a programming error that
`Run` rejects loudly, because the compiler cannot catch an omitted `flags:`
field in a keyed struct literal. `TestEveryCommandDeclaresItsFlags` is the only
guard and is CI-gating.

### A2. Enforce centrally in `Run`

```go
run func(a cmdArgs, stdout, stderr io.Writer) error

type cmdArgs struct {
    sub        string   // "" when no subcommand matched
    positional []string
    flags      parsedFlags
}
```

`wantJSON` is deleted — that exact-match scan is what let
`idea status --jsonn` print ANSI to stdout with exit 0.

Two delegating commands need real restructuring rather than a mechanical
rewrite, because they forward *raw* arg slices: `cmdPlan` prepends
`"execution_plan"` and calls `cmdExport`; `cmdDocs` forwards `args[1:]` to
`cmdDocsAdd`. Under `subs` the second becomes a dispatch concern; the first
synthesizes a `cmdArgs`.

**Blast radius** (measured, review 2): 37 dispatchable rows, 33 named `run`
functions, 4 inline closures, 9 non-table helpers, 23 in-command `parseArgs`
calls deleted, 7 `wantJSON` call sites rewritten, and **4 hand-rolled flag loops
deleted — `cmdNew`, `cmdDocsAdd`, `cmdExecutorSetup`, `cmdModel set`.**
`cmdNew` and `cmdExecutorSetup` appeared in no track in v1; `cmdNew` contains
the same `-`-rejecting arm that B1 exists to delete.

*A zero `parsedFlags{}` is safe: `has`/`value`/`list` read nil maps and return
zero values. No defensive initialization needed.*

### A3. Derive `jsonSupport` from the spec

**[v1 was wrong, twice.]** `flagBool` is `iota` — the zero value — so
`c.flags["--json"] == flagBool` is true for every command that does *not*
declare `--json`. v1's snippet would have labelled `logs`, `next` and `help` as
`yes`. And it tested `len(c.flags) == 0` *before* `rejectsJSON`, silently
flipping `plan`'s row from `rejected — use idea tasks --json` to
`takes no flags`.

Fixes: add a `flagUnset` sentinel at `iota` 0 so no real kind is the zero value;
use comma-ok; test `rejectsJSON` **first**; return `"—"` (not `""`) for
documentation-only rows. `plan` declares `{"--json": flagBool}` so the central
parser accepts it and `cmdPlan` rejects it with its own pointer message, which a
test locks — no test asserts that string today.

### A4. Tests that cannot be forgotten

Iterate `commands()` so a new command joins the test the moment it joins the
table.

**[v1 was wrong]** v1 proposed pointing `IDEA_HARNESS_ADDR` at a closed port.
`ensureDaemon` does not fail on a closed port — it spawns
`exec.Command(os.Executable(), "daemon")` detached and polls for 5 seconds. Under
`go test` that is the *test binary*, so on the very regression the test exists to
catch it spawns strays and burns 5s per command. Worse, `daemon` and `dev` call
`daemon.Run` in-process and block forever, and `new` opens an interactive form —
**the test would hang rather than fail.**

Use the harness that already exists: `newFakeDaemon(t)` plus the
`len(f.requests)` before/after assertion, which is how
`TestUnknownFlagFailsBeforeTheDaemon` proves "before the daemon" today. Rows
that block (`daemon`, `dev`, `new`, `watch`) carry a `noAutoInvoke` marker with
the reason, and are covered by asserting on the dispatch-level parse instead.
Each case runs under a timeout so an incomplete migration fails loudly.

### A5. **Render the flag names into the generated table** — the second axis

`renderCommandTable` gains a Flags column rendered from
`sorted(keys(c.flags))` and each subcommand's spec. A flag that exists in code
now *cannot* be absent from `docs/cli.md`: `make docs-check` fails.

This is what closes finding (k) as a class rather than as an instance, and it
replaces v1's `TestJSONDocMatchesTheSpec`, which was vacuous — `jsonDoc()` is
*defined* as a pure function of `c.flags`, so asserting it matches `c.flags`
asserts nothing.

### A6. Regenerate `docs/cli.md` **inside the Track A commit**

**[v1 was wrong]** v1 deferred all doc regeneration to Track D. But
`make docs-check` gates every commit, and Track A alone rewrites the table: 17
dispatchable rows change their JSON cell, 7 documentation-only rows change, and
A5 adds a column to all 44. A commit that does not regenerate is red.

The `docs/cli.md` "JSON output" prose section documents exactly four cell values
and gains a fifth; `takes no flags` stops meaning four commands and starts
meaning fifteen. **That whole section is rewritten**, not patched.

---

## Track B — CLI correctness

| Item | Finding | Fix | Acceptance |
|---|---|---|---|
| **B1** | (c), (r) | `cmdDocsAdd` uses `parseArgs(flagSpec{"--kind": flagValue, "--title": flagValue})`; the stdin branch below already works. `cmdDocs`' hand-filtering of `--json` goes away with `subs`. | The literal line at `docs/quickstart.md:47` runs and creates the doc; a test pipes stdin through `-`. |
| **B2** | (e) | Validate *before* `connect()`: a disposition flag with no requirement; more than one disposition; `--note` with no disposition. | `idea verify --pass --note x` and `--pass --fail REQ-1` both exit non-zero. |
| **B3** | (f) | `add` and `set` get their own specs via `subs`. `addProvider` also rejects a provider with neither `--type` nor `--base-url`, since `ValidateProvider` returns nil early when `BaseURL == ""`. | `idea model add x --base_url u` exits non-zero naming the flag. |
| **B4** | (m) | Falls out of `subs`: `daemon` declares `status`/`stop`; an unmatched token is rejected by dispatch. Bare `idea daemon` still runs the daemon. | `idea daemon stauts` exits non-zero naming valid subcommands. |
| **B5** | (t) | Non-nil error when not running; write to the passed `stdout`, not `fmt.Println`. | `Run` test asserts both. |
| **B6** | (p) | Falls out of `subs`. | `idea config valdiate` exits non-zero. |
| **B7** | (o) | Honor the positional: `idea init <dir>` initializes that directory; >1 positional is an error. Chosen over rejecting all positionals because the argument reads as intent. **Changes `usage`/`summary`, so it changes `idea help` output — see B11.** | `idea init ./sub` creates `./sub/.idea-harness/`; `idea init a b` exits non-zero. |
| **B8** | (n) | Carry-forward, unfixed since audit 1. `config.Load` returns a usable default for a directory with no `.idea-harness`, so doctor prints `✓ config valid` anywhere. Add an explicit project check first: absent `.idea-harness/` → `✗ no project here — run \`idea init\`` and non-zero. Daemon and version lines still print. | `idea doctor` in an empty dir exits non-zero and says "no project". |
| **B9** | (s) | `watchModel.poll` stores the error and returns, so `m.done` never sets. **Concrete threshold** (v1 said "a small threshold" and was not mechanically checkable): a not-found error quits immediately; any other poll error quits after **5 consecutive failures**. `watchInitiative` returns it so `main.go` exits 1. | Test drives `watchInitiative` against a fake daemon returning 404 and asserts a non-nil error within 2s. |
| **B10** | (q) | `parseArgs(flagSpec{"-f": flagBool, "--follow": flagBool, "-n": flagValue})`; a non-numeric positional is an error. | `idea logs -n 200` works; `idea logs 200x` exits non-zero. |
| **B11** | new | `inHelp`'s byte-stability invariant is enforced by comment only — no test locks `idea help`. B7 and D3 both change it. Add the golden test and update the golden in the same commits. | `go test` fails on an unintended help change. |

---

## Track C — Daemon and API contract

### C1. `GET …/risk` writes on every read (P0, finding a)

**[v1 was over-engineered]** v1 said "move persistence to the pipeline that
produces change sets." Review 1 established that `risk_profiles` has **no
`SELECT` anywhere in the tree** — the table has zero readers. Both consumers of
risk data (`web/src/HealthPage.tsx`, `internal/cli/change.go`) read the live
response. So v1 proposed relocating a write nobody reads, to a hook computing a
*different* quantity (per-change-set, not initiative-aggregate).

The justifying comment is self-refuting: `ComputeRiskProfile` is a pure function
of the concatenated patches, so the "tier floor re-derived from past scans" is
recomputable for free at any time.

**Fix: delete the `SaveRiskProfile` call from `handleRisk`. Add no replacement
write.** Leave `SaveRiskProfile` and the table dormant with a comment saying the
profile is recomputable and a durable one needs a reader first.

*Acceptance, strengthened:* `SaveRiskProfile` goes through `db.Apply` with
`EventType: "risk_profile.saved"`, so a GET also appends an **event** — which
`handleActivity` returns unfiltered and `handleEvents` streams over SSE. A read
therefore pollutes the activity feed and the live event stream, not just a
table. The test asserts N GETs leave `risk_profiles`, `events`, **and the
activity feed** unchanged.

### C2. `revert` and `execute` report success on impossible operations (P0, finding b)

**Revert half — as v1, and better supported than v1 knew.** `workflow` already
enforces `cs.Status != ChangeSetApplied`; the handler just needs to say so
synchronously, like `apply` does. `applied` is the correct and *complete* set:
every mid-apply failure path calls `fsdiff.Restore` then marks `conflicted`, and
`reconcileStaleOperations` moves a crashed `applying` to `conflicted` or back to
`approved`. So neither `applying` nor `conflicted` is revertible.

**Execute half — [v1 was wrong] and would have broken `idea next --until-human`.**
`NextAction` returns `"execute"` whenever status is `plan_approved` and no change
set is in review, **without checking that a ready task exists**. Today that
mismatch produces a background failure; a 409 precondition converts it into a
hard error out of the `--until-human` loop. v1 would have traded one bad DX for
another.

Also: `nextReadyTask` is unexported and needs `ListTasks` + edges + a
per-dependency applied-change-set check, and `Execute` holds `e.mu` for a model
call plus a container run — minutes. A synchronous ready-task check either
blocks the handler behind an in-flight pipeline or races it.

**Fix, scoped:** the precondition checks only the cheap, race-tolerant part —
initiative status must be `plan_approved`, matching what `ApprovePlanInitiative`
and `RevisePlanInitiative` already assert. "No ready task" stays on the async
path. **In the same commit, fix `NextAction` so it never proposes an impossible
`execute`**, returning `verify`/`done` instead. TOCTOU is acknowledged and
narrowed, not closed; the async failure path remains the backstop.

### C3. Precondition errors returned as 500 (P1, finding l)

A typed `*PreconditionError` that `writeErr` maps to 409, so the seventh handler
cannot get it wrong. **Narrowed after review:** returned *only* by
`Runtime.*Initiative` precondition checks, not by `internal/workflow`, and
enumerated exactly — `runtime.go:392` ("already running"), `:395` ("advance
requires an approved PRD"), `:498` ("initiative is running; wait…").

The hazard is the **14 sites passing `http.StatusBadRequest`** (notably
`ReviewChangeSet` and `VerifyInitiative`): if an engine precondition error on
those paths gets the new type, the response silently flips 400 → 409. A test
asserts all 14 still return 400. The reverse risk is absent by construction —
`IsStorageFailure` is checked *before* the tail branch and uses `errors.As`, so a
driver error wrapped in a precondition error still lands as 500.

*Acceptance:* `advance` twice → 409, **plus** a table test over the handler set
asserting a typed precondition error maps to 409 wherever it surfaces. That
tests the invariant; testing only `advance` tests the sixth handler.

### C4. Raw state path reaches the client on reload failure (P2, finding u)

**[v1 was wrong, and would have been worse than the bug.]** v1 proposed
`s.writeErr(w, 409, fmt.Errorf("config written but not applied: %w", err))`.
`IsStorageFailure` uses `errors.As`, so `%w` *preserves* detectability: `writeErr`
takes the storage branch and emits **HTTP 500 "internal error (correlation id)"**,
destroying the message. The user would learn neither that their config was
written nor that the daemon kept the old one.

**Fix: classify first, then respond.** If `IsStorageFailure(err)`, log with a
correlation id and write **409 with a fixed, path-free message** — "config
written but not applied (reload failed); the daemon kept the previous
configuration — see `idea logs` (correlation id: X)". Otherwise
`s.writeErr(w, 409, err)`, so `ReloadProject`'s legitimate domain error
("cannot reload while N operations are running") survives verbatim.

*Acceptance:* extend the existing "`/Users/` never appears" test to this path —
**and assert the 409 status**, or it passes trivially on a 500.

### C5. Two wildcard names (P2, finding v)

**[v1 was wrong, and dangerous.]** v1 proposed renaming `{id}` → `{initiativeId}`
across 53 registrations. Only **32 of those 53 are initiatives**; the other 21
are operations, decisions, questions, artifact versions, doc comments, findings
and change sets. Renaming them makes `PathValue("initiativeId")` return `""` —
no panic, no compile error, permanent 404s — across ~16 of the 29
`PathValue("id")` call sites.

v1's stated motivation was also false. I claimed the mux panics "without naming
the offending route"; it names **both** patterns with their `file:line`:

```
pattern "GET /a/{initiativeId}/x" (registered at main.go:9) conflicts with
pattern "GET /a/{id}/x" (registered at main.go:8)
```

**Fix: unify in the other direction.** Rename the **10** `initiatives/{initiativeId}`
routes to `{id}`. Measured: 10 route strings, 13 `openapi.yaml` lines, 10 doc
rows, and exactly **one** `PathValue("initiativeId")` call site in the package.
Zero per-call-site classification, zero silent-empty-string risk — against 32
routes, 64 spec lines and 29 hand-classified call sites the other way.

Keep the anti-collision test (no two registered patterns differ only by wildcard
name) — it is the part that earns its keep. The real hazard it guards is not the
panic (which is loud and well-labelled) but a *new* route registered under one
name whose handler reads the other.

### C6. Query escaping (P2, finding w)

**[v1 was wrong to include the envelope change.]** v1 flattened
`handleAttention`'s `{"items": […]}` to a bare array. Dropped:
`/attention` is the one cross-initiative rollup and the natural candidate for a
cursor later; the envelope is the better shape. Flattening buys consistency with
endpoints that will never need it, at the price of a breaking change plus
unguarded spec drift (`api-check` compares paths, not schemas, so a forgotten
`openapi.yaml` edit would be permanently invisible).

**The escaping half stays and widens.** `initiativeResource()` — added in audit 1
*precisely* to escape path segments — is bypassed by 8 call sites that
concatenate `"/initiatives/" + iniID` raw, and `resolveInitiative` returns
`args[0]` unvalidated. Route every call site through `initiativeResource`, and
replace `"?type=" + typ + "&id=" + objID` with `url.Values{}.Encode()`.

*Scope check performed:* I probed `idea tasks '../../health'` and
`idea tasks 'x/../../../openapi.yaml'` against a live daemon — both return 404,
**no traversal**. Review 1 framed this as a path-traversal risk; it is not. It is
a correctness bug for ids containing URL-special characters, and an instance of
the propagation failure this plan is about. Fixing it is cheap and right; the
security framing is not claimed.

---

## Track D — Documentation truth and drift guards

### D1. `docs/api.md` is ten endpoints behind (P1, finding g — the gate failure)

Verified: `make api-check` and `make docs-check` are **green right now** while
`routes.go` registers 81 `/api/` routes and `docs/api.md` says "71 routes".

**Design [changed after review]:** not a diff of two hand-maintained things, and
not full generation either — the Purpose column is real editorial prose and the
13 sections are an editorial grouping; moving 81 prose lines into `routes.go`
would trade this drift for a worse one. Mirror the existing `docs`/`docs-check`
pair instead, reusing `parseRoutes` (already written) behind both:

- `make api-docs` — inserts missing rows with a `TODO:` purpose into the section
  matching the path prefix, and deletes stale ones.
- `make api-docs-check` — fails on any difference. Added to `.PHONY` and to CI.

That makes the fix "one command plus prose", which is why `make docs` has held
for the CLI table.

Parsing caveats, each a silent false positive if missed: all 13 endpoint tables
share the byte-identical header `| Method | Path | Purpose |` (the three
non-endpoint tables differ, so header-keyed scoping is safe); doc paths omit the
`/api/v1` prefix; five rows carry query strings; one row contains an escaped
`\|` inside a cell, so split-and-index field 2 rather than asserting three
fields; wildcard names are normalized so D1 does not collide with C5.

**Honest scope:** this proves method+path *row existence*. A row with the right
path and a wrong description stays invisible — which is what D2 and D4 are.
Calling it "routes ↔ prose" oversells it. The "71 routes" sentence is derived or
deleted, since it is a second hand-maintained number that would go stale exactly
as it just did.

### D2. Stale claims in reference docs (P1, findings h, i, j)

| File | Claim | Truth |
|---|---|---|
| `architecture.md:73,90-91` | "No source mutation in M1: only `.idea-harness/` is written" | `apply` writes patches into the workspace and can commit |
| `architecture.md:25` | "REST + SSE (OpenAPI planned)" | shipped: 81 operations, served, CI-checked |
| `security.md:13` | token "required on every `/api/v1` request" | `GET /api/v1/health` is exempt (`server.go:83-86`) |
| `security.md:59-62` | sandbox, capability broker, executor isolation, apply journal "deferred to M3" | all shipped in v0.1.0 |
| `cli.md:70-71` | `next` "takes no flags" | takes `--until-human`; its own table says so |
| `configuration.md:74` | `executor.image` default `—` | `alpine:3.20`, as `agents.md:86` correctly says |

`cli.md`'s contradicting paragraph and its unknown-flag list are **deleted**, not
corrected — after A5 the generated table carries the truth. The replacement
sentence ("every command rejects unknown flags") is only shippable because A1
made the guard per-subcommand; under v1's union spec it would have been false
for `executors setup`, `new` and `model add`.

### D3. `verify --fail`/`--waive` documented nowhere (P1, finding k)

**[v1 deferred the guard; that deferral is withdrawn.]** v1 cited "request
bodies are anonymous inline structs, so there is nothing to compare against".
Backwards: the anonymity is local to the handler, and the JSON tags are uniform —
**20 of 20** body-decoding handlers use the identical
`var body struct{…\`json:"…"\`}` + `json.NewDecoder(r.Body).Decode(&body)` shape.
An AST pass over the files `parseRoutes` already parses collects the tag set per
handler and diffs it against the operation's
`requestBody…schema.properties`. **Field names only, not types** — cheap, and it
catches exactly this defect: the spec documents `requirement`/`pass`/`note`
while the handler also decodes `fail` and `waived`.

Implemented this round as part of `api-check`. Deferring it would have left the
next flag added to a request body reproducing this finding.

Content fixes: the `verify` summary (which regenerates the `cli.md` row and
changes `idea help` — see B11), README, quickstart, `api.md`, and the
`verifyInitiative` schema.

### D4. `docs/api.md`'s 409 account (P2, finding y)

Names one source. **Generated, not hand-written**: v1 said "~14 sites", review
said 16, the actual count in `handlers.go` today is **15** — a hand-counted list
that was stale before it was written, which is the failure mode D1 exists to
prevent, reintroduced two sections later. Either derive the list from the AST
alongside D1/D3 or drop the enumeration and describe the causes categorically.

### D5. `security-model.md` orphaned and inconsistent (P2, finding x)

Linked only from plan files; calls "trust tiers" what `security.md` calls "trust
levels". Link it from README and `security.md`; settle on **trust tier** (the
term the code uses). Add the control-plane deny — the largest security control
in v0.1.0, whose case-sensitivity bypass reached a draft release — to
`security.md`'s threat table, where it is absent.

### D6. P3 corrections

CHANGELOG "70 operations" → 81; README's duplicate `api.md` link; the hosting
doc's spec size (dropped rather than corrected, so it cannot go stale again);
`cli.md:135` "`idea new` returns an operation id" → `initiative_id`; README's
provider list omits DeepSeek, a built-in preset; `agents.md:44-46`'s `--smoke`
paragraph ends in a colon with no example; `executors setup --smoke` missing
from `cli.md` and README.

---

## Track E — Release, licensing, repo metadata

### E1. License (P1, finding d) — **done** (`088d7ec`)

Apache-2.0, chosen by the owner when asked. `LICENSE` at root, README section,
and added to `.goreleaser.yaml`'s archive `files:` — necessary because
goreleaser v2 *replaces* rather than merges the default file list, so the prior
config genuinely shipped no license. Verified with `goreleaser check`.

### E2. Repository metadata (P2, finding y) — **done**

Description and 10 topics applied with the owner's approval. `homepageUrl` left
empty deliberately: nothing is published to point at yet.

### E3. Untested shipped architectures (P2, finding z)

**[v1's reasoning was factually wrong.]** v1 argued for documenting rather than
testing, on the grounds of "pure Go, `CGO_ENABLED=0`, zero arch-specific source
files". The first-party claims hold — exactly one `//go:build` file, zero
`runtime.GOARCH` uses. **But the storage layer does not.** `modernc.org/sqlite`
vendors a machine-translated SQLite *per GOOS/GOARCH*, and `modernc.org/libc`
carries 194 arch-suffixed files. Measured directly:

```
darwin/arm64: 210 Go files from modernc libc+sqlite
darwin/amd64: 206 Go files from modernc libc+sqlite
```

The four shipped binaries embed four different persistence layers, and two never
execute a test. "Zero arch-specific source" is true of the repository and false
of the artifact — so v1 would have put a justification into README that the
evidence contradicts.

The cost premise was wrong too: **no emulation is needed.** Hosted x86-64 macOS
runners exist, so darwin/amd64 is one added `runs-on` on the existing `macos`
job.

**Fix:** add the Intel macOS job (native, cheap, closes half the gap). Document
the linux/arm64 gap with the real reason — hosted arm64 Linux runners are GA for
public repos and plan-dependent for private ones — rather than a manufactured
one. Replace README's "built and tested in CI" with an explicit table of what is
built versus what is executed, and drop the "no arch-specific source" line in
favor of "no first-party arch-specific source; the SQLite driver is per-arch
generated code."

---

## Verification

```
make test && make vet && make docs-check && make api-check \
  && make api-docs-check && make check-embed \
  && (cd web && pnpm build && pnpm test)
```

**The web build is in the list [v1 omitted it]** — `make test` is `go test ./...`
only, and CI's `web` job runs `pnpm build` *and* `pnpm test`.

Then: a re-run of the audit journey against a binary built from this branch, and
direct re-probing of every acceptance test against a real daemon. The audit
findings were established by running the product; they get closed the same way.

**Coverage of that re-probing, stated honestly.** Items with a mechanical
acceptance test: A4, A5, B1–B11, C1–C6, D1, D3, E1. Items that are prose edits
with **no mechanical check** — a subtly wrong correction ships green — are
**D2, D4, D5, D6** and the README half of E3. These get a reviewer pass against
the code they describe, and D4 is generated rather than hand-written precisely
because v1's hand-count was already stale.

## Out of scope, recorded

- **No SDK.** Scored 65/100 as a deliberate, documented gap. Generating one from
  the spec is a milestone, not a fix.
- **Public obtainability.** The repo is private; the audit measured the
  access-granted population. Going public is a launch decision. E1 and E2 were
  its prerequisites.
- **Response-schema checking.** D3 adds request-body field names. Response
  shapes stay unguarded — which is why C6's envelope change was dropped rather
  than shipped unguarded.
- **The `MAGIC_PATH_MAX_COMMANDS` overage** (11 vs 8) was partly audit
  instrumentation; README's documented path is 8 commands, at target.
