# Milestone 2 Plan — Adversarial Review Record

Five independent reviewers evaluated
[`docs/plans/milestone-2-prd-to-execution-plan.md`](./milestone-2-prd-to-execution-plan.md) from the
perspectives required by the constitution (§82). This file records the consolidated findings and
their dispositions.

## Reviewers

| Perspective | Verdict (summary) | Blocking findings |
|---|---|---|
| CTO / principal architect | Direction right; the plan conflates "logical parallelism" with a real concurrent scheduler and under-specifies the graph-write retrofit. | scheduler; edge-write retrofit; graph index/bound strategy |
| Staff Product | Correct cut; the milestone is over-scoped and the entry point UX is unclear; multi-role default unresolved. | scope split; `/advance` semantics; R5 default |
| Staff Security | Low risk; traversal must be bounded + redacted; LLM staleness annotation must never set state. | traversal bounds; edge/task redaction |
| Staff Quality | Plan review bar reuses M1 but the plan-specific deterministic checks are not explicit; cycle/untraced/missing-verification must be first-class. | plan deterministic checks; scheduler concurrency tests |
| Head of Design/UX | Multi-role "DECISION NEEDED" and "Why & Impact" are the differentiators and are under-specified in §9. | decision card spec; why/impact discoverability; plan card visual |

## Consolidated blocking findings → resolution

**B1 — Do not build a concurrent scheduler in M2 (CTO, Quality).**
The three Design stages are *independent* (a DAG), which the constitution expresses as "may run in
parallel" — it does **not** require OS-level concurrency. SQLite is single-writer; introducing
concurrent StageRuns + a scheduling goroutine is the single riskiest change in the milestone and is
not needed to deliver the M2 outcome. **Resolution:** keep the existing synchronous driver; run
Architecture → Product Design → Verification Design in a fixed, deterministic order; formalize the
`stage_runs` rows with `queued/running/review/done` states for visibility, but execute one stage at
a time. Defer a true concurrent scheduler to M3 (where OMP/executor demands it).

**B2 — Graph-edge writing must be a single auditable codepath, and it must retrofit the M1 creates
(CTO, Quality).**
The plan says "every CreateX also writes edges in the same transaction" but does not enumerate which
methods change. M1's `CreateRequirement`/`CreateDecision`/`CreateAssertion`/`CreateArtifact` do not
write edges; if M2 only wires the *new* creates, M1-produced objects (assertions, requirements,
decisions) are permanently un-wired and `why/trace/impact` silently miss them. **Resolution:** add a
single `wireEdges(tx, edges...)` helper invoked by *every* object-creating method (M1 and M2), and a
deterministic `doctor` check that reports any object missing its required inbound `derived_from` /
`produced_by` edge. No backfill for pre-existing initiatives (mark `unwired`).

**B3 — Trim the milestone: ship the pipeline first, thin-slice the graph (Product, CTO).**
As written, M2 bundles a parallel pipeline, 13 edge types, traceability, semantic diff, multi-role
synthesis, and four new roles. That breadth risks a half-finished milestone. **Resolution:** sequence
internally — (a) Design→Convergence→Execution Plan pipeline with review (the "vertical slice" for
M2); (b) a **thin** Product Graph slice: only `derived_from`, `implements`, `depends_on`,
`produced_by`, `supersedes` edges; the remaining edge types (contradicts, tests, verifies, reviews,
consumes, invalidates, addresses, supports) are declared but not all wired until M3/M4. Semantic diff
ships only for requirements + decisions (the two the human actually reviews), not tasks/artifacts.

**B4 — Plan review needs plan-specific deterministic checks (Quality).**
`EvaluateApproval` alone is insufficient for an execution plan. **Resolution:** add a
`DeterministicPlanChecks` set: (1) every Task has exactly one `implements` Requirement and at most
one `depends_on` chain into the DAG (no untraced, no orphan); (2) dependency graph is acyclic
(topological check); (3) every Requirement has at least one Task or an explicit "deferred" marker;
(4) every Task has a verification hook from the Verification Design. These run before the adversarial
reviewer, same as M1's structural checks.

**B5 — Multi-role decision card and "Why & Impact" are the product's differentiators; spec them, don't
defer to "reuse M1" (Design/UX, Product).**
**Resolution:** the Decisions tab renders a multi-role decision as a single card: the question, one
row per specialist recommendation (role-tagged, dissent preserved), then the orchestrator's
synthesis as the highlighted default, with Accept / Choose alternative / Discuss. The "Take
recommendations" default for a multi-role decision is the **orchestrator synthesis** (resolves R5).
Every Requirement/Task/Decision row gets a visible **"Why?"** link (not a hidden icon) that opens the
Why & Impact panel; the panel is the object's provenance tree + dependents with staleness chips.

**B6 — Bound and redact traversal (Security).**
`graph_edges.metadata_json` and task/plan text flow through the same redact-before-persist choke
point. `why/trace/impact` are depth-capped (depth ≤ 8, node cap ≤ 500) and token-scoped; the
classifier is deterministic and authoritative — any LLM annotation is display-only and can never set
an object's state (closes R4).

## Non-blocking findings (fold into the plan)

- **Entry UX:** `/advance` must be an explicit, human-triggered action (a "Proceed to design & plan"
  button, not auto-launch), so a long design pipeline never surprises the user.
- **Plan card visual:** ordered tasks as a collapsible list with status/priority chips and a small
  "→ depends on" inline indicator; do not attempt a Gantt view in M2.
- **Semantic diff versioning:** clarify that diff compares structured rows across the supersede chain
  (not `artifact_versions` alone), since requirements/decisions/tasks are row-based.
- **Concurrency:** even with B1's sequential execution, keep a regression test that two independent
  stages never corrupt state if the scheduler is later parallelized (guard for M3).
- **CLI `diff` output:** ship machine-readable `--json` first; human prose rendering is secondary.

## Status

Blocking findings: **6, all resolved by amendment to the plan.** Plan status: **awaiting human
approval to implement.** Recommended implementation order: B1/B3 (pipeline first) → B2 (edge write
path) → B4 (deterministic plan checks) → B5 (multi-role + why/impact UI) → B6 (bounds).
