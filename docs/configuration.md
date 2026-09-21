# Configuration

idea-harness reads portable config from `.idea-harness/` in the project root. Runtime state is
**never** stored here — it lives under `~/.local/share/idea-harness/` (§30) — with one narrow
exception: `.idea-harness/tmp/` is a self-cleaning staging directory for the atomic file writes
apply/revert make into your workspace (`internal/fsdiff.writeAtomic`, review defect I). It exists
only so that the temporary file is never left sitting directly beside your own tracked files if the
daemon is killed mid-write; a same-project directory (rather than the state root) is used
deliberately so the final rename is guaranteed to stay on one filesystem. `idea init` writes a
`.idea-harness/.gitignore` that ignores it; it is never meant to be committed.

`idea config validate` checks the files against embedded JSON Schemas and rejects inline secrets.
You can also configure providers and the default model from the web UI (**Settings**) or the CLI
(`idea model`), both of which write `harness.yaml` for you.

A hand edit to `harness.yaml` — in an editor, not through the UI/CLI — takes effect on the daemon's
next request for that project; no daemon restart is needed. The daemon notices the file's changed
mtime and reloads it automatically (review defect #4). The one exception: a reload is deferred,
with a logged warning, while that project has an operation in flight (execute, apply, revert, a
review, …), since swapping config out from under a running one is unsafe; it reloads on the request
after that operation finishes.

**This auto-reload watches `harness.yaml`'s mtime only — never `.env`'s** (review MEDIUM #7). A
`.env` edit (rotating a key, adding a new provider's) is read fresh the next time something ALSO
reloads `harness.yaml` (touching it, even with no content change, is enough) or the daemon restarts;
by itself, it never reaches a running daemon. See "Secrets" below.

## `project.yaml`

```yaml
name: my-product   # project display name
```

## `harness.yaml`

```yaml
# Network policy v0.1: disabled | enabled (default: disabled). This governs
# ONLY the deterministic idea-brief/research/PRD pipeline: whether the
# researcher fetches research.urls below, and whether its findings may be
# typed "fact" (disabled downgrades every finding to "assumption"). It does
# NOT gate the coding agent's network access or the build/test check
# sandbox's: the coding agent (executor.kind omp/hermes) always has network
# egress, deliberately, to reach its model API (see docs/agents.md); the
# check sandbox always runs with no network at all, unconditionally (see
# docs/security-model.md). Neither reads this setting (review defect #3).
network: disabled

# Review budget (§11, §16). The three fields bound different things — see the
# table below; they are not three spellings of one budget.
review:
  max_cycles: 3        # review passes in M1 before needs_human_judgment (0 also means 3)
  max_tokens: 0        # per-response output-token ceiling (0 = no ceiling)
  max_cost_usd: 0      # cumulative spend ceiling for the project (0 = no ceiling)
  rigor: auto          # auto | quick | standard | thorough (review B1)

# Research sources (fetched — public addresses only — during research when
# network is enabled). Offline/empty means the researcher marks everything as
# an assumption rather than a fact.
research:
  urls:
    - https://example.com/domain-research
    - https://en.wikipedia.org/wiki/Your_topic

# Default model as "provider/model" (first slash separates provider from model).
default_model: openai/gpt-5.6-luna

# Model providers. API keys are resolved from the environment variable named by
# api_key_env — NEVER stored inline (§47).
providers:
  openai:
    api_key_env: OPENAI_API_KEY
  anthropic:
    api_key_env: ANTHROPIC_API_KEY
  gemini:
    api_key_env: GOOGLE_API_KEY
  openrouter:
    api_key_env: OPENROUTER_API_KEY
  ollama: {}            # local; no key required
  # Any custom OpenAI-compatible endpoint (Groq, Together, LM Studio, …):
  groq:
    type: openai
    api_key_env: GROQ_API_KEY
    base_url: https://api.groq.com/openai/v1

# Create a git commit (with repository hooks disabled) for every applied change
# set, on top of the byte-for-byte snapshot revert. Off by default (§M7).
git_commit: false
git:
  # Allow commit-on-apply to land directly on the repository's default branch.
  # Off by default; see "git_commit guards" below.
  allow_default_branch: false
  # Create and check out idea/<slug> on an initiative's first apply, and
  # commit each of its change sets there instead of whatever branch was
  # already checked out. Opt-in. This ALONE is enough to get commits on that
  # branch -- git_commit above need not also be true (review defect D: it
  # used to silently do nothing without it, though its own name never
  # suggested a second flag was required).
  branch_per_initiative: false
```

Top-level fields:

| Field | Meaning | Default |
|---|---|---|
| `network` | `disabled` \| `enabled` — whether research may fetch `research.urls` | `disabled` |
| `review.max_cycles` | how many **review passes** the M1 review loop may run before it stops and sets the initiative to `needs_human_judgment`. A revise happens between passes, so `n` passes allow at most `n-1` revisions. `0` is **not** unlimited: the engine coerces any value `<= 0` to 3 | `3` |
| `review.max_tokens` | ceiling on the **output tokens of a single model response**. A response over it fails that stage — it is a per-call cap, not a pool that drains | `0` (no ceiling) |
| `review.max_cost_usd` | **cumulative** spend ceiling across every model call the daemon makes for this project. The running total is charged per billed attempt (retries included) and is never reset while the daemon lives, so a crossed ceiling refuses every later call until the daemon restarts | `0` (no ceiling) |
| `review.rigor` | `auto` \| `quick` \| `standard` \| `thorough` — how much scrutiny the PRD, plan and change-set reviews apply (review B1). See "Review rigor" below | `auto` |
| `research.urls` | seed URLs fetched during research when `network: enabled` | none |
| `default_model` | `provider/model` used by every role without an override | unset — deterministic demo output |
| `models` | per-role overrides, role name → `provider/model` | falls back to `default_model` |
| `providers` | provider map (fields below) | none |
| `executor.kind` | `internal` \| `container` \| `omp` \| `hermes` | `internal` |
| `executor.image` | container image for the non-internal kinds. The default is **per-kind**, not one blanket value: `alpine:3.20` only for `kind: container`; the pinned `nousresearch/hermes-agent:…` for `kind: hermes`; `kind: omp` has no default image at all and refuses every execute until one is set | `alpine:3.20` for `container`, the pinned Hermes image for `hermes`, none for `omp` |
| `executor.model` | coding model for the agent kinds (`omp`, `hermes`) | — |
| `check.image` | image holding the project toolchain for the build/test checks | unset — derived from the project's own toolchain markers when unambiguous, otherwise the check fails closed (see "Check auto-detection" below) |
| `build_cmd`, `test_cmd` | deterministic check commands; config-supplied, never model output | unset — derived from the project's own toolchain markers when unambiguous, otherwise the check is recorded `not_applicable` (see "Check auto-detection" below) |
| `check_timeout_seconds` | per-check timeout | `600` |
| `git_commit` | also create a git commit (repository hooks disabled) for each applied change set | `false` |
| `git.allow_default_branch` | let commit-on-apply commit while the repository's default branch is checked out | `false` |
| `git.branch_per_initiative` | create and check out `idea/<slug>` on an initiative's first apply, and commit each of its change sets there. **Implies committing on its own** — setting this alone, with `git_commit` left `false`, is sufficient (review defect D) | `false` |
| `demo.block_review_cycles` | how many adversarial-review calls the **built-in demo provider** answers with a blocking finding before reverting to its usual empty list, capped at `review.max_cycles`. Does nothing when the default model resolves to a real provider | `0` |
| `demo.block_plan_review_cycles` | the same for the M2 **execution-plan** review, on its own queue. Uncapped: `idea revise-plan` clears a plan review however many times it blocks | `0` |
| `demo.block_change_set_review_cycles` | the same for the M3 **change-set** review, on its own queue. The change-set review runs exactly once, when the change set is created (right after checks pass) — not again on `approve` — so this only ever drains at most one entry off the queue; a surplus is harmless. This is the one whose escape needs `idea review <CHG-n> approve --accept-findings`: a plain `approve` only evaluates the findings the one review already persisted (it never calls the model again), and the deny gate refuses outright until a human dispositions them | `0` |
| `demo.dangle_plan_dependency` | makes the demo's first execution plan name a dependency no task provides, parking the initiative on `plan_dependencies` | `false` |

### Check auto-detection

Leaving `check.image`, `build_cmd` and `test_cmd` unset does not necessarily
mean the deterministic build/test checks are skipped. Each is derived from
the project's own toolchain markers when — and only when — exactly one
toolchain is unambiguously recognized; a project with more than one
recognized marker, or none at all, falls back to the old fail-closed
behavior (no sandbox image → the check is rejected rather than run on the
host; no command → `not_applicable`). A configured value always wins: none of
this is ever consulted when the corresponding field is already set.

`check.image` is derived from, in order: `.nvmrc` or `package.json`'s
`engines.node` (Node), `go.mod`'s `go`/`toolchain` directive (Go), or
`.python-version` (Python).

`build_cmd`/`test_cmd` are derived per toolchain marker:

| Marker | `build_cmd` | `test_cmd` |
|---|---|---|
| `go.mod` | `go build ./...` | `go test ./...` |
| `package.json` (`scripts.build`/`scripts.test`) | `<pm> run build` | `<pm> test` |
| `pyproject.toml` | — (no unambiguous build step) | `pytest` |
| `Cargo.toml` | `cargo build` | `cargo test` |

`<pm>` is `pnpm` (with `pnpm-lock.yaml`), `yarn` (with `yarn.lock`), or `npm`
otherwise. A detected command is shown on the check's finding, prefixed
"`build_cmd`/`test_cmd` not configured; detected `<command>` from `<marker>`",
so it is never mistaken for a command you configured yourself — and it is
always overridable: set `build_cmd`/`test_cmd` (or `check.image`) explicitly
in `harness.yaml` and detection is never consulted for that field again.

`demo.block_review_cycles` exists because the demo provider was a happy-path
oracle. Its reviewer returned zero findings unconditionally, so the review loop
always converged and `needs_human_judgment` was **structurally unreachable** on
the only path CI, `scripts/smoke.sh` and six DX audits ever ran — which is how a
defect that failed 4/4 runs of this project's own quick start, on the model the
README names, survived six rounds.

Set it to `review.max_cycles` (3 by default) and the PRD review will not
converge, so the initiative parks on a human and `idea approve` / `idea revise`
become reachable without a provider key:

```yaml
demo:
  block_review_cycles: 3
```

The three demo knobs write to **separate queues** — `review`, `plan_review` and
`execution_plan` — so a leftover response cannot outlive the call it was meant
for. (`dangle_plan_dependency` queues a *plan*, not a review.) That matters because all
three adversarial reviews used to share one key: a surplus could land on the
**change-set** review, and a blocking finding there cannot be cleared by anything
the CLI can do. `block_review_cycles` is still capped at `review.max_cycles`
because M1's call count is position-dependent; the other two are not, because
`idea revise-plan` clears whatever they produce.

It is a count rather than a flag on purpose. A standing response would answer
every call on its queue, and the count is consumed by the first calls a run
makes. `block_review_cycles` is clamped to `review.max_cycles` because M1's call
count is position-dependent and a surplus on the `review` queue reaches the
**change-set** review, where a blocking finding cannot be cleared by anything the
CLI can do.

**The queue is primed per project open, not per initiative.** It fills when the
daemon first opens the project and refills on every config reload, so the *first*
initiative after each of those blocks and a second one in the same project
converges normally. That is fine for a scripted run — `scripts/smoke.sh` restarts
the daemon and then creates exactly one initiative — and it is the thing to know
if you set this by hand and wonder why the second `idea new` behaved differently.

`git_commit` is off by default on purpose: a repository hook would run arbitrary code in the
daemon's environment, so committing is never implied by anything other than an explicit opt-in —
which is exactly what `git.branch_per_initiative` is (see below): turning it on, by itself, is
already that opt-in, and `git_commit` need not also be set (review defect D; it used to require
both, silently, though `branch_per_initiative`'s own name and doc never said commits depended on a
second flag). Applied change sets are always revertible from the snapshot regardless of either
setting — the commit is an extra, not the mechanism. The web UI exposes the same switch under
**Settings**.

**Hooks stay disabled for every agent commit** (`-c core.hooksPath=<empty temp dir>`), whether it
lands via `git_commit` or `git.branch_per_initiative`: a pre-commit or commit-msg hook is arbitrary
code, and the daemon's commits never run it. This is not configurable.

**`git_commit` guards (review C4, reordered by review H2).** Even with `git_commit: true`, the
commit-on-apply path refuses to create a commit — and instead records why on the change set as
`git_commit_warning`, leaving the apply itself to succeed exactly as it would with `git_commit`
off. The checks run in two groups, in this order:

1. **Before any branch switch** (so a hazardous repository state is never checked out into):
   - HEAD is detached;
   - a rebase, merge or cherry-pick is in progress (`.git/rebase-merge`, `.git/rebase-apply`,
     `.git/MERGE_HEAD`, `.git/CHERRY_PICK_HEAD` — resolved per-worktree via `git rev-parse
     --git-path`, so this is correct even inside a linked worktree).
2. **After** `git.branch_per_initiative`'s branch switch (if enabled), judged against whatever branch
   is checked out by then — the initiative's own branch, not whatever HEAD was before:
   - the current branch is the repository's default branch (a remote's `origin/HEAD` when configured;
     otherwise the repository's own `init.defaultBranch` if set; otherwise the first of
     `main`/`master`/`trunk`/`develop` that exists), unless `git.allow_default_branch` is set.

A refused commit never half-applies a change set: the files are written and the change set is marked
`applied` exactly as it would be with the commit switched off. Only the commit itself (and, when the
pre-checkout group refuses, the branch switch too) is skipped. `git.allow_default_branch` is
irrelevant once `git.branch_per_initiative` is on, since apply then always commits on the
initiative's own branch, never the default one.

The commit itself is scoped to exactly the change set's own paths with `git commit --only -- <paths>`
(not a plain `git commit`), so anything the user had already staged in the index before the daemon
ever ran — a mid-edit, a partially staged unrelated change — can never ride into the agent's commit
just because `git add -- <paths>` staged the change set's own files onto that same index.

**`git.branch_per_initiative`** creates and checks out `idea/<short-slug-of-the-initiative-title>` on
an initiative's first apply, refusing if the workspace has uncommitted changes outside the change
set's own files — so a branch switch never carries someone else's in-progress edit along with it —
whether that switch creates the branch for the first time or reuses one from a prior apply of the
same initiative. A brand-new branch is created from the repository's default branch (resolved the
same way as the default-branch guard above) **only when HEAD is already at that exact commit** —
never from a DIFFERENT initiative's own branch, which would otherwise silently stack this
initiative's commits on top of that unrelated one. When HEAD has diverged from the resolved default
(unpushed local commits are the normal case for a local tool), branching from the default's commit
would silently revert every file this change set doesn't touch to that older version, so it branches
from current HEAD instead and reports a warning (surfaced as the change set's `git_commit_warning`)
rather than doing that silently or refusing outright. Every later change set for the initiative
commits there.

Two things never count as "uncommitted changes outside the change set" for this guard (review defect
D):

- **`.idea-harness/`**, the harness's own control-plane directory. `idea init` leaves it untracked
  until you choose to commit it (`harness.yaml`, `project.yaml`, `roles/*.yaml` are ordinary,
  committable project files — see the top of this document), and its own scratch space
  (`.idea-harness/tmp/`) is never meant to be committed at all. Neither should ever block a branch
  switch, and now neither does.
- **Files this same initiative's own earlier applied change sets already wrote**, when those change
  sets never got committed themselves (typically because `git_commit`/`branch_per_initiative` was
  turned on only after they applied). These are caught up onto the initiative's branch as their own
  commit — separate from the change set actually being applied — right after the branch switch,
  instead of refusing forever over the harness's own prior writes. Anything else dirty in the
  workspace still refuses the switch, naming only the paths that are genuinely unexplained.

**`idea pr`** pushes that branch — to its configured upstream remote (`branch.<name>.remote`) when
one is set, otherwise `origin` — and runs `gh pr create`, with a body built from the initiative (PRD
summary, change sets, verification progress). It requires the GitHub CLI (`gh`) on `PATH`; a missing
`gh` answers a clear message rather than an opaque exec error. `SSH_AUTH_SOCK`, `GIT_SSH_COMMAND`,
`XDG_CONFIG_HOME`, `GH_HOST` and `GH_TOKEN` are passed through from the daemon's own environment to
both `git push` and `gh` (nothing else is — see the redaction/sanitization notes above). When the
push remote is a `github.com` fork under a different owner than `origin`, `--head` is given the
`owner:branch` form so `gh` resolves it against the fork rather than the base repository; this
owner-parsing only understands `github.com` remote URLs (`git@github.com:owner/repo.git`,
`https://github.com/owner/repo.git`, `ssh://git@github.com/owner/repo.git`) — a non-GitHub host, or a
remote URL shaped differently, falls back to a plain branch name, which only resolves correctly when
the push remote and the base repository share the same owner. `idea pr` is idempotent — a repeat call
answers the pull request already recorded on the initiative (`initiative.pr_url`) rather than opening
a second one. `idea pr checks <change-set-id>` shows the latest CI status via `gh pr checks`, which
every change set of the initiative shares. See [`cli.md`](cli.md) and [`api.md`](api.md) for the
exact commands and routes.

### The three review budgets

`max_cycles`, `max_tokens` and `max_cost_usd` are independent and are enforced in different places,
so it is worth being exact:

- **`review.max_cycles`** bounds only the M1 review loop (`workflow.Engine.runReviewLoop`). Each pass
  runs the deterministic checks plus the adversarial reviewer; if the pass is blocked and the cap is
  not yet reached, requirements are superseded and the PRD is regenerated before the next pass. At
  the cap the initiative becomes `needs_human_judgment` rather than failing. The cap applies **per
  invocation of the loop**, so each entry into it — a new `idea new`, or a revise driven through
  `POST /initiatives/{id}/revise` — gets a fresh count. The CLI has no revise or resume verb
  of its own; it reaches the loop through `idea new` and `idea next`. The M2 plan review
  is a single pass and does not consult this field.
- **`review.max_tokens`** is checked against one response's output-token count. It never accumulates,
  and it does not limit input tokens.
- **`review.max_cost_usd`** is the only cumulative figure. It is charged in `Engine.chargeUSD` from
  each response's reported cost, including the vision-description calls for uploaded images.
  The cost is computed from a built-in price table (`internal/model/pricing.go`), not billed
  amounts, so it is an estimate. A model with no row of its own (a date or release suffix on a row counts as that row) is charged $0.50 per million input
  tokens and $2.00 per million output tokens. Calls to a local model (Ollama, or any provider on `localhost`) are charged nothing.
  The coding agent's own model calls, made inside its container, are not counted.

Reading `0` as "unlimited" is right for `max_tokens` and `max_cost_usd` and wrong for `max_cycles`,
which the engine rewrites to 3. `idea config validate` rejects a negative `max_cycles`.

### Review rigor (review B1)

`review.rigor` calibrates how much scrutiny the PRD review (M1), the execution-plan review (M2),
and the change-set review (M3) each apply. It exists because a fixed rigor level was an ICP round's
root cause: a 60-line toy app's review loop demanded ABNF grammars and audit schemas, produced 36,
then 70, then 82 findings, and never converged — every finding treated as if the project were an
enterprise system, regardless of its actual size.

- **`auto`** (the default) calibrates per review, from two things:
  - **project size** — a cheap file-count and line-count walk of the workspace, using the same
    ignore rules the executor's seed copy uses (tracked + untracked-but-not-ignored files in a git
    repository; a plain walk skipping dotdirs and `node_modules`/`vendor`/`dist`/`build`/`target`
    outside one);
  - **change size** — for the execution-plan review, the (already-approved) requirement count; for
    the change-set review, the file count and changed-line count. For the **PRD review**, this is
    deliberately **not** a requirement count: an ICP re-run found the PM model reliably writes 9+
    requirements regardless of idea size, so calibrating the PRD's own rigor from the PRD's own
    requirement count could never select `quick`, however small the idea. PRD-authoring and
    PRD-review rigor instead use the **idea's own word count** (from its description text), which is
    known before the PRD exists and does not change across revise cycles.

  A small project AND a small change calibrate to `quick`; a large project OR a large change
  calibrates to `thorough` (touching one file of a 500-file codebase still gets more scrutiny than a
  greenfield throwaway); anything else is `standard`. A workspace that could not be measured (no
  workspace yet, a brand-new idea) is treated as **unknown, not small** — it calibrates to at worst
  `standard`, never the least-scrutiny `quick`.
- **`quick`**, **`standard`**, **`thorough`** override calibration outright.

Effects, at every level:

- **Only critical/high findings ever block approval** (`Severity.Blocking()`) — but whether a
  *particular* rigor level can relax that depends on where the finding came from:
  - A finding from a **deterministic check** (a task-dependency cycle, an untraced task, a
    superseded-requirement citation, a duplicate REQ code, a missing/vague acceptance criterion) is
    **never** relaxed by rigor, at any level. It always goes through the ordinary approval gate —
    quick does not auto-approve a plan with a cycle, or a PRD that fails a deterministic check, just
    because it calibrated to `quick`.
  - A finding from the **adversarial reviewer**, on the PRD or the execution plan, is what `quick`'s
    one-pass convergence (below) relaxes: it is marked `advisory` and does not gate approval, but it
    is never silently dropped either — see the verify gate note below.
  - For the **change-set review**, critical/high findings block at every rigor level, `quick`
    included. `quick` there means one review pass and a smaller findings cap (5, see below), not "no
    gate" — a system-deny check (the secret sweep, host-exec, a blob-digest mismatch) was always a
    security invariant regardless of rigor, and a genuine in-diff critical/high finding is treated
    the same way.
  - A finding marked `advisory` never blocks *completion* either (`idea verify`): the verify gate
    skips every advisory finding regardless of severity or disposition. It is still shown in triage
    so a human can look at it, just not as something completion is waiting on.
- **A per-cycle findings cap** keeps the findings, non-advisory blocking ones first: `quick` 5,
  `standard` 10, `thorough` 20. A critical/high finding that is not marked advisory is **never**
  dropped by the cap, however many of them there are relative to the limit — only the rest (advisory
  findings, and non-blocking severities) are capped, preferring non-advisory and higher severity. A
  dropped finding is not silent — an extra observation-severity, advisory finding records how many
  were dropped, without claiming they were all "lower-severity" (some may be advisory findings of any
  severity).
- **`quick` converges in one pass.** The PRD and plan review loops run exactly one cycle at `quick`.
  The adversarial reviewer's own findings are accepted as advisories (never falsely dispositioned
  "fixed"), but a deterministic finding still routes to `needs_human_judgment` exactly as it would at
  any other rigor. The change-set review is always effectively "one pass" (there is no automated
  revise loop for it, and no re-run on `approve` either — it runs exactly once, when the change set
  is created); at `quick` its cap is simply smaller, not its gate weaker.
