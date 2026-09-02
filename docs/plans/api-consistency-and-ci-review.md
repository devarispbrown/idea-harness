# API Consistency Plan — Adversarial Review Record

Two independent reviewers evaluated [`api-consistency-and-ci-plan.md`](./api-consistency-and-ci-plan.md)
at its pre-review draft (`cd71771`), each reading the plan *and* the source.

| Perspective | Verdict | Blocking findings |
|---|---|---|
| CTO / principal architect | approve_with_changes | `/next-move` has no UI consumer; three CLI commands depend on the fallback, not the parameter; the SKIP grep is a no-op; the Docker skip list is wrong; the "responses are anonymous" claim is false; the CSP argument is a non-sequitur; `/docs` 404s |
| Senior frontend / platform | approve_with_changes | Scalar's spec fetch would 401; `/docs` unreachable in either layer; CLI fallback call sites uninventoried; CSP blocks Scalar's defaults even bundled |

**Net verdict: approve_with_changes.** The migration set and sequencing survived; three design claims did not.

## What the reviewers confirmed

Stated once so the record is not misread as all-negative. `initiativeFromQuery` has exactly ten call
sites mapping to exactly the ten named handlers; no other handler reads `initiative_id` directly;
`routes.go` has exactly 71 `HandleFunc`s; the UI count of 17 sites across 9 files is exact; no file
branches on `runtime.GOOS`. One reviewer registered all 71 existing patterns plus the 10 proposed
nested aliases plus `/guidance` on a real `http.ServeMux` — **no conflict panic**, so the three-step
sequencing premise holds. `scripts/smoke.sh` is macOS-clean (BSD-safe `sed -i.bak`, no `timeout`, no
GNU-only flags, bash 3.2 compatible) and `go build && go vet && go test` is green on darwin at
`de4dcdb`.

## Blocking findings → resolution

**B1 — The `next-move` rename migrates the wrong client.** The draft said "migrate the UI" and
implied only `/next` has a CLI consumer. `grep -r next-move web/src` returns nothing. The sole
production consumer is the CLI (`internal/cli/state_cmds.go:134`, type `cliNextMove` at `:55`, stub
at `json_flag_test.go:46`). The error came from `docs/api.md:158`, which claims the web UI's Next
Move card consumes it — stale.
**Resolution:** adopted. The rename is a CLI + CLI-test change touching zero UI files, so it is not
subject to the "wait for the UI tree to be quiet" mitigation and can land immediately.

**B2 — There is no `next_move` response field.** `projection.NextMove` marshals to
`{"action","why","affected"}`. The draft's "the response field `next_move` in the UI follows" names
a key that exists nowhere.
**Resolution:** adopted; clause deleted. The wire shape is unchanged by the rename, which makes the
CLI migration a one-line URL edit.

