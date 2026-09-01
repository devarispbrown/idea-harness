# v1.0 Security-Boundary Review

Final release-gate review, six perspectives scoped to the security boundary.
**Verdict: NOT PASSING** — 1 blocker, 7 high, 6 medium, 20 low.

## Blocker

1. **Symlink traversal in `fsdiff.Snapshot`** (`internal/fsdiff/fsdiff.go`).
   `DirEntry.Info()` follows symlinks, so an agent (which bind-mounts the host
   work root read-write and runs as root) can plant `/work/leak -> ~/.ssh/id_rsa`
   and the host-side snapshot/`persistDiff`/`renderDiffTextFromWorkRoot` copy the
   host file into the raw blob store and the applied patch. Real confidentiality
   breach. **Fix:** `os.Lstat` + reject symlinks/non-regular files in Snapshot
   and in the read paths.

## High

1. Secret-sweep write gate keys only on `hasCredential`, not the union of
   credential + risk-marker scan, so a marker-only secret is persisted then
   served via the diff endpoint.
2. API token is injected into `index.html` unauthenticated; `hostOriginGuard`
   trusts client-controlled Host/Origin, so a LAN/0.0.0.0 bind lets a remote
   client read the token.
3. `SandboxedAgentExecutor` runs as root with default caps and no resource
   limits (unlike `ContainerSandbox`).
4. `SandboxedAgentExecutor` has unrestricted network egress.
5. `forwardProviderKeys` forwards ALL provider keys via `-e NAME=value` (argv
   leak + over-exposure).
6. Apply journal `prepared` row carries `BaseSnapshotDigest` (never persisted),
   so a crash before `workspace_mutated` strands the change set in `applying`.
7. Raw model output leaks through the structured-decode error into logs,
   `failure_reason`, and the event stream (unredacted).

## Medium (representative)

- Credential catalog missing `github_pat_/gho_/ghu_/ghs_/glpat-/AIza/npm_/xoxp-/xoxa-/xoxr-/xoxs-` and `Bearer …`.
- `ContainerExecutor` lacks the hardened sandbox flags.
- `Engine.Reload` / `ReloadProject` field swaps race readers that don't take the mutex.
- Build/test stdout/stderr is not secret-swept before persisting to findings.
- Provider keys visible in docker `ps` argv.

## Disposition

Fix the blocker and the bounded highs; re-run the review before tagging v1.0.
The agent-executor (OMP/Hermes) hardening, token-in-HTML/LAN-bind, and the
concurrency races are tracked for a follow-up pass.
