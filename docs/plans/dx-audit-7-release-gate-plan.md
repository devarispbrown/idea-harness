# DX Audit 7 — R-8 and R-2 (the release gate and the spec version)

Audit 7 recorded these as *"the longest-lived open findings in the project, the
two cheapest to fix, one shared fix, and the only findings with no ledger row."*
R-2 has now been missed at three consecutive tags; R-8 has been open since
audit 6.

## The findings

**R-8.** `.github/workflows/release.yml` gates a tag on `go vet ./... && go test
./...`, on `ubuntu-latest`, and nothing else. It runs **none** of the eight
guards, never runs `scripts/smoke.sh`, has no macOS job, no web build and no
`pnpm test`. It carries no `needs:`.

**R-2.** `docs/openapi.yaml:17` is `version: 0.1.0`. `git log -L '/^info:/,+5'`
returns exactly one commit — the file's creation. It has not moved across
v0.1.0, v0.2.0 or v0.3.0, while the spec itself gained four response codes, a
schema, two properties and an enum this round alone. `docs/api-reference-hosting.md`
instructs *"Bump `info.version` when you tag"*, and that instruction lives in a
document nobody reads at tag time. Three misses is what an unowned manual step
looks like.

## Correction to the audit's proposed fix

The report says R-8 is *"two lines (`needs: [compile-no-node, backend, macos,
web]`)"*. **That does not work.** `needs:` resolves job ids **within one
workflow file**; it cannot reference a job in another workflow. A cross-workflow
dependency needs `workflow_run`, which fires against the default branch's
workflow definition and makes the triggering tag ref awkward to recover.

So the fix is structural, not two lines.

## The fix

### 1. Move the release job into `ci.yml`, delete `release.yml`

A job in `ci.yml` with:

```yaml
needs: [compile-no-node, macos, backend, web]
if: startsWith(github.ref, 'refs/tags/v')
permissions: { contents: write }
```

`ci.yml` already triggers on `push:` with no branch or tag filter, so its jobs
**already run on a tag push** — they simply do not gate. This makes them gate,
which is the whole finding.

**Why not keep `release.yml` and copy the guard steps in.** Duplication is the
defect this repo keeps finding: a ninth guard added to `ci.yml` would not reach
the copy, and nothing would say so. `needs:` cannot drift.

The job moves **with its steps and its checkout block verbatim**. Three
properties must survive the move, and none is visible from the surrounding
`ci.yml` jobs, every one of which uses a bare `actions/checkout@v5`:

- `fetch-depth: 0` — `.goreleaser.yaml` interpolates `{{ .Tag }}`,
  `{{ .FullCommit }}` and `{{ .Version }}`, all derived from git history. A
  depth-1 clone breaks the build silently.
- `persist-credentials: false` — goreleaser is handed `GITHUB_TOKEN`
  explicitly, so the persisted credential buys nothing and only widens the
  blast radius. Dropping it is a silent regression of the repo's posture.
- The comment forbidding `make embed-web` in the verify step. `git show
  04442b7` records that as a pre-merge **P0**: it overwrites the tracked
  placeholder, goreleaser refuses a dirty tree, and the first real tag push
  would have aborted. The new job sits directly below `backend`, whose second
  step *is* `make embed-web` — removing the warning while placing the job next
  to the anti-pattern it warns about re-arms a hazard a human caught once at
  the last possible moment.

The release job does **not** need `check-embed`: goreleaser's before-hook
dirties the tree only after goreleaser's own git validation, and nothing runs
afterward. `backend`'s `make embed-web` runs on a different runner and does not
interact. Both facts go in the comment, because neither is obvious.

**Two consequences the draft did not state.**

`ci.yml` carries `workflow_dispatch:`, and a manual dispatch can select a
**tag** ref — so `startsWith(github.ref, 'refs/tags/v')` alone opens a release
path `release.yml`'s `on: push: tags:` never had. The condition is therefore
`github.event_name == 'push' && startsWith(github.ref, 'refs/tags/v')`.

`ci.yml:8-10` says *"no job here writes to the repository."* After this, one
does. Job-level `permissions` replaces the workflow default entirely and the
`agent` job declares `contents: read` for itself, so nothing is actually
broken — but the comment must be rewritten to say which job is the exception
and why the agent tier still cannot inherit it.

The job also takes `concurrency: { group: release-${{ github.ref }} }` with
**no** `cancel-in-progress`. Nothing races today, but `ci.yml` now runs on every
push, which makes a workflow-level `concurrency` with `cancel-in-progress: true`
a tempting future addition — and it would cancel a running release. The group
plus a comment forbidding that is the cheap defence.

**What this costs.** The workflow named `release` disappears; a required status
check naming it would need repointing. Confirmed nothing references it: no
`workflow_run`, no in-tree branch-protection reference, and `git log --follow`
shows two commits and no comment justifying a separate workflow — the
separation is incidental, not deliberate.

The `agent` job stays out of `needs`: it is `workflow_dispatch`-only, so on a
tag push it is skipped, and a skipped job in `needs` would skip the release.

### 2. Two version guards, not one

The draft proposed only a tag-time check and rejected a CHANGELOG-based one as
"guessing". **That rejection was wrong**, and the reasoning it used is the
reasoning this same plan applies to R-8 four sections later. R-2 is not "the
spec fails to predict the next version." R-2 is **"the spec describes a release
three releases old."** That needs no guessing:

**Guard A, every push** — `info.version` must be **≥** the newest `## [X.Y.Z]`
heading in `CHANGELOG.md` (`[Unreleased]` is skipped: it carries no version).
It never predicts anything, it runs in `compile-no-node` beside the other eight
guards, and it would have gone red on the first push after v0.2.0 shipped —
four rounds before a tag-time guard could fire. Equality satisfies it, so it
does not nag after a release.

