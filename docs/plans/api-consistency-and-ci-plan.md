# API Consistency, macOS CI, and an OpenAPI Surface — Plan (v2, post-review)

Three pieces of the DX audit backlog deferred at v0.1.0, taken now because the window is open: the
repository is private, `v0.1.0` is an unpublished draft, and there are no external consumers. Every
breaking change here costs approximately nothing today and becomes a migration project the moment
someone depends on the API.

**v2 supersedes the pre-review draft (`cd71771`).** Two reviewers found the migration set and
sequencing sound and three design claims wrong; see
[`api-consistency-and-ci-review.md`](./api-consistency-and-ci-review.md). Claims below carry
file:line from `24f50ec` and were re-verified after review.

## Order and why

1. **M1 — macOS CI.** First, because it finds real bugs. It already did: asking what a macOS leg
   would catch turned up a live control-plane bypass — `.IDEA-HARNESS/harness.yaml` was accepted and
   written on a case-insensitive filesystem — fixed in `de4dcdb`. No file branches on
   `runtime.GOOS`, so the darwin/linux differences that matter are filesystem semantics, which is
   exactly where the path guard and the change-set deny live.
2. **M2 — route unification + `next-move` rename.** Breaking, and cheapest now.
3. **M3 — OpenAPI + Scalar.** After M2, so the spec describes the shape we intend to keep.

---

## Track M1 — macOS CI

**What it buys.** `internal/security/fsutil.go` resolves symlinks specifically because of macOS's
`/var` → `/private/var`; `internal/fsdiff` rejects symlinks via `Lstat`; `isControlPlane` compares
paths case-insensitively *because* of what macOS does. All three are security-relevant and invisible
to Linux CI.

**What it does not buy.** Two files skip without a Docker daemon, and GitHub's macOS runners have
none: `internal/executor/sandbox_test.go` and `internal/workflow/execute_test.go`. That is the whole
list — `container_test.go` drives a bash Docker stand-in and `agent_test.go` has no guard, so both
genuinely run on macOS. (The draft named four files; two of them were wrong, which would have
undersold the job.)

Work items:

1. A `macos` job on `macos-latest`: `go build ./... && go vet ./... && go test ./...`.
2. **A skip summary that works.** Non-verbose `go test` prints nothing for skipped tests, so the
   draft's `grep SKIP` would have reported "no skips" on every run. Use `go test -json ./...`
   filtered for `.Action == "skip"` to print what skipped and why, and keep a plain `go test ./...`
   as the gate. The suite gets noisier, not slower.
3. `scripts/smoke.sh` runs there too — verified macOS-clean (BSD-safe `sed -i.bak`, no `timeout`, no
   GNU-only flags, bash 3.2 compatible).
4. **Do not let the smoke script defeat the runner cache.** `scripts/smoke.sh:5` forces
   `GOPATH`/`GOCACHE`/`GOMODCACHE` into the repo, so `actions/setup-go`'s cache is bypassed and the
   module graph is re-downloaded — on a runner billed at 10× Linux. Either let the script honor
   pre-set values or accept the cost knowingly; do not discover it in the bill.
5. State platform support in `README.md`: macOS and Linux are tested; **Windows is unsupported**.
   `internal/cli/detach.go:1` is already `//go:build unix`; the gap is a missing `detach_windows.go`
   and the failure is `undefined: sysProcAttrDetach`.

**Test:** the job itself, green on `24f50ec` before any M2 work lands, so a later failure is
attributable to M2.

---

## Track M2 — one addressing convention, and an honest name for guidance

### The migration set is 10 endpoints

`initiativeFromQuery` has exactly ten call sites: `handleListRequirements`, `handleListAssertions`,
`handleListArtifacts`, `handleListDecisions`, `handleListQuestions`, `handleListFindings`,
`handleListChangeSets`, `handleListSuggestedAnswers`, `handleRisk`, `handleDoctor`. No other handler
reads `initiative_id` directly.

Everything else flat under `/projects/{projectId}/` stays: `providers`, `models`, `executors`,
`seen` are project-scoped; `change-sets/{id}/…`, `artifacts/{id}/…`, `decisions/{id}/…`,
`questions/{id}/…`, `findings/{id}/…` are addressed by their own id. Conflating these turns a
two-day change into a rewrite.

