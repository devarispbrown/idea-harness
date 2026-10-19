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
# sandbox's: the coding agent (executor.kind omp/hermes/acp) always has network
# egress, deliberately, to reach its model API (see docs/agents.md); the
# check sandbox always runs with no network at all, unconditionally (see
# docs/security-model.md). Neither reads this setting (review defect #3).
# verify.setup.prepare and verify.security.scanners entries marked
# network: true are the only sandboxed commands given network, and they do
# not read this setting either -- listing them is the opt-in.
network: disabled

# The project's immutable principles, injected into every stage and reviewer as
# hard constraints (the project_policy context, policy trust). A reviewer is
# expected to flag any output that violates one. Empty means no project-specific
# constitution.
#constitution:
#  - "every feature starts as a small library with its own tests"
#  - "no new third-party dependency without a written justification"

# Review budget (§11, §16). The three fields bound different things — see the
# table below; they are not three spellings of one budget.
review:
  max_cycles: 3        # review passes in M1 before needs_human_judgment (0 also means 3)
  max_tokens: 0        # requested output-token budget per call, 0 = each stage's own built-in size
  max_cost_usd: 0      # cumulative spend ceiling for the project (0 = no ceiling)
  rigor: auto          # auto | quick | standard | thorough (review B1)
  convergence:         # how a review loop decides it is done (all optional)
    mode: guarded            # guarded | legacy
    gate_severity: high      # high | critical — the lowest adversarial severity that blocks
    require_citation: true   # a blocking finding must cite a requirement/task (or quote the idea)
    later_findings: changed  # changed | critical | any — the bar for a NEW finding after cycle 1
    stall_cycles: 1          # non-decreasing cycles tolerated before stopping for a human

# Blast-radius guard (review C): the most files a single change set's diff may
# touch before `idea execute` refuses it, before the adversarial review ever
# runs. 0 is not unlimited -- it means "use the built-in default of 10". See
# "Blast radius" below.
execute:
  max_files: 10
  # max_parallel: 2   # run up to N independent ready tasks at once (default 1)

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

# Outcome-metric measurement (`idea metric measure`). Nothing here runs on its
# own; see "Objectives & outcomes" below.
outcomes:
  image: ""                     # sandbox image for command sources (default: check.image, then derived)
  measure_timeout_seconds: 120  # bound on one command source
  allow_url_sources: false      # opt in to fetching url sources (public addresses only)

# Spending cap per initiative, in USD (0 = none). An initiative can set its own
# with `idea budget <initiative> --set <usd>`. See "Budget" below.
budget:
  default_initiative_usd: 0

# Scheduled background jobs, every one off until enabled. See "Automations".
automations:
  metric_measurement:
    enabled: false              # measure command/url metrics on their own cadence
  eval:
    enabled: false              # run `idea eval` on a schedule (needs model + max_cost_usd)
    model: ""                   # provider/model
    interval_hours: 24
    max_cost_usd: 0             # per-run cap; required to enable
