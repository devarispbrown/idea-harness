# Changelog

All notable changes to idea-harness are recorded here, following
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

Versioning is pre-1.0: the CLI surface, the HTTP API, and the on-disk config schema may change in
any release. Breaking changes are listed under **Changed** or **Removed** with the migration in the
entry itself. Once 1.0 ships, this project follows semantic versioning.

## [Unreleased]

### Fixed

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

### Added

- **A CI job that runs the real agent path without an API key.** It starts the
  pinned image through the real executor against a refused endpoint and asserts
  Hermes accepted the query. Both startup defects above fail it; neither needed
  a key to detect. It runs on pushes rather than every pull request because the
  image is 5.4 GB, and releases now wait for it.

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
