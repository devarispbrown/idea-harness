# Configuration

idea-harness reads portable config from `.idea-harness/` in the project root. Runtime state is
**never** stored here — it lives under `~/.local/share/idea-harness/` (§30).

`idea config validate` checks the files against embedded JSON Schemas and rejects inline secrets.
You can also configure providers and the default model from the web UI (**Settings**) or the CLI
(`idea model`), both of which write `harness.yaml` for you.

## `project.yaml`

```yaml
name: my-product   # project display name
```

## `harness.yaml`

```yaml
# Network policy v0.1: disabled | enabled (default: disabled).
network: disabled

# Review budget (§11, §16).
review:
  max_cycles: 3        # maximum revise cycles before human judgment
  max_tokens: 0        # optional token budget (0 = unlimited)
  max_cost_usd: 0      # optional cost budget (0 = unlimited)

# Research sources (fetched — public addresses only — during research when
# network is enabled). Offline/empty means the researcher marks everything as
# an assumption rather than a fact.
research:
  urls:
    - https://example.com/domain-research
    - https://en.wikipedia.org/wiki/Your_topic

# Default model as "provider/model" (first slash separates provider from model).
default_model: openai/gpt-4o-mini

# Model providers. API keys are resolved from the environment variable named by
# api_key_env — NEVER stored inline (§47).
providers:
  openai:
    api_key_env: OPENAI_API_KEY
  anthropic:
    api_key_env: ANTHROPIC_API_KEY
  gemini:
    api_key_env: GOOGLE_API_KEY
  openrouter:
    api_key_env: OPENROUTER_API_KEY
  ollama: {}            # local; no key required
  # Any custom OpenAI-compatible endpoint (Groq, Together, LM Studio, …):
  groq:
    type: openai
    api_key_env: GROQ_API_KEY
    base_url: https://api.groq.com/openai/v1

# Create a git commit (with repository hooks disabled) for every applied change
# set, on top of the byte-for-byte snapshot revert. Off by default (§M7).
git_commit: false
```

Top-level fields:

| Field | Meaning | Default |
|---|---|---|
| `network` | `disabled` \| `enabled` — whether research may fetch `research.urls` | `disabled` |
| `review.max_cycles` | maximum revise cycles before the work goes to human judgment | `3` |
| `review.max_tokens`, `review.max_cost_usd` | optional per-review budgets (`0` = unlimited) | `0` |
| `research.urls` | seed URLs fetched during research when `network: enabled` | none |
| `default_model` | `provider/model` used by every role without an override | unset — deterministic demo output |
| `models` | per-role overrides, role name → `provider/model` | falls back to `default_model` |
| `providers` | provider map (fields below) | none |
| `executor.kind` | `internal` \| `container` \| `omp` \| `hermes` | `internal` |
| `executor.image` | container image for the non-internal kinds | `alpine:3.20` |
| `executor.model` | coding model for the agent kinds (`omp`, `hermes`) | — |
| `check.image` | image holding the project toolchain for the build/test checks | — |
| `build_cmd`, `test_cmd` | deterministic check commands; config-supplied, never model output | unset — the check is recorded `not_applicable` |
| `check_timeout_seconds` | per-check timeout | `600` |
| `git_commit` | also create a git commit (repository hooks disabled) for each applied change set | `false` |

`git_commit` is off by default on purpose: a repository hook would run arbitrary code in the
daemon's environment, so committing is never implied. Applied change sets are always revertible from
the snapshot regardless of this setting — the commit is an extra, not the mechanism. The web UI
exposes the same switch under **Settings**.

Each provider entry supports:

| Field | Meaning | Default |
|---|---|---|
| `type` | `openai` \| `anthropic` \| `gemini` | derived from the key |
| `api_key_env` | environment variable holding the API key | derived from the key |
| `base_url` | endpoint override | derived from the key |
| `models` | extra model names to show in the picker | catalog defaults |

