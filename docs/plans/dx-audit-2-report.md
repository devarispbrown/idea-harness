# DX Audit 2 — Report

Second adversarial developer-experience audit, run against the published
v0.1.0 release binary from a clean environment.

- **Repository/revision:** `69e9278` (main, clean); tag `v0.1.0`, non-draft, 5 assets
- **Artifact under test:** `idea-harness_0.1.0_darwin_arm64.tar.gz`, downloaded
  from the release — not a local build
- **Environment:** macOS (darwin/arm64), Go 1.25.5, Node 22; clean state root and
  empty project directory per run
- **Scope:** all 14 journey stages except `deploy`; 11 core + 7 optional steps,
  dry-run confirmed before execution
- **Verdict:** **FAIL** · **Overall DX 70/100** · all nine areas *Observed*

Rendered report: https://claude.ai/code/artifact/ac25f78c-45db-4879-8a46-4ba646f076d7

## Scope qualifier

The repository is private. Unauthenticated requests to both the repo page and
the v0.1.0 asset return 404. All measurements describe a developer **who has
been granted access**. A brand-new public developer cannot obtain the product;
that is recorded as a blocker for public launch and explicitly *not* exercised
as a passing install.

## Hard gates — both pass

| Gate | Result | Measured | Budget |
|---|---|---|---|
| Magic path | **PASS** (Observed) | 4.67s · 11 commands · 0 credentials · 1 context switch | 900s · 8 · 2 · 4 |
| Local development | **PASS** (Observed) | 15s · 3 commands (clone, `make test`, `make vet`) | 600s · 4 |

The command overage is partly instrumentation: the manifest adds `doctor` and
`model list` as verification steps. README's documented quick start is 8 `idea`
commands plus a `mkdir`, which sits exactly at target.

18/18 journey steps passed. `deploy` was not exercised — no production
deployment target exists for a local-first tool.

## Named gates

| Gate | Result | Why |
|---|---|---|
| `STALE_PUBLIC_REFERENCE` | **FAIL** (P1) | Seven verified disagreements between published reference docs and shipped behavior |
| `UNEXPLAINED_ERROR` | **FAIL** (P1) | Expected errors are not merely under-explained — several are never surfaced at all |
| `BROKEN_CANONICAL_INSTALL` | PASS WITH DEBT | Works for access-granted users; public 404s; README install block has no clone line |
| `UNTESTED_SUPPORTED_VERSION` | PASS WITH DEBT | 4 binaries ship, 2 architectures execute in CI |
| `BROKEN_QUICKSTART` | PASS | Reproduced end to end in 4.67s |
| `NON_REPRODUCIBLE_BUILD` | PASS | 15s from a fresh clone |
| `UNDOCUMENTED_BREAKING_API` | PASS | CHANGELOG carries a `Changed — BREAKING` section with per-endpoint migration |
| `UNSAFE_EXAMPLES` | PASS | No literal credentials; env-var indirection throughout |
| `SDK_API_DRIFT` | PASS (vacuous) | No official SDK is released |

## Scores

| Area | Weight | Score | Δ vs audit 1 |
|---|---:|---:|---:|
| Time to first value | 20 | 85 | +1 |
| API | 15 | 68 | −8 |
| SDK | 10 | 65 | +3 |
| CLI / config | 10 | 58 | −28 |
| Errors / recovery | 12 | 64 | −21 |
| Documentation | 10 | 58 | −28 |
| Local dev | 8 | 94 | +6 |
| Testing / quality | 8 | 70 | −12 |
| Release / compatibility | 7 | 62 | −16 |
| **Overall DX** | 100 | **70** | +10 |

Several areas fell because this pass probed deeper, not because the code
regressed — the earlier numbers were measured against a shallower set of checks.

World-class checklist: 1 of 5 (magic path Observed at ≤ 15 min). Overall DX 70
< 85; six of nine areas < 75; three P0 and ten P1 unresolved.

## Findings

### P0

1. **`GET …/risk` writes on every read.** Append-only by design, so unbounded.
   Measured: 3 GETs → +3 `risk_profiles` rows, +3 events.
