# HTTP API Reference

The daemon's REST + SSE API is the only way into the product graph. The CLI is a thin HTTP client
over it (`internal/cli/client.go`, Decision D1) and the embedded web UI calls the same routes from
the browser — every `idea` command and every UI panel is one or more of the requests below.

The route table lives in [`internal/daemon/routes.go`](../internal/daemon/routes.go); this document
is written from it and from the handlers it names. It is **not** generated, so it can drift — check
`routes.go` when something here does not match.

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
which classifies the error rather than trusting the handler's guess. Three cases, in order:

| Case | Status | Body |
|---|---|---|
| **Store miss** — `*store.NotFoundError` (or a bare `sql.ErrNoRows` that escaped wrapping) | `404` | The store's own message, which names the kind and the reference asked for: `no requirement matching "REQ-9"`. An unwrapped miss degrades to `not found`. |
| **Storage failure** — `store.IsStorageFailure`: a `*sqlite.Error` or an `*fs.PathError` | `500` | `internal error (correlation id: a1b2c3d4e5f6)`. The full error is logged server-side under that id (`idea logs`). Driver text, constraint names and absolute state paths never reach the client. |
| **Domain error** — everything else | the status the handler passed | The error's own message, verbatim: `initiative is already running`, `no initiatives in project`. These messages were written to be read, so they are not masked. |

The status argument to `writeErr` is therefore a *default* for the third case only; a 404 wins over
a handler that passed `500`. Handlers that validate their own request bodies write `400` directly
(`verdict is required`, `root_path is required`, `type and id query params are required`).

A `409` comes from `POST /api/v1/operations/{id}/cancel` when the operation is not cancellable.

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
`GET /api/v1/projects/{projectId}/events` (SSE) — which is what `idea watch` and the UI do. The
`202` endpoints are marked in the tables below. `POST /initiatives` returns `initiative_id`
alongside `operation_id`.

**Events.** `/events` is `text/event-stream`, one event per product-graph event, with the event
sequence as the SSE id; `Last-Event-ID` resumes from a sequence. The stream polls the store once a
second, so it is at-least-once catch-up rather than a push.

## Two things to know before you build on this

### Addressing is inconsistent

Some initiative-scoped resources take the initiative in the **path**; others sit flat under the
project and take it as a **query parameter**:

```text
GET /api/v1/projects/{projectId}/initiatives/{id}/tasks        # path
GET /api/v1/projects/{projectId}/change-sets?initiative_id=…   # query
```

| Shape | Endpoints |
|---|---|
| Path-nested under `/initiatives/{id}` | `understanding`, `next-move`, `next`, `changed`, `tasks`, `docs`, `why`, `impact`, `trace`, `diff`, plus all the lifecycle `POST`s |
| Flat under the project, `?initiative_id=` | `change-sets`, `risk`, `decisions`, `questions`, `requirements`, `assertions`, `artifacts`, `findings`, `doctor`, `suggested-answers` |

The query-parameter form resolves through `initiativeFromQuery`
(`internal/daemon/handlers.go`), and **omitting `initiative_id` is not an error**: it falls back to
the project's most recent initiative, matching what the CLI means by "the initiative you are working
on" (`resolveInitiative`, `internal/cli/advance.go`). It fell back to the oldest until `de4dcdb`,
which meant the daemon could answer about a different initiative than the CLI was asking about. In a
single-initiative project the fallback is invisible either way; in a multi-initiative project it is
still a guess. Always send it.

This is known, and deferred rather than fixed. Unifying the two shapes is a breaking change to
every client at once, and the web UI consumes both forms today — `web/src/App.tsx`,
`ProductHome.tsx` and `BuildPage.tsx` build `change-sets?initiative_id=`, `HealthPage.tsx` builds
`risk?initiative_id=`, while the same components read the path-nested `tasks` and `understanding`.
`handleListTasks` carries a comment recording that it was already migrated from the query form to
the path form; the rest of that migration has not happened. Route unification is listed as out of
scope in [`docs/plans/dx-audit-fix-plan.md`](plans/dx-audit-fix-plan.md) and needs its own plan.