**B3 — Three CLI commands depend on the fallback, not on the parameter.** Six CLI sites pass
`initiative_id` and migrate mechanically (`advance.go:114,116,120`, `change.go:133,270`,
`explain_export.go:129`). Three pass nothing and rely on the server-side fallback:
`cli.go:517` (`idea doctor`'s graph audit), `state_cmds.go:187` (`idea decisions`),
`state_cmds.go:226` (`idea reviews`). Step 3 breaks all three, and `scripts/smoke.sh:93,97` runs two
of them, so the smoke suite goes red.
**Resolution:** adopted, and it is the plan's real cost. Each needs a `resolveInitiative` call, which
adds a `GET /initiatives` round trip per command and changes the empty-project error text. `idea
doctor` is the sharp case: it is a health command and must still report daemon and config status in
a project with no initiatives, so its graph audit is skipped rather than failed there.

**B4 — Removing the fallback retires a test added days ago.**
`internal/daemon/initiative_default_test.go` exists specifically to pin the fallback semantics.
**Resolution:** adopted. The plan now says which behavior is deliberately dropped rather than letting
a test quietly become uncompilable.

**B5 — The SKIP grep is a no-op.** Non-verbose `go test` prints nothing for skipped tests; a
reviewer ran it — `grep -c SKIP` returns 0 whether or not anything skipped. The step would have
reported "no skips" forever, which is exactly the misreading it exists to prevent.
**Resolution:** adopted. `go test -json` filtered for skip actions, with a plain `go test ./...` kept
as the gate.

**B6 — The Docker skip list names the wrong files.** Only `internal/executor/sandbox_test.go` and
`internal/workflow/execute_test.go` are Docker-gated. `container_test.go` is hermetic — it drives a
bash Docker stand-in — and `agent_test.go` has no guard at all.
**Resolution:** adopted. Understating macOS coverage undersells the job and would make the summary
disagree with the plan.

**B7 — "Responses are anonymous inline structs" is false, and it inverts the argument.** 17 anonymous
structs are *request* bodies. Responses overwhelmingly hand named types to `writeJSON`: `domain.*` at
sixteen sites, `projection.Understanding`/`NextMove`, `providersView`, `RiskProfile`,
`buildinfo.Info`. Response schemas are the deep, drift-prone half — and they are the reflectable half.
**Resolution:** adopted as a correction to the *reasoning*, not the decision. The spec stays
hand-written, justified on effort across 71 endpoints rather than on a false claim about the code,
and the plan records that generating response schemas from the named types is the obvious upgrade
path if the spec starts drifting.

**B8 — The CSP argument for bundling Scalar is a non-sequitur.** The CSP header is set only inside
the `index.html` branch of `internal/web/web.go` — it is a per-response header on the token-bearing
document, not an origin-wide policy. A separately served docs page with its own headers would not
relax it.
**Resolution:** adopted. Offline-first is the real argument for bundling; the CSP claim is restated
as what it actually is.

**B9 — `/docs` cannot work in either layer, and Scalar's spec fetch would 401.** The app is
hash-routed (`web/src/router.ts` strips `^#`) and `internal/web/web.go` has no SPA history fallback,
so `GET /docs` hits the embedded `FileServer` and 404s. Separately, `internal/daemon/server.go:79-99`
requires `X-Idea-Token` on everything under `/api/` except `/api/v1/health` and the SSE `token=`
param, so Scalar fetching `/api/v1/openapi.yaml` renders a blank page with a silent 401. Adding a
catch-all fallback is worse than it looks: `index.html` is served `no-store` **with the token
injected**, so a fallback would hand the token to every mistyped URL.
**Resolution:** adopted, and M3's serving design is replaced. The reference is a separate
daemon-served page at `/api-reference`, outside `/api/` — which needs no change to the auth boundary,
because the auth middleware only guards `/api/`, and the spec is strictly less sensitive than `/`,
which already serves the token unauthenticated. `/docs` was also the third meaning of "docs" in this
product (`idea docs`, the context-doc API, the Explore tab).

**B10 — CSP details Scalar would trip even when bundled.** `font-src 'self'` blocks its default web
fonts; `connect-src 'self'` blocks `proxy.scalar.com`, so "Test Request" fails silently; `worker-src`
inherits `default-src 'self'`, blocking blob workers. And try-it would 401 anyway without the token.
**Resolution:** adopted. The page is configured with default fonts off, the proxy disabled and try-it
hidden, and the plan says so rather than discovering it in a browser.

## Non-blocking findings adopted

- `dist/` is gitignored and only the 1.8K placeholder is tracked, so the draft's framing about "the
  240KB bundle the project deliberately does not commit" was a non-issue. The real cost is that
  `@scalar/api-reference` is a **Vue 3** app: a second framework runtime. Separate entry point, not
  mounted in the React SPA.
- `docs/api.md` is invalidated by M2 and **nothing catches it**: `docs-check` only regenerates
  `docs/cli.md`, and `api-check` compares the spec to `routes.go`. Rewriting it is now an explicit
  M2 work item. `design_handoff_idea_harness_ui/README.md:123` also references `next-move`.
- `internal/cli/detach.go` is already `//go:build unix` — the Windows gap is a missing
  `detach_windows.go` and the failure is `undefined: sysProcAttrDetach`, not the `syscall` reference
  the draft described.
- `web/src/api.ts` exposes `get/post/put` over a caller-built path string with **no URL helper**, so
  all 17 sites hand-roll template literals. Adding a `resource(projectId, initiativeId, name)`
  builder is what stops the 18th site from re-introducing the drift, and it makes the migration a
  one-file change before the call sites are touched.
- 16 of 17 sites interpolate the initiative id **unencoded**. Moving it from query position to path
  position turns a harmless string into a path segment `fetch()` will normalize — `..` included.
  Encode all of them.
- `scripts/smoke.sh:5` forces `GOPATH`/`GOCACHE`/`GOMODCACHE` into the repo, defeating
  `actions/setup-go` caching and re-downloading the module graph on a runner billed at 10×.

## Disagreement recorded

The architect notes generating response schemas from named types is feasible and would kill the
drift class; the plan keeps the spec hand-written for now. That is a judgment call about effort, not
a factual dispute, and the plan says so explicitly rather than implying generation is impossible —
which is what the draft did wrong.
