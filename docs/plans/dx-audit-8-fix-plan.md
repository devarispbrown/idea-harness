# DX Audit 8 — fix plan

Audit 8 returned **FAIL at 60/100** with both hard gates passing for the first time.
This plan closes the four P0s and twelve P1s.

## What "passing" actually requires

The verdict is driven by **gates**, not by the Overall DX number
(`references/standards.md:66-74`), and there are **four** possible verdicts:

- **PASS** — no P0/P1 *gate* failures; required hard gates pass.
- **PASS WITH DEBT** — no hard gate failure, but P2/P3 backlog remains.
- **FAIL** — one or more P0/P1 gates fail.
- **UNVERIFIED** — evidence insufficient to prove critical gates.

Both hard gates already pass. Three named gates fire:
`UNDOCUMENTED_BREAKING_API` (P0), `STALE_PUBLIC_REFERENCE` (P1), `UNEXPLAINED_ERROR` (P1).

**The reading this plan relies on, stated plainly.** "P0/P1 gate failures" means the named
gates in `standards.md:161-169`, not every P0 *finding*. Under that reading Track A reaches
PASS. Under the stricter reading — an open P0 finding is itself disqualifying — Track A
reaches **PASS WITH DEBT**, because Tracks B–E leave P2/P3 backlog by design. Either way
Track A is the work that moves the verdict off FAIL; the plan does not depend on which
reading an auditor takes, and I will report whichever the evidence supports rather than
claiming the stronger one.

**What this plan does not claim.** `WORLD_CLASS_OVERALL_DX` = 85 with every area ≥ 75 is a
different bar. Best case here is roughly **72**: recomputed from the report's weight table as
(20·78 + 15·70 + 10·55 + 10·65 + 12·70 + 10·75 + 8·85 + 8·72 + 7·75)/100 = 71.8.
Saying otherwise would be arithmetic that does not survive the next audit.

## The skill version this was audited against

Audit 8 and this plan cite the installed plugin, **2.3.2**
(`~/.claude/plugins/cache/devex-skills/developer-docs-skills/2.3.2/`). The working source at
`~/Code/projects/devex/devex-skills/` is **2.9.4**. Diffing the gate constants, 2.9.4 adds
exactly one:

```
| `UNVERIFIABLE_CI_PARITY` | P1 | the documented local check command does not appear in CI
  configuration, so a green local run does not predict a green CI run |
```

Everything else in the gate list is unchanged, and the `## Verdict vocabulary` block is
byte-identical between the two, so audit 8's verdict machinery stands.

**That gate already fires narrowly at HEAD, and Track A can widen it.**

Measured against `.github/workflows/ci.yml`: `make test` is `go test ./...` and `make vet` is
`go vet ./...` — byte-identical to what CI runs at `:48`, `:196`, `:240` and `:47`, `:181`,
`:237`, so parity is real for those despite the `make` wrapper not appearing literally. The
nine guards, `make embed-web`, `scripts/smoke.sh` and the pnpm steps all appear verbatim.

**`make build` does not.** `Makefile:17-19` is `embed-web` + `go build -trimpath -ldflags … -o
bin/idea ./cmd/idea` + `restore-embed`; CI runs a bare `go build ./...`. Different scope,
different flags, and the make target is invoked nowhere in CI — so a green `make build` does
not predict CI.

