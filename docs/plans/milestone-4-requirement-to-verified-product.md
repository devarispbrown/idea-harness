# Milestone 4 Engineering Execution Plan — Requirement → Verified Product

**Milestone 4 = Requirement → verified product** (PRD v1.1 §50; Technical Design §70–§71).

M3 delivered the ChangeSet lifecycle — `execute → checks → review → apply → revert` — but the thin
slice made the "change" a changelog line, not code. M4 closes the loop: build the product for real,
verify it against its requirements, and surface the whole thing in a way both a product manager and
an indie developer find useful.

## 0. Why three tracks (reconciliation)

Two persona reviews (PM + indie developer) reconcile to one shared spine — every user wants these
five things:

1. **It keeps going until it needs me** (not `new→advance→execute→review→apply` by hand).
2. **Human language, not state-machine jargon** ("needs your approval", not `review_required`).
3. **It actually produces the product** (code, not a changelog line).
4. **A trustworthy "is it done?" verdict** — verified against requirements, not "tests exist".
5. **Always shows the one thing to do next** ("show me what happens next" — PRD §52).

Per-persona differentiators sit on top of that spine: the PM wants traceability and surfaced
judgment calls; the indie developer wants the adversarial reviewer, safe execution, cost control,
and editor/git interop. These are already designed (review loop, capability broker/sandbox,
BYO-model, `explain/why/impact/trace`) — they are not new M4 work.

The spine maps to **three tracks**:

| Track | Need | In M4 today? |
|---|---|---|
| **A. Build** — multi-role execution producing a real diff | #3 | ❌ missing (the critical gap) |
| **B. Verify** — `VerificationEvidence` + coverage + completion gate | #4 | ✅ core (Tech Design §70) |
| **C. Surface** — "continue until I'm needed" + human language + next-action | #1, #2, #5 | ❌ missing (PRD §52 north-star) |

**The critical finding:** the milestone chain has a missing link. M3 produces a *reviewed changelog*;
M4 verifies a *product*. Nothing between them builds the product. M4 must therefore start with Track
A, or the verification gate verifies an empty workspace.

## 1. Non-Goals (explicitly out of scope)

- Hosted SaaS, multi-user collaboration, enterprise RBAC, mobile app, deployment platform
  (PRD §51).
- Remote worker fleet, skill marketplace, extension registry, plugin marketplace.
- Full semantic code index, complex numeric risk engine, organization-wide memory.
- Unbounded role fan-out — Track A ships a **fixed, thin role pipeline** (§2.2); additional roles
  (dedicated tester, security auditor, UX reviewer) are added later, not in track 1.

## 2. Track A — Build (multi-role execution)

**Goal:** `idea execute` produces an actual file change in the canonical workspace — not a changelog
line — by coordinating multiple roles through an orchestrator, while preserving every M3 invariant
(isolated work root, capability gate, trust-tier policy, adversarial review, atomic apply journal).

### 2.1 Orchestrator vs. agents (coordination vs. code-writing)

The execution stack separates **coordination** from **code-writing**, and this separation is the
whole point:

- **The orchestrator** is our deterministic workflow `Engine` — policy-constrained code, not an
  external black box (§6). It enforces the role order, assigns each role's work, applies the
  capability/trust gate, and records each role's output as a durable artifact. Models cannot reorder
  the pipeline or grant themselves capabilities (§6, §49).
