# Changelog

All notable changes to idea-harness are recorded here, following
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

Versioning is pre-1.0: the CLI surface, the HTTP API, and the on-disk config schema may change in
any release. Breaking changes are listed under **Changed** or **Removed** with the migration in the
entry itself. Once 1.0 ships, this project follows semantic versioning.

## [Unreleased]

### Fixed

- `--json` works on every state-returning command. `tasks`, `changes`, `risk`, `explain`, `why`,
  `impact`, `trace`, `diff`, `advance`, `execute` and `watch` treated the flag itself as a positional
  initiative reference, so the daemon looked up an initiative named `--json`, missed, and the CLI
  printed `sql: no rows in result set` and exited 1. Two of those commands change state, so a
  mistyped flag aimed a workflow transition at an initiative that does not exist.

- A clean checkout now compiles with no Node toolchain. `internal/web/dist/` was ignored by git while
  `internal/web/web.go` embeds it, so `go install ./cmd/idea`, `make test`, and `make vet` all failed
  in a fresh clone with `pattern dist: no matching files found`. A placeholder `dist/index.html` is
  committed; `make build` overwrites it with the real UI.

### Changed

- Unknown flags are now an error naming the command's accepted flags, instead of being silently
  treated as a positional argument (or, for `idea next`, discarded).
- `idea plan --json` now fails with a message pointing at `idea tasks --json`. It previously accepted
  the flag and printed markdown. The execution plan is a markdown artifact and has no JSON form.
- `idea verify --json` now emits the verification status and coverage as JSON. It previously accepted
  the flag and printed human-formatted output.

- `README.md` no longer describes Node and pnpm as optional for installing. They are needed only to
  change the web UI.
