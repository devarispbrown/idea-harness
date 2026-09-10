# Hosting the API reference

The daemon bundles a rendered API reference at `http://127.0.0.1:8787/api-reference`, and that is
the default because documentation which needs the network is not offline-first. It costs **3.69 MiB
of a 33.99 MiB binary** — measured, not estimated:

```text
make build              35,639,810 bytes (33.99 MiB)
APIDOCS=0 make build    31,775,106 bytes (30.30 MiB)
```

If you would rather not carry that in every binary, build without it and host the reference
somewhere else.

## What you keep either way

The **spec is always embedded**. Only the renderer is optional:

```text
GET /openapi.yaml     always present
GET /openapi.json     always present (converted from the YAML at request time)
GET /api-reference    only when built with the renderer
```

That split is the point. `docs/openapi.yaml` is a plain text file and it is what any other viewer
consumes — a hosted Scalar, Redocly, `curl | yq`, a generated client. A binary that cannot describe
itself would be the worse trade.

## Building without the renderer

```bash
APIDOCS=0 make build
```

`/api-reference` then returns **404 with a body that explains why**, links the spec this binary does
serve, and — if you set it — links your hosted copy:

```bash
export IDEA_HARNESS_API_REFERENCE_URL=https://api-docs.example.com/
```

The 404 is deliberate. The document genuinely is not in that binary, and answering 200 for a page
that does not exist would be the worse lie.

> The mechanism is the Vite entry list, not a Go build tag. `internal/web` embeds `dist/` with
> `//go:embed all:dist`, and embed patterns are compile-time literals — no build tag can strip
> individual files back out of a live `embed.FS`, so the assets have to not be built.

## Producing the static bundle

```bash
make api-docs-dist
```

```text
dist-api-docs/
  index.html        the reference page
  assets/           its chunks
  openapi.yaml      the spec, co-hosted
  openapi.json
  _headers          CSP and caching, for hosts that read this format
```

Two properties this build has that the default one does not:

- **Relative asset URLs.** The default build emits `/assets/…`, correct at a site root and 404
  everywhere else; this one uses `./assets/…`, so `example.com/api-docs/` works.
- **The spec travels with the page.** It is *not* fetched from your daemon, and could not be: the
  daemon's `hostOriginGuard` rejects any request carrying a non-loopback `Origin` with 403, so a
  public page could not read it even if CORS headers were added — and Chrome's Private Network
  Access gate would require a preflight the daemon also rejects. Co-hosting is also what lets the
  page work with no daemon running at all.

## Publishing

```bash
scripts/publish-api-docs.sh <cloudflare|aws|fastly> [--dry-run]
```

Every credential is read from the environment, none is printed, and `--dry-run` shows the exact
command before you run it for real.

### Cloudflare Pages

```bash
export CLOUDFLARE_API_TOKEN=…   CLOUDFLARE_ACCOUNT_ID=…   CF_PAGES_PROJECT=idea-harness-api-docs
wrangler pages project create "$CF_PAGES_PROJECT"     # once
scripts/publish-api-docs.sh cloudflare
```

Pages reads `_headers` from the bundle, so the CSP and caching rules apply with no further setup.

### AWS: S3 + CloudFront

```bash
export API_DOCS_S3_BUCKET=my-api-docs   API_DOCS_CLOUDFRONT_ID=E123…
export API_DOCS_S3_PREFIX=api-docs/     # optional, for a subpath deploy
scripts/publish-api-docs.sh aws
```

Syncs with `--delete` and invalidates the distribution.

**Route53 is DNS, not hosting** — it can point a name at a CloudFront distribution and nothing more.
The script prints the alias-record command rather than running it, because writing records into your
hosted zone is not a documentation script's business. CloudFront also does not serve `index.html`
for a directory URL on its own: set a default root object, or attach a CloudFront Function that
rewrites `/api-docs/` to `/api-docs/index.html`. `_headers` is a Pages/Netlify format that CloudFront
ignores — reproduce it as a response-headers policy, using the file as the source of truth for what
the policy should say.

### Fastly

Fastly is genuinely different from the other two, and the script does not pretend otherwise. There is
no "deploy this directory" command: static sites are served by a Compute app backed by a KV Store,
scaffolded once by [`@fastly/compute-js-static-publish`](https://github.com/fastly/compute-js-static-publish),
which generates a project you keep (it holds the service id).

```bash
npx @fastly/compute-js-static-publish@latest \
    --root-dir=./dist-api-docs --kv-store-name=idea-harness-api-docs
cd <generated-project> && npm run fastly:deploy      # once
```

After that:

```bash
export FASTLY_API_TOKEN=…   FASTLY_STATIC_PROJECT=/path/to/generated-project
scripts/publish-api-docs.sh fastly
```

The script copies the freshly built bundle into the project's content directory and runs
`npm run fastly:publish`. If the project is not there yet, it prints the scaffold command and stops
rather than guessing.

## Staleness — read this before you publish

**Nothing keeps a hosted reference in step with an installed binary, and no check will tell you.**

`docs/openapi.yaml`'s `info.version` is a hand-typed literal. `internal/buildinfo` takes its version
from `git describe` through `-ldflags`. The two are still unconnected, but `info.version` is no
longer unguarded — that sentence used to say *"no guard reads `info.version` at all"*, and it was
true for three releases, during which the literal never moved off `0.1.0`.

`make version-check` now reads it, on two axes:

- **Every push**, in CI: `info.version` must not be older than the newest released heading in
  `CHANGELOG.md`. This never predicts the next version; it refuses a spec that describes a release
  already superseded.
- **At tag time**, as the release job's first step: `info.version` must equal the tag. It runs
  before goreleaser, so a mismatch costs a deleted tag rather than a published release.

**The bump moves at merge, not at tag.** The old instruction here was *"Bump `info.version` when you
tag"*, and three consecutive tags missed it. Bumping in the commit that opens a version's work is
what makes the every-push check passable, and `RELEASING.md` carries the sequence.

Publishing the hosted page is still a manual step. Republish after you release, and treat a hosted
page as describing whatever was last pushed to it rather than whatever your users are running.
