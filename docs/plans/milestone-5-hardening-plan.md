# Milestone 5 — Hardening Plan

Seven deferred gaps from the full-platform implementation review. Each section
states the defect, the fix, the files touched, and the verification.

Implementation order is chosen so that coupled items land together and each
commit is independently green:

- **A** — apply atomicity + change-set reconciliation (coupled)
- **B** — project-runtime race
- **C** — secret-catalog completion
- **D** — H3 host-execution sandboxing
- **E** — full M4 cost pricing
- **F** — task-dependency ordering

---

## A. Apply atomicity + change-set reconciliation

### A1. Defect
`ApplyChangeSet` calls `fsdiff.Apply`, which reads each touched file's pre-image
AND writes the new content in one pass, and only returns the pre-image manifest
on full success. The manifest is persisted *after* `fsdiff.Apply` returns, so a
crash or mid-apply failure leaves the user's workspace partially mutated with no
persisted pre-image digest — the change set is stuck in `applying` and the
mutation is unrecoverable.

### A2. Fix
Split the filesystem operation into **prepare** (capture + persist pre-images,
no mutation) and **commit** (mutate). Order in `ApplyChangeSet`:

1. `approved → applying`.
2. `fsdiff.Prepare(root, files, put)` → read every touched path's current bytes,
   persist them via `put`, return the pre-image manifest. **No mutation.**
3. `persistManifest(pre)` → pre-image digest.
4. Append `workspace_mutated` journal row carrying the pre-image digest —
   **before** any byte is written.
5. `fsdiff.Commit(root, files, get)` → write/delete files.
6. `MarkChangeSetApplied(preDigest)` (as today).

`fsdiff.Apply` is kept as a thin wrapper (Prepare + Commit) so existing callers
and tests are unchanged; `ApplyChangeSet` uses the two-phase form directly.

### A3. Change-set reconciliation
Extend `reconcileStaleOperations` (runtime startup) so that, after
`ReconcileJournal` marks `prepared`/`workspace_mutated` rows `recovery_required`,
the runtime:

1. For each change set stuck in `applying` whose latest journal row is
   `workspace_mutated` with a pre-image digest: restore the workspace from that
   digest (via `fsdiff.Restore`) and transition the change set to `reverted`
   (or `conflicted` if restore fails), so the record is terminal and the source
   tree is returned to its pre-apply state.
2. For each change set stuck in `checks_running` (no apply started): transition
   it to `draft` (recoverable; the human can re-execute) rather than leaving it
   permanently `checks_running`.
3. Keep the existing behavior for `draft`/`review_required` (no workspace change).

New store method: `ReconcileChangeSets(ctx)` returns the stuck change sets and
their recovery disposition; the runtime performs the restore + transitions.

### A4. Verification
- Unit test `fsdiff`: Prepare is non-mutating and Commit applies identically to Apply.
- Unit test `ApplyChangeSet`: simulate a Commit failure (missing blob) and assert
  the workspace_mutated journal row with pre-image digest exists before the failure.
- Unit test reconciliation: a change set stuck in `applying` with a
  `workspace_mutated` pre-image is restored + reverted at startup.

---

## B. Project-runtime race

### B1. Defect
`EnsureProject` is check-then-act (unlocks between the map lookup and
construction), and `ReloadProject` deletes the cached entry then re-opens. Two
concurrent first-opens (or a reload racing a live async op) can open two
`*store.DB` handles for one project and run `reconcileStaleOperations` against a
still-running goroutine — marking its operation failed and its initiative
`failed` spuriously.

### B2. Fix
1. **Single-flight construction.** Hold `r.mu` across the whole
   `EnsureProject` construction path (move the double-checked lookup inside the
   locked region), so only one `*projectRuntime` is ever built for a project.
2. **Reconcile only on first open.** Track per-project whether this daemon has
   already reconciled (a `reconciled map[string]bool` under `r.mu`, set after
   the first open). `ReloadProject` clears the cached runtime but keeps the
   `reconciled` flag so it never re-runs stale reconciliation against live ops.
