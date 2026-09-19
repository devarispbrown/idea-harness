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

The daemon binds loopback only and **refuses to start otherwise**: `Run` rejects any non-loopback
listener address (`internal/daemon/server.go`), because the API token is served to the web UI
unauthenticated inside `index.html`, so a LAN-reachable daemon would hand the token to anyone who
loaded the page. The address is overridable via `IDEA_HARNESS_ADDR` (still loopback-only); the CLI
reads the same variable and defaults to `127.0.0.1:8787`.

There is no content negotiation and no API versioning beyond the `/api/v1` prefix. Responses are
JSON except for the three endpoints marked **Not JSON** in the tables below
(`/artifacts/{id}/raw`, `/docs/{docId}`, `/events`). Everything outside `/api/` is the embedded web
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
- **SSE exception**: `GET /api/v1/projects/{projectId}/events` additionally accepts `?token=`,
  because `EventSource` cannot set headers. This is the **only** path where the token may appear in
  a URL; every other endpoint requires the header, so the token never reaches logs, shell history or
  a `Referer`.
- A missing or wrong token is `401 {"error": "unauthorized"}`.

### Host and Origin guard

Before authentication, `hostOriginGuard` (same file) rejects requests that a browser on another
origin could have caused — the DNS-rebinding and CSRF surface a loopback daemon otherwise has:

| Condition | Response |
|---|---|
| `Host` is not exactly `127.0.0.1`, `localhost` or `::1` (port ignored) | `403 {"error": "invalid host"}` |
| `Origin` is present and is not a loopback origin, or is the literal `null` | `403 {"error": "invalid origin"}` |
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
`GET /api/v1/projects/{projectId}/events` (SSE). Both shipped clients poll: `idea watch` re-fetches
the initiative's `understanding` once a second (`internal/cli/watch.go`) and the UI polls on a
`setInterval` timer (`web/src/App.tsx`). Nothing in-tree consumes the SSE endpoint, so it is offered for your clients,
not exercised by ours. The `202` endpoints are marked in the tables below. `POST /initiatives` returns `initiative_id`
alongside `operation_id`.

**Events.** `/events` is `text/event-stream`, one event per product-graph event, with the event
sequence as the SSE id; `Last-Event-ID` resumes from a sequence. The stream polls the store once a
second, so it is at-least-once catch-up rather than a push.

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
| GET | `/logs?n=<1-2000>` | Tail of the daemon log (global, not project-scoped; default 100 lines). |

### Projects

| Method | Path | Purpose |
|---|---|---|
| POST | `/projects` | Register a project by `root_path`; returns `project_id`. |
| GET | `/projects` | List registered projects with their root paths. |
| GET | `/projects/{projectId}` | One project's registry record. |
| GET | `/projects/{projectId}/attention` | What needs a human across every initiative in the project. Returns `{"items": [...]}` — the only *collection* endpoint with an envelope, because it is a cross-initiative rollup and so has somewhere to put a cursor later. (`/models` also wraps its list, but in a response whose other field names the provider consulted.) |

### Initiative lifecycle

