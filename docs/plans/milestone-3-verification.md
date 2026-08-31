# Milestone 3 — Acceptance Criteria & Demo Verification

**Milestone 3 = Plan → safe reviewed ChangeSet** (PRD v1.1 §50; Technical Design §36–§41).

This is what "done" means for M3, and how to verify the demo. It extends — and must not break — the
M1+M2 slice.

## Acceptance criteria

1. **`idea execute` produces a ChangeSet.** On an initiative with an approved execution plan,
   `idea execute` picks the next `ready` Task, runs it in the (deterministic `internal`) executor,
   and produces a `ChangeSet` (patch digest + base snapshot id) — never mutating the canonical
   workspace directly.
2. **ChangeSet state machine.** `draft → checks_running → review_required/rejected → …
   → approved → applying → applied/conflicted` is enforced by domain methods (no arbitrary status
   writes); the formal transition matrix is in code + docs.
3. **Deterministic checks before review.** Patch-applies-cleanly, build/type-check, tests, and a
   secret-in-diff sweep run in `checks_running`; their results are Findings.
4. **Independent review.** Every ChangeSet is reviewed by a reviewer ≠ author; blocking findings →
   `changes_requested`/`rejected`; `EvaluateApproval` (no open critical/high) gates `approved`.
5. **Crash-safe apply.** The ApplyJournal sequence (`prepared → workspace_mutated → committed`) is
   the authoritative record; a killed mid-apply reconciles to `recovery_required` on restart — no
   half-applied state; startup reconciliation runs.
6. **Revert.** `idea revert` restores the base exactly before acceptance; after later changes,
   revert is an inverse ChangeSet; a concurrent user edit is never overwritten by an old snapshot.
7. **Sandbox + capability broker.** Workers never receive the canonical workspace as a writable
   root; `local_restricted` is the tested CI/demo path; capabilities are declared and authorized
   deterministically (a model-suggested grant is rejected).
8. **Threat model + RiskProfile.** A ThreatModel is produced and approved; RiskProfile tiers
   low/medium/high/critical with a deterministic minimum; tier drives review ceremony.
9. **Calm UI + CLI + regression.** Changes tab (status chips, View diff, Approve/Request
   changes/Reject, Apply, Revert) with no raw git output; executor progress (Planning/Editing/
   Testing/Waiting/Reviewing/Finished) with no chain-of-thought; CLI
   `execute/changes/change/apply/revert/risk/executors` (all `--json`); `go test ./...` +
   `go vet ./...` clean; **M1+M2 `smoke.sh` still green**.

## Demo script (do this to verify)

Two terminals recommended. Demo path uses the deterministic `internal` executor + synthetic model.

**Terminal A — build + run the daemon:**

```bash
cd idea-harness
go install ./cmd/idea
export PATH="$PATH:$(go env GOPATH)/bin"

mkdir -p /tmp/idea-demo3 && cd /tmp/idea-demo3
idea init
idea daemon                    # watch per-stage + executor logs
```

**Terminal B — drive the demo:**

```bash
cd /tmp/idea-demo3
idea new "Build a recipe sharing app"     # M1 → needs_human_judgment or approved
idea watch                                # → approved
idea advance                              # M2 → execution plan
idea watch                                # → plan approved
idea execute                              # M3 → ChangeSet (draft → checks → review)
idea changes                              # list ChangeSets (status)
idea change CHG-1                         # detail + rendered diff
idea apply CHG-1                          # → applied (journal committed)
idea revert CHG-1                         # → exact rollback (pre-acceptance)
idea risk                                 # RiskProfile tier
idea executors                            # executors + capabilities
```

**Web UI:** verify the **Changes** tab shows the ChangeSet with status chip, **View diff**, and
review/apply/revert actions; executor progress shows phase states (never chain-of-thought); the
stepper shows `… Design → Plan → Build`.

## What the demo does NOT verify (needs your environment)

- **Docker `container` executor** — implemented as a real `docker run` adapter
  (`--network none`, mounted hand-off file) and selectable via
  `executor: {kind: container}` in `harness.yaml`; the default demo path stays
  `internal` and a full container run needs a local image + Docker daemon.
- **OMP (`local_external`) executor** — implemented as an `omp --print`
  subprocess adapter and selectable via `executor: {kind: omp}`; a real OMP run
  needs the OMP CLI plus a model/API key.
- **Real-model change quality** — the demo executor is deterministic; real
  coding-agent quality needs your Anthropic key and a real model.

## Known M3 limitations (by design)

- **No verification evidence / final gate** — Milestone 4.
- **No generalized executor ecosystem / remote fleet / plugin marketplace.**
- **Git optional** — no automatic `git init/remote/push`; treated as a capability.
- **Thin threat model + risk** — deterministic tier floor only; no numeric risk engine; semantic
  analysis may raise the tier but M3 ships the deterministic floor.
- **No embeddings / semantic retrieval.**

## Integration-review resolutions (M3 hardening)

After the M3 integration pass, these non-blocking review findings were resolved:

- **Content-blind change-set review** → the adversarial reviewer now receives the redacted patch
  text (not just the digest) via `renderChangeSet(cs, patchText)`.
- **Tautological capability gate** → `Execute` authorizes against the deterministic trust-tier
  policy (`executor.TrustAllows`), independent of the executor's self-declared capabilities.
- **Revert did not flip status** → new `reverted` ChangeSet status + `applied→reverted` transition;
  `MarkChangeSetReverted` writes status + rolled_back journal atomically.
- **Deterministic rejection fail-open** → hard-reject set is now secret/credential/pii; auth,
  network, and payment are review-gated and surfaced in the RiskProfile.
- **Risk profile not persisted / error swallowed** → `SaveRiskProfile` persists profiles;
  `handleRisk` propagates the list error.
- **Review-action gating** → Approve/Request-changes only at `review_required`; Reject at
  `checks_running` or `review_required`.
- **Execute button without a ready-task guard** → gated on a `ready`/`proposed` task.
- **Mount-time change-set fetch before an initiative** → `loadChangeSets` no-ops until an
  initiative is loaded.
- **No frontend test harness** → Vitest added; pure status-chip helpers extracted to
  `web/src/statusChips.ts` with unit tests (`web/src/statusChips.test.ts`).
- **Deterministic checks were thin (B6)** → the three diff-dependent checks
  (patch-applies, build/type-check, tests) are now recorded as explicit
  `not_applicable` Findings for changelog-only patches (they switch to their
  runner path once an executor captures a base snapshot + real diff), and the
  secret sweep records a blocking `secret_sweep` finding when it rejects.
