# ICP round: from "trial" to "adopt"

Source: an ICP journey on v0.5.1. The persona was a technical founder with a
live Node app, using `openai/gpt-5.6-luna` and Hermes, working toward
"per-user API keys with rate limiting". The verdict was **trial, not adopt**.
JTBD scores (1–5):

| JTBD 1 | JTBD 2 | JTBD 3 | JTBD 4 | JTBD 5 | JTBD 6 | JTBD 7 |
|---|---|---|---|---|---|---|
| 2 | 2 | 2 | 3 | 1 | 1 | 2 |

Two findings are verified in code:
- `internal/risk/risk.go:41-43` blocks the whole words `password`, `token`
  and `secret`.
- `RevertChangeSet` restores the pre-image without checking what is in the
  workspace now.

## Defaults chosen (the user said "fix all" without choosing; overridable)

- **D1 Rigor.** `review.rigor: auto` is the default. It calibrates to project
  size and change size. `quick|standard|thorough` override it.
- **D2 Secret sweep.**
  - Vocabulary words stop blocking. They become a non-blocking
    "sensitive area" signal that still summons the security specialist.
  - Real credential values (known formats and high-entropy assignments)
    still block, and now name the file and line.
  - A human may mark one such value "not a secret". Its fingerprint (sha256
    of the matched value, never the value) is allowed from then on for that
    project, recorded with actor and time.
- **D3 Git.** `git_commit` is on by default when the workspace is a git
  repo. `git.branch_per_initiative` is opt-in. With it on, `idea pr` opens a
  PR through `gh`.

## Tracks

### A. Trust breakers (must fix)

