# Milestone 2 Engineering Execution Plan — PRD → Reviewed Execution Plan

**Milestone 2 = PRD → reviewed execution plan** (PRD v1.1 §50; "Development Phases" §11
`Define → Design → Build`; Technical Design §12 workflow DAG).

M1 proved the vertical slice `empty folder → idea init → idea new → framing → research → reviewed
PRD`. M2 extends that slice forward: given a reviewed PRD, the team produces an independently
reviewed execution plan — the durable, traceable bridge from *what we're building* to *how we'll
build it* — without yet executing any code change (that is M3).

## 1. Goals

1. **Design in parallel.** After Product Definition, run three independent specialist stages —
   Architecture, Product Design, Verification Design — and converge them.
2. **Reviewed execution plan.** Decompose requirements + converged design into an ordered set of
   `Task`s, each traced to a Requirement, with dependencies, risk, and verification hooks; the plan
   is independently reviewed before it is "ready to build".
3. **Product Graph v0.** Introduce `graph_edges` and wire `supports / derived_from / depends_on /
   implements / addresses / verifies / reviews / supersedes / produced_by` edges as entities are
   created, enabling the first `why / trace / impact` answers.
4. **Semantic diff.** Requirement/decision/artifact changes across revisions are reported as
   *structured* changes (add/remove/priority-change/supersede), not raw markdown diff.
5. **Multi-role dissent as decisions.** When Architecture, Design, and Verification disagree, the
   conflict is synthesized into one human decision surface with per-role recommendations — never
   three separate chat windows (PRD §4.3).
6. **StageRun state machine.** Formalize stage execution (queued/ready/running/review/blocked/done)
   so the three Design stages can genuinely run in parallel and be independently gated.

## 2. Non-Goals (explicitly out of scope for M2)

- **Execution / source-tree mutation.** No sandbox, no OMP/executor, no ChangeSets, no apply
  journal — that is Milestone 3. M2 produces a plan; it never writes source files.
- **Threat model document** — still M3 (PRD §12 lists "Threat Model?" but constitution §68 pins the
  formal threat model to the start of M3; M2 records security-derived *requirements* only, as M1
  did).
- **Verification evidence + the final verification gate** — Milestone 4.
- **Full semantic code index, embeddings, marketplace, multi-user, SaaS, remote fleet.**
- **`idea why / trace / impact` across ChangeSets/Tests** (those node types don't exist until
  M3/M4); M2's traceability covers Source → Assertion → Requirement → Decision → Artifact → Task →
  Review/Verification-Design.

## 3. Requirements in scope (from the constitution)

| Source | Requirement |
|---|---|
| §12, §50 | PRD → reviewed execution plan; Design phase (Architecture, Product Design, Verification Design) may run in parallel. |
| §8, §4.3 | Expert disagreement synthesized into a human decision surface with per-role recommendations (Accept / Choose alternative / Discuss). |
| §4.9, §25, §26 | Explain / Why / Impact / Trace; semantic artifact diff; impact analysis with staleness classification (`unaffected/revalidate/revise/obsolete`). |
| §9, §10 | Role Contracts for the new specialists (Architect, Product Designer, Verification Engineer); deterministic specialist invocation. |
| §13 | StageRun state machine (queued/ready/running/review/blocked/done/skipped/cancelled) with declarative stage conditions; transitions via domain methods only. |
| §4.5 | Independent review of every material execution plan; no agent approves its own work. |
| §6 | Recommended Next Move derived from workflow state + unresolved decisions + blocking gates + dependency graph. |
| §52–§59 | Calm command center: add Plan/Tasks/Why&Impact surfaces without exposing raw machinery. |
| §64 | CLI parity: `plan`, `tasks`, `why`, `trace`, `impact` (all `--json`). |

## 4. Workflow — the M2 stage graph

M1 graph (unchanged, kept as the entry): `framing → research → product_definition → review loop`.

M2 appends, triggered when the PRD reaches `approved` (or is explicitly advanced past
`needs_human_judgment`):

```text
product_definition (reviewed)
      │
      ├──────────────┬──────────────────────┐
      ▼              ▼                      ▼
  Architecture   Product Design       Verification Design      (parallel)
      │              │                      │
      └──────────────┼──────────────────────┘
                     ▼
                Convergence
                     │
                     ▼
             Execution Plan
                     │
                     ▼
         Execution Plan Review            (adversarial, ≤3 cycles → human judgment)
```

