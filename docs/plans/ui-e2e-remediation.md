# UI end-to-end remediation plan

Source: UX walkthrough of the web UI on 2026-09-17 (demo journey on the
original binary, one real Hermes run against `openai/gpt-5.6-luna`). Verdict
was 3/10: the UI cannot finish the loop without the CLI, and one defect
destroys user code. Findings are numbered as in that report (F1–F13).

Order is by harm, not by size. Track A ships first and alone because it
changes what the agent sees and what apply writes.

## Track A — the agent works on a copy of the repository (F3, blocker)

**Defect.** The work root `<state>/projects/<id>/work/<initiative>` starts
empty. The agent never sees the user's files, so it writes `server.js` from
scratch; the diff calls it ADDED; apply overwrites the real file and deletes
the user's existing route. Review and checks passed it. This is the most
severe finding in the report: silent loss of user code.

**Fix.**

- A1. **Seed each attempt's work root from the workspace**, fresh per attempt
  (remove and recreate `work/<initiative>` before the "before" snapshot).
  Fresh-per-attempt also removes the stale baseline the third review found
  (an earlier attempt's leftovers counted as the new base; a cancelled run's
  files silently dropped on retry).
  - What is copied: in a git repository, `git ls-files -co --exclude-standard`
    (tracked plus untracked-not-ignored). Outside git, a walk that skips
    dot-directories, `node_modules`, `vendor`, `dist`, `build`, `target`.
  - Never copied, even if tracked: `.env*`, `.idea-harness/`, `.git/`, and any
    file the engine's credential check flags (`Engine.detectCredential`: the
    redactor's known values and `security.FindCredential` patterns), because
    the work root is mounted read-write into a container with network egress.
  - Git listing runs as `git -C <workspace> ls-files -co --exclude-standard -z`
    (scoped to the project root, not the repo top level). A listed path whose
    file no longer exists is skipped. Submodule gitlinks are skipped.
  - **Every skipped path is surfaced**, not only logged: the change set gets an
    observation finding listing skipped paths by reason (secret, env file,
    symlink, oversized, submodule), so a build that fails for a missing file
    shows why.
  - Bounds: skip files over 1 MiB and stop at 200 MiB total with a clear
    error naming the limit and the largest directories. Symlinks are not
    followed (copied as nothing; recorded).
- A2. **Record each modified or deleted file's base digest** in the patch
  (`FileChange.BaseDigest`, omitempty).
- A3. **Apply refuses a stale base**, keyed on `ChangeType`, never on
  `BaseDigest`'s emptiness (an all-adds patch has no base digests and must
  still be checked):
  - `ChangeType == ""`: patch predates this change; apply unchecked (legacy).
  - `added`: the destination must NOT exist in the workspace now.
  - `modified` / `deleted`: the workspace file must exist and match
    `BaseDigest`; missing or different is a conflict.
  On any mismatch, mark the change set `conflicted` naming the files; never
  write. For this cause the conflict card foregrounds "Reject and re-run" —
  retrying the same patch fails identically.
- A4. Free once A1 lands: `fsdiff.Diff` already classifies a path present in
  the before snapshot as modified. No extra logic.
- A5. **One in-flight change set per initiative.** Seeding from the workspace
  is only correct if the workspace already holds every earlier task's work.
  `Execute` refuses, naming it, while another change set of the initiative is
  in `checks_running`, `review_required`, `approved`, `applying` or
  `changes_requested` (other than the task's own prior attempt, which is
  superseded as today). Build's "Run next task" is disabled with that reason.
- A6. Seeding is synchronous under the engine mutex, which already serialises
  execution; bounded by the limits above.

**Acceptance.**
- Workspace has `server.js` with route `/`; fake agent appends `/health`.
  The change set lists `server.js` as modified; apply leaves both routes.
- `.env` in the workspace with `OPENAI_API_KEY=sk-…` is absent from the work
  root the container sees.
