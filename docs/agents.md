# Wire a real coding agent

By default, `idea execute` does **not** write real code. This guide explains what the default
executor actually does, how to switch to a real coding agent (OMP or Hermes), and — critically —
the security tradeoff you accept when you do.

## The default `internal` executor is a placeholder

Out of the box, `harness.executor.kind` defaults to `internal`. The internal executor is
deterministic and hermetic: it makes no model calls, performs no network I/O, and depends on no
external services. It writes a placeholder file into the isolated work root so the change-set
pipeline (snapshot → diff → review → apply → revert) runs end-to-end — but it does **not** generate
real code.

To produce real file changes, select a coding-agent executor.

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
(`nousresearch/hermes-agent:<version>`) and prints the pulled digest. For **OMP** `--image` is
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
real file on 2026-09-17, against the pinned image `nousresearch/hermes-agent:v2026.5.16`. Times
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

### Known executor/model compatibility

Not every model a provider serves works with a coding-agent executor, even though `idea-harness`'s
own model calls (framing, research, PRD, review) accept any model id. An ICP walkthrough found
`hermes` + `openai/gpt-4.1-mini` failed deterministically — every run, not intermittently — because
Hermes Agent refuses to start below 64,000 tokens of context
([FAQ](https://hermes-agent.nousresearch.com/docs/reference/faq)), and every provider here is wired
into Hermes as an explicit `custom_provider` (see `hermescfg.go`'s own comment on why), for which
idea-harness never declares `model.context_length`. Without it, Hermes falls back to its own
model-family auto-detection to guess the window, and for `gpt-4.1-mini` that guess lands under the
floor — the same failure mode as [NousResearch/hermes-agent#24000](https://github.com/NousResearch/hermes-agent/issues/24000),
a custom endpoint falling back to 32,768 tokens.

`idea executors setup`, the Build page's run-readiness check, and `idea execute` itself all run the
same small, data-driven table (`internal/executor/compat.go`) before spending an image pull or a
paid call on a combination already known not to work:

| Kind | Model | Verdict | Why |
|---|---|---|---|
| `hermes` | `openai/gpt-4.1-mini` | **Refused** | Hermes's context-window auto-detection puts it under its own 64k floor; see above. |
| `hermes` | any of the "Verified" models in the table above | OK | Smoke-tested end to end on 2026-09-17. |
| `hermes` | anything else | Warned | Not known-bad, but not verified either — run `--smoke` before relying on it. |

A refused combination stops `idea executors setup` and `idea execute` before anything runs, naming
the model and why. An untested one is a warning, not a block — most models nobody has run through
`--smoke` yet work fine — and both `idea executors setup --smoke` and the Build page's **Run smoke
test** are the way to confirm one before a real task depends on it. Add a row to that table only
once a combination has actually been observed to fail every time, with a reason; it is not a place
to guess at every model nobody has tried.

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
| `executor.kind` | `internal` (default) \| `container` \| `omp` \| `hermes` | `internal` |
| `executor.image` | Docker image containing the agent CLI (`omp` / `hermes`) | `alpine:3.20` (not useful for a real agent) |
| `executor.model` | Coding model for the agent, as `provider/model` | falls back to the default-model chain |

The per-role model map `models.implementer` overrides `executor.model` if both are set.

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
rather than CI defaults — so it runs manually (workflow_dispatch), not on every push.

## Watching and stopping a run

While the agent runs, the Build page's running card shows its latest output: the last 100 lines,
refreshed every couple of seconds, with known secrets redacted. When the run finishes, the same
lines stay on the change set it produced, under "Agent output".

**Stop keeps what the agent wrote.** When a run ends without a change set, the files the agent had
written are kept. That covers Stop, a failure, a timeout and the daemon stopping. The next run of the
same task continues from them: the Run button reads "Continue TASK-n (N files from the stopped
run)". Carried files are applied only where your workspace has not changed since the stopped run.
The change set's `carried_over` finding lists what was carried and what was skipped.

Choose **Start over** to discard them and begin from the workspace. Over the API, that is
`DELETE …/initiatives/{id}/partial-work`. Kept files are also discarded when the next task is a
different one, and when a phase is reopened or the plan revised. Files that look like they hold a
secret are never kept, and the Build page says so.

## Security / trust tradeoff — read this before wiring an agent

The coding-agent container is sandboxed for **filesystem and process** isolation. The argv below is a summary; [`security-model.md`](security-model.md) is the canonical table of all four executor kinds against what each one actually passes:

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
- receives **its own provider API key** (forwarded in via env-file), and
- runs in auto-approve mode — OMP with `--auto-approve`, Hermes with `--yolo` — so it writes files
  and runs commands without asking you first.

**A compromised or malicious agent image, or a prompt-injection attack through the task content,
could exfiltrate that API key or the generated data it can reach** — the sandbox does not block
network egress ([`security-model.md`](security-model.md) records this under what the execution
gateway does *not* protect against), and auto-approve means it acts without a human gate. Only wire OMP/Hermes images you
trust, and prefer a scoped, rotatable API key you can revoke.

The canonical statement of what each executor kind isolates, and what none of them do, is
[`security-model.md`](security-model.md#what-this-does-not-protect-against). This page repeats the
agent-specific part because it is where you decide whether to wire one.

The deterministic `internal` executor has none of this exposure (no network, no key, no real code),
which is why it remains the safe default until you deliberately opt into a real agent.
