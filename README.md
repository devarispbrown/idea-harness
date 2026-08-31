# idea-harness

A local-first AI product-development control plane that takes an idea from ambiguity to verified
software using a coordinated team of Staff-level AI experts.

**Status:** Milestone 1 (Idea → Reviewed PRD) implemented end-to-end.

```text
empty folder → idea init → idea new → framing → research → reviewed PRD
```

## What it is

idea-harness is **not** a coding agent, chatbot, Kanban, or project-management tool. It is a
local-first product-development operating system: agents reason, the engine governs, experts
challenge, and the human decides what matters.

The durable asset is a **Product Graph** (Evidence → Assertions → Requirements → Decisions →
Plans → Tasks → ChangeSets → Tests → Verification) that powers provenance, impact analysis, and
requirement verification.

## Install

### Prerequisites

- **Go 1.25.5 or newer** (`go version` — the module requires it).
- **Node.js + pnpm** — *optional*, only needed to rebuild the web UI from `web/`. The
  binary already embeds a built UI, so most users can skip this.

### Build & put `idea` on your PATH

```bash
cd idea-harness

# Option A — install into your Go bin directory (recommended):
go install ./cmd/idea
export PATH="$PATH:$(go env GOPATH)/bin"     # add to your shell profile to persist

# Option B — build in-place (also rebuilds the web UI):
make build                                   # produces ./bin/idea
export PATH="$PWD/bin:$PATH"
```

Verify:

```bash
idea doctor
```

> No daemon setup is required. The CLI auto-starts a local daemon on first use; `idea dev`
> runs it in the foreground and serves the web UI at `http://127.0.0.1:8787/`. All runtime state
> lives under `~/.local/share/idea-harness/` — never in your source tree.

## Quick start

```bash
mkdir my-product && cd my-product
idea init                              # initializes .idea-harness/ (works with no Git)
idea model set openai/gpt-4o-mini      # pick a model (or use the web UI Settings)
idea new "Build usage-based billing"   # starts framing → research → reviewed PRD
idea watch                             # follow the run live (Bubble Tea spinner)
idea prd                               # read the reviewed PRD in your terminal (Glamour)
idea status                            # watch progress (supports --json)
idea changed                           # What Changed summary
idea decisions                         # open/blocked decisions
idea dev                               # start the daemon and open the web UI
```

The daemon runs on `127.0.0.1:8787` and serves the web UI at `http://127.0.0.1:8787/`.

## Model providers

idea-harness supports **OpenAI, Anthropic, Google Gemini, OpenRouter, and Ollama (local)** — plus
any custom OpenAI-compatible endpoint (Groq, Together, LM Studio, …). Configure via the CLI or the
web UI (Settings):

```bash
idea model                        # list providers + status
idea model set gemini/gemini-2.5-flash
idea model add groq --type openai --base-url https://api.groq.com/openai/v1 --key-env GROQ_API_KEY
```

API keys are read from environment variables (never stored inline):

```yaml
# .idea-harness/harness.yaml
default_model: openai/gpt-4o-mini
providers:
  openai:    { api_key_env: OPENAI_API_KEY }
  anthropic: { api_key_env: ANTHROPIC_API_KEY }
  gemini:    { api_key_env: GOOGLE_API_KEY }
  openrouter: { api_key_env: OPENROUTER_API_KEY }
  ollama:    {}                      # local, no key
```

Without a configured provider (or key), idea-harness runs in a **demo mode** that responds to your
idea with clearly-synthetic placeholder output (labeled in the UI and CLI). To do real research and
produce a real PRD, configure a provider and — optionally — research seed URLs (see
`docs/configuration.md`).

## Repository layout

```text
cmd/idea/        CLI + daemon entrypoint
internal/        Go packages (domain, store, blob, model, workflow, review, daemon, …)
web/             React + TypeScript UI (built and embedded into the binary)
docs/            architecture, configuration, cli, security, plans
```

## Documentation

- [`docs/plans/milestone-1-idea-to-reviewed-prd.md`](docs/plans/milestone-1-idea-to-reviewed-prd.md) — the approved M1 plan.
- [`docs/architecture.md`](docs/architecture.md) — architecture and system areas.
- [`docs/configuration.md`](docs/configuration.md) — `.idea-harness` config reference.
- [`docs/cli.md`](docs/cli.md) — CLI reference.
- [`docs/security.md`](docs/security.md) — security model and invariants.

## Development

```bash
make test     # run the Go test suite
make vet      # go vet
make build    # build web UI + binary
```

See [`CONTRIBUTING.md`](CONTRIBUTING.md) for the process (plans are adversarially reviewed before
implementation; every ChangeSet is independently reviewed).
