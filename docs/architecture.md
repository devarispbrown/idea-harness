# Architecture

idea-harness is a local-first AI product-development control plane. It is organized into five major
system areas (§28):

```text
Product Orchestrator
Agent Runtime
Secure Execution
Change Control
Evidence & Observability
```

Milestone 1 implements the **Idea → Reviewed PRD** vertical slice. This document describes what
exists now; later milestones extend it without rearchitecting the foundations.

## Stack

| Concern | Choice |
|---|---|
| Daemon / CLI | Go (single `idea` binary) |
| Web UI | React + TypeScript (Vite), built by `make build` and embedded into the binary |
| Database | SQLite (pure-Go `modernc.org/sqlite`), WAL, single writer |
| Large content | content-addressed BlobStore (`blobs/sha256/<digest>`) |
| API | REST, described by [`openapi.yaml`](openapi.yaml), CI-checked against the routes — see [`api.md`](api.md). An SSE endpoint exists and nothing in-tree consumes it |
| Config | YAML + JSON Schema |
| Model | native adapters (OpenAI, Anthropic, Gemini) + OpenAI-compatible (OpenRouter, Ollama, custom) + fake provider |
| CLI | Charm.sh (Lip Gloss, Huh, Glamour, Bubble Tea) |
| MCP | stdio server (`idea mcp`), a client of the same API — see [`mcp.md`](mcp.md) |

## Process model

The workflow is a deterministic stage graph, not a waterfall (§1):

```text
M1  framing ──► research ──► product_definition ──► review ──► approved
                    ▲                                   │
                    └─────────── revise (≤ max_cycles) ──┘
                                        │ did not converge
                                        ▼
                              needs_human_judgment
                        (idea approve · idea revise)

M2  architecture ──► product_design ──► verification_design ──► convergence
      ──► execution_plan ──► execution_plan_review ──► plan_approved
                                        │ blocked
                                        ▼
                              needs_human_judgment
                   (idea revise-plan · idea approve-plan)

M3  implementation_brief ──► change set ──► review ──► apply
M4  verify ──► complete
```

The eleven stage keys are the constants in `internal/workflow/engine.go`.
`needs_human_judgment` is one status with four recorded reasons
(`domain.JudgmentReason`), which is what lets `idea next` name a command rather
than guess from which artifacts exist.

- Each stage produces a versioned Artifact + structured entities (Assertions, Requirements,
  Decisions, Questions), recorded via Run → RunManifest → ContextBundle.
- `product_definition` also states the initiative's **objectives** (`OBJ-n`) and their outcome
  **metrics** (`MET-n`), and links each requirement to the objectives it serves. Observations of
  those metrics close the loop after `verify`: drift is computed deterministically on read
  (`domain.EvaluateMetricDrift`), and an observation records which change sets landed since the
  last one, so the graph runs from objective to outcome. See the README's "Objectives & outcomes".
- Review is **mandatory**: deterministic checks run first, then an adversarial
  reviewer. The approval bar is "no open critical/high findings" that cite what they are about.
  From cycle 2 the reviewer sees the previous cycle's findings and what the revision did about
  them; re-raises are de-duplicated, and a loop whose blocking count stops falling pauses into
  `needs_human_judgment` early (§11; `review.convergence` in [configuration.md](configuration.md)).
  Each cycle's convergence record is stored on the reviewer's RunManifest.
- Recommended Next Move is generated from deterministic candidates; the orchestrator only ranks
  and explains within workflow policy (§6).

## Packages

```text
internal/domain      typed entities + controlled vocabularies (no I/O, no LLM)
internal/store       SQLite + migrations + atomic state+event transaction
internal/blob        content-addressed BlobStore (two: `blobs/` redacts on Put, `raw-blobs/` does not)
internal/config      .idea-harness config load/validate (JSON Schema)
internal/security    redaction, path guard, sanitization, token, trust
internal/roles       Role Contracts (competencies/behaviors/rubrics/checks)
internal/context     deterministic Context Compiler (trust levels, no embeddings)
internal/model       provider registry (OpenAI/Anthropic/Gemini/OpenRouter/Ollama) + structured-output pipeline
internal/review      deterministic checks + approval bar + findings
internal/workflow    stage graph + state machine + orchestrator
internal/daemon      token-authenticated, project-scoped REST; an SSE endpoint exists, unused by the CLI and UI
internal/integrations  integration event bus: tails the event log (the SSE endpoint's source) into a
                     SQLite outbox; Slack (Socket Mode) and Linear (polling) subscribers; see integrations.md
internal/projection  Current Understanding / Next Move / What Changed
internal/cli         thin HTTP client + Charm-styled commands (Lip Gloss, Huh, Glamour, Bubble Tea)
internal/mcp         stdio MCP server protocol (`idea mcp`); internal/cli supplies its tools
internal/web         embedded React UI
```