- **Line count also feeds change-size calibration** for the change-set review, alongside file count:
  a diff estimated at more than roughly 500 changed lines counts as a "big" change (pushing toward
  `thorough`) even when it touches only a handful of files.
- **The adversarial reviewer, PRD-author and planner prompts all receive the project's measured
  scale, the effective rigor level, and — capped at 4000 bytes, on a UTF-8 rune boundary, and
  labeled as untrusted data (it is read from the live workspace, which a prior agent-applied change
  set could have edited) — the project's own conventions doc** (a CONVENTIONS.md or CONTRIBUTING.md
  file, first one found, when one exists), with an explicit instruction to judge proportionately and
  not demand infrastructure or formal schemas a small project doesn't have. The **detected stack**
  (from root-level marker files: `go.mod`, `package.json`, `Cargo.toml`, …) is given only to the
  **PRD-authoring and framing ("explore") prompts**, not the reviewer or planner — this is review
  B5's proportionate-PRD behavior, which also adds an explicit "do not add infrastructure the
  project does not have unless the idea asks for it" instruction on those two prompts specifically.
- **The PRD-authoring prompt states a target requirement count** scaled to the calibrated rigor
  (roughly 3–6 at `quick`, 6–12 at `standard`, 10–20 at `thorough`) and requires acceptance criteria
  testable by the project's own tests, a deterministic check, or a manual gate — not statistical
  sample sizes, confidence levels, named hardware/test-machine specs, or percentile SLOs, unless the
  idea itself asks for performance work. This is guidance, not a hard cap: the deterministic checks
  and the adversarial reviewer still catch a genuinely under-specified PRD.