2. **`idea revert` reports success on a non-revertible change set.** Exit 0 and
   `✓ revert started` on an already-reverted set, while `apply` correctly exits
   1. `execute` has the same shape.
3. **`idea docs add -` does not work** despite being documented in README,
   `docs/ui.md`, the usage string, and as a literal copy-paste line in
   `docs/quickstart.md`.

### P1

4. **No LICENSE** in the repo, README, or release archive.
5. **`idea verify --pass --note` with no requirement** prints `✓ verified`,
   records nothing, exits 0; `--pass --fail` silently resolves to one.
6. **`idea model add`/`set --role` swallow misspelled flags**, writing a provider
   with no endpoint, exit 0.
7. **`docs/api.md` says 71 routes**; 81 are registered; 10 undocumented; README
   calls it "every endpoint".
8. **`docs/architecture.md`** — "no source mutation in M1" and "OpenAPI planned",
   both false.
9. **`docs/security.md`** — "token on every `/api/v1` request" (health is
   exempt), and a "deferred to M3" list describing shipped features.
10. **`docs/cli.md` prose contradicts its own generated table** about `next`.
11. **`idea verify --fail`/`--waive`** implemented and accepted by the daemon,
    documented in zero places including `openapi.yaml`.
12. **`advance`/`revise` return 500** where four sibling handlers return 409.
13. **`idea daemon <typo>` starts a foreground daemon.**

### P2

14. **`idea doctor` false-green outside a project** — carry-forward, unfixed
    since audit 1.
15. `idea init /some/path` initializes the current directory instead.
16. `idea config valdiate` exits 0 printing the model list.
17. **Ten commands swallow unknown flags** — `status`, `changed`, `executors`,
    `logs`, `docs`, `model`, `config`, `init`, `doctor`, `dev`.
    `idea status --jsonn` emits ANSI to stdout, exit 0; `idea logs -n 200`
    silently ignored.
18. `idea docs --jsn` → `no initiative matching "--jsn"`.
19. `idea watch` on a bad id spins at 1 Hz forever, exits 0.
20. `idea daemon status` exits 0 when down, writes to process stdout.
21. Absolute state path leaks to the client on executor config reload failure.
22. Two wildcard names for one path segment.
23. `attention` is the only `{"items": […]}` list endpoint; unescaped query
    values in three CLI call sites.
24. `docs/security-model.md` orphaned; "trust tiers" vs "trust levels";
    control-plane deny missing from the threat table.
25. Repo has no description/homepage/topics; `docs/api.md`'s 409 account names
    one of ~15 sources; `docs/cli.md`'s unknown-flag list omits 12 commands and
    closes with a false sentence.
26. Two of four shipped architectures never execute in CI.

### P3

CHANGELOG "70 operations" (81 shipped); `docs/configuration.md` wrong
`executor.image` default; README links `docs/api.md` twice; hosting doc's stale
spec size.

## The pattern

Across all three P0s and most of the P1s: **a fix made correctly in one place
and not propagated to its siblings.** `apply` got a precondition guard; `revert`
and `execute` did not. `parseArgs` was written with a carve-out for stdin; the
one command needing it does not call it. Four handlers return 409; two return
500. The unknown-flag test has a table, and the commands that still ignore flags
are the ones missing from it.

The documentation failures share that root: both CI drift guards check the axis
already fixed. `api-check` compares routes to the spec — which is correct, all
81 operations. Nothing compares routes to the human-readable tables, where the
ten missing endpoints are. `docs-check` regenerates the CLI table from the same
summary string the table is built from, so a flag absent from the summary is
invisible to it on both sides.

## Delegated evidence

| Deep dive | Standing in for | Findings | Re-verified | Disagreements |
|---|---|---|---|---|
| CLI and API surface | `api-design-reviewer`, `error-experience` | 15 ranked | 11 reproduced first-hand | One, and it was mine: I first reported the `GET /risk` mutation as not reproducing, having queried the database created by my own `idea init /nonexistent/path` probe. Against the real project database it is confirmed exactly as reported. |
| Documentation | `developer-docs-auditor` | 15 ranked + 5 below the cut | stdin-paste P0 and route-count gap reproduced directly | None |
