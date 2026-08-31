# Milestone 1 Plan — Adversarial Review Record

Five independent reviewers evaluated
[`docs/plans/milestone-1-idea-to-reviewed-prd.md`](./milestone-1-idea-to-reviewed-prd.md) from the
perspectives required by the constitution (§82). This file records the consolidated findings and
their dispositions. The plan itself (§23) is the authoritative resolution summary.

## Reviewers

| Perspective | Verdict (summary) | Blocking findings |
|---|---|---|
| CTO / principal architect | Architecture sound; schema/workflow are silent on decisions their own sections force. | dissent modeling; structured provenance + producer-run link; human-in-the-loop state machine |
| Staff Product | Right vertical cut; two surfaces contradict invariants (What Changed, dissent); acceptance bar is fake-provider-only. | real-provider acceptance; What Changed as grouped summary; dissent storage/UI |
| Staff Security | Directionally sound; auth token deferred leaves invariant (j) unmet; API unscoped; raw-output/redaction contradiction. | auth token; project-scoped API/SSE; redaction choke point + sequencing |
| Staff Quality | Test plan broad, but review loop pass bar, vagueness check, and disposition vocabulary are underspecified. | approval bar; vagueness-check rule; six-value disposition enum |
| Head of Design/UX | Faithful skeleton; §9 under-specifies the differentiating surfaces. | Initiative Home content model/states; Open Questions entity; Runtime expert-view framing |

## Consolidated blocking findings → resolution

All 13 blocking findings were resolved (see plan §23). Consensus across reviewers (highest-leverage):

1. **Human-in-the-loop state machine** (CTO, Product, Quality, UX) — resolved with explicit statuses,
   approval bar, auto-revise trigger, and `resume`/`approve` endpoints.
2. **Security posture** (Security, CTO) — resolved with per-install token, project-scoped API, and a
   single redaction choke point sequenced early.
3. **Dissent + provenance** (CTO, Product, UX) — resolved with `decision_dissents` and structured
   provenance (`provenance_json` + `produced_by_run_id`).
4. **Review loop spec** (Quality) — resolved with a defined approval bar, deterministic
   structural-verifiability rule, and the constitution's six-value disposition enum.
5. **Real-provider acceptance** (Product, Quality) — resolved with a mandatory real-provider exit
   criterion alongside the deterministic fake provider.

## Non-blocking findings

Deferred by design (out of M1 scope per §2): orphan-blob GC, OS keychain, statement-level provenance
anchors, source-file context ingestion, false-positive/defect-escape *rates*.

Folded into the plan: daemon lifecycle commands; `usage_json`/`read_cursors`; events digest-only;
web-tool SSRF controls; output sanitization; deterministic trust assignment; disposition actor
constraints; reviewer model independence; exact Host/Origin matrix; file permissions; path-traversal
guard; dependency-edge fixes; UI test tier; no-silent-fallback and review-independence tests; canary
leak sweep; concurrency tests; e2e artifact assertions; per-task testing.

## Status

Blocking findings: **resolved**. Plan status: **awaiting human approval to implement.**