- **The PRD adversarial reviewer's prompt always states the rigor and project scale it is reviewing
  at**, and at `quick`/`standard` caps "high" severity to ambiguity that would cause a wrong or
  unsafe implementation — measurement methodology, statistical rigor, and hardware/environment specs
  are at most "low" there unless the requirement under review is itself a performance SLO the idea
  asked for. `thorough` keeps the reviewer's own full judgment on severity.
- **A PRD revision keeps requirement codes stable.** When the model's response cites a requirement's
  exact existing code (e.g. `REQ-4`), that requirement is updated in place — same id, same code,
  preserving its `derived_from`/`produced_by` graph edges — instead of being superseded and
  recreated with a new code every revise cycle. Only the requirements this cycle's response does not
  continue at all are superseded.

`idea config validate` rejects any `review.rigor` value other than `auto`, `quick`, `standard`, or
`thorough`.

Each provider entry supports:

| Field | Meaning | Default |
|---|---|---|
| `type` | `openai` \| `anthropic` \| `gemini` | derived from the key |
| `api_key_env` | environment variable holding the API key | derived from the key |
| `base_url` | endpoint override | derived from the key |
| `models` | extra model names to show in the picker | catalog defaults |

`base_url` must be `https`, or `http` on loopback (`localhost`, `127.0.0.1`, `::1`) for local
runtimes such as Ollama and LM Studio. The built-in presets' endpoints may not be overridden: a
preset name keeps inheriting its well-known `api_key_env`, so repointing it would send that key and
the full prompt context to another host. Point a differently-named provider at the endpoint instead.
Keyless local presets (`ollama`) may still move between loopback ports. `idea config validate`
reports a `harness.yaml` that breaks these rules, and the API rejects the write.

