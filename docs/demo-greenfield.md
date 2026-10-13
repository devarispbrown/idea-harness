# Demo — a new project, start to finish

A guided walkthrough of the whole loop on an empty directory: idea → reviewed PRD →
execution plan → change set → apply → verify → complete. About two minutes.

**No API key needed.** Everything below runs on the built-in demo provider, which is
deterministic and offline. Artifacts are synthetic and labelled as such — the point of this
walkthrough is the *machinery*, not the prose the model writes. [Use a real
model](#step-8--point-it-at-a-real-model) at the end if you want.

For an existing codebase, see [`demo-existing-project.md`](demo-existing-project.md).

Prerequisites: `idea` on your PATH (see [Install](../README.md#install)).

---

## Step 1 — make a project

```bash
mkdir recipes && cd recipes
idea init
```

```text
✓ initialized project 4c1629f3382af9cd4a2de6dd5529c25a in /…/recipes
next: idea new "<describe your idea>"
```

`idea init` creates `.idea-harness/` here and starts a background daemon. No Git repo is
required. All runtime state — the database, blobs, logs — lives under
`~/.local/share/idea-harness/`, never in this directory.

> **If you have an API key, export it before this step.** The daemon reads your environment
> once, when it starts, and `idea init` is what starts it. A key exported afterwards is
> invisible to it until you run `idea daemon stop`.

## Step 2 — state the idea

```bash
idea new "A recipe sharing app for home cooks"
```

```text
✓ initiative started 6539d3c05b99aebe9ffbd84bcc1d147e
demo mode — synthetic output. Configure a model with: idea model set <provider/model>
working… (--no-wait returns immediately; idea watch follows it)
✓ reviewed PRD ready — next: idea next --until-human
```

This is M1: framing → research → PRD → adversarial review. Four model calls. The command
waits for the run it started, so the next step has something to work with; pass `--no-wait`
if you would rather get the prompt back and follow along with `idea watch` in another
terminal.

## Step 3 — see where it got to

```bash
idea status
```

```text
demo mode — synthetic output. Configure a model with: idea model set <provider/model>

A recipe sharing app for home cooks  ● approved
  requirements: 1 · decisions: 0 · questions: 2
  → Proceed to design & execution plan
    The reviewed PRD is ready. Advance to architecture, product design, verification, and an execution plan.
```

`approved` means the PRD passed adversarial review. Two questions are open — the reviewer
noted things a human should answer. They do not block progress.

Read the PRD itself with `idea prd`, and the open questions with `idea decisions`.

## Step 4 — drive to a change set

```bash
idea next --until-human
```

```text
→ advance …
→ execute …
⚠ NO REAL CODE WILL BE WRITTEN: executor.kind is unset (auto) and no coding model is configured (demo mode), so this run uses the internal placeholder executor. Fix: run `idea model set <provider>/<model>` and set that provider's API key. To keep the placeholder on purpose, set executor.kind: internal in .idea-harness/harness.yaml.
Next: review CHG-1 — idea review CHG-1 approve
```

`idea next` runs the next automatable step and stops at each point that needs your judgment.
`--until-human` keeps going through the automatable ones instead of stopping after one.

Two steps ran here: **advance** (M2 — architecture, product design, verification design,
execution plan, plan review) and **execute** (M3 — the next ready task becomes a change set).
It stopped at `review`, because approving a change set is a human decision.

That warning is honest. With no model configured, the run falls back to the `internal`
executor, which is deterministic and hermetic. It writes a placeholder file, not real code.
Once a model and its key are set and Docker is running, the same unset `executor.kind` runs a
real coding agent instead (see [Wire a real coding agent](agents.md)).

## Step 5 — trace where a requirement came from

Before approving anything, ask why it exists:

```bash
idea why requirement REQ-1
```

```text
Why REQ-1
  REQ-1 —produced_by→ run 6d3b7326
```

`idea impact requirement REQ-1` answers the other direction — what breaks or goes stale if
you change it. `idea trace task TASK-1` shows both. This is the Product Graph, and it is the
reason the tool exists: every node records why it is there.

## Step 6 — review and apply

```bash
idea review CHG-1 approve
idea apply CHG-1
```

```text
✓ review recorded CHG-1 → approved
✓ apply started CHG-1
cancel with: idea cancel ccb4341805f1af543d4e0d4ffbdd885c
```

> **If the review blocks.** With a real model the adversarial reviewer may raise a
> blocking finding on the change set, and `idea review CHG-1 approve` then refuses.
> `idea reviews` lists what it found, and
> `idea review CHG-1 approve --accept-findings` accepts the change set findings and
> all — the same judgment call `idea approve` makes on the PRD. A finding raised by
> the credential sweep is not acceptable that way and still refuses.

`apply` writes the reviewed patch into your working directory and records its pre-image
first, which is what makes the revert in step 9 exact.

## Step 7 — verify and finish

```bash
idea verify REQ-1 --pass
idea next
```

```text
✓ evidence recorded for REQ-1
✓ verified status: complete
  REQ-1  1/1 verified
✓ initiative complete
```

`idea verify` runs the verification gate. `--pass` records your judgment against every
acceptance criterion of that requirement; `--fail` and `--waive` are the other two verdicts.
An unmet gate exits non-zero (`5`, `gate_unmet` — see [exit codes](cli.md#exit-codes)), so `idea verify` is usable in a script.

## Step 8 — point it at a real model

Everything above was synthetic. To get real output:

```bash
export OPENAI_API_KEY=sk-…          # or ANTHROPIC_API_KEY, GOOGLE_API_KEY, …
idea daemon stop                     # it started at `idea init`, without the key
idea model set openai/gpt-5.6-luna
idea doctor
```

The `idea daemon stop` matters — see the note in step 1. If you skip it, `idea doctor` says so
and exits non-zero: it asks the daemon whether it has the key, not the shell you are typing in,
so it can tell "you exported it after the daemon started" from "there is no key anywhere".
`idea model set` flags the same thing at the moment you configure the model, which is earlier.

Then start a fresh initiative with `idea new`. Expect 4–8 model calls for M1 and 5 for M2;
`docs/quickstart.md` has the per-stage cost table.

## Step 9 — undo it, byte for byte

```bash
idea revert CHG-1
```

```text
✓ revert started CHG-1
cancel with: idea cancel 98b6df69d4b9d9f8e90d30aef4238b70
```

Every applied change set is stored as a content-addressed patch alongside its pre-image, so
revert restores exactly what was there — it is file-level, not `git revert`. With
`git_commit: true` in `.idea-harness/harness.yaml`, each apply also makes a git commit
staging only that patch's files.

---

## What to look at next

| Want | Read |
|---|---|
| The same loop on a codebase you already have | [`demo-existing-project.md`](demo-existing-project.md) |
| Real code instead of a placeholder | [`agents.md`](agents.md) |
| An annotated version of this with more detail | [`quickstart.md`](quickstart.md) |
| Every command and flag | [`cli.md`](cli.md) |
| The web UI at `http://127.0.0.1:8787/` | [`ui.md`](ui.md) |
