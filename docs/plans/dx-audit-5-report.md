# DX Report — idea-harness

## Verdict

**Release gate:** FAIL
**Overall DX:** 61/100 (evidence: Observed)

Two hard gates were assessed. **Local development PASSES.** **The magic path FAILS** —
`BROKEN_QUICKSTART` — and a gate failure forces FAIL regardless of the score.

The score fell from audit 4's 79. **This is audit depth, not tree regression.** The two P0s
below have been in the tree since the initial commit and shipped in v0.1.0; four prior audits
missed them because no audit had ever made a real provider call or run the README's block
verbatim on the no-key path. Everything the previous rounds fixed stayed fixed: I could not
find a single stale command, flag, route row, spec enum, link or anchor.

## Evidence

- **Repository/revision:** github.com/devarispbrown/idea-harness, main @ `de8ced5` (clean clone),
  plus `f517d1b` (a CI fix made mid-audit, see Note).
- **Environment:** macOS (darwin/arm64), Go 1.25.5, clean clone 2026-09-04. Isolated
  `IDEA_HARNESS_STATE` per run; ports 18901-18999. The user's shell exports OPENAI/ANTHROPIC/
  GOOGLE keys — every no-key measurement used `env -u` to remove them.
- **Journey scope:** full — all 14 stages.
- **Checks executed:** journey runner (dry-run + `--execute --scope all`); the README quick start
  verbatim 10x under two key conditions; cold and warm `make test`; `scripts/smoke.sh`; the real
  v0.1.0 release binary downloaded and run; ~70 live HTTP probes; all seven doc guards.
- **Checks not executed:** real OpenAI/Anthropic paid calls by me (the delegated dive made them,
  3/3 reproduction, and I confirmed the cause statically); Docker executor paths; `linux/arm64`.

## Magic path

**Status:** FAIL
**Evidence:** Observed
**Elapsed:** 11s on the with-key demo path; **both documented paths fail** (below).
**Start / stop condition:** from `mkdir my-product` to `idea next` reporting a verified
complete initiative with a real applied change set.

| Metric | Count | Target | Status |
|---|---:|---:|---|
| Commands | 8 | 8 | PASS |
| Credentials | 0 | 2 | PASS |
| Context switches | 1 | 4 | PASS |

The budget is not the problem. **The documented sequence is.** Run verbatim from README:144-149:

