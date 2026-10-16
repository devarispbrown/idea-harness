# HTTP API Reference

The daemon's REST + SSE API is the only way into the product graph. The CLI is a thin HTTP client
over it (`internal/cli/client.go`, Decision D1) and the embedded web UI calls the same routes from
the browser — every `idea` command and every UI panel is one or more of the requests below.

The route table lives in [`internal/daemon/routes.go`](../internal/daemon/routes.go); this document
is written from it and from the handlers it names. It explains the API; the machine-readable
description is [`openapi.yaml`](openapi.yaml), and `make api-check` fails the build if a route
exists that the spec does not document, or the reverse.

A running daemon serves both, without a token, outside `/api/`:

```text
http://127.0.0.1:8787/api-reference    # the reference, rendered
http://127.0.0.1:8787/openapi.yaml     # the spec itself (also .json)
```

There is no SDK, and none is planned: the supported integration surfaces are this API and
`idea <command> --json`. If a client library is ever wanted, the spec above is what generates it.

The reference can also be hosted away from the binary — `APIDOCS=0 make build` drops it and its
3.7 MiB, and `make api-docs-dist` produces a static bundle for Cloudflare, AWS or Fastly. See
[`api-reference-hosting.md`](api-reference-hosting.md).

Pre-1.0 the API may change in any release; breaking changes are recorded in
[`CHANGELOG.md`](../CHANGELOG.md).

## Base URL

```text
http://127.0.0.1:8787/api/v1
```

The main listener is loopback only and the daemon **refuses to start otherwise**: `Run` rejects a
non-loopback `IDEA_HARNESS_ADDR` (`internal/daemon/server.go`), because the page served there
carries the API token. The CLI reads the same variable and defaults to `127.0.0.1:8787`.

