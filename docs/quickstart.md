# Quickstart — build a recipe-sharing app end to end

This walkthrough takes one idea — a recipe-sharing app — through idea-harness's entire governed
loop, from a one-line idea to a verified codebase. Follow along in a fresh directory.

> Prefer the short version? The ["Quick start" section of the README](../README.md) shows the same
> commands in one block. This guide adds the "what's happening and why" around each step.

## 1. Create a project

```bash
mkdir recipes && cd recipes
idea init
```

`idea init` also starts the background daemon, which inherits your shell environment **at that
moment** — so if you have a provider API key, export it before this step. (If you forget, Path B
below shows the one-command fix.)

`idea init` creates a `.idea-harness/` directory in the current folder (no Git repo required). All
runtime state lives under `~/.local/share/idea-harness/`, never in your source tree.

### Then pick exactly one path

What you do next depends on whether you have a provider API key. **The two paths are mutually
exclusive** — do one or the other, not both.

#### Path A — no key: skip the model step entirely

Run nothing here and go straight to step 2. With **no `default_model` set and no provider
configured**, model resolution falls through to the built-in **demo provider**
(`internal/workflow/engine.go`, `resolveModelSpec`): no network call is made, and every artifact is
generated locally and labeled as such ("Synthetic demo PRD for: …"; runs record
`actual_model: demo`). The build step is synthetic too, and independently so — see step 4.