- **The coding agents** are interchangeable `Executor` backends that actually write code. M4 adds
  **two**: **OMP** (the `omp` CLI) and **Hermes** (Nous Research's `hermes` agent). Each writes
  files in the isolated work root and returns a real diff, behind the existing `Executor` interface
  and the trust-tier policy (`local_external`: `fs.write`, no `run_command`).

So OMP and Hermes are **peers** — two coding-agent backends the orchestrator can dispatch a role to.
The orchestrator is ours; the agents are external CLIs.

- `OMPExecutor` (shipped in M3, **stdout-only**): `omp --print --cwd <work> --model <model> <prompt>`
  captures stdout, it does **not** write files. Track A rewrites it to a file-editing invocation and
  reuses the same snapshot/diff collection path as the internal implementer (review B1/B3).
- `HermesExecutor` (new): `hermes chat --oneshot --query-file - --in <work> --model <model>`,
  reading the brief on stdin and writing files in `<work>`.

### 2.2 The thin role pipeline (track 1)

For one ready `Task`, the orchestrator runs a fixed three-step pipeline:

1. **`engineering_lead` (planner, model role).** Decomposes the task into a short implementation
   brief — files to touch, changes to make, acceptance criterion targeted. Produces a durable
   artifact. Writes no code.
2. **Implementer (coder, an agent backend).** Receives the brief, writes the real files in the
   isolated work root, returns an ordered `FileChange` list + `BaseSnapshotDigest`. The implementer
   is a coding agent — **OMP or Hermes**, selected by config (not model output).
3. **`adversarial_reviewer` (reviewer, model role).** Reviews the diff against the requirement and
   the brief (this is the existing M3 review, now over a real diff).

Each step is a durable `Run` (or `ExecutionAttempt`) with actor + model + agent + digest, so the
pipeline is traceable end to end. The orchestrator can stop and surface a human judgment point after
steps 1 and 3 (autonomy presets, §4).

### 2.3 Result/patch representation

- `executor.Result` gains a real patch: an ordered `[]FileChange{Path, Digest, Mode}` plus a
  `BaseSnapshotDigest` (content-addressed snapshot of the affected files *before* mutation).
- A deterministic `internal` implementer stays as the hermetic demo path (it writes a real file, not
  a changelog), so the whole slice is testable without a model or any agent CLI.
- `container`, `omp`, and `hermes` implementers produce real diffs; `Collect` reads the changed files
  back into `FileChange`s and persists each to the blob store.

### 2.4 Deterministic checks become real

The B6 checks currently recorded as `not_applicable` flip to real checks once
`BaseSnapshotDigest != ""`:

1. **patch applies** — apply the patch to the base snapshot; verify resulting hashes match the
   collected `FileChange` digests.
2. **build/type-check** — run the project's build command in the executor, record exit status as a
   Finding.
3. **tests** — run the test command, record results as a Finding.

### 2.5 Thin-sliced Track A (track 1)

1. A task targets one Requirement (M2 already links `TASK —implements→ REQ`).
2. The orchestrator runs: `engineering_lead` brief → implementer (deterministic internal first)
   writes a real file → `adversarial_reviewer` reviews the diff.
3. Deterministic checks run for real (patch-applies at minimum).
4. ChangeSet records the real patch; review/apply use the existing state machine.
5. `apply` is **net-new** work (review B1): the orchestrator writes the computed diff into the
   canonical workspace via per-file temp+rename with a pre-image snapshot, and `revert` restores the
   pre-images. The ApplyJournal states (`prepared→workspace_mutated→committed→recovery_required`)
   gain their real file semantics here — today they are only markers.

The OMP and Hermes backends are swapped in for the deterministic implementer as config-selected
executors. OMP is a **rewrite** (stdout→file-editing), not a config swap; `HermesExecutor` mirrors the
rewritten OMP adapter. Both must run OS-sandboxed (review B2).

## 3. Track B — Verify (Tech Design §70)

**Goal:** a Requirement is "verified" only when evidence exists — not because tests happen to exist.

### 3.1 Domain additions

- `VerificationEvidence` — `{id, initiative_id, requirement_id, criterion_id, kind, status, evidence_digest, run_id, created_at}`
  where `kind ∈ {acceptance, security, ux}` and `status ∈ {pass, fail, blocked, needs_manual}`.
- `RequirementStatus.verified` (and `Requirement.VerifiedAt`).
- A `verification_plan` (M2 artifact) drives which evidence each mandatory AcceptanceCriterion
  requires.

### 3.2 The completion gate

- Every **mandatory** AcceptanceCriterion requires at least one `pass` `VerificationEvidence`.
- Coverage = `verified mandatory criteria / total mandatory criteria` per Requirement, then per
  Initiative.
- The initiative cannot reach a terminal "done" status until coverage is complete; the gate is
  deterministic (computed from the store, not model judgment).

### 3.3 Verification runs

- The `verification_engineer` role (already a contract) produces evidence: runs build/tests, maps
  results to criteria, and flags `needs_manual` where human verification (UX/security sign-off) is
  required.
- A new `verify` trigger runs the verification stage for applied change sets and writes evidence.

## 4. Track C — Surface (PRD §52 north-star)

**Goal:** deliver the six "show me" lines, and hide the machinery until asked.

1. **`idea next` / "continue"** — runs the deterministic next move (advance → execute → verify) and
   stops only at a human-judgment point. Backed by autonomy presets (§71): Supervised / Standard /
   High, each compiling to an explicit "stop for me" policy.
2. **Human status language** — translate `review_required → "needs your approval"`,
   `checks_running → "checking"`, `needs_human_judgment → "waiting on you"` at the surface, while
   keeping the raw status in `--json`.
3. **Persistent next-action** — one always-visible line: "Approve CHG-1", "Run verify", "Configure
   a model".
4. **Guided review** — `idea review` with no verdict lists pending change sets and prompts
   Approve / Request changes / Reject.

## 5. CLI / UI / API

- CLI: `idea next`, `idea verify`, `idea coverage`, and human labels on `status`/`changes`.
- UI: the stepper's **Verify** step becomes real (driven by coverage); a persistent "next action"
  banner; the Changes tab keeps its gated actions.
- API: `POST /initiatives/{id}/verify`, `GET /initiatives/{id}/coverage`,
  `GET /requirements/{id}/evidence`.

## 6. Acceptance criteria

1. **Multi-role build.** `idea execute` on a ready task runs the orchestrator's role pipeline
   (planner → implementer → reviewer); each step is a durable, traceable run.
2. **Real diff.** The ChangeSet's patch is real file changes (≥1 `FileChange`) computed by the
   orchestrator from a pre/post work-root diff (never agent-declared); `apply` writes them to the
   canonical workspace via the new atomic file-mutation path, and `revert` restores the pre-images.
3. **Checks are real.** For a diff patch, patch-applies/build/tests Findings reflect actual outcomes
   (pass/fail), never `not_applicable`; build/tests run in the orchestrator's check runner, never in
   the agent (review B5).
4. **Verify.** Every mandatory AcceptanceCriterion has `VerificationEvidence`; coverage is computed
   deterministically from a structured criterion→evidence table; a human can record a `pass` for
   `needs_manual` criteria; the initiative reaches `complete` only at 100% coverage and no open
   blocking findings (Tech Design §70; review B4/B10).
5. **Surface.** `idea next` advances through Build→Verify and stops only at judgment points; status
   output uses human language; a next-action line (with a `Why`) is always visible (PRD §52).
6. **Safety invariants.** The orchestrator is deterministic; the capability broker gates
   harness-internal dispatch; external agents (OMP/Hermes) run **OS-sandboxed** (`container`, no
   network, writable-only work root — review B2); the canonical-workspace apply is an unconditional
   hard human confirmation no autonomy preset can skip (review B8); adversarial review and the atomic
   ApplyJournal are preserved.
7. **Regression.** `go test ./...` + `go vet ./...` clean; M1+M2+M3 `smoke.sh` still green; web
   `pnpm build` + `pnpm test` clean.

## 7. Verification

- `smoke.sh` M4 extension: `new → advance → execute (multi-role, real file) → change → review
  approve → apply → verify → coverage → done`.
- Golden fixtures: a `pass` criterion, a `fail` criterion, and a `needs_manual` criterion each
  produce the expected evidence + gate behavior.
- A test that the orchestrator's role order is deterministic and that a model cannot reorder it or
  grant capabilities (mirrors the existing broker test).
- The six-perspective adversarial review is recorded in `milestone-4-adversarial-review.md`; the
  B1–B10 resolutions in §9 are the bar this milestone is verified against.

## 8. Resolved decisions

- **Multi-role execution is in Track A.** The deterministic orchestrator (our workflow `Engine`)
  coordinates roles; **OMP** and **Hermes** are two peer coding-agent backends behind the `Executor`
  interface. The orchestrator is ours; the agents are external CLIs.
- **Hermes = Nous Research's `hermes` agent** (`hermes --in <dir> --yolo chat --query …`), a
  coding agent. **Hermes is the primary/default real-agent backend** (cleaner non-interactive
  contract, `--yolo` auto-approve, `--worktree`, `--toolsets`); OMP remains a supported alternative
  behind the same `Executor` interface, and `internal` remains the hermetic demo default.
- **The orchestrator stays built-in.** A black-box external orchestrator would violate the
  deterministic-orchestrator invariant (§6) — coordination is policy-constrained code we own; only
  the *code-writing* is delegated to external agents.
- **Remaining question for implementation kickoff:** whether the implementer role is a single agent
  invocation per task (thin slice, recommended) or one invocation per *file/step* (finer-grained,
  larger). Track 1 assumes the former.
- **Open integration detail:** how a role is bound to an agent (e.g. `executor.agents.engineer =
  hermes`, `executor.agents.implementer = omp`) — deterministic config, never model output. The
  default for track 1 binds the implementer to the internal/`omp`/`hermes` backend selected by
  `executor.kind`, extended with a per-role override map.

## 9. Adversarial review resolutions

The six-perspective review (`milestone-4-adversarial-review.md`) returned **3 reject / 3
approve-with-changes** and 10 consolidated blockers. The plan above already incorporates them; the
mapping is recorded here so nothing regresses silently:

- **B1 — file apply/rollback is net-new** → §2.5 now states it explicitly; acceptance #2/#6 reworded.
- **B2 — agents must be OS-sandboxed** → OMP/Hermes run inside `container` (`--network none`,
  writable-only work root) before they are selectable; acceptance #6 reworded.
- **B3 — orchestrator computes the diff** → §2.2/§2.3 now require a pre/post work-root diff, never an
  agent-declared `FileChange` list.
- **B4 — gate data model** → §3.1/§3.2 now specify criterion id + "all mandatory" + `verified` +
  `InitiativeStatus.complete` with a deterministic transition.
- **B5 — build/tests are orchestrator-run** → §2.4 now assigns them to the Engine check runner; never
  the agent.
- **B6 — cancellation wired** → added as a Track A deliverable (cancel on all exit paths, process-group
  kill, cancel route, persisted PID).
- **B7 — cost + durable Run** → added as a Track A deliverable (agent `RunKind`, `Result.Usage`,
  `max_cost_usd` enforced).
- **B8 — autonomy never skips the final write** → acceptance #6 now makes the canonical-workspace apply
  an unconditional hard human confirmation.
- **B9 — hermetic stubs** → §7 requires `FakeProvider` cases for the brief + verify schemas and a
  model-free full-pipeline CI test.
- **B10 — human evidence path** → §5 adds `idea verify <req> --pass` + UI sign-off; acceptance #4
  reworded.

Non-blocking findings (autonomy-preset reconciliation, full status→label table, next-action `Why`,
per-track DoD, brief role contract, structured coverage table, `FileChange` ordering spec, `hermes`
config surface, untrusted-selector priority) are folded into the respective sections.