- **Architecture** (role `architect`): components, data flow, storage/API boundaries, and
  architecture-level Decisions (type `architecture`). Output artifact `technical_design`.
- **Product Design** (role `product_designer`): user flows, screens, interaction model; raises
  design-level Decisions (type `design`). Output artifact `design_spec`.
- **Verification Design** (role `verification_engineer`): per-Requirement verification strategy —
  what will prove each acceptance criterion (test type, deterministic check, or manual gate).
  Output artifact `verification_plan`.
- **Convergence** (role `orchestrator`): reads the three design artifacts, detects conflicts,
  synthesizes them into Decisions with per-role dissents, and produces a converged design summary.
  Non-convergence pauses at `needs_human_judgment` exactly like M1 review.
- **Execution Plan** (role `engineering_lead`/`orchestrator`): emits ordered `Task`s, each with a
  `depends_on` edge and an `implements` edge to a Requirement, plus a rendered `execution_plan`
  artifact.
- **Execution Plan Review** (role `adversarial_reviewer`): reuses the M1 review loop — structured
  Findings against the plan (untraced tasks, missing verification, cyclic dependencies, unsourced
  estimates). Blocking findings pause for human judgment; ≤3 auto-revise cycles.

### StageRun state (sequential execution; no concurrent scheduler)

M1 stored `stage_runs` but drove stages synchronously. The three Design branches are **independent
in the DAG** but are executed **sequentially in a fixed deterministic order** (Architecture →
Product Design → Verification Design) by the existing synchronous driver. M2 formalizes the
`stage_runs` rows with `queued/running/review/done` for visibility, but runs one stage at a time —
SQLite is single-writer and OS concurrency is not needed to deliver this outcome (adversarial
review B1). A true concurrent scheduler is deferred to M3. State transitions remain domain methods
only (§13).

## 5. Data model — schema additions

Append-only migrations (current: 6). New tables/columns:

```text
graph_edges
  id, project_id, from_type, from_id, edge_type, to_type, to_id,
  metadata_json, created_at
  -- edge_type ∈ {supports, contradicts, derived_from, depends_on, implements,
  --              addresses, tests, verifies, reviews, supersedes, invalidates,
  --              produced_by, consumes}
  -- index (project_id, from_type, from_id), (project_id, to_type, to_id)

tasks
  id, initiative_id, code, title, statement, status, priority,
  estimate, produced_by_run, superseded_by, created_at
  -- status ∈ {proposed, ready, blocked, done, cancelled}
  -- dependencies are graph_edges (task --depends_on--> task)

decisions: add column `converged_by` TEXT (run id that synthesized it)   -- nullable
decisions: type enum already includes product|architecture|design|security|operations

findings: already has target_type/target_id — reuse for plan review (target_type = "task"|"plan")

artifact_kinds (enum only; rows reuse artifacts/artifact_versions):
  technical_design, design_spec, verification_plan, execution_plan  (already declared in the enum)
```

**Graph edge writing is the durable traceability spine.** M2 ships a **thin slice**: only
`derived_from`, `implements`, `depends_on`, `produced_by`, `supersedes` are wired. The remaining
edge types are declared in the enum but not yet written until M3/M4. Edges are written by a single
`wireEdges(tx, edges...)` helper invoked from *every* object-creating method — including the M1
creates (assertion/requirement/decision/artifact/finding) — in the same `db.Apply` transaction
(atomic state+event+edge, §26). A `doctor` check reports any object missing its required
`derived_from` / `produced_by` edge. No backfill for pre-existing initiatives; M1 initiatives are
marked `unwired` (adversarial review B2/B3).

## 6. Traceability — `why / trace / impact`

Deterministic graph traversal, no LLM required for the mechanics:

- **`why <id>`** — the reverse chain (`derived_from`, `supports`, `implements`) from an object back
  to its sources, rendered as a short provenance tree (with per-node source refs).
- **`trace <id>`** — the full forward/backward subgraph around an object.
- **`impact <id>`** — forward traversal from a changed object to potential dependents, classified
  by staleness: `unaffected | revalidate | revise | obsolete` (§26). Deterministic classification
  (edge-type driven); an LLM may only *annotate* ambiguous cases, never set state.

**Semantic diff** ships for **requirements and decisions only** in M2 (the two objects the human
actually reviews). It compares the *structured rows* across the supersede chain (not
`artifact_versions` alone, since requirements/decisions/tasks are row-based) and reports:
requirement add/remove, priority change, decision supersession, new risk, modified acceptance
criterion. Raw markdown diff is secondary (§25).

