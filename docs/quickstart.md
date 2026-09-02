# Quickstart — build a recipe-sharing app end to end

This walkthrough takes one idea — a recipe-sharing app — through idea-harness's entire governed
loop, from a one-line idea to a verified codebase. Follow along in a fresh directory.

> Prefer the short version? The ["Quick start" section of the README](../README.md) shows the same
> commands in one block. This guide adds the "what's happening and why" around each step.

## 1. Create a project and pick a model

```bash
mkdir recipes && cd recipes
idea init
```

`idea init` creates a `.idea-harness/` directory in the current folder (no Git repo required). All
runtime state lives under `~/.local/share/idea-harness/`, never in your source tree.

```bash
idea model set openai/gpt-4o-mini
```

This chooses the model that drives the reasoning pipeline. Any supported provider works — Anthropic,
Gemini, OpenRouter, or local Ollama — and you can change it later from the web UI's Settings. API
keys are read from environment variables (e.g. `OPENAI_API_KEY`), never stored inline.

> **No key yet?** idea-harness falls back to a clearly-labeled **demo mode** that returns synthetic
> placeholder output. It's fine for following this walkthrough — but to do real research and
> produce a real PRD, configure a provider first.

## 2. Turn the idea into a reviewed PRD (M1)

```bash
idea new "A recipe-sharing app"
```

This starts the M1 pipeline: a team of experts frames your idea, does research, and drafts a PRD,
which an independent reviewer then challenges and revises. Follow it live from a second terminal:

```bash
idea watch
```

`idea watch` tails the run with a Bubble Tea spinner and event feed. When it settles, read the
result:

```bash
idea prd
```

The reviewed PRD renders in your terminal. Expect to see:

- a product summary and the core **requirements** (`REQ-1`, `REQ-2`, …), each with measurable
  acceptance criteria;
- **decisions** (`DEC-1`, …) the team asks you to confirm, each with a recommendation;
- any **open questions** and **review findings** still needing your judgment.

Run `idea status` any time to see where the initiative is, and `idea decisions` / `idea reviews`
to inspect the judgment items the reviewer raised. If you disagree with a recommendation, resolve
it (via `idea decisions` or the web UI) and the PRD is revised — for a clean first run you can
accept them.

## 3. Design and plan (M2)

```bash
idea advance
```

`advance` runs the M2 pipeline: architecture and design produce a **technical design**, a **design
spec**, and a **verification plan**, then those become a **dependency-ordered execution plan** —
the ordered list of tasks the implementer will tackle one at a time.

```bash
idea plan      # render the execution plan
idea tasks     # list the tasks in dependency order (T-1, T-2, …)
```

Each task shows its priority, estimate, and statement. Order matters: a task that depends on
another can't run first. Trace any node to its evidence with `idea why task T-1` or
`idea trace task T-1`.

## 4. Execute the first task (M3)

```bash
idea execute
```

`execute` takes the next ready task and runs it, producing a **change set** — a content-addressed
patch. By default the `internal` executor is a placeholder: deterministic and hermetic (no model,
no network), it writes a placeholder file so the pipeline runs end to end. That's fine for
learning the loop, but it is **not real code** — see the closing section for wiring a real agent.

```bash
idea changes
```

This lists change sets (e.g. `CHG-1`) with their status. `idea changed` shows "What Changed" since
your last look.

```bash
idea review CHG-1 approve
```

Change sets are gated by an independent review and then by you, the human. `approve` records your
decision (you can also request changes or reject).

```bash
idea apply CHG-1
```

M4: `apply` writes the reviewed patch into your workspace and records the files' pre-images, so it
can later be reverted byte-for-byte. With `git_commit: true` in `.idea-harness/harness.yaml`, each
apply also creates a git commit staging only the patch's files.

## 5. Verify and complete

```bash
idea verify REQ-1 --pass
```

`verify` records evidence that a requirement is met. Repeat for each requirement (`REQ-2`, …). Each
pass lands in the Product Graph, so "done" is provable, not asserted.

```bash
idea next
```

`idea next` is the loop driver: it runs the next automatable step and stops at each human judgment
point (review, apply, resolve), printing exactly what to run next. When every requirement is
verified it prints:

```text
✓ initiative complete
```

You've now taken an idea through the entire governed pipeline. Try `idea revert CHG-1` to see the
byte-for-byte rollback, `idea export` to dump artifacts, or `idea doctor` to confirm the
environment is healthy.

## Now wire a real coding agent

The placeholder executor never writes real code. To produce real file changes, wire a coding agent
(OMP or Hermes) into the executor slot:

```bash
# Hermes — auto-downloads the pinned official image
idea executors setup --model anthropic/claude-3-7-sonnet --yes

# OMP — bring your own image
idea executors setup --kind omp --image ghcr.io/your-org/omp:latest --model anthropic/claude-3-7-sonnet --yes
```

This needs a model plus a provider API key, and it runs the agent inside an isolated Docker
container. It also carries a real security tradeoff (network egress + auto-approve) — read
[`docs/agents.md`](agents.md) before wiring one. Once wired, the same `idea execute` produces real
file changes instead of a placeholder.
