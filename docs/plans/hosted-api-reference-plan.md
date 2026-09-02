# Hosting the API Reference Off-Binary — Plan (v2, post-review)

The bundled API reference costs **3.69 MiB of a 33.9 MiB binary (+12.2%)** — measured, not
estimated: the same tree with Scalar's chunks removed from `internal/web/dist` links at
31,705,618 B against 35,570,322 B. Keeping it bundled is the right default for a local-first tool,
but dropping it and hosting the reference elsewhere should be one command.

**v2 supersedes the pre-review draft (`889f060`);** see
[`hosted-api-reference-review.md`](./hosted-api-reference-review.md). Six specifics were wrong,
including two the draft asserted in two contradictory places.

## The split that makes this cheap

The weight is **Scalar's Vue application**, not the API description. `docs/openapi.yaml` is 122,850 B.

| Artifact | Size | Bundled |
|---|---|---|
| `docs/openapi.yaml` (+ generated `.json`) | 120 KiB | **always** — a binary that cannot describe itself is a worse trade |
| Scalar renderer at `/api-reference` | 3.69 MiB | optional |

A binary built without the renderer still serves the spec, which is what any other viewer consumes.

## M4.1 — Build without the renderer

**Mechanism: the Vite entry, not a Go build tag.** `internal/web/web.go:17` is `//go:embed all:dist`,
which embeds every matched file regardless of build tags — embed patterns are compile-time literals
and the linker cannot strip individual files out of a live `embed.FS`. The assets have to not be
built.

1. `web/vite.config.ts` reads its entry list from the environment, so `APIDOCS=0 make build`
   produces a `dist/` with no Scalar chunks. `make build` keeps them; nobody gets a smaller binary
   by accident.
2. **`/api-reference` keeps returning 404** in such a build, with an explanatory body —
   `internal/daemon/apidocs.go:116` already does this. The draft asked for 200 in one place and
   "not 404" in another; both were wrong. The page genuinely is not in this binary, and answering
   200 for a document that does not exist is the worse lie.
3. **The body must be true in both cases it can be reached.** `internal/web/apiref.go:21` returns a
   bare `(nil, false)`, which cannot distinguish an `APIDOCS=0` build from a checkout that never ran
   `make build` — and today's text ("built without the web UI", "run `make build`") is false for the
   first, where `make build` is exactly what produced it. Probe instead: `dist/index.html` present
   and the reference absent means *built without the reference*; both absent means *UI not built*.
4. `IDEA_HARNESS_API_REFERENCE_URL`, when set, is linked from that page — an env var like
   `IDEA_HARNESS_STATE` and `IDEA_HARNESS_ADDR`, because this is a daemon concern, not project
   config. It is **escaped into the HTML**, not `Sprintf`'d.

**Test:** `scripts/smoke.sh` gains assertions for `/openapi.yaml` and `/api-reference` — it has none
today, so this is new work the draft did not scope. Plus a daemon test for the fallback body.

## M4.2 — `make api-docs-dist`

```text
dist-api-docs/
  index.html          # the reference page, renamed from api-reference.html
  assets/…            # copied wholesale, see below
  openapi.yaml
  openapi.json
  _headers            # CSP for Cloudflare Pages; CloudFront equivalent documented
```

Four things the draft got wrong or omitted:

- **`base` must be set for this build.** `web/vite.config.ts` sets no `base`, so the built page
  references `/assets/…` absolutely; at `example.com/api-docs/` those 404 before the spec URL is
  ever read. The subpath case the draft used to justify a configurable spec URL breaks on assets
  first. (The daemon-served page survives a relative base only because `GET /api-reference` is
  registered without a trailing slash at `apidocs.go:75` and `/api-reference/` is not — so the
  document base is `/`. Load-bearing, and previously unstated.)
- **`assets/` is copied wholesale.** It holds 90 files and the reference entry alone references 87
  dynamic chunks, so separating Scalar's graph from the SPA's needs parsing, not a glob. The cost is
  240 KB of SPA bundle on the CDN. `index.html` — which carries the injected token — is not copied.