```

Top-level fields:

| Field | Meaning | Default |
|---|---|---|
| `network` | `disabled` \| `enabled` — whether research may fetch `research.urls` | `disabled` |
| `constitution` | list of strings — the project's immutable principles, injected into every stage and reviewer as hard constraints (`project_policy` trust); a reviewer flags violations | (none) |
| `review.max_cycles` | how many **review passes** the M1 review loop may run before it stops and sets the initiative to `needs_human_judgment`. A revise happens between passes, so `n` passes allow at most `n-1` revisions. `0` is **not** unlimited: the engine coerces any value `<= 0` to 3 | `3` |
| `review.max_tokens` | the **output-token budget requested for every model call**, and the ceiling checked against the response — it is a per-call figure, not a pool that drains. Set, it directly overrides a stage's own built-in request size (still bounded by the model's real output ceiling — see "Model output ceilings" below); a response over it still fails that stage. Unset (`0`), each stage uses its own built-in request size as before | `0` (each stage's built-in size) |
| `review.max_cost_usd` | **cumulative** spend ceiling across every model call the daemon makes for this project. The running total is charged per billed attempt (retries included) and is never reset while the daemon lives, so a crossed ceiling refuses every later call until the daemon restarts | `0` (no ceiling) |
| `review.rigor` | `auto` \| `quick` \| `standard` \| `thorough` — how much scrutiny the PRD, plan and change-set reviews apply (review B1). See "Review rigor" below | `auto` |
| `review.convergence.mode` | `guarded` \| `legacy` — whether the review convergence rules apply. `legacy` is the pre-0.7 loop exactly. See "Review convergence" below | `guarded` |
| `review.convergence.gate_severity` | `high` \| `critical` — the lowest adversarial-reviewer severity that blocks the PRD or plan review; anything below is recorded as advisory. Deterministic checks are unaffected | `high` |
| `review.convergence.require_citation` | whether an adversarial finding must cite an active requirement (or, on the plan, a task) — or the idea, quoting it — to block | `true` |
| `review.convergence.later_findings` | `changed` \| `critical` \| `any` — what a finding first raised in cycle 2+ must be to block: about a requirement the revision added or changed (or critical), critical, or anything | `changed` |
| `review.convergence.stall_cycles` | how many consecutive PRD review cycles the open blocking count may fail to decrease before the loop stops for a human, ahead of `max_cycles` | `1` |
| `research.urls` | seed URLs fetched during research when `network: enabled` | none |
| `default_model` | `provider/model` used by every role without an override | unset — deterministic demo output |
| `models` | per-role overrides, role name → `provider/model` | falls back to `default_model` |
| `providers` | provider map (fields below) | none |
| `executor.kind` | `auto` \| `internal` \| `container` \| `omp` \| `hermes` \| `acp` \| `openhands`. `openhands` runs the agent on an OpenHands Agent Server and is never chosen by `auto` ([agents.md](agents.md#openhands-agent-server-kind-openhands)). `auto` (what an unset kind means) runs `hermes` when Docker is reachable and the coding model's key is set, and otherwise falls back to `internal` with a warning on every surface that no real code will be written ([agents.md](agents.md#the-default-auto-with-a-loud-placeholder-fallback)). `internal` set explicitly keeps the placeholder quietly | `auto` |
| `executor.image` | container image for the non-internal kinds. The default is **per-kind**, not one blanket value: `alpine:3.20` only for `kind: container`; the digest-pinned `nousresearch/hermes-agent:…@sha256:…` for `kind: hermes` (and for `auto` when it resolves to hermes); `kind: omp` and `kind: acp` have no default image at all and refuse every execute until one is set | `alpine:3.20` for `container`, the pinned Hermes image for `hermes`, none for `omp`/`acp` |
| `executor.model` | coding model for the agent kinds (`omp`, `hermes`, `acp`, `openhands`) | — |
| `executor.acp.command` | `acp` only: the ACP agent's argv inside the image, e.g. `[claude-code-acp]`; `{model}` and `{bare_model}` are substituted from `executor.model` | — (required for `acp`) |
| `executor.acp.key_env` | `acp` only: the name the provider key is exposed under inside the container (e.g. `GEMINI_API_KEY`) | the provider's own variable |
| `executor.acp.timeout_seconds` | `acp` only: bound on one prompt turn, after which the client sends `session/cancel` and then kills the container | `840` |
| `executor.openhands.url` | `openhands` only: the Agent Server's base URL. Must be `https` unless the host is loopback, and carry no credentials; anything else is refused at load | — (required for `openhands`) |
| `executor.openhands.api_key_env` | `openhands` only: the NAME of the variable holding the server's session key (sent as `X-Session-API-Key`), read from the daemon's environment, then `.env`. Never the key itself | `OPENHANDS_API_KEY` |
| `executor.openhands.agent` | `openhands` only: `openhands` (the server's own agent, driving `executor.model`) or `acp` (an ACP agent the server's image carries) | `openhands` |
| `executor.openhands.acp_command` | `openhands` with `agent: acp` only: the ACP agent's argv on the server, e.g. `[claude-agent-acp]` | — (required for `agent: acp`) |
| `executor.openhands.workspace_dir` | `openhands` only: absolute directory on the server under which each run gets its own subdirectory | `/workspace/idea-harness` |
| `executor.openhands.withhold_provider_key` | `openhands` only: do not send the coding model's provider key to the server; the server must supply its own | `false` |
| `executor.openhands.max_iterations` | `openhands` only: bound on the agent's steps on the server | `100` |
| `executor.openhands.timeout_seconds` | `openhands` only: bound on one run from dispatch, after which it is interrupted on the server | `840` |
| `executor.openhands.ui_url` | `openhands` only, optional: an OpenHands web UI that shows the server's conversations; each run then links to `<ui_url>/conversations/<id>` ("Open in OpenHands"). Absolute http(s), no credentials | unset (no link) |
| `executor.mcp_servers` | `acp` only: MCP servers passed in `session/new` — `name` plus `command`/`args`/`env` (stdio, runs inside the agent's container) or `type: http\|sse`/`url`/`headers`. Each `env`/`headers` entry is `{name, value}` for a literal or `{name, value_env}` naming a variable resolved from the daemon's environment, then `.env`; a literal that looks like a secret is refused at load. See [agents.md](agents.md#mcp-servers-acp-only) for the trust tradeoff | none |
| `check.image` | image holding the project toolchain for the build/test checks | unset — derived from the project's own toolchain markers when unambiguous, otherwise the check fails closed (see "Check auto-detection" below) |
| `build_cmd`, `test_cmd` | deterministic check commands; config-supplied, never model output | unset — derived from the project's own toolchain markers when unambiguous, otherwise the check is recorded `not_applicable` (see "Check auto-detection" below) |
| `check_timeout_seconds` | per-check timeout | `600` |
| `execute.max_files` | the **blast-radius guard** (review C): the most files a single change set's diff may touch before `idea execute` refuses it, before the adversarial review ever runs. Counts only what `fsdiff.Diff` reports, which already excludes its own well-known-dir/control-plane paths and (for the produced image) the workspace's own `.gitignore` — a lockfile regen or another generated/ignored file never counts against it. `0` is **not** unlimited: the engine coerces any value `<= 0` to `10`. See "Blast radius" below | `10` |
| `execute.max_parallel` | how many independent ready tasks `idea execute` runs at once (a wave): tasks whose `depends_on` predecessors are applied run concurrently, each in its own work root. Independent tasks editing overlapping files are caught at apply by the stale-base guard (the later change set is refused as stale and re-run). `0`/unset means `1` — serial | `1` |
| `git_commit` | also create a git commit (repository hooks disabled) for each applied change set | `false` |
| `git.allow_default_branch` | let commit-on-apply commit while the repository's default branch is checked out | `false` |
| `git.branch_per_initiative` | create and check out `idea/<slug>` on an initiative's first apply, and commit each of its change sets there. **Implies committing on its own** — setting this alone, with `git_commit` left `false`, is sufficient (review defect D) | `false` |
| `demo.block_review_cycles` | how many adversarial-review calls the **built-in demo provider** answers with a blocking finding before reverting to its usual empty list, capped at `review.max_cycles`. Does nothing when the default model resolves to a real provider | `0` |
| `demo.block_plan_review_cycles` | the same for the M2 **execution-plan** review, on its own queue. Uncapped: `idea revise-plan` clears a plan review however many times it blocks | `0` |
| `demo.block_change_set_review_cycles` | the same for the M3 **change-set** review, on its own queue. The change-set review runs exactly once, when the change set is created (right after checks pass) — not again on `approve` — so this only ever drains at most one entry off the queue; a surplus is harmless. This is the one whose escape needs `idea review <CHG-n> approve --accept-findings`: a plain `approve` only evaluates the findings the one review already persisted (it never calls the model again), and the deny gate refuses outright until a human dispositions them | `0` |
| `outcomes.image` | sandbox image a metric's `command` source runs in | `check.image`, then the image derived from the project's toolchain markers, then the sandbox's minimal `alpine:3.20` |
| `outcomes.measure_timeout_seconds` | bound on one `command` source; past it the measurement fails and nothing is recorded | `120` |
| `outcomes.allow_url_sources` | fetch metrics' `url` sources at all. Off by default: it is the one measurement path that reaches the network, and `network: enabled` (which is about research) does not imply it | `false` |
| `budget.default_initiative_usd` | spending cap for every initiative without its own (`idea budget`), checked before every model call and before the coding agent starts; reaching it parks the initiative on `needs_human_judgment` (`budget_exceeded`). See "Budget" below | `0` (no cap) |
| `automations.metric_measurement.enabled` | measure each command/url metric on its own `cadence` (at most every 15 minutes). See "Automations" below | `false` |
| `automations.eval.*` | `enabled`, `model`, `interval_hours`, `max_cost_usd`, `suite`, `cases`, `baseline` — the scheduled `idea eval`. See "Automations" below | off |
| `demo.cost_per_call_usd` | makes every demo-provider call report this cost, so a spending cap can be reached without a real provider (CI, the browser suite) | `0` |
| `demo.dangle_plan_dependency` | makes the demo's first execution plan name a dependency no task provides, parking the initiative on `plan_dependencies` | `false` |
| `verify.dimensions.<dimension>` | `off` \| `advisory` \| `gate` for one of `setup_reproducibility`, `docs_completeness`, `api_compatibility`, `api_ergonomics`, `security`, `performance`, `operational_readiness`. See "Quality dimensions" below | per effective rigor (below) |
| `verify.setup.commands` | the exact commands setup_reproducibility runs, replacing README/Makefile detection | detected |
| `verify.setup.prepare` | commands run **with network** before the offline run, to fetch dependencies ([security-model.md](security-model.md#execution-gateway)) | none |
| `verify.security.scanners` | `{name, command, network}` scanners run in the sandbox; non-zero exit fails | none — vulnerability scan `not_measured` |
| `verify.performance.budgets` | `{name, command, metric, unit, max, max_regression_pct}` budgets | none — performance `not_measured` |
| `verify.model_review` | add a model-reviewed check to `docs_completeness` and/or `operational_readiness` | none |
| `verify.timeout_seconds` | per-command bound for sandboxed quality commands | `check_timeout_seconds`, else `600` |

### Writes from Settings, Automations and the CLI

`idea model set`, `idea executors setup`, `idea budget --set` and every save in the web UI's
Settings and Automations pages write `harness.yaml` through one path. It re-reads the file from
disk (so a hand edit is never clobbered), changes only the fields the request names, refuses
anything `idea config validate` would refuse — the schema, an inline secret, a token pasted into an
`*_env` field — before writing a byte, **keeps your comments** and your key order, and reloads the
project so the change applies without restarting the daemon. A reload is refused while an operation
is running; the file is then written but not applied (`409 config written but not applied`), and
the next request after the operation finishes picks it up.

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

Under the default `review.convergence` (guarded) any value of 2 or more parks the review, and it
parks at **cycle 2**: the demo's blocking finding cites the requirement and justifies its re-raise,
so it is a genuine persisting blocker, and the stall detector stops the loop as soon as the
blocking count fails to fall. The surplus queue entry stays on the `review` queue (a later
`idea revise` would consume it). A value of 1 blocks once and converges at cycle 2.

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

### Blast radius (review C)

The ICP founder journeys turned up a one-endpoint task that restructured an 80-line service into
`cmd/` + `internal/`, unasked, producing a 15-file / ~26 KB diff that went on to hit the adversarial
reviewer's own output ceiling. `execute.max_files` is the guard for that shape of sprawl: a change set
whose diff touches more files than the limit allows is **refused before the adversarial review ever
runs** — no model call is spent reviewing a diff the human is going to have to split anyway — with a
message naming the real count, the real limit, and both ways forward.

The count is **files only**, not added lines: "lines added" is not a field this project persists
anywhere (a change set's patch manifest records per-file content digests, not line-level diff stats),
so deriving it would mean inventing a new column purely for this guard. Counting files keeps the
guard reachable from what already exists, and files are what actually hit the reviewer's output
ceiling in the observed failure.

The count excludes whatever `fsdiff` already excludes — its well-known-dir list (`node_modules`,
`vendor`, `.tools`, …), the harness's own control-plane paths, and, for the produced image, the
workspace's own `.gitignore` (shipped in 0.6.0 for the toolchain-download failure, review defect #2).
A lockfile regen or another generated/ignored file an agent rewrites never counts against a limit
meant for hand-written sprawl, because `execute.max_files` is checked against the same post-exclusion
diff (`fsdiff.Diff(before, after)`) every other guard in this pipeline already uses — nothing here
applies a second, different filter.

**The default is `10`, not `15`.** The known-bad diff that motivated this guard was 15 files; the
default sits strictly below it rather than at it, while still comfortably fitting this project's own
target of 2-4 vertical slices per task.

**The refusal.** The change set still exists — `idea status` / `idea reviews` show it `rejected`,
with a `blast_radius` finding naming the change set, the count, and the limit (e.g. `CHG-7 touches 23
files, over execute.max_files (10)`), and a recommendation naming both ways forward: split the task
(`POST .../tasks/{id}/split`) and execute each piece, or raise `execute.max_files` in `harness.yaml`
if the task genuinely needs this many files. The task returns to `proposed` — the same recovery
`idea status` already uses for a secret-sweep denial — so it is neither stuck nor silently marked
done; running `idea execute` again re-picks it. No build/test check and no adversarial review ever
ran against the refused diff.

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
- **`review.max_tokens`** sets the output-token budget **requested** for every model call
  (`workflow.effectiveMaxOutput`) and is then checked against one response's actual output-token
  count. It never accumulates, and it does not limit input tokens. Before 0.6.0 it was checked-only:
  setting it could fail a stage whose response came in over it, but could never raise what was
  actually sent — a truncated adversarial review's own error message pointed at this field as "the
  stage budget" to raise, and doing so did nothing. It is now wired through: set, it overrides a
  stage's own built-in request size directly (see "Model output ceilings" below for what that size
  is still bounded by).
- **`review.max_cost_usd`** is the only cumulative figure. It is charged in `Engine.chargeUSD` from
  each response's reported cost, including the vision-description calls for uploaded images.
  The cost is computed from a built-in price table (`internal/model/pricing.go`), not billed
  amounts, so it is an estimate. A model with no row of its own (a date or release suffix on a row counts as that row) is charged $0.50 per million input
  tokens and $2.00 per million output tokens. Calls to a local model (Ollama, or any provider on `localhost`) are charged nothing.
  The coding agent's own model calls, made inside its container, are not counted.

Reading `0` as "unlimited" is right for `max_cost_usd`, "each stage's own built-in request size" for
`max_tokens`, and wrong for `max_cycles`, which the engine rewrites to 3. `idea config validate`
rejects a negative `max_cycles`.

### Review convergence

`review.convergence` decides when an adversarial review loop is done. It exists because the PRD
review did not converge in 2 of 2 measured real-model runs, and the code — not the model — explained
why:

1. **The reviewer was memoryless.** Each cycle received the requirements and nothing else — not the
   previous cycle's findings, not what the revision did about them — so every cycle was a fresh
   adversarial review of a usually larger PRD, and a fresh adversarial review always finds something.
2. **One finding anywhere blocked.** Convergence was "no open critical/high finding in one fresh
   pass", including a finding about text the reviewer had already seen unchanged and one that cited
   nothing at all.
3. **No cross-cycle identity.** A reworded re-raise was a brand-new finding (the near-duplicate
   matching only carried a *human's* disposition forward), and earlier cycles' findings stayed open,
   so a parked initiative listed every cycle's findings as current.
4. **The reviser was told imprecisely.** It got every finding the initiative ever had as
   "[severity] text" — no evidence, recommendation or requirement — and its policy only obliged it to
   act on a `mitigate` disposition no in-loop finding carries.
5. **Nothing measured progress.** The loop ran to `max_cycles` whether the blocking count was falling
   or flat, and recorded nothing either way.

In `guarded` mode (the default) the loop answers each:

- **The reviewer sees the previous cycle.** From cycle 2 it gets that cycle's blocking findings by
  label (`F1`, `F2`, …), what each cited, the reviser's own response to each, and which requirements
  the revision added or changed. A persisting finding is re-raised by label (`reraises`); re-raising
  one the revision *addressed* — answered it, or changed or replaced the requirement it cited —
  needs a `justification`, or it is recorded as advisory.
- **Re-raises are recognized even unlabelled**, by the same token-Jaccard similarity the decision
  and question de-duplication uses (requirement codes stripped, since a revision mints new ones).
  The superseded finding is dispositioned `duplicate`; a previous finding nobody re-raised is
  dispositioned `fixed` (if the revision addressed it) or `not_applicable`. Only the current cycle's
  findings stay open.
- **Only cited findings block.** A critical/high finding must name an active requirement (`REQ-n`),
  or a task on the plan review, or — for a gap — cite `idea` and quote it. An uncited one is scope
  expansion by construction and is recorded as advisory, never dropped.
- **Later cycles have a higher bar.** A finding that is *new* in cycle 2+ blocks only if it is about a
  requirement the revision added or changed, or is critical (`later_findings: changed`). Anything
  about text already reviewed unchanged should have been raised then.
- **Severity is a floor, not a hint.** Medium and below never blocked; they are now marked advisory so
  the record says so. `gate_severity: critical` raises the floor further.
- **The reviser gets a precise brief**: only the findings blocking *this* cycle, by label, with
  evidence and recommendation, and is asked to answer each in `review_responses`.
- **A stall stops the loop early.** The open blocking count must fall from one cycle to the next;
  after `stall_cycles` cycles where it does not, the initiative parks in `needs_human_judgment`
  without spending the rest of `max_cycles`. This is where a genuine blocker — a contradiction in the
  idea, a decision only a human can make — still reaches a human, and sooner.
- **Every cycle is recorded.** Each adversarial review run's manifest carries a `review` record:
  findings raised, new, re-raised and resolved by severity; what each rule demoted; the open
  blocking count before and after; and the outcome (`converged`, `revise`, `stalled`, `max_cycles`,
  or `blocked` for the single-pass plan review). `idea eval` reports them per case.

Nothing here lowers the bar for a genuine blocker: a cited finding that persists, with a reason,
keeps blocking until a human acts. Deterministic checks are never subject to any of it. The plan
review is a single pass, so only the per-finding rules (severity floor, citation) apply there.
A human `idea revise` (or resume) starts a new loop seeded with the still-open findings, so a
reworded twin of something the human just revised away is a re-raise rather than a new blocker.

`legacy` restores the pre-0.7 loop exactly — no prior-cycle context, no demotions, no stall
detector — though telemetry is still recorded. It is there to measure the difference (`idea eval`
runs the non-convergence scenario both ways) and as an escape hatch.

### Model output ceilings

Every stage's request carries a `MaxOutput` (the number of output tokens the model may produce),
capped by the model's own real ceiling so the request is never rejected outright — `internal/model`
tracks each provider's current models' verified ceilings (checked against that provider's own model
documentation, not guessed): Claude Sonnet 5/Opus 5/Fable 5.1/Opus 4.8 at 128,000, Claude Haiku
4.5/Sonnet 4.5 at their real, lower 64,000; current OpenAI models at 128,000 (the gpt-5.x/gpt-6
family), 32,768 (gpt-4.1/gpt-4.1-mini), 100,000 (o3-mini) or 16,384 (gpt-4o/gpt-4o-mini, their real
ceiling); Gemini's 3.x Flash family at 65,536. A model this table does not recognize falls back to
its provider's own conservative baseline rather than an optimistic guess.

If a reply is still cut off at that ceiling (`model: output truncated at the token budget`), the
engine retries ONCE with a smaller ask (fewer findings, more concise) before giving up — this is
usually enough for a moderately oversized diff or PRD. If it still cannot complete: for the
change-set adversarial review specifically, this is recorded on the change set explicitly (never
silently) instead of failing the whole run, and `idea review <id> approve --accept-findings` is the
supported way to proceed without a completed review (see the `idea review` row in
[`cli.md`](cli.md)); for every other stage, the run fails with a message naming `review.max_tokens`
as the field to raise, since that is now what actually controls the size of the request.

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

### Quality dimensions (`verify`)

`idea verify` measures more than the acceptance tests. Each **quality dimension** produces a
result — `pass`, `warn`, `fail`, `not_measured` or `not_applicable` — made of checks, each saying
whether it was **deterministic** or **model-reviewed** and citing the file:line (and requirement)
its evidence is in. Results are recorded in the product record and linked to the requirements they
bear on with a `verifies` edge, so `idea impact requirement REQ-1` lists them and
`idea why quality_result docs_completeness` walks back to the requirements.

`not_measured` is never a pass. It is what a dimension reports when the harness could not look
(no Docker, no toolchain image, no scanner configured, a vulnerability database that needs the
network, the demo model). A dimension's status is the worst of its checks in the order
`fail` > `not_measured` > `warn` > `pass` > `not_applicable`, so one unmeasured check keeps a
dimension from reading as passed.

```yaml
verify:
  dimensions:                  # override the rigor defaults, per dimension
    api_compatibility: gate    # off | advisory | gate
    performance: advisory
  setup:
    commands: []               # set to replace README/Makefile detection
    prepare: ["go mod download"]   # runs WITH network, before the offline run
  security:
    scanners:
      - { name: govulncheck, command: "go run golang.org/x/vuln/cmd/govulncheck@latest ./...", network: true }
  performance:
    budgets:
      - name: parse
        command: go test -run '^$' -bench BenchmarkParse -benchtime 2000x ./internal/parse
        metric: 'BenchmarkParse\S*\s+\d+\s+([0-9.]+) ns/op'   # first capture group is the number
        unit: ns/op
        max: 50000                # absolute ceiling (optional)
        max_regression_pct: 20    # vs the previous measurement (optional)
  model_review: [docs_completeness]
