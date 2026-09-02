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
| API | REST + SSE (OpenAPI planned) — see [`api.md`](api.md) |
| Config | YAML + JSON Schema |
| Model | native adapters (OpenAI, Anthropic, Gemini) + OpenAI-compatible (OpenRouter, Ollama, custom) + fake provider |
| CLI | Charm.sh (Lip Gloss, Huh, Glamour, Bubble Tea) |

## Process model

The workflow is a deterministic stage graph, not a waterfall (§1):

```text
framing ──► research ──► product_definition ──► review ──► approved
                ▲                                    │
                └──────────── revise (≤3) ───────────┘
```

- Each stage produces a versioned Artifact + structured entities (Assertions, Requirements,
  Decisions, Questions), recorded via Run → RunManifest → ContextBundle.
- Review is **mandatory**: deterministic checks run first, then an independent adversarial
  reviewer. The approval bar is "no open critical/high findings." Non-convergence pauses into
  `needs_human_judgment` (§11).
- Recommended Next Move is generated from deterministic candidates; the orchestrator only ranks
  and explains within workflow policy (§6).

## Packages

```text
internal/domain      typed entities + controlled vocabularies (no I/O, no LLM)
internal/store       SQLite + migrations + atomic state+event transaction
internal/blob        content-addressed BlobStore (redaction before Put)
internal/config      .idea-harness config load/validate (JSON Schema)
internal/security    redaction, path guard, sanitization, token, trust
internal/roles       Role Contracts (competencies/behaviors/rubrics/checks)
internal/context     deterministic Context Compiler (trust levels, no embeddings)
internal/model       provider registry (OpenAI/Anthropic/Gemini/OpenRouter/Ollama) + structured-output pipeline
internal/review      deterministic checks + approval bar + findings
internal/workflow    stage graph + state machine + orchestrator
internal/daemon      token-authenticated, project-scoped REST + SSE
internal/projection  Current Understanding / Next Move / What Changed
internal/cli         thin HTTP client + Charm-styled commands (Lip Gloss, Huh, Glamour, Bubble Tea)
internal/web         embedded React UI
```

## Key invariants

- **Relational state is authoritative**; events are appended atomically in the same SQLite
  transaction (§32). Not event sourcing.
- **Single writer**: the daemon owns all state. The CLI is a thin HTTP client that auto-starts a
  managed daemon (§29, D1).
- **No source mutation in M1**: only `.idea-harness/` is written into the working tree. Git is
  optional and never auto-initialized (§24).
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
    blobs/             content-addressed blobs
```

Portable config lives in the user's project under `.idea-harness/` (project.yaml, harness.yaml,
roles/, policies/, …) and is the only thing idea-harness writes into the working tree.