3. **Do not swap the engine while ops are in flight.** `ReloadProject` waits for
   the project's in-flight operations to settle before swapping (track in-flight
   counts in `projectRuntime` via the existing op registry), or — simpler and
   sufficient with (1)+(2) — keeps the same `*projectRuntime` object and only
   rebuilds its `registry`/`cfg` in place. Choose the in-place rebuild: it
   avoids a second DB handle entirely.

### B3. Verification
- Unit test: concurrent `EnsureProject` calls return the same `*projectRuntime`
  and open the DB once.
- Unit test: `ReloadProject` with a running op does not mark the op failed
  (reconciliation runs only once per daemon lifetime).

---

## C. Secret-catalog completion

### C1. Defect
Three gaps: (a) `collectSecrets` only gathers explicitly-configured providers,
not the built-in presets (so a preset-only `GOOGLE_API_KEY=AIza…` is used but
never added to the Redactor's known values); (b) the M3 rejection gate
`security.FindCredential` is pattern-only and never checks known values; (c)
structured model-output fields (requirement statements, decisions,
questions/findings/assertions/task statements, decision discussion, finding
notes) are persisted and re-served unredacted.

### C2. Fix
1. `collectSecrets` also resolves `config.Presets[*].DefaultKeyEnv` through
   `lookupEnv` (deduplicated).
2. Add `Redactor.ContainsCredential(s string) bool` (known values + patterns) and
   make the M3 sweep + `RenderPatch` use the engine's redactor (known values +
   patterns) instead of the pattern-only `security.FindCredential`.
3. Redact structured free-text at the write boundary. Add a small helper on the
   engine — `e.redact(s string) string` — applied to every model/human free-text
   string before it reaches a store `Create*`/`Set*` call (requirements,
   decisions, questions, findings, assertions, task statement/title, decision
   discussion, finding notes). IDs/codes/enums are **not** redacted.

### C3. Verification
- Unit test `collectSecrets` includes preset keys.
- Unit test `ContainsCredential` detects a known value and `AIza…`.
- Canary test: run M1 with a provider key in a model field and assert no
  requirement/decision/finding row contains the key.

---

## D. H3 host-execution sandboxing

### D1. Defect
`checkCommand` runs the config build/test command via `exec.CommandContext("sh",
"-c", cmd)` directly on the host with only a sanitized env — agent-authored code
executes outside any sandbox, bypassing the executor sandbox and trust broker.

### D2. Fix
1. Add `check.image` to `HarnessConfig` (default `alpine:3.20`).
2. `checkCommand` runs the command inside `executor.ContainerSandbox`:
   `docker run --network none --read-only --tmpfs /tmp -v <workRoot>:/work -w
   /work <image> sh -c <cmd>`. The command sees only the work root; no network;
   no host filesystem.
3. **Fail closed:** if Docker is unavailable (launch error), the check is a
   `high` finding (`disposition=rejected`) — never silently fall back to host
   exec. Document that meaningful build/test checks require a toolchain image
   (e.g. `check.image: golang:1.25` or `node:22`).

### D3. Verification
- Unit test `ContainerSandbox` integration (skip when Docker absent): a command
  that writes outside `/work` or `/tmp` fails; a `/work` write succeeds; network
  is blocked.
- Unit test `checkCommand` fails closed when Docker is missing.

---

## E. Full M4 cost pricing

### E1. Defect
`Review.MaxCostUSD` is parsed but never enforced — no provider computes
`CostUSD`, so `max_cost_usd` is dead. `runModel` only enforces `max_tokens`.

### E2. Fix
1. Add a per-model pricing table (USD per 1M input/output tokens) in a new
   `internal/model/pricing.go` with a `CostUSD(model string, in, out int64)
   float64` helper and a default rate for unknown models.
2. Populate `Usage.CostUSD` in each provider's `Complete` (OpenAI, Anthropic,
   Gemini) using the pricing table keyed on the **actual** model returned.
3. Enforce `Review.MaxCostUSD` in `runModel` as a hard stop (mirroring
   `max_tokens`): if `resp.Usage.CostUSD > max_cost_usd`, fail the run.

