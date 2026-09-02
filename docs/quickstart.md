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

> **No key yet? Then everything below is synthetic.** With no provider key in the environment,
> idea-harness falls back to a **demo provider**: no network call is made, and every artifact is
> generated locally and labeled as such ("Synthetic demo PRD for: …"; runs record
> `actual_model: demo`). The build step is synthetic too, and independently so — see step 4.
> Following this walkthrough in that mode is deliberate: it proves the control plane — the state
> machine, the review gates, the product graph, apply/revert — without spending a token. It does
> **not** show you the product's actual output. For that, see
> [The real path](#the-real-path--what-it-takes-what-it-costs-what-changes) at the end.

## 2. Turn the idea into a reviewed PRD (M1)

> **Ground the agents in your own material (optional).** If you have a PRD, design system, tech
> spec, market analysis, or UI mockup images, feed them to the team. Uploaded docs are
> credential-swept, redacted, and content-addressed, then injected into design & planning (and PRD
> revisions) so the team works from your context — never raw. Text formats are extracted directly;
> images are described by a vision model:
>
> ```bash
> idea docs add prd.md                # .md / .txt / .pdf / .docx / .xlsx
> idea docs add mockup.png            # .jpg / .png — described by a vision model
> printf '# PRD\n…' | idea docs add - --kind prd --title "pasted PRD"   # paste from stdin
> idea docs                           # list uploaded docs
> ```
>
> They also appear under the **Context** tab in the web UI (`http://127.0.0.1:8787/`). Trace a
> doc's influence with `idea trace context_doc <id>`.

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

## The real path — what it takes, what it costs, what changes

Everything above ran in **demo mode with a placeholder executor**. That is the out-of-the-box
default, and it is synthetic in two independent places:

| Layer | Default | What it produces |
|---|---|---|
| Reasoning (framing, research, PRD, review, design, plan) | **demo provider** — used whenever no provider key is resolvable | Locally generated, input-aware, explicitly labeled text: "Synthetic demo PRD for: …", assumptions reading "This is synthetic demo output — no model provider is configured." Runs record `actual_model: demo`. |
| Build (`idea execute`) | **`internal` executor** (`executor.kind: internal`) | A deterministic, hermetic placeholder file. No model call, no network, not code. |

They are separate switches. A configured `OPENAI_API_KEY` gives you a real PRD and a real plan and
still leaves `idea execute` writing a placeholder file; wiring an executor without a provider key
leaves the reasoning synthetic. **The real path needs both.**

### 1. A provider key

```bash
export ANTHROPIC_API_KEY=sk-ant-…        # or OPENAI_API_KEY, GOOGLE_API_KEY, …
idea model set anthropic/claude-3-7-sonnet
idea doctor                              # names the key the configured provider needs, if it is missing
```

Keys are read from the environment (or the project `.env`), never stored in `harness.yaml`. A
missing key does **not** fail the run — the registry always keeps the demo provider as a last
resort, so the pipeline quietly produces synthetic output instead. `idea doctor` is what catches
that before you spend time on it:

```text
warn  provider "anthropic" needs ANTHROPIC_API_KEY, which is not set; the harness will produce demo output
demo mode — no model configured; run `idea model set <provider>/<model>` for real output.
```

Run it after `idea model set` and before `idea new`.

### 2. A real executor

```bash
# Hermes — downloads the pinned official image and prints its digest
idea executors setup --model anthropic/claude-3-7-sonnet --yes

# OMP — bring your own image
idea executors setup --kind omp --image ghcr.io/your-org/omp:latest --model anthropic/claude-3-7-sonnet --yes
```

This verifies the agent CLI exists in the image, prints the security tradeoff, and writes
`executor.kind/image/model` into `.idea-harness/harness.yaml`. It needs **Docker**, and it accepts
a real security tradeoff: the agent container is filesystem- and process-isolated but **not**
network-isolated, receives its own provider API key, and runs in auto-approve mode. Read
[`docs/agents.md`](agents.md) before wiring one — that document is the prerequisite, not an
appendix.

Add `--smoke` to run a trivial coding task through the agent and assert it produces a real file
change before you trust it with a real one.

### What is different about the output

Same commands, same gates, same graph — different content:

- **`idea prd`** returns requirements derived from the idea with real acceptance criteria, and the
  adversarial reviewer raises findings that are about *your* product rather than the fixed demo
  strings. The review loop can now actually block and revise (up to `review.max_cycles`), which in
  demo mode it effectively never does.
- **`idea tasks`** is a plan whose dependency order reflects the real architecture, not a fixed
  skeleton.
- **`idea changes` / `idea change CHG-1`** shows a patch touching real source files with real
  content. In demo mode `CHG-1` is one placeholder file. This is the single biggest difference: the
  apply/revert/verify machinery is identical, but what it is governing is now code.
- **Runs record real usage.** Each model call's manifest carries `input_tokens`, `output_tokens`,
  `cost_usd` and `duration_ms` — all zero in demo mode.

### What it costs

Two components, and only one of them is countable from the source.

**The harness's own model calls are countable.** Each is one structured request:

| Command | Model calls |
|---|---|
| `idea new` | 4 — framing, research, PRD, adversarial review — plus 2 more (PRD + review) per blocked revise cycle, bounded by `review.max_cycles` (default 3). So 4–8. |
| `idea advance` | 5 — architecture, product design, verification plan, execution plan, plan review — plus one convergence call per open decision that has preserved dissents. The plan review is a single pass; it does not loop. |
| `idea execute` | 2 from the harness — implementation brief and change-set review — **plus the coding agent's own multi-turn run inside the container.** |

**The agent's own loop is not countable from here**, and it dominates. Hermes and OMP each run
their own agent loop, and the number of turns is a function of the task statement's size, the
repository they are reading, and the model. idea-harness sees one container invocation, not the
turns inside it.

So rather than a number this document cannot support, here is how to get your own:

- **Read the real figures.** Every model response is logged with its cost: `idea logs | grep
  cost_usd` shows `stage`, `actual` model, `duration` and `cost_usd` per call. Run the loop once on
  a throwaway initiative and you have your project's actual number.
- **Cap it before you start.** `review.max_cost_usd` and `review.max_tokens` in
  `.idea-harness/harness.yaml` are cumulative per-project ceilings (`0` = unlimited); the engine
  refuses further model calls once a ceiling is crossed. `review.max_cycles` bounds the most
  expensive variable in M1.
- **The order of magnitude follows from the table above**: a low-double-digit count of structured
  calls for M1 + M2, each carrying a compiled context bundle rather than a whole repository. The
  agent's implementation run is where the cost concentrates.

### What it costs in wall-clock

`idea new` and `idea advance` are sums of model latency — the count above times whatever your chosen
model takes per structured response. A small fast model and a large reasoning model differ by an
order of magnitude on the same pipeline, so a single number here would be fiction. The two levers
are **the model** and **the size of the task statement**.

`idea execute` against a real agent is bounded by the container run, not by idea-harness. This is
why [`scripts/agent-smoke.sh`](../scripts/agent-smoke.sh) waits 12 seconds after `execute` before
looking for a change set, and why the scripted demo waits 3.

**The one measured number** in this repository is the opt-in CI agent tier
([`.github/workflows/ci.yml`](../.github/workflows/ci.yml), job `agent`, `workflow_dispatch` only).
It runs a real Hermes agent against a real model and prints:

```text
agent_tier_wall_clock_seconds=<n>
```

Read it for what it is: the **whole** opt-in run — `go build`, `idea new`, `idea advance`,
`idea execute` with a real container, review, apply — on a GitHub-hosted runner, and including
about 22 seconds of fixed `sleep` in the script. It is an end-to-end upper bound for one small task,
not the latency of `idea execute` alone, and it moves with the runner and the configured
`AGENT_MODEL`. The job prints the number and nothing else from the run: the daemon log and the
model's output can carry material outside the redactor's catalog and are never echoed to CI logs.