- **A1 Secret sweep (D2).**
  - Markers become non-blocking.
  - Credential findings carry `path:line` and the kind of secret.
  - A human "not a secret" disposition records the fingerprint, and the
    sweep honours it.
  - The denial copy says "Run the task again" (it said "the task will be
    re-planned", which never happens).
  - A denied task can be run again; today it is refused with 409.
- **A2 Only live inputs reach design and plan.**
  - Superseded requirements and superseded or rejected decisions are
    excluded from the inputs to design, plan, revise and review.
  - A decided human decision is authoritative: it is marked as such in the
    prompt, and the reviewer may not overrule it.
  - Acceptance: a test where a newer requirement contradicts a decided
    decision, and the plan inputs contain the decision and not the
    superseded requirement.
- **A3 Revert protects later edits.** Revert compares each touched file with
  the change set's post-image. If a file changed since apply, revert refuses,
  naming the files, and writes nothing. It is the mirror of apply's
  `StaleBase`.
- **A4 Impact is true (JTBD 5).**
  - Every generated task gets its `implements` edge. Find why generated
    tasks lacked edges, and fix it.
  - `add task` links a requirement.
  - Change sets link to their task and requirements.
  - Changing or reopening a decided decision marks dependents stale: the
    requirements that cite it, the tasks implementing those requirements,
    and the applied change sets for those tasks. Guidance says "N items may
    be invalid", and `impact` lists them.
  - Acceptance: change DEC after apply → `impact decision DEC-n` lists the
    requirement, task and CHG, and guidance flags a re-plan.

### B. Rigor fits the change

- **B1 Review calibration (D1).**
  - Rigor level from project size (files, LOC) and change size.
  - Cap findings per cycle: quick 5, standard 10, thorough 20.
  - Only critical/high block; the rest is advisory.
  - Quick converges in one pass.
  - The reviewer prompt receives the project's conventions and scale.
- **B2 Findings about pre-existing code do not block.** A change-set finding
  whose evidence is outside the diff becomes advisory ("backlog") instead of
  blocking approval.
- **B3 Vertical-slice plans.** The planner prompt targets 2–4 tasks that
  each ship a working slice, with one task per requirement only when asked.
  - Plan editing: merge and split tasks, edit dependencies, and delete with
    a readable dependency error.
  - Added tasks take a requirement.
- **B4 Fewer, better judgment calls.**
  - Near-duplicate decisions and questions (normalized plus similarity) are
    merged when created, not only at display.
  - A question already answered is not re-asked.
  - "Accept all recommendations" (API and UI).
  - Triage: the 3 highest-impact open items first, and the rest collapsed as
    "optional — recommendations will be used".
- **B5 Proportionate PRDs.** The PRD prompt respects the existing code and
  conventions (scale, stack) and does not add infrastructure the project
  does not have unless asked.

### C. Make it great

- **C1 What changed (JTBD 6).**
  - A per-initiative timeline in human language, correctly scoped: change
    sets applied, rejected and denied with why, decisions changed, stale
    items.
  - It is on the home page and in `idea changed`.
  - Short initiative titles are generated from the prompt; the full prompt
    is kept as the description.
- **C2 Verify proves something.** Map test results to acceptance criteria,
  since a passing check is evidence. The verification designer emits
  executable test names, and criteria that name a passing test are
  auto-verified with that evidence.
- **C3 Cost and time.**
  - Per-initiative and per-phase spend and duration in the UI and
    `idea status`.
  - Agent spend is estimated from Hermes's usage output where available,
    and otherwise shown as "not measured".
  - The CLI shows the running stage instead of "still working…".
- **C4 Git-native (D3).** Commit on apply by default in git repos; opt-in
  branch per initiative; `idea pr`; the latest CI status (via `gh`) shown
  on the change set when a PR exists.
- **C5 Toolchain detection.** The check sandbox image follows the project's
  toolchain (`.nvmrc`, `engines.node`, `go.mod` go directive,
  `.python-version`) when no `sandbox_image` is set.
- **C6 Honest status after failure.** Guidance after a denied, failed or
  rejected change set says what happened and the next step, not "ready to
  build".

### D. Delighters

- **D-a** "Rerun with a note to the agent": an optional note on
  execute/retry, appended to the brief.
- **D-b** A queue view across initiatives: "Needs you" sorted by impact.

## Ownership (parallel waves)

| Wave | Worker | Owns |
|---|---|---|
| 1 | security | A1 (internal/risk, internal/security, workflow sweep, finding disposition) |
| 1 | integrity | A3, C4, C5 (apply/revert, internal/git, checkCommand, cli pr) |
| 1 | graph | A2, A4 (advance/engine inputs, graph edges, impact, stale marking) |
| 2 | rigor | B1, B2, B3 backend, B4 backend, B5, C6 (review, prompts, plan ops, decision dedupe) |
| 3 | surfaces | C1, C2, C3, D-a, D-b backend (projection, verify, cost, CLI stage) |
| 3 | web | all UI: finding override, plan editing, accept-all/triage, timeline, cost, verify evidence, rerun note, queue |

Each worker writes tests for every behaviour and mutation-checks guards.
Adversarial review of each wave, then a final ICP re-run on the same journey.

## Review changes (adopted; supersede the text above)

1. **D2 / A1: allowing a secret value.**
   - The fingerprint is the sha256 of the secret value's capture group, not
     the raw match, so it is stable across quoting and whitespace.
   - Entries are listed in Settings and revocable, and each records actor
     and time.
   - `docs/security-model.md` documents `secret_sweep` as conditionally
     overridable by this human disposition, mirroring `review_open_blocking`.
   - A test keeps that doc row and the code in agreement.
2. **D3 / C4: git.** `git_commit` stays **opt-in** and gets a prominent
   toggle in Settings. The commit refuses, with a clear message, on:
   - a detached HEAD;
   - a rebase, merge or cherry-pick in progress;
   - the repository's default branch, unless `git.allow_default_branch`.

   Hooks stay disabled for agent commits, and this is documented. C4 is split
   into three independent pieces: guards and toggle; `branch_per_initiative`;
   `idea pr` plus CI status.
3. **A2: live inputs only.**
   - Decisions are `open | decided | superseded`; there is no "rejected".
     Superseded requirements and decisions are excluded from the
     design/plan/verification inputs, reusing `activeRequirements`.
   - "Authoritative" is made checkable: a deterministic plan check raises a
     blocking finding when a task or plan cites a superseded requirement code,
     or a decision in a way that contradicts its resolution. The resolution
     keyword check is advisory only.
4. **A4: task to requirement is N:M.**
   - `createTask` takes several requirement IDs.
   - An unresolved requirement reference fails loudly (a blocking
     `plan_traceability` finding), as unresolved `depends_on` already does.
   - Tasks added after approval must name at least one requirement.
   - Confirm the root cause with a real-model run before fixing.
   - A decision reopened or changed after build marks dependents stale:
     requirements that cite it, their tasks, and applied change sets.
5. **B2: in-diff evidence.** The review schema gains `path` (optional) and
   `in_diff` (reviewer self-report).
   - It is decided deterministically when `path` is given: a path not in the
     change set's files is advisory.
   - Self-report is used only when no path is given, and is labelled
     unverified.
6. **C2: evidence from tests.** A criterion whose mapped test passed is
   pre-filled with that evidence and shown as "evidence: test X passed". A
   human still sets the pass, and there is a "Confirm all with passing tests"
   action. Nothing is auto-verified.
7. **A1: the 409.** Reproduce it first, then fix the actual path. The UI copy
   change for A1 ships in wave 1 with the security worker; it is the one web
   string that worker may touch.
8. **B4: merging similar decisions.**
   - Merged when they have the same type and a token Jaccard similarity of
     at least 0.85 after normalization.
   - A test proves two distinct near-worded decisions ("which database?" vs
     "which cache?") are NOT merged.
9. **C6 changes `BuildGuidance`'s signature**: it takes the initiative's
   recent change sets.
10. **C5: toolchain image.** A toolchain-derived image is used only when
    detection is unambiguous; it still fails closed otherwise.
11. **Waves are sequential**, each merged and reviewed before the next
    starts. Within wave 1:
    - security lands first;
    - integrity rebases on it, since both touch `execute.go` in different
      regions;
    - graph owns the stale-marking primitive, and integrity consumes it.
    The C2 wire contract (below) is fixed before wave 3.

### C2 wire contract (wave 3)

A requirement's verification gains `evidence_tests: [{name, status,
change_set}]`. `POST …/verify` accepts `{"confirm_passing_tests": true}`,
which confirms every criterion whose evidence tests all passed.

## Wave 3 contracts (fixed before the workers start)

Backend (surfaces worker):
- **Timeline (C1).** `GET …/initiatives/{id}/timeline` returns
  `[{at, kind, title, detail, ref:{type,id}}]`, newest first.
  - `kind` is one of `change_set_applied`, `change_set_rejected`,
    `change_set_denied`, `change_set_reverted`, `run_failed`, `run_stopped`,
    `decision_changed`, `stale`, `phase_completed`.
  - Optional `?since=<RFC3339>`.
  - `idea changed` uses it.
- **Short titles.** Initiatives gain `short_title` (≤ 60 chars), generated on
  create from the idea. The full text stays in `title`/`description` as
  today. The UI shows `short_title || title`.
- **Evidence from tests (C2).** Each requirement in `GET …/requirements`
  gains `evidence_tests: [{name, status: "passed"|"failed", change_set}]`.
  `POST …/verify` accepts `{"confirm_passing_tests": true}`, which confirms
  every criterion whose evidence tests all passed; the actor is human.
- **Cost and time (C3).**
  - `GET …/initiatives/{id}/costs` returns
    `{total_usd, by_phase: [{phase, usd, seconds, calls}], agent: {runs, seconds, usd: number|null, note}}`.
  - Operations gain `stage` (the step running now, e.g. "Writing the PRD",
    "Reviewing the plan", "Agent running TASK-2").
  - The CLI prints it while waiting.
- **Rerun with a note (D-a).** `POST …/execute` and `POST …/retry` accept an
  optional `note`, appended to the implementer's brief. It is decoded
  leniently, like `fresh`.
- **Queue (D-b).** `GET /api/v1/projects/{projectId}/queue` returns
  `[{initiative_id, short_title, kind, label, rank, href}]` across
  initiatives, highest impact first.
- **Git settings.** `PUT …/providers` accepts `git_allow_default_branch` and
  `git_branch_per_initiative` alongside `git_commit`. `GET …/providers`
  returns all three.

Already shipped (waves 1–2), for the web workers:
- Findings: `secret_overridable`, `secret_preview`, `secret_kind`,
  `secret_path`, `advisory`, `path`, `in_diff`.
- `POST …/findings/{id}/disposition` `{"disposition":"not_a_secret"}`.
- `GET`/`DELETE …/secret-allowlist[/{id}]`.
- Plan: `POST …/tasks/merge`, `…/tasks/{taskId}/split`,
  `…/tasks/{taskId}/depends-on`.
- `POST …/decisions/accept-recommendations`.
- `GET …/triage`.
- Guidance: `stale_items`; `POST …/stale/{staleId}/ack`.
- `ChangeSet.git_commit_warning`.
- `Initiative.git_branch` / `pr_url`; `POST …/initiatives/{id}/pr`;
  `GET …/change-sets/{id}/pr-checks`.
- A revert refusal is a 409 whose message names the files and the advice.
- `harness.yaml` `review.rigor`, via `PUT …/providers` `review_rigor`. The
  surfaces worker adds this if absent.
