# Milestone 3 Engineering Execution Plan — Plan → Safe Reviewed ChangeSet

**Milestone 3 = Plan → safe reviewed ChangeSet** (PRD v1.1 §50; Technical Design §36–§41,
§48–§52).

M2 proved `PRD → reviewed execution plan` (Design → Execution Plan with the Product Graph and
traceability). M3 extends that slice forward: given an approved execution plan, the system executes
one Task in a sandboxed worker, captures the result as a **ChangeSet**, runs deterministic checks,
gets **independent review**, and **safely applies** it to the canonical workspace through a durable
journal — without the executor ever touching the canonical tree directly.

## 0. M2 tail work (folded into the M3 kickoff)

Two refinements deferred from M2 are completed first, before M3's new surface area:

- **T-0a — Orchestrator synthesis for multi-role decisions.** M2 recorded per-role dissents but did
  not run the orchestrator merge. Add a `Convergence` orchestrator call that reads multi-role
  decisions (with dissents) and emits a single synthesized recommendation (the highlighted default
  in the decision card).
- **T-0b — `doctor` graph-edge audit.** Add a standalone missing-edge check to `idea doctor`: every
  Task has an `implements` edge, every Requirement/Assertion/Artifact has a `produced_by` edge, and
  the task dependency graph is acyclic.

## 1. Goals

1. **Sandbox + capability broker + executor abstraction.** A worker executes in a sandbox
   (`container` default, `local_restricted` for CI/demo); the capability broker mediates actions;
   the executor interface is `internal` / `local_external` / `remote_external`.
2. **ChangeSet is the only source mutation.** Every source change is a `ChangeSet` (patch digest +
   base snapshot), reviewed independently, and applied through the journal (§36–§38).
3. **Crash-safe apply.** The `ApplyJournal` makes apply atomic-in-effect even though filesystem +
   SQLite cannot commit atomically: `prepared → workspace_mutated → committed → rolled_back →
   recovery_required`, with startup reconciliation (§39).
4. **Deterministic checks before review.** Compiler/test/lint/type/schema checks run before the
   human/AI review gate (§4.7, §32).
5. **Threat model + risk tiering.** A ThreatModel is produced and approved at the start of M3
   (§68); a deterministic `RiskProfile` (low/medium/high/critical) drives review ceremony (§31).
6. **Revert.** Exact rollback before acceptance; inverse ChangeSet after later changes; never
   restore an old snapshot over newer user work (§40).

## 2. Non-Goals (explicitly out of scope for M3)

- **Verification evidence + the final gate** (acceptance/security/UX verification, requirement
  coverage) — Milestone 4.
- **Generalized executor ecosystem / remote fleet / OMP generalization** — M3 validates the
  executor interface with **one** deterministic `internal` executor plus an OMP smoke path
  (`local_external`); no plugin marketplace.
- **Git as a first-class feature** — Git is *optional* and treated as a capability; no automatic
  `git init/remote/push` (§41).
- **Embeddings / semantic retrieval / numeric risk scores** (tiers only, no numeric engine).
- **OS keychain, enterprise RBAC, multi-user, SaaS.**

## 3. Requirements in scope (from the constitution)

| Source | Requirement |
|---|---|
| §36–§40 | ChangeSet primitive + state machine + ApplyJournal + Revert; canonical workspace writes serialized; user edits never silently overwritten. |
| §48–§52 | Executor interface + trust tiers; sandbox (OS blast radius) vs capability broker (semantic governance); policy controls intent, sandbox controls containment. |
| §30–§31 | Threat model before design convergence when triggered; RiskProfile tiers with deterministic minimum signals. |
| §4.5, §6 | Every ChangeSet receives independent review; authors cannot self-review; reviews may vary in depth but cannot disappear. |
| §4.7 | Deterministic evidence (compiler/test/lint) preferred over LLM judgment. |
| §50 | Executor progress (heartbeat/phase/action); never expose chain-of-thought. |
| §26 | Crash-safe writes via the apply journal; startup reconciliation of incomplete journals. |
| §37 | Workers never receive the canonical workspace as a writable execution root. |
| §52–§53 | Sandbox modes (container/local_restricted/host_approved); network disabled/enabled. |

## 4. Workflow — the M3 slice

Triggered from an `approved` (M2) execution plan:

```text
execution plan (approved)
      ↓  (pick next ready Task, risk → ceremony)
Threat Model (if high-risk)   ← produced & approved at M3 start
      ↓
Execution (sandboxed worker)
      ↓
ChangeSet (patch + base snapshot)
      ↓
Deterministic checks (build/test/lint)
      ↓
ChangeSet review (adversarial; ≤3 cycles → human judgment)
      ↓
Apply (ApplyJournal)  →  applied
```

- **Execution** (`executor`, stage `implementation`): the executor, in an isolated worktree/copy,
  implements the Task and emits a patch.