```

**Modes and the gate.** `off` is not measured. `advisory` is measured and reported and never
blocks. `gate` holds the verification gate — the initiative is not completed, and `idea verify`
exits `5` (`gate_unmet`) — while the dimension's latest result is `fail` or `not_measured`, or is
**stale** (measured against different applied change sets, policy or verify config than the
initiative has now). `warn` and `not_applicable` satisfy a gate. Edits made to the workspace
outside the harness between two verifies do not make a result stale; re-run `idea verify` after
them, or apply them through a change set.

**Defaults follow the effective review rigor** (`review.rigor`, with `auto` calibrated from project
size and the size of the initiative's applied change, as for the reviews):

| rigor | setup_reproducibility, docs_completeness, api_compatibility | api_ergonomics, security, performance, operational_readiness |
|---|---|---|
| `quick` | off | off |
| `standard` | advisory | off |
| `thorough` | **gate** | advisory |

`idea verify` measures every enabled dimension whose result is missing or stale before it runs the
gate (`--no-measure` skips that and uses the last recorded scorecard), prints the scorecard, and
includes it in `--json` as `quality`. `POST .../quality` measures from the API; `GET .../quality`
reads.

What each dimension checks:

- **`setup_reproducibility`** (deterministic, sandboxed). Copies the workspace's committable files
  (`git ls-files -co --exclude-standard`, minus `.env*` and `.idea-harness/`) — what a fresh clone
  of the applied workspace holds — and runs the documented commands there in the check sandbox with
  **no network**, in `check.image` or the image detected from the toolchain markers. Commands come
  from `verify.setup.commands`; else the shell blocks under the README's
  install/setup/build/test/development headings (only lines starting with a known tool —
  `make`, `go`, `npm`, `pnpm`, `yarn`, `pip`, `pytest`, `cargo`, … — or `./script`; `cd <dir>` is
  followed within a block when the directory exists); else the Makefile's `build`/`test` targets;
  else the build/test commands [detected from the toolchain](#check-auto-detection), with a `warn`
  that nothing documents them. Dependency-fetch commands (`npm ci`, `pip install`,
  `go mod download`, `git clone`, `curl`, …) and long-running ones (`npm start`, `go run`,
  `docker …`) are skipped and listed. A documented command that fails is a `fail` citing its README
  line — except after a skipped fetch with no `verify.setup.prepare`, where the run cannot tell
  missing dependencies from broken docs and reports `not_measured`. `verify.setup.prepare` commands
  run first, with network; package caches are kept inside the copy so the offline run sees them.
  No Docker or no image: `not_measured`.
- **`docs_completeness`** (deterministic; optional model review). A root README exists (else
  `fail`); it has a heading naming install/setup and one naming usage/examples (else `warn`);
  every HTTP route and CLI flag the change added in source is mentioned in some Markdown/RST doc or
  OpenAPI spec (else `fail`, citing the declaring line); every environment variable the change
  started reading is mentioned (else `warn`), and the README has a configuration/environment
  section when it added any; every exported Go declaration the change added has a doc comment, and
  every OpenAPI operation it added has a summary or description (else `warn`). Routes are recognized
  in Go (`HandleFunc`/`Handle`, gin/echo/chi/fiber verbs), Express/Fastify, Flask/FastAPI; flags in
  Go `flag`/pflag/cobra, argparse, click, commander; env reads via `os.Getenv`/`LookupEnv`,
  `process.env`, `os.environ`/`os.getenv`. A form not listed is not detected — silence, never a false
  failure. `verify.model_review: [docs_completeness]` adds a verification_engineer review.
- **`api_compatibility`** (deterministic). Diffs every changed OpenAPI 3 spec (YAML or JSON) before
  and after the initiative's change sets. **Breaking:** a path or operation removed; a parameter
  removed, a required parameter added, an optional one made required, or a parameter's type changed;
  a request body made required, or one gaining a required property or changing type; a 2xx response
  removed, a response media type removed, a response schema's type changed, or a property removed
  from a response object. Additions are reported, not breaking. Enum, format and constraint changes,
  and anything behind `oneOf`/`anyOf`/`allOf`, are out of scope. It also diffs the exported API of
  every changed importable Go package (not `internal/`, not `main`, not tests) with `go/parser`:
  a removed symbol, a changed func/method signature or field type, a changed type kind, or a method
  added to an existing exported interface is breaking. It cannot see changes only a type checker
  would (an underlying type changing behind a name). A breaking change is a `fail` unless an active
  requirement (statement or criteria) or a decided decision names the path or symbol, in which case
  it is a `warn` citing that requirement or decision.
- **`api_ergonomics`** (model-reviewed). The verification_engineer reviews the diff of the change's
  public surface — changed OpenAPI specs, changed importable Go files, source files adding a route or
  flag — against a rubric: naming consistency, error shapes, pagination, idempotency, examples. A
  finding counts only when it cites a file it was shown and a line inside one of that file's hunks;
  others are recorded in the summary check and never move the scorecard. High/critical → `fail`,
  medium → `warn`. The built-in demo provider declares it did not review, which is `not_measured`;
  so is a failed model call. No public surface changed: `not_applicable`.
- **`security`** (deterministic). Re-scans the applied files as they are now with the same
  credential detector and allowlist the secret sweep uses (`fail`; generic assignments in test
  fixtures `warn`); summarizes the deny checks — a denied change set never applied and is reported,
  an undisposed deny-coded or sensitive-area finding on an applied one is a `warn`; and runs each
  `verify.security.scanners` command in the sandbox (non-zero exit `fail`, exit 127 — not installed
  in the image — `not_measured`). With no scanner configured the vulnerability check is
  `not_measured`: govulncheck, `npm audit` and pip-audit need a vulnerability database from the
  network, which the sandbox only has for a scanner marked `network: true`.
- **`performance`** (deterministic, sandboxed). Runs each budget's command offline, reads the first
  capture group of `metric` from its output, and fails over `max` or over `max_regression_pct`
  against the previous measurement's value (the first measurement records the baseline). No budgets:
  `not_measured`. A metric that matches nothing: `not_measured`.
- **`operational_readiness`** (deterministic; optional model review). When the code starts a
  network server (`http.ListenAndServe`, `grpc.NewServer`, `.listen(port)`, `createServer(`,
  `uvicorn.run(`, `FastAPI(`, `Flask(__name__)`, `app.run(`): a route whose last segment is
  `health`/`healthz`/`healthcheck`/`livez`/`readyz`/`ready`/`ping`; SIGTERM handled
  (`signal.Notify`/`NotifyContext` naming `SIGTERM` plus a `Shutdown`/`GracefulStop` call in Go,
  `process.on('SIGTERM')`, `signal.signal(signal.SIGTERM)`, or uvicorn/gunicorn); a structured logger
  (slog, zap, zerolog, logrus, pino, winston, bunyan, structlog, python-json-logger); configuration
  read from the environment. Each absent one is a `warn` — absence of a pattern is weak evidence.
  Without a server they are `not_applicable`. Every Dockerfile has a `HEALTHCHECK` (`warn`).
  Migration safety, for changed `.sql` files and files under `migrations/`/`migrate/`: editing an
  already-present numbered migration is a `fail` (a database that applied it will not re-run it);
  an added `DROP TABLE`/`DROP COLUMN`/`DROP SCHEMA`/`TRUNCATE`/`RENAME`/unqualified `DELETE` is a
  `warn`. `verify.model_review: [operational_readiness]` adds a verification_engineer review for
  what the patterns cannot see.

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

  The daemon log is redacted too, by the same catalog plus every loaded project's secret values,
  and `GET /api/v1/logs` redacts again on read, so it no longer returns it verbatim. One thing this does **not** cover, deliberately:
  the raw blob store (`raw-blobs/`) keeps change-set file content and image bytes exactly as
  received. Both are documented at
  [`security-model.md`](security-model.md#what-this-does-not-protect-against).

  Redaction is also not a uniform gate on what leaves for the model provider — it is applied at
  specific points, not to every outbound request.

## Integrations

`integrations.slack` and `integrations.linear` in `harness.yaml` turn on the Slack (notifications,
approval buttons, `/idea`) and Linear (plan tasks as issues, labeled issues as initiatives)
integrations. Each is off until its section exists; tokens come from the environment or `.env` by
the `*_env` names, never from the file. Setup, every key, and the security trade-offs:
[integrations.md](integrations.md).

```yaml
integrations:
  slack:
    channel: C0123456789
    allowed_users: [U0123ABCD]   # empty = notify-only
  linear:
    team_key: ENG
```

## Remote access

Using the web UI from your phone or another computer — answering the "Needs you" queue, reviewing a
change set — is off until you turn it on. Unlike everything above, it is **daemon-wide**, not per
project: there is one daemon, one listener and one token for every project it serves. It lives in
`config.yaml` under the state root (`~/.local/share/idea-harness/config.yaml`, or
`$IDEA_HARNESS_STATE/config.yaml`), section `daemon:`, and `idea daemon` takes the same settings as
flags (which win over the file). What each setting changes, and what it does not protect against,
is specified in [`security-model.md`](security-model.md#remote-access) — read it before turning
this on.

```yaml
# What each key changes: docs/security-model.md#remote-access
daemon:
  # A name a TLS-terminating proxy in front of loopback answers to. With
  # `tailscale serve` this is your machine's MagicDNS name.
  allowed_hosts: [laptop.tail1234.ts.net]
  # Or: a second, non-loopback listener. Plain HTTP only on a Tailscale or
  # private address (with a warning); anything else needs tls_cert/tls_key.
  # listen: 100.101.102.103:8787
  # tls_cert: /path/to/cert.pem
  # tls_key: /path/to/key.pem
  # Browser sessions end after this long (and after a week unused). Default 720h.
  # session_ttl: 720h
  # The VAPID contact push services may use to reach you (mailto: or https:).
  # push_contact: mailto:you@example.com
```

| Key | Flag | Default | |
|---|---|---|---|
| `allowed_hosts` | `--allowed-hosts h1,h2` | none | Extra `Host`/`Origin` names to accept — a proxy's hostname |
| `listen` | `--listen ip:port` | none (loopback only) | A second listener on a non-loopback IP; the policy table is in [`security-model.md`](security-model.md#remote-access) |
| `tls_cert`, `tls_key` | `--tls-cert`, `--tls-key` | none | Serve `listen` over HTTPS. Both or neither |
| `session_ttl` | — | `720h` | A browser session's absolute lifetime |
| `push_contact` | — | the project page | VAPID `sub` for Web Push |

The daemon refuses to start on a `listen` it will not allow, and logs a warning for every one it
allows without TLS. Loopback (`IDEA_HARNESS_ADDR`, default `127.0.0.1:8787`) is served exactly as
before either way, and keeps handing the token to the local UI.

### Recommended: Tailscale

[`tailscale serve`](https://tailscale.com/kb/1312/serve) gives the daemon an HTTPS address on your
tailnet with a real certificate, reachable from your own devices and nobody else, while the daemon
itself keeps listening on loopback only:

```bash
tailscale serve --bg 8787                      # https://laptop.tail1234.ts.net → 127.0.0.1:8787
idea daemon stop                               # restart with the name allowed:
idea daemon --allowed-hosts laptop.tail1234.ts.net   # or put it in config.yaml
idea ui pair --label phone                     # prints a one-time code
```

Open `https://laptop.tail1234.ts.net` on the phone, enter the code, and you are signed in. HTTPS
matters beyond encryption: browsers only install the app to the home screen, and only allow push
notifications, on a secure context (HTTPS or `localhost`). `tailscale funnel` would put the same
page on the public internet; don't.

To keep the daemon running always-on — at boot, after a crash, or on a box in the cloud — see
[`self-hosting.md`](self-hosting.md).

### Signing in and out

- `idea ui pair [--label <name>]` mints a one-time code (ten minutes, single use) and prints where
  to open the UI. Only the CLI on the daemon's machine can do this — it needs the API token, which
  never reaches a remote browser. `--qr` also draws that address as a QR code in the terminal, so
  the phone's camera can open it; the code is printed beside it and is never inside it (codes
  never go in URLs). Without colour (output not a terminal) it is drawn for a dark background, and comes out inverted on a light one, which
  most scanners accept; the address is printed under it either way.
- `idea ui sessions` lists every browser session; `idea ui sessions revoke <id>` signs one out, and
  `revoke --all` signs out every one. Settings → **Remote access** in the UI does the same.

### Push notifications

Settings → **Notifications** turns on Web Push for the project you are viewing, per browser, with
a toggle per event: needs your judgment, change set ready for review, verification failed, outcome
drifting. The browser asks for permission only when you press the button. Delivery rides the same
integration bus as Slack ([integrations.md](integrations.md)): retried with backoff when the push
service is unreachable, dead-lettered after eight attempts, removed when the push service says the
browser unsubscribed. It needs a secure context (HTTPS or `localhost`) and a browser that can reach
its push service (Google's, Mozilla's or Apple's); a plain-HTTP Tailscale IP is neither. The
daemon's VAPID key is generated on first use in `<state root>/push/vapid.json`; deleting it orphans
every subscription.

## Budget

A spending cap in US dollars per initiative: `budget.default_initiative_usd` for every initiative
in the project, or one initiative's own cap (stored in the project's state database, since a cap is
a decision about one piece of work) — `idea budget <initiative> --set <usd>`, the Build page's
meter, or `PUT …/initiatives/{id}/budget`. Its own cap wins; `--clear` returns it to the default.

- **What counts.** The initiative's own recorded spend, the number `GET …/costs` reports: every
  model call's billed cost (retries included), plus the coding agent's where the agent reported one.
  **Hermes and OMP usually print no cost, so their spend is not counted** — the meter says so
  (`agent_not_counted`) rather than presenting a partial number as the whole.
- **Where it is enforced.** Before every model call (the design pipeline, reviews, the planner,
  image descriptions) and before the coding agent starts, for every executor kind — the local
  container kinds, `acp` and `openhands` alike. A call already in flight finishes and is recorded,
  so an initiative can end at most one call over its cap.
- **What happens.** The run stops with `budget exceeded: …` and the initiative is parked on
  `needs_human_judgment` with reason `budget_exceeded` (`idea next` answers `resolve budget`),
  remembering the status it was in. An `initiative.budget_exceeded` event goes to the integration
  bus (Slack posts it by default). Raising the cap past the spend returns the initiative to where it
  stopped — `plan_approved` (or its earlier judgment) for a build-phase stop, `failed` naming
  `idea retry` for a stop in a new initiative's design pipeline — and the step is run again.
- **Not the same as `review.max_cost_usd`**, which is a process-lifetime ceiling on model calls
  reset by every daemon restart or config reload. The budget survives both.

## Automations

Background jobs the daemon runs on a schedule. **Every one is off until a person enables it** (the
Automations page, `PUT …/automations/{name}`, or the YAML below); a section that merely exists does
not run. "Run now" runs one job once, enabled or not.

| Job | Switch | Schedule | What it does |
|---|---|---|---|
| Linear inbound polling | `integrations.linear.inbound` (default `true` once Linear is configured) | every `poll_interval_seconds` | imports issues carrying the inbound label as initiatives ([integrations.md](integrations.md)) |
| Scheduled metric measurement | `automations.metric_measurement.enabled` | each metric's own `cadence`, at most every 15 minutes | `idea metric measure` for every live metric with a `command` or `url` source. Manual metrics and metrics without a cadence are never scheduled; `url` sources still need `outcomes.allow_url_sources` — enabling the schedule implies no network |
| Scheduled eval | `automations.eval.enabled` | every `interval_hours` (default 24); the first run is one interval after enabling | `idea eval` against `model`, capped at `max_cost_usd` per run (required: each case's initiative is held to what is left, and no case starts once it is spent). Reports are written to the project's state directory (`eval-reports/<stamp>.json` and `.md`) and compared with `baseline` (a saved report, relative to the project root) or, when unset, with the previous run; a regression is an `eval.regressed` event on the integration bus |

Each run is recorded with its trigger (`schedule` or `manual`), status, cost and a summary, and the
Automations page lists the history. For Linear, only polls that imported an issue, failed, or were
run by hand are kept — a quiet poll every minute would bury those.

## Objectives & outcomes

The PRD stage states the objectives an initiative exists to move (`OBJ-n`), each with outcome
metrics (`MET-n`), and links every requirement to the objectives it serves; humans add, edit and
link more with `idea objective …` / `idea metric …` (see [cli.md](cli.md)) or the Define page. A
metric's **source** says how its value is read:

| Source | Measured by | Where it runs |
|---|---|---|
| `manual` (default) | `idea metric record MET-n <value>` (or the MCP `record_metric` tool) | nowhere — a person types it |
| `command` | `idea metric measure [MET-n]` | `sh -c "cd /repo && <command>"` in the build/test check sandbox ([isolation properties](security-model.md#execution-gateway)), with the workspace mounted **read-only** at `/repo`. The last number printed is the value. Never the host — no Docker, no measurement |
| `url` | `idea metric measure [MET-n]`, with `outcomes.allow_url_sources: true` | the daemon's SSRF-guarded fetcher (public http(s) addresses only, at most 5 redirects); the JSON response is read at the metric's RFC 6901 `pointer` |

There is deliberately no networked `command` source: a number that lives behind an API is a `url`
source, where the request is the daemon's own and goes through the address guard; a command with
egress would be project code with the network, which the check sandbox exists to prevent. Nothing
measures on a timer unless you enable [`automations.metric_measurement`](#automations) — a metric
with a `cadence` that goes unmeasured becomes `stale`, which `idea status`, `idea next` and the
Health page report.

An **observation** is one measured value. Recording one also records which change sets landed
since the previous value, so `idea impact requirement REQ-1` reaches the metric the work moved and
`idea trace metric MET-2` names the change sets behind a regression. A **hypothesis** ("we believe
the checklist will move activation") is an assertion linked to the metric it predicts.

The PRD review enforces the chain: a requirement that serves no objective, and an objective with no
measurable metric, are high findings; a PRD with no objectives at all gets one medium finding, so
it is flagged but not wedged. The adversarial reviewer may cite `OBJ-n`/`MET-n` under the same
citation rules as a requirement.

```bash
idea objectives                                  # OBJ-n, what serves each, what measures each
idea objective add "New users reach a first sync on day one" --owner pm@example.com
idea objective link REQ-3 OBJ-1                  # REQ-3 serves OBJ-1
idea metric add OBJ-1 "day-one sync rate" --unit % --baseline 22 --target 40 --cadence weekly
idea metric record MET-1 31 --note "dashboard, week 2"
idea metric add OBJ-1 "signups" --target 500 --command "wc -l < /repo/data/signups.csv"
idea metric measure                              # run command/url sources now
idea metrics                                     # every metric with its drift
```

### Drift

Drift is computed deterministically on every read (`domain.EvaluateMetricDrift`):

| Status | When |
|---|---|
| `off_track` | the latest value is worse than the baseline — lower for `increase`, higher for `decrease`, farther from the target for `maintain` — or it breaches the guardrail (a floor for `increase`, a ceiling for `decrease`, the largest allowed distance from the target for `maintain`). With no declared baseline, the first observation is the baseline, so one reading can only be off track by its guardrail |
| `stale` | a cadence is set and more than one cadence has passed since the last observation (or since the metric was defined, if never observed) |
| `regressed` | the latest value is worse than the previous one by the same rule **and** at least one change set landed between the two. A drop with nothing landed is the world, not the work — it can still be `off_track` |
| `no_data` | never observed |
| `on_track` | none of the above |

The most urgent one wins (`regressed` > `off_track` > `stale` > `no_data` > `on_track`); every
signal is still reported. A requirement that is **verified** while an objective it serves has an
off-track or regressed metric is flagged **"works, but fails its objective"** — on `idea status`,
`idea next`, the Health page, the MCP `list_metrics` tool, and `GET …/outcomes`. An observation
that moves a metric into `off_track` or `regressed` publishes `outcome.drifted` to the
integrations ([integrations.md](integrations.md)). `idea gaps` reports the same chain
(`orphan_requirement`, `unmeasured_objective`, `unserved_objective`, `drifting_metric`,
`ineffective_requirement`); exact triggers are in
[architecture.md](architecture.md#asking-the-graph-gaps).

## Role Contracts

Bundled Role Contracts live in `.idea-harness/roles/*.yaml`. Each declares competencies, required
behaviors, artifact expectations, rubrics, required checks, and tool requirements (§9).
