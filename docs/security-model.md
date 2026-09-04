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
| `secret_sweep` | Execute (pre-persist) + Apply (re-sweep) | A known secret value or credential pattern appears in produced file content. The content is never written to the redaction-free store. |
| `pii` | Execute | A blocking PII signal (e.g. SSN) appears in produced file content. |
| `path_escape` | fsdiff Prepare/Commit/Restore | A diff path escapes the workspace (rejected before any write). |
| `host_exec` | checkCommand | Build/test runs only inside the container sandbox; no `check.image` or an unavailable sandbox fails closed. |
| `blob_digest_mismatch` | blob.Store.Get / checkPatchApplies | Stored content hash ≠ requested digest. |
| `trust_deny` | Execute | The engine-assigned trust tier does not authorize a required action. |
| `review_open_blocking` | ReviewChangeSet (approve) | An undisposed critical/high finding targets the change set; approve is denied. |

Each deny produces either a persisted `Finding` (with a machine-readable
`Finding.Code`) or a fail-closed error mapped to the same `DenyCode`.

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

## What this does not protect against

Kept as a section so the negative space has somewhere to live. The failure this
page had was an unscoped generalisation, not a typo.

- **A compromised or prompt-injected coding agent** can read anything under
  `/work`, write to it, use the forwarded provider key, and send both anywhere on
  the network. Filesystem isolation bounds *what it can reach*; nothing bounds
  *what it can send*.
- **The raw blob store is deliberately redaction-free.** Redaction happens at the
  read/display surface, never at write time: `RenderPatch` and the structured
  diff redact what they return, and the bytes underneath stay as written. This
  page is the canonical statement of that property; `docs/api.md` and
  `docs/openapi.yaml` link here rather than restating it.
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