**Guard B, tag time** — `info.version` must equal `${GITHUB_REF_NAME}` with any
leading `v` stripped, run as the release job's first step, before goreleaser.
This is the only thing that catches "bumped to 0.4.0, tagged v0.5.0". It fires
late, but not too late: goreleaser has published nothing when it runs, and
`.goreleaser.yaml` makes the release a draft regardless, so the recovery is
delete the tag, bump, re-push.

Both live in `internal/docsgen`, which already parses the spec for `-spec-json`
and already reads `CHANGELOG.md` paths, so neither greps.

`make version-check` with no `VERSION`: `Makefile:10` already defines
`VERSION ?= $(shell git describe --tags --exact-match 2>/dev/null)`, which is
empty on any non-tag checkout. The empty case is an **error** naming the fix
(`pass VERSION=vX.Y.Z`), not a silent pass.

### 3. `info.version: 0.1.0` → `0.4.0`, and two sentences that become false

The release dive argued v0.4.0 from the project's own thrice-stated pre-1.0
policy. Setting it now is what makes both guards pass; if the tag ends up
different, guard B says so.

No published artifact starts announcing 0.4.0 early: publishing is
`scripts/publish-api-docs.sh` run by hand, and `make api-docs-dist` writes only
into a local bundle. The only exposure is a `main` binary serving
`/openapi.yaml` as 0.4.0 while `idea version` says `dev` — strictly better than
today, where it says 0.1.0 against a v0.3.0 binary.

`docs/api-reference-hosting.md` must be edited twice, and this is exactly the
class `make claims-check` exists for:

- *"no guard reads `info.version` at all … three axes, none of them the
  version"* — false the moment guard A exists.
- *"Bump `info.version` when you tag"* — the policy changes to bumping at
  merge, which is what makes the guard passable. A different policy, stated as
  such.

### 4. `RELEASING.md`

There is no release checklist anywhere: `CONTRIBUTING.md` contains no occurrence
of "release", there is no `RELEASING.md`, and `git tag`/`goreleaser` appear only
in `CHANGELOG.md` and two archived audit plans. The one instruction that exists
sits in a hosting document. Three consecutive misses of a one-line manual step
is the argument for writing the step down where it is looked for.

### 5. `scripts/ci-test-summary.py` misses a whole class of failure

The macOS job runs `go test -json … ; continue-on-error: true` and delegates the
verdict to the summary script, which returns non-zero only when it parsed an
`action == "fail"` event carrying a `Test` (`ci-test-summary.py:40`).

**An earlier draft of this plan got the case list wrong**, and the correction is
the useful part. Measured on go1.25.5, all four shapes:

| case | terminal events | script today |
|---|---|---|
| (a) compile error | `build-fail` on the ImportPath, then `fail` with `FailedBuild` and no `Test` | **exit 0 — missed** |
| (b) panic in `TestMain` | `fail` with `Package`, no `Test` | **exit 0 — missed** |
| (c) panic **inside a test** | `fail` with `Test`, **and** a package-level `fail` | **exit 1 — already caught** |
| (d) panic in a **background goroutine** | only `fail` with `Package`; the running test never gets a terminal event | **exit 0 — missed** |

So the case this plan originally led with, (c), is not a defect at all —
test2json attributes a repanic to the test goroutine. The case that actually
evades is (d), which the draft never named: the binary dies out from under a
running test and the script prints `All tests passed (0 package(s) with tests).`

**Fix, and it is not the one the draft proposed.** Every failing case emits a
package-scoped `{"Action":"fail","Package":…}` with **no** `Test`. The script
already discriminates on exactly that shape for the *pass* case
(`ci-test-summary.py:44`, `elif action == "pass" and not test`) and simply has
no `fail` counterpart. Add one, plus `build-fail` for the ImportPath. That
catches all four, names the package that died, and needs no workflow change.

The draft's "capture `go test`'s exit status and pass it to the script" is the
weaker half and is awkward where it would live: `ci.yml`'s steps run under
`bash --noprofile --norc -eo pipefail`, so the redirect aborts the step at the
failing command and `$?` cannot be read on a following line without
`|| status=$?`; and `continue-on-error: true` makes `steps.<id>.outcome`
`success` regardless, so that is not a substitute either. Keep it as a
**secondary** signal — it still catches a truncated or unwritten JSON file —
and spell out the `|| status=$?`.