| Method | Path | Purpose |
|---|---|---|
| POST | `/projects/{projectId}/initiatives` | Create an initiative from `title`/`description` (optional inline `docs[]`) and start the M1 pipeline. **202**, returns `initiative_id` + `operation_id`. |
| GET | `/projects/{projectId}/initiatives` | List the project's initiatives, oldest first. |
| GET | `/projects/{projectId}/initiatives/{id}` | One initiative: status, title, failure reason. |
| POST | `/projects/{projectId}/initiatives/{id}/retry` | Re-run the phase that failed under `{"model": …}`, which is **required** — a missing or blank `model` is `400 model is required`. **202**. `409` when the initiative is running. |
| POST | `/projects/{projectId}/initiatives/{id}/reopen-phase` | Reopen a completed phase named by `{"phase": …}` so it can be revised. **202**. `409` when the initiative is running. |
| POST | `/projects/{projectId}/initiatives/{id}/resume` | Resume a paused/interrupted initiative; returns the new status. |
| POST | `/projects/{projectId}/initiatives/{id}/approve` | Human approval of the reviewed PRD. |
| POST | `/projects/{projectId}/initiatives/{id}/revise` | Apply resolved decisions/answers and regenerate the PRD. **202** |
| POST | `/projects/{projectId}/initiatives/{id}/advance` | Run the M2 Design → Execution Plan pipeline. **202** |
| POST | `/projects/{projectId}/initiatives/{id}/approve-plan` | Human approval gate on the execution plan — accepts it, findings and all. **202** |
| POST | `/projects/{projectId}/initiatives/{id}/revise-plan` | Regenerate the execution plan and its review, keeping PRD and design. **202** |
| POST | `/projects/{projectId}/initiatives/{id}/execute` | Run the next ready task through the executor into a change set. Continues from a stopped run's kept files for the same task, if any. **202** |
| GET | `/projects/{projectId}/initiatives/{id}/agent-output` | The running coding agent's last 100 output lines (redacted), and whether it is still running. |
| DELETE | `/projects/{projectId}/initiatives/{id}/partial-work` | Discard a stopped run's kept files, so the next run starts from the workspace ("Start over"). |
| POST | `/projects/{projectId}/initiatives/{id}/verify` | Run the verification gate. With `requirement` plus one of the booleans `pass`, `fail` or `waived` (and an optional `note`), records that human judgment against every acceptance criterion of the requirement first. More than one set is not an error here: the handler takes the first of `fail`, `waived`, `pass` — `idea verify` is what refuses two at once, so a direct API caller should send one. `fail` keeps the requirement in the active set and blocks completion; `waived` takes it out of scope but keeps the judgment on the record. Returns the initiative status and per-requirement coverage. A `requirement` sent with no disposition is validated and **404**s if it does not resolve, but the gate and the coverage stay initiative-wide. **409** when the initiative has failed — the gate does not run over a recorded failure; `idea retry` re-runs it. Recording human evidence is *not* refused there, because `retry` supersedes every requirement and evidence not recorded now can never be recorded. |

### Understanding, guidance and analysis

