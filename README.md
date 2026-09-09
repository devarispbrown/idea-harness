# idea-harness

A local-first AI product-development **control plane** that takes an idea from ambiguity to verified
software.

**Status:** M1–M4 pipeline implemented end-to-end (governed team-of-experts + provable provenance +
byte-for-byte revert). The build step uses a placeholder executor by default — wire OMP/Hermes for
real code.

```text
idea → reviewed PRD → execution plan → change set → apply → verify → complete
```

## Why idea-harness

Two properties make idea-harness trustworthy where a raw coding agent is not.

**Provable provenance.** Every requirement, decision, plan, task, change set, test, and verification
is a node in a **Product Graph**, with edges recording *why* it exists and *what depends on it*.
When the model says something, you can trace the chain back to evidence — and see the blast radius
before you change it:

```bash
idea why requirement REQ-1      # the evidence → assertions → decisions behind it
idea impact requirement REQ-1   # what breaks or goes stale if you change it
idea trace task TASK-1          # both directions (why + dependents)
idea changed                    # What Changed since your last look
```

**Safe byte-for-byte revert.** Every applied change set is captured as a content-addressed patch, so
any change can be rolled back to its exact pre-image — no guesswork about what the agent touched:

```bash
idea changes                    # list change sets
idea apply CHG-1                # apply an approved change set
idea revert CHG-1               # restore the previous files, byte-for-byte
```

## The governed pipeline

idea-harness is **not** a coding agent, chatbot, Kanban, or project-management tool. It is a
local-first product-development operating system: agents reason, the engine governs, experts
challenge, and the human decides what matters.

A coordinated team of Staff-level AI experts moves an initiative through the whole lifecycle —
framing and research produce a reviewed PRD, design produces a dependency-ordered execution plan,
the implementer produces change sets, and a reviewer role gates each one before the human
approves, applies, and verifies it.

The durable asset is the **Product Graph** (Evidence → Assertions → Requirements → Decisions →
Plans → Tasks → ChangeSets → Tests → Verification) that powers provenance, impact analysis, and
requirement verification.

## Execution model

- **Serial per initiative.** Tasks run one at a time, in dependency order from the execution plan.
  Multiple initiatives are supported; each advances independently, one task at a time.
- **Placeholder executor by default.** The default `internal` executor is deterministic and hermetic
  (no model, no network) — it writes a placeholder file, not real code. To generate real code, wire
  a coding agent with one command: `idea executors setup --model <provider/model> --yes`, which
  auto-downloads the pinned Hermes image (OMP needs `--kind omp --image <img>`). Add `--smoke` to
  run a trivial task through the agent and assert it produces a real file change — see
  [`docs/agents.md`](docs/agents.md).