## Supported providers

| Provider | Wire type | Default endpoint | Key env |
|---|---|---|---|
| OpenAI | `openai` | `https://api.openai.com/v1` | `OPENAI_API_KEY` |
| Anthropic | `anthropic` | `https://api.anthropic.com/v1` | `ANTHROPIC_API_KEY` |
| Google Gemini | `gemini` | `https://generativelanguage.googleapis.com/v1beta` | `GOOGLE_API_KEY` |
| DeepSeek | `openai` | `https://api.deepseek.com/v1` | `DEEPSEEK_API_KEY` |
| OpenRouter | `openai` | `https://openrouter.ai/api/v1` | `OPENROUTER_API_KEY` |
| Ollama (local) | `openai` | `http://localhost:11434/v1` | — |

DeepSeek, OpenRouter, Ollama, and custom providers all speak the OpenAI-compatible chat-completions
wire format, so they are configured as `type: openai` with a custom `base_url`.

## Choosing a model

The model is a `provider/model` string. Examples:

```text
openai/gpt-5.6-luna
anthropic/claude-sonnet-5
gemini/gemini-3.6-flash
deepseek/deepseek-v4-pro
openrouter/anthropic/claude-opus-5   # note: OpenRouter model ids contain slashes
ollama/llama3.1
groq/llama-3.3-70b-versatile
```