- **ChangeSet review** (`adversarial_reviewer`): structured Findings against the ChangeSet
  (correctness, scope, security, tests present); blocking findings → `changes_requested` or
  `rejected`.
- **Apply** (`ApplyJournal`): serialize the canonical workspace write, verify base, snapshot,
  apply, verify hashes, mark committed.

### ChangeSet state machine (formal transition matrix in code + docs)

`draft → checks_running → review_required | rejected` · `review_required → changes_requested →
superseded | approved` · `approved → applying → applied | conflicted` (§38).

## 5. Data model — schema additions

Append-only migrations (current: 8).

```text
change_sets
  id, human_id, initiative_id, task_id, base_snapshot_id, status,
  patch_digest, review_id, git_commit_sha, created_by_run_id, created_at

apply_journal
  id, change_set_id, state, workspace_snapshot_digest, applied_at, created_at
  -- state ∈ {prepared, workspace_mutated, committed, rolled_back, recovery_required}

execution_attempts
  id, change_set_id, executor, status, progress_json, result_digest, created_at

risk_profiles
  id, initiative_id, tier, signals_json, reason_json, created_at

executors (config/registry, not per-row)
  name, trust (internal|local_external|remote_external), sandbox_mode, capabilities_json

snapshots (workspace snapshots)
  id, initiative_id, kind (worktree|copy), digest, created_at
```

`patch_digest`, `result_digest`, and `workspace_snapshot_digest` are content-addressed BlobStore
digests (§31) — never inline. All writes go through the existing `db.Apply` (atomic state+event).

## 6. Sandbox, capability broker, executor

- **Sandbox** — OS blast-radius control. Modes: `container` (default, Docker), `local_restricted`
  (CI/demo), `host_approved` (explicit, high-risk). **M3 ships `local_restricted` as the tested
  path**; `container` is a smoke path (adversarial review B1).
- **Capability broker** — semantic governance for mediated actions. Tools/executors declare
  capabilities; the deterministic engine authorizes; **models cannot grant capabilities** (B3).
- **Executor** — `internal` (deterministic, in-process) ships first for the demo/CI slice; OMP is a
  stub behind the same interface (B2). The interface: `Name / Capabilities / Start / Status /
  Cancel / Collect`. Progress emits `heartbeat/phase/action`, never chain-of-thought.

## 7. ApplyJournal & Revert

- Apply sequence (from §39): lock canonical workspace → verify base → create rollback snapshot →
  persist `prepared` → apply patch → verify hashes → persist `workspace_mutated` → transactionally
  mark `applied` + event + journal `committed` → release lock → GC rollback later.
- Startup reconciliation: incomplete journals → `recovery_required`, surfaced, never silently
  dropped.
- Revert: exact rollback before acceptance; inverse ChangeSet after later changes (§40).

## 8. Threat model & RiskProfile

- **Threat model** produced and approved at the start of M3 (§68); high-risk initiatives trigger a
  `ThreatModel` stage before design convergence. Security-derived concerns become ordinary
  Requirements.
- **RiskProfile**: deterministic security signals establish a minimum tier; semantic analysis may
  raise it. Tier drives review breadth/ceremony (§4.6). No numeric scores.

## 9. API endpoints

```text
POST /projects/{pid}/initiatives/{id}/execute           # pick next task → run executor → ChangeSet
GET  /projects/{pid}/change-sets                        # list (initiative-scoped)
GET  /projects/{pid}/change-sets/{id}                   # detail (status, patch, review)
POST /projects/{pid}/change-sets/{id}/review            # resolve/request-changes/approve/reject
POST /projects/{pid}/change-sets/{id}/apply             # apply via ApplyJournal
POST /projects/{pid}/change-sets/{id}/revert            # exact rollback (pre-acceptance)
GET  /projects/{pid}/change-sets/{id}/diff              # rendered patch
GET  /projects/{pid}/executors                          # list + capabilities
GET  /projects/{pid}/risk                              # RiskProfile
```

## 10. CLI commands

```text
idea execute                 # run the next ready task → ChangeSet
idea changes [--json]        # list ChangeSets
idea change <id>             # detail + diff
idea apply <id>              # apply a reviewed ChangeSet
idea revert <id>             # revert
idea risk [--json]           # RiskProfile
idea executors               # list executors + capabilities
```

## 11. UI — screens/components

- **Changes tab** — the ChangeSet list with status chips (draft/checks/review/approved/applying/
  applied/conflicted), each with **View diff**, **Approve**, **Request changes**, **Reject**.
- **ChangeSet detail modal** — rendered diff, deterministic-check results, review findings, and
  executor progress (Planning/Editing/Testing/Waiting/Reviewing/Finished, no chain-of-thought).
- **Executor/Runtime card** — the risk tier and active sandbox mode.
- The existing calm tabbed design + stepper extends with a **Build** step (`… Design → Plan →
  Build`).

