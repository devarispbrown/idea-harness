# DX Audit 8 — report

**HEAD `1f70f79`.** Run 2026-09-10, ~30 commits after audit 7. Every metric below is
**Observed** unless labelled otherwise.

## Verdict

**FAIL. Overall DX = 60/100** (Observed), up from audit 7's 49.

**Both hard gates PASS for the first time in eight rounds.** The verdict is still FAIL,
and not because of the score: three *named* gates fire, and the skill's rule is that a
failing gate is not averageable. Two of the three are defects introduced by the
remediation work between audit 7 and this run.

This is the most improved round the project has had. It is also the first round in which
the auditor's own repairs are a leading source of P0s, which is the finding worth keeping.

## Hard gates

| Gate | Result | Evidence |
|---|---|---|
| Magic path (`MAGIC_PATH_MAX_MIN` = 15) | **PASS** | Keyed on `openai/gpt-5.6-luna`: **3/3**, 369 s / 844 s / 398 s. No-key: **10/10** at ~4 s, 7 commands. First keyed pass ever recorded. Observed |
| Local development (`LOCAL_DEV_MAX_MIN` = 10) | **PASS** | Clean clone → `make build` 22 s → `make test` 46 s = **68 s**, 23 packages `ok`. Observed |

Margin is thin on the keyed path: 844 s is 14 min 04 s against a 15 min gate — a 6 % margin
on a three-sample basis. One slower model or one extra review cycle fails it.

Journey manifest 14/14 stages, span 6.14 s. Cold install 27 s, reporting `dev (1f70f79)`.

## Named gates

| Gate | Fires | Evidence |
|---|---|---|
| `UNDOCUMENTED_BREAKING_API` | **Yes** | `decodeStrict` has **three** call sites — `handleUpdateProviders` (`handlers.go:463`), `handleUpdateExecutors` (`:2336`) and `handleReviewChangeSet` (`:2143`). `CHANGELOG.md:269` documents two. `openapi.yaml`'s 400 for the review endpoint is byte-identical to v0.3.0. `decode.go:8` still says "The two config-writing endpoints" |
| `STALE_PUBLIC_REFERENCE` | **Yes** | `RELEASING.md:30` sends a maintainer to `docs/versioning.md` for the version-classification rules. `git log --all -- docs/versioning.md` is **empty** — the file has never existed in any commit. `docs-lint` globs `README.md` + `docs/*.md`, so `RELEASING.md` is unguarded |
| `UNEXPLAINED_ERROR` | **Yes** | `idea init` against a held port prints **nothing at all** for a code-bound ~51 minutes (see P0-1). `idea next --until-human` prescribes a command that always fails, with no route to the id it demands (P0-2) |
| `SDK_API_DRIFT` | **Cleared** | `phases.ts:39,140-142` now consumes `judgment_reason` instead of inferring from artifact presence, with tests in `phases.test.ts` |
| `NON_REPRODUCIBLE_BUILD` | No | 68 s clean clone |
| `BROKEN_CANONICAL_INSTALL` | No | 27 s cold install, correct version string |
| `BROKEN_QUICKSTART` | No | magic path inside `MAGIC_PATH_MAX_MIN` |

## Scores

| Area | Weight | Score | Evidence | Movement |
|---|---:|---:|---|---|
| Time to first value | 20 | 68 | Observed | 42 → 68 · both gates pass, three live-locks fixed; held back by 33–37 commands vs a target of 8, keyed 369–844 s vs a documented "about two minutes", and P0-1 |
| API | 15 | 56 | Observed | 48 → 56 · four of audit 7's most dangerous findings genuinely closed; one unrecoverable-state P0 and read-modify-write broken on both PUTs |
| SDK | 10 | 55 | Estimated (no SDK; scored on the first-party client) | 32 → 55 · `SDK_API_DRIFT` cleared and tested; 403/405 declared on 0 of 81 operations |
| CLI/config | 10 | 48 | Observed | 44 → 48 · argument validation is now genuinely solid; 18 commands exit 0 over a reported or concealed failure |
| Errors/recovery | 12 | 40 | Observed | 42 → 40 · 3 of 3 TTR scenarios miss the 5-min target, two catastrophically; six audit-7 findings unfixed and one regressed |
| Documentation | 10 | 60 | Observed | 64 → 60 · demo guides remain strong and guarded; five new defects, all self-inflicted this round |
| Local dev | 8 | 85 | Observed | 80 → 85 · 68 s clean clone; `af19253` closed audit 7's CONTRIBUTING gap — all nine CI-gating targets are now listed, plus smoke and the web job no make target reaches |
| Testing/quality | 8 | 72 | Observed + CI-observed | 58 → 72 · `ciguard` tests, the macOS exit-status test closes the panic-goes-green hole, `version-check` added, `EnqueueResponse` made all three live-locks testable |
| Release/compatibility | 7 | 56 | Observed + CI-observed | 52 → 56 · R-8 verified end-to-end against live CI run `34535066068`; R-2 guards fail closed against 7 attacks; one misfiled-history defect blocks a clean release |

