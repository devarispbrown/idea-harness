#!/usr/bin/env bash
#
# Publish the static API reference produced by `make api-docs-dist`.
#
# Each target is thin on purpose: the vendor's own CLI does the work, this
# script's job is to fail clearly when it is missing and to never carry a
# credential. Every token is read from the environment; none is printed, and
# --dry-run shows the exact command that would run so the first real use is not
# a surprise.
#
#   scripts/publish-api-docs.sh cloudflare [--dry-run]
#   scripts/publish-api-docs.sh aws        [--dry-run]
#   scripts/publish-api-docs.sh fastly     [--dry-run]
#
# See docs/api-reference-hosting.md for what each target needs.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIST="${API_DOCS_DIST:-$ROOT/dist-api-docs}"
DRY_RUN=0
TARGET=""

usage() {
  sed -n '3,17p' "$0" | sed 's/^# \{0,1\}//'
  exit "${1:-1}"
}

for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    -h|--help) usage 0 ;;
    cloudflare|aws|fastly) TARGET="$arg" ;;
    *) echo "unknown argument: $arg" >&2; usage ;;
  esac
done
[ -n "$TARGET" ] || { echo "no target given" >&2; usage; }

die() { echo "error: $*" >&2; exit 1; }

need_cli() { # need_cli <command> <install hint>
  command -v "$1" >/dev/null 2>&1 || die "$1 is not installed. $2"
}

need_env() { # need_env <var> <why>
  [ -n "${!1:-}" ] || die "$1 is not set. $2"
}

run() {
  if [ "$DRY_RUN" = "1" ]; then
    printf 'would run:'
    printf ' %q' "$@"
    printf '\n'
    return 0
  fi
  "$@"
}

[ -d "$DIST" ] || die "$DIST does not exist. Run: make api-docs-dist"
[ -f "$DIST/index.html" ] || die "$DIST has no index.html. Run: make api-docs-dist"
[ -f "$DIST/openapi.yaml" ] || die "$DIST has no openapi.yaml. Run: make api-docs-dist"

case "$TARGET" in
  cloudflare)
    # Cloudflare Pages. The project must already exist — `wrangler pages deploy`
    # prompts to create one interactively, which is not something a script
    # should answer on your behalf.
    need_cli wrangler "See https://developers.cloudflare.com/workers/wrangler/install-and-update/"
    need_env CLOUDFLARE_API_TOKEN "wrangler authenticates with it non-interactively."
    need_env CLOUDFLARE_ACCOUNT_ID "wrangler needs it when several accounts are reachable."
    need_env CF_PAGES_PROJECT "The Pages project to deploy into; create it once with: wrangler pages project create <name>"
    run wrangler pages deploy "$DIST" --project-name "$CF_PAGES_PROJECT"
    ;;

  aws)
    # S3 holds the files, CloudFront serves them. Route53 is DNS and cannot
    # host anything — if a custom domain is wanted, the alias record is printed
    # below rather than created, because writing records into someone's hosted
    # zone is not a docs script's business.
    need_cli aws "See https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html"
    need_env API_DOCS_S3_BUCKET "The bucket to sync into, e.g. my-api-docs."
    need_env API_DOCS_CLOUDFRONT_ID "The distribution to invalidate after the sync."

    # --delete so a removed asset does not linger and get served from a stale
    # index; the hashed filenames make that otherwise invisible.
    run aws s3 sync "$DIST" "s3://$API_DOCS_S3_BUCKET/${API_DOCS_S3_PREFIX:-}" --delete
    run aws cloudfront create-invalidation \
      --distribution-id "$API_DOCS_CLOUDFRONT_ID" \
      --paths "/${API_DOCS_S3_PREFIX:+${API_DOCS_S3_PREFIX%/}/}*"

    cat <<'NOTE'

Note: S3 + CloudFront serve the files; Route53 only points a name at them.
If you want a custom domain, create the alias record yourself:

  aws route53 change-resource-record-sets --hosted-zone-id <ZONE> \
    --change-batch '{"Changes":[{"Action":"UPSERT","ResourceRecordSet":{
      "Name":"api-docs.example.com","Type":"A",
      "AliasTarget":{"HostedZoneId":"Z2FDTNDATAQYW2",
        "DNSName":"<distribution>.cloudfront.net","EvaluateTargetHealth":false}}}]}'

Z2FDTNDATAQYW2 is CloudFront's fixed hosted-zone id, the same for every
distribution. A deploy under a subpath also needs the distribution to serve
index.html for the directory — set a default root object or a CloudFront
Function; see docs/api-reference-hosting.md.
NOTE
    ;;

  fastly)
    # Fastly is genuinely different from the other two and this script does not
    # pretend otherwise. There is no "deploy this directory" command: static
    # sites are served by a Compute app backed by a KV Store, scaffolded once by
    # @fastly/compute-js-static-publish, which writes its own project (with the
    # service id in fastly.toml) that you keep. After that one-time setup,
    # publishing new content is a single command from that project.
    need_cli fastly "See https://www.fastly.com/documentation/reference/tools/cli/"
    need_env FASTLY_API_TOKEN "The Fastly CLI and the publisher both authenticate with it."
    need_env FASTLY_STATIC_PROJECT "Path to the Compute project scaffolded for these docs (see below)."

    if [ ! -f "$FASTLY_STATIC_PROJECT/fastly.toml" ]; then
      cat <<NOTE >&2
error: $FASTLY_STATIC_PROJECT is not a Fastly Compute project (no fastly.toml).

Fastly has no one-shot static deploy. Scaffold the project once:

  npx @fastly/compute-js-static-publish@latest \\
      --root-dir=$DIST --kv-store-name=idea-harness-api-docs

then from that project directory:

  npm run fastly:deploy     # once, to create the Compute service
  npm run fastly:publish    # each time the content changes

Keep the generated directory (it holds the service id) and point
FASTLY_STATIC_PROJECT at it.
NOTE
      exit 1
    fi

    # The publisher reads the content directory from its own config, so the
    # freshly built dist is copied into place first.
    run rsync -a --delete "$DIST/" "$FASTLY_STATIC_PROJECT/public/"
    ( cd "$FASTLY_STATIC_PROJECT" && run npm run fastly:publish )
    ;;
esac

[ "$DRY_RUN" = "1" ] && echo "(dry run: nothing was published)"
exit 0
