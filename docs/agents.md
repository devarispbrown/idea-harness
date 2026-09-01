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

Edit `.idea-harness/harness.yaml`:

```yaml
# .idea-harness/harness.yaml
executor:
  kind: omp                                       # or hermes
  image: ghcr.io/your-org/omp:latest              # image with the `omp` CLI installed
  model: anthropic/claude-3-7-sonnet              # coding model (provider/model)
```

For Hermes:

```yaml
# .idea-harness/harness.yaml
executor:
  kind: hermes
  image: ghcr.io/your-org/hermes:latest           # image with the `hermes` CLI installed
  model: anthropic/claude-3-7-sonnet
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

The agent's model key is resolved from the model's provider — a built-in preset (e.g.
`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`) or a custom `providers.<name>.api_key_env` — read from the
process environment (or the project `.env`). It is forwarded into the container via a `0600`
env-file, so the secret never appears in the `docker run` argv and is never written to state.

## Smoke test

[`scripts/agent-smoke.sh`](../scripts/agent-smoke.sh) runs a real Hermes coding task end-to-end in
the isolated container and asserts that it produces a real file diff. It is **opt-in**: it requires
`HERMES_IMAGE` and `AGENT_MODEL` (plus a model API key), which are repository variables/secrets
rather than CI defaults — so it runs manually (workflow_dispatch), not on every push.

## Security / trust tradeoff — read this before wiring an agent

The coding-agent container is sandboxed for **filesystem and process** isolation:

- read-only root filesystem (`--read-only`); only the mounted `/work` and a `/tmp` tmpfs are writable
- `--cap-drop ALL`
- `--security-opt no-new-privileges`
- runs as a **non-root** user (`--user <uid>:<gid>`)
- bounded resources: `--memory 2g`, `--pids-limit 256`

But it is **not network-isolated**. The agent needs egress to reach its model API, so
`--network none` is deliberately omitted. Concretely, the agent container:

- can reach the network,
- receives **its own provider API key** (forwarded in via env-file), and
- runs in auto-approve mode — OMP with `--auto-approve`, Hermes with `--yolo` — so it writes files
  and runs commands without asking you first.

**A compromised or malicious agent image, or a prompt-injection attack through the task content,
could exfiltrate that API key or the generated data it can reach** — the sandbox does not block
network egress, and auto-approve means it acts without a human gate. Only wire OMP/Hermes images you
trust, and prefer a scoped, rotatable API key you can revoke.

The deterministic `internal` executor has none of this exposure (no network, no key, no real code),
which is why it remains the safe default until you deliberately opt into a real agent.