### `/next` and `/next-move` are different things

Two endpoints on the same initiative, one letter apart, with unrelated outputs. Neither is a rename
of the other:

| | `GET …/initiatives/{id}/next` | `GET …/initiatives/{id}/next-move` |
|---|---|---|
| Computed by | `workflow.Engine.NextAction` (`internal/workflow/next.go`) | `projection.BuildNextMove` (`internal/projection/projection.go`) |
| Response | `{"action": "review CHG-1"}` | `{"action": "Resolve open decisions", "why": "2 decision(s) need your judgment before proceeding.", "affected": ["Product Definition"]}` |
| `action` is | a **command token** from a closed set: `advance`, `execute`, `review <CHG-n>`, `apply <CHG-n>`, `verify`, `resolve`, `done`, `wait` | an **English sentence** for a human to read |
| Inputs | initiative status **and the initiative's change sets** — it walks them for `review_required`, then `approved`, then `applied`, and names the specific change set to act on | initiative status **and the Understanding projection** — counts of open decisions and open questions. It never loads change sets and can never name one |
| Blocked/failed | collapses `running` and `failed` to the same `wait` | distinguishes them, and surfaces the initiative's `failure_reason` in `why` |
| Consumed by | `idea next` — the token is dispatched as the next CLI command | the web UI's Next Move card |

So: `/next` is **machine-executable and change-set aware**; `/next-move` is **display copy and
change-set blind**. They can disagree legitimately — a `plan_approved` initiative with an approved
change set returns `apply CHG-1` from `/next` and "Execution plan approved" from `/next-move`. Do
not treat one as a richer rendering of the other.

## Endpoints

71 routes. `202` marks an endpoint that starts asynchronous work and returns an `operation_id`.

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

### Initiative lifecycle

| Method | Path | Purpose |
|---|---|---|
| POST | `/projects/{projectId}/initiatives` | Create an initiative from `title`/`description` (optional inline `docs[]`) and start the M1 pipeline. **202**, returns `initiative_id` + `operation_id`. |
| GET | `/projects/{projectId}/initiatives` | List the project's initiatives, oldest first. |
| GET | `/projects/{projectId}/initiatives/{id}` | One initiative: status, title, failure reason. |
| POST | `/projects/{projectId}/initiatives/{id}/resume` | Resume a paused/interrupted initiative; returns the new status. |
| POST | `/projects/{projectId}/initiatives/{id}/approve` | Human approval of the reviewed PRD. |
| POST | `/projects/{projectId}/initiatives/{id}/revise` | Apply resolved decisions/answers and regenerate the PRD. **202** |
| POST | `/projects/{projectId}/initiatives/{id}/advance` | Run the M2 Design → Execution Plan pipeline. **202** |
| POST | `/projects/{projectId}/initiatives/{id}/approve-plan` | Human approval gate on the execution plan (re-runs its review). **202** |
| POST | `/projects/{projectId}/initiatives/{id}/revise-plan` | Regenerate the execution plan and its review, keeping PRD and design. **202** |
| POST | `/projects/{projectId}/initiatives/{id}/execute` | Run the next ready task through the executor into a change set. **202** |
| POST | `/projects/{projectId}/initiatives/{id}/verify` | Run the verification gate; with `{"requirement","pass","note"}` records human evidence first. Returns status + coverage. |

### Understanding, guidance and analysis

