# The web UI

idea-harness ships a React web UI, embedded in the `idea` binary and served by the local daemon at
**http://127.0.0.1:8787/**. No separate install or server is needed: the daemon auto-starts on the
first `idea` command. A binary built with `go install` alone serves a "web UI not built" placeholder
— `make build` (or a release binary) embeds the real UI; see the README's
[Install](../README.md#install) section.

The UI and the CLI drive the **same state** — anything you click here is immediately visible to
`idea status` and vice versa.

Two things to know before reading the rest:

- **There is no live event stream, and not every screen even polls.** The shell and the five phase
  pages re-fetch on a **2.5-second timer** (`setInterval(load, 2500)` in each phase page;
  `App.tsx:154` uses the same interval with an inline closure). There is no SSE or WebSocket client
  anywhere in `web/src` — the daemon does serve `GET /projects/{id}/events`, but nothing in the UI
  opens it — so a change made elsewhere shows up within a few seconds, not instantly.

  **Five screens do not poll at all** and go stale until you reload: Health, Decision, the doc
  reader, the artifact diff, and Settings. Each loads once on mount.
- **The project is not selectable.** The app loads `GET /api/v1/projects` and uses the **first**
  entry (`App.tsx`). There is no project switcher. With no projects registered it renders "Run
  `idea init` in a project directory to get started"; when the daemon is unreachable it renders
  "Lost connection to the daemon at 127.0.0.1:8787".

## Routes

Routing is hash-based (`web/src/router.ts`). Seven shapes are recognised; anything else falls back
to home.

| Hash | Screen |
|---|---|
| `#/` | Home — all initiatives |
| `#/i/<initiativeId>/<phase>` | A phase page. `<phase>` is one of `explore`, `define`, `design`, `plan`, `build`, `verify` |
| `#/i/<initiativeId>/decision/<decisionId>` | Decision discussion |
| `#/i/<initiativeId>/health` | Graph health |
| `#/i/<initiativeId>/doc/<artifactId>[/<version>]` | Document reader |
| `#/i/<initiativeId>/diff/<fromArtifactId>/<toArtifactId>` | Artifact version diff |
| `#/settings` | Settings |

The daemon additionally serves the API reference at
[`/api-reference`](http://127.0.0.1:8787/api-reference) and the spec at `/openapi.yaml` and
`/openapi.json`. Those are separate pages, not part of this app.

## Layout

A left sidebar (`PhaseNav`) plus a main region.

The sidebar shows:

- the **`ih` logo**, which returns to home;
- the **initiative button** — its title over `<project name> · #<n> of <total>` — which also returns
  to home (there is no dropdown behind the `▾`);
- **Needs you elsewhere** with a count, shown only while you are inside one initiative and *other*
  initiatives have open decisions, questions or findings (from `GET …/attention`);
- the **Phases** rail (below);
- a footer with the **model chip** — the last segment of `default_model`, or `demo` when none is
  set — and a **Settings** button showing the `⌘K` hint. Both navigate to Settings, including the
  chip's `Activity ↗` label, which has no activity view behind it.

The main region renders, above the routed page:

- a **Run failed** banner when the initiative's status is `failed`, showing the failure reason and a
  `provider/model` input with **Retry** (`POST …/retry`);
- a **running card** whenever an operation for this initiative is `running` or `pending` — the
  operation's name, a live elapsed timer, "you can leave this page", and **Stop**
  (`POST /operations/<id>/cancel`).

## Home

The home screen lists every initiative. Its header carries the `ih` mark, the label `my-product`
(a literal in `ProductHome.tsx`, not the project's name), a non-interactive "Settings" label, and a
**+ New idea** button. Below it: the initiative count, a "*n* need you" chip, and an input —
"What are you thinking about building?" — where Enter or **+ New idea** creates the initiative
(`POST …/initiatives`) and jumps to its Explore page.

Each initiative is a card with its number, title, a status chip, and six small phase bars:

| Status | Chip |
|---|---|
| `running` | running |
| `needs_human_judgment` | needs you |
| `approved` | approved |
| `plan_approved` | plan approved |
| `complete` | shipped |
| `failed` | failed |

## The phase rail

Six phases in fixed order — **Explore → Define → Design → Plan → Build → Verify** — each with one of
**three** states (`phases.ts`): **done** (✓, with a one-line summary of what it produced),
**current** (▶, with a live pill), or **future** (no marker). There are no blocked or locked states,
and no counts are rendered under the rail.

Completion is derived client-side from the initiative's status plus its artifacts, tasks and change
sets:

| Phase | Done when… |
|---|---|
| **Explore** | an idea brief **and** a research brief exist |
| **Define** | status is `approved`, `plan_approved` or `complete`, **or** an execution-plan artifact exists |
| **Design** | a technical design, a design spec **and** a verification plan all exist |
| **Plan** | status is `plan_approved` or `complete` |
| **Build** | there is at least one task and **every** task is `done` or `cancelled`, **and** at least one change set and **every** change set is `applied` or `reverted` |
| **Verify** | status is `complete` |

Done summaries read: `brief + research`, `PRD v<n> approved`, `architecture + spec`,
`<n> tasks approved`, `<n> change sets applied`, `<n> requirements verified`. (The last one always
reads `0 requirements verified` in the sidebar — `App.tsx` passes `verifiedRequirements: 0` with a
"wired in the Verify page" note. The home screen's bars use the real count from `…/attention`.)

The current phase carries a pill taken from the initiative's status:

| Status | Pill | Note |
|---|---|---|
| `running` | ▶ | working… |
| `needs_human_judgment` | ! | waiting on you |
| `approved` | ✓ | approved |
| `plan_approved` | ✓ | plan approved |
| `complete` | ✓ | shipped |
| `failed` | ✕ | failed |

## Explore

Two sections: **Grounding** (your uploaded material) and **Produced** (the idea brief and research
brief, each with **Read** and **Export .md**).

Grounding accepts a dropped or browsed file, or pasted text. The file picker accepts
`.md .txt .pdf .docx .xlsx .jpg .jpeg .png .gif .webp`; the dropzone reads
"max 2 MiB · *n* of 100 used". Pasted text is filed under a kind you pick from `prd`, `design`,
`tech`, `market`, `other`.

Each uploaded doc is a row with a coloured kind chip, its title and preview, a **trace** button and
a **Download** button. **The trace button opens the graph-health page** (`#/i/<id>/health`) — it is
not a per-document trace view. For that, use `idea trace context_doc <title-or-id>` in the terminal.

An image that has not been described yet shows a spinner and "Describing the image with a vision
model — the description is what reaches the design agents." An upload rejected for containing a
credential gets its own card explaining that nothing was stored.

Uploaded docs are injected into **every** downstream stage that compiles a context bundle — design,
planning and PRD revisions among them — not into a single stage.

> **Redaction is not uniform, and this page's own copy overstates it.** Text documents are
> credential-swept and redacted at ingest. **Images are not**: they are stored verbatim, served
> verbatim, and sent unredacted to the vision provider, which is an accepted, documented risk.
> [`docs/security.md` → Context docs](security.md#context-docs-uploaded-source-material) is the
> canonical statement; read it before uploading a screenshot that might contain a secret.

## Define

Where the reviewer's output waits on you. When anything is open the page leads with "*n* things
need your judgment — then the PRD revises to v*n+1* and Design unlocks", followed by three kinds of
card:

- **Decisions** — the recommendation as the primary button, each remaining option beside it, and
  **Write my own…** for a free-text resolution. The `DEC-n` chip is a lineage popover: it fetches
  the node's provenance and dependents and shows "Depends on", "Blocks", and a `blocks n` count.
- **Findings** — a severity chip, the finding, its recommendation, and dispositions **Fix it**,
  **Accept risk**, **Not applicable**, **Defer**, with **Fixed**, **Duplicate** and **Rejected**
  behind the `…` overflow.
- **Questions** — an answer box, plus **✨ Suggest** (`POST …/suggest-answers`) which fills in a
  suggested answer you can accept with **Use**.

Resolved decisions and dispositioned findings move to a **Resolved** section; a resolved decision
keeps an **Undo** button (`POST …/decisions/<id>/reopen`).

Under **Produced** the current PRD offers **Read** and, once there is more than one version,
**Diff v*n-1* → v*n***.

The bar at the bottom is the phase's commit action:

| Status | Bar |
|---|---|
| `approved` | "The PRD is reviewed and agreed." → **Proceed to design & plan →** (`advance`) |
| `needs_human_judgment` | "*n* unresolved will use the recommendation." (or "All resolved — regenerate the PRD.") → **Revise PRD to v*n+1* →** (`revise`) |

## Design

Lists the design artifacts — technical design, design spec, verification plan, and a threat model if
one exists — each with **Read**.

When an execution plan exists **and** no change set has been created yet, a **Reopen a design
stage** card offers **Architecture**, **Product design** and **Verification design**. Reopening
re-runs that stage and everything after it, regenerating the plan; it is refused once execution has
begun.

## Plan

The execution plan, editable before you approve it. Each task row shows its code, title, statement,
priority and estimate, with **↑ ↓** to reorder, **Edit**, **Delete** and **Cancel**, plus
**+ Add a task** at the end. The plan artifact appears under **Produced** with **Read** and a diff
against the previous version.

The bottom bar reads "Approving unlocks Build. Tasks run one at a time, each pausing for review."
with **Approve plan →** and **Ask for a revision**.

## Build

The header reads `<executor> executor · placeholder code` — the name comes from `GET …/executors`;
the second half is a fixed string, printed whichever executor is selected. Below it, one card per change set showing its
`CHG-n` code, the task title, the first 12 characters of the patch digest, a status chip, and
**View diff**.

Status chips: draft, checking, review required, rejected, changes requested, superseded, approved,
applying, applied, reverted, conflicted.

The diff renders per file — an **added** / **modified** / **deleted** badge, the path, the size, and
the file's new content — falling back to the raw patch text when the response is not structured.

Buttons follow the status:

| Status | Buttons |
|---|---|
| `review_required` | **Approve**, **Request changes**, **Reject** |
| `approved` | **Apply** |
| `applied` | **Revert** — which opens a confirmation card explaining that this restores the exact pre-apply bytes and leaves git history untouched |
| `conflicted` | **Retry apply**, **Reject** |

These are the same gates as `idea review`, `idea apply` and `idea revert` — including the refusal:
an adversarial reviewer's blocking finding stops **Approve**, and the UI has no equivalent of
`idea review <CHG-n> approve --accept-findings`, so clearing one is a CLI action today. A finding
from the credential sweep carries a deny code and is not acceptable either way.

A change set rejected by a deterministic boundary renders instead as a **SYSTEM DENY** card carrying
the deny code: it cannot be approved, overridden or applied, and the task is re-planned.

## Verify

A coverage meter — "*p* of *t* criteria · *v* of *r* requirements verified", plus "· *w* waived"
when any are — over one card per requirement.

Each card shows the requirement code and statement, a `verified` / `failed` / `waived` pill once it
has one, an *n*/*n* criteria count, and the acceptance criteria as `metric target (condition)`.
Requirements still awaiting evidence get an evidence-note field and four actions: **Pass**,
**Fail**, **Waive**, and **Add verify task** (which generates a verification task for the
requirement). A waived requirement offers **Un-waive (pass)**.

**Pass** is the equivalent of `idea verify REQ-1 --pass --note "<how you checked>"`, and the note is
recorded as evidence in the Product Graph.

## Other surfaces

**Graph health** (`#/i/<id>/health`) — the deterministic risk profile with its tier and detected
signals ("Derived deterministically from the patch contents — no model, no score"), a
"Traceability gaps · *n*" list from the graph audit, and a daemon row showing the address, version
and state directory. This is where Explore's **trace** button lands.

**Document reader** (`#/i/<id>/doc/<artifactId>/<version>`) — the artifact body rendered with a
deliberately minimal Markdown renderer (headings, bold, lists, paragraphs; React nodes, never raw
HTML). Below it, comments: add one with an optional section, and **Promote to question** turns a
comment into an open question.

**Artifact diff** (`#/i/<id>/diff/<from>/<to>`) — a line diff of two artifact versions, `+` added,
`-` removed, everything else context.

**Decision discussion** (`#/i/<id>/decision/<decisionId>`) — a thread you can add to, resolve
buttons for the recommendation and each option, and a "What changed" panel with **Mark seen**. No
button in the app navigates here today; reach it by typing the URL.

**Command palette** — `⌘K` / `Ctrl-K` opens a filter box over the six phases of the current
initiative plus **All initiatives** and **Settings**. `↑`/`↓` move, Enter runs, Escape closes.

## Settings

Settings writes the same `.idea-harness/harness.yaml` the CLI edits. A **Demo mode** banner —
"synthetic output. Configure a model provider to run real agents" — appears at the top of *this
page only* when the daemon reports demo mode, which means one thing and not the two this used to
describe: the configured `default_model` does not resolve to a registered provider, so requests
fall through to the built-in one (`internal/daemon/providers.go`, `isDemo`).

Environment variables are not consulted. A key exported with no `default_model` set is still demo
mode; a `default_model` naming a provider you have no key for is **not** demo mode — it is a run
that will fail with a 401, which is why `idea doctor` reports that case separately.

### Model

Pick a **Provider** from the configured list (each labelled "key missing", "refused" or "not
configured" when it is not ready); the model list for that provider loads live. Filter it, click a chip, or type any
`provider/model` into **Selected model**, then **Save model** — which sets `default_model` and
confirms "Saved — will apply to the next initiative." **Reload models** re-fetches the list. Leaving
it blank auto-selects.

### Providers

Each provider's readiness — **ready**, **missing key**, **refused**, or **not configured** — with
its type, base URL and the environment variable its key is read from.

**refused** is its own state, not a kind of missing key. The daemon rejects an entry that would
send a preset's `api_key_env` somewhere else — an overridden `base_url` on a built-in preset — and
the reason is printed under the provider. The key may well be set; setting it again fixes nothing,
and the file is what needs editing. These were all rendered as "missing key" until the daemon began
reporting `key_set` separately from `ready`.

### Add provider

Six presets: **OpenAI**, **Anthropic**, **Google Gemini**, **DeepSeek**, **OpenRouter** and
**Ollama (local)**. Choosing one fills in its base URL and the environment variable its key is read
from, and saving registers the provider under that preset's name. API keys are read from environment
variables, never stored inline.

The form cannot register a **custom** endpoint: its provider name is always one of the six, and the
daemon refuses a preset whose `base_url` has been changed — that combination would send the preset's
well-known key to an operator-supplied host (the one exception is Ollama moving between loopback
ports). A custom OpenAI-compatible endpoint needs a new provider *name*, so it goes in from the
terminal: `idea model add groq --type openai --base-url https://api.groq.com/openai/v1 --key-env
GROQ_API_KEY`.

### Executor

- **Kind** — `internal` (the default placeholder), `container`, `omp`, or `hermes`.
- **Image** — the container image carrying the agent CLI.
- **Model** — the coding model as `provider/model`; empty falls back to the default.

`internal` shows "placeholder executor — writes no real code. Select hermes/omp for real code."
Selecting `omp` or `hermes` shows a warning:

> Runs with network egress + auto-approve and receives its provider API key (see
> [`security-model.md`](security-model.md#what-this-does-not-protect-against)). Only use an image you
> trust.

That is the same tradeoff `idea executors setup` prints. Read [`docs/agents.md`](agents.md) before
switching; the one-liner is `idea executors setup --kind hermes|omp --image <img> --model
<provider/model> --yes`.

### Apply

A single **git commit on apply** checkbox — the `git_commit: true` option in
`.idea-harness/harness.yaml`. When enabled, each applied change set also creates a git commit
staging only the patch's files. The resulting commit SHA is not surfaced anywhere in the UI; revert
remains a byte-for-byte file restore, not git history.
