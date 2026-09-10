# Releasing

There was no release checklist in this repository until DX audit 7, and the one
instruction that existed — *"Bump `info.version` when you tag"* — lived in
`docs/api-reference-hosting.md`, a document nobody opens at tag time. It was
missed at v0.1.0, v0.2.0 and v0.3.0. Three misses of a one-line manual step is
the argument for writing the step down where it is looked for, and for having a
machine check it.

## What gates a tag

Pushing a `v*` tag runs `.github/workflows/ci.yml`. The `release` job declares
`needs: [compile-no-node, macos, backend, web]`, so a tag publishes nothing
unless all four are green: the eight guards, the Go suite on Linux and both
macOS architectures, `scripts/smoke.sh`, and the web build and unit tests.

It lives in `ci.yml` rather than a workflow of its own because `needs:` resolves
job ids within one workflow file. A separate `release.yml` cannot depend on
`ci.yml`, which is how the old one came to gate a tag on `go vet` and `go test`
on a single OS.

`internal/ciguard` asserts that every job in `ci.yml` either gates the release
or is on an explicit exemption list, so a job added later cannot quietly sit
outside the gate.

## Before you tag

1. **Decide the version.** Pre-1.0, breaking changes go in a minor bump —
   `CHANGELOG.md`, `README.md` and the release notes all state that contract.
   `docs/versioning.md` has the classification rules.
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
release with `checksums.txt`.

## If the release job fails

Nothing has been published — the version check runs before goreleaser, and the
release is a draft regardless. Delete the tag, fix, re-push:

```bash
git push --delete origin vX.Y.Z
git tag -d vX.Y.Z
```

## After

1. Publish the draft release. Keep the "Upgrade from" section and the
   `shasum -a 256 -c checksums.txt` block that v0.2.0 and v0.3.0 carry.
2. Republish the hosted API reference —
   `docs/api-reference-hosting.md` has the procedure. It does not update itself.

## Not automated, on purpose

Signing, attestations and SBOM are recorded as deferred in
`docs/plans/deferred.md`. The repository is private, which `README.md` says.
