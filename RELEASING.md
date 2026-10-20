# Releasing

There was no release checklist in this repository until DX audit 7, and the one
instruction that existed — *"Bump `info.version` when you tag"* — lived in
`docs/api-reference-hosting.md`, a document nobody opens at tag time. It was
missed at v0.1.0, v0.2.0 and v0.3.0. Three misses of a one-line manual step is
the argument for writing the step down where it is looked for, and for having a
machine check it.

## What gates a tag

Pushing a `v*` tag runs `.github/workflows/ci.yml`. The `release` job declares
`needs: [compile-no-node, macos, backend, web, e2e, agent-contract]`, so a tag
publishes nothing unless all six are green: the nine guards, the Go suite on
Linux and both macOS architectures, `scripts/smoke.sh`, the web build and unit
tests, the browser suite with its accessibility gate (`make e2e`), and the
agent contract against the pinned Hermes image.

It lives in `ci.yml` rather than a workflow of its own because `needs:` resolves
job ids within one workflow file. A separate `release.yml` cannot depend on
`ci.yml`, which is how the old one came to gate a tag on `go vet` and `go test`
on a single OS.

`internal/ciguard` asserts that every job in `ci.yml` either gates the release
or is on an explicit exemption list, so a job added later cannot quietly sit
outside the gate.

## First public release (runbook)

The one-time path from private → public with a working
`brew install devarispbrown/tap/idea-harness`. Do these in order; each step
points at the ongoing-release mechanics below.