- **Byte-for-byte apply/revert.** `idea apply CHG-1` writes the reviewed patch into your workspace
  and records its pre-image; `idea revert CHG-1` restores it exactly. Revert is **file-level**, not
  git history. To also get a git commit per applied change set, set `git_commit: true` in
  `.idea-harness/harness.yaml` (opt-in, hooks-disabled, stages only the patch's files).

## Install

### Prerequisites

- **Go 1.25.5 or newer** (`go version` — the module requires it).
- **Node.js + pnpm** — *only needed to change the web UI*. Building and installing the binary does
  not require them; `make build` rebuilds the UI and embeds it.
- **macOS or Linux.** **Windows is not supported**: the CLI starts its daemon in a new session
  (`internal/cli/detach.go`, `//go:build unix`), which has no Windows equivalent here yet.

  Releases ship four binaries. What CI actually executes:

  | Platform | Built | Tests run in CI |
  |---|---|---|
  | `darwin/arm64` | yes | yes (`macos-latest`) |
  | `darwin/amd64` | yes | yes (`macos-15-intel`) |
  | `linux/amd64` | yes | yes (`ubuntu-latest`) |
  | `linux/arm64` | yes | **no** |

  `linux/arm64` is the one gap, and it is the SQLite driver rather than first-party code: nothing
  here branches on `GOARCH`, but `modernc.org/sqlite` vendors a machine-translated SQLite per
  GOOS/GOARCH, so that binary's storage layer is compiled and never exercised. Hosted arm64 Linux
  runners are not available on this repository's plan; on a public repository they are, and adding
  the job is a two-line change to `.github/workflows/ci.yml`.

### Build & put `idea` on your PATH

```bash
git clone git@github.com:devarispbrown/idea-harness.git
cd idea-harness

# Install into your Go bin directory. No Node toolchain required.
go install ./cmd/idea
export PATH="$PATH:$(go env GOPATH)/bin"     # add to your shell profile to persist
```

That gives you the **complete CLI**. The **web UI** is built from `web/` and embedded at build
time, and the checked-in copy is a placeholder — so a binary installed this way serves a page that
says "web UI not built" at `http://127.0.0.1:8787/` until you build it:

```bash
make build                                   # needs Node.js + pnpm; embeds the real UI
export PATH="$PWD/bin:$PATH"
```

Release binaries ship with the UI already embedded, so a downloaded release needs neither Node nor
this step. Generated bundles are deliberately not committed: the UI changes daily, and a 240 KB
minified blob in git is a merge conflict on every change and a diff no reviewer can read.

### Upgrading

**Stop the daemon after replacing the binary.** It is detached and outlives the binary that
started it, so after `go install`, `make build`, or downloading a new release, the *previous*
version keeps serving every command while `idea version` reports the new one:

```bash
idea daemon stop      # the next command starts one from the new binary
```

`idea doctor` fails if you forget, naming both builds.

Verify:

```bash
idea doctor
```

> No daemon setup is required. The CLI auto-starts a local daemon on first use; `idea dev`
> runs it in the foreground and serves the web UI at `http://127.0.0.1:8787/` (see the note above
> on which builds carry the UI). All runtime state lives under `~/.local/share/idea-harness/` —
> never in your source tree.

## Quick start

The complete loop, from idea to verified product:

```bash
mkdir my-product && cd my-product

# With a provider key — export it BEFORE idea init, which starts the daemon:
export OPENAI_API_KEY=sk-…
# Without a key — skip that line and the `idea model set` below. Everything runs
# on the built-in demo provider; artifacts are synthetic and labeled as such.

idea init                              # creates .idea-harness/ (works with no Git)
idea model set openai/gpt-4o-mini      # only with a key; or use the web UI Settings

idea new "Build usage-based billing"   # M1: idea → reviewed PRD
idea next --until-human                # M2–M3: design → plan → change set, then stops for you
idea review CHG-1 approve              # human approves the change set
idea apply CHG-1                       # M4: apply it to your workspace
idea verify REQ-1 --pass               # record pass evidence; repeat per requirement
idea next                              # → "✓ initiative complete"
```

> **Export before `idea init`, and set a model only if you have its key.** The daemon reads your
> environment once, when it starts — and `idea init` starts it — so a key exported afterwards is
> invisible to it. If you forget, `idea daemon stop` makes the next command restart it with the key.
>
> Setting a model without its key is worse than setting none: `default_model` is resolved before the
> demo fallback, so the first model call becomes a non-retryable `401` and the initiative goes to
> `failed`, where doing nothing would have produced a complete synthetic run. `idea model set` warns
> when it can tell, and `idea doctor` asks the **daemon** whether it has the key — which is the
> process that makes the call, and the one that read your environment once at start. It fails,
> naming the variable and whether the daemon or only your shell is missing it.
>
> An initiative already in `failed` is recovered with **`idea retry`**, which re-runs the phase that
> failed rather than starting over — `idea next` names it, and `--model <provider/model>` retries on
> a different one. Fix the cause first: retrying into the same missing key just fails again.

> **With a real model, expect a stop the demo provider never produces.** The adversarial
> reviewer is doing its job when it does not converge, so `idea next` parks the initiative and
> names what to do: `idea reviews` lists the findings, `idea approve` accepts the PRD as it
> stands, `idea revise` regenerates it. The same happens at the plan (`idea approve-plan`,
> `idea revise-plan`). `idea status` says which one you are in.

Two guided walkthroughs of that block, with the output at each step and what to look at
before approving anything: [a new project](docs/demo-greenfield.md), and
[an existing codebase](docs/demo-existing-project.md).

`idea next` drives the loop: it runs the next automatable step (`advance` → `execute` → `verify`)
and stops at each human judgment point — `review`, `apply`, or resolving open decisions, questions
and findings (which is a web-UI action; the CLI only lists them) — printing exactly what to run
next. `idea new` waits for the run it starts (`--no-wait` returns immediately and
`idea watch` follows it), and `--until-human` keeps going through the automatable steps —
waiting for each to finish —
instead of stopping after one, which is why the block above is eight commands rather than thirteen.
The judgment points stay yours either way: `idea new` never drives itself past them.

Look closer whenever you want:

```bash
idea watch                             # follow a run live (Bubble Tea spinner)
idea prd                               # read the reviewed PRD in your terminal (Glamour)
idea plan                              # render the execution plan
idea tasks                             # list the dependency-ordered tasks
idea changes                           # list change sets (e.g. CHG-1)
```

After you wire a real executor (see [`docs/agents.md`](docs/agents.md)), `execute` produces real
file changes instead of a placeholder.

**Ground the agents in your own material (optional):** upload a PRD, design spec, tech doc,
market analysis, or a UI mockup image and it's content-addressed and injected into every downstream
stage — design, planning, and PRD revisions among them — so the team works from your context. Text
formats are extracted directly; images are described by a vision model, and only the description
reaches the agents.

```bash
idea new "Build usage-based billing" --doc prd.md   # text files, ingested with the idea
idea docs add spec.pdf      # .pdf / .docx / .xlsx / .jpg / .png — or `-` to paste
idea docs                   # list uploaded context docs
```

`idea docs add` attaches to an existing initiative, so it runs *after* `idea new`; `--doc` is how
text material goes in at the same time as the idea. **Text documents are credential-swept and
redacted at ingest; images are not** — they are stored, served, and sent to the vision provider
verbatim. See [`docs/security.md`](docs/security.md#context-docs-uploaded-source-material).

The daemon runs on `127.0.0.1:8787` and serves the web UI at `http://127.0.0.1:8787/` — from
`make build` or a release binary; see [Install](#install).

**Learn more:** [`docs/quickstart.md`](docs/quickstart.md) — the annotated end-to-end walkthrough
· [`docs/ui.md`](docs/ui.md) — the web UI at `http://127.0.0.1:8787/`.

## Model providers

idea-harness ships six built-in presets — **OpenAI, Anthropic, Google Gemini, DeepSeek, OpenRouter,
and Ollama (local)** — plus any custom OpenAI-compatible endpoint (Groq, Together, LM Studio, …).
Configure via the CLI or the web UI (Settings):

```bash
idea model                        # list providers + status
idea model set gemini/gemini-2.5-flash
idea model add groq --type openai --base-url https://api.groq.com/openai/v1 --key-env GROQ_API_KEY
```

API keys are read from environment variables (never stored inline):

```yaml
# .idea-harness/harness.yaml
default_model: openai/gpt-4o-mini
providers:
  openai:    { api_key_env: OPENAI_API_KEY }
  anthropic: { api_key_env: ANTHROPIC_API_KEY }
  gemini:    { api_key_env: GOOGLE_API_KEY }
  deepseek:  { api_key_env: DEEPSEEK_API_KEY }
  openrouter: { api_key_env: OPENROUTER_API_KEY }
  ollama:    {}                      # local, no key
```

Without a configured provider (or key), idea-harness runs in a **demo mode** that responds to your
idea with clearly-synthetic placeholder output (labeled in the UI and CLI). To do real research and
produce a real PRD, configure a provider and — optionally — research seed URLs (see
`docs/configuration.md`).

## Repository layout

```text
cmd/idea/        CLI + daemon entrypoint
internal/        Go packages (domain, store, blob, model, workflow, review, daemon, …)
web/             React + TypeScript UI (source; `make build` embeds it into the binary)
docs/            architecture, configuration, cli, security, agents, plans
scripts/         smoke tests (agent-smoke.sh exercises a real coding agent)
```

## Versioning

Pre-1.0: the CLI surface, HTTP API, and config schema may change in any release. Breaking changes
and their migrations are recorded in [`CHANGELOG.md`](CHANGELOG.md).

`idea version` prints the build — version, commit, and whether the tree was dirty — so a bug report
can name one:

```bash
idea version        # v0.3.0 (a1b2c3d)  ·  dev (a1b2c3d, dirty) from a working tree
```

Tagged releases publish prebuilt binaries for macOS and Linux (amd64/arm64), built in CI; the
table under [Install](#install) says which of the four are also tested there. Until the
repository is public, the canonical install stays `git clone` + `go install ./cmd/idea`:
`go install github.com/devarispbrown/idea-harness/cmd/idea@latest` additionally requires the module
path to be publicly fetchable.

## Documentation

**Start here — step-by-step walkthroughs of the whole loop, no API key required:**

- [`docs/demo-greenfield.md`](docs/demo-greenfield.md) — a new project, empty directory to
  verified and complete, in about two minutes.
- [`docs/demo-existing-project.md`](docs/demo-existing-project.md) — the same loop pointed at a
  repository that already has code, including what `apply` does and does not touch, and how to
  confirm a revert left the tree pristine.

- [`docs/agents.md`](docs/agents.md) — wire a real coding agent (OMP/Hermes) and read the security tradeoffs.
- [`docs/quickstart.md`](docs/quickstart.md) — annotated end-to-end walkthrough (recipe-sharing app).
- [`docs/ui.md`](docs/ui.md) — the web UI at `http://127.0.0.1:8787/`.
- [`docs/architecture.md`](docs/architecture.md) — architecture and system areas.
- [`docs/configuration.md`](docs/configuration.md) — `.idea-harness` config reference.
- [`docs/cli.md`](docs/cli.md) — CLI reference.
- [`docs/api.md`](docs/api.md) — the daemon HTTP API: auth, error shape, and a row per route
  (`make api-docs-check` keeps it complete), with [`docs/openapi.yaml`](docs/openapi.yaml) as the
  machine-readable spec. A running daemon serves it rendered at `http://127.0.0.1:8787/api-reference`.
- [`docs/api-reference-hosting.md`](docs/api-reference-hosting.md) — building without the bundled
  reference (3.7 MiB smaller) and hosting it on Cloudflare, AWS or Fastly instead.
- [`docs/security.md`](docs/security.md) — threat model, secrets, trust levels.
- [`docs/security-model.md`](docs/security-model.md) — system-deny conditions, executor trust tiers, execution gateway.
- [`docs/plans/`](docs/plans/) — approved milestone plans and adversarial reviews.

## Development

```bash
make test     # run the Go test suite
make vet      # go vet
make build    # build web UI + binary
```

See [`CONTRIBUTING.md`](CONTRIBUTING.md) for the process (plans are adversarially reviewed before
implementation; every ChangeSet gets a reviewer run before a human verdict).

## License

[Apache License 2.0](LICENSE). Copyright 2026 DeVaris Brown.