| Method | Path | Purpose |
|---|---|---|
| GET | `/projects/{projectId}/initiatives/{id}/understanding` | The Current Understanding projection: requirements, decisions, questions, findings. |
| GET | `/projects/{projectId}/initiatives/{id}/guidance` | Human-readable next move + rationale + affected areas (see above). When a decision it depends on was reopened, adds a `stale_items` list and a sentence in `why` naming the count and the decision, plus the re-plan/review path (review A4 #5). |
| GET | `/projects/{projectId}/initiatives/{id}/next` | Machine-executable next command token (see above). |
| GET | `/projects/{projectId}/initiatives/{id}/impact-counts?nodes=` | Dependent counts per graph node, for badging the UI without a query per node. `nodes` is **required**: a comma-separated list of `type:id` pairs, where `id` is the graph node id the read endpoints return, not the human code — `decision:<id>,requirement:<id>`. Missing, malformed, or more than 200 entries is `400`. |
| POST | `/projects/{projectId}/initiatives/{id}/stale/{staleId}/ack` | Acknowledge one stale item, clearing it (review A4 #5). Re-planning clears every stale item for the initiative at once instead. |
| GET | `/projects/{projectId}/initiatives/{id}/operations` | The initiative's async operations, newest first. |
| GET | `/projects/{projectId}/initiatives/{id}/changed` | What Changed since the project's `ui` read cursor, grouped and narrated. |
| POST | `/projects/{projectId}/seen` | Advance the `ui` read cursor to `{"through": <seq>}` echoed from `/changed`. Project-scoped despite pairing with an initiative view. |
| GET | `/projects/{projectId}/initiatives/{id}/why?type=&id=` | Provenance chain behind one graph node. `type` and `id` are required (`400` otherwise). |
| GET | `/projects/{projectId}/initiatives/{id}/impact?type=&id=` | Dependents and staleness for one graph node. A task's change sets are query-linked (not edge-linked) and are included as synthetic dependents; `staleness` is `"stale"` when the node carries an active review-A4 stale mark, else `"obsolete"`/`"revalidate"` by edge type. |
| GET | `/projects/{projectId}/initiatives/{id}/trace?type=&id=` | Both directions at once: `{"why": …, "impact": …}`. |
| GET | `/projects/{projectId}/initiatives/{id}/diff` | Structured requirement/decision diff: requirements as added vs. removed, decisions as open/decided/superseded, read from the graph rather than by diffing rendered markdown. Not a source diff — that is `/change-sets/{id}/diff`. |
| GET | `/projects/{projectId}/initiatives/{id}/doctor` | Product-graph traceability audit: untraced nodes, dependency cycles. |
| GET | `/projects/{projectId}/initiatives/{id}/risk` | Deterministic risk profile computed from the initiative's patches. A read: it writes nothing. |

### Plan tasks

| Method | Path | Purpose |
|---|---|---|
| GET | `/projects/{projectId}/initiatives/{id}/tasks` | Execution-plan tasks in plan order (`position`, then creation). |
| POST | `/projects/{projectId}/initiatives/{id}/tasks` | Add a task (`title`, `statement`, `priority`, `estimate`, `implements`, `requirement_ids[]`, `depends_on[]`). A task implements one or more requirements, N:M (review A4 #3/#4): `implements` and `requirement_ids` are merged and tolerantly resolved by code or id; at least one is required. |
| PATCH | `/projects/{projectId}/initiatives/{id}/tasks/{taskId}` | Edit a task's title, statement, priority, estimate, or (via `requirement_ids[]`, which replaces the links entirely and requires at least one) its requirements. |
| DELETE | `/projects/{projectId}/initiatives/{id}/tasks/{taskId}` | Delete a task. |
| POST | `/projects/{projectId}/initiatives/{id}/tasks/{taskId}/cancel` | Cancel a running task. |
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
| POST | `/projects/{projectId}/decisions/{id}/resolve` | Record the human's `{"resolution": …}`. |
| POST | `/projects/{projectId}/decisions/{id}/reopen` | Undo a resolution, returning the decision to open. Also marks stale (review A4 #5) the requirements that cite it, their tasks, and the applied change sets for those tasks — see `stale_items` on `/guidance` and `staleness` on `/impact`. |
| GET | `/projects/{projectId}/decisions/{id}/discussion` | The decision's discussion thread. |
| POST | `/projects/{projectId}/decisions/{id}/discuss` | One human→AI discussion turn: `{"message": …}`. |
| GET | `/projects/{projectId}/initiatives/{id}/questions` | List open questions. |
| POST | `/projects/{projectId}/questions/{id}/answer` | Record `{"answer": …}`. |
| POST | `/projects/{projectId}/initiatives/{id}/suggest-answers` | Ask the model to propose answers for the open questions. **202** |
| GET | `/projects/{projectId}/initiatives/{id}/suggested-answers` | The persisted AI-suggested answers. |

### Product graph reads

| Method | Path | Purpose |
|---|---|---|
| GET | `/projects/{projectId}/initiatives/{id}/requirements` | Requirements with acceptance criteria. |
| GET | `/projects/{projectId}/initiatives/{id}/assertions` | Assertions with their evidence and trust level. |
| GET | `/projects/{projectId}/initiatives/{id}/artifacts` | Artifact versions (PRD, design, plan, …). |
| GET | `/projects/{projectId}/artifacts/diff` | Unified line diff between two versions of one artifact, given `?from=` and `?to=` version ids. Versions from different artifacts are a `400`; bodies over 1 MiB or 20,000 lines are a `413`. |
| GET | `/projects/{projectId}/artifacts/{id}/comments` | Human comments on one artifact version. |
| POST | `/projects/{projectId}/artifacts/{id}/comments` | Attach a comment to an artifact version, optionally anchored to a quoted span. |
| POST | `/projects/{projectId}/comments/{id}/promote` | Promote a comment into the product graph as an open **question** — the only node kind promote produces. Returns that question. |
| GET | `/projects/{projectId}/artifacts/{id}/body` | One artifact **version**'s markdown body, sanitized, as JSON. |
| GET | `/projects/{projectId}/artifacts/{id}/raw` | **Not JSON.** The same artifact version's body as `text/markdown` for download/export — served **unsanitized**, unlike `/body`. |

### Findings

| Method | Path | Purpose |
|---|---|---|
| GET | `/projects/{projectId}/initiatives/{id}/findings` | Review findings for the initiative. |
| POST | `/projects/{projectId}/initiatives/{id}/requirements/{reqId}/verification-task` | Add a plan task whose purpose is verifying that requirement, wired with a `verifies` edge. |
| POST | `/projects/{projectId}/findings/{id}/disposition` | Set `{"disposition": …}` — one of `fixed`, `accepted_risk`, `not_applicable`, `duplicate`, `deferred`, `rejected`, `mitigate`, `not_a_secret`. `not_a_secret` is valid only on a `secret_sweep` finding and also writes a secret-allowlist entry; anything else invalid is a 400. |
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
| POST | `/projects/{projectId}/change-sets/{id}/review` | The human verdict: `{"verdict": "approved"\|"rejected"\|"changes_requested", "accept_findings": bool}`. The wire vocabulary is not the CLI's — `idea review CHG-1 approve` sends `approved`; a client that posts `approve` or `reject` gets a 400. `accept_findings` (`--accept-findings`) signs off the reviewer's own blocking findings, without which an approval over one is refused; a finding carrying a deny code is refused either way. The body is closed: an undeclared field is a 400. |
| POST | `/projects/{projectId}/change-sets/{id}/apply` | Apply an approved change set to the workspace. **202** |
| POST | `/projects/{projectId}/change-sets/{id}/revert` | Roll it back byte-for-byte. **202** |
| POST | `/projects/{projectId}/change-sets/{id}/resolve-conflict` | Resolve a conflicted change set: `{"resolution": "retry"}` (**202**) or `"reject"` (synchronous). |

### Operations

| Method | Path | Purpose |
|---|---|---|
| GET | `/operations/{id}` | Status of one async operation. Not project-scoped: the daemon searches every registered project for the id. |
| POST | `/operations/{id}/cancel` | Interrupt a running operation. `404` when no operation matches the id; `409` when it exists and is not cancellable. |

### Configuration

| Method | Path | Purpose |
|---|---|---|
| GET | `/projects/{projectId}/providers` | Configured providers plus the unconfigured presets, each with type, base URL, key-env **name**, and a `ready` flag saying whether that key is present. Key values are never returned. |
| PUT | `/projects/{projectId}/providers` | Merge `{default_model, providers, models, git_commit}` into `harness.yaml` and reload the runtime. Answers `{status, warnings?}`; `warnings` carries non-blocking problems attributable to a field this request wrote — a model id absent from the bundled catalog, say — under the same attribution rule as the `400`. |
| GET | `/projects/{projectId}/models?provider=` | List a provider's models live, falling back to the bundled catalog. Defaults to the default model's provider. |
| GET | `/projects/{projectId}/executors` | Available executors, their declared capabilities, and which one is configured. |
| PUT | `/projects/{projectId}/executors` | Field-level merge of `{kind, image, model}` into `harness.yaml`, validated, then reload. |
| POST | `/projects/{projectId}/executors/smoke` | Run the coding agent's smoke test with the daemon's own config and keys; one small real model call. **202** |

### Activity and events

| Method | Path | Purpose |
|---|---|---|
| GET | `/projects/{projectId}/activity` | The raw event timeline (oldest 200 by sequence), unlike `/changed`'s narrated grouping. |
| GET | `/projects/{projectId}/events` | **Not JSON.** SSE stream of product-graph events; `Last-Event-ID` resumes; accepts `?token=`. |

## Related

- [`docs/cli.md`](cli.md) — the CLI surface built on these routes.
- [`docs/architecture.md`](architecture.md) — where the daemon sits in the system.
- [`docs/security.md`](security.md) — the token, loopback and redaction invariants behind the auth
  model above.
