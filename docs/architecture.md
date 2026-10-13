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
