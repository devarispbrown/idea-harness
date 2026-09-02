# API Consistency, macOS CI, and an OpenAPI Surface — Plan

Three pieces of the DX audit backlog that were deferred at v0.1.0, taken now because the window
is open: the repository is private, `v0.1.0` is an unpublished draft, and there are no external
consumers. Every breaking change here costs approximately nothing today and becomes a migration
project the moment someone depends on the API.

Claims below carry file:line from `de4dcdb` and were verified before writing.

## Order and why

1. **M1 — macOS CI.** First, because it finds real bugs. It already did: asking what a macOS leg
   would catch turned up a live control-plane bypass (`.IDEA-HARNESS/harness.yaml` was accepted and
   written on a case-insensitive filesystem), fixed in `de4dcdb`. No file in this repo branches on
   `runtime.GOOS`, so the darwin/linux differences that matter are filesystem semantics — exactly
   where the path guard and the change-set deny live.
2. **M2 — route unification + `next-move` rename.** Breaking, and cheapest now.
3. **M3 — OpenAPI + Scalar.** After M2, so the spec describes the shape we intend to keep rather
   than one we are about to change.

---

## Track M1 — macOS CI

**What it buys.** `internal/security/fsutil.go` resolves symlinks specifically because of macOS's
`/var` → `/private/var`; `internal/fsdiff` rejects symlinks via `Lstat`; `isControlPlane` compares
paths. All three are security-relevant and all three behave differently on a case-insensitive,
symlink-heavy filesystem. Linux CI cannot see any of it.

**What it does not buy, stated so a green job is not misread.** These skip without a Docker daemon,
and GitHub's macOS runners have none: `internal/executor/{agent,sandbox,container}_test.go` and
`internal/workflow/execute_test.go`.

Work items:

1. A `macos` job on `macos-latest`: `go build ./... && go vet ./... && go test ./...`.
2. The job prints which packages skipped and why, so "green on macOS" is never read as "the sandbox
   tests passed on macOS". A step that greps the test output for `SKIP` and echoes a summary is
   enough; it must not fail on skips.
3. `scripts/smoke.sh` runs there too — it is bash and already ran on darwin during the audit.
4. State platform support in `README.md`: macOS and Linux are tested; **Windows is not supported** —
   `internal/cli/detach.go:9` uses `syscall.SysProcAttr{Setsid: true}`, which does not exist there.
   Say it rather than leaving a reader to discover it at build time.

**Test:** the job itself. It must pass on `de4dcdb` before anything in M2 lands, so a failure later
is attributable to M2.

**Cost:** macOS runners bill at 10× Linux minutes. The suite is ~15s, so this is minutes per month,
not a budget question — but keep the job to build/vet/test/smoke, not the whole matrix.

---

## Track M2 — one addressing convention, and an honest name for `next-move`

### The actual migration set is 10 endpoints, not 34

`grep initiativeFromQuery internal/daemon/handlers.go` resolves to exactly ten handlers:
`handleListRequirements`, `handleListAssertions`, `handleListArtifacts`, `handleListDecisions`,
`handleListQuestions`, `handleListFindings`, `handleListChangeSets`, `handleListSuggestedAnswers`,
`handleRisk`, `handleDoctor`.

Everything else under `/projects/{projectId}/` that looked flat in the audit is **not**
initiative-scoped and must not move:

- `providers`, `models`, `executors`, `seen` — project-scoped by nature.
- `change-sets/{id}/…` (`review`, `apply`, `revert`, `diff`, `checks`, `resolve-conflict`),
  `artifacts/{id}/…`, `decisions/{id}/…`, `questions/{id}/…`, `findings/{id}/…` — addressed by
  their own id, which is already unambiguous.

Conflating these is how "34 inconsistent routes" turns into a rewrite nobody wants. The real change
is ten list endpoints.

### The target shape

`GET /api/v1/projects/{projectId}/initiatives/{id}/{resource}` for all ten, matching `tasks`, `why`,
`impact`, `trace`, `diff` which already nest.

### Sequencing, so nothing is broken between commits

1. **Add** the nested routes alongside the existing flat ones. Both call the same handler; the
   handler takes the initiative id from the path when present, falling back to the query. No
   behavior change, nothing to migrate yet.
2. **Migrate `web/src`** — 17 call sites across 9 files (`App.tsx`, `BuildPage.tsx`, `DefinePage.tsx`
   ×5, `ExplorePage.tsx`, `HealthPage.tsx`, `PlanPage.tsx`, `ProductHome.tsx`, `VerifyPage.tsx`,
   `DecisionPage.tsx`) — and the CLI's flat call sites, to the nested form.
3. **Remove** the flat routes and the query fallback. One breaking release, one changelog entry.

Steps 1–2 are non-breaking and can land separately; only step 3 breaks, and it breaks a client we
ship in the same commit.

### `next` vs `next-move`