## 7. API endpoints

```text
GET  /projects/{pid}/initiatives/{id}/tasks
GET  /projects/{pid}/initiatives/{id}/plan                 # execution plan projection
GET  /projects/{pid}/initiatives/{id}/graph                # nodes+edges (bounded)
GET  /projects/{pid}/objects/{type}/{id}/why               # provenance tree
GET  /projects/{pid}/objects/{type}/{id}/impact            # dependents + staleness
GET  /projects/{pid}/objects/{type}/{id}/trace             # subgraph
POST /projects/{pid}/initiatives/{id}/advance              # advance past PRD into Design (M2 entry)
GET  /projects/{pid}/initiatives/{id}/diff?from=&to=       # semantic diff between versions
```

`/advance` is the M2 analog of M1's `revise`: it launches the Design→Convergence→Plan pipeline as
an async Operation (returns `operation_id`).

## 8. CLI commands

```text
idea plan [--json]           # render the execution plan
idea tasks [--json]          # ordered task list
idea why <type> <id>         # provenance chain
idea impact <type> <id>      # dependents + staleness
idea trace <type> <id>       # subgraph
idea diff [--from V --to V]  # semantic diff
idea advance                 # advance the initiative into Design/Plan
```

All state commands honor `--json` (schema = API response).

## 9. UI — screens/components

- **Initiative Home** gains two surfaces below "Needs Your Judgment":
  - **Plan** card — the execution plan: ordered Tasks with code/status/priority and a
    "depends on" indicator; collapsible; each task links to its Requirement.
  - **Design** summary — one line per design lane (Architecture / Product Design / Verification)
    with status (running / review / done) and a link to its artifact.
- **New "Why & Impact" panel** — every Requirement/Task/Decision row carries a visible **"Why?"**
  link (not a hidden icon); it opens a drill-down showing the provenance tree + dependents, with
  staleness chips (`revalidate`, `revise`, `obsolete`).
- **Tasks tab** — the ordered plan, filterable by status; "show dependencies" toggles the graph
  edges inline (no raw machinery — §79).
- **Decisions tab** — a **multi-role decision** is one card: the question, one row per specialist
  recommendation (role-tagged, dissent preserved), then the orchestrator's **synthesis** highlighted
  as the default, with Accept / Choose alternative / Discuss (reuses the M1 decision UI + discussion
  thread). The "Take recommendations" default for a multi-role decision is the orchestrator
  synthesis (resolves R5).
- **Diff** — the "What Changed" card links to a per-version semantic diff view.

All of the above follow the existing calm, tabbed, chip-based design language; the confirm-and-revise
bulk flow from M1 generalizes to "Take recommendations" for design decisions.

## 10. Role contracts (new)