1. **Make the repository public** — Settings → General → Change visibility.
   Do this *before* the first tag, not after: signing a private repository's
   release discloses the repository name on the public Rekor log (see
   [Signing on a private repository](#signing-on-a-private-repository)), and a
   public repo is what makes `go install …@latest` and the tap's download URLs
   work from the first release. Also note `go install` needs the module path to
   be publicly fetchable, which only a public repo provides.

2. **Create the tap** — a public repository `devarispbrown/homebrew-tap`;
   empty is fine. goreleaser will write `Casks/idea-harness.rb` into it.

3. **Grant push access** — a fine-grained personal access token with
   **Contents: read and write** on `devarispbrown/homebrew-tap` only, stored as
   the Actions secret `HOMEBREW_TAP_GITHUB_TOKEN` on
   `devarispbrown/idea-harness`. (`GITHUB_TOKEN` cannot push to another
   repository.) Without it the release still succeeds and the cask push is
   silently skipped.

4. **Cut `v0.7.0`** — [Before you tag](#before-you-tag), then
   [Tagging](#tagging). The release job gates on every guard, signs, records the
   build provenance (now that the repo is public), and pushes the cask to the
   tap.

5. **Publish the draft release** — see [After](#after). The cask's download URLs
   404 until the draft is published, so `brew install` fails for everyone in
   between.

6. **Verify once, end to end** — [Verifying a release](#verifying-a-release),
   then the command a user would run: `brew install devarispbrown/tap/idea-harness`.

The notarization caveat is under [Homebrew](#homebrew); the arm64-Linux test gap
is under [Platforms](#platforms).

## Before you tag

1. **Decide the version.** Pre-1.0, breaking changes go in a minor bump —
   `CHANGELOG.md`, `README.md` and the release notes all state that contract.
   The classification rules, in full:

   | Change | Bump | Goes under |
   |---|---|---|
   | A request or response field removed or renamed; a status code changed; a body closed to a field it used to accept; a CLI flag or verb removed or renamed; a config key removed, renamed, or newly rejected | **minor** (pre-1.0) | `### Changed — BREAKING`, with a Was/Now row and a consumer-impact sentence |
   | A new endpoint, flag, verb, config key, or response field; a new optional request field | **minor** | `### Added` |
   | Behaviour corrected to match what was already documented; an error message improved; a guard added that rejects what was always invalid | **patch** | `### Fixed` |
   | Documentation, comments, tests, CI, or refactoring with no observable change | **patch** | `### Changed`, or omitted if nothing a consumer can see moved |

   Two rules that have caught real mistakes: a guard that starts rejecting a
   configuration that passed yesterday **is** breaking, even though the config
   was always wrong; and "we only added a field" is breaking if the field is
   *required*, or if the body it lands in is closed.
   **Correcting a released section.** `make version-check` compares every
   released `## [X.Y.Z]` section against its content at the newest tag, so a
   correction to already-shipped history is red on every push until the next
   release moves that baseline. This is deliberate — released history is what
   shipped inside every archive — but it means such a correction lands with a
   known-red guard. Make the correction its own commit, say in the message that
   `changelog-history` is expected to fail and why, and cut the release
   promptly. Do not work around it by editing the guard.

2. **Bump `docs/openapi.yaml`'s `info.version`** to that version, in a normal
   commit, merged before the tag. `make version-check` fails on every push while
   it is older than the newest released `CHANGELOG.md` heading, so this is
   usually already done.
3. **Move `## [Unreleased]`** to `## [X.Y.Z] - YYYY-MM-DD` and open a fresh
   `[Unreleased]`. Check that every breaking change is under
   `### Changed — BREAKING` with a Was/Now row and a consumer-impact sentence —
   audit 7 found two filed under `### Fixed`.
4. **`make version-check VERSION=vX.Y.Z`** locally. This is what the release job
   runs first; failing it here costs nothing.
5. **Green on `main`.** The tag inherits nothing from an earlier run.

## Tagging

```bash
git tag -a vX.Y.Z -m "vX.Y.Z"
git push origin vX.Y.Z
```

`.goreleaser.yaml` builds `darwin,linux × amd64,arm64`, stamps the tag and
commit into `internal/buildinfo` through `-ldflags`, and creates a **draft**
release carrying:

| File | What it is |
|---|---|
| `idea-harness_X.Y.Z_<os>_<arch>.tar.gz` | the four archives |
| `idea-harness_X.Y.Z_<os>_<arch>.tar.gz.sbom.json` | an SPDX SBOM per archive, generated by syft |
| `checksums.txt` | SHA-256 of every archive and SBOM |
| `checksums.txt.sigstore.json` | the keyless cosign signature of `checksums.txt`: a Sigstore bundle holding the signature, the short-lived certificate naming this workflow, and the transparency-log proof |

After goreleaser, the job records a GitHub build-provenance attestation for each
archive (`actions/attest-build-provenance`) — on a public repository only; see
[Signing on a private repository](#signing-on-a-private-repository). And
goreleaser pushes `Casks/idea-harness.rb` to `devarispbrown/homebrew-tap` when
the tap token is configured; see [Homebrew](#homebrew).

Signing needs no key and no secret. The release job holds `id-token: write`;
cosign exchanges the workflow's GitHub OIDC token for a short-lived certificate
from Sigstore's Fulcio naming the workflow and the tag, and the signature is
logged in Rekor. `internal/ciguard` fails if the job loses that permission, if
cosign or syft stop being installed before goreleaser, or if `.goreleaser.yaml`
stops signing or producing SBOMs.

There is no separate `release.yml` for any of this, for the reason under
[What gates a tag](#what-gates-a-tag): the signing and attestation steps belong
to the one job that `needs:` every guard.

## If the release job fails

If it failed before goreleaser ran, nothing has been published — the version
check runs first, and the release is a draft regardless. Delete the tag, fix,
re-push:

```bash
git push --delete origin vX.Y.Z
git tag -d vX.Y.Z
```

If it failed at or after goreleaser, also delete the draft release, and revert
the tap commit if one landed (`idea-harness vX.Y.Z` in
`devarispbrown/homebrew-tap`) — the cask would otherwise point at archives that
no longer exist. The Rekor entry for the signature cannot be removed; it is
harmless, because it signs a checksum file nobody will publish.

## After

1. Publish the draft release. Keep the "Upgrade from" section and the
   `shasum -a 256 -c checksums.txt` block that v0.2.0 and v0.3.0 carry.
   Publish promptly once the tap token is configured: the cask is pushed at
   release time, and until the draft is published its download URLs 404, so
   `brew install` fails for everyone in between.
2. Run the [verification](#verifying-a-release) below against the published
   release, once. It is what a user would run, and the only end-to-end check
   that the signature names the identity this document says it does.
3. Republish the hosted API reference —
   `docs/api-reference-hosting.md` has the procedure. It does not update itself.

## Platforms

Releases ship four binaries, with the web UI built in. Windows is not supported: the CLI starts its
daemon in a new session (`internal/cli/detach.go`, `//go:build unix`), which has no Windows
equivalent here yet.

| Platform | Built | Tests run in CI |
|---|---|---|
| `darwin/arm64` | yes | yes (`macos-latest`) |
| `darwin/amd64` | yes | yes (`macos-15-intel`) |
| `linux/amd64` | yes | yes (`ubuntu-latest`) |
| `linux/arm64` | yes | **no** |

`linux/arm64` is the one gap, and it is the SQLite driver rather than first-party code: nothing
here branches on `GOARCH`, but `modernc.org/sqlite` vendors a machine-translated SQLite per
GOOS/GOARCH, so that binary's storage layer is compiled and never exercised. Hosted arm64 Linux
runners are not available on this repository's plan; on a public repository they are, and adding
the job is a two-line change to `.github/workflows/ci.yml`.

Until the repository is public, the canonical install is `git clone` and `make build` (or
`go install ./cmd/idea`, without the UI). `go install …@latest` needs the module path to be
publicly fetchable.

## Verifying a release

With [cosign](https://docs.sigstore.dev/cosign/system_config/installation/) and
the GitHub CLI:

```bash
VERSION=vX.Y.Z
gh release download "$VERSION" -R devarispbrown/idea-harness \
  -p 'checksums.txt*' -p '*_darwin_arm64.tar.gz*'

# 1. checksums.txt was signed by this repository's ci.yml, for this tag.
cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity "https://github.com/devarispbrown/idea-harness/.github/workflows/ci.yml@refs/tags/$VERSION" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt

# 2. The archive and its SBOM are the ones checksums.txt lists.
grep '_darwin_arm64.tar.gz' checksums.txt | shasum -a 256 -c

# 3. Build provenance (public repository only): built by this repository's
#    workflow, from the tagged commit.
gh attestation verify idea-harness_*_darwin_arm64.tar.gz -R devarispbrown/idea-harness
```

`--certificate-identity` is exact on purpose. A regexp such as
`--certificate-identity-regexp 'devarispbrown/idea-harness'` would also accept a
signature made by any workflow in the repository, on any branch.

## Homebrew

```bash
brew install devarispbrown/tap/idea-harness
```

goreleaser writes a cask (`homebrew_casks`, not the deprecated `brews` formula)
to `Casks/idea-harness.rb` in `devarispbrown/homebrew-tap`.

**The cask updates itself — there is no manual Homebrew step.** Every release
runs goreleaser in the `release` job, which regenerates the cask from the new
tag (version, download URL, sha256, homepage) and pushes it to the tap with the
commit message `idea-harness vX.Y.Z`. Tagging a release *is* the trigger; the
only thing a maintainer does is publish the draft. `brew upgrade idea-harness`
then picks the new cask up like any other tap.

Two things have to be true before that command works for anyone:

- **The tap exists and the release job can push to it.** Create the public
  repository `devarispbrown/homebrew-tap` (empty is fine), then a fine-grained
  personal access token with **Contents: read and write** on that repository
  only, stored in this repository as the Actions secret
  `HOMEBREW_TAP_GITHUB_TOKEN`. `GITHUB_TOKEN` cannot push to another
  repository. Without the secret the release still succeeds and the cask push
  is skipped (`skip_upload: auto` in `.goreleaser.yaml`); prerelease tags
  (`v1.0.0-rc1`) are skipped either way.
- **The release archives are downloadable.** The cask fetches them from this
  repository's release, and while this repository is private they 404 for
  anyone without access — Homebrew sends no credentials.

The binary is not Apple-notarized. The cask's post-install hook removes the
quarantine attribute Homebrew applies, or Gatekeeper would refuse to run it; the
signature and checksums above are the integrity check instead.

## Signing on a private repository

Signing runs on every release, private or not, with two consequences worth
knowing before the first signed tag:

- **Rekor is public.** The certificate cosign logs there names the repository,
  the workflow path and the tag. Signing a private repository's release
  therefore discloses that `devarispbrown/idea-harness` exists and that it
  tagged `vX.Y.Z` — not its code, but not nothing. To avoid that until the
  repository is public, release with `args: release --clean --skip=sign` in the
  release job; the checksums are still published.
- **Attestations are skipped.** On GitHub Free, Pro and Team plans artifact
  attestations are only available for public repositories, so the attestation
  step runs only when `github.event.repository.visibility == 'public'`. It
  starts working with the first release after the repository is made public,
  with no change here.
