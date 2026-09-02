# Hosting the API Reference Off-Binary — Plan

The bundled API reference costs **3.7 MB of a 33.9 MB binary** (+12%). Keeping it bundled is the
right default for a local-first tool — documentation that needs the network is not offline-first —
but it should be possible to drop it and host the reference somewhere else, and that path should be
one command rather than a research project.

Claims below carry file:line from `eac3ab3` and were verified before writing.

## The split that makes this cheap

The 3.7 MB is **Scalar's Vue application**, not the API description. `docs/openapi.yaml` is ~100 KB
of text. So the two are separated rather than removed together:

| Artifact | Size | Bundled |
|---|---|---|
| `docs/openapi.yaml` (+ `.json`) | ~100 KB | **always** — a binary that cannot describe itself is a worse trade than 100 KB |
| Scalar renderer at `/api-reference` | 3.7 MB | optional |

A binary built without the renderer still serves the spec at `/openapi.yaml`, which is what any
other viewer — hosted Scalar, Redocly, `curl | yq` — consumes.

## M4.1 — Build without the renderer

**Mechanism: the Vite entry, not a Go build tag.** The weight is in generated assets that
`internal/web` embeds wholesale (`//go:embed all:dist`), and embed patterns are static — a build tag
cannot exclude *part* of an embedded directory. The assets have to not be built.

1. `web/vite.config.ts` takes the `api-reference` entry from an env var, so
   `APIDOCS=0 make build` produces a `dist/` with no Scalar chunks.
2. `internal/web/apiref.go` already returns `(page []byte, ok bool)` — absence is a case it was
   written for. `internal/daemon/apidocs.go` serves a small built-in page when `ok` is false: it
   states the renderer was not built in, links to `IDEA_HARNESS_API_REFERENCE_URL` when that is set,
   and always links to the spec this binary does serve.
3. `IDEA_HARNESS_API_REFERENCE_URL` is an env var, matching `IDEA_HARNESS_STATE` and
   `IDEA_HARNESS_ADDR` — this is a daemon-level concern, not project config in `harness.yaml`.
4. `make build` keeps the renderer. Nobody gets a smaller binary by accident.

**Test:** a build with `APIDOCS=0` serves `/openapi.yaml` 200 and `/api-reference` 200 with the
fallback page; the default build serves the real reference. Both asserted against a running daemon,
because "the asset is absent" is exactly the case a unit test mocks away.

## M4.2 — `make api-docs-dist`

One command producing a directory that any static host can serve:

```text
dist-api-docs/
  index.html          # the reference page, renamed from api-reference.html
  assets/…            # Scalar's chunks
  openapi.yaml
  openapi.json
```

Two things this has to get right:

- **The spec must be co-hosted.** `web/src/api-reference.ts` fetches `/openapi.yaml`. If the hosted
  page fetched the *daemon's* spec instead, the browser would block it: the daemon sets no CORS
  headers, and adding them to a loopback-bound daemon so a public page can read it is a worse idea
  than copying a 100 KB file. Co-hosting also makes the page work with no daemon running at all.
- **The spec URL is build-time configurable.** The page hardcodes `/openapi.yaml`, which resolves to
  the site root — correct at `docs.example.com/`, wrong at `example.com/api-docs/`. A
  `API_REFERENCE_SPEC_URL` env var (default `./openapi.yaml`, relative so a subpath works) is read
  by the entry point.

**Test:** `make api-docs-dist` output is served by a throwaway static server and asserted to render
without reaching the network — the same offline property the bundled version has.

## M4.3 — Publishing

`scripts/publish-api-docs.sh <target>` over the directory from M4.2. Each target is thin: the
vendor's own CLI does the work, and the script's job is to fail clearly when it is missing, never to
carry credentials.

| Target | What it runs | Prerequisite |
|---|---|---|
| `cloudflare` | `wrangler pages deploy` | `wrangler`, `CLOUDFLARE_API_TOKEN` |
| `aws` | `aws s3 sync` + `aws cloudfront create-invalidation` | `aws`, a bucket + distribution id |
| `fastly` | see below | `fastly` |

**Route53 is DNS, not hosting.** It cannot serve these files. On AWS the pair is S3 + CloudFront,
with Route53 optionally holding the alias record that points a custom domain at the distribution —
so the script's `aws` target does S3 + CloudFront and prints the Route53 alias command rather than
running it, because creating DNS records in someone's hosted zone is not something a docs script
should do unprompted.

**Fastly needs verification before it is written.** Fastly has no first-class static-file hosting the
way Cloudflare Pages does; the options are Compute with `@fastly/compute-js-static-publish`, or
Fastly fronting an origin bucket. The reviewer should confirm which is current — this plan does not
assert a mechanism it has not checked, and if the honest answer is "front an origin", the `fastly`
target documents that configuration instead of pretending to be a one-command deploy.

**Credentials come from the environment only.** No token is read from a file the repo could ever
contain, none is echoed, and `--dry-run` prints the command it would run so the first use is not a
surprise.

## M4.4 — Documentation

`docs/api-reference-hosting.md`: why you would host it (binary size), what you lose (offline docs),
the three targets, and the staleness problem — a hosted reference describes whatever version was
last published, so the page footer and the spec's `info.version` must name it, and publishing
belongs in the release flow rather than in someone's memory. Linked from `docs/api.md` and README.

## Out of scope

- Publishing the reference automatically on release. The plan makes it one command; wiring it into
  `release.yml` needs a place to publish to, which is the user's decision, not this plan's.
- Hosting the *UI* off-binary. Different problem: the UI carries the per-install token and is
  meaningless without a daemon.
- Vendor accounts, domains, or TLS. The scripts assume a target that already exists.

## Definition of done

- `APIDOCS=0 make build` produces a binary without Scalar; it still serves `/openapi.yaml`, and
  `/api-reference` explains itself rather than 404ing. Binary size difference recorded.
- `make api-docs-dist` produces a directory that renders offline from a static server.
- `scripts/publish-api-docs.sh` supports the three targets, fails clearly without the vendor CLI,
  reads credentials only from the environment, and has a `--dry-run`.
- `docs/api-reference-hosting.md` written and linked; README and `docs/api.md` updated.
- `go test ./...`, `scripts/smoke.sh`, `make api-check`, `make docs-check`, `make check-embed` green.
- Adversarially reviewed before implementation, blocking findings resolved in the text.
