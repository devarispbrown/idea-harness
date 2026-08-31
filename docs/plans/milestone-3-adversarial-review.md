# Milestone 3 Plan — Adversarial Review Record

Six independent reviewers evaluated
[`docs/plans/milestone-3-plan-to-reviewed-changeset.md`](./milestone-3-plan-to-reviewed-changeset.md).
This file records the consolidated findings and dispositions.

## Reviewers

| Perspective | Verdict (summary) | Blocking findings |
|---|---|---|
| CTO / principal architect | Right primitive; the executor abstraction is over-emphasized relative to the ChangeSet+journal, which are the real deliverables; snapshot strategy under-specified. | executor scope; snapshot representation; transaction boundary |
| Staff Product | Correct vertical cut, but over-scoped (threat model + risk + OMP + sandbox). "execute" trigger semantics unclear. | thin-slice; explicit execute trigger |
| Staff Security | ChangeSet/journal design is sound; capability broker must be deterministic + fail-closed; revert-safety needs a concrete test. | capability authorization test; revert-safety test |
| Staff Quality | State machines need enforced-in-code transition matrices; deterministic-check list and journal reconciliation must be exhaustive. | transition enforcement; deterministic checks; kill-at-each-step reconciliation |
| Staff SRE / Platform | Docker dependency makes the slice un-demoable in sandboxed CI; `local_restricted` must be first-class. | sandbox fallback path |
| Head of Design/UX | Changes tab is the new primary surface and is under-specified; executor progress framing is right but needs the concrete states. | Changes tab spec; progress states |

## Consolidated blocking findings → resolution

**B1 — Thin-slice M3 (Product, CTO).**
Threat model + risk tiering + OMP + three sandbox modes is too broad for one milestone. **Resolution:**
ship M3 as the **ChangeSet lifecycle** — `draft → checks → review → apply → revert` — with one
deterministic `internal` executor and `local_restricted` sandbox as the primary path; `container`
(Docker) and OMP (`local_external`) are isolated smoke paths; threat model + RiskProfile are
authored (thin) but not a full stage graph. Mirror M2's B3.

**B2 — The executor is a thin adapter; the ChangeSet + journal are the deliverable (CTO).**
The `internal` executor produces a deterministic, content-addressed patch (not a real coding agent)
so the slice is hermetically testable. The OMP adapter is validated behind an interface but does
not gate M3. **Resolution:** executor interface is shipped, `internal` is the only wired
implementation for the demo; OMP is a stub behind the same interface.

**B3 — Capability broker is deterministic and fail-closed (Security).**
**Resolution:** capabilities are declared by tools/executors and authorized by deterministic code;
a regression test asserts a model-suggested capability grant is rejected (models cannot grant
capabilities). Sandbox (blast radius) and broker (semantic governance) are separate code paths.

**B4 — Revert safety is tested, not asserted (Security).**
**Resolution:** add a test that a concurrent user edit + revert does **not** restore an old snapshot
over newer work; after acceptance, revert is always a new inverse ChangeSet (never a snapshot
restore). "User edits are never silently overwritten" is a test, not a comment.

**B5 — ApplyJournal is the single source of truth; reconciliation is exhaustive (CTO, Quality).**
**Resolution:** the journal is the authoritative apply record; the 9-step apply sequence has a
kill-at-each-step test verifying every interrupted state recovers to `recovery_required` or a
consistent terminal state on restart. The "transactionally mark applied + event + journal
committed" boundary is explicit: journal row and relational state commit in one SQLite transaction
(the filesystem is already mutated by then and is reconciled against the journal).

**B6 — Deterministic checks are enumerated (Quality).**
**Resolution:** M3's deterministic checks are: (1) patch applies cleanly to the base snapshot,
(2) build/type-check passes, (3) tests pass (a trivial deterministic test for the demo), (4) no
secrets in the diff (redaction sweep). These run in `checks_running` before review, and their
results are Findings — deterministic evidence, not LLM judgment.

## Non-blocking findings (fold into the plan)

- **Execute trigger:** `idea execute` is an explicit human action (like `advance`), not an
  auto-run; it picks the next `ready` Task deterministically.
- **Snapshot representation:** base/rollback snapshots are content-addressed per *changed file*
  (not full-tree copies) for non-Git; Git uses worktrees when available.
- **Changes tab spec:** card = status chip + one-line summary (task title) + diff behind "View
  diff" + inline Approve / Request changes / Reject; Apply is a separate button; no raw git output.
- **Progress states:** Planning / Editing / Testing / Waiting / Reviewing / Finished (no
  chain-of-thought), driven by executor progress events.
- **Risk tier floor:** deterministic signals (e.g. auth/network/secret-touching diffs) establish a
  minimum tier; semantic analysis may only raise it.

## Status

Blocking findings: **6, all resolved by amendment.** Plan status: **awaiting human approval to
implement.** Recommended order: B1/B2 (ChangeSet lifecycle + internal executor + local_restricted)
→ B5 (journal + reconciliation) → B3/B4 (capability + revert safety) → B6 (deterministic checks) →
UI/CLI.
