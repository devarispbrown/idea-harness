# Hosted API Reference Plan — Adversarial Review Record

One reviewer evaluated [`hosted-api-reference-plan.md`](./hosted-api-reference-plan.md) at its
pre-review draft (`889f060`), reading the plan and measuring against the source.

**Verdict: approve_with_changes.** The mechanism survived; six specifics did not.

## Verified correct

Measured rather than argued: the full binary is 35,570,322 B and the same tree with Scalar's chunks
removed is 31,705,618 B — a **3.69 MiB delta, +12.2%**, so the plan's "3.7 MB of a 33.9 MB binary"
is accurate. The build-tag claim holds: `//go:embed all:dist` (`internal/web/web.go:17`) embeds
every matched file regardless of build tags, so conditioning the Vite entry is the correct lever. A
`dist` without Scalar's chunks still builds, and `APIReferencePage()`
(`internal/web/apiref.go:21`) returns `(nil, false)` rather than erroring.

## Blocking findings → resolution

**B1 — The plan asserts two different statuses for `/api-reference`, and the code does a third
thing.** Line 39 wanted a 200 with a fallback page; line 115 wanted it to "explain itself rather
than 404ing"; `internal/daemon/apidocs.go:116` already returns **404 with an explanatory body**, and
`apidocs_test.go:123` accepts either.
**Resolution:** 404 stays, and the plan says so. The page genuinely is not in this binary; answering
200 for a document that does not exist is the worse lie. The body explains and links onward.

**B2 — The fallback page cannot distinguish the two ways it can be reached.** `APIReferencePage()`
returns a bare `bool`, so an `APIDOCS=0` build and a checkout that never ran `make build` are
indistinguishable — and the current text ("built without the web UI", "run `make build`") is false
for the first case, where `make build` is exactly what produced it.
**Resolution:** probe rather than guess. `dist/index.html` present + reference absent means "built
without the reference"; both absent means "UI not built". No build-tag plumbing, and the message is
true in both cases.

**B3 — Vite emits absolute asset paths, so the subpath case breaks before the spec URL matters.**
`web/vite.config.ts` sets no `base`, so the built page references `/assets/…`. At
`example.com/api-docs/` those 404 regardless of how the spec URL is configured.
**Resolution:** the dist build sets `base`. Also recorded: the daemon-served page survives a relative
base only because `GET /api-reference` is registered without a trailing slash
(`apidocs.go:75`) and `/api-reference/` is not, so the document base is `/`. That is load-bearing and
was unstated.

**B4 — `openapi.json` has no source to copy.** There is no `docs/openapi.json`; the JSON is produced
at request time by an unexported `sync.OnceValues` in package `daemon` (`apidocs.go:148`).
**Resolution:** `internal/docsgen` writes it during `make api-docs-dist` — it already parses the YAML,
so the conversion belongs there rather than being duplicated or curled out of a running daemon.

**B5 — Two staleness claims are wrong, and one of them is a real gap.** The spec is 122,850 B, not
"~100 KB". More seriously: `docs/openapi.yaml`'s `info.version: 0.1.0` is a hand-typed literal that
nothing connects to `internal/buildinfo`, and **`make api-check` is not wired into CI at all** — only
`check-embed` and `docs-check` run there.
**Resolution:** wire `api-check` into CI, which is a gap worth closing independently of this plan.
The version link is *not* automated, and the plan now says that plainly instead of implying a guard
exists.

**B6 — The definition of done contradicted the plan's own hedge**, requiring a script for all three
targets while saying Fastly might end up documented instead.
**Resolution:** the DoD is conditional on what Fastly turns out to support.

## Non-blocking findings adopted

- The CORS argument names the weakest of three reasons. `hostOriginGuard`
  (`internal/daemon/server.go:110-121`) **403s any request carrying a non-loopback `Origin`**, so
  adding `Access-Control-Allow-Origin` would not even help; Chrome's Private Network Access gate
  requires a preflight the daemon rejects; and only then does CORS matter. Co-hosting the spec is
  right for stronger reasons than the plan gave.
- A CDN sets no CSP, and `apidocs.go:31-56` leans on `connect-src 'self'` for the property that
  nothing in the bundle can reach off-origin. The dist emits a `_headers` file and documents the
  CloudFront equivalent so the hosted page keeps it.
- `web/dist/assets` holds 90 files and the reference entry alone references 87 dynamic chunks:
  separating Scalar's graph from the SPA's needs parsing, not a glob. Copy `assets/` wholesale and
  accept the extra 240 KB — `index.html`, which carries the token, is not copied.
- `wrangler pages deploy` also needs `CLOUDFLARE_ACCOUNT_ID` and an existing Pages project;
  CloudFront needs `--distribution-id`, `--paths`, and a default-root-object rewrite for a subpath.
- `IDEA_HARNESS_API_REFERENCE_URL` gets escaped into the fallback HTML, not `Sprintf`'d.
- The plan claimed "claims below carry file:line" and contained none. v2 does.
- `scripts/smoke.sh` has no `/openapi.yaml` or `/api-reference` assertions, so M4.1's "asserted
  against a running daemon" is new smoke work the draft did not scope.

## Recorded, not resolved

The reviewer could not verify Fastly's current static-hosting mechanism without network access. The
plan's refusal to assert one stands; the target is written only after checking.
