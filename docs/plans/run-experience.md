# Run experience: smoke button, live agent output, resumable runs

Three items deferred from the UI round (`docs/plans/deferred.md`, "UI e2e /
C3", "UI e2e / B3", "UI e2e / review"). Each is user-visible; S and L are
additive, C changes what a retry does.

## S — "Run smoke test" in Settings

Today the only way to prove an agent setup works is
`idea executors setup --smoke` from a terminal whose environment may differ
from the daemon's.

- **S1. `POST /api/v1/projects/{projectId}/executors/smoke`** → `202
  {"operation_id"}`. It runs `executor.SmokeAgent` as an operation
  (`launchOperation`, type `executor_smoke`, no initiative) with exactly what
  a real run would use:
  - kind and image from the config (empty image → `DefaultAgentImage`);
  - model from `workflow.ImplementerModelSpec`;
  - provider from `workflow.ImplementerProvider`;
  - key lookup from the daemon env, then `.env`.

  Refusals, before launching:
  - `400` for a non-agent kind (`internal`, `container`) or an unresolvable
    provider;
  - `409` while a smoke for this project is already running.

  The operation fails with the smoke's own error, which carries the agent's
  output and is redacted via `operationErrorMessage`. It is bounded at 15 min
  and cancelled on daemon stop like any operation. The smoke's container is
  removed by its deferred Cancel.
- **S2. The last result survives a reload**: `GET …/executors` `status` gains
  `last_smoke: {status, finished_at, error}` from the newest `executor_smoke`
  operation for the project, if any (`omitempty`).
- **S3. UI.** Settings → Executor gets a "Run smoke test" button:
  - Enabled when an agent kind is saved and `status.problem` is empty;
    otherwise disabled, with the reason.
  - Says before running that it makes one small real model call.
  - Shows running with elapsed time.
  - Shows a result line: pass, or the failure with the agent output folded.
- **Acceptance.**
  - A daemon test with a fake docker whose smoke writes a file → the
    operation succeeds, and `last_smoke` shows it.
  - A fake agent that writes nothing → the operation fails, and its error
    contains the agent output.
  - `400` for `internal`.
  - `409` for a second concurrent smoke.
  - A mutation check on each guard.

## L — live agent output in the running card

- **L1.** The engine records the live attempt of a running Execute:
  initiative → (`OutputTailer`, attempt id, redactor snapshot). It is kept
  under its own mutex, **not** `e.mu`, which Execute holds for the whole
  run, and cleared when Execute returns.
- **L2. `GET /api/v1/projects/{projectId}/initiatives/{id}/agent-output`** →
  `{"running": bool, "output": string}`.
  - `output` is the last 100 lines via `OutputTail`: sanitized, capped at
    8 KB and redacted with the snapshot redactor.
  - Cached for 1.5 s per initiative, so N viewers polling cost at most one
    `docker logs` per 1.5 s.
  - No live attempt → `{"running": false, "output": ""}`.
- **L3. UI.** While an execute operation runs, RunningCard polls every 2 s
  and shows a collapsible "Agent output". It is a fixed-height `<pre>` that
  auto-scrolls unless the user has scrolled up. It is rendered as text, never
  HTML.
- **Acceptance.**
  - An engine test with a fake agent that prints lines: the output is
    visible while it runs and empty after.
  - A key in the output is redacted.
  - The endpoint does not block while Execute holds `e.mu`: call it
    mid-run, under a timeout.
  - A vitest for the scroll-stick logic.

## C — a retry continues a stopped run

Each attempt starts from a fresh copy of the workspace (Track A), so
stopping a run threw away everything the agent had written, and "Run again"
began from nothing.

- **C1. Capture.** When Execute ends after the agent started but without a
  change set (cancelled, failed, timed out, daemon stopping), it:
  1. diffs the work root against its before snapshot;
  2. if anything changed and the credential sweep passes, persists the
     changed content to the content store;
  3. writes `work/.partial-<initiative>.json`:
     `{task_id, files: [FileChange with BaseDigest], created_at}`.

  Changed content that fails the credential sweep is not kept, and the
  partial says so. Capture is best effort and never replaces Execute's own
  error.
- **C2. Resume.** The next Execute of the same initiative:
  1. seeds and snapshots as now;
  2. if a partial exists **for the same task**, writes each carried file
     into the work root — only where the file's current base still matches
     the carried `BaseDigest` (absent for adds);
  3. skips the rest, because the workspace changed there;
  4. deletes the partial.

  Carried files are written after the before snapshot, so they are part of
  the change set's diff. The change set gets an observation finding
  (`carried_over`) listing what was carried and what was skipped and why. A
  partial for a different task is discarded.
- **C3. Visible and discardable.** Guidance gains `partial_work:
  {task_id, task_human_id, files}` (`omitempty`), and a new endpoint
  `DELETE /api/v1/projects/{projectId}/initiatives/{id}/partial-work` discards
  it. After Stop, the running card says: "Stopped. The agent had written N
  files; Run again continues from them." It offers "Run again" and "Start
  over", which discards and then runs.
- **Acceptance.**
  - A fake agent writes `a.txt`, then the run is cancelled; Run again → the
    change set contains `a.txt` plus the new work, and the finding lists
    `a.txt`.
  - The workspace edits the carried file's path between the runs → it is
    skipped and listed as skipped.
  - A different next task → the partial is discarded.
  - A partial containing a credential → it is not carried.
  - `DELETE` discards.
  - Mutation checks on the base match, the task match and the sweep.

## Docs

`docs/openapi.yaml` and `docs/api.md` cover the two new routes and the new
fields. `docs/agents.md` covers the smoke button and resumable runs.
`CHANGELOG.md [Unreleased]` gets Added (endpoints, fields) and Changed ("Run
again after Stop continues from the files the agent wrote"). The three
deferred.md rows are marked done.

## Order

Backend first (S1–S2, L1–L2, C1–C3), then the web work (S3, L3, C3 UI).

## Review changes (adopted)

1. **S1:** `SmokeAgent` takes an explicit work root. The daemon passes
   `<projDir>/smoke-<opID>`, a path Docker can see; `os.MkdirTemp("")` is not
   one under snap Docker or Colima. The CLI keeps the temp directory.
2. **C2 adds:** a carried add is written only if the path is absent in the
   fresh seed. The check reuses `fsdiff.StaleBase`'s add rule; the plan's
   earlier "absent for adds" wording was wrong.
3. **C vs. plan changes:** reopening a phase or revising the plan discards
   the initiative's partial, because the task it belongs to may have
   changed.
4. **C1 ordering:** capture is an explicit step after a confirmed container
   removal (`exec.Cancel` returned). It is not a sibling defer.
5. **S1 concurrency:** an atomic per-project claim (a map under a mutex)
   taken before `CreateOperation`, released when the operation ends.
6. **C3 UI:** no second Run button on the running card. Build's own Run
   button reads "Continue TASK-n (N files from the stopped run)", and
   "Start over" sits beside it. Resuming is the default for the API and CLI,
   and documented as such.
7. **L1:** the live entry is marked finished when `Collect` succeeds, so the
   endpoint reports `running: false` during the deterministic checks.
8. **Partial file:** written by temp file and rename. A corrupt or
   unreadable file is treated as absent, never as an error. Capture, carry
   and discard are logged, and carry is also recorded as the `carried_over`
   finding.
9. **Output length:** `OutputTailer.OutputTail` takes a line count.
   Failure messages keep 40 lines; the live view uses 100.
10. **C1 sweep:** the same gate `persistDiff` uses,
    `hasCredential || hasBlockingRiskSignal`.
11. **Capture context:** capture runs on `context.WithoutCancel(ctx)`.
12. **S2 store:** a new store method returns the newest operation of a type
    for a project.
13. **Partial file path:** it lives in the work directory's parent,
    `<root>/work/.partial-<initiative>.json`, outside the per-initiative
    directory that seeding removes.

Added acceptance tests:
- a partial written, then the Engine rebuilt (crash), then resume;
- concurrent smoke POSTs, exactly one accepted;
- `agent-output` reports `running: false` after Collect, during checks.