- Edit `server.js` in the workspace after execute, then apply: change set is
  conflicted, file untouched.
- A second attempt does not see the first attempt's files.
- An `added` file whose path exists in the workspace at apply time is a
  conflict, never an overwrite.
- A legacy patch (no `ChangeType`) applies unchecked; a new all-adds patch is
  still checked.
- Task 1's change set in `review_required`: executing task 2 is refused with
  a message naming CHG-1.
- A committed file that merely looks like a key is skipped AND the skip is
  listed on the change set.
- Retrying a stale-base conflict conflicts again (documents the trap).
- Mutation-check each: no seeding, `.env` copied, base check skipped, add
  check skipped, in-flight guard removed.

## Track B — Build can be started, watched and diagnosed in the UI (F1, F2 part, F9)

- B1. **"Run next task" primary action on Build**: shown when a ready task
  exists and nothing is running; label names the task
  (`Run TASK-2: Add /health endpoint`); calls `POST …/execute`; disabled with
  the reason when the executor is not ready (see C3).
- B2. **Failed execution is visible.** Record the operation's error on the
  operation (new `error` field, redacted) and surface it through the existing
  failed banner on Build. Today the status stays `plan_approved`, the running
  card vanishes and the reason is only in `daemon.log`.
- B3. **Running card names the task** and shows elapsed time; after Stop, a
  "Stopped — run again?" state instead of vanishing.
- B4. Header "placeholder code" only for the `internal` executor.
- B5. Remove the stale "Implementation and ChangeSets arrive in Milestone 3"
  string (`internal/projection/projection.go:323`).
- B6. **Diff view renders the unified patch** (`patch`, not `content`), with
  added/modified/deleted labels from A4; a whole-file rewrite of an existing
  file is flagged.

**Acceptance.** Playwright: from `plan_approved`, click Run → running card
with task name → change set appears with a unified diff. With a failing fake
agent, the banner shows the agent's error. Vitest for B4/B6 label logic.

## Track C — executor settings that work (F2)

- C1. **Server-side default image**: `PUT /executors` with kind hermes/omp
  and no image stores the pinned default (`executor.DefaultAgentImage`).
  Reload shows what is actually stored. **And the runtime fallback**: an empty
  image in an existing or hand-edited `harness.yaml` resolves to
  `DefaultAgentImage(kind)`, not `alpine:3.20` (agent.go:49), which has no
  agent CLI and was the actual cause of exit 127.
- C2. **Validate on save**: the model must resolve through
  `executor.ResolveAgentProvider` (unknown provider, bad name/type → 400 with
  the reason).
- C3. **Executor status in Settings**: image present locally (yes/no, with the
  `idea executors setup` command to pull it), key present in the daemon's env
  or `.env`. A "Run smoke test" button is **deferred** (it needs a daemon-side
  smoke operation; recorded in deferred.md).
- Acceptance: daemon tests for C1/C2; Playwright reload shows stored image.

## Track D — judgments on the right page, once (F4)

- D1. Scope judgment cards by phase: PRD decisions and PRD-review findings on
  Define; plan-review findings on Plan (where approval happens).
- D2. Branch on `judgment_reason`; "Revise PRD to v2" only in `prd_review`.
  After approval: "Optional — unanswered items use the recommendation".
- D3. Deduplicate decisions that ask the same question (same normalized
  question text within an initiative) in the projection: show the newest
  unresolved one; if all are resolved, the newest resolved one; never hide an
  unresolved decision behind a resolved one or vice versa. Display-only; the
  store is unchanged.
- Acceptance: projection unit tests for D1/D3; vitest for D2.

## Track E — approve, apply, revert give feedback (F5)

- E1. Approve runs the adversarial review detached from the request context
  (`context.WithoutCancel` + timeout) so closing the tab does not cancel it.
- E2. Every Build action shows a busy state, disables its buttons, and
  catches errors into an inline message.
- Acceptance: daemon test that a cancelled request context still completes
  the approval; vitest for the busy/error state.

