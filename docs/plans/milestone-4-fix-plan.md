# M4 Implementation — Fix Plan

Ordered by severity and dependency (each fix = one commit, verified before the next).

## P0 — C1/C2: redaction/digest mismatch (data corruption)

**Problem:** `blob.Store.Put` redacts content before hashing, but `fsdiff.Snapshot` hashes raw bytes.
So `FileChange.Digest` (raw) ≠ store key (redacted). Secret-bearing files get rejected or — on revert —
restored as `[REDACTED]`.

**Fix:** give the diff/apply/revert path a redaction-free content store. The daemon creates a second
`rawBlobs` store (no redactor); the engine uses `rawBlobs` for file content, patch manifests, and
pre-image snapshots. The redacted store stays for model artifacts (secrets never reach the artifact
store).

**Files:** `internal/blob/blob.go` (no change needed — just use `nil` redactor), `internal/daemon/runtime.go`
(create raw store), `internal/workflow/engine.go` (`rawBlobs` field), `internal/workflow/execute.go`
(rewire the diff path). **Test:** a redacting store + a secret-bearing file → apply→revert byte-for-byte.

## P1 — H1: gate deadlocks on removed requirements

**Fix:** `VerifyInitiative`'s all-verified loop skips `RequirementRemoved` (match `Coverage`).
**Files:** `internal/workflow/verify.go`. **Test:** superseded requirement + a verified one → complete.

## P1 — H2: crash-recovery journal

**Fix:** `ApplyChangeSet` writes `prepared` before mutating and `workspace_mutated` after; startup
reconciliation calls `ReconcileJournal`.
**Files:** `internal/store/changeset.go`, `internal/workflow/execute.go`, `internal/daemon/runtime.go`.

## P1 — H3: sandbox the build/test checks

**Fix:** `checkCommand` runs `build_cmd`/`test_cmd` inside `ContainerSandbox` (or a bounded timeout),
not host `sh -c`.
**Files:** `internal/workflow/execute.go`, `internal/executor/sandbox.go`.

## Medium

- **M1 real secret detector** — reuse `security` credential patterns as a rejection signal (not
  whole-word scan). `internal/risk/risk.go`, `internal/workflow/execute.go`.
- **M2 revert-clears-verified** — `RevertChangeSet` resets `verified`→`approved` + moves initiative
  out of `complete`. `internal/workflow/execute.go`, `verify.go`.
- **M3 cancel route/CLI + orphan reaping** — persist attempt identity, `idea cancel`. 
- **M4 cost recording/enforcement** — record usage (or explicit "unknown"), enforce `max_cost_usd`.
- **M5 stale-finding disposition** — failed build/test findings get `Disposition: rejected`.

## Low (if time)

UI verify step, `next` loop, execute mutex, patch-applies strength, file permissions, config error
text, `human:human` prefix.

## Verification per commit

`go vet ./...` + `go test ./...` + `bash scripts/smoke.sh` → push → CI green, then next.