**Overall DX = 60.**

## P0 findings

### P0-1 — `idea init` against a held port prints nothing for a code-bound ~51 minutes
`healthy()` (`internal/cli/client.go:203-213`) builds its client with `newClient()`, whose
timeout is **60 s** (`:85`). `ensureDaemon` (`:274`) calls it once up front and then 50 times
in a loop. Against a socket that accepts and never answers, that is 51 × 60 s ≈ **51 minutes**
before `daemon did not become healthy` is ever printed. Observed: 786.0 s of empty stdout and
stderr before SIGKILL.

The cause is in the log the entire time and nothing points at it — `idea logs` answers in
0.03 s with `listen tcp 127.0.0.1:19562: bind: address already in use`. The same 60 s stall
wedges every diagnostic: `idea doctor` hangs, `idea status` hangs, and `idea daemon status`
takes 60.0 s to answer **wrongly** (`daemon is not running`).

`daemon.NewRuntime` writes the token *before* `net.Listen`, so the token exists even though
the bind failed — which is why `healthy()` gets far enough to make the request at all.

Audit 7 recorded this as 1 of 6 diagnostic questions answered. It is now **0 of 6**.

### P0-2 — `idea next --until-human` prescribes a command that always fails
With two initiatives in one project, each carrying a `CHG-1`:

```
idea next --until-human   → exit 0   Next: apply CHG-1 — idea apply CHG-1
idea apply CHG-1          → exit 1   ambiguous change set reference "CHG-1": … use the full change set id
idea next --until-human   → exit 0   Next: apply CHG-1 — idea apply CHG-1
```

The mechanism is a scope mismatch: `idea changes` resolves one initiative and prints
`cs.HumanID` only (`internal/cli/change.go:142`), while `apply` and `change` resolve
**project-wide** (`internal/store/changeset.go:274`). The error demands "the full change set
id" and **no human-readable command prints one** — it exists only in `idea changes --json`,
which no error, help line or hint names. `next` exits 0 while making no progress.

### P0-3 — A 413 on `POST /initiatives` leaves an initiative permanently stuck in `running`
`StartInitiativeWithDocs` pre-validates each doc for emptiness and credentials
(`internal/daemon/runtime.go:471-479`) under a comment promising "a rejected doc never leaves
a half-created row". The **size** cap is not in that loop: it lives inside `ingestTextDoc`
(`internal/daemon/handlers.go:1689`), called at `runtime.go:485` — after `CreateInitiative` at
`:481` and before `CreateOperation` at `:490`.

So an oversized doc yields 413 with an initiative row already committed as `running` and no
operation row. `reconcileStaleOperations` sweeps operations, so it never sees it. `retry`,
`resume` and `next` all refuse or wait forever, `/attention` is wedged, and there is **no
`DeleteInitiative` anywhere in the tree** — verified. Recovery requires hand-editing SQLite.

### P0-4 — `e53bf2f` promoted 30 lines of released v0.1.0 history into `[Unreleased]`
The commit moved the whole `### Changed` block when only the 26-line P1-6 entry was
misplaced. The removed and added line sets are **identical** — nothing was deleted — but 30
lines that shipped in v0.1.0 now sit under `[Unreleased]` and will be re-advertised as new.

Verified against the tags: at `v0.3.0` the entry *"The Go module path is now
github.com/devarispbrown/idea-harness"* sits under `## [0.1.0] - 2026-09-03`; at HEAD it sits
under `## [Unreleased]`. That entry's own text reads *"it lands before the first tag"*.

goreleaser ships `CHANGELOG.md` inside every archive, so this blocks a clean release.

*(Correcting my own earlier report of this defect: I first described it as 33 lines deleted.
It is 30 lines misfiled. Nothing was lost.)*

## P1 findings