`base_url` must be `https`, or `http` on loopback (`localhost`, `127.0.0.1`, `::1`) for local
runtimes such as Ollama and LM Studio. The built-in presets' endpoints may not be overridden: a
preset name keeps inheriting its well-known `api_key_env`, so repointing it would send that key and
the full prompt context to another host. Point a differently-named provider at the endpoint instead.
Keyless local presets (`ollama`) may still move between loopback ports. `idea config validate`
reports a `harness.yaml` that breaks these rules, and the API rejects the write.

## Supported providers

| Provider | Wire type | Default endpoint | Key env |
|---|---|---|---|
| OpenAI | `openai` | `https://api.openai.com/v1` | `OPENAI_API_KEY` |
| Anthropic | `anthropic` | `https://api.anthropic.com/v1` | `ANTHROPIC_API_KEY` |
| Google Gemini | `gemini` | `https://generativelanguage.googleapis.com/v1beta` | `GOOGLE_API_KEY` |
| DeepSeek | `openai` | `https://api.deepseek.com/v1` | `DEEPSEEK_API_KEY` |
| OpenRouter | `openai` | `https://openrouter.ai/api/v1` | `OPENROUTER_API_KEY` |
| Ollama (local) | `openai` | `http://localhost:11434/v1` | — |

DeepSeek, OpenRouter, Ollama, and custom providers all speak the OpenAI-compatible chat-completions
wire format, so they are configured as `type: openai` with a custom `base_url`.

## Choosing a model

The model is a `provider/model` string. Examples:

```text
openai/gpt-4o-mini
anthropic/claude-3-7-sonnet-latest
gemini/gemini-2.5-flash
deepseek/deepseek-chat
openrouter/anthropic/claude-3.7-sonnet   # note: OpenRouter model ids contain slashes
ollama/llama3.1
groq/llama-3.3-70b-versatile
```

Splitting is on the **first** slash, so `openrouter/anthropic/claude-3.7-sonnet` resolves to the
`openrouter` provider with model `anthropic/claude-3.7-sonnet`.

Set it via:

```bash
idea model set openai/gpt-4o-mini
# or interactively / via the web UI (Settings)
```

### Live model listing

To see a provider's **current** models (not the bundled suggestions):

```bash
idea model ls openrouter      # OpenRouter is public (no key needed)
idea model ls ollama          # your locally installed Ollama models
idea model ls openai          # needs OPENAI_API_KEY set
```

The web UI has a **"Load latest models"** button under Settings → Model. When a provider can't
enumerate models (no key / offline), idea-harness falls back to a bundled catalog of suggestions —
but any model id is accepted, so you can always type the exact model name your provider supports.

## Research & evidence (§46)

Research fetches seed URLs from `research.urls` when `network: enabled`. Fetches are restricted to
**public addresses only** (loopback, private, link-local, and cloud-metadata ranges are blocked),
are capped in size/time, and limited to 5 redirects. Fetched content is stored content-addressed as
evidence and injected as untrusted context.

With `network: disabled` (the default) or no URLs configured, the research stage runs offline and
marks every claim as an **assumption** with source `"model knowledge, unverified"` — never a Fact.

## Secrets

- Secrets are resolved from the process environment, with a **`.env` file in the project root** as
  a fallback (OS keychain and project-scoped secret adapters are later milestones). Example
  `.env`:

  ```dotenv
  DEEPSEEK_API_KEY=sk-...
  OPENAI_API_KEY=sk-...
  ```

  The process environment takes precedence over `.env`; `.env` values are never written to config
  or state, only read for API calls and redaction.
- `config validate` and the config API reject any key that would store a secret **value** inline
  (e.g. `api_key`, `password`, `token`); `*_env` references are permitted.
- Secret values are redacted before any persistence (`[REDACTED]`), including logs, events, blobs,
  and context bundles.

## Role Contracts

Bundled Role Contracts live in `.idea-harness/roles/*.yaml`. Each declares competencies, required
behaviors, artifact expectations, rubrics, required checks, and tool requirements (§9).
