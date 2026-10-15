# CLI / UI parity

Every verb the `idea` CLI accepts, and where the web UI offers the same thing — or an
explicit reason it does not. The CLI is the complete surface; this table is how the UI's
coverage of it stays honest.

The table is machine-checked by `TestUIParityLedger` (internal/cli/ui_parity_test.go). It
fails when:

- a verb in the CLI command table (`commands()` in internal/cli/cli.go, the same table
  `idea help --format=md` prints) has no row here — add one;
- a row names a verb the CLI no longer has — delete it;
- a row's **Status** and **UI surface** disagree: `ui` and `partial` need a `#/…` route in
  the surface cell, `cli-only` needs a cell starting `CLI-only:` with the reason.

A verb is the command word plus any subcommand word (`model set`, `pr checks`). A usage row
that only lists alternatives which each have their own row (`objective add|edit|link`) is
not a separate verb.

Status values:

- `ui` — the UI does what the verb does.
- `partial` — the UI covers part of it; **Notes** says which part is missing.
- `cli-only` — no UI surface. The reason says whether that is by design or a gap.

Routes use the UI's hash router (web/src/router.ts); `<id>` is an initiative id.

<!-- ui-parity:begin -->
| Verb | Status | UI surface | Notes |
| --- | --- | --- | --- |
| `init` | cli-only | CLI-only: creates `.idea-harness/` in a directory and starts the daemon the UI is served by — there is no UI before it runs | |
| `new` | ui | `#/` — New initiative form on the product home | |
| `status` | ui | `#/i/<id>/<phase>` — phase nav status chips and the phase header pill | |
| `changed` | ui | `#/i/<id>/changed` — What changed view; compact timeline panel in the shell | |
| `decisions` | ui | `#/i/<id>/define` — decision cards; `#/i/<id>/decision/<DEC>` for one decision | |
| `questions` | ui | `#/i/<id>/define` — open questions; answered ones, with their answer, under Resolved | |
| `answer` | ui | `#/i/<id>/define` — Answer on a question card | |
| `resolve` | ui | `#/i/<id>/define` and `#/i/<id>/decision/<DEC>` — option buttons / free-text resolution | |
| `reopen-decision` | ui | `#/i/<id>/define` — Reopen on a resolved decision | |
| `accept-recommendations` | ui | `#/i/<id>/define` — Accept all recommendations, with resolved / skipped / untouched-question counts | |
| `triage` | ui | `#/i/<id>/define` — ranked "needs your judgment" ordering of the judgment cards | |
| `reviews` | partial | `#/i/<id>/define`, `#/i/<id>/plan`, `#/i/<id>/build` — findings scoped to each phase | no single list of every finding across phases |
| `model` | ui | `#/settings` — Models: current default and curated set | |
| `model list` | ui | `#/settings` — provider list with readiness | |
| `model ls` | ui | `#/settings` — live model list per provider | |
| `model set` | partial | `#/settings` — default model chips / custom model id | `--role` (per-role model) has no UI control |
| `model add` | ui | `#/settings` — Add provider | |
| `prd` | ui | `#/i/<id>/doc/<artifact>` — Read on the PRD document row (Define) | |
| `explain` | cli-only | CLI-only: gap — the Current Understanding (`GET …/understanding`) is not rendered anywhere in the UI yet | |
| `export` | partial | `#/i/<id>/explore` — download an artifact as markdown | Explore lists artifacts for download; no kind picker like `export [kind]` |
| `advance` | ui | `#/i/<id>/define` — Proceed to design & plan | |
| `approve` | cli-only | CLI-only: gap — at `prd_review` the Define page offers only Revise PRD, not "accept the PRD as it stands" | |
| `revise` | ui | `#/i/<id>/define` — Revise PRD | |
| `approve-plan` | ui | `#/i/<id>/plan` — Approve plan | |
| `revise-plan` | ui | `#/i/<id>/plan` — Ask for a revision… with a note (`--note`) | reports "same plan: still vN" when nothing changed |
| `plan` | ui | `#/i/<id>/plan` — Read on the execution plan document row | |
| `tasks` | ui | `#/i/<id>/plan` — ordered task list (edit, split, merge, reorder, dependencies) | |
| `why` | ui | `#/i/<id>/lineage/<type>/<ref>` — Why tree; **Why / Impact** panel on requirements, tasks, change sets, decisions, objectives and metrics | |
| `impact` | ui | `#/i/<id>/lineage/<type>/<ref>` — Impact tree with staleness; **Why / Impact** panel | |
| `trace` | ui | `#/i/<id>/lineage/<type>/<ref>` — full edge trace; reached from Explore's per-document trace and every lineage node | |
| `gaps` | partial | `#/i/<id>/gaps` — full report with kind/severity filters; top 8 on `#/i/<id>/health` | `--fail-on` exit gating is a CI concern with no UI equivalent |
| `assumption` | ui | `#/i/<id>/gaps` — Accept / Dispute / Retract on unvalidated-assumption gaps | |
| `objectives` | ui | `#/i/<id>/define` — Objectives section | |
| `objective add` | ui | `#/i/<id>/define` — Objectives: add objective | |
| `objective edit` | ui | `#/i/<id>/define` — Objectives: Edit (statement, rationale, owner, status) | |
| `objective link` | ui | `#/i/<id>/define` — Objectives: link a requirement; × unlinks one (`--unlink`) | |
| `metrics` | ui | `#/i/<id>/define` — metrics under each objective; drift on `#/i/<id>/health` | |
| `metric add` | ui | `#/i/<id>/define` — Objectives: add metric | |
| `metric edit` | partial | `#/i/<id>/define` — Objectives: Edit on a metric (name, direction, baseline incl. clearing it, target, unit) | guardrail, cadence, definition, source (`--command`/`--url`/`--manual`) and moving it to another objective are CLI-only |
| `metric record` | ui | `#/i/<id>/define` — Objectives: record a value | |
| `metric measure` | ui | `#/i/<id>/outcomes` — Measure for command/url sources | |
| `diff` | cli-only | CLI-only: gap — the structured requirement/decision diff (`GET …/diff`) is not shown; `#/i/<id>/diff/<from>/<to>` is the text diff of two artifact versions, a different thing | |
| `execute` | ui | `#/i/<id>/build` — Run TASK-n / Start over, with the Note to the agent field (`--note`) | |
| `reopen-phase` | ui | `#/i/<id>/design` — Reopen on a design phase | |
| `retry` | ui | `#/i/<id>/<phase>` — Retry on the failed banner, with model and note (`--model`, `--note`) | |
| `changes` | ui | `#/i/<id>/build` — change set list | |
| `change` | ui | `#/i/<id>/build` — change set card, diff and agent output | |
| `apply` | ui | `#/i/<id>/build` — Apply | |
| `revert` | ui | `#/i/<id>/build` — Revert | |
| `pr` | ui | `#/i/<id>/build` — open a pull request | |
| `pr checks` | ui | `#/i/<id>/build` — PR checks list | |
| `review` | ui | `#/i/<id>/build` — Approve / Approve, accepting n findings / Request changes… with a note (`--note`) / Reject / Re-run review | Reject takes no note in the UI (`reject --note` is CLI-only) |
| `verify` | ui | `#/i/<id>/verify` — run the gate, Pass / Fail / Waive with a note, confirm passing tests | |
| `next` | partial | `#/i/<id>/<phase>` — the phase header's primary action is the next step | no "keep going until a human is needed" (`--until-human`) loop |
| `risk` | ui | `#/i/<id>/health` — risk profile | |
| `executors` | ui | `#/settings` — Executor section | |
| `executors setup` | partial | `#/settings` — executor kind and smoke test | image pull and ACP/OpenHands wiring flags are CLI-only |
| `integrations` | ui | `#/settings` — Integrations panel | |
| `integrations test` | ui | `#/settings` — Integrations panel Test buttons | |
| `watch` | ui | `#/i/<id>/build` — the run panel streams the running agent's events live and resumes after a reload | |
| `run events` | ui | `#/i/<id>/build` — the run panel shows the current run's events live (`--follow`) and resumes after a reload; the last run stays under Last agent run | |
| `steer` | ui | `#/i/<id>/build` — Send a note on the run panel, with Interrupt the current step (`--interrupt`) | |
| `cancel` | ui | `#/i/<id>/<phase>` — Cancel on the running card | |
| `config` | partial | `#/settings` — providers and executor settings are written to harness.yaml | no view of the whole config file |
| `config validate` | cli-only | CLI-only: validates harness.yaml on disk with a non-zero exit, for scripts and CI | |
| `doctor` | ui | `#/i/<id>/health` — doctor checks | |
| `docs` | ui | `#/i/<id>/explore` — context documents list | |
| `docs add` | ui | `#/i/<id>/explore` — upload a context document | |
| `logs` | cli-only | CLI-only: operator diagnostics; tailing the daemon log is a terminal task | |
| `daemon` | cli-only | CLI-only: runs the process that serves the UI | |
| `daemon status\|stop` | cli-only | CLI-only: process management of the daemon serving the UI | |
| `dev` | cli-only | CLI-only: starts the daemon and opens the UI | |
| `mcp` | cli-only | CLI-only: stdio server for MCP clients; no UI by design | |
| `eval` | cli-only | CLI-only: runs a private in-process daemon over an eval suite; a developer tool | |
| `version` | ui | `#/i/<id>/health` — daemon version in the Health footer | |
| `help` | cli-only | CLI-only: the CLI's own command list | |
<!-- ui-parity:end -->