| # | Finding | Class |
|---|---|---|
| P1-1 | The model-id change was **documentation-only**. `internal/model/catalog.go:12` still ships `claude-3-7-sonnet-latest` — the exact id audit 7's L3 deleted from 9 doc sites *because it does not resolve* — so `idea model set anthropic/claude-3-7-sonnet-latest` is silently blessed. `UnknownModelProblems` (`problems.go:186-189`) matches exactly against that catalog, and its doc comment at `:167` names that very id as the defect it exists to prevent. Meanwhile `openai/gpt-5.6-luna` — README:154, quickstart:46,56, the model the project actually runs on — is **absent** from the catalog and warns | CLI/config |
| P1-2 | `--accept-findings` appears **nowhere** in `README.md` or `docs/quickstart.md`. It landed in code and in both demo guides, but not in the two documents that define the canonical magic path — so a user on the keyed path who hits a blocking change-set finding has no documented escape | Documentation |
| P1-3 | Ledger row **L1 is green over a live contradiction**. `runtime.go:1689-1690` still says a blocking change-set finding "cannot be cleared by anything the CLI can do", six lines below the correction at `:1680-1683` saying the opposite. The row's predicate contains a literal `\n` and is unmatchable | Documentation |
| P1-4 | 3 of 3 TTR scenarios miss `TTR_TARGET_MIN` = 5:00 — port-held init (never), hung provider (~15:05 to the first visible error), ambiguous `CHG-1` (no route exists) | Error/recovery |
| P1-5 | `idea doctor` exits 0 saying "all checks passed" over an unreadable store, rendering the read failure as `graph audit: no initiative yet — nothing to audit`. Separately, it reports `daemon: not running` while `curl` gets a 200 from that daemon. The CLI holds **three mutually contradictory answers** to "is the daemon running" | Error/recovery |
| P1-6 | `idea retry` exits 0 with `✓ retry started` for a retry that fails 0.51 s later. `cmdRetry` (`change.go:532-567`) has no wait loop, unlike `cmdNew` | Error/recovery |
| P1-7 | **18 commands exit 0 over a reported or concealed failure**, including `idea status` printing `● failed` with a full 401, and `idea risk` printing `Risk Profile (low)`. `cmd/idea/main.go` has exactly one `os.Exit(1)`; ~35 distinct failures all exit 1. `--json` never emits an error object, and `idea status --json` **drops `failure_reason` entirely** | CLI/config |
| P1-8 | Read-modify-write is now impossible on both PUTs. `GET /providers` → `PUT /providers` verbatim → `400 json: unknown field "name"`; executors likewise on `capabilities`. Strict decoding shipped without making the GET shape PUT-compatible, and no writable subset is documented | API |
| P1-9 | A YAML syntax error in `harness.yaml` answers **404** on 34 project-scoped routes with an **absolute filesystem path**, and 500 on one — violating `NotFound`'s documented contract and `InternalError`'s promise that "absolute state paths never reach the client" | API |
| P1-10 | `docs/openapi.yaml:2559` is **structurally invalid**: `required: [status]` is over-indented into `properties:`, declaring a property named `required` whose schema is a YAML array. Verified as the only such defect by walking every `properties` node; `make api-check` does not see it | API |
| P1-11 | 403 and 405 are emitted on all **81** operations and declared on **zero**, with no `default:` response. The spec's own justification says "repeating them 84 times" — a stale count | API |
| P1-13 | My `--accept-findings` insertion broke the command→output block pairing in `docs/demo-greenfield.md` (the blockquote now sits between the command block at 118-121 and its output at 131-135) and left a stray double blank line in both guides (`demo-greenfield.md:131`, `demo-existing-project.md:143`) | Documentation |

## Disagreement with a delegated verdict

The API dive scored `P0-2 — graph endpoints silently return a wrong answer for the identifier
the entire product prints` as a P0, citing `idea why requirement REQ-1` from
`docs/demo-greenfield.md:104`.

**I re-verified and downgraded it to P1.** Both first-party consumers resolve codes before
calling: the CLI runs `resolveObjectID` (`internal/cli/advance.go:251,289`), which converts
`REQ-1` to the hex id and errors cleanly on a miss; the web UI passes `d.id` and labels with
`d.code` (`web/src/JudgmentCard.tsx:73`). The documented CLI journey is **not** broken.

What remains real, and is why it is still a P1: the API accepts only the opaque id, returns a
deliberately non-nil `[]` (`internal/workflow/trace.go:36`) that is indistinguishable from "no
edges", and `impact-counts`' own worked example in the spec (`openapi.yaml:980`) is
`requirement:req-1` — a form that returns a wrong answer. `GraphNodeId` is documented as
"Graph node id." while `ChangeSetId` explicitly promises both forms resolve.

The dive's mechanism was right; its blast radius was not.

## What the round genuinely fixed

Recording these because the score movement alone understates them.

- **Three live-locks of identical shape, all closed** — PRD review (audit 6), plan review
  (audit 7 P0-1, `001d420`), change-set review (`5593472`). Each had an escape command that
  re-ran a model reviewer, and a model reviewer is not a fixed point.
