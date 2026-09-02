# The web UI

idea-harness ships a React web UI, embedded in the `idea` binary and served by the local daemon at
**http://127.0.0.1:8787/** (the daemon listens on `127.0.0.1:8787`). No separate install or server
is needed: the daemon auto-starts on the first `idea` command. The UI and the CLI drive the **same
state** — anything you click here is immediately visible to `idea status` and vice versa; they are
two views onto one Product Graph.

## Top bar

- **Project selector** — switch between projects (directories where you ran `idea init`).
- **Initiative selector** — when a project has more than one initiative, switch between them. Each
  initiative advances independently, one task at a time.
- **Ideas / Settings** — the two main views.
- **⌘K** (or Ctrl+K) — a command palette for jumping around quickly.
- **+ New idea** — start a new initiative (the equivalent of `idea new "<idea>"`).

When no model provider is configured, a **Demo mode** banner appears at the top — output is
clearly synthetic. Configure a model in Settings to switch to real output.

## Workflow progress stepper

The Overview shows a six-step stepper for the current initiative:

**Explore → Define → Design → Plan → Build → Verify**

| Step | Done when… |
|---|---|
| **Explore** | framing + research complete (idea brief and research brief exist) |
| **Define** | a reviewed PRD exists |
| **Design** | technical design, design spec, and verification plan exist |
| **Plan** | the execution plan is approved |
| **Build** | at least one change set has been applied |
| **Verify** | verification is recorded; the initiative is complete |

Each step is marked **done** (✓), **active** (in progress), **blocked** (! — something needs your
judgment), or **locked** (🔒 — can't start until earlier steps finish). When the initiative needs
human judgment, a note under the stepper tells you how many findings, decisions, and questions are
waiting on you.

## Tabs

Eleven tabs organize the initiative's detail:

- **overview** — current understanding, recommended next move, the stepper, the execution plan, and
  anything awaiting your judgment.
- **requirements** — the reviewed requirements and their acceptance criteria, plus the **Verify**
  action (see below).
- **decisions** — open and resolved decisions; accept, decline, or edit the resolution, or
  "Discuss" to ask the team for more context.
- **findings** — review findings with severity, impact, and recommendation; disposition each as
  "Fix it", "Accept risk", "Not applicable", or "Defer".
- **questions** — open questions from the review, with an answer field.
- **artifacts** — every generated artifact (idea brief, research brief, PRD, technical design, …)
  with **View** and **Export**.
- **tasks** — the dependency-ordered execution plan tasks, each with priority, estimate, and a
  "Why?" provenance link.
- **changes** — change sets (see below).
- **context** — upload context docs (see below).
- **activity** — "What Changed" grouped by category, plus a live event feed.
- **logs** — recent daemon log lines.

## Context tab

The **context** tab grounds the agents in your own material. Upload a PRD, design system, tech
spec, market analysis (`.md` / `.txt` / `.pdf` / `.docx` / `.xlsx`), a UI mockup image
(`.jpg` / `.png`), or paste text directly. Each doc is credential-swept and redacted on ingest,
then content-addressed and injected into design & planning (and PRD revisions) — never served or
fed to a model unredacted. Images are described by a vision model before they reach the design
agents. Uploaded docs are listed with a **Trace** link to `idea trace context_doc <id>`.

## Verify action (Requirements tab)

In the **requirements** tab, each requirement (`REQ-1`, `REQ-2`, …) lists its acceptance criteria.
Once you've applied the change set that satisfies a requirement, mark it passed by typing an
optional note and clicking **Pass** — the equivalent of `idea verify REQ-1 --pass`.

Each Pass records verification evidence in the Product Graph and updates the coverage shown above
the list (e.g. `REQ-1 1/1`). When the last requirement is passed, the initiative reaches
**complete**: the tab shows a "✓ complete" badge and the stepper's final step lights up.
Requirements whose acceptance criteria need human eyes are flagged **needs manual**.

## Changes tab

The **changes** tab lists change sets (`CHG-1`, …), each showing:

- its **status** chip (e.g. review required, approved, applied),
- its short **patch digest**, and
- when git commit on apply is enabled, the **git commit SHA** of the applied commit.

Buttons appear per status: **Approve / Request changes / Reject** while a change set awaits review,
**Apply** once approved, **Revert** after it's applied, and **View diff** to inspect the patch.
These are the same human gates as `idea review`, `idea apply`, and `idea revert`.

## Settings

Settings configures the model, providers, executor, and apply behavior. It writes the same
`.idea-harness/harness.yaml` the CLI edits.

### Model & Providers

- **Model** — pick a provider and a model from the live list (or type any `provider/model`), then
  Save. Leave blank to auto-select.
- **Providers** — see each provider's readiness ("ready", "missing key", "not configured").
- **Add provider** — add a provider (OpenAI, Anthropic, Gemini, DeepSeek, OpenRouter, Ollama, or a
  custom OpenAI-compatible endpoint). API keys are read from environment variables, never stored
  inline.

### Executor

The **Executor** section switches what `execute` uses to build:

- **Kind** — `internal` (the default placeholder), `container`, `omp`, or `hermes`.
- **Image** — the container image with the agent CLI (required for OMP; Hermes auto-downloads its
  pinned official image when omitted).
- **Model** — the coding model as `provider/model` (falls back to the default model).

`internal` is the safe default: deterministic and hermetic, it writes **no real code** — a note
under the form says so. Selecting `omp` or `hermes` shows a security warning:

> Runs with network egress + auto-approve and receives its provider API key. Only use an image you
> trust.

That warning is the same tradeoff the CLI prints in `idea executors setup`. Read
[`docs/agents.md`](agents.md) for the full details before switching to a real agent; the
equivalent one-liner is `idea executors setup --kind hermes|omp --image <img> --model
<provider/model> --yes`.

### Apply

The **Apply** section has a single toggle: **git commit on apply**. When enabled, each applied
change set also creates a git commit (staging only the patch's files), and the SHA appears on the
change set in the **changes** tab. This is the `git_commit: true` option in
`.idea-harness/harness.yaml`; revert remains a byte-for-byte file restore, not git history.
