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
| `secret_sweep` | Execute (pre-persist) + Apply (re-sweep) + the deterministic-check output sweep (review M2) | A real credential value — a known key/token format (`security.FindCredential`), or one of the project's own resolved secret values — appears in produced file content, or in the raw output of a build/test check. The content is never written to the redaction-free store. The raised `Finding` names `path:line` and the kind of secret (e.g. "src/keys.js:14: looks like an OpenAI API key"), or `"in the output of <check>"` when found in check output, never the value; `Finding.Evidence` carries a masked preview of the source line instead (see below). **Conditionally overridable** (review changes #1, ICP round; hardened by review H3): a human may disposition the raised finding `not_a_secret`, which allowlists its fingerprint scoped to the path it was found at — never the value — for the project, with the actor and time recorded. The sweep ignores an allowlisted (fingerprint, path) pair from then on, at Execute, Apply, and the check-output sweep. The override is scoped to a pattern-shaped match; a value drawn from the project's own configured secrets is never eligible for it, and the store refuses the disposition outright (not merely re-denying it on the next sweep) when `Finding.SecretOverridable` is `false`. |
| `pii` | **declared, never produced** | Reserved for a hard-rejected PII signal. A bare vocabulary marker (below) no longer hard-rejects anything, so no code path emits this today. |
| `path_escape` | **declared, never produced** | Reserved for a diff path escaping the workspace. The path guard rejects that case, but it returns its own error rather than this code, so no `Finding` ever carries it. |
| `host_exec` | checkCommand | Build/test runs only inside the container sandbox; no `check.image` or an unavailable sandbox fails closed. |
| `blob_digest_mismatch` | blob.Store.Get / checkPatchApplies | Stored content hash ≠ requested digest. |
| `trust_deny` | **declared, never produced** | Reserved for the engine-assigned trust tier refusing a required action. No code path emits it today. |
| `review_open_blocking` | ReviewChangeSet (approve) | An undisposed critical/high finding targets the change set; approve is denied. **Conditionally overridable — one of the two conditions above** (with `secret_sweep`'s `not_a_secret` disposition). `idea review <CHG-n> approve --accept-findings` signs off findings that carry no deny code — the adversarial reviewer's own judgment, which a human is entitled to overrule, as they already can on the PRD (`idea approve`) and the plan (`idea approve-plan`). A finding carrying any of the codes above is left untouched and still denies, whatever disposition it carries. A finding with NO deny code (a reviewer's own judgment call) resolves the block the same way whether a human disposition it `fixed`, `accepted_risk`, or `rejected` (`Finding.ResolvesBlocking`, ICP re-run defect #2) — `rejected` there means "the human judged this finding wrong," not a system deny, and used to deadlock approval permanently (`--accept-findings` kept failing and recommending itself). It is unaffected for a SYSTEM-set `rejected` — a failing build/test check, or a rejected change set's own findings closed via `rejected` (review M5) — which keeps blocking exactly as before: only fixing the underlying problem clears those. The gate is re-run after the sign-off, so what the accept declined to touch still blocks. |

Each deny produces either a persisted `Finding` (with a machine-readable
`Finding.Code`) or a fail-closed error mapped to the same `DenyCode` — except the
three marked **declared, never produced**, which exist in the `DenyCode` enum and
in `Valid()` but have no producer. They are listed rather than deleted because a
client switching on `DenyCode` should know the full set it may one day see; do
not treat any of them as evidence that the condition is enforced.

### Vocabulary markers are advisory, not a deny code

`internal/risk.RiskSignals` scans produced content for whole-word markers — `auth`, `secret`,
`token`, `password` (review low-priority item narrowed the catalog to these four; see below) —
and used to hard-reject a change set on a bare word match, with no file or line and no override
(the ICP-round finding this fixed: a patch that legitimately mentioned "per-user API keys with
rate limiting" could never get past it). It no longer blocks anything: a detected marker now
raises **at most one** non-blocking `sensitive_area` finding per change set (severity `medium`, no
`Finding.Code`) so the change set still gets a closer look in review, exactly as before, but a
human decides instead of a word scan. Only a real credential value — `secret_sweep` above — still
blocks.

The catalog used to also include `api_key`, network URLs (`https://`/`http://`), `payment`,
`credit card` and `ssn`, every one of which turned out to be noise for what this scan actually
gates: a change set that merely linked to a website matched `https://` and was flagged as a
"sensitive area" with nothing sensitive in it, and `payment`/`credit card`/`ssn` fired on ordinary
product copy discussing a billing feature as often as on code that actually handles the data.
`api_key` was dropped alongside them — it overlaps `password`/`token`/`secret` in intent, and the
catalog that actually detects credential *values* is `internal/security`'s pattern catalog above,
not this vocabulary scan. `auth`/`secret`/`token`/`password` are kept because they at least
correlate with code that plausibly touches credentials or an auth boundary, which is the judgment
call this scan exists to prompt. This also means the initiative-level risk tier (`idea risk`) can
no longer be floored to `medium` by a payment/PII word alone, or to `high` by a bare URL — an
accepted trade-off of the same tuning, not a separate decision.

### Credential-assignment pattern coverage (review M3, H5)

Alongside the vendor-specific key formats (`AKIA…`, `sk-…`, `ghp_…`, …),
`internal/security.credentialCatalog` includes a generic "credential assignment" rule for values
that carry no distinctive prefix, keyed on the surrounding variable/field name instead:
`(?i)[A-Za-z0-9_]*(?:api[_-]?key|secret(?:[_-]?(?:key|value))?|passw(?:or)?d|pwd|token)["']?\s*[:=]\s*(?:"(...)"|'(...)'|(...))`.
It matches a JSON key with a quote before the colon (`{"password": "..."}`), a SCREAMING_SNAKE_CASE
env-var assignment (`DB_PASSWORD=...`), a snake_case field (`client_secret: ...`), a `pwd` keyword,
and a `secret_key`/`secret_value`-style suffix (`SECRET_KEY = "..."`) — none of the last three
matched an earlier version of this rule. A separate pattern matches URL userinfo credentials
(`scheme://user:PASSWORD@host`, e.g. `postgres://admin:hunter2345@db`). A dedicated rule covers
`AWS_SECRET_ACCESS_KEY=...` separately, since `ACCESS_KEY` (not one of the generic rule's keywords)
sits between `SECRET` and the separator.

The value itself must look like a literal, not an identifier, a member-access expression, or a
function call. A quoted value counts (almost anything 8+ characters, so long as it does not start
with `$` — `"${API_KEY}"` is a reference, not a literal); RE2 (Go's regexp engine) has no
lookahead, so an unquoted value additionally goes through a Go-level check
(`validCredentialAssignmentValue`) after the regex match, rejecting it when it:

- starts with `$` (`$GITHUB_TOKEN_VALUE`);
- is immediately followed by a call's `(` (`generate_csrf_token()`, `getToken()`);
- reads as a bare camelCase/snake_case name with no digit (`authToken`, `token_from_somewhere`); or
- reads as a member-access chain — every dot-segment itself identifier-shaped
  (`req.headers.authorization`, `settings.API_KEY_FROM_ENV`).

One accepted trade-off: a dotted literal whose every segment is a plain identifier word with no
digit (e.g. a placeholder `xyz.abc.def.ghi`) is indistinguishable, by shape alone, from a genuine
member-access expression, and is treated as the latter (not flagged) rather than risk a false
positive on real code like `req.headers.authorization`.

### Placeholder/low-entropy values and test-fixture paths (ICP re-run defect #1)

A founder-journey run on an API-key-auth-with-rate-limiting feature hit three shapes of false
positive the sweep did not distinguish from a real secret:

1. The generic "credential assignment" rule matched a `"secret": "..."` field in a `*_test.go`
   table-driven fixture, and a doc comment naming a header's shape (`// X-API-Key: <id>:<secret>`).
2. Three example keys shaped like a Stripe secret key, with obviously-fake bodies
   (`sk_live_...0000`, `...1111`, `...2222`), sat in `config.json` as documentation.
3. `not_a_secret` only allowlists the exact (fingerprint, path) a human already saw, so it could
   not help the *next* file or execute that hit the same class of false positive.

Three independent mechanisms now apply, all in `internal/security` (`redact.go`):

- **Placeholder/low-entropy shape (`isPlaceholderShape`, `validFlatTokenValue`).** A captured
  value with a run of 4+ identical characters (`sk_live_...0000`), or whose Shannon entropy per
  character falls under 2.5 bits (a repetitive or low-variety body), is never a credential at
  all — not even advisory — for EVERY provider-specific pattern in the catalog (AWS, GitHub,
  GitLab, Google, npm, OpenAI/Anthropic, Stripe, Slack) as well as the generic assignment rule.
  Values over 200 characters (a PEM block, a JWT) are exempt: those are already highly specific
  formats where a repeated-character run or a raw entropy score is not a meaningful signal.
- **Placeholder vocabulary and sequential fill (`containsPlaceholderWord`, `isSequentialFill`,
  `validGenericAssignment`).** Scoped to the GENERIC assignment rule only, never a
  provider-specific format: a value containing `example`/`test`/`dummy`/`fake`/`placeholder`/
  `changeme`/`xxx` (case-insensitive), or one that IS essentially a single long ascending/
  descending run start to end (`abcdefghijklmnop`, not a short embedded fragment like
  `django-insecure-abcdefgh`), is not a credential. Scoped this narrowly because a real,
  documented example key can legitimately carry one of these words or a short sequential
  fragment — AWS's own `AKIAIOSFODNN7EXAMPLE` and
  `wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY` docs literals, and hand-written realistic-looking
  test fixtures using `0123456789`/the alphabet as filler — and those must keep blocking.
- **Test/fixture paths (`isTestFixturePath`, `CredentialFinding.Advisory`).** A GENERIC
  assignment match (never a provider-specific format, never a configured secret value) found in
  a `*_test.go`, `.spec.`/`.test.` file, or a path under `test/`, `tests/`, `testdata/`,
  `fixtures/`, or `__tests__/` is downgraded to **advisory**: recorded as a real finding, visible
  to the reviewer, but `Finding.Advisory` means `review.EvaluateApproval` and
  `denyReviewOpenBlocking` skip it outright regardless of severity (§11/§15) — it never blocks a
  change set on its own. A provider-specific format or a configured secret value keeps blocking
  in a test path exactly as anywhere else.
- **Comment/template placeholders (`looksLikeTemplatePlaceholder`).** A value that is (or
  contains) `<...>`, or contains `${`/`{{`, never matches the generic assignment rule at all —
  `<secret>`, `<id>:<secret>`, `"${API_KEY}"`, `"{{ vault_secret }}"`.

None of this weakens detection of a real secret: `not_a_secret`, its HMAC fingerprinting, and its
project-scoped allowlist (below) are unchanged, and every provider-specific format with realistic
entropy still blocks unconditionally, test path or not.

### The secret allowlist (review H3/M1)

A `secret_sweep` finding's `not_a_secret` disposition writes one row to a project-scoped
allowlist (`secret_allowlist`, exposed read-only via `GET /api/v1/projects/{id}/secret-allowlist`).
Several properties hold regardless of how the disposition is invoked:

- **A configured secret can never be allowlisted.** Every finding carries `SecretOverridable`
  (`secret_overridable` in the API): `true` for an ordinary pattern-shaped match (might be a
  false positive), `false` for a value drawn from the project's own resolved secrets. The store's
  `SetFindingDisposition` refuses `not_a_secret` outright when `SecretOverridable` is `false` — it
  does not merely record the disposition and let the sweep silently re-deny the value later.
- **Scoped to fingerprint *and* path.** An allowlist entry only suppresses the identical
  fingerprint at the identical path. A value allowlisted in `tests/fixture.js` still blocks the
  same value in `src/prod.js`.
- **A known-value match always wins over a pattern match, even at the same fingerprint.** If a
  configured secret's value happens to also match a pattern rule, the pattern-shaped occurrence's
  fingerprint is identical (fingerprinting is a function of the value alone) — but the known-value
  finding's `SecretOverridable = false` means allowlisting the pattern-shaped finding's fingerprint
  can never suppress the known-value finding at that same fingerprint+path. `filterAllowlisted`
  only ever drops a match where `Overridable` is true.
- **The fingerprint is a per-project HMAC, not a bare sha256.** `internal/security.Fingerprint`
  previously hashed the matched value alone, so the same secret fingerprinted identically on every
  installation that ever scanned it — a cross-project correlation risk for a value meant to be
  secret. Each project now generates and persists a random 32-byte key
  (`store.DB.SecretFingerprintKey`, stored in `project_state`) the first time it is needed, and
  every fingerprint this project's sweep computes is `HMAC-SHA256(key, value)`. The allowlist
  listing API still shows only a short prefix of the resulting digest, never the full value.
  **Scope of what this protects, precisely:** the HMAC key is stored in plaintext, in the same
  `state.db` as the fingerprints it keys — it is not a secret the sweep is protected against an
  attacker who already has read access to that database, and it defends only against a *different*
  threat: a fingerprint from this project being recognizably the same value as a fingerprint from
  a completely different installation's project (the bare-sha256 cross-project correlation
  problem above). There is no key rotation: rotating the key would invalidate every existing
  allowlist entry's fingerprint (a fingerprint computed under the old key can never match one
  computed under a new key for the same value), so the key is generated once and kept for the
  life of the project's `state.db`.
  **Migration note:** upgrading past migration 30 switches every NEW fingerprint to this
  per-project HMAC, but existing `secret_allowlist` rows recorded under the old bare-sha256 scheme
  are **not**, and cannot be, recomputed — there is no way to derive an HMAC from a stored sha256
  digest without the original secret value. Every "not a secret" exemption granted before that
  upgrade is silently orphaned (its fingerprint never matches a newly-computed one again) and must
  be re-granted; see `CHANGELOG.md`.
- **Findings never carry the raw value, but do carry a masked preview.** `Finding.SecretPreview`
  (`secret_preview`) reveals at most `min(4, len/4)` characters of the value on each side, with the
  middle collapsed to a single ellipsis (e.g. `sk-p…Xy9z` for a long key) — and nothing at all for a
  value under 16 characters, which is masked entirely. `Finding.Evidence` carries the full source
  line with **every** credential match on that line replaced by its own such mask (not just the
  finding's own value), and is itself redacted again before being persisted, so a reviewer can see
  roughly where and what without the secret — or a second, different secret on the same line — ever
  being persisted or returned in full.
- **A renamed file's allowlist entry does not follow it.** `fsdiff` expresses a rename as a delete
  of the old path plus an add of the new one, so an allowlist entry keyed on `(fingerprint, path)`
  for the old path does not cover the new one — a renamed, previously-allowlisted fixture blocks
  again and must be re-allowlisted at its new path. This fails closed (a rename can't be used to
  smuggle an allowlisted value to an unreviewed location) and is accepted as-is, not a defect.
- **One entry per (fingerprint, path).** `secret_allowlist` has a unique index on
  `(fingerprint, path)`; a repeat disposition upserts rather than duplicating a row, and revoking
  an entry deletes by that same pair (cleaning up any pre-existing duplicate too). Changing a
  finding's disposition away from `not_a_secret` removes the entry it created, so the sweep denies
  that value again.

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