- **Four of audit 7's most dangerous API findings**, verified closed live: `/revise-plan`
  202-then-`failed`, `PUT /providers` dropping an inline `api_key` and flipping to demo,
  `/verify` rewriting `failed`→`complete`, and the undeclared 409.
- **R-8 verified end to end against a real CI run** (`34535066068`): all five gating jobs ran
  on the tagged commit and the release job evaluated ~3 minutes after they started, which is
  the observable signature of `needs` serialising.
- **`idea doctor`'s missing-key path is the best error message in the tree** — cause,
  consequence, fix and retry-safety in one screen, 0.1 s, and its prediction was correct.
- **Secret scrubbing held under every attack tried**, in stdout, `idea status` and the log.
- **`idea new`'s final line now reflects the terminal state**, and both routes it names work.
- **CONTRIBUTING now names every CI-gating target**, closing audit 7's local-dev finding, and
  calls out the `web` job that no make target reaches.

## Gate list: the version this audit applied

This audit applied the gate list from the **installed** plugin, 2.3.2. The working source at
`~/Code/projects/devex/devex-skills/` is **2.9.4** and adds exactly one gate constant —
`UNVERIFIABLE_CI_PARITY` (P1): *"the documented local check command does not appear in CI
configuration."* Nothing else in the gate list differs, and the verdict vocabulary is
byte-identical, so the FAIL above stands either way.

Applied forward-looking, that gate **fires narrowly**: `make test` and `make vet` are
byte-identical to CI's `go test ./...` / `go vet ./...`, and every guard, `embed-web`, smoke
and the pnpm steps appear verbatim in `ci.yml` — but `make build` (`Makefile:17-19`:
`embed-web` + `go build -trimpath -ldflags … -o bin/idea ./cmd/idea` + `restore-embed`) is
invoked nowhere in CI, which runs a bare `go build ./...`. Recorded here rather than rescored,
because the audit ran against 2.3.2.

## A finding withdrawn after review

**P1-12, "CONTRIBUTING names 2 of the 9 CI-gating targets", was false and has been removed.**
`CONTRIBUTING.md:34-42` lists all nine, plus `scripts/smoke.sh` and the web job. The error was
mine: `grep -oE "make [a-z-]+"` captures only the first token after `make`, so the line
`make check-embed docs-check api-check api-docs-check docs-lint` reported as `make check-embed`
alone. Local dev is rescored 78 → 85 and Overall DX 59 → 60 accordingly. Caught by adversarial
review of the fix plan, not by me.

## World-class checklist

| # | Criterion | Result |
|---|---|---|
| 1 | All hard gates pass | **Yes** — first time in eight rounds |
| 2 | Overall DX ≥ 85 | No — 59 |
| 3 | Every area ≥ 75 | No — one area (local dev, 85); testing/quality is next at 72 |
| 4 | No unresolved P0/P1 | No — 4 P0, 12 P1 |
| 5 | Magic-path timing Observed ≤ 15 min | **Yes** — keyed 3/3 at 369/844/398 s; margin is 6 % |

## Delegated evidence

| Dive | Skill | Verdict | Re-verified by me |
|---|---|---|---|
| Documentation | `developer-docs-auditor` | FAIL | Yes — all three P0s confirmed directly |
| API / contract | `api-design-reviewer` | API DX 54, OpenAPI 57 | Yes — P0-1 ordering, no `DeleteInitiative`, the 81/0 count, and the invalid schema all confirmed; **P0-2 downgraded, see above** |
| Errors / TTR | `error-experience` | FAIL | Yes — the 60 s `healthy()` bound and the `HumanID`-only lister both confirmed |
| Release | `release-guardian` | Blocked | Yes — CHANGELOG measured across `v0.1.0`/`v0.3.0`/HEAD; `docs/versioning.md` confirmed never to have existed |

## The lesson this round

Audits 1–4 compared artifacts to artifacts. Audit 5 broke that by calling a real provider.
Audit 8's new failure mode is different again: **the repairs are now a leading source of
defects.** Four of the seventeen P0/P1s above were introduced by the remediation work
between audit 7 and this run — the misfiled changelog history, the undocumented third strict
decoder, the ledger row green over a surviving false sentence, and the demo-guide block
pairing.

The common shape is a fix applied to the *description* of a thing without the thing, or to
one instance of a pattern without the others: documentation-only model ids, two of three
strict decoders documented, a correction added beside a falsehood rather than replacing it.
Guards did not catch any of the four, because every one of them is internally consistent
with itself — and the guards compare artifacts to artifacts.