| Condition | Result |
|---|---|
| Provider keys **set** in the environment | 5/5 pass, 11s, real `hello.txt` |
| Provider keys **unset** (the documented no-key path) | **0/5 pass**, ends in a false success |
| `OPENAI_API_KEY` set + `idea model set openai/gpt-4o-mini` (README's own example) | **fails 100%** at `product_definition` |

## Per-stage timing

| Stage | Objective met? | Time | Commands | Credentials | Context switches | Errors | Evidence |
|---|---|---|---|---|---|---|---|
| find | Partial | — | 0 | 0 | 1 | 0 | Observed — repo is private; README is candid about it and names the canonical install |
| understand | Yes | <1 min | 0 | 0 | 1 | 0 | Observed |
| install | Yes | 20s | 2 | 0 | 0 | 0 | Observed — `go install ./cmd/idea`, no Node needed |
| auth | Yes | <1s | 1 | 0 | 0 | 0 | Observed — no credential required for the documented path |
| configure | Yes | <1s | 1 | 0 | 0 | 0 | Observed |
| execute | **No** | — | 1 | 0 | 0 | 1 | Observed — see P0-1/P0-2 |
| verify | **No** | — | 1 | 0 | 0 | 1 | Observed — reports success over an empty initiative |
| modify | Yes | 1s | 1 | 0 | 0 | 0 | Observed — with a wait inserted |
| break | Yes | <1s | 1 | 0 | 0 | 8 | Observed |
| diagnose | **No** | — | 1 | 0 | 0 | 4 | Observed — `doctor` reports healthy during 3 distinct failures |
| recover | **No** | — | 1 | 0 | 0 | 4 | Observed — 4 of 8 induced errors have no in-product route |
| test | Yes | 23s cold | 1 | 0 | 0 | 0 | Observed — 592 tests, 22 pkgs, 0 failures |
| deploy | Yes | — | 0 | 0 | 1 | 0 | Observed — `deploy/api-docs`, `docs/api-reference-hosting.md` |
| upgrade | Partial | — | 0 | 0 | 1 | 0 | Observed — changelog excellent, release 57 commits stale |

## Per-area scores

| Area | Weight | Score | Evidence | Notes |
|---|---:|---:|---|---|
| Time to first value | 20 | 45 | Observed | Machinery is fast (11s); both documented routes fail |
| API | 15 | 68 | Observed | Coherent model, uniform 202s; envelope broken at the routing layer |
| SDK | 10 | 60 | Observed | None, deliberately; spec is now complete and enum-checked, so generation is viable |
| CLI/config | 10 | 64 | Observed | Excellent arg hygiene; one P0 silent data loss |
| Errors/recovery | 12 | 48 | Observed | 4 of 8 induced errors have no in-product recovery |
| Documentation | 10 | 68 | Observed | Top-decile accuracy; behaviour/sequence claims unenforced |
| Local dev | 8 | 92 | Observed | Clean clone to green tests in ~21s, zero setup |
| Testing/quality | 8 | 62 | Observed + CI-observed | Strong drift guards; blind to provider contracts |
| Release/compatibility | 7 | 58 | Observed | Best-in-class changelog; only artifact is 57 commits stale |
| **Overall DX** | 100 | **61** | Observed | |

## Gate failures

| Gate | Severity | Stage | Finding | Evidence | Acceptance test |
|---|---|---|---|---|---|
| `BROKEN_QUICKSTART` | P0 | execute/verify | README's 8-command block on the no-key path does nothing, then prints `✓ initiative complete` over 0 change sets | Observed 0/5 | Run README:144-149 verbatim with no keys; assert a change set exists and a file was written before `complete` |
| `BROKEN_QUICKSTART` | P0 | execute | README's own with-key example (`openai/gpt-4o-mini`) fails 100% — `max_tokens 32000` vs OpenAI's 16384 ceiling | Observed 3/3 (delegated), cause confirmed statically | Table test: `maxOutputForStage(s) <= provider.MaxOutput` for every stage × provider |
| `UNEXPLAINED_ERROR` | P1 | diagnose/recover | Wipe the state dir while the daemon runs: every command `idea: unauthorized`; `daemon stop` says "no daemon pidfile"; `doctor` and `daemon status` report healthy, exit 0 | Observed | Assert `doctor` exits non-zero when the daemon rejects the CLI's token |
| `UNEXPLAINED_ERROR` | P1 | diagnose | OpenAI/Anthropic discard the API error body (`status %d`); Gemini keeps it | Observed + code | Assert the provider error includes the upstream message |

## Problem-classification backlog

| Priority | Problem class | Stage | Finding | Owner skill | Acceptance test |
|---|---|---|---|---|---|
| P0 | CLI and configuration | execute | `idea next --until-human` does not wait for the operation `idea new` left in flight, though README:164 promises it does | developer-onboarding | Run the block back-to-back 10x with no keys; all must reach `review CHG-1` |
| P0 | CLI and configuration | verify | `idea next` reports `✓ initiative complete` for an initiative with 0 tasks and 0 change sets | quality-engineer | Assert completion requires at least one applied change set |
| P0 | API and contract design | execute | `maxOutputForStage` returns 32000 under a comment asserting the 16k ceiling; nothing clamps; `Capabilities.MaxOutput` is declared and never read | api-design-reviewer | The table test above |
| P0 | CLI and configuration | configure | `idea model set` writes from the daemon's cached config, silently reverting hand edits (`max_cycles`, `research.urls`, `git_commit`) at exit 0. The sibling `handleUpdateExecutors` re-reads from disk with a comment saying why | api-design-reviewer | Hand-edit, `model set`, assert untouched fields survive |
| P0 | CLI and configuration | configure | A bare `default_model` (`gpt-4o`, no provider) is accepted by every guard and silently routes to the demo provider with the banner suppressed | error-experience | Assert a spec with no `/` is rejected |
| P1 | Error and recovery quality | recover | `idea daemon stop` destroys an in-flight run, writes no reason, logs nothing, and points at `idea retry` — a command named 5x in the codebase that does not exist | error-experience | Assert a graceful stop either drains or records a reason |
| P1 | Error and recovery quality | diagnose | `idea next` tells a **failed** initiative the workflow is running, exit 0 (`next.go:65` groups failed with running) | error-experience | Assert `next` on a failed initiative names the recovery |
| P1 | API and contract design | execute | `POST …/suggest-answers` returns 202 + an operation id for a nonexistent initiative; every sibling 404s. Already recorded as `C5` in `deferred.md` | api-design-reviewer | Assert 404 parity across all async entry points |
| P1 | API and contract design | — | Routing-layer errors are `text/plain`, contradicting "Every error response has this shape and nothing else"; 405 never emitted | api-design-reviewer | Assert every 4xx is JSON |
| P1 | Documentation | — | `api.md` documents review verdicts (`approve`/`reject`) the API 400s — the repo's own `openapi.yaml` is already correct | developer-docs-auditor | Extend `enum-check` to api.md prose |
| P1 | Documentation | configure | Demo mode is described by a condition that is not the code's; a user who exports a key as instructed gets synthetic output while `doctor` says "all checks passed" | developer-docs-auditor | Assert the documented predicate matches `isDemo` |
| P1 | CLI and configuration | — | `idea why requirements REQ-1` (plural typo) → "No provenance edges found", exit 0. Bad *id* is caught; bad *type* is not | api-design-reviewer | Reject an unknown node type |
| P1 | CLI and configuration | — | `idea config` is byte-identical to `idea model` while help promises "show or validate .idea-harness config" | developer-onboarding | Assert `config` renders config |
| P2 | API and contract design | — | `/doctor` returns `null`; `/diff` mixes `[]` and `null` in one response. **My audit-4 sweep missed both** | api-design-reviewer | Extend the null sweep to every route in `routes.go` |
| P2 | Testing and quality story | test | No push-tier test makes a real provider call, so a 100%-reproducible OpenAI failure ships green | quality-engineer | A recorded-cassette contract test per provider |
| P2 | Testing and quality story | test | Tests that read ambient provider keys pass locally and fail in CI (I merged one this session) | quality-engineer | Run the suite with all provider keys unset in a pre-push hook |
| P2 | Release and compatibility | upgrade | Only artifact is v0.1.0, 57 commits and ~10 BREAKING entries behind, shipping 4 false-green defects the changelog says are fixed | release-guardian | Cut 0.2.0 |
| P2 | Release and compatibility | upgrade | `idea version` reports `v0.1.1-0.2026…`, a version that does not exist and outranks the only release; the documented `dev` form is unreachable | release-guardian | Assert `version` on a non-tag build matches the documented form |
| P2 | Error and recovery quality | break | 50s of silence when the port is busy, then a generic failure | error-experience | Surface `bind: address already in use` immediately |
| P2 | CLI and configuration | — | No per-command help: `--help`/`-h` rejected on all 33 subcommands | developer-onboarding | `idea <cmd> --help` exits 0 with usage |
| P3 | — | — | 15 further items across the three dives (no did-you-mean, doubled log lines, `idea cancel` with no way to get an op-id, stale bundled Gemini id, `.env` absent from onboarding, api.md/ui.md detail errors) | — | — |

## Delegated evidence

| Finding | Delegated skill | Verdict | Re-verified? | Disagreements reported |
|---|---|---|---|---|
| Documentation dive (P0 quickstart, 5 P1s, ~20 more) | developer-docs-auditor (fallback) | 68/100 Observed | **Yes** — I reproduced the quickstart P0 myself | **Yes.** Its P0 contradicted my own passing magic-path runs. Resolved by controlled A/B: the variable is ambient provider keys — 5/5 pass with, 0/5 without. Both results were correct; neither had isolated the cause |
| API + CLI dive (P0 config clobber, 5 P1s) | api-design-reviewer (fallback) | API 68, CLI 64, Observed | **Yes** — reproduced the config clobber, the 202, the `text/plain` 404, the trace typo and the `config` alias | None |
| Errors + testing + release dive (2 P0s, 4 P1s) | error-experience / quality-engineer / release-guardian (fallback) | 48 / 62 / 58, Observed | **Partly** — confirmed the token-ceiling P0 statically (decisive); did not repeat paid API calls | One self-correction accepted: it flagged an Anthropic 400 as transient rather than claiming a second P0 |

## World-class checklist

- [ ] all hard gates pass — **NO**, magic path fails
- [ ] Overall DX >= 85 — **NO** (61)
- [ ] every per-area score >= 75 — **NO** (5 of 9 below)
- [ ] no unresolved P0/P1 findings — **NO** (5 P0, 8 P1)
- [ ] magic path Observed at <= 15 min — **NO**

## Note on a defect introduced during this audit

I merged audit 4's remediation reporting "green", based on local runs. **CI failed on that merge.**
Two subtests asserted "no warnings" for a correct model id, but a missing provider key also warns —
so they passed on my machine (keys exported) and failed on CI (none). That is the same
environment-dependence I had fixed in a sibling test earlier in the same session, with a comment
explaining it, and did not carry across. Fixed in `f517d1b`; CI is green on main. It is recorded
as a P2 above because the class is general: nothing in the tree stops an env-dependent test.