Remote use — a phone, another laptop — is opt-in and separate: an extra listener
(`daemon.listen`) or a TLS-terminating proxy's hostname (`daemon.allowed_hosts`, e.g. under
`tailscale serve`). A browser that is not provably on this machine never receives the token; it
signs in with a one-time pairing code and gets a session cookie instead. The policy, the cookie
and its CSRF defence are specified once, in
[`security-model.md`](security-model.md#remote-access); setup is in
[`configuration.md`](configuration.md#remote-access).

There is no content negotiation and no API versioning beyond the `/api/v1` prefix. Responses are
JSON except for the endpoints marked **Not JSON** in the tables below
(`/artifacts/{id}/raw`, `/docs/{docId}`, `/events`, `/runs/{runId}/events/stream`, and
`/runs/{runId}/report` unless `format=json`). Everything outside `/api/` is the embedded web
UI, not the API.

## Authentication

Every request under `/api/` carries the per-install API token
(`internal/daemon/server.go`, `auth`):

```bash
curl -H "X-Idea-Token: $(cat ~/.local/share/idea-harness/token)" \
     http://127.0.0.1:8787/api/v1/projects
```

- **Header**: `X-Idea-Token: <token>`, or `Authorization: Bearer <token>`. Compared with a
  constant-time equality check (`security.TokenEqual`).
- **Token source**: `~/.local/share/idea-harness/token`, mode `0600`, generated per install
  (`IDEA_HARNESS_STATE` relocates the state root). The daemon also substitutes it into the UI's
  `index.html` at serve time so the same-origin UI can authenticate its own `fetch` calls.
- **Unauthenticated exceptions**: `GET /api/v1/health` (so `idea doctor` and CI can probe a daemon
  they have no token for), and everything outside `/api/` — the embedded UI's own assets.
- **SSE exception**: the two SSE streams, `GET /api/v1/projects/{projectId}/events` and
  `GET /api/v1/projects/{projectId}/runs/{runId}/events/stream`, additionally accept `?token=`,
  because `EventSource` cannot set headers. These are the **only** paths where the token may appear
  in a URL (matched by exact shape: the run's JSON page, `.../runs/{runId}/events`, is not one);
  every other endpoint requires the header, so the token never reaches logs, shell history or a
  `Referer`.
- A missing or wrong token is `401 {"error": "unauthorized"}`.
- **Remote browsers** authenticate with a session cookie instead of the token, and every
  state-changing request they make also carries `X-Idea-CSRF` and an `Origin`
  ([`security-model.md`](security-model.md#remote-access)). A request that presents a token is
  judged by the token alone — a wrong one is `401` even with a valid cookie.

### Host and Origin guard

Before authentication, `hostOriginGuard` (same file) rejects requests that a browser on another
origin could have caused — the DNS-rebinding and CSRF surface a loopback daemon otherwise has. The
names it accepts beyond loopback are opt-in remote access
([`security-model.md`](security-model.md#remote-access)):

| Condition | Response |
|---|---|
| `Host` is not exactly `127.0.0.1`, `localhost` or `::1`, a `daemon.allowed_hosts` name, or the remote listener's address (port ignored) | `403 {"error": "invalid host"}` |
| `Origin` is present and is not in that same set, or is the literal `null` | `403 {"error": "invalid origin"}` |
| `Origin` absent | allowed (non-browser clients: CLI, `curl`) |

There is no CORS: no `Access-Control-Allow-Origin` is ever sent, so a page on any other origin
cannot read a response even if it could make the request.

## Errors

Every error response is the same shape (`writeError`, `internal/daemon/server.go`):

```json
{ "error": "no change set matching \"CHG-99\"" }
```

Status selection is centralized in `writeErr` ([`internal/daemon/errors.go`](../internal/daemon/errors.go)),
which classifies the error rather than trusting the handler's guess. Four cases, in order:

| Case | Status | Body |
|---|---|---|
| **Store miss** — `*store.NotFoundError` (or a bare `sql.ErrNoRows` that escaped wrapping) | `404` | The store's own message, which names the kind and the reference asked for: `no requirement matching "REQ-9"`. An unwrapped miss degrades to `not found`. |
| **Precondition failure** — `*PreconditionError`, the daemon's own type for "the current state does not allow this" | `409` | The precondition's own message: `initiative is already running`, `advance requires an approved PRD (status=…)`. See the `409` notes below. |
| **Storage failure** — `store.IsStorageFailure`: a `*sqlite.Error` or an `*fs.PathError` | `500` | `internal error (correlation id: a1b2c3d4e5f6)`. The full error is logged server-side under that id (`idea logs`). Driver text, constraint names and absolute state paths never reach the client. |
| **Domain error** — everything else | the status the handler passed | The error's own message, verbatim: `tasks are editable only after the plan is drafted and before it is executed` (`409`), `comment "…" was already promoted to a question` (`400`). These messages were written to be read, so they are not masked. |

The status argument to `writeErr` is therefore a *default* for the fourth case only; a 404 or a 409
wins over a handler that passed `500`. Handlers that validate their own request bodies write `400` directly
(`verdict is required`, `root_path is required`, `type and id query params are required`).

A `409` means the current state does not allow what was asked. It is not one endpoint but a class,
described by cause rather than enumerated, because a hand-written list of call sites had already
gone stale once:

- **An asynchronous operation cannot start from this state.** `apply` on a change set that is not
  approved, `revert` on one that is not applied, `execute` or `advance` without an approved plan or
  PRD, and anything at all while the initiative is running. These are checked synchronously and on
  purpose: the operation is asynchronous, so launching it and failing in the background would answer
  `202` and report success for work that never happened.
- **A cancellation that cannot apply.** `POST /operations/{id}/cancel` when the operation exists
  and has already finished. An id matching no operation is a `404`, like every other id-addressed
  route — it read as `409 is not running` until the next release, because the in-memory cancellation handle
  was consulted before the row was looked up.
- **A mutation blocked by something that depends on it**, such as deleting a task another task
  depends on.
- **`revert` on a workspace that moved since apply** (review A3, message sharpened by review M4).
  Revert compares every file the change set touched against the *post*-image apply wrote — the
  mirror of apply's own pre-image check (`fsdiff.StaleBase`) at the opposite end of the change
  set's life. If a touched file no longer matches (edited, deleted, or an "added" file now
  missing), revert refuses before writing anything: `revert refused: the workspace changed since
  CHG-3 was applied: src/app.js: it was edited in the workspace after apply; discard (or stash)
  your edits to src/app.js, then revert again`. The advice is **discard (or stash)**, never
  "commit" — committing does not change a file's bytes, so it cannot make the comparison pass. When
  a *later* change set is what actually touched the file, the advice names it instead —
  `revert CHG-4 first` — rather than telling the human to discard what is really CHG-4's own
  change. There is no force option — the change set stays `applied`, and the recovery is to act on
  the advice, then revert again. This is checked synchronously (before the revert operation
  starts), the same way an already-reverted change set is: launching the operation and failing in
  the background would answer `202` for a revert that erased nothing but also changed nothing. The
  refusal also records a `stale_post` finding on the change set at an observation (non-blocking)
  severity — it does not gate `complete`.
- **Configuration written but not applied**, when the daemon could not reload the project. The
  config file *was* written; the running daemon kept the previous one.

Preconditions carry their own error type inside the daemon, so a handler cannot accidentally report
one as a `500` — which `advance` and `revise` both did while four sibling endpoints returned `409`
for the identical condition.

## Conventions

**Project scoping.** Almost every route is nested under `/projects/{projectId}`. A project id comes
from `POST /api/v1/projects` (which registers a `root_path`) or from `GET /api/v1/projects`; the CLI
resolves the current working directory against that list on every command.

**Asynchronous work.** Anything that runs the model pipeline returns `202 Accepted` with an
operation id and returns immediately:

```json
{ "operation_id": "op_01H…" }
```

Poll `GET /api/v1/operations/{id}` for its status, or follow
`GET /api/v1/projects/{projectId}/events` (SSE). `idea watch` polls: it re-fetches the
initiative's `understanding` once a second (`internal/cli/watch.go`). The UI follows the SSE stream
(`web/src/useEventStream.ts`): a product-graph event triggers its refetch, with a slow timer as a
backstop, and it falls back to polling when the stream cannot be opened. The `202` endpoints are
marked in the tables below. `POST /initiatives` returns `initiative_id` alongside `operation_id`.

**Events.** `/events` is `text/event-stream`: one unnamed message per product-graph event, and one
`event: run_event` message per [run event](#run-events). The SSE id is
`<event seq>.<run event id>`; `Last-Event-ID` (or `?after=`) resumes both cursors, so a reconnect
gets only what it missed. A bare `<event seq>` resumes the product-graph events and starts run
events at now; `?after=now` skips the backlog entirely. Product-graph events are polled once a
second; run events are pushed as they are stored. An idle stream sends a `: ping` comment every
15 s.

## Run events

A coding agent's run is narrated as a stream of normalized **run events**, whichever executor ran
it. Each is `{seq, run_id, operation_id, initiative_id, task_id, executor, at, kind, source, title,
detail, path, tool_call_id, tool_kind, status, added, removed, input_tokens, output_tokens,
cost_usd, change_set_id, truncated}` (`internal/domain/runevent.go`), where `kind` is one of:

| Kind | What it is |
|---|---|
| `action` | A coarse step: a line of a CLI agent's output, an executor phase, an ACP plan. |
| `tool_call` / `tool_result` | A tool call and its outcome, tied by `tool_call_id`; `status` is `pending`, `in_progress`, `completed` or `failed`; a call still open when the run ends (Stop, a failure, an agent that never reported its end) is closed by a harness `tool_result` with `status: cancelled`. |
| `file_change` | A file the agent wrote. Live ones come from the agent; the `harness` ones with `status` `add`/`modify`/`delete` and `added`/`removed` line counts are the orchestrator's own diff, written once the agent finishes - the authoritative list. |
| `permission` | An ACP permission request and the answer given on the operator's behalf. |
| `message` | The agent's words, or a steering note (`source: user`). |
| `thought` | The agent is reasoning. Never carries the text (§50). |
| `cost` | A token/cost **delta** the agent or server reported. |
| `status` | Lifecycle: starting, stages (checks, review), `agent_finished`, `stopped`, `finished` (with `change_set_id`). |
| `error` | A failure the agent or executor reported. |

Every event is redacted ([security-model.md](security-model.md)) before it is stored and bounded (`title` 240 characters, `detail` 4096
bytes). `seq` is monotonic per run and is the resume cursor. Storage is capped: a run keeps its
newest 5000 events (`seq` is never reused, so a cursor stays valid; `first_seq` above 1 says older
ones were dropped), and only the 50 most recent runs keep any.

**Reading a run.** Ask `GET .../initiatives/{id}/current-run` for the run to show (the live one, or
the last), page `GET .../runs/{runId}/events?after=` until `more` is false, then stream
`GET .../runs/{runId}/events/stream?after=<last seq>`. The stream resumes from `Last-Event-ID` or
`?after=` - only the events after it, never the session again - and ends with `event: end` once the
run is over, so `EventSource` stops reconnecting.

**Steering.** `POST .../runs/{runId}/steer {"note", "interrupt"}` sends a note to the running
agent: `acp` queues it as the next prompt turn (or, with `interrupt`, cancels the current turn and
sends it at once), `openhands` sends it into the conversation, and every other executor answers
`steering not supported by <kind>` and holds it for the task's next run (`delivery: next_run`). See
[agents.md](agents.md#steering-a-running-agent). Stop is `POST /api/v1/operations/{id}/cancel`.

## Two things to know before you build on this

### One addressing convention

Every initiative-scoped resource takes the initiative in the **path**:

```text
GET /api/v1/projects/{projectId}/initiatives/{id}/tasks
GET /api/v1/projects/{projectId}/initiatives/{id}/change-sets
```

This was not always true. Ten reads — `change-sets`, `risk`, `decisions`, `questions`,
`requirements`, `assertions`, `artifacts`, `findings`, `doctor`, `suggested-answers` — used to sit
flat under the project and take `?initiative_id=`, so the same question had two shapes depending on
which resource you asked about. Worse, omitting the parameter was not an error: the daemon picked an
initiative for you, which in a project with more than one was a silent wrong answer.

Those flat routes are **gone**, not deprecated. A request to one returns 404. The initiative is now
a required path segment, so there is nothing left to guess and no way to ask an ambiguous question.
A stale `?initiative_id=` appended to a nested URL is ignored.

Resources addressed by their own id keep their shape — `change-sets/{id}/apply`,
`artifacts/{id}/raw`, `decisions/{id}/resolve` and friends are unambiguous already — and so do the
genuinely project-scoped routes (`providers`, `models`, `executors`, `seen`).

### `/next` and `/guidance` are different things

Two endpoints on the same initiative, one letter apart, with unrelated outputs. Neither is a rename
of the other:

| | `GET …/initiatives/{id}/next` | `GET …/initiatives/{id}/guidance` |
|---|---|---|
| Computed by | `workflow.Engine.NextAction` (`internal/workflow/next.go`) | `projection.BuildGuidance` (`internal/projection/projection.go`), plus `next_task`/`run_blocked_reason` composed by the handler itself |
| Response | `{"action": "review CHG-1"}` | `{"action": "Resolve open decisions", "why": "2 decision(s) need your judgment before proceeding.", "affected": ["Product Definition"], "next_task": {"id": "...", "human_id": "TASK-2", "title": "Add /health endpoint"}, "run_blocked_reason": "CHG-1 is still review required; apply or reject it before running the next task"}` |
| `action`/`why`/`affected` is | a **command token** from a closed set: `advance`, `execute`, `review <CHG-n>`, `apply <CHG-n>`, `verify`, `resolve <reason>`, `done`, `failed`, `wait` | an **English sentence** for a human to read |
| Inputs | initiative status **and the initiative's change sets** — it walks them for `review_required`, then `approved`, then `applied`, and names the specific change set to act on | `action`/`why`/`affected` come from initiative status and the Understanding projection alone (counts of open decisions/questions) and are still change-set blind. `next_task`/`run_blocked_reason` are the Build contract's exception: they DO look at tasks, change sets and the executor's readiness (image/key), because the Build page's "Run next task" button needs to know before the click, not after a 409 |
| Blocked/failed | distinguishes them: `wait` for running, `failed` for failed, with the initiative's `failure_reason` alongside the token | distinguishes them, and surfaces the initiative's `failure_reason` in `why` |
| Consumed by | `idea next` — the token is dispatched as the next CLI command | the web UI's Next Move card and Build page |

So: `/next` is **machine-executable and change-set aware**; `/guidance`'s `action`/`why`/`affected`
are **display copy and change-set blind**, while its `next_task`/`run_blocked_reason` are a second,
narrower contract layered on top for the Build page specifically. `action`/`why`/`affected` can
still disagree with `/next` legitimately — a `plan_approved` initiative with an approved change set
returns `apply CHG-1` from `/next` and "Execution plan approved" from `/guidance`. Do not treat one
as a richer rendering of the other.

## Endpoints

`202` marks an endpoint that starts asynchronous work and returns an `operation_id`.
`make api-docs-check` fails if a registered route has no row here, so this list cannot fall
behind the daemon again -- it went ten endpoints stale under a hand-counted total.

### Daemon

| Method | Path | Purpose |
|---|---|---|
| GET | `/health` | Liveness plus version. **The only unauthenticated API route.** |
| GET | `/version` | The build stamp: `version`, `commit`, `modified` (dirty tree). |
| GET | `/logs?n=<1-2000>` | Tail of the daemon log (global, not project-scoped; default 100 lines). Every line is credential-redacted on the way out, on top of the redaction the log handler applies on the way in — see [`security-model.md`](security-model.md#what-this-does-not-protect-against). |

### Projects

| Method | Path | Purpose |
|---|---|---|
| POST | `/projects` | Register a project by `root_path`; returns `project_id`. |
| GET | `/projects` | List registered projects with their root paths. |
| GET | `/projects/{projectId}` | One project's registry record. |
| GET | `/projects/{projectId}/attention` | What needs a human across every initiative in the project. Returns `{"items": [...]}` — the only *collection* endpoint with an envelope, because it is a cross-initiative rollup and so has somewhere to put a cursor later. (`/models` also wraps its list, but in a response whose other field names the provider consulted.) |
| GET | `/projects/{projectId}/queue` | (D-b) The cross-initiative "needs you" queue: open blocking decisions, change sets awaiting review, conflicts, denials, stale items, and verifications ready to confirm — one row per item, ranked blocking first and then by age. `rank` is a small 1..N ordinal (1 = most urgent), never the internal sort key. A denial is only the latest change set of its task and only a real system deny (never a plain human reject or check failure); "ready to confirm" only offers an `approved` requirement; a `complete` or `failed` initiative contributes nothing. |

### Initiative lifecycle

| Method | Path | Purpose |
|---|---|---|
| POST | `/projects/{projectId}/initiatives` | Create an initiative from `title`/`description` and start the M1 pipeline. Accepts JSON (`title`/`description`, optional inline `docs[]` of text) or multipart (`title`/`description` + repeated `file` parts, sniffed as text or image). **202**, returns `initiative_id` + `operation_id`. |
| GET | `/projects/{projectId}/initiatives` | List the project's initiatives, oldest first. |
| GET | `/projects/{projectId}/initiatives/{id}` | One initiative: status, title, failure reason. |
| POST | `/projects/{projectId}/initiatives/{id}/retry` | Re-run the phase that failed under `{"model": …}`. `model` is optional and decoded leniently (like `fresh`/`note` on `/execute`): absent or blank falls back to the initiative's configured default model, so `{"note": "…"}` alone is accepted. **202**. `409` when the initiative is running. An optional `note` (D-a) steers this retry: it joins the framing prompt that restarts the pipeline (there is no implementer brief at this stage) and is recorded on that run. |
| POST | `/projects/{projectId}/initiatives/{id}/reopen-phase` | Reopen a completed phase named by `{"phase": …}` so it can be revised. **202**. `409` when the initiative is running. |
| POST | `/projects/{projectId}/initiatives/{id}/resume` | Resume a paused/interrupted initiative; returns the new status. |
| POST | `/projects/{projectId}/initiatives/{id}/approve` | Human approval of the reviewed PRD. |
| POST | `/projects/{projectId}/initiatives/{id}/revise` | Apply resolved decisions/answers and regenerate the PRD. **202** |
| POST | `/projects/{projectId}/initiatives/{id}/advance` | Run the M2 Design → Execution Plan pipeline. **202** |
| POST | `/projects/{projectId}/initiatives/{id}/approve-plan` | Human approval gate on the execution plan — accepts it, findings and all. **202** |
| POST | `/projects/{projectId}/initiatives/{id}/revise-plan` | Regenerate the execution plan and its review, keeping PRD and design. Optional `{"note": "..."}` says what to change: it joins the planner's inputs for this revision and is recorded on its run (`idea revise-plan --note`). A regenerated plan is the next version of the same artifact; one identical to the current plan records no version, and a task that kept its title keeps its code. **202** |
| POST | `/projects/{projectId}/initiatives/{id}/execute` | Run the next ready task through the executor into a change set. Continues from a stopped run's kept files for the same task, if any. `{"fresh": true}` discards them first ("Start over"). An optional `note` (D-a) steers this run: appended to the implementer's brief and recorded on the agent run. Both are decoded leniently. **202** |
| GET | `/projects/{projectId}/initiatives/{id}/agent-output` | The running coding agent's last 100 output lines (redacted), and whether it is still running. |
| DELETE | `/projects/{projectId}/initiatives/{id}/partial-work` | Discard a stopped run's kept files, so the next run starts from the workspace ("Start over"). |
| POST | `/projects/{projectId}/initiatives/{id}/pr` | Push the initiative's dedicated branch (`git.branch_per_initiative`) and open a pull request via `gh pr create`, with a body built from the initiative (PRD summary, change sets, verification progress). Synchronous — no workspace mutation, so no background operation. Idempotent: a repeat call answers the same `pr_url` rather than opening a second PR. **409** if the initiative has no dedicated branch yet, or `gh` is not installed. |
| POST | `/projects/{projectId}/initiatives/{id}/verify` | Run the verification gate. With `requirement` plus one of the booleans `pass`, `fail` or `waived` (and an optional `note`), records that human judgment against every acceptance criterion of the requirement first. More than one set is not an error here: the handler takes the first of `fail`, `waived`, `pass` — `idea verify` is what refuses two at once, so a direct API caller should send one. `fail` keeps the requirement in the active set and blocks completion; `waived` takes it out of scope but keeps the judgment on the record. `{"confirm_passing_tests": true}` (C2/C3/H2/H4) additionally confirms every requirement that is `approved`, has acceptance criteria, and whose `evidence_tests` all passed, through the same human-evidence path, actor `human` — requires at least one applied change set; nothing is ever auto-verified without this explicit request, and one ineligible requirement never aborts the rest. The response then also carries `confirmed` (codes flipped) and `skipped` (`{requirement, reason}` for every candidate that was not). Returns the initiative status, per-requirement coverage, and `quality`: the quality-dimension scorecard the gate read, whose `gate_unmet` (gated dimensions that failed, were not measured, or are stale) also holds completion. A `requirement` sent with no disposition is validated and **404**s if it does not resolve, but the gate and the coverage stay initiative-wide. **409** when the initiative has failed — the gate does not run over a recorded failure; `idea retry` re-runs it. Recording human evidence is *not* refused there, because `retry` supersedes every requirement and evidence not recorded now can never be recorded. |
| GET | `/projects/{projectId}/initiatives/{id}/quality` | The quality-dimension scorecard (`verify.dimensions`): each dimension's mode under the current rigor/policy, its latest result with checks and file/requirement citations, whether that result is stale, `needs_measurement`, and `gate_unmet`. A read; nothing is measured. |
| POST | `/projects/{projectId}/initiatives/{id}/quality` | **202.** Starts a `measure_quality` operation that measures every enabled dimension (sandboxed setup run, OpenAPI/Go API diff, docs and ops heuristics, model-reviewed ergonomics, security re-scan and scanners, performance budgets) and records one result per dimension with `verifies` edges to the requirements it bears on. Does not claim the initiative. |
| GET | `/projects/{projectId}/initiatives/{id}/quality/history` | Every recorded quality result, oldest first, with its checks, citations and linked requirement codes — the series the scorecard is the latest of. `?dimension=` narrows it to one dimension; an unknown one is a **400** naming the known ones. A read; the web UI's Quality page charts it. |
| GET | `/projects/{projectId}/initiatives/{id}/review-telemetry` | Review convergence, one record per review cycle, oldest first, read from the adversarial reviewer's run manifests: findings `raised`/`new`/`reraised`/`resolved` per severity, `demoted` per convergence rule, `open_blocking` and `prev_open_blocking`, and the `outcome` (`converged`, `revise`, `stalled`, `max_cycles`, `blocked`). `?review=prd` or `?review=execution_plan` narrows it to one loop; anything else is a **400**. `[]` when no review ran. The web UI's review convergence timeline on Define and Plan reads it. |

### Understanding, guidance and analysis

| Method | Path | Purpose |
|---|---|---|
| GET | `/projects/{projectId}/initiatives/{id}/understanding` | The Current Understanding projection: requirements, decisions, questions, findings. |
| GET | `/projects/{projectId}/initiatives/{id}/guidance` | Human-readable next move + rationale + affected areas (see above). When a decision it depends on was reopened, adds a `stale_items` list and a sentence in `why` naming the count and the decision, plus the re-plan/review path (review A4 #5). When the current change set was rejected by the secret sweep, `why` also names the actual cause -- the file (or "in the output of `<check>`") and kind of secret, never the value, plus the not_a_secret disposition route when the match is overridable -- instead of only the generic "a check or the review found a problem" (ICP re-run defect #5). |
| GET | `/projects/{projectId}/initiatives/{id}/next` | Machine-executable next command token (see above). |
| GET | `/projects/{projectId}/initiatives/{id}/impact-counts?nodes=` | Dependent counts per graph node, for badging the UI without a query per node. `nodes` is **required**: a comma-separated list of `type:id` pairs, where `id` is the graph node id the read endpoints return, not the human code — `decision:<id>,requirement:<id>`. Missing, malformed, or more than 200 entries is `400`. |
| POST | `/projects/{projectId}/initiatives/{id}/stale/{staleId}/ack` | Acknowledge one stale item, clearing it (review A4 #5). Re-planning clears every stale item for the initiative at once instead. |
| GET | `/projects/{projectId}/initiatives/{id}/operations` | The initiative's async operations, newest first. |
| GET | `/projects/{projectId}/initiatives/{id}/changed` | What Changed since the project's `ui` read cursor, grouped and narrated. Scoped to this initiative (fixed in C1: this used to read the whole project's events, unscoped, and could report another initiative's counts). |
| POST | `/projects/{projectId}/seen` | Advance a read cursor to `{"through": <seq>}` echoed from `/changed`. `initiative_id` (M1) scopes the cursor to that initiative (`ui:<initiativeId>`), so marking one initiative seen cannot advance another's own cursor; omitted, it writes the project-wide `ui` cursor (back-compat default). |
| GET | `/projects/{projectId}/initiatives/{id}/timeline?since=` | (C1) The human-language "what changed" timeline: change sets applied/rejected/denied/reverted, decisions changed, stale marks, run failures/stops, and completed phases, newest first. `since` (optional, RFC3339) excludes entries at or before it. `idea changed` uses this, not `/changed` above. |
| GET | `/projects/{projectId}/initiatives/{id}/costs` | (C3) Spend and time per workflow phase from the run records, plus the coding-agent run count/duration/spend separately (`usd: null` with a note when agent spend could not be measured for every run). `idea status` prints the total. |
| GET | `/projects/{projectId}/initiatives/{id}/why?type=&id=` | Provenance chain behind one graph node. `type` and `id` are required (`400` otherwise). |
| GET | `/projects/{projectId}/initiatives/{id}/impact?type=&id=` | Dependents and staleness for one graph node. A task's change sets are query-linked (not edge-linked) and are included as synthetic dependents; `staleness` is `"stale"` when the node carries an active review-A4 stale mark, else `"obsolete"` when reached by `supersedes` or itself the target of an `invalidates` edge, else `"revalidate"`. An incoming `invalidates` edge is not followed (the invalidator is not a dependent); `why` lists it instead. |
| GET | `/projects/{projectId}/initiatives/{id}/trace?type=&id=` | Both directions at once: `{"why": …, "impact": …}`. |
| GET | `/projects/{projectId}/initiatives/{id}/diff` | Structured requirement/decision diff: requirements as added vs. removed, decisions as open/decided/superseded, read from the graph rather than by diffing rendered markdown. Not a source diff — that is `/change-sets/{id}/diff`. |
| GET | `/projects/{projectId}/initiatives/{id}/doctor` | Product-graph traceability audit: untraced nodes, dependency cycles. |
| GET | `/projects/{projectId}/initiatives/{id}/risk` | Deterministic risk profile computed from the initiative's patches. A read: it writes nothing. |
| GET | `/projects/{projectId}/initiatives/{id}/gaps?kind=` | Product Graph gap analysis (`idea gaps`), no model call: `{"initiative_id", "gaps": [...], "summary"}`, most severe first. Each gap has `kind`, `state` (which trigger fired), `severity`, `nodes` (`nodes[0]` is the subject), `why`, `next` (the closing command) and `dependents`. `kind` (comma-separated) filters; an unknown kind is a `400` listing the known ones. Triggers: [architecture.md](architecture.md#asking-the-graph-gaps). |
| GET | `/projects/{projectId}/gaps?kind=` | The same report over every initiative of the project (each gap carries `initiative_id`) — what `idea gaps --all` reads. |

### Plan tasks

| Method | Path | Purpose |
|---|---|---|
| GET | `/projects/{projectId}/initiatives/{id}/tasks` | Execution-plan tasks in plan order (`position`, then creation). Each carries `requirement_ids[]`/`depends_on[]` (always arrays, never `null`), computed from the task's graph edges. |
| POST | `/projects/{projectId}/initiatives/{id}/tasks` | Add a task (`title`, `statement`, `priority`, `estimate`, `implements`, `requirement_ids[]`, `depends_on[]`). A task implements one or more requirements, N:M (review A4 #3/#4): `implements` and `requirement_ids` are merged and tolerantly resolved by code or id; at least one is required. |
| PATCH | `/projects/{projectId}/initiatives/{id}/tasks/{taskId}` | Edit a task's title, statement, priority, estimate, or (via `requirement_ids[]`, which replaces the links entirely and requires at least one) its requirements. |
| DELETE | `/projects/{projectId}/initiatives/{id}/tasks/{taskId}` | Delete a task. |
| POST | `/projects/{projectId}/initiatives/{id}/tasks/{taskId}/cancel` | Cancel a running task. |
| POST | `/projects/{projectId}/initiatives/{id}/tasks/{taskId}/depends-on` | Set a task's `depends_on` wholesale from `{"depends_on": [...]}` (titles, codes, or ids). Refused if it would introduce a cycle anywhere in the graph (review B3). |
| POST | `/projects/{projectId}/initiatives/{id}/tasks/{taskId}/split` | Split one task into two or more from `{"tasks": [{"title", "statement", "requirement_ids"}, ...]}`. Each replacement defaults to the original's requirements when `requirement_ids` is omitted, inherits its `depends_on`, and anything depending on the original now depends on all the replacements. The original is deleted (review B3). |
| POST | `/projects/{projectId}/initiatives/{id}/tasks/merge` | Merge two tasks from `{"task_ids": [survivor, merged]}`: requirements union onto the survivor, statements concatenate, every dependency edge rewires onto the survivor, and the merged task is deleted (review B3). |
| POST | `/projects/{projectId}/initiatives/{id}/tasks/reorder` | Reorder the plan from `{"task_ids": [...]}`. |

### Context docs

| Method | Path | Purpose |
|---|---|---|
| POST | `/projects/{projectId}/initiatives/{id}/docs` | Ingest a context doc. Two shapes: JSON `{content, kind, title}` (paste), or `multipart/form-data` with `file`. Text formats (md/txt/csv/pdf/docx/xlsx) are credential-swept, redacted and content-addressed; image formats (jpg/png/gif/webp, classified by magic bytes) are stored verbatim and described by a vision model — see [`security.md`](security.md). |
| GET | `/projects/{projectId}/initiatives/{id}/docs` | List the initiative's ingested docs. |
| GET | `/projects/{projectId}/initiatives/{id}/docs/{docId}` | **Not JSON.** Downloads the stored content as an attachment: raw bytes for images, redacted text otherwise. |

### Decisions and questions

| Method | Path | Purpose |
|---|---|---|
| GET | `/projects/{projectId}/initiatives/{id}/decisions` | List decisions with status and recommendation. |
| POST | `/projects/{projectId}/initiatives/{id}/decisions/accept-recommendations` | Resolve every OPEN decision with its own `recommendation`, actor `human`, in one call. When a decision has `options`, the recommendation must map onto exactly one of them (verbatim, an "Option B"/letter/index reference, or a case/whitespace/punctuation-tolerant match); a decision left unresolved is named in `skipped` with a `reason` (review B4, review #2). |
| GET | `/projects/{projectId}/initiatives/{id}/triage` | The top 3 open decisions/questions/blocking findings by impact (blocking, then severity, then dependents count), labelled `priority`; the rest `optional` (review B4). |
| POST | `/projects/{projectId}/decisions/{id}/resolve` | Record the human's `{"resolution": …}`. |
| POST | `/projects/{projectId}/decisions/{id}/reopen` | Undo a resolution, returning the decision to open. Also marks stale (review A4 #5) the LIVE requirements that cite it (never one a later PRD revision superseded/removed — ICP re-run defect #4: a superseded requirement keeps its old `derived_from` edge forever, so without this filter a reopen surfaced only long-dead requirements instead of whatever live one now covers the decision's subject), their tasks, and the applied change sets for those tasks — see `stale_items` on `/guidance` and `staleness` on `/impact`. |
| GET | `/projects/{projectId}/decisions/{id}/discussion` | The decision's discussion thread. |
| POST | `/projects/{projectId}/decisions/{id}/discuss` | One human→AI discussion turn: `{"message": …}`. |
| GET | `/projects/{projectId}/initiatives/{id}/questions` | List open questions. |
| POST | `/projects/{projectId}/questions/{id}/answer` | Record `{"answer": …}`. |
| POST | `/projects/{projectId}/initiatives/{id}/suggest-answers` | Ask the model to propose answers for the open questions. **202** |
| GET | `/projects/{projectId}/initiatives/{id}/suggested-answers` | The persisted AI-suggested answers. |

### Product graph reads

| Method | Path | Purpose |
|---|---|---|
| GET | `/projects/{projectId}/initiatives/{id}/requirements` | Requirements with acceptance criteria. Each carries `evidence_tests[]` (C2): tests mapped to it by the verification plan's hooks, a code-name match, or — since a real test's name almost never contains the requirement's code — the test files touched by the applied, check-passed change set of a task that `implements` it (ICP re-run defect #3), and whether the last parsed check run had them passing. `confirm_passing_tests`'s `skipped` reason distinguishes "nothing is mapped" (names the requirement and how to map it) from "something mapped failed" ("not every mapped test passed"). |
| GET | `/projects/{projectId}/initiatives/{id}/assertions` | Assertions with their evidence and trust level. |
| POST | `/projects/{projectId}/assertions/{id}/status` | Record a human judgment on an assumption or hypothesis: `{"status": …}` with `accepted`, `disputed` or `retracted` (`idea assumption`). The only path out of `candidate`, and what closes an `unvalidated_assumption` gap. A superseded assertion is a `409`. |
| GET | `/projects/{projectId}/initiatives/{id}/artifacts` | Artifact versions (PRD, design, plan, …). |
| GET | `/projects/{projectId}/artifacts/diff` | Unified line diff between two versions of one artifact, given `?from=` and `?to=` version ids. Versions from different artifacts are a `400`; bodies over 1 MiB or 20,000 lines are a `413`. |
| GET | `/projects/{projectId}/artifacts/{id}/comments` | Human comments on one artifact version. |
| POST | `/projects/{projectId}/artifacts/{id}/comments` | Attach a comment to an artifact version, optionally anchored to a quoted span. |
| POST | `/projects/{projectId}/comments/{id}/promote` | Promote a comment into the product graph as an open **question** — the only node kind promote produces. Returns that question. |
| GET | `/projects/{projectId}/artifacts/{id}/body` | One artifact **version**'s markdown body, sanitized, as JSON. |
| GET | `/projects/{projectId}/artifacts/{id}/raw` | **Not JSON.** The same artifact version's body as `text/markdown` for download/export — served **unsanitized**, unlike `/body`. |

### Objectives and outcomes

The outcome end of the Product Graph: objectives (`OBJ-n`), the outcome metrics that measure them
(`MET-n`), and observations. `{objectiveId}` and `{metricId}` accept the code (any case) or the id.
Every write here is a human edit; the PRD stage writes its own objectives through the engine, and a
human-added objective is never rewritten by a later PRD revision.

| Method | Path | Purpose |
|---|---|---|
| GET | `/projects/{projectId}/initiatives/{id}/objectives` | Live objectives (not superseded by a PRD revision), each with `requirement_ids` and `metric_ids`. |
| POST | `/projects/{projectId}/initiatives/{id}/objectives` | Add an objective (`statement`, optional `rationale`, `owner`, `status`). **201**. |
| PATCH | `/projects/{projectId}/initiatives/{id}/objectives/{objectiveId}` | Edit the fields present; `status` is `active`, `achieved` or `abandoned`. |
| POST | `/projects/{projectId}/initiatives/{id}/objectives/{objectiveId}/requirements` | Link `{"requirement": "REQ-1"}` to the objective (`requirement --serves--> objective`), or unlink with `"unlink": true`. **409** for a superseded requirement. |
| GET | `/projects/{projectId}/initiatives/{id}/metrics` | Live metrics, each with `status` -- its deterministic drift evaluation at request time. |
| POST | `/projects/{projectId}/initiatives/{id}/metrics` | Add a metric to an objective (`objective`, `name`, `direction`, `target`; optional `baseline`, `guardrail`, `cadence`, `unit`, `definition`, `source`, `hypothesis`). **201**. |
| POST | `/projects/{projectId}/initiatives/{id}/metrics/measure` | Measure `command`/`url` sources now -- one (`{"metric": "MET-2"}`) or all -- and record what they read. Commands run in the no-network check sandbox (never the host); url sources need `outcomes.allow_url_sources`. Synchronous. |
| PATCH | `/projects/{projectId}/initiatives/{id}/metrics/{metricId}` | Edit the fields present; `clear_baseline` / `clear_guardrail` remove those values. |
| GET | `/projects/{projectId}/initiatives/{id}/metrics/{metricId}/observations` | The metric's observations, oldest first, each with the change sets attributed to it. |
| POST | `/projects/{projectId}/initiatives/{id}/metrics/{metricId}/observations` | Record a value (`value`, optional `note`, `observed_at`). Returns the observation and the metric's drift after it. Moving the metric into `off_track`/`regressed` emits the event the integration bus publishes as `outcome.drifted`. **201**. |
| GET | `/projects/{projectId}/initiatives/{id}/outcomes` | The drift report: objectives, metrics with drift, "works but fails its objective" signals, the chain's gaps, and counts. What `idea status`, `idea next`, the Health page and MCP read. |

### Findings

| Method | Path | Purpose |
|---|---|---|
| GET | `/projects/{projectId}/initiatives/{id}/findings` | Review findings for the initiative. |
| POST | `/projects/{projectId}/initiatives/{id}/requirements/{reqId}/verification-task` | Add a plan task whose purpose is verifying that requirement, wired with a `verifies` edge. |
| POST | `/projects/{projectId}/findings/{id}/disposition` | Set `{"disposition": …}` — one of `fixed`, `accepted_risk`, `not_applicable`, `duplicate`, `deferred`, `rejected`, `mitigate`, `not_a_secret`. `not_a_secret` is valid only on a `secret_sweep` finding and also writes a secret-allowlist entry; anything else invalid is a 400. `rejected` means "the human judged this finding wrong": on a finding with no system deny code (the adversarial reviewer's own judgment call) it resolves the approval bar exactly like `fixed`/`accepted_risk` (ICP re-run defect #2); it is always recorded (200) but has no effect on approval for a finding that carries a deny code (`secret_sweep`, `host_exec`, …) or was set by the system itself (e.g. a failing build/test check) — the response then also carries `"warning"` naming the deny code that still applies. |
| POST | `/projects/{projectId}/findings/{id}/note` | Attach a free-text human `{"note": …}`. |
| GET | `/projects/{projectId}/secret-allowlist` | List this project's "not a secret" overrides: fingerprint prefix, kind, path at the time, actor, created_at. |
| DELETE | `/projects/{projectId}/secret-allowlist/{id}` | Revoke one override, so the secret sweep denies that fingerprint again. |

### Change sets

| Method | Path | Purpose |
|---|---|---|
| GET | `/projects/{projectId}/initiatives/{id}/change-sets` | List change sets with status. |
| GET | `/projects/{projectId}/change-sets/{id}` | One change set, by human id (`CHG-1`) or internal id. |
| GET | `/projects/{projectId}/change-sets/{id}/checks` | Deterministic checks projected from the change set's findings: `patch_applies`, `build`, `tests`. |
| GET | `/projects/{projectId}/change-sets/{id}/diff` | The patch in two forms: `patch`, the whole rendered diff as one string, and `files`, a per-file list (`path`, `change_type`, `size`, `content`). Content is redacted **at this display surface, not at write time** — the raw blob store is deliberately redaction-free so apply and revert round-trip byte-for-byte ([`security-model.md`](security-model.md#what-this-does-not-protect-against)). |
| POST | `/projects/{projectId}/change-sets/{id}/review` | The human verdict: `{"verdict": "approved"\|"rejected"\|"changes_requested"\|"rerun", "accept_findings": bool, "note": "..."}`. `note` (`idea review --note`) goes with `changes_requested` or `rejected`: it is recorded on the change set as `review_note` and appended to the implementer's brief on the task's next run, then never again; it is a 400 with `approved` or `rerun`. The wire vocabulary is not the CLI's — `idea review CHG-1 approve` sends `approved`; a client that posts `approve` or `reject` gets a 400. `accept_findings` (`--accept-findings`) signs off the reviewer's own blocking findings, without which an approval over one is refused; a finding carrying a deny code is refused either way. `approved`/`rejected`/`changes_requested` call no model and answer synchronously, **200**, `{"status": "reviewed"}`. `rerun` (ICP re-run defect #1) is not a state-machine verdict — the change set stays `review_required` — it re-runs the adversarial reviewer for a change set whose review never completed the first time (e.g. `Execute`'s own review call was cancelled mid-review, or every attempt was cut off at the model's output-token ceiling even after a retry); `approved` refuses with a 400 naming `rerun` until it has, EXCEPT with `accept_findings: true` on a change set whose review was genuinely attempted and explicitly recorded as unable to complete (a truncation that persisted through `rerun` too) — that one case may approve without ever reaching a completed review, still requiring `accept_findings` and still refusing a plain `approved`. `rerun` **is** a real model call (observed 1m12s–1m38s) and answers **202** with `{"operation_id": "..."}`, like `apply`/`execute` below — poll `GET /operations/{id}` for completion (ICP re-run D2: running it synchronously meant the daemon always finished the review while a client's own HTTP timeout gave up waiting on the response). `accept_findings: true` is refused (400) alongside `rerun`. The body is closed: an undeclared field is a 400. |
| POST | `/projects/{projectId}/change-sets/{id}/apply` | Apply an approved (or reverted — ICP re-run defect #3) change set to the workspace. **202** |
| POST | `/projects/{projectId}/change-sets/{id}/revert` | Roll it back byte-for-byte. **202**. **409** if the change set is not `applied`, or if any file it touched no longer matches what apply wrote there (see the 409 notes above) — nothing is written either way, and there is no force option. |
| GET | `/projects/{projectId}/change-sets/{id}/pr-checks` | The latest CI status of the change set's initiative's pull request, via `gh pr checks`. Every change set of one initiative shares the same PR and so the same result. **409** if there is no PR yet (`idea pr` first) or `gh` is not installed. |
| POST | `/projects/{projectId}/change-sets/{id}/resolve-conflict` | Resolve a conflicted change set: `{"resolution": "retry"}` (**202**) or `"reject"` (synchronous). |

### Operations

| Method | Path | Purpose |
|---|---|---|
| GET | `/operations/{id}` | Status of one async operation. Not project-scoped: the daemon searches every registered project for the id. |
| POST | `/operations/{id}/cancel` | Interrupt a running operation. `404` when no operation matches the id; `409` when it exists and is not cancellable. |

### Configuration

| Method | Path | Purpose |
|---|---|---|
| GET | `/projects/{projectId}/providers` | Configured providers plus the unconfigured presets, each with type, base URL, key-env **name**, and a `ready` flag saying whether that key is present. Key values are never returned. Also returns `git_commit`, `git_allow_default_branch`, `git_branch_per_initiative` and `review_rigor` (`"auto"` when unset). |
| PUT | `/projects/{projectId}/providers` | Merge `{default_model, providers, models, git_commit, git_allow_default_branch, git_branch_per_initiative, review_rigor}` into `harness.yaml` and reload the runtime. `review_rigor` must be one of `auto\|quick\|standard\|thorough` (`400` otherwise). Answers `{status, warnings?}`; `warnings` carries non-blocking problems attributable to a field this request wrote — a model id absent from the bundled catalog, say — under the same attribution rule as the `400`. |
| GET | `/projects/{projectId}/models?provider=` | List a provider's models live, falling back to the bundled catalog. Defaults to the default model's provider. |
| GET | `/projects/{projectId}/executors` | Available executors, their declared capabilities, and which one is configured. |
| PUT | `/projects/{projectId}/executors` | Field-level merge of `{kind, image, model}` (and `acp_*` / `openhands_*` for those kinds) into `harness.yaml`, validated, then reload. An `openhands_url` that is plain http to a non-loopback host is refused. |
| POST | `/projects/{projectId}/executors/smoke` | Run the coding agent's smoke test with the daemon's own config and keys; one small real model call. **202** |
| GET | `/projects/{projectId}/integrations` | Slack and Linear health: configured/running, which token env vars are set (never values), problems, outbox counts, Socket Mode and poll state. See [integrations.md](integrations.md). |
| POST | `/projects/{projectId}/integrations/{name}/test` | `slack`: `auth.test`, a test message to the channel, and a Socket Mode token check. `linear`: key, team, state names, project and label. `200` with `ok: false` when the service answered with a failure; `400` when not configured or the token is unset. |
| PUT | `/projects/{projectId}/integrations/{name}` | `{config: {...}\|null}`: replace (or, with `null`, remove) `integrations.slack`/`.linear` in `harness.yaml`, validated like a hand edit, comments kept, applied without a restart. Token *names* only; a value that looks like a credential is refused. Closed body. |
| GET | `/projects/{projectId}/executors/config` | The executor block as `harness.yaml` holds it (`acp`, `openhands`, `mcp_servers` included), which referenced env vars are set, its config problems, and the executor/model compatibility advisory for `?kind=&model=`. |
| GET | `/projects/{projectId}/budget` | The project's default per-initiative cap (`budget.default_initiative_usd`) and every initiative's meter. |
| PUT | `/projects/{projectId}/budget` | `{default_initiative_usd}` (0 removes it): written to `harness.yaml`; initiatives stopped at their cap that the new default leaves room for are resumed (`resumed`). Closed body. |
| GET | `/projects/{projectId}/initiatives/{id}/budget` | One initiative's cap (its own, else the project default), spend counted against it, and whether the next model/agent call would be refused. `agent_not_counted` when hermes/omp ran without reporting a cost. |
| PUT | `/projects/{projectId}/initiatives/{id}/budget` | `{cap_usd}` or `{clear: true}`. When the initiative is stopped at its cap (`needs_human_judgment`/`budget_exceeded`) and the new cap leaves room, it returns to where it stopped (`resumed: true`). Closed body. |

### Automations

Scheduled background jobs, each off until enabled. See [configuration.md](configuration.md#automations).

| Method | Path | Purpose |
|---|---|---|
| GET | `/projects/{projectId}/automations` | `linear_inbound`, `metric_measurement`, `eval`: enabled, schedule, last and next run, history with status and cost. |
| PUT | `/projects/{projectId}/automations/{name}` | `{enabled}` (and, for `eval`, `model`, `max_cost_usd`, `interval_hours`, `suite`, `cases`, `baseline`), written to `harness.yaml`. Enabling the eval needs a model and a cost cap. Closed body. |
| POST | `/projects/{projectId}/automations/{name}/run` | Run now, enabled or not; `{target}` narrows metric measurement to one metric. **202** `{status, name, runs}`; the run lands in `history`. The eval spends real money up to its cap. |

### Activity and events

| Method | Path | Purpose |
|---|---|---|
| GET | `/projects/{projectId}/activity` | The raw event timeline (oldest 200 by sequence), unlike `/changed`'s narrated grouping. |
| GET | `/projects/{projectId}/events` | **Not JSON.** SSE stream of product-graph events and `run_event`s; `Last-Event-ID`/`?after=` resumes both cursors (`<seq>.<run event id>`, or `now`); accepts `?token=`. |

### Runs

| Method | Path | Purpose |
|---|---|---|
| GET | `/projects/{projectId}/initiatives/{id}/remote-runs` | The initiative's OpenHands remote runs (`executor.kind: openhands`), newest first: `status`, `attempt`, `server_url`, `conversation_id`, `task_code`, `last_event` (the progress log's last line), `event_count` and `error`. `idea runs remote`; the run panel's Remote runs. |
| GET | `/projects/{projectId}/remote-runs` | The same across every initiative, at most 200; `?active=true` keeps the ones not yet over. `idea runs remote --all`; the UI's Remote runs (`#/runs`). |
| GET | `/projects/{projectId}/initiatives/{id}/current-run` | The run the run view shows: the live agent run, else the last one with events. `live`, `executor`, `steerable`, `ui_url` ("Open in OpenHands", only when `executor.openhands.ui_url` is set) and `pending_notes`. |
| GET | `/projects/{projectId}/runs/{runId}/events` | A run's events, paged: `?after=<seq>&limit=` (at most 500). `{events, first_seq, last_seq, more, run_status}`. |
| GET | `/projects/{projectId}/runs/{runId}/events/stream` | **Not JSON.** SSE stream of the run's events (`event: run_event`, id = seq); resumes from `Last-Event-ID`/`?after=`; `event: end` when the run is over; accepts `?token=`. |
| POST | `/projects/{projectId}/runs/{runId}/steer` | `{note, interrupt?}`: send a note to the running agent. `{delivery: delivered\|queued\|next_run, message}`; `409` when the run is not running. |
| GET | `/projects/{projectId}/runs/{runId}/report` | Download the run report - events, files with line counts, findings, cost, steering. `?format=json` (default), `md`, or `zip` (both). |

### Remote access and notifications

Sign-in for browsers on another device, and Web Push. See
[`security-model.md`](security-model.md#remote-access) for the session model and
[`ui.md`](ui.md#notifications) for the notification kinds.

| Method | Path | Purpose |
|---|---|---|
| POST | `/ui/pairing-codes` | Mint a one-time pairing code (`XXXXX-XXXXX`, ten minutes, single use) — what `idea ui pair` calls. **Token only**: a session cookie is refused `403`. `{code, expires_at, remote, urls, burned_codes, last_burned_at?}` — the last two report codes burned in the last 7 days. |
| POST | `/ui/login` | `{code, label?}` → a session cookie and `{auth, csrf_token, session}`. Unauthenticated; must be JSON with an allowed `Origin`. `401` for a bad, used, expired or burned code. `429` with `Retry-After` once a client has failed 10 times in 15 minutes (that client only; never a loopback request). A code that 100 wrong codes were tried against while it was outstanding is burned; sign-in is never refused for everyone — see [security-model.md](security-model.md#remote-access). |
| POST | `/ui/logout` | Revoke the calling session and clear its cookie. `204`. |
| GET | `/ui/session` | `{auth: token\|session, csrf_token?, session?}` — how this request is authenticated; how a reloaded page gets its CSRF token back. |
| GET | `/ui/sessions` | Every browser session (newest first, revoked ones included), `current`, and `burned_codes`/`last_burned_at` (pairing codes burned in the last 7 days). Never a secret. |
| DELETE | `/ui/sessions` | Revoke every browser session — `idea ui sessions revoke --all`. `{revoked}`. |
| DELETE | `/ui/sessions/{id}` | Revoke one browser session. `204`. |
| GET | `/push/vapid-public-key` | The daemon's VAPID public key (the `applicationServerKey` for `PushManager.subscribe`) and the subscribable `event_kinds`. |
| GET | `/projects/{projectId}/push/subscriptions` | This project's push subscriptions: id, label, the endpoint's **host only**, events, last delivery and error. |
| POST | `/projects/{projectId}/push/subscriptions` | Subscribe a browser: its `PushSubscription` JSON (`endpoint` must be https) plus `events?` (default all four) and `label?`. Keyed by endpoint: `201` new, `200` updated. The response alone carries `renewal_token`, for the service worker's `…/renew`. Each becomes its own integration-bus subscriber with Slack's delivery semantics. |
| PATCH | `/projects/{projectId}/push/subscriptions/{id}` | `{events}`: which kinds notify this browser; `[]` pauses it. |
| DELETE | `/projects/{projectId}/push/subscriptions/{id}` | Unsubscribe, with its bus cursor and pending deliveries. `204`. |
| POST | `/projects/{projectId}/push/subscriptions/{id}/test` | Send one notification now. `{ok, error?, gone?}`; `gone` means the push service reported it dead and it was removed. |
| POST | `/projects/{projectId}/push/subscriptions/{id}/renew` | **Unauthenticated; the service worker's.** `{renewal_token, endpoint, keys}` re-points this subscription after the push service replaced it (`pushsubscriptionchange`) and returns it with the next `renewal_token`. The token comes from the subscribe response and is spent by each renewal; every refusal is the same `401`. See [security-model.md](security-model.md#remote-access). |

## Related

- [`docs/cli.md`](cli.md) — the CLI surface built on these routes.
- [`docs/architecture.md`](architecture.md) — where the daemon sits in the system.
- [`docs/security.md`](security.md) — the token, loopback and redaction invariants behind the auth
  model above.