**Consequence for Track A, which is the load-bearing part.** Track A adds three new guards
(A4's `changelog-history`, A5's `decodeStrict` census, A6b's model-id census) and widens
`docs-lint` (A6a). Documenting any of them in `CONTRIBUTING.md:39-40` without a matching step
in `compile-no-node` fires this gate — turning "Track A reaches PASS" into a fresh FAIL caused
by the fix round itself. **Every new guard ships with all three of: its `make` target, its step
in `compile-no-node`, and — for A4's rule — `fetch-depth: 0` on that job.** The `fetch-depth`
change is therefore not optional polish; without it A4's guard is a documented check that
cannot predict CI, which is the gate's exact wording.

## What adversarial review changed

Two reviewers took the first draft apart. Four items were rejected outright and eleven
revised. Recording it here rather than quietly editing, because the rejections are the
useful part.

| Item | Planned | What survived |
|---|---|---|
| **D4** | "CONTRIBUTING names 2 of the 9 CI-gating targets" | **Rejected — the premise is false.** `CONTRIBUTING.md:34-42` lists all nine plus smoke and the web job. My `grep -oE "make [a-z-]+"` captured only the first token of a multi-target line. `af19253` — this session's own work — closed audit 7's gap. The audit report has been corrected and Local dev rescored 78 → 85 |
| **A6b** | Remove the dead Anthropic catalog entries | **Rejected — breaks three passing tests** on the strength of a claim I could not prove. Rewritten as additive-only |
| **A4 guard** | Released sections must equal their content at their own tag | **Rejected — unmeetable.** `[0.1.0]` at HEAD already differs from `v0.1.0` by a ratified correction. Baseline is now the newest tag |
| **C2** | Config parse failure → 500 | **Rejected — contradicts `internal/daemon/errors.go:20-27`**, which reserves 500 for "the daemon's problem". A malformed user-authored file is not that. Now a typed 4xx |
| A1 | Acceptance test runs `ensureDaemon` under `go test` | Revised — that forks the **test binary** (`harness_test.go:17-22`), so it can never see a bind failure and may fork recursively. Needs an exec seam |
| A2 | Add a JOIN for initiative titles | Revised — must be a **LEFT** JOIN, and the test must assert the error *text*: `changeset_test.go:81` only checks `err != nil` and would stay green on an INNER-JOIN regression |
| B6 | "One-character fix" | Revised — changing `category` breaks `handlers.go:2045` and `execute_test.go:134` |
| B4 | "Commands that print a failure exit non-zero" | Revised — undefined as written, and breaks `smoke.sh` in four places under `set -euo pipefail` |

---

## Track A — the three gates and the four P0s

### A1. `idea init` against a held port: ~51 minutes of silence → sub-second

`healthy()` (`internal/cli/client.go:203-213`) builds its client with `newClient()`, whose
timeout is **60 s** (`:85`). `ensureDaemon` (`:255-282`) calls it once at `:256`, then 50 more
times at `:275`. Against a socket that accepts and never answers: 51 × 60 s ≈ 51 minutes with
**no output at all**. The 786 s observed was a SIGKILL partway through, ≈13 probes — the two
numbers describe different runs and the plan should not conflate them.

The 2 s client at `:246` belongs to `daemonBuild`, a different function.

**The `Setsid` risk is answered: part 2 is safe.** `internal/cli/detach.go` (`//go:build unix`,
no Windows sibling) sets only `Setsid: true`. The child remains a direct child, so `cmd.Wait()`
reaps it normally on darwin and linux. There is no re-fork: `cli.go:335` calls `daemon.Run`
inline, `server.go:41` does `net.Listen` inline, and `main.go:13` prints to stderr and exits 1
— and `ensureDaemon` already assigns `daemon.log` to `cmd.Stderr`, so the bind error does land
in the log. `healthy()`'s three other callers (`cli.go:312`, `:378`, `:690`) are all liveness
probes; none relies on patience.

**Fix, three parts.**

1. Give `healthy()` a short timeout. It is a liveness probe, not a data call.
2. Run `cmd.Wait()` in a goroutine and select on it in the poll loop, so a daemon that dies on
   `bind: address already in use` ends the loop immediately.
3. On giving up, read the tail of `daemon.log` and print it.

**Required seam.** `ensureDaemon` spawns `os.Executable()`, which under `go test` is the *test
binary* — `internal/cli/harness_test.go:17-22` documents exactly this. A test calling
`ensureDaemon` directly would fork a copy of the test binary, which binds nothing, writes no
bind error, exits 0, and forks again. Introduce `var daemonExecutable = os.Executable` (the
only call site is `client.go:259`) or use the `TestMain` + `GO_WANT_HELPER_PROCESS` pattern.
Without a seam this item is not testable and must not be marked done.

**Acceptance.** With the seam, a test points `daemonExecutable` at a helper that exits non-zero
after writing a bind error to the log, and asserts `ensureDaemon` returns an error naming the
bind failure in under 10 s.

**Mutation — three, one per mechanism.** The first draft named two and mis-assigned one: the
bind text comes from part 3, not from `cmd.Wait()`, which yields only an exit status.
1. Restore the 60 s timeout → must fail on elapsed time.
2. Drop the `cmd.Wait()` watch → must fail on elapsed time. **State the probe timeout**, so the
   margin against the 10 s bound is real rather than coin-flip.
3. Delete the log-tail read → must fail on message content.

**Pre-existing hazard this test will surface, not caused by A1.** `cli.go:333-334` runs
`writePID()` before `daemon.Run` with `defer removePID()`, so a daemon that fails to bind
deletes the *live* daemon's pidfile. File it; do not fix it here.

### A2. `idea next --until-human` prescribes a command that always fails

`idea changes` resolves one initiative (`internal/cli/change.go:121`) and prints
`cs.HumanID` only (`:142`). `apply` and `change` resolve **project-wide** —
`GetChangeSetByRef` queries `WHERE human_id = ?` with no initiative scope
(`internal/store/changeset.go:274`). `next` prescribes the human id
(`internal/workflow/next.go:37`, rendered at `internal/cli/next.go:250`).

The message reaches the user verbatim: the ambiguity error is a plain `fmt.Errorf`, so
`internal/daemon/errors.go:38-67` falls through to `writeError` with the full text.

**Fix.** The ambiguity error names each candidate: full id, human id, initiative title. A
`LEFT JOIN initiatives i ON i.id = cs.initiative_id` — same package, same DB, one line.

Deliberately *not* resolving to the newest initiative: silently applying a change set from the
wrong initiative is worse than refusing.

**The vacuity trap, closed.** It must be a **LEFT** join with a fallback label.
`internal/store/changeset_test.go:10-21` creates change sets with **no initiative row**, and
the existing `TestGetChangeSetByRefAmbiguousFailsClosed` (`:81`) asserts only `err == nil`. An
INNER join would yield zero rows → `notFound` instead of the ambiguity error, and that test
would **still pass** while the behaviour silently regressed.

**Acceptance.** A new test creates two real initiative rows and asserts the error *text*
contains both full ids and both titles — not merely that it is non-nil.
**Mutation.** Reverting to the current message must fail. Switching LEFT→INNER must fail.

### A3. A 413 on `POST /initiatives` leaves an unrecoverable `running` row

Pre-validation loop `runtime.go:470-478` checks emptiness and credentials under a comment at
`:458-459` promising "a rejected doc never leaves a half-created row". The size cap is not in
it — `handlers.go:1688` inside `ingestTextDoc`, called at `runtime.go:485`, after
`CreateInitiative` at `:481`. `DeleteInitiative` does not exist anywhere.

`maxDocFileBytes` (`handlers.go:1496`) is reachable: both files are `package daemon`, and
`runtime.go:465` already uses its sibling `maxContextDocsPerInitiative`.

**Fix, two parts.**

1. Move the size check into the pre-validation loop, making the existing comment true.
2. On any failure between `CreateInitiative` and `launchOperation`, call
   `FailInitiative` (`internal/store/repo.go:97-110`), which sets `status` **and**
   `failure_reason` in one event-sourced transaction. `CompleteInitiative`'s guard
   (`repo.go:287`) constrains completion, not failing. `idea retry` requires `failed`
   (`runtime.go:932`), and `runtime.go:1242` confirms `failed` is a first-class recoverable
   state — so this turns a strand into a recovery route.

**Companion edit, required.** `internal/daemon/claim_invariant_test.go:32-33` says
`StartInitiativeWithDocs` "does not claim: the initiative is created running, so there is no
prior state to take". After part 2 the function writes a terminal status on a post-create
error. The `releases` field is not machine-checked, so nothing fails — which is exactly the
"green over a live contradiction" defect this plan files as D2. Update the prose in the same
commit or ship the class of defect we are fixing.

**Acceptance.** `internal/daemon/docs_test.go:333-358` is the template. Test 1: an oversized
doc leaves **zero** initiative rows — fails before the fix, passes after, confirmed
non-vacuous. Test 2: sabotage at the SQL level via the exported handle
(`db.SQL().Exec("DROP TABLE operations")`) — `pr.db` is a concrete `*store.DB` with no
interface seam, so "injected failure" overstates what exists.
**Mutation.** Restoring the ordering must fail test 1; removing the `FailInitiative` call must
fail test 2.

**Residual to name.** `StartInitiativeWithDocs` returns `"", "", err`, so the failed
initiative's id never reaches the caller; recovery is via `idea list` / `/attention`.

### A4. `e53bf2f` promoted released v0.1.0 history into `[Unreleased]`

Verified: at `v0.3.0` the entry *"The Go module path is now …"* sits under `## [0.1.0]`; at HEAD
under `## [Unreleased]`. The removed and added line sets are identical — nothing deleted. The
misfiled block at HEAD is `CHANGELOG.md:110-138`. `.goreleaser.yaml:40` ships the changelog in
every archive.

**Fix.** Move `CHANGELOG.md:110-138` back under `## [0.1.0]`, keeping the P1-6 entry in
`[Unreleased]`.

**Guard — baseline corrected.** The first draft compared each released section to its **own**
tag. That is unmeetable: the `[0.1.0]` section was legitimately edited between `v0.1.0` and
`v0.2.0`, correcting "all 70 operations" to "every operation" — and Track C4 says the true
count is 81, so reverting it would re-introduce a false claim to satisfy a checker. Measured
md5 of the section: `v0.1.0` `62e1ca92…`, `v0.2.0` and `v0.3.0` `4c86fa3f…`, HEAD `4a39a2e8…`.

The rule is therefore: **for every released section, its content at HEAD must equal its content
at the newest tag.** That passes after the fix, still catches the whole class going forward,
and still satisfies the mutation.

**Two implementation facts.** (a) No rule in `internal/docsgen` shells out — all nine are pure
file/AST readers, so this would be the first to use `os/exec`; it is not "the same shape" as
anything that exists. (b) The guards run in `compile-no-node`, whose `actions/checkout@v5`
(`ci.yml:27`) has **no `fetch-depth`** → depth 1, no tags. Either add `fetch-depth: 0` there or
the rule is decorative in CI. It must **skip with a stated reason** when tags are absent, never
pass silently — and since a `docsgen` rule is a `main` that exits non-zero rather than a Go
test, say which shape the skip takes.

**Acceptance.** The guard fails on HEAD before the fix and passes after.
**Mutation.** Re-applying `e53bf2f`'s move must fail; deleting one line from any released
section must fail.

### A5. `UNDOCUMENTED_BREAKING_API` — the third strict decoder

Exactly three `decodeStrict` call sites: `handlers.go:463` (`handleUpdateProviders`), `:2143`
(`handleReviewChangeSet`), `:2336` (`handleUpdateExecutors`). `CHANGELOG.md:269` names two.
`openapi.yaml`'s `reviewChangeSet` 400 is byte-identical to v0.3.0 — the only change since is
`accept_findings` on the *request body*. The break is genuinely new: `decodeStrict` did not
exist at `v0.3.0`; the third site landed in `5593472`, i.e. in `[Unreleased]`.

The stale sentence is at **`decode.go:13`**, not `:8` (line 8 is the import block's `)`). That
citation, and A6a's, were copied from the audit report without re-checking — the same
verify-your-own-citations failure the report itself documents.

**Fix.** CHANGELOG breaking row names all three with migration guidance; `openapi.yaml`'s
`reviewChangeSet` 400 documents the closed body; `decode.go:13` describes the current call set.

**Guard — AST, and folded into an existing rule.** A textual census returns **six** hits, only
three of which are calls (`:2138` and `decode.go:10` are comments, `:31` the definition), so it
must match `*ast.CallExpr` with an `*ast.Ident` named `decodeStrict` — the shape
`enumcheck.go:119` already uses.

It belongs in `internal/docsgen/bodycheck.go`, not a tenth rule. That file's `decodesBody`
(`:46-68`) matches only `json.NewDecoder(…).Decode(…)`, so it returns **false** for all three
strict handlers — a latent fail-open — and its header comment at `:17-19` ("every
body-decoding handler … uses `json.NewDecoder`") is **already false at HEAD**. Teaching
`decodesBody` about `decodeStrict` gives the census for free and repairs a stale guard claim.
A separate guard leaves the false comment standing.

**Acceptance.** A fourth call site fails the guard. Parameterise over a directory
(`bodycheck.go:73`'s `handlerBodyFields(dir string)` shape) so a fixture can exist.

### A6a. `STALE_PUBLIC_REFERENCE` — a pointer to a file that has never existed

`RELEASING.md:29` (not `:30`) sends a maintainer to `docs/versioning.md`;
`git log --all -- docs/versioning.md` is empty. `docs-lint` globs `README.md` + `docs/*.md`
(`internal/docsgen/docslint.go:41,45`), so `RELEASING.md` is uncovered — the only `docsgen`
references to it are strings inside `versioncheck.go`'s error messages.

**Fix.** Inline the classification rules into `RELEASING.md` and delete the pointer.

**Guard — scoped, not widened.** The first draft said "extend `docs-lint` to every tracked
markdown file" with a "check first" caveat. That question is already answered, with its reason,
in the very function the draft proposed to change — `docslint.go:37-39`:

> *"docs/plans/ is deliberately excluded: those are historical records of what was true at the
> time, and linting them would force the rewriting of history to satisfy a checker."*

And it would indeed fail: `dx-audit-5-report.md:51,92` and `dx-audit-3-fix-plan.md:241` quote
commands and ids that were true then and are not now, by design.

So: **add `RELEASING.md` and `CONTRIBUTING.md` to the named set in `lintedDocs`**, preserving
the `docs/plans/` exclusion and its comment. Add a relative-link-existence rule over that set.

### A6b. The model-id contradiction — rewritten as additive-only

**What the first draft got wrong.** It proposed removing the dead Anthropic entries. That
breaks three currently-passing tests — `catalog_warning_test.go:21` (`claude-3-7-sonnet-latest`
must **not** warn, commented "the correct id"), `:37` (the warning must *name* it), and
`problems_test.go:94-102` (`wantWarns: 0`) — on the strength of a claim I had inherited rather
than proved.

**What I then measured, against the live APIs.**

| Id | `/v1/models` |
|---|---|
| `openai/gpt-5.6-luna` | **LIVE** (138 models listed) |
| `openai/gpt-4o`, `gpt-4o-mini`, `gpt-4.1`, `gpt-4.1-mini`, `o3-mini` | all **LIVE** |
| `anthropic/claude-sonnet-4-5-20250929` | **LIVE** |
| `claude-sonnet-4-5`, `claude-3-7-sonnet-latest`, `claude-3-5-haiku-latest`, `claude-opus-4-1` | **absent** from the listing |

**And what I could not prove.** `/v1/models` lists dated snapshots; it does not follow that an
unlisted alias fails *inference*. I tried to settle it with a 1-token request per id and every
one returned the same `400 Your credit balance is too low` — billing is checked before model
validation, so the test is inconclusive. **I cannot prove the Anthropic aliases 404.**

**Therefore the fix is additive only.**

1. **Append** `gpt-5.6-luna` to the openai catalog — proven live, and it closes the actual gate
   defect (the README's front-page command warns). **Append, never prepend**: `DefaultModelFor`
   returns `models[0]` (`catalog.go:22`), so prepending would silently change the openai default
   from `gpt-4o`, and `registry_test.go:67` only asserts non-empty and would not catch it.
2. **Add** `claude-sonnet-4-5-20250929` to the anthropic catalog — proven live.
3. **Move the docs** from `anthropic/claude-sonnet-4-5` (11 sites) to the dated id, which is
   proven live and already what `openapi.yaml:1050` uses.
4. **Remove nothing.** Record the contradiction — audit 7's L3 versus
   `catalog_warning_test.go:21` — as an open question in `deferred.md` with the evidence above,
   and resolve it when a funded key can distinguish a 404 from a 400.

**Guard — scoped, with the tokenisation stated.** "Every `provider/model` string in tracked
docs resolves against the catalog" is unshippable as written, for three measured reasons:

- It fails on the audit archive (`dx-audit-4-report.md:75` deliberately quotes the *broken* id),
  which bites the moment A6a widens any glob — **A6a → A6b is a real dependency the first draft
  did not state**.
- It fails on `CHANGELOG.md:52-53`, which quotes ids precisely because they are being removed.
- Two live extraction false positives: `openai/v1` (from `https://api.openai.com/v1`) and
  `anthropic/claude-opus-5` matched *inside* `openrouter/anthropic/claude-opus-5`
  (`docs/configuration.md:191,196,197`), because `/` is a non-word character so `\b` does not
  help.

So: scope to the named document set, exclude `docs/plans/` and `CHANGELOG.md` explicitly with
the reason, tokenise on the **first** slash of a token that starts the match, and exclude URL
contexts. Skip `openrouter` and `ollama` for the reason `problems.go:173-176` already gives.

**Acceptance.** The guard fails on HEAD (`openai/gpt-5.6-luna`, 8 sites in scope, absent from
the catalog) and passes after. `go test ./internal/config/...` stays green — the measure of
whether this stayed additive.

### A7. `UNEXPLAINED_ERROR`

Closed by A1 and A2, and verified by re-running the two scenarios rather than by assertion.
`dx-audit-8-report.md:36` attributes the gate to exactly those two. Conditional on A1's seam.

---

## Track B — error and recovery (area 40/100, weight 12: the largest score lever)

| # | Finding | Fix |
|---|---|---|
| B1 | `cmdRetry` (`change.go:532-568`) has no wait loop, so `✓ retry started` precedes a failure 0.51 s later. `handleRetry` (`handlers.go:307`) returns the same `operation_id` key `cmdNew` reads, and retry already holds an `iniID` from `:536`, so `waitForOperation` generalises | Add the wait. **Lockstep work the draft omitted**: `--no-wait` needs the flag spec and usage at `cli.go:126`, `make docs` for the generated block at `docs/cli.md:13-68`, and `-update-golden` for `TestHelpOutputIsStable` |
| B2 | `cli.go:849-851` renders a store read *failure* as an absence (`no initiative yet`), because `resolveInitiative` returns both through one `error` | Distinguish the two and set `graphOK = false` on a read failure. **Scope corrected**: "a failed check exits non-zero" is already implemented at `cli.go:875-889`; claiming it as a deliverable would invite re-implementing working code and a mutation test against a mechanism this round did not add |
| B3 | `doctor` says `not running` while `curl` gets 200. `healthy()` → `newClient()` fails on the missing token file *before* any HTTP (`client.go:76-79`), and `/health` is the one unauthenticated route | Separate "cannot ask" from "not running". A1's timeout does not touch this path |
| B4 | 18 commands exit 0 over a reported or concealed failure; `cliInitiative` (`state_cmds.go:12-16`) is `{id,title,status}` so `--json` drops `failure_reason` | See the enumeration rule below — this is the highest-risk item in the plan |
| B5 | A hung provider is invisible ~15 min: `openai.go:38` is a 5-minute client, `engine.go:312` is 3 attempts, and `isTransientModelError` (`:265-273`) matches `"timeout"`, so the retry really does run three times | Print elapsed time while waiting; name the timeout in the failure |
| B6 | `execute.go:794` builds `"set " + category + "_cmd"`; callers pass `"build"` and `"tests"` (`:732-733`). `build_cmd` is right, `tests_cmd` is not — the field is `test_cmd` (`config.go:109`) | **Not a one-character fix.** `category` is also the finding's `Category` (`:787`), and changing it breaks `handlers.go:2045` (the `/checks` endpoint stops counting test findings) and `execute_test.go:134`. Pass the config field name separately, leaving `Category` at `"tests"` |
| B7 | `judgment.go:65,68` prescribe `idea reopen-phase`, which is not a CLI verb; the endpoint exists at `routes.go:34` | **Five-file change, not "add the verb"**: the table row in `cli.go`, `make docs` for `docs/cli.md`, `-update-golden`, and passing the three table-walking tests in `command_table_test.go` that *invoke* every row. The phase argument must be validated against `handlers.go:333-338`'s three values or the CLI sends a body the daemon 400s |
| B8 | `runtime.go:1296` renders `no plan to approve (status=approved)` where `approved` is the **PRD's** status | Qualify which status is reported, and name a route. **The "four vs three siblings" split does not reproduce** — there are ~30 `preconditionf` sites and at least six name a route (`:534`, `:652`, `:1017`, `:1106`, `:1247`, `:1256`). Enumerate the exact set by `file:line` before writing the test, or it pins a set nobody agreed to |

### B4's rule, stated as an enumeration

The draft said "the commands that *print* a failure exit non-zero". That is not a rule an
implementer can apply, and the natural reading breaks `scripts/smoke.sh` — which runs under
`set -euo pipefail` (`:2`) — in four places:

- `:357` `VOUT=$(idea next)` after `idea verify --fail`, then greps for `failed after this
  initiative completed`. Bare command substitution: smoke aborts with no message.
- `:322` and `:466` `idea reviews | grep -q 'Review findings'`, run *because* the review is
  blocked. Under `pipefail` a non-zero `reviews` fails the pipeline though the grep matched.
- `:460` `PR_OUT=$(idea next)` asserting `plan review raised blocking findings`.

`internal/cli/next.go:171-173` states the current contract explicitly: *"Exits 0: waiting on a
person is the loop working, not failing."*

**So the rule is:** an enumerated list of command × initiative-state pairs, with the exclusion
written down — **a `needs_human_judgment` state is not a failure**. That exclusion is what keeps
`idea next` and `idea reviews` at 0. The `failure_reason` half is a three-line CLI struct change
(the wire and `openapi.yaml` already carry it), but `docs/cli.md:94` states the shape literally
as `[{"id","title","status"}]` and is outside the generated block, so it is a hand edit in the
same commit.

**Acceptance for the track.** Re-run the three failing TTR scenarios against
`TTR_TARGET_MIN` = 5:00, and re-run the 28-command sweep to verify B4's count — measured, not
reasoned.

---

## Track C — API and contract

| # | Finding | Fix |
|---|---|---|
| C1 | Round-trip is broken on both PUTs, but **not with the error the draft quoted**. `GET /providers` → `PUT` verbatim gives `json: unknown field "demo"` (reproduced), not `"name"` — `demo` comes first and `DisallowUnknownFields` stops at the first unknown key. And `providers` is a `[]providerSummary` array against a `map`, which `decode.go:35-40` maps to `invalid body` | Quote the *observed* error for each PUT. For `/executors`, say what is true: its GET (`handlers.go:2292-2319`) is a **top-level array catalog of all four executor kinds**, not a config projection — there is no writable-subset relationship to document |
| C2 | A YAML error in `harness.yaml` answers 404 with an **absolute path** from `config.go:253`. **Two** handlers answer 500, not one (`handlers.go:479`, `:2352`). "34 routes" is an undercount — 75 of 81 routes are project-scoped | **500 rejected.** `errors.go:20-27` reserves 500 for "the daemon's problem … keeps absolute state paths out of the response"; a malformed user-authored file is not that, and 500 tells clients to retry what can never succeed. And the leaked path is rooted at the **user's project dir**, so "state-relative" is meaningless. Instead: a typed `*ConfigParseError` carrying `(file, line, message)`, mapped by `writeErr` to **422**, path rendered project-relative. Re-derive the route count before implementing |
| C3 | `openapi.yaml:2559` — `required: [status]` indented 18 spaces, inside `properties:`. Independently confirmed as the **only** non-object property schema in the spec | Fix the indentation; extend `api-check` to walk every `properties` node. **Must land before C6**: C6 adds `Put`/`Post` to `response_schema_test.go:64-66`, at which point `Properties map[string]openAPISchema` fails to decode `['status']` and the test errors out |
| C4 | "84 times" (`openapi.yaml:2857`) against an actual **81**; 403 and 405 declared on zero; no `default` | Add `default: $ref Error` and correct the count. **Two corrections**: the spec's sentence is about **404 and 405, not 403** — 403 comes from `hostOriginGuard` (`server.go:138,175-177`), a different mechanism; and adding `default` everywhere makes a naive C6 guard vacuous, so **sequence C6 before C4** or require the literal status key |
| C5 | `openapi.yaml:980`'s worked example is `requirement:req-1`, a form that returns a wrong answer; `handleImpactCounts` (`:1205-1243`) does no resolution | Correct the example; document that codes do not resolve. **Downgraded from the dive's P0**: `advance.go:251,289` and `JudgmentCard.tsx:73` both resolve first |
| C6 | 10 of 36 non-GET operations miss `500`; **2** miss `404`, not 1 — and `POST /api/v1/projects` has no path parameter so cannot 404 | Declare them, **stating the `POST /projects` exemption** or the guard encodes a wrong expectation. Build the guard on `internal/docsgen/conflictcheck.go`'s AST model, which already derives "can answer 409" and requires the declaration. **Drop the response-schema-test extension** — provoking a 500 on ten mutating routes is an E-sized fixture job, and that test validates 200 payloads, not error codes |

---

## Track D — documentation and ledger

| # | Finding | Fix |
|---|---|---|
| D1 | `--accept-findings` is in both demo guides, `docs/cli.md:48` and `smoke.sh:610,615,620` — and in **neither** `README.md` nor `docs/quickstart.md` | Add to both. `docs-lint` validates verbs and `TYPE-n` refs only (`docslint.go:16-18`), so this is safe and disturbs no `absent:` predicate |
| D2 | Ledger L1 is green over a live contradiction. The false sentence is `runtime.go:1689-1690`; the correction is `:1680-1683`. L1's predicate (`dx-audit-7-doc-defects.md:62`) contains a **two-character literal** `\n`; `normalizeWrapping` (`ledgercheck.go:466-468`) collapses only real newlines, so the `present:` clause is permanently false and the row passes as fixed. The source also wraps after *"cannot be"*, so even unescaping would not match | Delete the sentence; rewrite the predicate. **No collateral**: L1 is the only predicate containing a literal `\n`, and rows A1/A2 pin `:1106`, untouched. **Add the class guard**: `evalPredicate` (`:164-211`) should reject literals containing `\n`/`\t` escapes — an unmatchable `present:` is indistinguishable from a fixed defect, which is the blind-guard failure `ledgercheck.go:477-483` says the file exists to prevent |
| D3 | The blockquote sits between the command block (`demo-greenfield.md:118-121`) and its output (`:131-135`) | Move the note after the output block. **Line numbers corrected**: the double blanks are `:129-130` and `demo-existing-project.md:141-142`, not `:131`/`:143`; and there is a **second** double blank at `demo-existing-project.md:94-95` — decide whether it is in scope. **Tripwire**: `dx-audit-6-doc-defects.md:26` pins `absent:docs/demo-greenfield.md:apply started CHG-1 cancel with`, which requires those two output lines to stay adjacent. Moving the blockquote *after* the block preserves it; inserting anything *inside* flips the row red |

**D4 is withdrawn.** See the review table above: the premise was false, and the audit report has
been corrected. A CONTRIBUTING-vs-CI completeness guard is still worth building as **drift
prevention** — the natural sibling to `internal/ciguard` — but it must be motivated that way and
must pass on HEAD.

---

## Track E — deferred, with reasons

| # | Item | Why deferred |
|---|---|---|
| E1 | Pagination, idempotency keys, ETag/If-Match | Three cross-cutting capabilities touching ~32 collection GETs and every mutating route. A design round, not a fix round |
| E2 | Full exit-code taxonomy (~35 failures, one `os.Exit(1)` at `main.go:14`) | Changes every command's contract. **The B4/E2 boundary is now an enumerated command × state list**, not the word "printed" — that ambiguity was the whole risk |
| E3 | Envelope sprawl: 7 list shapes, 3 identifier policies, 12 meanings of `status` | Breaking across the whole surface; belongs with E1 |
| E4 | `DELETE /initiatives` and a delete/archive/reset verb | A3 removes the *recovery* motivation. As a feature it is real and open since audit 7 |
| E5 | Config GET/PUT symmetry | **Reframed**: `/executors`' GET is a capability catalog of all four kinds, not a config projection, so "make the GET PUT-compatible" is not the work — it would be inventing a new resource. Say that, or the next round budgets a symmetry job that is actually a design job |
| E6 | Resolve the `claude-3-7-sonnet-latest` contradiction | A6b measured that it is absent from `/v1/models`, but a billing error masked the inference check. Needs a funded key to distinguish 404 from 400 |

---

## Order

Revised: the draft called A4–A6 "cheapest", which is wrong. A6b is a code change with a
test-breakage radius and a guard whose scope depends on A6a; A5 is a code change plus an AST
guard.

1. **A4 fix, A5, A6a** — genuinely cheap, and A4's fix blocks any release.
2. **A6b** — after A6a, because the guard inherits its scope.
3. **A4 guard** — needs the `fetch-depth` decision in CI.
4. **A1, A2, A3** — the three product P0s. A1 first: it needs the exec seam, which is the
   largest unknown in the plan.
5. **B**, sequencing B4 last in the track and re-running smoke after it.
6. **C3 → C6 → C4** (that order is forced), then the rest of C, then D.
7. Re-run the full audit and score it. The number is whatever it is.

Every fix: mutation-checked tests proving the test is non-vacuous, adversarial review before
merge, merge on green.
