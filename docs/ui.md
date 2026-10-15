# The web UI

idea-harness ships a React web UI, embedded in the `idea` binary and served by the local daemon at
**http://127.0.0.1:8787/**. No separate install or server is needed: the daemon auto-starts on the
first `idea` command. A binary built with `go install` alone serves a "web UI not built" placeholder
— `make build` (or a release binary) embeds the real UI; see the README's
[Install](../README.md#install) section.

The UI and the CLI drive the **same state** — anything you click here is immediately visible to
`idea status` and vice versa.

Two things to know before reading the rest:

- **The shell follows the project's event stream; most pages still poll.** The shell (`App.tsx`:
  initiatives, the What changed and attention badges, the open initiative's data), the running card
  and the queue page refetch when `GET /projects/{id}/events` (SSE) reports a change, with a slow
  timer as a backstop, and fall back to their old timer when the stream cannot be opened
  (`web/src/useEventStream.ts`). The run panel streams its run's events live. The five phase pages
  still re-fetch on a **2.5-second timer** (`setInterval(load, 2500)`), so a change made elsewhere
  shows up there within a few seconds, not instantly.

  **The other screens do not poll** and go stale until you reload: Health, What changed, Decision,
  the doc reader, the artifact diff, and the Product Graph pages (lineage, Gaps, Outcomes,
  Quality). Each loads once on mount. Settings loads once too, and polls only while a smoke test it
  is showing is still running.
- **The project is not selectable.** The app loads `GET /api/v1/projects` and uses the **first**
  entry (`App.tsx`). There is no project switcher. With no projects registered it renders "Run
  `idea init` in a project directory to get started"; when the daemon is unreachable it renders
  "Lost connection to the daemon at 127.0.0.1:8787".

## Routes

Routing is hash-based (`web/src/router.ts`). Anything that matches none of the shapes below renders
**Page not found** — it no longer falls back to home — and every route that names an initiative
renders **Initiative not found** when that id is not in this project, instead of the page with
nothing in it.

| Hash | Screen |
|---|---|
| `#/` | Home — all initiatives |
| `#/queue` | **Needs you** — open decisions, questions and findings across every initiative, highest impact first (`GET …/queue`); reached from the sidebar's "Needs you elsewhere" row |
| `#/i/<initiativeId>/<phase>` | A phase page. `<phase>` is one of `explore`, `define`, `design`, `plan`, `build`, `verify` |
| `#/i/<initiativeId>/changed` | **What changed** — the initiative's timeline, grouped by day (`GET …/timeline`, the same narration as `idea changed`); the sidebar's **Activity ↗** and the shell's "See full history" both land here |
| `#/i/<initiativeId>/decision/<decisionId>` | Decision discussion (by id or `DEC-n` code; an unknown one renders **Decision not found**) |
| `#/i/<initiativeId>/health` | Graph health |
| `#/i/<initiativeId>/lineage/<type>/<ref>` | One Product Graph node's lineage. `<type>` is a graph node type (`requirement`, `task`, `decision`, `assertion`, `artifact`, `context_doc`, `objective`, `metric`, `change_set`, `quality_result`, `question`, `run`); `<ref>` is its code (`REQ-3`, `CHG-1`), an artifact kind, a quality dimension, or an id |
| `#/i/<initiativeId>/gaps` | Every Product Graph gap |
| `#/i/<initiativeId>/outcomes` | Outcomes dashboard |
| `#/i/<initiativeId>/quality` | Quality scorecard and history |
| `#/i/<initiativeId>/doc/<artifactId>[/<version>]` | Document reader |
| `#/i/<initiativeId>/diff/<fromArtifactId>/<toArtifactId>` | Artifact version diff |
| `#/settings` | Settings |

Every route may carry a **project prefix**, `#/p/<projectId>/…` — `#/p/<projectId>/i/<id>/build`
opens that change set in that project, whichever one the reader last chose. A bare `#/…` means the
current project, and the shell rewrites it to the prefixed form in place (`replaceState`, so Back is
unchanged), so a copied address — and every push notification's link — names its project. A prefix
naming a project the daemon does not serve renders **Project not found**.

The daemon additionally serves the API reference at
[`/api-reference`](http://127.0.0.1:8787/api-reference) and the spec at `/openapi.yaml` and
`/openapi.json`. Those are separate pages, not part of this app.

## Layout

A left sidebar (`PhaseNav`) plus a main region.

The sidebar shows:

- the **`ih` logo**, which returns to home;
- the **project switcher** — a labelled select of every project the daemon serves — when there is
  more than one. The choice is remembered in this browser (`localStorage`) and a link naming a
  project wins over it; switching goes to that project's home;
- **Needs you** with a count of everything waiting across the project, when no initiative is open
  (Home, Settings, the queue) — on a phone, that is the top bar's way into the queue;
- the **initiative button** — its title over `<project name> · #<n> of <total>` — which also returns
  to home (there is no dropdown behind the `▾`);
- **Needs you elsewhere** with a count, shown only while you are inside one initiative and *other*
  initiatives have open decisions, questions or findings (from `GET …/attention`);
- the **Phases** rail (below);
- a footer with the **model chip** — the last segment of `default_model`, or `demo` when none is
  set — which opens Settings; inside an initiative, **Activity ↗** (its What changed timeline) and
  **Health ↗** (Graph health); and a **Settings** button showing the `⌘K` hint.

Above both, on every page, a **Demo mode** banner while no model is selected for the project — "every
document, review and change you see here is synthetic" — linking to Settings. It is the only one;
Settings no longer adds a second.

The main region renders, above the routed page:

- a **Run failed** banner when the initiative's status is `failed`, showing the failure reason, a
  `provider/model` input, an optional **Note for the retry** and **Retry** (`POST …/retry` with
  `model` and `note` — the note joins the framing prompt, as `idea retry --note` does);
- a **running card** whenever an operation for this initiative is `running` or `pending` — the
  operation's name, a live elapsed timer, "you can leave this page", and **Stop**
  (`POST /operations/<id>/cancel`).

## Home

The home screen lists every initiative. Its header carries the `ih` mark, the project's name, a
**Settings** button, and a **+ New idea** button. Below it: the initiative count, a "*n* need you" chip, and an input —
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
a **Download** button. **trace** opens that document's lineage, `#/i/<id>/lineage/context_doc/<docId>`
— the same question as `idea trace context_doc <title-or-id>`.

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

**Accept all recommendations** (`POST …/decisions/accept-recommendations`, `idea
accept-recommendations`) appears while a decision is open and resolves every open decision with its
own recommendation, then reports how many it resolved, which it skipped and why, and how many open
questions it did not touch — a question carries no recommendation, so it is never answered for you.
With only questions open, the button is replaced by a line saying exactly that.

Resolved decisions, dispositioned findings and **answered questions — with their answer** — move to
a **Resolved** section; a resolved decision keeps an **Undo** button
(`POST …/decisions/<id>/reopen`).

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
**+ Add a task** at the end. The plan artifact appears under **Produced** with **Read** and, once a
revision produced a second version, **Diff v*n-1* → v*n***.

The bottom bar follows the plan's state:

| State | Bar |
|---|---|
| Awaiting approval | "Approving accepts the plan as it stands and unlocks Build…" → **Approve plan →**, with **Ask for a revision…** |
| Parked for another reason (e.g. `plan_dependencies`) | "This plan needs regenerating before it can be approved." → **Ask for a revision…** |
| Approved, nothing built yet | "This plan is approved. Build runs it task by task." → **Ask for a revision…** |
| Approved, Build started | "This plan is approved and Build has started. Revising is closed once a change set exists." → **Go to Build →** |

**Ask for a revision…** opens a note — "What should the revised plan change?" — that joins the
planner's inputs (`POST …/revise-plan {"note"}`, `idea revise-plan --note`). When the revision
settles the page says what it changed: "Plan revised: v1 → v2 (added TASK-3)", or "The revision
produced the same plan: still v1, tasks unchanged." An identical plan keeps its version, and a task
that kept its title keeps its code. The pill reads **approved** for an approved plan whatever the
viewport and whether or not Build has started.

## Build

The header reads `<executor> executor`, with `· placeholder code` appended only for `internal`.
Above the cards, the next task's **Run TASK-n: <title>** button, and under it a labelled **Note to
the agent for TASK-n** field — appended to the implementer's brief, as `idea execute --note` does. A
failed run's banner has its own note field for its **Retry**. Below, one card per change set showing
its `CHG-n` code, the task title, the first 12 characters of the patch digest, a status chip, and
**View diff**.

Status chips: draft, checking, review required, rejected, changes requested, superseded, approved,
applying, applied, reverted, conflicted. The chip is always the status; what is also true about the
change set rides beside it as a marker — **demo** for placeholder output (the `internal` executor
or the demo model), **no file changes** for a no-op, **no changes** when the agent produced no
diff. An applied demo change set reads `APPLIED` `DEMO`.

The diff renders per file — an **added** / **modified** / **deleted** badge, the path, the size, and
the file's new content — falling back to the raw patch text when the response is not structured.

Buttons follow the status:

| Status | Buttons |
|---|---|
| `review_required` | **Approve**, **Approve, accepting *n* findings** (only while the reviewer left *n* open blocking findings), **Request changes…**, **Reject** |
| `approved` | **Apply** |
| `applied` | **Revert** — which opens a confirmation card explaining that this restores the exact pre-apply bytes and leaves git history untouched |
| `conflicted` | **Retry apply**, **Reject** |

These are the same gates as `idea review`, `idea apply` and `idea revert` — including the refusal:
an adversarial reviewer's blocking finding stops **Approve**, and **Approve, accepting *n*
findings** is `idea review <CHG-n> approve --accept-findings`. A finding from the credential sweep
carries a deny code and is not acceptable either way.

**Request changes…** opens "What should change in CHG-n?"; **Send back with this note** records the
verdict with the note (`POST …/review {"verdict":"changes_requested","note"}`, `idea review <CHG-n>
changes_requested --note`). The card then shows "You asked: …", and the task's next run gets the
note in its brief, labelled with the change set it came from.

A change set rejected by a deterministic boundary renders instead as a **SYSTEM DENY** card carrying
the deny code: it cannot be approved, overridden or applied, and the task is re-planned.

### Run panel

While an execute runs, the running card shows the run panel (`web/src/RunPanel.tsx`). When nothing
is running, the Build page keeps the last run under a collapsed **Last agent run**, with its files,
actions, notes and report. The panel shows:

- **Current action** - a chip with what the agent is doing now: "editing src/x.ts", "running npm
  test", "reading …". It falls back to **Thinking** while the agent reasons (only the fact is
  shown; the reasoning text is never stored).
- **Actions** - tool calls grouped by call, summarized as "7/12 actions" (with the failed count),
  expandable to the list with each call's status.
- **Files** - the files the agent has written, live, with +/- line counts; replaced by the
  orchestrator's own diff once the agent finishes.
- **Permissions answered for you** - for an ACP agent, every permission it asked for and the answer
  the harness gave.
- **Meter** - elapsed time, tokens and cost where the executor reports them, and the executor.
- **Errors** - surfaced at the top as an alert.
- **Send a note** - steer the running agent (see [agents.md](agents.md#steering-a-running-agent)).
  For an executor that cannot be steered the box says the note will apply to the next run, and
  notes held for the next run are listed.
- **Open in OpenHands** (when `executor.openhands.ui_url` is set) and **Report** downloads.

It reads the run's stored events first, so a reload or a second tab mid-run shows the whole run so
far, then follows the run's SSE stream (`web/src/useEventStream.ts`). A dropped connection
reconnects with backoff (1 s doubling to 30 s) and resumes after the last event it has - never
replaying the run - the stream is closed while the tab is hidden and resumed when it is shown, and
if the stream cannot be opened at all the panel polls instead (and says "polling"). The running
card, the What changed and attention badges and the queue page refetch when the project's event
stream reports a change, with a slow timer as a backstop, instead of polling every few seconds.

## Verify

A coverage meter — "*p* of *t* criteria · *v* of *r* requirements verified", plus "· *w* waived"
when any are — over one card per requirement.

Each card shows the requirement code and statement, a `verified` / `failed` / `waived` pill once it
has one, an *n*/*n* criteria count, and the acceptance criteria as `metric target (condition)`.
Requirements still awaiting evidence get a note field and **Waive** and **Add verify task** (which
generates a verification task for the requirement); **Pass** and **Fail** appear beside them once a
change set has been built and applied — until then a line above the cards says so, and the buttons
are not shown at all. A waived requirement offers **Un-waive (pass)**.

**Pass** is the equivalent of `idea verify REQ-1 --pass --note "<how you checked>"`, and the note is
recorded as evidence in the Product Graph.

## Other surfaces

**Graph health** (`#/i/<id>/health`) — the deterministic risk profile with its tier and detected
signals ("Derived deterministically from the patch contents — no model, no score"), a
"Product gaps · *n*" list from `GET …/gaps` (the same report as `idea gaps`: the eight most severe,
each with its why and the command that closes it, and a **See all** link to the Gaps page — see
[architecture.md](architecture.md#asking-the-graph-gaps)), a
"Traceability gaps · *n*" list from the graph audit, a **Quality** card (below), and a daemon row
showing the address, version and state directory. The sidebar's **Health ↗** opens it.

**Quality** card (on Graph health, and on Build once a change set is applied) — the quality-dimension
scorecard (`GET .../quality`): each enabled dimension with its mode (`advisory`/`gate`) and status,
the checks needing attention with their `[deterministic]`/`[model]` method and file:line citation,
which requirements the result is evidence for, whether the gated dimensions meet the gate, and a
**Measure** button (`POST .../quality`). A dimension never measured, or measured against older work,
reads "not measured" / "(stale)" in the warning tone — never green. Hidden when every dimension is
off. See [configuration](configuration.md#quality-dimensions-verify).

**Document reader** (`#/i/<id>/doc/<artifactId>/<version>`) — the artifact body rendered with a
deliberately minimal Markdown renderer (headings, bold, lists, paragraphs; React nodes, never raw
HTML). Below it, comments: add one with an optional section, and **Promote to question** turns a
comment into an open question.

**Artifact diff** (`#/i/<id>/diff/<from>/<to>`) — a line diff of two artifact versions, `+` added,
`-` removed, everything else context.

**Decision discussion** (`#/i/<id>/decision/<decisionId>`) — a thread you can add to, resolve
buttons for the recommendation and each option, a **Why / Impact** panel, and a "What changed"
panel with **Mark seen**, in words ("Plan tasks replaced", "1 stale mark cleared") rather than event
names. Reached from a decision's timeline entry, a decision node's lineage (**Open where it
lives**), or an invalidated-decision gap; an unknown decision renders **Decision not found** with a
way back to Define, not an empty thread.

**What changed** (`#/i/<id>/changed`) — the initiative's timeline in full, grouped by day: change sets
applied, rejected, denied or reverted, decisions changed, stale marks, failed or stopped runs, and
completed phases, each linking to where it happened. The compact panel above every page shows the
newest few.

**Needs you** (`#/queue`) — every open decision, question and blocking finding across the project's
initiatives, ranked, each linking to where it is resolved.

**Command palette** — `⌘K` / `Ctrl-K` opens a filter box over the six phases of the current
initiative plus **All initiatives** and **Settings**. `↑`/`↓` move, Enter runs, Escape closes.

## Product Graph views

The sidebar's **Product graph** group (below the phases; a second row under them on a narrow
screen) opens the three initiative-wide views. Every node in them, and in the panels below, is
written code-first (`REQ-3`, `TASK-2`, `CHG-1`) and links to its own lineage page.

**Lineage** (`#/i/<id>/lineage/<type>/<ref>`) — the answers `idea why`, `idea impact` and
`idea trace` give, from the same `GET …/trace`. **Why** is the provenance tree: what the node points
at (derived from, implements, serves, produced by), followed up to its sources. **Impact** is the
dependents tree — everything that points at it, through to the change sets built from its tasks
and the metrics attributed to those change sets — with a count per staleness class (`stale`,
`obsolete`, `to revalidate`); rows are badged only when stale or obsolete. A node reachable two ways
is expanded once and marked "(shown above)" elsewhere. **Full trace** is every edge as a table. The
`<ref>` resolves exactly as the CLI resolves it (id, code, kind or title; codes case-insensitively;
a live row before a superseded one), plus an artifact's graph id and an unambiguous id prefix.
**Open where it lives** goes to the page that shows the node; the `idea trace` command is there to
copy.

**Why / Impact panel** — a disclosure on each requirement (Define's **Requirements** list), task
(Plan), change set (Build), the decision page, and each objective and metric (Define's Objectives
section, Outcomes). Nothing is fetched until it is opened. It shows the first ring each way, the
transitive dependent count with how many are stale, and **Full lineage →**.

**Gaps** (`#/i/<id>/gaps`) — the whole `GET …/gaps` report (Health shows the top eight and links
here), grouped by kind with the most severe group first, filterable by kind and severity. Each gap
shows its why, the nodes it is about and its trigger. The closing action is a button where the UI
can take it — **Accept** / **Dispute** / **Retract** on an unvalidated assumption records the
judgment in place (`POST /assertions/{id}/status`); unimplemented requirements and orphan tasks link
to Plan, unverified requirements and untested criteria to Verify, an invalidated decision to its
discussion, and gaps about objectives or metrics to Outcomes. Every gap also shows its `idea …`
command with a **Copy** button, which is the only action for a kind the UI does not know yet.

**Outcomes** (`#/i/<id>/outcomes`) — each objective with its metrics. A metric card shows the latest
value, baseline → target, a progress bar (the share of the way from the effective baseline to the
target; absent for `maintain` metrics or before a value exists), the drift badge (`on track`,
`off track`, `regressed`, `stale`, `no data`) with the daemon's reasons, guardrail and target-met
chips, and an inline-SVG sparkline of the observations against the target (dashed) and baseline
(dotted). **Observation history** lists every reading newest first with its change and the change
sets that landed since the previous one, each linked to its lineage. **Record value** (value and an
optional note) is `idea metric record`; **Measure** appears for command and url sources and is
`idea metric measure`. Above the objectives, **Works, but fails its objective** lists verified
requirements whose objective is drifting; below them, the outcome chain's gaps.

**Quality** (`#/i/<id>/quality`) — the full form of the Quality card. Per enabled dimension: its
mode (`gate`/`advisory`), status (pass, warn, fail, not measured, stale), every check with its
method (`deterministic`/`model`), detail and citations (`path:line`, or a requirement linked to its
lineage), the requirements the result is evidence for, and a strip of its past measurements from
`GET …/quality/history`. Checks that had nothing to judge fold under "*n* not applicable checks".
Dimensions that are off are listed in one line. **Measure** / **Re-measure** starts the same
operation as the card.

**Review convergence** (Define for the PRD review, Plan for the plan review) — one timeline per
review loop from `GET …/review-telemetry`; a human revise starts a new loop. Each cycle shows its
outcome (converged, revised, stalled, hit max cycles, blocked), the open blocking count and how it
moved since the previous cycle, findings raised / new / re-raised / resolved by severity, findings
demoted to advisory by rule, and gating deterministic findings. Hidden until a review has run.

**Graph diagram** (top of Graph health) — the chain objective → requirement → task → change set →
quality evidence as five linked boxes with counts (and how many are on track, verified, done or
applied); each opens the page holding that layer.

## Settings

Settings writes the same `.idea-harness/harness.yaml` the CLI edits. The shell's **Demo mode**
banner (see Layout) is the only one here. Demo mode means one thing and not the two this used to
describe: the configured `default_model` does not resolve to a registered provider, so requests
fall through to the built-in one (`internal/daemon/providers.go`, `isDemo`).

Environment variables are not consulted. A key exported with no `default_model` set is still demo
mode; a `default_model` naming a provider you have no key for is **not** demo mode — it is a run
that will fail with a 401, which is why `idea doctor` reports that case separately.

### Model

Pick a **Provider** from the configured list (each labelled "key missing", "refused" or "not
configured" when it is not ready); the model list for that provider loads live, starting with the
default model's provider or, with none, the first ready one. What the load found — the count, or why
that provider cannot be called — is shown in this card, not at the bottom of the page. Filter it,
click a chip, or type any `provider/model` into **Selected model**, then **Save model** — which sets
`default_model` and confirms "Saved — will apply to the next initiative." **Reload models**
re-fetches the list. Leaving it blank auto-selects.

### Providers

Each provider's readiness — **ready**, **missing key**, **refused**, or **not configured** — with
its type, base URL and the environment variable its key is read from. In demo mode a ready provider
the default model does not use reads **ready · not selected**: a working local Ollama does not take
the project out of demo mode until a model is selected.

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

- **Kind** — `auto` (what an unset kind means), `internal` (the placeholder), `container`, `omp`,
  or `hermes`. The selection is what `harness.yaml` asks for; under `auto` the card also says what
  it resolves to right now — "Requested: auto · resolves to: internal." — with the daemon's
  `NO REAL CODE WILL BE WRITTEN` warning when it fell back to the placeholder (`GET …/executors`'
  `resolution`).
- **Image** — the container image carrying the agent CLI.
- **Model** — the coding model as `provider/model`; empty falls back to the default.

`internal`, or `auto` resolving to it, shows "placeholder executor — writes no real code. Select
hermes/omp for real code." The image and key status lines appear only for a kind that has an image
or a key to report.
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

### Integrations

Read-only health for Slack and Linear: whether each is configured and running, which token
environment variables are set (never their values), problems such as an empty Slack allowlist or
dropped deliveries, delivery counts, the Socket Mode connection and the Linear poll. A configured
integration has a **Send test message** (Slack) or **Test connection** (Linear) button, the same
check as `idea integrations test`. Configuration stays in `harness.yaml` and `.env` so tokens never
pass through the browser; see [`integrations.md`](integrations.md).

### Notifications

Web Push for the project being viewed, per browser. **Turn on notifications on this device** asks
the browser for permission — only then, never on load — subscribes with the daemon's VAPID key and
registers the subscription (`POST …/push/subscriptions`). Each subscribed browser is listed with the
push service it uses (host only), a checkbox per event — *needs your judgment*, *change set ready
for review*, *verification failed*, *outcome drifting* — **Send a test**, and **Turn off** (this
project only; other projects keep theirs). A notification opens the page it is about. Delivery,
retries and what the payload carries: [`configuration.md`](configuration.md#push-notifications).
Unavailable, with the reason, on a page that is not a secure context (HTTPS or `localhost`) or a
browser without push — on an iPhone, add the app to the Home Screen first.

### Remote access

The browsers signed in from other devices, each with **Revoke** (or **Sign out** on this one), and
**Sign out everywhere**. Signing a device in is deliberately not here: `idea ui pair` on the
daemon's machine mints the one-time code. See [`configuration.md`](configuration.md#remote-access).

## Remote use and phones

The same app serves a phone. Turned on and secured as in
[`configuration.md`](configuration.md#remote-access) (`tailscale serve` is the recommended path), a
browser on another device gets the page **without the API token** and a **Sign in** screen asking
for a pairing code from `idea ui pair`; after that it is the full UI, on a session that expires and
can be revoked. If the session ends, the next request returns it to Sign in.

At phone widths (≤ 760px) the sidebar is a top bar, and change-set review is built for the thumb:

- **diffs** wrap long lines by default (**Wrap long lines** turns it off, and then each file scrolls
  sideways on its own, never the page); a change set of more than three files opens with only the
  first two expanded, each file header showing `+added −removed`, with **Expand/Collapse all
  files**;
- the **decision bar** — Approve, Request changes…, Reject, and the note form — sticks to the bottom
  of the screen while the diff scrolls. The note can go back as **Send back with this note** or
  **Reject with this note**;
- questions and decisions are answered in place, as on the desk.

**Installable.** The daemon serves a web app manifest and icons, so a browser on a secure context
offers "Install" / "Add to Home Screen". A service worker (`web/public/sw.js`) provides an offline
page when the daemon cannot be reached and caches the content-hashed `/assets/` files. It never
caches the app's HTML (on loopback it carries the token) or anything under `/api/`, and does not even
intercept those requests: no product data is ever stored on the device.

## Tests

- **Component tests** — `web/src/components.test.tsx` renders the components in jsdom (opted in per
  file with `// @vitest-environment jsdom`) against a stubbed daemon (`web/src/testing/fakeDaemon.ts`)
  and asserts what they send and show: the note inputs, the change-set chip, NotFound, Settings'
  executor display. `pnpm test` runs them with the rest of the unit suite.
- **Browser suite** — `make e2e` (`web/e2e`, Playwright, Chromium) builds the binary, starts a
  hermetic demo daemon (port 19590 or `IDEA_E2E_PORT`, state under `web/e2e/.work`, every provider
  key unset), seeds an initiative, and drives idea → PRD → answer → plan and a no-op revision → run
  with a note → diff → request changes with a note → approve → apply → verify → activity and health,
  plus not-found routes and Settings. Every page gets an axe (WCAG 2 A/AA) check that fails on a
  serious or critical violation; accepted exceptions, each with its reason, live in
  `web/e2e/a11y.ts` (none today). `IDEA_E2E_A11Y=report` logs violations instead of failing, and
  `IDEA_E2E_SHOTS=<dir>` keeps a screenshot of each checked page.
- **Remote use** — `web/e2e/remote.e2e.ts` adds a second project and checks the switcher (canonical
  `#/p/` addresses, remembered choice, deep links, an unknown project); then a phone on "another
  device" — Chromium resolves `remote.test` to the loopback daemon, which `demo-daemon.sh` allows —
  is served no token, signs in with a code from `idea ui pair`, switches project in the top bar,
  answers a question, reviews a diff and approves and applies from the sticky bar at 390px, and signs
  out from Settings, with an axe check on each page. `remoteUse.test.ts` and
  `remoteUse.components.test.tsx` cover the same logic and components in vitest.
- **CLI/UI parity** — [`ui-parity.md`](ui-parity.md) maps every CLI verb to its UI surface or says
  why it is CLI-only; `TestUIParityLedger` fails when a verb is added without a row.