**Target:** `GET /api/v1/projects/{projectId}/initiatives/{id}/{resource}` for all ten, matching
`tasks`, `why`, `impact`, `trace`, `diff`. Verified on a real `http.ServeMux`: registering all 71
existing patterns plus the 10 nested aliases plus `/guidance` produces **no conflict panic**.

### Sequencing

1. **Add** the nested routes alongside the flat ones, same handler, initiative id from the path when
   present and the query otherwise. Non-breaking.
2. **Migrate clients.** Start with `web/src/api.ts`: it exposes `get/post/put` over a caller-built
   path with **no URL helper**, so all 17 call sites hand-roll template literals. Add a
   `resource(projectId, initiativeId, name)` builder there first — that is what stops the 18th call
   site from re-introducing the drift — then convert the 17 sites across 9 files. **Encode the
   initiative id**: 16 of 17 sites interpolate it unencoded today, and moving it from query position
   to path position turns a harmless string into a path segment `fetch()` will normalize, `..`
   included.
3. **Remove** the flat routes and the query fallback. One breaking release, one changelog entry.

### The CLI migration is the real cost, and it is not mechanical

Six CLI sites pass `initiative_id` and convert directly (`advance.go:114,116,120`,
`change.go:133,270`, `explain_export.go:129`). **Three pass nothing** and depend on the server-side
fallback:

| Site | Command | After step 3 |
|---|---|---|
| `cli.go:517` | `idea doctor` graph audit | needs an initiative it may not have |
| `state_cmds.go:187` | `idea decisions` | needs `resolveInitiative` |
| `state_cmds.go:226` | `idea reviews` | needs `resolveInitiative` |

`scripts/smoke.sh:93,97` runs two of them, so a botched migration turns the smoke suite red — after
the fact. Each gains a `GET /initiatives` round trip and a changed empty-project error
(`no initiatives in project` → `no initiatives yet; run \`idea new\` first`), which belongs in the
changelog entry.

**`idea doctor` is the sharp case.** It is a health command: in a project with no initiatives it must
still report daemon and config status. Its graph audit is therefore **skipped, not failed**, when no
initiative resolves — the audit is one of doctor's checks, not its purpose.

`decisions` and `reviews` gain an optional initiative positional, consistent with `tasks` and
`changes`. Their usage strings change, so `make docs-check` will flag it — regenerate.

### Guidance, not `next-move`

Verified: `/next` (`workflow.Engine.NextAction`) returns a **machine-executable token** from a closed
set and is change-set aware. `/next-move` (`projection.BuildNextMove`) returns **English display
copy** with `why` and `affected`, is change-set blind, and legitimately disagrees with `/next`.

`/next` keeps its name. `/next-move` becomes `/guidance`; `handleNextMove` → `handleGuidance`;
`projection.BuildNextMove` → `BuildGuidance`.

**This is a CLI-only migration.** `grep -r next-move web/src` returns nothing — the sole production
consumer is `internal/cli/state_cmds.go:134` (type `cliNextMove` at `:55`, stub at
`json_flag_test.go:46`). The draft said "migrate the UI", which was wrong, and the error came from
`docs/api.md:158` claiming the UI's Next Move card consumes it. The wire shape
(`{"action","why","affected"}`) is unchanged, so the CLI change is a one-line URL edit plus
identifier renames. It touches zero UI files and is **not** subject to the conflict mitigation below.

### Deliberately dropped

`internal/daemon/initiative_default_test.go` pins the fallback semantics fixed in `de4dcdb`. Step 3
removes the fallback, so that test is retired with the behavior it describes — named here so it does
not quietly become uncompilable.

### Docs that M2 invalidates and no check catches

`docs/api.md`'s addressing table, its `/next` vs `/next-move` section (`:115-160`) and ~10 endpoint
rows (`:212-270`) all describe the pre-M2 shape. `make docs-check` only regenerates `docs/cli.md`;
M3's `make api-check` compares the spec to `routes.go`. **Rewriting `docs/api.md` is an M2 work
item.** `design_handoff_idea_harness_ui/README.md:123` also references `next-move`.

### Conflict risk