Verified difference: `/next` (`workflow.Engine.NextAction`, `internal/workflow/next.go:12-49`)
returns a **machine-executable command token** from a closed set (`advance`, `execute`,
`review CHG-n`, `apply CHG-n`, `verify`, `resolve`, `done`, `wait`) and is change-set aware.
`/next-move` (`projection.BuildNextMove`, `internal/projection/projection.go:268-307`) returns
**English display copy** with `why` and `affected`, is change-set blind, and can legitimately
disagree with `/next` — a `plan_approved` initiative with an approved change set gets `apply CHG-1`
from one and "Execution plan approved" from the other.

They are not variants of one thing, and the names say they are. Rename:

- `/next` stays `/next` — it is the actionable one and the CLI depends on it.
- `/next-move` becomes **`/guidance`**, which says what it is. `handleNextMove` →
  `handleGuidance`; `projection.BuildNextMove` → `projection.BuildGuidance`; the response field
  `next_move` in the UI follows.

Same three-step sequencing: add `/guidance` as an alias, migrate the UI, remove `/next-move`.

**Tests:** a table asserting every nested route answers identically to its flat predecessor while
both exist (step 1); after step 3, a test asserting the flat routes are gone (404) so the removal
cannot silently regress; the existing daemon tests cover the handlers themselves.

**Risk.** The UI is being actively rebuilt on `main` — 18 commits touching `web/` in the two weeks
before v0.1.0. Step 2 touches 9 UI files and will conflict if that work is mid-flight. Mitigation:
land step 1 immediately (backend only, zero UI files), then do step 2 as one focused commit at a
moment the UI tree is quiet, and step 3 right behind it.

---

## Track M3 — OpenAPI spec, served through Scalar

### Generated or hand-written: neither, exactly

Full generation from source is not available cheaply: request and response bodies are declared as
**anonymous inline structs inside the handlers** (e.g. `handleCreateInitiative`,
`internal/daemon/handlers.go:80-89`), so there is no named type to reflect over. Hand-writing 71
endpoints guarantees drift the first time someone adds a route.

The plan splits the difference, the same way `docs/cli.md` already works:

1. **Hand-write** `docs/openapi.yaml` — paths, methods, parameters, the `{"error": …}` shape, the
   auth scheme, and response schemas for the resources that matter most (initiatives, change sets,
   requirements, operations). Schema depth is a judgment call per endpoint; breadth is not.
2. **A CI drift check** (`make api-check`) that parses `internal/daemon/routes.go`, extracts every
   `method + path`, and fails if the spec is missing one or documents one that does not exist. That
   catches the drift that actually happens — a new route nobody documented — without pretending to
   verify schemas.
3. Extend `internal/docsgen` rather than adding a second generator; it already walks the repo and
   is wired into CI.

### Serving it — and the CSP constraint that decides how

`internal/web/web.go` sets `script-src 'self'` on the UI response, so **Scalar cannot be loaded from
a CDN**; the browser would block it silently. That is the right default for a page carrying the
per-install API token, and it should not be relaxed for documentation.

So Scalar is bundled, not linked: add `@scalar/api-reference` to `web/package.json`, mount it at a
`/docs` route in the existing React app pointed at a spec the daemon serves. This keeps the product
offline-first, which is the whole premise of a local-first tool.

Work items:

1. `GET /api/v1/openapi.yaml` (and `.json`) serving the embedded spec — embed it with `//go:embed`
   into `internal/daemon` so a binary is self-describing with no filesystem dependency.
2. `@scalar/api-reference` bundled into `web/`, mounted at `/docs`, reading that endpoint.
3. `make api-check` in CI.
4. Link it from `docs/api.md` (which stays as the prose explanation — the spec is a reference, not a
   narrative) and from the README documentation list.

**Note on scope:** the spec documents the API *after* M2, so `/guidance` and the nested routes are
what it describes. Writing it before M2 means writing it twice.

**Test:** `make api-check` fails when a route is added to `routes.go` and not to the spec — proven
by adding a throwaway route in the test and asserting the check goes red.

---

## Out of scope

An SDK. The integration surface is `--json` plus this HTTP API, both now solid; a client library for
a loopback-bound, token-authenticated local daemon is not obviously wanted by anyone, and if it ever
is, M3's spec is what generates it. `docs/api.md` should say this explicitly so its absence reads as
a decision rather than an omission.

Windows support. `detach.go` would need a build-tagged implementation; nobody has asked.

## Definition of done

- macOS CI green on `de4dcdb`, with skipped packages named in the job output.
- All ten list endpoints answer on the nested path; the flat forms and `/next-move` are gone; the UI
  and CLI use the new shapes; `go test ./...`, `scripts/smoke.sh`, `make docs-check`, `make
  check-embed` green.
- `docs/openapi.yaml` covers every route in `routes.go`, `make api-check` proves it, and `/docs`
  renders Scalar from the embedded spec with no network access.
- README states the supported platforms.
- CHANGELOG carries one entry for the breaking API change with the before/after paths.
- This plan adversarially reviewed before implementation (CONTRIBUTING §14), with blocking findings
  resolved in the text.