- **`openapi.json` has no file to copy.** There is no `docs/openapi.json`; the JSON is produced at
  request time by an unexported `sync.OnceValues` in package `daemon` (`apidocs.go:148`).
  `internal/docsgen` writes it during this target — it already parses the YAML.
- **A CDN sets no CSP,** and `apidocs.go:31-56` leans on `connect-src 'self'` for the property that
  nothing in the bundle reaches off-origin. The dist ships `_headers` so Cloudflare Pages keeps that
  property, and documents the CloudFront response-headers-policy equivalent.

**The spec is co-hosted, and for stronger reasons than the draft gave.** A hosted page cannot read
the local daemon's spec because `hostOriginGuard` (`internal/daemon/server.go:110-121`) **403s any
request carrying a non-loopback `Origin`** — adding `Access-Control-Allow-Origin` would not help.
Chrome's Private Network Access gate also requires a preflight the daemon rejects. CORS is the
weakest of the three reasons. Co-hosting also makes the page work with no daemon running.

**Test:** the output is served by a throwaway static server and renders with the network blocked.

## M4.3 — Publishing

`scripts/publish-api-docs.sh <target>` over that directory. Each target is thin: the vendor CLI does
the work; the script fails clearly when it is missing and never carries credentials.

| Target | Runs | Needs |
|---|---|---|
| `cloudflare` | `wrangler pages deploy` | `wrangler`, `CLOUDFLARE_API_TOKEN`, `CLOUDFLARE_ACCOUNT_ID`, an existing Pages project |
| `aws` | `aws s3 sync` + `aws cloudfront create-invalidation --distribution-id --paths` | `aws`, bucket + distribution; a subpath deploy needs a default-root-object rewrite |
| `fastly` | to be determined — see below | `fastly` |

**Route53 is DNS, not hosting.** On AWS the pair is S3 + CloudFront; Route53 optionally holds the
alias record. The script prints the Route53 command rather than running it, because creating DNS
records in someone's hosted zone is not a docs script's business.

**Fastly is checked before it is written.** Fastly has no first-class static hosting the way
Cloudflare Pages does; the candidates are Compute with `@fastly/compute-js-static-publish` or Fastly
fronting an origin bucket. The reviewer could not confirm which is current without network access.
If the honest answer is "front an origin", the `fastly` target is **documented configuration, not a
deploy command** — and the definition of done reflects whichever it turns out to be.

**Credentials come from the environment only,** none is echoed, and `--dry-run` prints the command it
would run.

## M4.4 — Documentation

`docs/api-reference-hosting.md`: why you would host it, what you lose, the targets, and staleness.

On staleness, plainly: `docs/openapi.yaml:17` is `version: 0.1.0`, a hand-typed literal that nothing
connects to `internal/buildinfo`, whose version comes from `git describe` via `-ldflags`. **No check
enforces the link** — `make api-check` compares paths only. So publishing is a manual release step,
and the doc says that rather than implying a guard.

## Adopted regardless of this plan

**`make api-check` is not wired into CI** — only `check-embed` and `docs-check` run there. The drift
check that has already caught three endpoints added by another session runs only when someone
remembers. That gets fixed here.

## Out of scope

Publishing on release (needs a destination, which is the user's decision); hosting the UI off-binary
(it carries the per-install token and is meaningless without a daemon); vendor accounts and domains.

## Definition of done

- `APIDOCS=0 make build` produces a binary without Scalar that still serves `/openapi.yaml`;
  `/api-reference` returns 404 with a body true to how it was built. Binary delta recorded.
- `make api-docs-dist` renders offline from a static server, at both site root and a subpath.
- `scripts/publish-api-docs.sh` supports `cloudflare` and `aws` as deploys, `fastly` as whichever of
  deploy-or-documented-configuration checking establishes, fails clearly without the vendor CLI,
  reads credentials only from the environment, and has `--dry-run`.
- `docs/api-reference-hosting.md` written and linked from README and `docs/api.md`.
- `make api-check` runs in CI.
- `go test ./...`, `scripts/smoke.sh` (with its new assertions), `make api-check`, `make docs-check`,
  `make check-embed` green.
