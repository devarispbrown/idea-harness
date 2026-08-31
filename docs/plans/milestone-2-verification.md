# Milestone 2 — Acceptance Criteria & Demo Verification

**Milestone 2 = PRD → reviewed execution plan** (PRD v1.1 §50; Technical Design §12).

This is what "done" means for M2, and exactly how to verify the demo. It extends — and must not
break — the M1 slice, which remains a prerequisite.

## Acceptance criteria

1. **`idea advance` continues from a reviewed PRD.** On an initiative whose PRD is `approved` (or
   past `needs_human_judgment`), `idea advance` launches the Design → Convergence → Execution Plan
   pipeline as an async Operation (returns an `operation_id`; no held HTTP connection).
2. **Three design artifacts, converged.** The run produces an Architecture (`technical_design`),
   a Product Design (`design_spec`), and a Verification Design (`verification_plan`) artifact, then
   a Convergence result — each independently reviewed (reviewer ≠ producer).
3. **Reviewed execution plan.** The run produces an `execution_plan` artifact and ordered `Task`
   rows; every Task has an `implements` edge to a Requirement and either a `depends_on` edge or is a
   root; no untraced/orphan task; the dependency graph is acyclic. The plan passes independent
   adversarial review (`EvaluateApproval`: no open critical/high findings) plus the plan-specific
   `DeterministicPlanChecks`.
4. **Product Graph traceability.** `why / trace / impact` return correct, bounded results
   (depth ≤ 8, node cap ≤ 500): `why` walks the provenance chain to sources; `impact` lists
   dependents with staleness classification (`unaffected / revalidate / revise / obsolete`). A
   `doctor` run reports no object missing its required `derived_from`/`produced_by` edge.
5. **Semantic diff.** `idea diff` between two PRD versions reports structured changes — requirement
   add/remove, priority change, decision supersession, new risk, modified acceptance criterion —
   for requirements and decisions (not raw markdown diff).
6. **Multi-role decision synthesis.** When Architecture, Product Design, and Verification disagree,
   the conflict surfaces as a **single** decision card: per-role recommendations (dissent preserved,
   never erased) + the orchestrator's synthesis as the highlighted default, with Accept / Choose
   alternative / Discuss. "Take recommendations" defaults to the orchestrator synthesis.
7. **Calm UI.** Initiative Home adds Plan (ordered Tasks with status/priority/dependency chips) and
   a Design summary; every Requirement/Task/Decision row has a visible **"Why?"** link to a Why &
   Impact panel (provenance + dependents + staleness); a Tasks tab and multi-role Decisions; no raw
   agent/run machinery on primary surfaces.
8. **CLI parity.** `plan`, `tasks`, `why`, `impact`, `trace`, `diff`, `advance` all work; state
   commands support `--json` (schema = API response).
9. **Security & regression.** New endpoints are loopback + token-authenticated; graph/task/diff
   content is redacted before persistence; `go test ./...` and `go vet ./...` are clean; **the M1
   `smoke.sh` still passes unchanged** (the M1 path is not regressed).

## Demo script (do this to verify)

Two terminals recommended. The demo path uses the deterministic FakeProvider (synthetic, labeled);
a real model is optional.

**Terminal A — build + run the daemon in the foreground (see live logs):**

```bash
cd idea-harness
go install ./cmd/idea          # or: make build
export PATH="$PATH:$(go env GOPATH)/bin"

mkdir -p /tmp/idea-demo2 && cd /tmp/idea-demo2
idea init                      # empty dir, no git
idea daemon                    # leave running — watch per-stage logs
```

**Terminal B — drive the demo:**

```bash
cd /tmp/idea-demo2

idea new "Build a platform that lets someone buy a domain and get a matching email address"
idea watch                      # M1 slice → should end "approved"

idea advance                    # M2 slice: Design → Convergence → Execution Plan
idea watch                      # → should end with an approved execution plan

idea plan                       # ordered execution plan, rendered
idea tasks --json               # ordered Task list (codes, status, dependencies)
idea why requirement REQ-1      # provenance chain back to assertions/sources
idea impact requirement REQ-1   # dependents + staleness classification
idea trace decision DEC-1       # subgraph around a decision
idea diff                       # semantic diff (after a revise; empty on first pass)
idea doctor                     # no missing graph edges
idea status --json              # machine-readable state
```

**Open the web UI:** `idea dev` (or `http://127.0.0.1:8787/`). Verify:

- Initiative Home shows the **Plan** card (ordered tasks) and a **Design** summary (Architecture /
  Product Design / Verification status).
- Each Requirement/Task/Decision row has a **"Why?"** link → Why & Impact panel (provenance tree +
  dependents + staleness chips).
- **Decisions** shows a **multi-role** card for any cross-lane conflict (per-role recommendations +
  orchestrator synthesis + Accept/Choose/Discuss).
- **Tasks** tab lists the plan with dependency toggle.
- **Artifacts → View** opens the technical_design / design_spec / verification_plan / execution_plan.

## What the demo does NOT verify (needs your environment)

- **Real-model quality** — the fake provider is synthetic; a real provider needs an API key
  (`ANTHROPIC_API_KEY` etc. or `.env`). `scripts/smoke-real.sh` (extended for M2) runs the same
  slice against a live model: `IDEA_REAL_MODEL=anthropic/claude-sonnet-5 bash scripts/smoke-real.sh`.
- **Real sources in research** — enable `network: enabled` + `research.urls` in `.idea-harness/harness.yaml`.

## Known M2 limitations (by design)

- **No execution / source-tree mutation** — M2 produces a plan; no sandbox, OMP/executor,
  ChangeSets, or apply journal (that is M3).
- **No threat model document** — still M3 (§68); M2 records security-derived *requirements* only.
- **No final verification gate / evidence** — Milestone 4.
- **Thin Product Graph** — only `derived_from`, `implements`, `depends_on`, `produced_by`,
  `supersedes` edges are wired; the remaining edge types are declared but not written until M3/M4.
- **Semantic diff scope** — requirements and decisions only (tasks/artifacts deferred).
- **No concurrent scheduler** — Design stages are independent in the DAG but executed sequentially
  (single-writer SQLite); true concurrency is M3.
- **No backfill** — initiatives created before M2 are marked `unwired`; `why/trace/impact` on them
  report "unwired" rather than a reconstructed graph.
- **No embeddings / semantic retrieval; no keychain; no numeric risk engine.**
- **Orchestrator synthesis for multi-role decisions** — per-role dissents are recorded and rendered,
  but the orchestrator does not yet run an explicit synthesis pass; the first role's recommendation
  serves as the default with dissents shown alongside.
- **`doctor` graph-edge audit** — untraced tasks are flagged by the plan review
  (`DeterministicPlanChecks`), but `idea doctor` does not yet run a standalone missing-edge audit.