Splitting is on the **first** slash, so `openrouter/anthropic/claude-opus-5` resolves to the
`openrouter` provider with model `anthropic/claude-opus-5`.

Set it via:

```bash
idea model set openai/gpt-5.6-luna
# or interactively / via the web UI (Settings)
```

### Live model listing

To see a provider's **current** models (not the bundled suggestions):

```bash
idea model ls openrouter      # OpenRouter is public (no key needed)
idea model ls ollama          # your locally installed Ollama models
idea model ls openai          # needs OPENAI_API_KEY set
```

The web UI has a **"Load latest models"** button under Settings → Model. When a provider can't
enumerate models (no key / offline), idea-harness falls back to a bundled catalog of suggestions —
but any model id is accepted, so you can always type the exact model name your provider supports.

## Research & evidence (§46)

Research fetches seed URLs from `research.urls` when `network: enabled`. Fetches are restricted to
**public addresses only** (loopback, private, link-local, and cloud-metadata ranges are blocked),
are capped in size/time, and limited to 5 redirects. Fetched content is stored content-addressed as
evidence and injected as untrusted context.

With `network: disabled` (the default) or no URLs configured, the research stage runs offline and
marks every claim as an **assumption** with source `"model knowledge, unverified"` — never a Fact.

## Secrets