Following this walkthrough in that mode is deliberate: it proves the control plane — the state
machine, the review gates, the product graph, apply/revert — without spending a token. It does
**not** show you the product's actual output. For that, see
[The real path](#the-real-path--what-it-takes-what-it-costs-what-changes) at the end.

#### Path B — with a key: export it, then set a model

**If you already exported the key before `idea init` above**, the daemon has it and you only need:

```bash
idea model set openai/gpt-5.6-luna
idea doctor                                # asks the DAEMON whether it has the key; non-zero if not
```

**If you are exporting it now**, the daemon is already running without it and must be restarted —
it reads the environment once, at start:

```bash
export OPENAI_API_KEY=sk-…                 # or ANTHROPIC_API_KEY, GOOGLE_API_KEY, …
idea daemon stop                           # the next command restarts it, with the key
idea model set openai/gpt-5.6-luna
idea doctor
```

Any supported provider works — Anthropic, Gemini, DeepSeek, OpenRouter, or local Ollama — and you
can change it later from the web UI's Settings. API keys are read from environment variables, never
stored inline.

> **The daemon reads your environment once, when it starts.** `idea init` starts it, so a key
> exported *afterwards* is invisible to it — `idea model set` will report success and the first
> model call still fails with `401`. Either export before `idea init`, or run `idea daemon stop`
> after exporting so the next command restarts it. `idea model set` warns when it detects this.
>
> **And do not set a model without the matching key at all.** Once `default_model` is set,
> `resolveModelSpec` returns it *before* it ever reaches the demo fallback, and the built-in presets
> are always registered — with an empty key when the environment variable is unset
> (`internal/daemon/runtime.go`, `buildRegistry`). The first model call then comes back
> `openai: status 401`, which is not in the engine's transient-error list, so the run is not retried
> and the initiative goes to **failed**. `idea doctor` reports it as a failing check and exits
> non-zero, naming the variable and whether the daemon or only your shell is missing it.
>
> Once an initiative is `failed`, `idea retry` re-runs the phase that failed — not the whole
> initiative — and takes `--model <provider/model>` to retry somewhere else. Fix the cause first.

## 2. Turn the idea into a reviewed PRD (M1)

> **Ground the agents in your own material (optional).** If you have a PRD, design system, tech
> spec, market analysis, or UI mockup images, feed them to the team. Uploaded docs are
> content-addressed and injected into every downstream stage that compiles a context bundle —
> design, planning, and PRD revisions among them. Text formats are extracted directly; images are
> described by a vision model, and only the description reaches the agents.
>
> **Text documents go in with the idea itself** (`.md`, `.txt`, `.csv`, `.json`, `.yaml`, … —
> anything that sniffs as text), because `idea docs add` needs an initiative to attach to and there
> is none until `idea new` has run. Use this *instead of* the plain `idea new` below:
>
> ```bash
> idea new "A recipe-sharing app" --doc prd.md --doc notes.txt   # text files only
> ```
>
> Everything else is added *after* the initiative exists — so these run once `idea new` has returned:
>
> ```bash
> idea docs add spec.pdf              # .pdf / .docx / .xlsx (text formats work here too)
> idea docs add mockup.png            # .jpg / .png / .gif / .webp — described by a vision model
> printf '# PRD\n…' | idea docs add - --kind prd --title "pasted PRD"   # paste from stdin
> idea docs                           # list uploaded docs
> ```
>
> They also appear under **Explore → Grounding** in the web UI (`http://127.0.0.1:8787/`). Trace a
> doc's influence with `idea trace context_doc prd.md` (the title, which defaults to the file's
> base name, or the id from `idea docs --json`).
>
> **Redaction is not uniform.** Text documents are credential-swept and redacted at ingest; images
> are stored, served, and sent to the vision provider **unredacted**. See
> [`docs/security.md` → Context docs](security.md#context-docs-uploaded-source-material) before
> uploading a screenshot that might contain a secret.

```bash
idea new "A recipe-sharing app"
```

This starts the M1 pipeline: a team of experts frames your idea, does research, and drafts a PRD,
which a reviewer role then challenges and revises. (A separate run and prompt — not necessarily a
different model; see [`security.md`](security.md).) `idea new` waits for this run to finish
before returning, so the next command in the sequence has something to work with; pass
`--no-wait` to get the prompt straight back. Either way you can follow it live from a second
terminal:

```bash
idea watch
```

`idea watch` polls the initiative once a second and renders one live line — a spinner, the title,
a status badge, and `n requirements · n decisions · n questions` — then prints `Done.` when the
run reaches `approved`, `needs_human_judgment` or `failed`. It is a status view, not an event
log; `idea logs -f` follows the daemon log. When it settles, read the result:

```bash
idea prd
```

The reviewed PRD renders in your terminal. With a real model, expect a product summary, the
**objectives** the initiative exists to move (`OBJ-1`, …) each with the **outcome metrics** that
would show it moved (`MET-1`, …), the core **requirements** (`REQ-1`, `REQ-2`, …) each with
measurable acceptance criteria and the objectives it serves, **decisions** (`DEC-1`, …) the team asks
you to confirm, and any **open questions** and **review findings** still needing your judgment. The
review checks that chain: a requirement serving no objective, or an objective nothing can measure, is
a high finding (a PRD with no objectives at all gets one medium finding instead). `idea objectives` and `idea metrics` list them;
[configuration.md](configuration.md#drift) has the drift rules.

> **On the no-key path, most of that is empty by construction.** The demo provider emits exactly
> one objective (`OBJ-1`) with one metric (`MET-1`), one requirement (`REQ-1`) serving it,
> **no decisions**, **no review findings**, and two open questions (`internal/model/fake.go`). So `idea decisions` prints "No decisions yet.", `idea reviews` is
> empty, and `DEC-1` does not exist — any command naming one will fail to resolve it. Downstream,
> the plan is a single task (`TASK-1`) and execute produces a single change set (`CHG-1`).

Run `idea status` any time to see where the initiative is, and `idea decisions` / `idea reviews`
to inspect the judgment items the reviewer raised. Its last line counts the Product Graph's gaps —
`gaps: 5 low — idea gaps` on the no-key path, before anything is planned — and `idea gaps` lists
each with the command that closes it.

**Decisions and questions can be resolved from the CLI.** `idea resolve <decision> <option#>|"<text>"`
resolves a decision (by option number or free text), `idea answer <question> "<answer>"` answers a
question, `idea reopen-decision <decision>` returns a resolved one to open, and
`idea accept-recommendations` resolves every open decision with its own recommendation in one call.
`idea triage` ranks everything needing your judgment — open decisions, questions, and blocking
findings — across all three. **Dispositioning an individual review finding is still a web-UI (or API)
action, not a CLI one**: the only CLI disposition is `idea review <CHG-n> approve --accept-findings`
(step 4), which accepts a change set's reviewer findings together. Open `http://127.0.0.1:8787/` → the initiative's
**Define** phase to accept a recommendation, pick another option, write your own, disposition a
finding, or answer a question there instead; the PRD then revises. For a clean first run you can
leave decisions and questions unresolved too: an undecided decision goes into the next revise
prompt carrying its recommendation rather than a resolution.

## 3. Design and plan (M2)

```bash
idea advance
```

`advance` runs the M2 pipeline: architecture and design produce a **technical design**, a **design
spec**, and a **verification plan**, then those become a **dependency-ordered execution plan** —
the ordered list of tasks the implementer will tackle one at a time.

```bash
idea plan      # render the execution plan
idea tasks     # list the tasks in dependency order (TASK-1, TASK-2, …; one on the no-key path)
```

Each task shows its priority, estimate, and statement. Order matters: a task that depends on
another can't run first. Trace any node to its evidence with `idea why task TASK-1` or
`idea trace task TASK-1`.

## 4. Execute the first task (M3)

```bash
idea execute
```

`execute` takes the next ready task and runs it, producing a **change set** — a content-addressed
patch. With no model configured yet, `executor.kind` (unset, so `auto`) falls back to the
`internal` executor and `execute` prints a warning saying so: it is deterministic and hermetic (no
model, no network), and it writes a placeholder file so the pipeline runs end to end. That's fine
for learning the loop, but it is **not real code** — see the closing section for a real agent.

```bash
idea changes
```

This lists change sets (e.g. `CHG-1`) with their status. `idea changed` shows "What Changed" since
your last look.

```bash
idea review CHG-1 approve
```

Change sets are gated by a reviewer run and then by you, the human — the verdict is yours, not the
model's. `approve` records your
decision (you can also request changes or reject).

With a real model the reviewer can raise a blocking finding, and `approve` then refuses. `idea
reviews` lists what it found, and `idea review CHG-1 approve --accept-findings` signs off the
reviewer's own findings — the same judgment call `idea approve` makes on the PRD. A finding from
the credential sweep, the sandbox guard or a digest mismatch carries a deny code and still refuses;
that is the line between "the reviewer disagrees with you" and "this change set is unsafe".

If a change set instead sits there not progressing — the review itself never finished, a secret
was found, a revert won't apply — [`docs/failure-modes.md`](failure-modes.md) names the exact
message and the way out.

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

`verify` records evidence that a requirement is met — the note is optional but is what makes the
evidence readable later (`idea verify REQ-1 --pass --note "checked the applied file"`). Repeat for
each requirement your PRD produced; on the no-key path `REQ-1` is the only one. Each pass lands in
the Product Graph, so "done" is provable, not asserted. `idea verify` with no requirement prints the
coverage table.

Before the gate reads anything, `verify` also measures the work's **quality dimensions** — setup
reproducibility, docs completeness, API compatibility and more — and prints a scorecard. Which ones
it measures, and whether any of them can hold the gate, follows the effective review rigor: on the
no-key path the project is tiny, the rigor is `quick`, and nothing is measured; at `thorough` the
first three must pass for the initiative to complete. See
[quality dimensions](configuration.md#quality-dimensions-verify).

```bash
idea next
```

`idea next` is the loop driver: it runs the next automatable step and stops at each human judgment
point — review, apply, or resolving open judgment items in the web UI — printing exactly what to run
next. When every requirement is verified it prints:

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
| Reasoning (framing, research, PRD, review, design, plan) | **demo provider** — used when no model is set and no provider is configured | Locally generated, input-aware, explicitly labeled text: "Synthetic demo PRD for: …", assumptions reading "This is synthetic demo output — no model provider is configured." Runs record `actual_model: demo`. |
| Build (`idea execute`) | **`auto`** (`executor.kind` unset) | The pinned Hermes coding agent when Docker is running and the coding model's key is set; otherwise the `internal` placeholder file — no model call, no network, not code — with a warning on every surface saying so. |

They are mostly one switch now. A configured `OPENAI_API_KEY` with `idea model set openai/…`
gives you a real PRD and a real plan, and — with Docker running — a real coding agent for
`idea execute`, because an unset `executor.kind` resolves to Hermes. Without Docker, the reasoning
is real and the build is a placeholder, and `idea execute` says so. `executor.kind: internal`
keeps the placeholder on purpose.

### 1. A provider key

```bash
export ANTHROPIC_API_KEY=sk-ant-…        # or OPENAI_API_KEY, GOOGLE_API_KEY, …
idea daemon stop                         # it started at `idea init`, without the key
idea model set anthropic/claude-sonnet-5
idea doctor                              # asks the DAEMON whether it has the key; non-zero if not
```

The `idea daemon stop` is the same point Path A makes above, and it is easy to skip here because
this section reads as self-contained: you are already past `idea init`, so the daemon is running
and it read your environment before this key existed. `idea model set` warns when it can tell —
it compares your shell against the daemon's own view — but stopping the daemon first is what makes
the warning unnecessary. The next command restarts it.

Keys are read from the environment (or the project `.env`), never stored in `harness.yaml`. The two
commands belong together: **setting the model without exporting the key is worse than doing
neither**, because `default_model` short-circuits the demo fallback and the first model call fails
with a non-retryable `401`, sending the initiative to `failed`. `idea doctor` is what catches that
before you spend time on it:

```text
✗ key no ANTHROPIC_API_KEY (anthropic, from default_model) anywhere
  A model is configured, so this is not demo mode: the first call fails with a
  401 the run cannot retry. Export it, then idea daemon stop — or point that field at a provider you have a key for.
```

Run it after `idea model set` and before `idea new`. It **exits non-zero**, because with a
`default_model` set this is not demo mode — the run fails rather than falling back to synthetic
output, which is what step 1b spells out.

The key question is answered by the **daemon**, not by the shell you are typing in. That matters
because the daemon reads its environment once, at start: export a key afterwards and it is visible
to you and invisible to the process that makes the call. `idea doctor` names that case separately
and tells you to run `idea daemon stop`.

### 2. A real executor

```bash
# Hermes — downloads the pinned official image and prints its digest
idea executors setup --model anthropic/claude-sonnet-5 --yes

# OMP — bring your own image
idea executors setup --kind omp --image ghcr.io/your-org/omp:latest --model anthropic/claude-sonnet-5 --yes
```

Any Agent Client Protocol agent (Claude Code, Codex, Gemini CLI, …) is `--kind acp` with an image and
its `--acp-command`, and an OpenHands Agent Server on another machine is `--kind openhands --url …`;
both are in [`docs/agents.md`](agents.md). The rest of this section describes the container kinds.

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

  **This changes the shape of the run, not just its content.** Measured on
  `openai/gpt-5.6-luna`, before the review convergence rules
  ([`review.convergence`](configuration.md#review-convergence)) existed: the PRD review did not
  converge in 2 of 2 runs and the plan review raised blocking findings in 2 of 2, so the sequence
  stopped twice for a human before it reached `idea review`. Those numbers describe the old loop and
  need re-measuring with `idea eval --model <provider/model>`; the new rules have so far only been
  verified against the deterministic scripted provider. `idea new` and `idea next` each name the command that moves it on — `idea approve`
  or `idea revise` for the PRD, `idea approve-plan` or `idea revise-plan` for the plan — and you
  re-run `idea next --until-human` after each. Budget for those stops; they are the governance
  working, not a fault.

- **Requirement codes are not stable across review cycles, so `REQ-1` is a demo-mode artifact.**
  A revision that does not carry a requirement's code forward supersedes it and mints a new one
  (the reviser is asked to keep codes when it revises in place). One measured run created 34
  requirement rows, superseded 22, and ended with 12 live ones numbered `REQ-23`–`REQ-34`. Use
  `idea status` to list the live codes rather than assuming `REQ-1`; verifying a superseded code
  answers `requirement "REQ-1" in state "removed" cannot take a human judgment`. Expect several
  requirements, each with 3–5 acceptance criteria needing sign-off — not the single
  `idea verify REQ-1 --pass` the demo walkthrough shows.
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
| `idea verify` | none at the `quick` and `standard` rigor defaults; one per model-reviewed quality check when one is enabled — `api_ergonomics` (advisory at `thorough`), or a `verify.model_review` check. |

**The agent's own loop is not countable from here**, and it dominates. Hermes and OMP each run
their own agent loop, and the number of turns is a function of the task statement's size, the
repository they are reading, and the model. idea-harness sees one container invocation, not the
turns inside it.

So rather than a number this document cannot support, here is how to get your own:

- **Read the real figures.** Every model response is logged with its cost: `idea logs | grep
  cost_usd` shows `stage`, `actual` model, `duration` and `cost_usd` per call. Run the loop once on
  a throwaway initiative and you have your project's actual number.
- **Cap it before you start.** In `.idea-harness/harness.yaml` (`0` removes the ceiling on the
  first two — **not** on `max_cycles`, where the engine coerces any value `<= 0` to 3):
  `review.max_cost_usd` is a **cumulative** ceiling — the engine adds up every model call's cost and
  fails the call that crosses it, so the run stops; the total is per project and resets when the
  daemon restarts. `review.max_tokens` is a **per-call** figure, not a running total: it sets the
  output-token budget requested for every call and is checked against the response's actual usage.
  `review.max_cycles` bounds the most expensive variable in M1 — the M1 revise
  loop (default 3). See [`docs/configuration.md`](configuration.md) for the full reference.
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
looking for a change set.

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