### E3. Verification
- Unit test `CostUSD` math.
- Unit test a provider sets `CostUSD` from the table.
- Unit test `runModel` fails when cost exceeds `MaxCostUSD`.

---

## F. Task-dependency ordering

### F1. Defect
`nextReadyTask` returns the first `proposed`/`ready` task ignoring `depends_on`
edges, and `TaskReady` is never computed. `runExecutionPlan` also silently drops
a `depends_on` reference to a task listed later (title not yet seen).

### F2. Fix
1. **Resolve deps in two passes.** In `runExecutionPlan`, first create all tasks
   (collecting title→id), then in a second pass write the `depends_on` edges;
   a dependency title that cannot be resolved is a hard error (emit a blocking
   finding) instead of being silently dropped. (Requires a store method to add
   edges after creation, or a refactor of `CreateTask`.)
2. **Compute readiness from the graph.** In `nextReadyTask`, a task is
   executable when its status is `proposed`/`ready` AND every `depends_on`
   predecessor is `done`. Set a task to `ready` once deps are satisfied (or
   compute on the fly — prefer on-the-fly to avoid extra state). Tasks with no
   deps are immediately executable.

### F3. Verification
- Unit test `runExecutionPlan` errors on an unresolved dependency title.
- Unit test `nextReadyTask` does not return a task with an incomplete dependency.

---

## Out of scope (explicitly deferred)
- Provider key forwarding via `--env-file` instead of `-e` (separate hardening).
- Redacting at the store layer rather than the engine (larger refactor; the
  engine-level choke point covers all current write paths).
- A project-scoped mutation lock replacing the per-engine mutex (single-flight
  construction in B makes the per-engine mutex sufficient).

## Definition of done
- All seven items implemented with unit tests.
- `go vet ./...`, `go test ./...`, `scripts/smoke.sh` green.
- Each phase committed; a final adversarial review of the commits; fixes merged
  on green CI.

---

## Plan review corrections (v2)

The adversarial plan review (6 perspectives) rejected the v1 plan. Corrections
adopted below; these supersede the v1 text.

### A — apply atomicity + reconciliation (corrected)
- **Legal transitions only.** Recovery uses: `applying → conflicted` (restore
  succeeded / apply abandoned) and `checks_running → rejected` + reopen task
  (mirrors the human reject path). Add ONE new legal transition `applying →
  approved` for the no-mutation crash window (Prepare never mutated; revert the
  status flip). `reverted` is NOT used for a never-applied change set.
- **Journal sequence.** Keep a `prepared` row (as today) before mutation, then
  `workspace_mutated` with the pre-image digest BEFORE the first write.
- **Reconcile only stuck change sets.** `ReconcileJournal`/`ReconcileChangeSets`
  join on `change_sets.status = 'applying'` (never mark rows of an already
  applied/reverted change set), group by `change_set_id`, and act on the newest
  row only.
- **Recovery branches.**
  1. `applying` + newest row `workspace_mutated` → `fsdiff.Restore` from the
     pre-image digest, then `applying → conflicted`.
  2. `applying` + newest row `prepared`/absent (no bytes written) → `applying →
     approved`.
  3. `checks_running` → `checks_running → rejected` + reopen the task.
- **Immediate rollback on Commit failure.** In `ApplyChangeSet`, if `Commit`
  fails after the pre-image manifest is persisted, best-effort `fsdiff.Restore`
  immediately and transition `applying → conflicted` before returning the error
  (no reliance on next-startup reconcile alone).
- **Ordering.** Open `raw-blobs` in `EnsureProject` BEFORE `reconcileStaleOperations`
  so restore can resolve pre-image digests.

### B — project-runtime race (corrected)
- **Single-flight construction** (hold `r.mu` across construction, double-checked).
- **Reconcile once per daemon lifetime** (`reconciled map[string]bool`).
- **In-place engine reload** via a new `Engine.Reload(cfg, registry, redactor,
  blobs, rawBlobs)` that swaps all engine fields under `e.mu`; `ReloadProject`
  rebuilds config/registry/redactor/blob-stores and calls it (no second DB handle,
  no `delete`+rebuild race).