| Method | Path | Purpose |
|---|---|---|
| GET | `/projects/{projectId}/initiatives/{id}/understanding` | The Current Understanding projection: requirements, decisions, questions, findings. |
| GET | `/projects/{projectId}/initiatives/{id}/next-move` | Human-readable next move + rationale + affected areas (see above). |
| GET | `/projects/{projectId}/initiatives/{id}/next` | Machine-executable next command token (see above). |
| GET | `/projects/{projectId}/initiatives/{id}/changed` | What Changed since the project's `ui` read cursor, grouped and narrated. |
| POST | `/projects/{projectId}/seen` | Advance the `ui` read cursor to `{"through": <seq>}` echoed from `/changed`. Project-scoped despite pairing with an initiative view. |
| GET | `/projects/{projectId}/initiatives/{id}/why?type=&id=` | Provenance chain behind one graph node. `type` and `id` are required (`400` otherwise). |
| GET | `/projects/{projectId}/initiatives/{id}/impact?type=&id=` | Dependents and staleness for one graph node. |
| GET | `/projects/{projectId}/initiatives/{id}/trace?type=&id=` | Both directions at once: `{"why": …, "impact": …}`. |
| GET | `/projects/{projectId}/initiatives/{id}/diff` | Structured requirement/decision diff across artifact versions. |
| GET | `/projects/{projectId}/doctor?initiative_id=` | Product-graph traceability audit: untraced nodes, dependency cycles. |
| GET | `/projects/{projectId}/risk?initiative_id=` | Deterministic risk profile computed from the initiative's patches; persisted as a side effect. |

### Plan tasks

| Method | Path | Purpose |
|---|---|---|
| GET | `/projects/{projectId}/initiatives/{id}/tasks` | Execution-plan tasks in dependency order. |
| POST | `/projects/{projectId}/initiatives/{id}/tasks` | Add a task (`title`, `statement`, `priority`, `estimate`, `implements`, `depends_on[]`). |
| PATCH | `/projects/{projectId}/initiatives/{id}/tasks/{taskId}` | Edit a task's title, statement, priority or estimate. |
| DELETE | `/projects/{projectId}/initiatives/{id}/tasks/{taskId}` | Delete a task. |
| POST | `/projects/{projectId}/initiatives/{id}/tasks/{taskId}/cancel` | Cancel a running task. |
| POST | `/projects/{projectId}/initiatives/{id}/tasks/reorder` | Reorder the plan from `{"task_ids": [...]}`. |

### Context docs

| Method | Path | Purpose |
|---|---|---|
| POST | `/projects/{projectId}/initiatives/{id}/docs` | Ingest a context doc. Two shapes: JSON `{content, kind, title}` (paste), or `multipart/form-data` with `file` (md/txt/pdf/docx/xlsx/jpg/png). Credential-swept, redacted, content-addressed. |
| GET | `/projects/{projectId}/initiatives/{id}/docs` | List the initiative's ingested docs. |
| GET | `/projects/{projectId}/initiatives/{id}/docs/{docId}` | **Not JSON.** Downloads the stored content as an attachment: raw bytes for images, redacted text otherwise. |

### Decisions and questions

| Method | Path | Purpose |
|---|---|---|
| GET | `/projects/{projectId}/decisions?initiative_id=` | List decisions with status and recommendation. |
| POST | `/projects/{projectId}/decisions/{id}/resolve` | Record the human's `{"resolution": …}`. |
| POST | `/projects/{projectId}/decisions/{id}/reopen` | Undo a resolution, returning the decision to open. |
| GET | `/projects/{projectId}/decisions/{id}/discussion` | The decision's discussion thread. |
| POST | `/projects/{projectId}/decisions/{id}/discuss` | One human→AI discussion turn: `{"message": …}`. |
| GET | `/projects/{projectId}/questions?initiative_id=` | List open questions. |
| POST | `/projects/{projectId}/questions/{id}/answer` | Record `{"answer": …}`. |
| POST | `/projects/{projectId}/initiatives/{id}/suggest-answers` | Ask the model to propose answers for the open questions. **202** |
| GET | `/projects/{projectId}/suggested-answers?initiative_id=` | The persisted AI-suggested answers. |

### Product graph reads