- Secrets are resolved from the process environment, with a **`.env` file in the project root** as
  a fallback (OS keychain and project-scoped secret adapters are later milestones). Example
  `.env`:

  ```dotenv
  DEEPSEEK_API_KEY=sk-...
  OPENAI_API_KEY=sk-...
  ```

  The process environment takes precedence over `.env`; `.env` values are never written to config
  or state, only read for API calls and redaction.
- `config validate` and the config API reject any key that would store a secret **value** inline
  (e.g. `api_key`, `password`, `token`); `*_env` references are permitted.
- Secret values are redacted on the paths that store them: the artifact/blob store, and the event
  log, whose rows carry digests into that store rather than content.

  Two things this does **not** cover, both deliberate and both documented at
  [`security-model.md`](security-model.md#what-this-does-not-protect-against): the daemon log is not
  redacted and `GET /api/v1/logs` returns it verbatim, and the raw blob store (`raw-blobs/`) keeps
  uploaded file content and image bytes exactly as received.

  Redaction is also not a uniform gate on what leaves for the model provider — it is applied at
  specific points, not to every outbound request.

## Role Contracts

Bundled Role Contracts live in `.idea-harness/roles/*.yaml`. Each declares competencies, required
behaviors, artifact expectations, rubrics, required checks, and tool requirements (§9).