`ci-test-summary.py:33`'s comment (*"a build error is printed as plain text, not
JSON"*) is also false on go1.24+: `build-output`/`build-fail` arrive as JSON on
stdout, and stderr was empty in all four measured runs.

## Acceptance

| # | Assertion | Where |
|---|---|---|
| 1 | `version-check` passes for `v0.4.0`, fails for `v0.5.0` naming both versions, and errors with a usable message when `VERSION` is empty | `internal/docsgen` unit test over a fixture spec |
| 2 | It rejects a spec whose `info.version` is absent or unparseable | same |
| 3 | Guard A fails when `info.version` is older than `CHANGELOG.md`'s newest released heading, and passes on equality | same, with a fixture CHANGELOG |
| 4 | `ci-test-summary.py` exits non-zero for **each** of cases (a) compile error, (b) `TestMain` panic, (d) background-goroutine panic — and still for (c) | four `go test -json` fixtures, captured from a real run, committed under `scripts/testdata/` |
| 5 | It still exits 0 on a clean run, and the fixture contains a package-level `pass` so the new branch is proved not to fire on it | fifth fixture |
| 6 | **Every job id in `ci.yml` is either in the release job's `needs`, or is the release job itself, or is in an explicit exemption list carried in the test with a one-line reason** | a Go test parsing `.github/workflows/ci.yml` with `gopkg.in/yaml.v3` (already a dependency), using `internal/docsgen`'s existing `repoRoot()`; `go test ./...` runs in all four gating jobs |

Assertion 6 is the one that keeps this fixed, and its rule is a **completeness**
rule, not the membership rule the draft wrote. *"Every non-`workflow_dispatch`
job"* was wrong twice: it requires `release` to appear in its own `needs` (a
cycle GitHub rejects), and `agent`'s exemption is a literal `if:` string, not a
property a future job necessarily shares — a scheduled job, a docs-preview
deploy, or a job that itself depends on the release would all legitimately not
gate. Under the completeness rule a new job matches none of the three branches
and the test fails, forcing a decision instead of guessing one. Same shape as
the `deferred.md` mechanism this repo already runs.

**Mutation checks.** Each: remove the guard, assert the test fails. For
assertion 6, add a fake job to a fixture `ci.yml` and assert it fails.

## Ledger

`ledgerGlob` is `docs/plans/dx-audit-*-doc-defects.md`, and **there is no
`dx-audit-7-doc-defects.md`** — audit 6's is the newest, so nothing in audit 7's
report is visible to the checker. This round creates it, and its table must
match the parser's row regex or `ledger-check` fails with *"no ledger rows
parsed"*.

**R-2** carries both directions, per audit 7's method finding: the `present:`
predicate is `present:docs/openapi.yaml:version: 0.1.0` (verified unique — the
prose "in 0.1.0" elsewhere in that file does not collide, because `evalPredicate`
cuts on the first two colons only), paired with an `absent:` asserting the
correct version is stated.

**R-8 must be `manual:`.** Its false state is *the absence of a `needs:`* in a
file this change deletes, and a predicate naming a path that cannot be read is
an **error** in `evalPredicate`, which turns `ledger-check` permanently red —
in `compile-no-node`, on every push. There is no `present:` literal for it. The
`manual:` reason points at acceptance assertion 6, which is the mechanical check
that actually holds it.

## Bookkeeping the repo's own guards expect

- **`CHANGELOG.md` `[Unreleased]`** gets an entry. A release-gate change and a
  spec version bump are exactly what a reader of 0.4.0's notes needs, and every
  comparable change in this tree has one.
- **`deferred.md` rows are added, not "recorded".** Audit 7's report names *"no
  `deferred.md` row for any release finding"* as itself a finding, and
  `ledgercheck.go` cross-checks a round's coverage claims against that file by
  audit number. R-6 and R-7 (signing, attestations, SBOM) get rows; the repo is
  private, which README says, and that is the reason.

## Other frozen version literals (G2)

`info.version` is not the only one, so the plan's "single shared fix" claim is
narrowed rather than dropped:

| Site | State | Disposition |
|---|---|---|
| `web/src/HealthPage.tsx:91` | `{health?.version ?? "v0.1.0"}` — a hardcoded **v0.1.0 rendered in the UI** whenever `/health` has not resolved | **In scope.** It is R-2's class, user-visible, and a one-line fix: render nothing, or a neutral placeholder, rather than a frozen version |
| `web/package.json:4` | `"version": "0.1.0"`, frozen since creation | Deferred with a row — it is not published to a registry and nothing reads it |
| `README.md:284` | `idea version  # v0.3.0 (a1b2c3d)`, an example that moves each tag | Deferred with a row. `make docs-lint` is the guard family that would own it |

`.goreleaser.yaml`, `internal/buildinfo` and `Makefile` are clean — all derive
from `{{ .Tag }}` or `git describe`.
