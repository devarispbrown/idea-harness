# Wire a real coding agent

This guide explains which executor `idea execute` uses by default, how to choose a real coding
agent (Hermes, OMP, any Agent Client Protocol agent, or an OpenHands Agent Server), and — critically — the security tradeoff
you accept when you do.

## The default: `auto`, with a loud placeholder fallback

Out of the box, `harness.executor.kind` is unset, which means `auto`. Each run resolves it:

- **Docker reachable and the coding model's provider key set** → `hermes`, on the digest-pinned
  image (see [Select OMP or Hermes](#select-omp-or-hermes)). Docker pulls the image on the first
  run if it is not present.
- **Anything else** → `internal`, the placeholder executor, with a warning that starts
  `NO REAL CODE WILL BE WRITTEN` and says why (no model configured, the key missing from both the
  daemon's environment and `.env`, Docker not running, or a keyless provider such as Ollama, which
  auto never picks) and how to fix it.

The warning is not silent anywhere: `idea execute` and `idea next` print it in red, `idea doctor`
prints it as `⚠ executor:` (a warning; doctor still exits 0), the Build page's guidance carries it
as `executor_resolution.warning`, `GET /executors` carries it on the row that will run, the change
set's agent output starts with it, and the run manifest records `requested: auto, resolved:
internal` with the warning text.

The internal executor is deterministic and hermetic: no model calls, no network I/O, no external
services. It writes a placeholder file into the isolated work root so the change-set pipeline
(snapshot → diff → review → apply → revert) runs end to end, but it does **not** write real code.
To keep it on purpose — a demo, a CI run — set `executor.kind: internal`; runs then print only a
one-line note instead of the warning.

**Projects created before auto existed** have `kind: internal` written into `harness.yaml` by
`idea init`, so they keep the placeholder. Delete the line (or set `kind: auto`) to opt in.

To choose a specific agent, select its executor kind below.

## Select OMP or Hermes

Two coding agents are wired behind the same executor interface: **OMP** and **Hermes** (Nous
Research). Both run inside an isolated Docker container, never as a host subprocess, and both write
files into the mounted work root; the engine snapshots that directory before/after to compute the
diff.

### One command (recommended)

```bash
# Hermes (default) — auto-downloads the pinned official image
idea executors setup --model anthropic/claude-sonnet-5 --yes

# Hermes with a custom/self-built image
idea executors setup --image ghcr.io/your-org/hermes:latest --model anthropic/claude-sonnet-5 --yes

# OMP (bring your own image — there is no canonical OMP image)
idea executors setup --kind omp --image ghcr.io/your-org/omp:latest --model anthropic/claude-sonnet-5 --yes
```

`idea executors setup` verifies the agent CLI is present in the image (hardened, no network, no
pull), prints the security tradeoff, and writes `executor.kind/image/model` to `harness.yaml`.
For **Hermes** `--image` is optional: when omitted it downloads the pinned official image
(`nousresearch/hermes-agent:<version>@sha256:<digest>`) and prints the pulled image id. The
default is pinned by **digest**, not only by tag: a tag can be re-pointed at different bytes, a
digest cannot. After the pull, the image's `RepoDigests` must contain that digest for that
repository, or setup refuses it (a registry mirror rewriting images is the usual cause). Any
`--image` given as `repo[:tag]@sha256:…` gets the same check; a tag-only `--image` is used as
given. For **OMP** `--image` is
required. `--model` is optional if a default model is already configured. There is **no silent
fallback** — pass `--kind omp` explicitly to choose OMP.

Add `--smoke` (with `--model`) to also run a trivial coding task through the agent and assert it
produces a real file change — this verifies the agent CLI contract end-to-end, not just that the
binary exists:

```bash
idea executors setup --kind hermes --model anthropic/claude-sonnet-5 --smoke --yes
```

### Choose a provider and model

Hermes works with all six providers. Pass the model as `provider/model`. The agent receives only
that provider's key. The daemon reads it from its own environment, then from the project's `.env`,
the same way it does for every other model call. If a keyed provider's key is set in neither place,
the run stops before the agent starts, and the error names the variable.

| Provider | `--model` example | Key the agent receives | Verified |
|---|---|---|---|
| OpenAI | `openai/gpt-5.6-luna` | `OPENAI_API_KEY` | smoke passed, 20 s |
| Anthropic | `anthropic/claude-sonnet-5` | `ANTHROPIC_API_KEY` | smoke passed, 15 s |
| Google Gemini | `gemini/gemini-3.6-flash` | `GOOGLE_API_KEY` | smoke passed, 17 s |
| DeepSeek | `deepseek/deepseek-v4-pro` | `DEEPSEEK_API_KEY` | smoke passed, 16 s |
| OpenRouter | `openrouter/anthropic/claude-sonnet-5` | `OPENROUTER_API_KEY` | smoke passed, 16 s |
| Ollama (local) | `ollama/qwen3.8:latest` | none | smoke passed, 46 s to 7 min |

"Verified" means `idea executors setup --kind hermes --model <that> --smoke --yes` produced a
real file on 2026-09-17, against the pinned image `nousresearch/hermes-agent:v2026.5.16` (since
pinned by its index digest, `sha256:b6e41c15…`; the bytes are the same). Times
are for the whole command, with the image already pulled. Ollama's depends on the machine and on
whether the model is already loaded: two runs on the same machine took 46 s and 429 s. Any
model the provider serves works; these are the ones measured.

How each provider reaches the agent. idea-harness writes a Hermes config that pins the agent to one
endpoint, instead of relying on Hermes's own provider names. Those names do not match: Hermes maps
`openai` to OpenRouter and does not know `ollama`, so an OpenAI key would go unused while
OpenRouter was billed. Each provider is declared explicitly, with the base URL from
`.idea-harness/harness.yaml` and the name of its key variable. An entry under `providers:`
applies to the agent as it does to every other call. For example, an Ollama moved to another port,
or an OpenAI key held in a project-specific variable. `--smoke` uses the same resolved provider,
custom providers included.

Two providers need more than a base URL:

- **Gemini.** Its configured base URL is Google's native API. The agent speaks the
  OpenAI-compatible protocol, so it uses Google's compatible endpoint (`…/v1beta/openai`).
- **Ollama.** Its base URL is `localhost:11434`, and inside a container `localhost` is the
  container. The agent uses `host.docker.internal` instead, and the container gets a mapping for
  that name. Ollama must be running on the host, with the model already pulled.

  On Linux, `host.docker.internal` is the Docker bridge address, not `127.0.0.1`, and Ollama
  listens only on `127.0.0.1` by default. Start it with `OLLAMA_HOST=0.0.0.0` (or the bridge
  address) so the container can reach it. That also exposes Ollama, which has no authentication,
  to your network unless a firewall blocks port 11434. Docker Desktop on macOS and Windows needs
  neither step.

  A small local model can be slow and weak at tool use. Larger tasks take longer than the smoke.

**Model ids go stale.** On 2026-09-17 Google returned 404 for `gemini-2.5-flash` and
`gemini-2.5-pro` ("no longer available to new users"), while its model listing still showed them.
A dead model id makes the agent exit without writing anything, and `--smoke` prints the provider's
own error when that happens. Run `--smoke` whenever you change the model. Both ids have since been
removed from the bundled catalog (`internal/model/catalog.go`) so a new project cannot pick them up
as a default; a real API call to any model id, dead or otherwise, now names the model in the error
and says how to change it (`idea model set <provider>/<model>`, or Settings → Providers).

### Hermes declares the model's context window explicitly

Hermes Agent refuses to start below 64,000 tokens of context
([FAQ](https://hermes-agent.nousresearch.com/docs/reference/faq)). Left to guess, it resolves a
model's context window through its own chain — a config override, then a custom provider's
per-model setting, a cached prior discovery, its own `/v1/models` query against the endpoint, and
finally a model-family pattern falling back to 128K. Every provider here is wired into Hermes as an
explicit `custom_provider` (see `hermescfg.go`'s own comment on why idea-harness does not rely on
Hermes's built-in provider names), which gives that chain the least to go on — and for
`openai/gpt-4.1-mini` its family-pattern guess landed under the 64k floor even though the model's
real window is roughly 1M tokens, the same failure mode as
[NousResearch/hermes-agent#24000](https://github.com/NousResearch/hermes-agent/issues/24000) (a
custom endpoint falling back to 32,768 tokens). An ICP walkthrough hit exactly this: `hermes` +
`openai/gpt-4.1-mini` failed deterministically, every run, with the agent refusing to start.

The generated config now declares `model.context_length` explicitly instead of leaving Hermes to
guess (`internal/model/catalog.go`'s `ContextWindowFor`, wired into `hermescfg.go`):

- A handful of specific `provider/model` ids have a figure checked against that provider's own
  model documentation (`ModelContextWindows` — `openai/gpt-4.1-mini` and `openai/gpt-4.1` are both
  1,047,576; `openai/gpt-4o` and `openai/gpt-4o-mini` are 128,000; `openai/o3-mini` is 200,000).
- Everything else gets a conservative per-provider default (`contextWindowFallback`): 128,000 for
  OpenAI, DeepSeek and OpenRouter, 200,000 for Anthropic, 1,000,000 for Gemini — each already the
  figure this codebase states in that provider's own `Capabilities.ContextLimit`, not a fresh guess,
  and comfortably above the 64k floor.
- Ollama gets no explicit value, deliberately: a local model's real context ranges from ~2k to over
  1M depending entirely on what the operator pulled, so a blanket number here could just as easily
  understate it (harmless) as overstate it (Hermes then sends a prompt the local server truncates or
  rejects — a new, different failure). Ollama's endpoint can report its real context directly,
  which is why Hermes's own docs single it out as the one case its auto-detection is actually
  reliable for — so it is left to that.

### Known executor/model compatibility

`idea executors setup`, the Build page's run-readiness check, and `idea execute` itself all run a
small, data-driven table (`internal/executor/compat.go`) before spending an image pull or a paid
call on a model×executor combination already known not to work. As of this fix the table is empty:
investigating `hermes` + `openai/gpt-4.1-mini` found the fixable root cause above rather than a
genuine incompatibility, so it is not refused — it still gets the same "untested" advisory below,
since nobody has actually run it through `--smoke` yet.

| Kind | Model | Verdict | Why |
|---|---|---|---|
| `hermes` | any of the "Verified" models in the table above | OK | Smoke-tested end to end on 2026-09-17. |
| `hermes` | anything else | Warned | Not verified — run `--smoke` before relying on it. |
| `acp` | any | Warned | No ACP agent has been smoke-tested against a real model yet; run `--smoke` (with `--image` and `--acp-command`) first. |
| `openhands` | any | Warned | No Agent Server run has been smoke-tested against a real model yet; run `--smoke` (with `--url`) first. |

A row placed in the table stops `idea executors setup` and `idea execute` before anything runs,
naming the model and why — reserved for a combination that turns out to be genuinely, unfixably
incompatible, not a stand-in for "nobody has tried this yet" (that is what the untested warning is
for). An untested combination is a warning, not a block, since most models nobody has run through
`--smoke` yet work fine — `idea executors setup --smoke` and the Build page's **Run smoke test** are
the way to confirm one before a real task depends on it.

### Manual (equivalent)

Edit `.idea-harness/harness.yaml`:

```yaml
# .idea-harness/harness.yaml
executor:
  kind: omp                                       # or hermes
  image: ghcr.io/your-org/omp:latest              # image with the `omp` CLI installed
  model: anthropic/claude-sonnet-5              # coding model (provider/model)
```

For Hermes:

```yaml
# .idea-harness/harness.yaml
executor:
  kind: hermes
  image: ghcr.io/your-org/hermes:latest           # image with the `hermes` CLI installed
  model: anthropic/claude-sonnet-5
```

Then:

```bash
idea config validate
idea daemon stop        # then run any command — the daemon auto-starts and re-reads harness.yaml
```

> Editing `harness.yaml` by hand requires a daemon restart so the cached runtime re-reads the
> config. Configuring the model/providers via `idea model …` or the web UI **Settings** reloads it
> for you.

### Fields

| Field | Meaning | Default |
|---|---|---|
| `executor.kind` | `auto` \| `internal` \| `container` \| `omp` \| `hermes` \| `acp` \| `openhands` | `auto` (unset) |
| `executor.image` | Docker image containing the agent (`omp` / `hermes` / `acp`) | the pinned Hermes image for `hermes`; none for `omp`/`acp` (required) |
| `executor.model` | Coding model for the agent, as `provider/model` | falls back to the default-model chain |
| `executor.acp.command` | The ACP agent's argv inside the image; `{model}` / `{bare_model}` are substituted | none (required for `acp`) |
| `executor.acp.key_env` | Name the provider key is exposed under inside an `acp` container | the provider's own variable |
| `executor.acp.timeout_seconds` | Bound on one ACP prompt turn; then `session/cancel`, then kill | `840` |
| `executor.openhands.*` | The Agent Server, its session-key variable and agent (see [OpenHands Agent Server](#openhands-agent-server-kind-openhands)) | none (`url` required for `openhands`) |
| `executor.mcp_servers` | MCP servers handed to an `acp` agent (see [MCP servers](#mcp-servers-acp-only)) | none |

The per-role model map `models.implementer` overrides `executor.model` if both are set.

## Any Agent Client Protocol agent (`kind: acp`)

The `acp` kind drives any agent that speaks the [Agent Client Protocol](https://agentclientprotocol.com)
— JSON-RPC 2.0 over stdio — instead of a CLI with a bespoke argv. That covers Claude Code
(`claude-code-acp`, from `@zed-industries/claude-code-acp`), Codex (`codex-acp`), Gemini CLI
(`gemini --experimental-acp`), OpenHands (`openhands acp`) and OpenCode. There is no canonical image
for any of them: build one with the agent installed and pass it with `--image`.

```bash
idea executors setup --kind acp --image ghcr.io/your-org/claude-acp:1 \
  --acp-command claude-code-acp --model anthropic/claude-sonnet-5 --yes

# argv with arguments: whitespace-separated words, or a JSON array when an argument has a space
idea executors setup --kind acp --image ghcr.io/your-org/gemini:1 \
  --acp-command "gemini --experimental-acp -m {bare_model}" --acp-key-env GEMINI_API_KEY \
  --model gemini/gemini-3.6-flash --yes
```

Setup verifies the image the ACP way: it starts the command in the hardened container with no
network and no key (the probe's isolation is in [`security-model.md`](security-model.md)), sends
`initialize`, and requires a protocol-version-1 answer (an ACP
adapter need not implement `--version`, so the CLI-presence probe Hermes and OMP use does not
apply). `--smoke` then runs the same trivial task Hermes's smoke runs, through the agent.

How a run works. The agent runs in the same hardened container as Hermes and OMP (see the security
section below), attached with `docker run -i --rm`, and the harness is its ACP client:

1. `initialize` — the harness advertises `fs.readTextFile` and `fs.writeTextFile`, and nothing else.
   `terminal` is **false**: the agent runs its own commands inside its own container, and a client
   terminal would run them on the host. An agent that calls `terminal/*` anyway gets
   method-not-found. A protocol version other than 1 fails the run.
2. `session/new` with `cwd: /work` and the configured `mcpServers`.
3. `session/prompt` with the implementation brief. The run succeeds on `stopReason: end_turn`; any
   other stop reason, an agent that exits, or a JSON-RPC error fails it with the reason.

While the turn runs:

- **`fs/read_text_file` and `fs/write_text_file` are confined to the work root.** The path must be
  absolute and under `/work`; it is cleaned (`/work/../etc/passwd` is refused) and then resolved on
  the host, symlinks included, so a link inside the work root pointing out of it is refused too.
  Refusals answer `-32602` and are written to the agent output.
- **`session/request_permission` is answered without asking anyone**: `allow_once` when offered,
  else `allow_always`, else `reject_once`/`reject_always`, else `cancelled`. The container is the
  boundary, exactly as Hermes's `--yolo` and OMP's `--auto-approve` are. Every request and its answer
  is recorded on the run manifest (`executor.permissions`), so nothing approved on your behalf is
  invisible.
- **Output** — the agent's messages, tool-call titles, permission answers, file writes and stderr —
  feeds the same live view and change-set `agent_output` as the other agents. Thought chunks are
  not kept. Known secret values (the key, MCP `value_env` values) are redacted.
- **Timeout and cancel.** After `executor.acp.timeout_seconds` (default 840 s, under the engine's
  15-minute ceiling), or when the run is stopped, the harness sends `session/cancel`, waits 10 s for
  the agent to answer `cancelled`, then kills the container.

**The model.** ACP has no standard way to choose a model, so `executor.model` does two things: it
picks which provider key is forwarded (the same resolution as Hermes), and it is substituted into
`executor.acp.command` wherever `{model}` (`provider/model`) or `{bare_model}` appears. An agent
that reads its model from its own config or environment ignores it. The agent talks to its own
vendor's API directly; a `providers:` base URL override does not apply to it.

**The key's name.** The provider key is forwarded under the provider's own variable unless
`executor.acp.key_env` renames it — Gemini CLI reads `GEMINI_API_KEY`, not the `GOOGLE_API_KEY` the
`gemini` provider uses. Docker reads the value from its own environment (`-e NAME` with no value), so
it never appears in the `docker run` argv.

**Not verified yet.** The protocol handling is tested against a fake ACP agent (handshake, file
writes, permission requests, path-escape refusal, timeout, cancel), and the production transport
against a real Docker container running a shell ACP agent. No real ACP coding agent has been run
end to end through `--smoke` against a real model yet, so every `acp` combination is reported as
untested; run `--smoke` before relying on one.

### MCP servers (acp only)

`executor.mcp_servers` hands MCP servers to an ACP agent in `session/new`, in ACP's own shape:

```yaml
executor:
  kind: acp
  image: ghcr.io/your-org/claude-acp:1
  acp:
    command: [claude-code-acp]
  mcp_servers:
    - name: github                     # stdio: runs INSIDE the agent's container
      command: github-mcp-server
      args: [stdio]
      env:
        - name: GITHUB_PERSONAL_ACCESS_TOKEN
          value_env: GITHUB_TOKEN      # the NAME of a variable; never the token itself
        - name: LOG_LEVEL
          value: info                  # a literal, for a non-secret setting
    - name: docs                       # http (or sse): reached by the agent over the network
      type: http
      url: https://docs.example.com/mcp
      headers:
        - name: Authorization
          value_env: DOCS_MCP_AUTH
```

The trust tradeoff, which you take on per server:

- **A stdio server runs inside the agent's container**, as the agent's child — with the same
  isolation, the same unrestricted egress, and the same auto-approved permissions. Its binary must
  be in the image. It is as trusted as the agent itself.
- **An http/sse server is reached by the agent**, which sends it whatever its `headers` carry. The
  agent must advertise `mcpCapabilities.http` (or `sse`); if it does not, the run fails rather than
  silently dropping the server.
- **Secrets are never inline.** Each `env`/`headers` entry is either `value` (a literal) or
  `value_env` (the name of a variable). `harness.yaml` is committable, so a literal whose name looks
  like a credential (`*_TOKEN`, `*_KEY`, `Authorization`, …) or whose value matches a known credential
  format is refused when the config loads. `value_env` is resolved like a provider key — the
  daemon's environment, then the project's `.env` — and one that is set in neither stops the run
  before the container starts, naming the variable. The resolved values travel to the agent in the
  `session/new` message on its stdin, never in argv, and are redacted from its output.
- The agent can use every server you list, for anything, with nobody approving the call. List only
  servers whose reach you would grant the agent directly.

`mcp_servers` on any kind other than `acp` is reported by `idea config validate` and `idea doctor`
as unused, rather than ignored silently.

### API key

The executor resolves the *name* of the key's environment variable from the model's provider — a
built-in preset (e.g. `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`) or a custom
`providers.<name>.api_key_env`. It then reads that variable the same way the model registry does for
every other model call: **the daemon's own process environment first, then the project's `.env`**
(`internal/workflow/execute.go`'s `lookupKey`, wired into the executor via `SetKeyLookup`). The value
is forwarded into the container via a `0600` env-file, so the secret never appears in the `docker
run` argv and is never written to state.

> An earlier revision of this page said `.env` **does not reach the agent** — true before
> `979e0d0`, and stale ever since: since that commit, a key that lives only in `.env` is enough,
> with no separate step for the agent. If a keyed provider's key is set in neither the daemon's
> environment nor `.env`, the run stops before the container starts, naming the variable. If you
> set or change the environment variable itself (rather than `.env`), the daemon still needs a
> restart to pick it up:
>
> ```bash
> export ANTHROPIC_API_KEY=...   # before the daemon starts
> idea daemon stop               # then any command restarts it with the variable set
> ```
>
> If the provider cannot be resolved at all, the run stops before the container starts, naming the
> model it could not resolve — it never falls back to some other provider's key.

## OpenHands Agent Server (`kind: openhands`)

The `openhands` kind hands the agent run to an [OpenHands Agent
Server](https://docs.openhands.dev/sdk) — a separate execution plane, usually on another machine —
while idea-harness stays the control plane: the task, the change set, its review, approval and
apply never leave here. It is **opt-in only**. `auto` never resolves to it, because the work root
and (unless withheld) the provider key leave the machine; what exactly leaves, and what is checked
on the way back, is in [`security-model.md`](security-model.md#the-remote-execution-plane-openhands).

```bash
export OPENHANDS_API_KEY=...          # the server's session key (or put it in .env)
idea executors setup --kind openhands --url https://agents.example.com \
  --model anthropic/claude-sonnet-5 --yes

# an ACP agent the server's image carries (claude-agent-acp, codex-acp, gemini-cli)
idea executors setup --kind openhands --url https://agents.example.com \
  --agent acp --acp-command claude-agent-acp --model anthropic/claude-sonnet-5 --yes
```

Setup refuses a plain-http URL to another host, checks that `GET /server_info` answers and that
the session key is accepted (an authenticated `GET /api/conversations/count`), prints what leaves the
machine, and writes:

```yaml
executor:
  kind: openhands
  model: anthropic/claude-sonnet-5     # passed to the server's agent as its LiteLLM model
  openhands:
    url: https://agents.example.com
    api_key_env: OPENHANDS_API_KEY     # the variable's NAME; default shown
    agent: openhands                   # or acp, with acp_command
    # workspace_dir: /workspace/idea-harness
    # withhold_provider_key: true      # the server supplies its own model key
    # max_iterations: 100
    # timeout_seconds: 840
```

`--smoke` runs the shared trivial task on the server. `idea doctor` re-checks the server and key on
every run (a warning, not a failure). Field reference: [configuration.md](configuration.md).

**The model.** For the server's own agent, `executor.model` becomes the agent's LLM: the six
built-in providers already use LiteLLM's prefixes (`anthropic/…`, `openai/…`, `gemini/…`,
`deepseek/…`, `openrouter/…`, `ollama/…`), and a custom OpenAI-compatible provider goes through
`openai/<model>` with its `base_url`. An Ollama at `localhost` only works when the server runs on
this machine — the server's localhost is its own. An ACP agent on the server chooses its model its
own way; its provider key reaches it as a conversation secret under the provider's variable name.

### How a run works

1. **Up.** The work root — the seeded copy of the workspace — is packed as one tar.gz of exactly
   the files the engine's own snapshot sees, uploaded (`POST /api/file/upload`), and unpacked on
   the server into `<workspace_dir>/<conversation-id>` with one `tar -xzf`
   (`POST /api/bash/execute_bash_command`).
2. **Run.** `POST /api/conversations` creates the conversation with that directory as its
   workspace and the implementation brief as its initial message, which starts the agent. The
   confirmation policy is left at the server's default, `NeverConfirm` — the same auto-approve the
   local agent kinds run with.
3. **Follow.** A watcher ingests events through `GET /api/conversations/{id}/events/search` from a
   stored cursor, and the event websocket (`/sockets/events/{id}`) only wakes it up; without the
   socket it polls every 2 s. Agent messages, tool calls, failing tool results and errors become
   compact, redacted lines — the live view and the change set's agent output — and thoughts are not
   kept. `finished` completes the run; `error`, `stuck`, a pause or a confirmation request nobody
   here will answer fail it, with the agent's own error text.
4. **Down.** `GET /api/file/archive?format=tar.gz` brings the directory back. It is unpacked into a
   staging directory and validated before anything in the work root changes, then mirrored in: a
   file is deleted locally only if it was uploaded and is gone on the server (a file the archive
   route's default excludes leave out is fetched on its own, not read as deleted). From here the
   pipeline is the one every agent uses: snapshot, diff, secret sweep, checks, review.

Why a tarball: per-file transfer is thousands of requests and leaves a half-copied tree on a crash;
the archive route's `git-delta` format needs a repository on the server (the work root has no
`.git`, deliberately) and a patch applier here, when the engine computes its own diff anyway; and
the bash route returns only the last 1 MiB of output, so it cannot carry files back.

### Durable runs: idempotency, restarts, cancel

Every run is a row in `remote_runs` (the project's `state.db`): local id, remote conversation id,
server URL, status, event cursor, idempotency key, timestamps, and the compact progress log.

- **Idempotent dispatch.** The key is a hash of (task, attempt), where the attempt number counts
  the task's runs that are already over, so a retry after a crash computes the same key and finds
  the same row. The conversation id is derived from the key, and the server treats a create with an
  existing id as "return it" — so a crash between "create sent" and "id recorded" still cannot
  start a second conversation. Every dispatch step is idempotent (the unpack starts with `rm -rf`).
- **Daemon restart.** Stopping the daemon fails the Execute operation, as for any kind, but does
  **not** stop the remote run: the daemon lets go of it first. The next daemon re-attaches to every
  run not yet over (once per project, in the background), catches up on the events it missed from
  the cursor — each exactly once — and follows it to the end. A finished run waits for the task's
  next `idea execute`, which collects its files instead of starting again — as long as the freshly
  seeded work root still matches what was uploaded; if the workspace changed meanwhile, the stale
  run is retired (and interrupted on the server) and a new attempt starts.
- **Vanished.** A conversation the server no longer has (deleted, or a server restarted without
  persistence) fails the run with that reason; the next execute starts a new attempt.
- **Cancel.** `idea cancel` / Stop marks the run cancelled, interrupts it on the server
  (`/interrupt`, then `/pause` as the fallback), and brings back what the agent had written so
  the next run can continue from it, as Stop does for a local agent.
- **Retries.** Network errors, timeouts, 408/429 and 5xx are retried with exponential backoff
  (up to 5 tries per call; a watcher keeps retrying until the run's timeout). 401/403 and 404 are
  answers, not outages, and are never retried.
- **Timeout.** `timeout_seconds` (default 840 s, under the engine's own 15-minute ceiling) is
  measured from dispatch; past it the run is interrupted on the server and failed.
- **The engine lock.** `idea execute` holds the engine's lock while it waits on its own run, as it
  does for every kind. Reconciliation and the watchers never take it: they write only their own
  `remote_runs` rows.

### API version, and what is confirmed

Written against **openhands-agent-server v1.54.0** (OpenHands/software-agent-sdk, released
2026-10-09; image `ghcr.io/openhands/agent-server:1.54.0-python`), from the server's own routers
(`openhands-agent-server/openhands/agent_server/*_router.py`, `sockets.py`) and the SDK's remote
client. Setup and doctor report a server on another version as a warning, not a refusal.

| Part | Status |
|---|---|
| `X-Session-API-Key`; websocket first-frame auth; 401 on a wrong key | Confirmed from source |
| `GET /server_info`, `GET /api/conversations/count`, `GET /api/conversations/{id}`, `execution_status` values | Confirmed from source |
| `POST /api/conversations` with a client-chosen `conversation_id` (201 new / 200 existing), `initial_message.run` starting the agent, default `NeverConfirm` | Confirmed from source |
| `Agent` spec: `llm.model/api_key/base_url`, tools by registered name (`terminal`, `file_editor`, `task_tracker`); `ACPAgent` with `acp_command` | Confirmed from source |
| `/interrupt`, `/pause`; `events/search` parameters and `{items, next_page_id}` shape | Confirmed from source |
| `POST /api/file/upload?path=` (field `file`), `GET /api/file/download?path=`, `GET /api/file/archive?format=tar.gz`, `POST /api/bash/execute_bash_command` | Confirmed from source |
| `events/search` `page_id` starts a page **at** that event (inclusive) | Confirmed against the real image |
| Archive entries rooted under the directory's base name, plus an `archive_manifest.json` of metadata the route appends at the root (left out of the work root; a real file of that name is fetched on its own) | Confirmed against the real image |
| `tar` present in the server image; a missing file answering 404 on download; `interrupt` on an unknown conversation answering 400, not 404 | Confirmed against the real image |
| A wrong session key refused; a conversation with a bad model key settling as `error` with the provider's own message | Confirmed against the real image |
| A conversation secret serialised as `{"kind": "StaticSecret", "value": …}` (only `agent: acp` sends one) | Assumed (`acpSecrets`) |

**What is verified, and what is not.** The behaviour is tested against a fake Agent Server
(`internal/executor/ohfake`, which mimics the real archive layout): the file round trip, idempotent
re-dispatch, a restart with reconciliation, a stale base, a vanished conversation, cancel, 5xx
retries, auth failure, plain-http refusal, timeout, redaction, archive safety, and an `idea execute`
whose returned files go through the secret sweep. The protocol is also tested against the real
`ghcr.io/openhands/agent-server:1.54.0-python` image with no LLM key
(`TestOpenHandsAgainstRealAgentServer`, skipped unless the image is present locally): session-key
auth, upload and unpack, idempotent create, event ingestion, the run settling on the provider's
auth error, and the archive round trip. No run with a real model — an agent actually editing files
on a real server — has been made, so every `openhands` combination is reported as untested; run
`--smoke` before relying on one.

## Smoke test

**From the web UI:** Settings → Executor → **Run smoke test**. The daemon runs a trivial task
through the configured agent, which must write one file. It uses the daemon's own configuration and
keys (its environment, then the project's `.env`), so it tests what a real run will use. It makes
one small real model call. The last result stays on the Settings page. The same test is
`POST /api/v1/projects/{projectId}/executors/smoke`.

**From the CLI:** `idea executors setup --kind hermes --model <provider/model> --smoke --yes`
runs it in your terminal's environment, which is not always the daemon's.

**In CI:** [`scripts/agent-smoke.sh`](../scripts/agent-smoke.sh) runs a real Hermes coding task end-to-end in
the isolated container and asserts that it produces a real file diff. It is **opt-in**: it requires
`HERMES_IMAGE` and `AGENT_MODEL` (plus a model API key), which are repository variables/secrets
rather than CI defaults — so it runs manually (workflow_dispatch), not on every push. Setting
`ACP_IMAGE` and `ACP_COMMAND` (a YAML flow sequence such as `[claude-code-acp]`, optionally
`ACP_KEY_ENV`) instead runs the same task through an ACP agent.

## Watching and stopping a run

While the agent runs, the Build page's running card shows the **run panel** (see
[ui.md](ui.md#run-panel)): what the agent is doing now, its tool calls, the files it has written
with line counts, the permissions answered for it, tokens, cost and elapsed time, and any error. It
is fed by the run's normalized event stream ([api.md](api.md#run-events)), which every executor
produces at the grain it can:

| Executor | Where its events come from |
|---|---|
| `acp` | The agent's own `session/update` notifications (messages, tool calls and their updates, plans, thought markers), its `fs/write_text_file` calls with +/- counts against the previous content, and every permission answer. |
| `openhands` | The Agent Server's events, ingested by the run's watcher from the same cursor as the progress log (so exactly once, across daemon restarts): actions, observations, the agent's messages, errors, status, and a `stats` update's running cost turned into deltas. |
| `hermes`, `omp`, `container` | Coarse: each new line of the agent's output tail is an action, a work-root scan (size and mtime, every 2 s) reports files as they change, and status changes become events. |
| `internal` | A few synthetic events: started, one per phase, the placeholder file. |

Whatever the executor, once the agent finishes the engine adds the authoritative file list from its
own diff, with +/- counts, then the checks and review stages and the change set it produced. The
agent's raw output (the last 100 lines, secrets redacted) is still there under "Agent output", and
stays on the change set when the run finishes.

### Steering a running agent

The run panel's note box sends a note to the agent while it runs (`POST …/runs/{runId}/steer`).
What happens depends on the executor:

| Executor | Steering | How |
|---|---|---|
| `acp` | Yes | Queued and sent as the agent's next `session/prompt` turn, in the same session, once its current turn ends. ACP defines no concurrent prompts in a session ("once a prompt turn completes, the Client may send another `session/prompt`"), so a note is never sent mid-turn. **Interrupt the current step** ends the turn first with `session/cancel` - which the agent must answer with `stopReason: cancelled` - then sends the note at once. |
| `openhands` | Yes | Sent into the conversation as a user message (`POST /api/conversations/{id}/events`, `SendMessageRequest` with `run: true`); the agent reads it at its next step. Sent once, never retried, so it cannot arrive twice. |
| `internal`, `container`, `hermes`, `omp` | No | The reply is `steering not supported by <kind>`. The note is held and appended to the task's **next** run's brief, the same way the rerun note on execute is. |

A note sent while an agent is finishing (its session already over) is held for the next run too.
Every note is recorded as a run event and on the run manifest's executor provenance. Held notes live
in the daemon's memory: a daemon restart drops them. **Stop** is the running card's Stop button (the
operation's cancel), unchanged.

### Open in OpenHands

An Agent Server serves no UI of its own. If you also run an OpenHands web UI that shows the server's
conversations, set `executor.openhands.ui_url` and the run panel links each run to
`<ui_url>/conversations/<conversation id>`. Without it there is no link; the address is never
guessed.

### Run report

**Report (JSON)** / **Report (MD)** on the run panel download the run written down - its events,
the files it changed with line counts, the findings on its change set, cost, and the steering notes
- from `GET …/runs/{runId}/report?format=json|md|zip`. Everything in it is redacted.

**Stop keeps what the agent wrote.** When a run ends without a change set, the files the agent had
written are kept. That covers Stop, a failure, a timeout and the daemon stopping. The next run of the
same task continues from them: the Run button reads "Continue TASK-n (N files from the stopped
run)". Carried files are applied only where your workspace has not changed since the stopped run.
The change set's `carried_over` finding lists what was carried and what was skipped.

Choose **Start over** to discard them and begin from the workspace. Over the API, that is
`DELETE …/initiatives/{id}/partial-work`. Kept files are also discarded when the next task is a
different one, and when a phase is reopened or the plan revised. Files that look like they hold a
secret are never kept, and the Build page says so.

The timeline's entry for a Stop reads "Run stopped: Execute initiative — Stopped by you" — never the
raw `context canceled` the daemon's own log records internally (review defect F: the two used to be
the same text, and "Execute initiative — context canceled" reads exactly like a crash for what was
actually your own action). `daemon.log` and `GET /operations/{id}` still carry the real underlying
error for debugging; only the timeline's human-facing detail is replaced.

## Agent spend

`GET .../initiatives/{id}/costs` reports the deterministic pipeline's model-call spend precisely
(every phase call goes through the harness's own model registry, which always knows the price). The
coding agent's own spend is different: OMP/Hermes run as an opaque subprocess in a container, and the
harness only has whatever that subprocess printed to stdout (redacted, kept as the change set's
`agent_output`) to work with.

The harness tries two things, in order, against that text:

1. An explicit dollar figure ("cost: $0.42", "total cost: $1.20") — used verbatim.
2. Failing that, a reported token count (`input_tokens`/`output_tokens`, or the OpenAI-shaped
   `prompt_tokens`/`completion_tokens`) — priced against the model catalog's own per-token rates
   (`internal/model/pricing.go`, the same table `review.max_cost_usd` enforcement uses), via
   `model.CostUSD`.

Neither is guaranteed: an agent that prints neither leaves `agent.usd: null` with
`agent.note: "agent spend not measured"` (`idea status` and the UI both show this as a caveat next
to the total, not as a silent zero — review defect #5). A run's total cost is only ever reported
once EVERY agent run in the initiative was priced one of these two ways; a partial sum would
understate the real spend, which is worse than admitting it is not measured.

**Follow-up, not yet done:** Hermes's CLI documents a `--usage-file <path>` flag that writes a
machine-readable JSON usage report (`estimated_cost_usd`, `input_tokens`/`output_tokens`/
`total_tokens`, `api_calls`, …) after a run — but it is documented against `hermes -z`
(the standalone one-shot entry point), and this harness invokes `hermes chat --yolo -q ...`. Whether
`--usage-file` (or an equivalent) is available in that exact invocation, and what changing the
invocation to get it would risk breaking (the exact argv Hermes accepts has broken this integration
silently before — see the `hermesArgs` comment in `internal/executor/agent.go`), needs verifying
against a real Hermes container before being wired in. Until then, the stdout-scraping above is the
whole story.

## Live output during a run: what "silent for minutes" is, and is not

A real founder-journey run watched the live output view (`GET .../agent-output`, backed by `docker
logs --tail N` against the agent's own container — see `SandboxedAgentExecutor.tailLogs` in
`internal/executor/agent.go`) go silent for 3.5+ minutes while Hermes was generating code, then Stop
was clicked. This was investigated as a possible Hermes stdout-buffering problem — the concern being
that a CLI built for an interactive terminal (spinners, progress lines) might fully buffer its output
when stdout is a pipe rather than a TTY, so nothing reaches `docker logs` until the process exits or
an internal buffer fills.

Checked against Hermes's own published CLI reference
(`https://hermes-agent.nousresearch.com/docs/reference/cli-commands` and `.../faq`), as this document
insists on for every other Hermes argv detail after the `--in`/`--usage-file` incidents above:

- Real, documented `hermes chat` flags exist for output verbosity and format: `-v`/`--verbose`,
  `-Q`/`--quiet` (suppresses cosmetic output), and `--format stream-json` (structured JSONL "for
  machine consumption"). A `display.tool_progress` setting (`off`/`new`/`all`/`verbose`) also exists,
  but only in `~/.hermes/config.yaml` — there is no CLI flag for it.
- **None of Hermes's own documentation describes any flag, including these, as affecting output
  buffering or flush timing when stdout is redirected to a pipe or file rather than a TTY.** The docs
  distinguish TTY vs. non-TTY only for one thing — whether `hermes chat` stays interactive or answers
  and exits — never for I/O buffering.

Given that, wiring in `--format stream-json` (the most plausible-sounding candidate, since a
machine-consumption format is likely to be written incrementally) would be exactly the kind of
argv guess that has broken this integration silently before (`--in`, above). It would also be a much
larger change than this one question needs: `agent_output`'s consumers today — `scrubAgentLog`'s
noise filtering, the cost-scraping regexes above, and the plain-text UI/CLI rendering — all assume
free text, not JSON Lines, and switching formats would need to touch all three.

The 3.5-minute silence is equally, and more mundanely, explained by the agent genuinely having
nothing to show: `docker logs` only ever reflects what the container actually printed, and a coding
agent mid-generation between tool calls (or waiting on its own model's response) prints nothing to
print. The captured live-output snapshot from that exact run showed the agent still in its read/
search phase (`search_files`, `read_file`, `session_search`) with no write yet at the moment of Stop
— consistent with slow generation, not a buffering bug caught in the act.

**Follow-up, not yet done:** confirming this either way needs a real Hermes container with stdout
piped (not a TTY) and a known-slow prompt, watching whether *any* output line appears before the
process exits. Until that is done, no flag is added to `hermesArgs` on the strength of a plausible
name alone.

## Toolchains: the agent's image doesn't need one, and shouldn't install one

The OMP/Hermes agent image only needs to contain the agent CLI itself. It does **not** need a Go,
Node, Python, or Rust toolchain: the harness runs its own build/test checks after every task, in a
*separate*, network-isolated sandbox (`check.image`, `internal/executor/sandbox.go`) — never inside
the agent's own container. `network: disabled` in `harness.yaml` does not change this either way; see
[`configuration.md`](configuration.md#harnessyaml) for what that setting actually governs.

A real run surfaced the failure mode this invites: given network egress (which the agent container
has, deliberately, to reach its own model API — see
[`security-model.md`](security-model.md#what-this-does-not-protect-against)) but no toolchain, an agent asked to
verify its own Go code ran `go build`/`go test` by downloading a full Go toolchain into the
workspace. That toolchain (~14,300 files under `.tools/`) then got swept up as though it were part
of the change set, which is a separate, now-fixed defect (see the `.gitignore`-aware collector and
sanity cap in `internal/fsdiff`). The right fix is upstream of that: the implementation brief
(`runPlanner`, `internal/workflow/execute.go`) now names the harness's own detected (or configured)
build/test commands and tells the agent not to install a toolchain, compiler, or SDK to verify its
own work — the harness already will, right after.

**Follow-up, not yet done:** offering the check image's own toolchain to the agent container
directly (e.g. a read-only bind mount of a matching toolchain layer, or running the agent from the
same base image as `check.image`) would remove even the temptation, and might be cheap for the
common case where `executor.image` and the derived/configured `check.image` are the same toolchain
family. It isn't implemented here — it changes how the agent container and the check sandbox relate
to each other, which is more than this fix's scope — but it's a reasonable next step if this keeps
recurring in practice.

## Security / trust tradeoff — read this before wiring an agent

The coding-agent container is sandboxed for **filesystem and process** isolation. The argv below is a summary; [`security-model.md`](security-model.md) is the canonical table of every executor kind against what each one actually passes:

- read-only root filesystem (`--read-only`); only the mounted `/work` and a `/tmp` tmpfs are writable
- `--cap-drop ALL`
- `--security-opt no-new-privileges`
- runs as the daemon's own UID/GID (`--user <uid>:<gid>`) — so if the daemon runs as root, the
  container does too; nothing refuses to start or warns
- bounded resources: `--memory 2g`, `--pids-limit 256`

But it is **not network-isolated**. The agent needs egress to reach its model API, so
`--network none` is deliberately omitted — see [`security-model.md`](security-model.md).
Concretely, the agent container:

- can reach the network,
- receives **its own provider API key** (forwarded in via env-file; for `acp`, via the docker CLI's
  environment), and
- runs in auto-approve mode — OMP with `--auto-approve`, Hermes with `--yolo`, an ACP agent by
  having every permission request answered `allow_once` — so it writes files and runs commands
  without asking you first.

**A compromised or malicious agent image, or a prompt-injection attack through the task content,
could exfiltrate that API key or the generated data it can reach** — the sandbox does not block
network egress ([`security-model.md`](security-model.md) records this under what the execution
gateway does *not* protect against), and auto-approve means it acts without a human gate. Only wire OMP/Hermes images you
trust, and prefer a scoped, rotatable API key you can revoke.

The canonical statement of what each executor kind isolates, and what none of them do, is
[`security-model.md`](security-model.md#what-this-does-not-protect-against). This page repeats the
agent-specific part because it is where you decide whether to wire one.

The deterministic `internal` executor has none of this exposure (no network, no key, no real code).
It is no longer the silent default: an unset `executor.kind` runs Hermes once a model key and
Docker are both present, so having both is the opt-in. Set `executor.kind: internal` if you want
the placeholder regardless.
