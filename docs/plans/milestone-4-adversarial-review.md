# Milestone 4 Plan — Adversarial Review Record

Six independent reviewers evaluated
[`docs/plans/milestone-4-requirement-to-verified-product.md`](./milestone-4-requirement-to-verified-product.md).
Each reviewer read the plan *and* the source tree to verify its claims. This file records the
consolidated findings and dispositions.

## Reviewers

| Perspective | Verdict | Blocking findings (summary) |
|---|---|---|
| CTO / principal architect | approve_with_changes | apply/rollback is net-new, not existing; OMP "real diff" is false; gate data model undefined; build/tests must be orchestrator-run |
| Staff Product | approve_with_changes | no human-evidence path (needs_manual dead-ends); OMP self-contradiction; acceptance criteria verify mechanics not user value |
| Staff Security | **reject** | external agents run **unsandboxed**; diff integrity left to the agent; autonomy could bypass the human gate |
| Staff Quality | **reject** | apply/rollback unimplemented; criterion/mandatory undefined; gate semantics self-contradictory; hermetic-testability claim false |
| Staff SRE / Platform | **reject** | OMP `--print` writes no files; cancellation is dead code; apply mischaracterized as existing; trust gate is a label not a sandbox; no cost/Run records |
| Head of Design/UX | approve_with_changes | Track C direction right; multi-role pipeline must hide machinery; status-label table incomplete |

**Net verdict: the plan must be amended before implementation.** Three reviewers would reject as
written, and the three "approve_with_changes" reviewers raised the same blocking issues.

## The single meta-finding

The plan's Track A asserts four things "already exist" that **do not**:

1. *"apply writes the file atomically (existing ApplyJournal path)"* — `ApplyChangeSet`/`MarkChangeSetApplied`
   only flip status and insert a journal row (`workspace_snapshot_digest` hardcoded `''`); there is
   **no file I/O, no canonical-workspace path, and revert is a no-op marker.**
2. *"OMP already produces real diffs"* — `OMPExecutor` captures `omp --print` **stdout** as the patch
   and writes no files.
3. *"the capability/trust gate sandboxes the agents"* — `local_restricted`/`Network:false` are
   self-declared labels; OMP/Hermes run as host subprocesses with full user privileges. Only the
   `container` executor has real isolation (`--network none`).
4. *"the gate data model exists"* — `AcceptanceCriterion` has no id or "mandatory" flag;
   `RequirementStatus.verified` and an initiative terminal "done" status do not exist.

## Consolidated blocking findings → resolution

**B1 — Real file apply + rollback is net-new work (CTO, Security, Quality, SRE).**
The plan cites the ApplyJournal as if it already writes files. It does not. **Resolution:** add an
explicit Track A work item — apply the orchestrator-computed diff to the canonical workspace via
per-file temp+rename, capture a pre-image snapshot into the journal *before* mutating, populate
`workspace_snapshot_digest`, implement `RevertChangeSet` as an actual inverse apply from that
snapshot, and drive crash recovery off the `prepared→workspace_mutated→committed→recovery_required`
ApplyState with a kill-at-each-state test. Define the canonical workspace path in config.

**B2 — External agents must be OS-sandboxed, not just label-gated (Security, SRE).**
`local_restricted`/`Network:false` are declared fields, not enforcement; a malicious OMP/Hermes has
the daemon's full OS authority. **Resolution:** run OMP and Hermes inside the existing `container`
executor (`--network none`, writable-only isolated work root, everything else read-only) — or ship an
equivalent OS sandbox — before they are selectable for the real-diff path. Re-scope the capability
broker's claim to harness-internal tool dispatch; add a test that an agent cannot read/write outside
`WorkRoot` or open a socket.

**B3 — The orchestrator computes the diff; the agent never self-declares it (Security, Quality).**
If the implementer "returns" its own `FileChange` list, the patch-applies check is circular and an
agent can omit a file it actually wrote. **Resolution:** the orchestrator captures a content-addressed
base snapshot of the work root *before* the agent runs, then computes `FileChange[]` deterministically
by diffing pre/post. Agent-provided lists/digests are untrusted metadata (or dropped). All downstream
steps (patch-applies, build, tests, secret sweep, apply) use only the orchestrator-computed diff.