## Asking the graph: gaps

`idea why`, `idea impact` and `idea trace` answer questions about one node. `idea gaps` asks the
whole Product Graph what is missing or no longer holds, and names the command that closes each
answer. It is a projection (`projection.BuildGaps`): deterministic, no model call, no score, and it
never writes. Every trigger below is a comparison of recorded state — an edge, a status, an event's
sequence number, a timestamp in the apply journal — so the same graph always gives the same report.

```bash
idea gaps                                   # the newest initiative, grouped by kind, most severe first
idea gaps --kind unverified_requirement     # one kind (comma-separate several)
idea gaps --all --json                      # every initiative, machine-readable
idea gaps --fail-on high                    # CI gate: exit 5 (gate_unmet) on any high or critical gap
```

```text
Gaps · 3 (1 high · 1 medium · 1 low)

unimplemented_requirement · 1
  high   REQ-3  REQ-3 is planned (TASK-2) but no change set implementing it has been applied
         → idea apply CHG-2

unverified_requirement · 1
  medium REQ-1  pass evidence for REQ-1/AC-1 predates CHG-4 changing the code it verified (…)
         → idea verify REQ-1 --pass

unvalidated_assumption · 1
  low    3f9a1c0e  assumption 3f9a1c0e was never validated
         → idea assumption 3f9a1c0e accept|dispute|retract
```

The same report is `GET …/initiatives/{id}/gaps` (and `GET /projects/{projectId}/gaps` for every
initiative), the MCP `gaps` tool, and the **Product gaps** list on the web UI's Health page.
`/guidance` carries the counts as `gap_summary`, which `idea status` prints as one line.

"In scope" below means a requirement that is live (not removed, not superseded by a PRD revision)
and `approved`, `verified` or `failed` — not a draft nobody approved, not one a human waived. A
change set "implements" a task only when it is `applied` and changed at least one file
(`ChangeSet.ImplementsTask`: an applied no-op implements nothing).

| Kind | State | Fires when | Severity |
|---|---|---|---|
| `unimplemented_requirement` | `no_task` | an in-scope, not-yet-verified requirement has no live (not cancelled, not superseded) task with an `implements` edge to it | by priority: must high, should medium, could low; **low** while no execution plan exists |
| | `not_applied` | it has implementing tasks, and none of them has an applied, non-empty change set | by priority |
| `unverified_requirement` | `evidence_failed` | the requirement's status is `failed`, or some criterion's latest decisive evidence (`pass`, `fail`, `blocked` or `waived`; a `needs_manual` placeholder decides nothing) is `fail` or `blocked`, or — when mapped-test evidence is available — a test mapped to it last failed | high |
| | `evidence_stale` | a criterion's latest decisive evidence is a `pass` recorded *before* the most recent apply or revert of a non-empty change set whose task implements or verifies the requirement (apply-journal time, strictly later) | medium |
| | `no_criteria` | the requirement has no acceptance criteria — the gate needs a pass per criterion and `--waive` refuses it, so it can never be verified | high |
| `untested_acceptance_criterion` | `no_evidence` | a criterion of an in-scope requirement has no decisive evidence at all | medium for a must requirement already implemented, else low |
| `unvalidated_assumption` | `unvalidated` | a live assumption or hypothesis (facts are not reported) is still `candidate` | medium if any live node rests on it, else low |
| | `disputed` | it is `disputed` | same |
| | `refuted` | it is `retracted` and live nodes still rest on it | high |
| `invalidated_decision` | `dependents_stale` | the decision has active stale marks — it was resolved or reopened after requirements, tasks or change sets were built on it, and they have not been re-planned or acknowledged. The same mark `idea impact` reports as `stale` | high when a task or change set is marked, medium when only requirements are |
| | `requirement_revised` | it is decided, and a live requirement with a `derived_from` edge to it has a `requirement.revised` event with a higher sequence number than the decision's last `decision.resolved` (or `decision.created`) | medium |
| | `basis_refuted` | it is decided, and an assertion it derives from or that `supports` it is `retracted` or `disputed`, or any node `invalidates` or `contradicts` it (the edges `idea impact` calls obsolete) | high |
| | `requirements_removed` | it is decided, requirements cited it, and every one of them has since been removed or superseded | low |
| `orphan_task` | `no_requirement` | a live task has no `implements` or `verifies` edge to a live requirement | medium |
| `orphan_change_set` | `task_missing` | a change set still holding work (not rejected or superseded) names a task that no longer exists | medium if applied, else low |
| | `task_orphaned` | its task is an `orphan_task` | same |
| `stale_artifact` | `upstream_newer` | the newest version of `technical_design`, `design_spec` or `verification_plan` is strictly older than the newest PRD, or the newest `execution_plan` is strictly older than any of those four | medium |

