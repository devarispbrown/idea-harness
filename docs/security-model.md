# Security Model

idea-harness enforces a small set of deterministic, **non-overridable** security
boundaries. This document names them so they are auditable and testable.

## Principle: system deny beats human approve

A handful of conditions always reject or block, regardless of executor trust
tier, configuration, or a human "approve" verdict. These are the **system-deny**
conditions. Everything else is ordinary policy that a human may override.

## System-deny conditions

| DenyCode | Where enforced | Description |
|----------|----------------|-------------|
| `secret_sweep` | Execute (pre-persist) + Apply (re-sweep) | A real credential value — a known key/token format (`security.FindCredential`), or one of the project's own resolved secret values — appears in produced file content. The content is never written to the redaction-free store. The raised `Finding` names `path:line` and the kind of secret (e.g. "src/keys.js:14: looks like an OpenAI API key"), never the value. **Conditionally overridable** (review changes #1, ICP round): a human may disposition the raised finding `not_a_secret`, which allowlists its fingerprint — the sha256 of the matched value's capture group, never the value — for the project, with the actor and time recorded. The sweep ignores an allowlisted fingerprint from then on, at both Execute and Apply. The override is scoped to a pattern-shaped match; a value drawn from the project's own configured secrets is never eligible for it. |
| `pii` | **declared, never produced** | Reserved for a hard-rejected PII signal. A bare vocabulary marker (below) no longer hard-rejects anything, so no code path emits this today. |
| `path_escape` | **declared, never produced** | Reserved for a diff path escaping the workspace. The path guard rejects that case, but it returns its own error rather than this code, so no `Finding` ever carries it. |
| `host_exec` | checkCommand | Build/test runs only inside the container sandbox; no `check.image` or an unavailable sandbox fails closed. |
| `blob_digest_mismatch` | blob.Store.Get / checkPatchApplies | Stored content hash ≠ requested digest. |
| `trust_deny` | **declared, never produced** | Reserved for the engine-assigned trust tier refusing a required action. No code path emits it today. |
| `review_open_blocking` | ReviewChangeSet (approve) | An undisposed critical/high finding targets the change set; approve is denied. **Conditionally overridable — one of the two conditions above** (with `secret_sweep`'s `not_a_secret` disposition). `idea review <CHG-n> approve --accept-findings` signs off findings that carry no deny code — the adversarial reviewer's own judgment, which a human is entitled to overrule, as they already can on the PRD (`idea approve`) and the plan (`idea approve-plan`). A finding carrying any of the codes above, or one already dispositioned (a failing build check is `rejected`), is left untouched and still denies. The gate is re-run after the sign-off, so what the accept declined to touch still blocks. |

Each deny produces either a persisted `Finding` (with a machine-readable
`Finding.Code`) or a fail-closed error mapped to the same `DenyCode` — except the
three marked **declared, never produced**, which exist in the `DenyCode` enum and
in `Valid()` but have no producer. They are listed rather than deleted because a
client switching on `DenyCode` should know the full set it may one day see; do
not treat any of them as evidence that the condition is enforced.

### Vocabulary markers are advisory, not a deny code

`internal/risk.RiskSignals` scans produced content for whole-word markers —
`auth`, `secret`, `token`, `password`, `api_key`, network URLs, `payment`,
`credit card`, `ssn` — and used to hard-reject a change set on a bare word
match, with no file or line and no override (the ICP-round finding this fixed:
a patch that legitimately mentioned "per-user API keys with rate limiting"
could never get past it). It no longer blocks anything: a detected marker now
raises a non-blocking `sensitive_area` finding (severity `medium`, no
`Finding.Code`) so the change set still gets a closer look in review, exactly
as before, but a human decides instead of a word scan. Only a real credential
value — `secret_sweep` above — still blocks.

## Engine-assigned trust tiers

A *trust tier* rates what an executor may do, and is distinct from the *trust levels* in
[`security.md`](security.md), which rate where content came from. Both names match the code.

The engine assigns a trust tier from the selected executor's kind — **never**
from the executor's self-declared capabilities:

| kind | tier | read_file | write_file | run_command |
|------|------|-----------|------------|-------------|
| internal | `internal` | allow | allow | allow |
| container | `local_external` | allow | allow | deny |
| omp | `local_external` | allow | allow | deny |
| hermes | `local_external` | allow | allow | deny |
| unknown | `remote_external` | deny | deny | deny (fail-closed) |

Note: `run_command` on the host is separately gated by the deterministic-check
sandbox (`check.image` + `ContainerSandbox`), not by the trust tier.

<!-- claim:executor-isolation -->
## Execution gateway

There is no single "the executor sandbox". Four executor kinds are selectable via
`harness.executor.kind`, and they do not share an isolation posture. Every row
below is the actual `docker run` argv, not a summary of intent.

| kind | type | network | workspace mount | provider key inside |
|---|---|---|---|---|
| `internal` (default) | `InternalExecutor` | n/a — **no container at all** | runs in the daemon process | n/a |
| `container` | `ContainerExecutor` | `--network none` | `/work` | no |
| `omp`, `hermes` | `SandboxedAgentExecutor` | **unrestricted** | `/work`, **read-write** | **yes**, via `--env-file` |
| (deterministic checks) | `ContainerSandbox` | `--network none` | `/work` + `/repo:ro` | no |

**`ContainerSandbox` is not the executor.** It has exactly one non-test caller —
the deterministic build/test check in `internal/workflow/execute.go`. Its
`--network none` and read-only `/repo` mount are real, and they apply to
`run_command`, not to the executors that run model-generated code.

**Coding agents (`omp`, `hermes`) get filesystem and process isolation only.**
`--read-only` root, `--cap-drop ALL`, `--security-opt no-new-privileges`,
`--user`, `--memory 2g`, `--pids-limit 256`, a `/tmp` tmpfs. A timeout or cancel
genuinely kills the container. The agent never runs code on the host.

But `internal/executor/agent.go` says the rest plainly, and this page previously
did not:

> NOT network isolation: the agent needs egress to reach its model API, so the
> provider keys are forwarded in and `--network none` is deliberately omitted.

So for `omp` and `hermes` specifically:

- **Network egress is unrestricted.** There is no `--network` flag, no
  destination allowlist and no proxy — the container reaches anything the host
  can reach, not only the model provider.
- **The provider API key is inside the container**, written to a 0600 env-file
  and passed with `--env-file`.
- **The workspace is mounted read-write** at `/work`. It is not the read-only
  `/repo` mount described above; that belongs to `ContainerSandbox`.
- **A second read-write mount holds the Hermes agent's home**, at
  `/tmp/agent-home`. It is a fresh directory per attempt, created next to the
  work root. idea-harness writes one file into it, the generated agent config:
  provider name, base URL, model, and the *name* of the key variable, never the
  key itself. The agent then writes its own state and the image's bundled skills
  there. It exists because the image's own home is not writable under
  `--read-only` with a host `--user`, and the agent cannot start without one.
  OMP gets no such mount and keeps its image's home.
- **The container is not started with `--rm`**, because a fast agent deleted
  itself before its status was read. The container keeps the forwarded key in
  its configuration until it is removed, visible to anyone who can run
  `docker inspect`. idea-harness removes the container and the agent home when
  the attempt is collected or cancelled, and when `docker run` fails after
  creating the container. `idea daemon stop` cancels running attempts and
  waits up to 30 seconds for their containers to be removed. A daemon that is
  killed or crashes mid-run leaves both behind; the next daemon start removes
  them before it starts any run, finding them by the agent home and by
  container label. If Docker cannot be reached at that moment, the record is
  kept and the removal is retried at the following start. The `idea executors setup --smoke` container
  is removed on Ctrl-C, but not if the CLI itself is killed.
- **A loopback provider gets a route to the host.** When the configured base URL
  is a loopback host (`localhost`, `127.0.0.0/8` or `[::1]`), which is Ollama by
  default, the container gets
  `--add-host host.docker.internal:host-gateway` so it can reach that service.
  On Linux this is a route the agent did not otherwise have; on Docker Desktop
  the name already exists. No other provider gets it.

<!-- /claim -->

<!-- claim:redaction -->
## What this does not protect against

Kept as a section so the negative space has somewhere to live. The failure this
page had was an unscoped generalisation, not a typo.

- **A compromised or prompt-injected coding agent** can read anything under
  `/work`, write to it, use the forwarded provider key, and send both anywhere on
  the network. Filesystem isolation bounds *what it can reach*; nothing bounds
  *what it can send*.
- **With a loopback provider, "anywhere" includes the host.** An agent pointed at
  Ollama can open connections to other services on the host through
  `host.docker.internal`, not only to Ollama. The daemon's own API requires a
  token the container never sees, but anything on the host that trusts local
  connections without authentication is reachable.
- **The raw blob store is deliberately redaction-free.** For *that* store — the
  one holding file content and uploaded bytes, which must round-trip
  byte-for-byte — redaction happens at the read/display surface rather than at
  write time: `RenderPatch` and the structured diff redact what they return, and
  the bytes underneath stay as written.

  This is not the rule for the whole system, and an earlier version of this
  sentence read as if it were. The *other* store, `blobs/`, is constructed with
  the redactor as its write hook (`blob.New(..., redactor.Redact)` in
  `internal/daemon/runtime.go`), so model artifacts and context bundles are
  redacted on the way in. Two stores, two rules.

  This page is the canonical statement of both; `docs/api.md` and
  `docs/openapi.yaml` link here rather than restating them.
- **Uploaded images skip redaction entirely**: stored verbatim, served verbatim,
  and sent unredacted to the vision provider. Text documents are swept; images
  are not.
- **`--user` passes the daemon's own UID/GID.** If the daemon runs as root, the
  container runs as root inside its capability-dropped, read-only namespace.
  Nothing refuses to start or warns.
- **The agent image is pinned by tag, not digest**, and no signature is verified.
  A re-pushed tag is not detected. `omp` has no default image at all, so the
  operator supplies one.
- **The control-plane deny covers `.idea-harness/` and `.git/` only.** A change
  set may touch `.github/workflows/`, `.envrc` or similar auto-executing files;
  the defence there is the human review gate, not the deny.
- **Daemon logs are not redacted**, and `GET /api/v1/logs` returns them.

## Defense in depth

The system-deny decision is an **additive aggregator**, not a replacement. The
deep enforcers remain independent: `security.ResolveWithin` (path containment),
`blob.Store.Get` (digest verification), and the redactor sweep all still run at
their own layer; a regression in one does not disable the others.
<!-- /claim -->