Add four YAML contracts (same shape as M1's):

- `architect.yaml` — competencies (systems decomposition, data flow, storage/API boundaries,
  tradeoff articulation); required outputs (technical_design artifact, architecture Decisions).
- `product_designer.yaml` — competencies (IA, flows, states, accessibility); required outputs
  (design_spec artifact, design Decisions).
- `verification_engineer.yaml` — competencies (test design, deterministic checks, coverage
  mapping); required outputs (verification_plan artifact, per-requirement verification hooks).
- `engineering_lead.yaml` — competencies (task decomposition, sequencing, risk-based ceremony);
  required outputs (execution_plan artifact, Task rows with dependency edges).

Each contract carries an eval/regression rubric and a "never self-approve" behavior, consistent with
M1 (§10).

## 11. Review & human-in-the-loop

- Every Design lane, the Convergence synthesis, and the Execution Plan are **independently**
  reviewed by `adversarial_reviewer` (reviewer ≠ producer, §4.5).
- Approval bar: no open critical/high Findings (reuse M1's `EvaluateApproval`).
- **Plan-specific deterministic checks** (`DeterministicPlanChecks`, before the adversarial
  reviewer — adversarial review B4): every Task has exactly one `implements` Requirement and no
  orphan `depends_on` chain; the dependency graph is acyclic (topological check); every Requirement
  has at least one Task or an explicit deferred marker; every Task has a Verification Design hook.
- Blocking Findings → `needs_human_judgment`; human dispositions (fix/accept-risk/not-applicable/
  defer/mitigate + notes) → "Take recommendations & revise" re-runs the failing stage (not the
  whole pipeline).
- Cross-lane disagreement → multi-role Decision (per-role dissents preserved, never erased).

## 12. Security

- New endpoints are loopback + token-authenticated (reuse `auth`).
- `graph_edges.metadata_json`, task/plan text, and diff content flow through the same
  redact-before-persist choke point as M1.
- `why/trace/impact` traversal is bounded (depth ≤ 8, node cap ≤ 500) and token-scoped.
- The staleness classifier is deterministic and authoritative; any LLM annotation is display-only
  and can never set an object's state.
- No source-tree reads/writes in M2 (the plan consumes only the in-graph model + prior artifacts).

## 13. Testing plan

- Unit: graph-edge writing on every create/supersede (invariant: no orphan object without
  `produced_by`/`derived_from`); impact traversal classification; semantic-diff comparators.
- Integration: `advance` runs Design→Convergence→Plan end-to-end on the FakeProvider; parallel
  stages complete and converge; blocking plan review pauses for human judgment; `why/impact` return
  correct dependents.
- Regression: M1 `smoke.sh` must stay green (M1 path unchanged).
- `go test ./...`, `go vet ./...` clean.

## 14. Dependency-ordered tasks

1. Domain + migration 7/8: `graph_edges`, `tasks`, `decisions.converged_by`.
2. GraphEdge writer in `store` (wired into every create/supersede call) + tests.
3. `why/trace/impact` traversal service + staleness classifier + tests.
4. Semantic diff comparators (requirement/decision/task) + tests.
5. Four new role contracts + `schema.Architecture`, `schema.ProductDesign`,
   `schema.VerificationPlan`, `schema.ExecutionPlan`.
6. StageRun scheduler: parallel Design branches + Convergence + Execution Plan stages.
7. Multi-role decision synthesis (dissent merge → one Decision) + review loop for the plan.
8. API + CLI (`plan/tasks/why/impact/trace/diff/advance`).
9. UI: Plan/Tasks/Why&Impact/multi-role Decisions/semantic diff.
10. `smoke.sh` extension + full test/vet pass + demo verification doc.

## 15. Exit criteria & acceptance

1. `idea advance` on a reviewed PRD produces, in one async run, three Design artifacts + a
   Convergence result + an Execution Plan, all independently reviewed.
2. Execution Plan = ordered Tasks, every Task has a `depends_on` (or is a root) and `implements` a
   Requirement; no untraced task.
3. `why / impact / trace` return correct, bounded results; `impact` classifies staleness.
4. Semantic diff between PRD versions reports structured requirement/decision changes.
5. Cross-lane disagreement surfaces as a single multi-role Decision with per-role recommendations
   and dissent preserved.
6. UI exposes Plan/Tasks/Why&Impact/multi-role Decisions with no raw machinery on primary surfaces.
7. `smoke.sh` (M1) still green; `go test ./...` + `go vet ./...` clean.

## 16. Risks & open decisions (carried into the adversarial review)

- **R1 — Scope size.** Resolved: pipeline ships first, graph/traceability is a thin slice
  (B3).
- **R2 — Parallel scheduler correctness.** Resolved: sequential execution in deterministic order,
  no concurrent scheduler (B1).
- **R3 — Graph-edge backfill.** Resolved: retrofit M1 creates via `wireEdges`; pre-existing
  initiatives marked `unwired`, no backfill (B2).
- **R4 — LLM in the loop for staleness.** Resolved: deterministic classifier authoritative; LLM is
  display-only (B6).
- **R5 — "Take recommendations" default for multi-role decisions.** Resolved: orchestrator
  synthesis is the default (B5).

## 17. Adversarial review & resolutions

Six blocking findings were raised and resolved; see
[`docs/plans/milestone-2-adversarial-review.md`](./milestone-2-adversarial-review.md) for the full
record. Summary:

1. **B1** No concurrent scheduler — sequential, deterministic Design-stage execution.
2. **B2** Single `wireEdges` codepath retrofitted into M1 creates + `doctor` audit.
3. **B3** Thin-slice the graph (5 edge types) and semantic diff (requirements + decisions).
4. **B4** `DeterministicPlanChecks` before the adversarial reviewer.
5. **B5** Multi-role "DECISION NEEDED" card + visible "Why?" affordance; orchestrator-synthesis
   default.
6. **B6** Bounded, token-scoped, redacted traversal; deterministic staleness classifier.