Step 2's UI half touches 9 files. The `api.ts` helper lands first as a one-file change; the call-site
conversion follows and can be redone cheaply if it conflicts with in-flight UI work.

**Tests:** while both shapes exist, a table asserting each nested route answers identically to its
flat predecessor; after step 3, a test asserting the flat routes 404 so the removal cannot silently
regress.

---

## Track M3 — OpenAPI spec, served through Scalar

### Hand-written spec, route-coverage check

Request bodies are 17 anonymous inline structs, so there is nothing to reflect over for inputs.
**Responses are the opposite** — `writeJSON` is handed named types (`domain.*` at sixteen sites,
`projection.Understanding`/`NextMove`, `providersView`, `RiskProfile`, `buildinfo.Info`), so response
schemas *are* generatable. The draft claimed otherwise and used that claim to justify hand-writing;
the claim was false and the justification is now the honest one: hand-writing 71 endpoints once is
less work than building a Go-type→JSON-Schema generator, and generating response schemas from the
named types is the documented upgrade path if the spec starts drifting.

1. Hand-write `docs/openapi.yaml`: paths, methods, parameters, the `{"error": …}` shape, the auth
   scheme, and response schemas for the resources that matter (initiatives, change sets,
   requirements, operations).
2. `make api-check` parses `internal/daemon/routes.go`, extracts every `method + path`, and fails on
   a route missing from the spec or a spec path that does not exist. That catches the drift that
   actually happens without pretending to verify schemas. Extend `internal/docsgen`.

### Serving it

Two constraints, both from review:

- **Anything under `/api/` requires the token.** `internal/daemon/server.go:79-99` exempts only
  `/api/v1/health` and the SSE `token=` param, so Scalar fetching `/api/v1/openapi.yaml` would
  render a blank page on a silent 401.
- **`/docs` is unreachable and the name is taken.** The app is hash-routed and `internal/web/web.go`
  has no SPA history fallback, so `GET /docs` 404s. Adding a catch-all is worse than it looks:
  `index.html` is served `no-store` **with the token injected**, so a fallback hands the token to
  every mistyped URL. And `docs` already means context docs in three places.

So the reference is served **outside `/api/`**, as its own page:

1. `GET /openapi.yaml` and `/openapi.json` serve the spec, embedded with `//go:embed` so a binary is
   self-describing. No auth-boundary change is needed — the middleware only guards `/api/` — and the
   spec is strictly less sensitive than `/`, which already serves the token unauthenticated.
2. `GET /api-reference` serves a standalone page. **Not** mounted in the React SPA:
   `@scalar/api-reference` is a Vue 3 application, and mounting it would pull a second framework
   runtime into a React bundle. It gets its own Vite entry point, code-split from the app.
3. Configure it for this CSP and this auth model: `withDefaultFonts: false` (`font-src 'self'`
   blocks its Google Fonts load), the request proxy disabled and try-it hidden (`connect-src 'self'`
   blocks `proxy.scalar.com`, and a try-it call would 401 without the token anyway). Bundling is
   chosen for **offline-first**, not for CSP: the CSP header is set only on the token-bearing
   `index.html` response, so a separate page's headers would not relax it.
4. Link from `docs/api.md` (prose explanation) and the README.

**Test:** `make api-check` goes red when a route is added to `routes.go` and not to the spec, proven
by adding a throwaway route and asserting it fails.

---

## Out of scope

An SDK. The integration surface is `--json` plus this HTTP API; if one is ever wanted, M3's spec
generates it. `docs/api.md` should say so, so the absence reads as a decision.

Windows support. Needs `detach_windows.go`; nobody has asked.

## Definition of done

- macOS CI green, with skipped packages named via `go test -json` (not a grep that cannot see them).
- All ten list endpoints answer on the nested path; flat forms and `/next-move` are gone; CLI and UI
  migrated, including the three fallback-dependent commands and `doctor`'s skip-not-fail behavior.
- `docs/api.md` rewritten; `docs/openapi.yaml` covers every route in `routes.go`; `make api-check`
  proves it; `/api-reference` renders with no network access.
- README states supported platforms.
- CHANGELOG carries one breaking entry with before/after paths and the changed CLI error text.
- `go test ./...`, `scripts/smoke.sh`, `make docs-check`, `make check-embed`, `make api-check` green.