"Rests on" (`dependents`, the ranking key within a severity) is the set of distinct live nodes
reachable through incoming edges — what derives from, implements, verifies or depends on the node —
plus the targets of its own `supports` edges, plus the change sets still holding work of every task
reached, bounded like `idea impact` (depth 8, 500 nodes). Provenance edges (`produced_by`,
`consumes`) are not dependence.

What it deliberately does not do:

- **No file-area heuristics.** A decision does not record which files it governs, so "a later change
  set touched what this decision governs" is not a trigger; only the recorded edges and events are.
- **Ties are fresh.** Evidence and an apply in the same millisecond, or two artifacts with the same
  timestamp, are not stale.
- **Some edges have no writer yet.** Nothing in the workflow links an assertion to a decision, writes
  `supports`, `invalidates` or `contradicts`, or sets a requirement's source assertion, so
  `basis_refuted` and assertion dependents are exercised only by tests today; the rules are in place
  for when those edges are recorded. Framing and research assumptions therefore report as
  unvalidated with no dependents (low) until judged with `idea assumption <id>
  accept|dispute|retract` — the only path that moves an assertion out of `candidate`.
- **Guidance counts omit mapped tests.** `gap_summary` is computed without the mapped-test parse
  (it needs every check output); `idea gaps` includes it, so its verification counts can be higher.

New kinds slot in as one more analyzer in `projection.gapAnalyzers` plus a `domain.GapKind`
constant and its spec enum value; nothing that renders, filters or gates on gaps switches on the
kind.

## Key invariants

- **Relational state is authoritative**; events are appended atomically in the same SQLite
  transaction (§32). Not event sourcing.
- **Integrations observe, they never sit in the write path.** Slack and Linear are fed by a
  dispatcher that tails the event log after the fact, through a persistent outbox; anything they
  *do* (a Slack approve, a Linear import) goes back in through the same HTTP handlers the CLI uses
  ([integrations.md](integrations.md)).
- **Single writer**: the daemon owns all state. The CLI is a thin HTTP client that auto-starts a
  managed daemon (§29, D1).
- **Source is written only by an approved apply**: `.idea-harness/` is the only thing written as a
  side effect of running the tool. Change sets do write into the working tree, but only through
  `idea apply` (or the MCP server's `apply_change_set`, which exists only when `idea mcp` is
  started with `--allow-apply`) after a review verdict, and only via the journal that makes `idea revert`
  byte-for-byte. Git is optional and never auto-initialized (§24).
- **Structured output is centralized**: model response → schema validation → bounded repair →
  validated object (§39).
- **No silent model fallback**: every RunManifest records requested vs actual model (§36).

## Data placement (§30)

```text
~/.local/share/idea-harness/
  global.db            project registry + daemon meta
  token                per-install API token (0600)
  projects/<id>/
    state.db           per-project relational state
    blobs/             content-addressed blobs, redacted on write
    raw-blobs/         change-set file content, apply pre-images, image bytes — deliberately NOT redacted
                       (see security-model.md#what-this-does-not-protect-against)
```

Portable config lives in the user's project under `.idea-harness/` (project.yaml, harness.yaml,
roles/, policies/, …). It is the only thing written as a side effect of running the tool; the other
writes into the working tree are applied change sets, which require a human approval and are
reversible through the apply journal.