### C — secret-catalog completion (corrected)
- **Known values** gathered from presets AND explicit providers; nil-safe
  `ContainsCredential` (pattern-only fallback when redactor is nil).
- **Redact at write boundary** for every model/human free-text field, including
  the omitted paths: suggested answers (Question/Answer), initiative
  Title/Description, `DecisionDissent.Position`, and nested fields
  (acceptance-criteria metric/target/condition; finding
  finding/evidence/impact/recommendation/note; decision question/options/
  recommendation).
- **Generic sweep finding** — the secret-sweep finding does NOT echo the matched
  credential value.
- `RenderPatch` already uses the redactor (unchanged); only the M3 sweep changes.

### D — H3 host-execution sandboxing (corrected)
- **Config surface.** Add a `check` object (`image`, `cache_volume`, `network`)
  to `HarnessConfig` AND the embedded `harnessSchema` (or setting it fails
  validation).
- **Toolchain.** `check.image` is REQUIRED whenever `build_cmd`/`test_cmd` is
  set; if unset and a check command exists, config load fails with a clear
  message. No toolchain-less default that silently rejects every change set.
- **Writable caches + network policy.** The sandbox gets writable cache mounts
  (GOCACHE/GOMODCACHE/npm) and a documented network policy (offline/vendored by
  default; opt-in egress for module fetch).
- **Hardening.** `--rm --cap-drop ALL --security-opt no-new-privileges`,
  non-root `--user`, and `--memory/--pids-limit` limits.
- **Fail closed** only when an image IS configured but Docker is unavailable.

### E — cost pricing (corrected)
- **Normalize model names.** Match pricing by longest canonical prefix (strip
  dated suffixes: `gpt-4o-2024-08-06` → `gpt-4o`); unknown models hit an
  explicit default rate.
- **Cumulative budget.** Track per-initiative cumulative `CostUSD` across ALL
  `runModel` calls (and retries); fail once the running total exceeds
  `max_cost_usd`. Per-response check is a secondary guard only.

### F — task-dependency ordering (corrected)
- **Normalize titles** (trim/lowercase/collapse whitespace) before resolving
  `depends_on`; unresolved/duplicate titles become a **blocking finding** routed
  to `needs_human_judgment` (not a raw `InitiativeFailed`).
- **Readiness keyed on "applied", not "done".** A task is executable only when
  every `depends_on` predecessor has an `applied` change set (or no
  dependencies). A rejected upstream re-opens its task so dependents are never
  permanently wedged.

---

## Implementation + commit review (v3)

Implemented in six commits (A–F), each green. A second six-perspective review of
the commits found one blocker (task-dependency deadlock: a checks-rejected
upstream was left "done" and permanently blocked its dependents) plus several
highs. All were fixed in a follow-up commit (`4c444eb`):

- checks/secret-sweep rejection re-opens the task (`proposed`)
- reconciliation only marks `conflicted` after a successful restore; the
  pre-image digest lookup is idempotent across re-runs
- `ReloadProject` reassigns fields under `r.mu` (no torn runtime)
- cost budget is a cumulative per-project ceiling (no per-entry reset, so
  concurrent pipelines cannot zero each other, and Execute/ReviewChangeSet/
  DiscussDecision are budgeted too)
- deterministic-check output, framing assumptions/questions, and the
  unresolved-dependency finding are redacted

### Remaining known follow-ups (non-blocking)
- Container sandbox: `--user`/userns remap, capture-and-kill container on
  timeout, mount the canonical workspace read-only for real build/test.
- `collectSecrets` still gathers only provider keys (not arbitrary `.env`
  secrets like `AWS_SECRET_ACCESS_KEY`).
- Rejected secret-bearing patches leave raw bytes in the redaction-free store
  (no GC).
- `MarkChangeSetApplied` failure between Commit and the status flip is only
  repaired at next startup reconcile.
