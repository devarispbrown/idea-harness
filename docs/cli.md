# CLI Reference

The `idea` binary is both the CLI and the daemon. State-returning commands support `--json`.
Output is styled with [Charm.sh](https://charm.sh) (Lip Gloss, Huh, Glamour, Bubble Tea) when
stdout is a terminal, and falls back to plain text when piped or passed `--json`.

```text
idea init              initialize a project in the current directory (no Git required)
idea new "<idea>"      create an initiative and start the workflow (interactive if run without args)
idea status            show initiative + current work
idea changed           show What Changed since last look
idea decisions         list open/blocked decisions
idea reviews           list review findings
idea model             list/set model providers (Gemini, OpenAI, Anthropic, OpenRouter, Ollama)
idea model ls <p>      list a provider's models LIVE (falls back to the catalog offline)
idea model set <p/m>   set the default model, e.g. idea model set openai/gpt-4o-mini
idea model add <name>  add a provider (--type, --base-url, --key-env)
idea prd [kind]        render the PRD (or idea_brief / research_brief) with Glamour
idea explain           show the synthesized Current Understanding
idea export [kind]     write an artifact as markdown to stdout (> prd.md)
idea watch [id]        watch a run live with a Bubble Tea spinner
idea config            show the current model configuration
idea config validate   validate .idea-harness config
idea doctor            check daemon/config/state health
idea logs [n]          show the last n lines of the daemon log
idea logs -f           follow the daemon log live (tail -f)
idea dev               start the daemon and serve the web UI
idea daemon            run the daemon in the foreground
idea daemon status     check daemon health
idea daemon stop       stop the managed daemon
```

## Watching progress in real time

The daemon logs **every model request** (stage, role, model, duration) and every review cycle to
`~/.local/share/idea-harness/daemon.log`. To watch it live:

```bash
idea logs -f        # tail the log
# or run the daemon in the foreground in its own terminal:
idea daemon         # then run `idea new` in another terminal
```

The web UI shows a **Live activity** feed (same events, via SSE).

## How it works

The CLI is a thin HTTP client to the daemon (§29, D1). On the first state command it auto-starts a
detached daemon if none is running, then retries. The daemon is the single writer; the CLI never
mutates state directly.

Environment variables:

```text
IDEA_HARNESS_STATE   override the global state directory (default ~/.local/share/idea-harness)
IDEA_HARNESS_ADDR    override the daemon address (default 127.0.0.1:8787)
```

## Configuring providers

```bash
# List providers and their status (ready / missing key / not configured).
idea model

# Set the default model (provider/model).
idea model set gemini/gemini-2.5-flash

# Add a custom OpenAI-compatible provider.
idea model add groq --type openai --base-url https://api.groq.com/openai/v1 --key-env GROQ_API_KEY
```

Provider configuration is also available in the web UI under **Settings**.

## Example

```bash
mkdir my-product && cd my-product
idea init
idea model set ollama/llama3.1      # point at a local model
idea new                            # interactive Huh form
idea watch                          # follow the run live
idea prd                            # read the reviewed PRD in your terminal
```

The workflow runs asynchronously (Operations); `idea new` returns an operation id immediately, and
`idea watch` follows it to completion.