## Track F — keys and errors in user language (F6)

- F1. **Pre-flight key check** before any provider call: a keyed provider
  whose key is unset fails with `OPENAI_API_KEY is not set in the daemon's
  environment or the project's .env; set it and run idea daemon stop`.
- F2. `api.ts` unwraps `{"error": "…"}` so UI errors are the message only.
- F3. Retry resets its busy state when the retried operation settles.
- F4. Settings warns on save when the chosen provider is not ready; the
  sidebar provider dot turns amber, not green.
- Acceptance: workflow test for F1; vitest for F2/F3/F4 logic.

## Track G — demo mode is obvious and looks intentional (F7)

- G1. Global demo banner in the app shell (not only in Settings), linking to
  Settings → Providers.
- G2. Fake provider strips the schema suffix from the user idea
  (`internal/model/fake.go:141`).
- G3. "Shipped (demo)" / "Placeholder change" labels when the executor is
  `internal` or the actual model is `demo`.
- G4. First-run empty home explains the six phases and the one next action.

## Track H — daemon down is honest (F8)

- H1. Any failed poll sets `daemonDown`; last good data is kept (never
  replaced with `[]`); message uses `location.host`.
- H2. `idea dev` detects a running daemon and prints its URL instead of
  `bind: address already in use`; `idea init` and `idea daemon status` print
  the UI URL.

## Track I — model picker (F10)

- I1. Load models for the default provider on mount.
- I2. Curated catalog models first; filter non-chat ids (tts, embedding,
  veo, lyria, aqa, image, transcribe, live).
- I3. A provider that is not configured names its key variable and the
  restart step.

## Track J — mobile (F11)

- Top navigation always rendered; plan row actions collapse into a menu
  under 600 px; provider rows wrap. Playwright screenshots at 390×844.

## Track K — navigation and status consistency (F12)

- Next-phase handoff link after a phase completes; rail states derived from
  one source; Verify count agrees with the page; unknown initiative/doc deep
  links show a not-found state; home header shows the project name; "+ New
  idea" with an empty input explains; Suggest shows loading; plan task Delete
  confirms, and edit/delete are hidden once the plan is approved.

## Track L — polish and docs (F13)

- Dark-mode hard-coded colours to tokens; "Activity ↗" target; Explore copy
  claims only what is swept; `docs/agents.md` .env contradiction (:55 vs
  :156 — the daemon does pass `.env` keys since 979e0d0; fix :156); `idea
  daemon stop` message names the PID instead of suggesting `pkill -f`.

## Deferred (recorded in deferred.md)

- Daemon-side smoke-test operation and button (C3 remainder).
- Live agent log streaming in the running card (B3 remainder); the task name
  and elapsed time ship now.

## Parallelisation

Track A first (backend; `internal/workflow`, `internal/fsdiff`,
`internal/domain`). Then, in parallel with disjoint ownership:

| Worker | Owns |
|---|---|
| backend-2 | B2 operation error, B5, C1, C2, E1, F1, G2, H2, D3 (daemon, workflow, projection, cli, model/fake) |
| web-build | BuildPage, RunningCard, FailedBanner, CommitBar (B1, B3, B4, B6, E2, F3, G3) |
| web-shell | App, api.ts, ProductHome, PhaseNav, styles.css, router (F2, G1, G4, H1, J, K) |
| web-settings | Settings, providerStatus, DefinePage, PlanPage, JudgmentCard (C3 UI, D1, D2, F4, I) |

Shared contracts, fixed before workers start:

- **Errors**: `api.ts` throws `Error` whose `message` is the server's `error`
  string (F2 lands in the first web-shell commit; the others code against
  that contract).
- **`styles.css`** is append-only for web-build and web-settings: each adds
  new classes in its own clearly-headed block and never edits existing rules.
  web-shell owns existing rules.

Track L is split by file owner. Each worker: tests with the change,
mutation-check the guards, no edits outside its files.
