# Demo — an existing codebase, start to finish

The same loop as the [greenfield walkthrough](demo-greenfield.md), pointed at a repository
that already has code in it. About three minutes.

**Read this first.** With no model key configured, this walkthrough does *not* modify your
source files. It runs the whole governed loop — grounding, PRD, plan, change set, review,
apply, revert — against your real repository, and the change set it applies is a placeholder
file. Generating real edits to your code needs a coding agent: the default executor (`auto`)
runs one by itself once a model key is set and Docker is running, which is why that is
[a deliberate step](agents.md) covered at the end, not something to do first.

That is the honest shape of the tool today, and it is worth knowing before you point it at
something you care about: **`idea apply` writes into your working directory.** Run this on a
branch, or on a scratch clone, the first time.

Prerequisites: `idea` on your PATH, and a repository with some code in it.

---

## Step 1 — initialise inside the repo

`cd` into the repository and run:

```bash
idea init
```

```text
✓ initialized project 6afe47c87c721d5f0489bdf49ffa00e3 in /…/acme-api
web UI: http://127.0.0.1:8787
next: idea new "<describe your idea>"
```

Nothing existing is touched. It adds one directory:

```bash
git status --short
```

```text
?? .idea-harness/
```

**Commit it or ignore it, deliberately.** `.idea-harness/` holds `project.yaml`,
`harness.yaml` and the role definitions — configuration that is portable and worth
committing so a teammate gets the same setup. Runtime state is *not* in there; it lives
under `~/.local/share/idea-harness/`. If you would rather not commit it, add
`.idea-harness/` to `.gitignore` now, before it ends up in a diff by accident.

> **If you have an API key, export it before `idea init`.** The daemon reads your
> environment once, at start, and `idea init` starts it. Otherwise run `idea daemon stop`
> and the next command restarts it with the key.

## Step 2 — ground it on the code you already have

This is the step that differs most from greenfield, and the ordering matters:

```bash
idea new "Add tiered pricing: volume discounts above 10k calls" --doc src/billing.py
```

```text
✓ initiative started ac69c678bf0e67f36491ba0400a02f3e
demo mode — synthetic output. Configure a model with: idea model set <provider/model>
working… (--no-wait returns immediately; idea watch follows it)
✓ reviewed PRD ready — next: idea next --until-human
```

`--doc` attaches files at creation, so the framing and PRD stages see your actual code
rather than reasoning from the title alone. Pass it more than once for several files.

**`idea docs add` does not work before `idea new`:**

```bash
idea docs add src/billing.py     # before any initiative exists
```

```text
idea: no initiatives yet; run `idea new` first
```

Context docs attach *to an initiative*, so there has to be one. Use `--doc` at creation, or
`idea docs add` afterwards — both reach the same place:

```bash
idea docs
```

```text
Context docs
  other billing.py
      """Usage billing for the API."""

RATE_PER_CALL = 0.002


def charge(calls: int) -> float:
    return calls * RATE_PER_CALL
```

The preview is the first 200 characters of the file with one leading indent, so
anything past the first line sits flush left. That is the preview, not the stored
doc.

Text formats are credential-swept and redacted at ingest. Images are stored verbatim and
described by a vision model — see [`security.md`](security.md) before uploading a screenshot
that might contain a secret.

## Step 3 — run the loop

```bash
idea next --until-human
```

```text
→ advance …
→ execute …
⚠ NO REAL CODE WILL BE WRITTEN: executor.kind is unset (auto) and no coding model is configured (demo mode), so this run uses the internal placeholder executor. Fix: run `idea model set <provider>/<model>` and set that provider's API key. To keep the placeholder on purpose, set executor.kind: internal in .idea-harness/harness.yaml.
Next: review CHG-1
  idea review CHG-1 approve  review it, and approve if it passes
  idea review CHG-1 approve --accept-findings  accept it, findings and all
outcomes: 1 metric(s) · 1 no data
```

Identical to greenfield from here. Inspect what it intends to change before approving:

```bash
idea change CHG-1        # status, task, patch digest
idea diff                # requirements and decisions added or removed
```

## Step 4 — apply, and see exactly what moved

```bash
idea review CHG-1 approve
idea apply CHG-1
```

> **If the review blocks.** With a real model the adversarial reviewer may raise a
> blocking finding on the change set, and `idea review CHG-1 approve` then refuses.
> `idea reviews` lists what it found, and
> `idea review CHG-1 approve --accept-findings` accepts the change set findings and
> all — the same judgment call `idea approve` makes on the PRD. A finding raised by
> the credential sweep is not acceptable that way and still refuses.

Then look at your repository rather than at the tool's output:

```bash
git status --short
```

```text
?? .idea-harness/
?? hello.txt
```

`hello.txt` is the placeholder. **Your existing files are untouched** — verify it directly:

```bash
md5 src/billing.py    # or md5sum on Linux
```

The digest is the same before and after. That is the `internal` placeholder executor doing
exactly what it says: it is deterministic, hermetic, makes no model call and no network
request, and it writes a placeholder so the apply/verify/revert machinery can be exercised
without a coding agent in the loop.

With `git_commit: true` in `.idea-harness/harness.yaml`, each apply also makes a git commit
staging only that patch's files. It is off by default, which is why `git log` is unchanged
here.

## Step 5 — revert, and confirm the repo is pristine

```bash
idea revert CHG-1
```

```text
✓ revert started CHG-1
cancel with: idea cancel dba7535cfa0eb1e30000800a12073913
```

```bash
git status --short
```

```text
?? .idea-harness/
```

`hello.txt` is gone and nothing else moved. Revert restores each file's recorded pre-image
byte for byte, so a file that existed before the apply comes back exactly as it was, and one
that did not is removed. This is file-level and does not depend on Git.

This is the property worth testing on your own repository before you trust an agent with it.

## Step 6 — generate real code

Everything above ran without a model touching your source. For real edits you need a coding
agent in a container. Once a model key is set (`idea model set`, with the key exported before the
daemon starts) and Docker is running, the default `auto` executor already picks the pinned Hermes
agent. Wire it explicitly, and prove it works, before you let it near this repository:

```bash
idea executors setup --model anthropic/claude-sonnet-5 --yes --smoke
```

That auto-downloads the pinned Hermes image and, with `--smoke`, runs a trivial task through
it and asserts it produced a real file change before you trust it with anything else. Other
agents — OMP, Claude Code or Codex over ACP, an OpenHands Agent Server — are `--kind` choices in
[`agents.md`](agents.md).

**Read [`agents.md`](agents.md) first.** The agent container is sandboxed for filesystem and
process isolation but is deliberately *not* network-isolated — it needs egress to reach its
model API, it receives its own provider API key, and it runs in auto-approve mode. Those are
real tradeoffs and that document sets them out plainly.

After that, `idea next --until-human` produces change sets containing actual edits to your
files, and steps 3–5 work the same way — with `idea change CHG-n` and `idea diff` now worth
reading closely before you approve.

---

## What to look at next

| Want | Read |
|---|---|
| The loop on an empty directory | [`demo-greenfield.md`](demo-greenfield.md) |
| Wiring a coding agent, and its security tradeoffs | [`agents.md`](agents.md) |
| What is redacted, and what is not | [`security.md`](security.md) |
| Every command and flag | [`cli.md`](cli.md) |