## 12. Role contracts (new)

- `security_architect.yaml` — threat modeling, risk tiering, security Requirements.
- `executor_supervisor.yaml` — task→ChangeSet orchestration, deterministic checks, apply
  coordination.
- Reuse `adversarial_reviewer` for ChangeSet review; add a ChangeSet rubric.

## 13. Review & human-in-the-loop

- Every ChangeSet is **independently** reviewed (author ≠ reviewer); `EvaluateApproval` reuses the
  M1 bar (no open critical/high findings).
- Human dispositions: Approve / Request changes / Reject (mapped onto `approved` /
  `changes_requested` / `rejected`); Apply is a separate explicit action.
- Risk tier determines review depth (higher tier → broader review + more deterministic checks).

## 14. Security

- Canonical workspace is never a writable execution root for workers (§37).
- Models cannot grant capabilities; capabilities are declared and authorized by deterministic code
  (§6.8, §6.10).
- Apply is serialized; user edits are never silently overwritten (§6.18, §6.19).
- Patch/diff/executor output flows through the redact-before-persist choke point.
- Fail-closed on any security check failure (§6.24).

## 15. Testing plan

- Unit: ChangeSet state machine (every legal/illegal transition); ApplyJournal state machine +
  reconciliation; RiskProfile tier floor; capability authorization.
- Integration: `execute` produces a ChangeSet from a Task (deterministic `internal` executor) →
  checks → review → apply → journal `committed`; `revert` restores the base.
- Concurrency: two applies to the same workspace serialize; a mid-apply crash reconciles on
  restart.
- Regression: M1+M2 `smoke.sh` stays green; `go test ./...` + `go vet ./...` clean.

## 16. Dependency-ordered tasks

0. **M2 tail**: (a) orchestrator synthesis in Convergence; (b) `doctor` edge audit.
1. Domain + migration 9: `change_sets`, `apply_journal`, `execution_attempts`, `risk_profiles`,
   `snapshots`.
2. ChangeSet state machine (formal transition matrix) + tests.
3. ApplyJournal + Revert + startup reconciliation + tests.
4. Sandbox (container/local_restricted) + capability broker + executor interface (`internal`
   first) + tests.
5. Threat model stage (security_architect) + RiskProfile (deterministic tier floor) + tests.
6. `execute` pipeline: pick task → executor → ChangeSet → deterministic checks → review.
7. API + CLI.
8. UI: Changes tab, ChangeSet detail modal, Build stepper step.
9. `smoke.sh` M3 extension + full verification.

## 17. Exit criteria & acceptance

1. `idea execute` on an approved plan produces a reviewed, applied ChangeSet end-to-end.
2. Every source mutation is a ChangeSet; the executor never writes the canonical workspace.
3. Apply is crash-safe: a killed mid-apply reconciles to `recovery_required` on restart (no
   half-applied state).
4. `revert` restores the base exactly before acceptance; inverse ChangeSet after.
5. RiskProfile tiers low/medium/high/critical with a deterministic minimum; threat model produced
   and approved.
6. UI exposes Changes + ChangeSet diff/review/apply with no raw machinery.
7. CLI parity: `execute/changes/change/apply/revert/risk/executors` (all `--json`).
8. `go test ./...` + `go vet ./...` clean; M1+M2 `smoke.sh` still green.

## 18. Risks & open decisions (carried into the adversarial review)

- **R1 — OMP vs internal executor.** Resolved: `internal` executor ships first; OMP is a stub
  behind the same interface (B2).
- **R2 — Docker dependency.** Resolved: `local_restricted` is the tested CI/demo path; `container`
  is a smoke path (B1).
- **R3 — Filesystem/SQLite atomicity.** Resolved: the journal is the single source of truth;
  kill-at-each-step reconciliation (B5).
- **R4 — Revert safety.** Resolved: inverse-ChangeSet after acceptance; never overwrite newer user
  work (B4).
- **R5 — Scope.** Resolved: thin-slice to the ChangeSet lifecycle; threat model + risk are thin
  (B1).

## 19. Adversarial review & resolutions

Six blocking findings were raised and resolved; see
[`docs/plans/milestone-3-adversarial-review.md`](./milestone-3-adversarial-review.md). Summary:

1. **B1** Thin-slice: ChangeSet lifecycle + `internal` executor + `local_restricted`; threat/risk
   thin; OMP a stub.
2. **B2** The ChangeSet + journal are the deliverable; the executor is a thin adapter.
3. **B3** Capability broker deterministic + fail-closed; model-granted capability is rejected.
4. **B4** Revert safety is tested (never overwrite newer user work; inverse ChangeSet after
   acceptance).
5. **B5** ApplyJournal is the single source of truth; exhaustive kill-at-each-step reconciliation.
6. **B6** Deterministic checks enumerated (apply/build/test/secret-sweep).