**B4 — The verification-gate data model must be defined (CTO, Quality).**
`criterion_id`, "mandatory", `RequirementStatus.verified`, and an initiative terminal status do not
exist. **Resolution:** give each AcceptanceCriterion a stable id and define "mandatory" (all criteria
are mandatory for the thin slice); add a `verification_evidence` migration; add
`RequirementStatus.verified` (settable only when `approved` and coverage == 1.0, cleared on
revert/supersede) and `InitiativeStatus.complete` with a single deterministic, orchestrator-only
transition gated on 100% coverage and no open blocking findings.

**B5 — Build/tests are orchestrator-run, never agent-run (CTO).**
Agents at `local_external` cannot `run_command`, and delegating build/tests to the agent makes the
checks prompt-injectable. **Resolution:** a deterministic, Engine-owned check runner executes
build/type-check/tests against a throwaway post-apply copy of the workspace; commands are
config-supplied (`build_cmd`/`test_cmd`), never model/agent output. This runner is shared by Track B's
verification evidence.

**B6 — Cancellation must be wired (SRE).**
`Executor.Cancel` is never called; `waitForCompletion` does not kill on timeout/failure; a hung agent
keeps spending tokens and writing files, and is orphaned on daemon restart. **Resolution:** call
`Cancel` on every exit path (timeout, ctx cancel, failure); launch subprocesses in their own process
group and kill the group; add a cancel route/`idea cancel`; persist the attempt PID so startup
reconciliation can reap orphans.

**B7 — Cost attribution + durable per-step Run records (SRE).**
`Result` has no usage field; `Execute` never creates a `Run`; `max_cost_usd` is never enforced.
**Resolution:** add an agent `RunKind`, create a `Run` + `RunManifest` per implementer step with
requested/actual model and usage/cost (parsed from CLI metadata, or recorded `unknown`); enforce
`max_cost_usd`/`max_tokens` as a hard stop; add `Result.Usage`.

**B8 — Autonomy must never skip the final write (Security).**
A "High" preset could auto-apply an external agent's diff with only the (injectable) model reviewer as
gate. **Resolution:** the canonical-workspace apply is an **unconditional hard human confirmation** no
preset can skip; autonomy automates advance→execute→verify and surfaces verdicts, never the final
write. Add a deterministic pre-apply secret/credential/pii + sensitive-path scan independent of the
model reviewer.

**B9 — Hermetic testability requires stubbing the new model stages (Quality).**
`FakeProvider` has no case for the engineering_lead brief or verify/evidence schemas. **Resolution:**
define the brief and evidence-mapping JSON schemas, add deterministic `demoResponse` cases for each,
and add a CI test running the full Execute→verify pipeline with `FakeProvider` + `InternalExecutor`.

**B10 — A human evidence path is required (Product).**
UX/security criteria dead-end in `needs_manual` with no way to record a human pass. **Resolution:**
add `idea verify <req> --pass [--note]` and a UI "sign off" affordance that writes
`VerificationEvidence{status: pass, actor: human}`; add a golden fixture for
needs_manual → human pass → gate opens → done.

## Non-blocking findings (fold into the plan)

- **Autonomy preset set** — reconcile PRD §37 (four presets) vs Tech Design §71 (three); enumerate
  per-preset stop conditions so "stops only at judgment points" is testable.
- **Complete status→label table** — human labels for *every* ChangeSet/Initiative status (not just
  three); raw status only in `--json`.
- **Next-action "Why"** — render the PRD §9 `Why` alongside the deterministic next-action line.
- **Per-track definition-of-done** — sequence A → B → C with each independently shippable.
- **Role contract for the brief** — either a distinct planner role or bump `engineering_lead` to v1.1
  with an "implementation brief" artifact + JSON schema.
- **Coverage gate reads structured state** — persist a queryable criterion→evidence-kind mapping at
  verification-design time; the gate must not read the opaque blob.
- **FileChange spec** — path-sorted order, digest = `blob.Digest(bytes)`, Mode carries type/perm;
  document the `Executor` interface break across all four adapters.
- **Config surface for Hermes** — add `hermes` to `ExecutorKind` + schema + `selectExecutor` + the
  per-role override map with fail-closed load validation.
- **Untrusted selector priority** — the `change_set`/`brief`/`source_files` context selectors must
  sort at lowest priority (below `immutable_policy`); add a test.

## Status

Blocking findings: **10, all resolved by amendment.** Plan status: **awaiting human approval to
implement.** Recommended order: B1/B3 (file apply + orchestrator-computed diff) → B2 (sandbox) →
B6/B7 (cancellation + cost/Run) → B4/B10 (gate data model + human evidence) → B5 (check runner) →
Track B → Track C, with B8 (hard apply confirmation) as a gate on the first real workspace write.