| Method | Path | Purpose |
|---|---|---|
| GET | `/projects/{projectId}/requirements?initiative_id=` | Requirements with acceptance criteria. |
| GET | `/projects/{projectId}/assertions?initiative_id=` | Assertions with their evidence and trust level. |
| GET | `/projects/{projectId}/artifacts?initiative_id=` | Artifact versions (PRD, design, plan, …). |
| GET | `/projects/{projectId}/artifacts/{id}/body` | One artifact **version**'s markdown body, sanitized, as JSON. |
| GET | `/projects/{projectId}/artifacts/{id}/raw` | **Not JSON.** The same body as `text/markdown` for download/export. |

### Findings

| Method | Path | Purpose |
|---|---|---|
| GET | `/projects/{projectId}/findings?initiative_id=` | Review findings for the initiative. |
| POST | `/projects/{projectId}/findings/{id}/disposition` | Set `{"disposition": …}` (accepted, not applicable, …). |
| POST | `/projects/{projectId}/findings/{id}/note` | Attach a free-text human `{"note": …}`. |

### Change sets

| Method | Path | Purpose |
|---|---|---|
| GET | `/projects/{projectId}/change-sets?initiative_id=` | List change sets with status. |
| GET | `/projects/{projectId}/change-sets/{id}` | One change set, by human id (`CHG-1`) or internal id. |
| GET | `/projects/{projectId}/change-sets/{id}/checks` | Deterministic checks projected from the change set's findings: `patch_applies`, `build`, `tests`. |
| GET | `/projects/{projectId}/change-sets/{id}/diff` | The rendered patch as `{"patch": "…"}` (content redacted at write time). |
| POST | `/projects/{projectId}/change-sets/{id}/review` | The human verdict: `{"verdict": "approve"\|"reject"\|"changes_requested"}`. |
| POST | `/projects/{projectId}/change-sets/{id}/apply` | Apply an approved change set to the workspace. **202** |
| POST | `/projects/{projectId}/change-sets/{id}/revert` | Roll it back byte-for-byte. **202** |
| POST | `/projects/{projectId}/change-sets/{id}/resolve-conflict` | Resolve a conflicted change set: `{"resolution": "retry"}` (**202**) or `"reject"` (synchronous). |

### Operations

| Method | Path | Purpose |
|---|---|---|
| GET | `/operations/{id}` | Status of one async operation. Not project-scoped: the daemon searches every registered project for the id. |
| POST | `/operations/{id}/cancel` | Interrupt a running operation. `409` when it is not cancellable. |

### Configuration

| Method | Path | Purpose |
|---|---|---|
| GET | `/projects/{projectId}/providers` | Configured providers plus the unconfigured presets, each with type, base URL, key-env **name**, and a `ready` flag saying whether that key is present. Key values are never returned. |
| PUT | `/projects/{projectId}/providers` | Merge `{default_model, providers, models, git_commit}` into `harness.yaml` and reload the runtime. |
| GET | `/projects/{projectId}/models?provider=` | List a provider's models live, falling back to the bundled catalog. Defaults to the default model's provider. |
| GET | `/projects/{projectId}/executors` | Available executors, their declared capabilities, and which one is configured. |
| PUT | `/projects/{projectId}/executors` | Field-level merge of `{kind, image, model}` into `harness.yaml`, validated, then reload. |

### Activity and events

| Method | Path | Purpose |
|---|---|---|
| GET | `/projects/{projectId}/activity` | The raw event timeline (most recent 200), unlike `/changed`'s narrated grouping. |
| GET | `/projects/{projectId}/events` | **Not JSON.** SSE stream of product-graph events; `Last-Event-ID` resumes; accepts `?token=`. |

## Related

- [`docs/cli.md`](cli.md) — the CLI surface built on these routes.
- [`docs/architecture.md`](architecture.md) — where the daemon sits in the system.
- [`docs/security.md`](security.md) — the token, loopback and redaction invariants behind the auth
  model above.
