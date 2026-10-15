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
| `pii` | Execute (produced content), blocking at ReviewChangeSet (approve) | A payment card number or a US Social Security number appears in produced file content (`security.FindPIIInFile`). Card numbers need a known issuer prefix at that issuer's length, the issuer's own grouping or none, and a valid Luhn checksum, and the issuers' published test numbers are excluded; SSNs must be dashed and inside the ranges ever issued, and the published example numbers are excluded. Email addresses and phone numbers are **not** detected — they are in most codebases' ordinary content, and a finding on every change set trains a reviewer to dismiss it. One finding per match names `path:line` and the kind, with a masked preview (a card keeps its last four digits; an SSN keeps nothing). It does not reject the change set, because the detector is a checksum and a range rule, not proof: it blocks approval. **Conditionally overridable:** `--accept-findings` never signs it off; a human dispositions that one finding `accepted_risk` ("this is synthetic data") after reading it. In a test/fixture path (the same rule as the credential sweep's generic patterns) the finding is advisory instead: shown, never blocking, and carrying no code. The raw bytes are persisted like any other change-set content (see the raw blob store below). |
| `path_escape` | ApplyChangeSet (before any write) | A change's path is absolute, not a clean relative path (`..`, `a//b`), or — against the workspace as it is at apply time — resolves outside the workspace or into `.git/`/`.idea-harness/` through a symlink (`fsdiff.Escapes`). A path cannot be made to escape at Execute: the orchestrator's snapshot never follows symlinks. What can change is the workspace by apply time, which is where this runs — before the change set is marked `applying` and before the stale-base check, so nothing is written. One finding per offending path; the change set is rejected and its task reopened, as for an apply-time secret, and the error is a `workflow.SystemDenyError` carrying the code. `security.ResolveWithin` still guards every `Prepare`/`Commit`/`Restore` write independently. |
| `host_exec` | checkCommand | Build/test runs only inside the container sandbox; no `check.image` or an unavailable sandbox fails closed. |
| `blob_digest_mismatch` | blob.Store.Get / checkPatchApplies | Stored content hash ≠ requested digest. |
| `trust_deny` | Execute and ExecutePrecondition | The selected executor's engine-assigned trust tier (below) does not allow `write_file`. Refused before any spend with a `workflow.SystemDenyError` carrying the code. Every kind `selectExecutor` can build today maps to a tier that allows it, so no shipped configuration trips this: it is the fail-closed backstop for an executor kind added without a trust-tier mapping, which gets no tier at all (`executor.TrustUnassigned`) and may do nothing. |
| `review_open_blocking` | ReviewChangeSet (approve) | An undisposed critical/high finding targets the change set; approve is denied. The adversarial review that raises these findings runs exactly once, inside `Execute` right after checks pass (ICP re-run defect A) — not again on `approve` — so a plain `approve` only ever evaluates findings already on the change set plus whatever a human has since dispositioned; it makes no model call. **Conditionally overridable — one of the two conditions above** (with `secret_sweep`'s `not_a_secret` disposition). `idea review <CHG-n> approve --accept-findings` signs off findings that carry no deny code — the adversarial reviewer's own judgment, which a human is entitled to overrule, as they already can on the PRD (`idea approve`) and the plan (`idea approve-plan`). A finding carrying any of the codes above is left untouched and still denies. For every code but `pii` the change set carrying it was already rejected, so no disposition reopens it; a `pii` finding blocks a change set that is still awaiting review, and only that finding's own human `accepted_risk` (or `fixed`) disposition clears it. A finding with NO deny code (a reviewer's own judgment call) resolves the block the same way whether a human disposition it `fixed`, `accepted_risk`, or `rejected` (`Finding.ResolvesBlocking`, ICP re-run defect #2) — `rejected` there means "the human judged this finding wrong," not a system deny, and used to deadlock approval permanently (`--accept-findings` kept failing and recommending itself). It is unaffected for a SYSTEM-set `rejected` — a failing build/test check, or a rejected change set's own findings closed via `rejected` (review M5) — which keeps blocking exactly as before: only fixing the underlying problem clears those. The gate is re-run after the sign-off, so what the accept declined to touch still blocks. |

Each deny produces either a persisted `Finding` (with a machine-readable
`Finding.Code`) or a fail-closed error mapped to the same `DenyCode`. Until the
release after 0.6.0, `pii`, `path_escape` and `trust_deny` were declared, never
produced — present in the `DenyCode` enum with no code path emitting them. Each
now has the producer named in its row; none was deleted, because each has a
condition it can soundly enforce.

### Protected paths: auto-executing files need an explicit sign-off

Separate from the control-plane deny (`.idea-harness/` and `.git/` never enter a
change set at all), a change set that adds, edits or deletes a file that **runs
without anyone invoking it** gets one blocking `protected_path` finding at
Execute (`fsdiff.AutoExecutingPath`; severity `high`, no deny code). The list:
CI definitions (`.github/workflows/`, `.github/actions/`, `.gitlab-ci.yml`,
`.circleci/`, `Jenkinsfile`, `.travis.yml`, `azure-pipelines.yml`,
`bitbucket-pipelines.yml`, `.buildkite/`, `.drone.yml`), git hooks installed
from the tree (`.husky/`, `.githooks/`, `.pre-commit-config.yaml`,
`lefthook.yml`), and files the developer's tools run on entry (`.envrc`,
`.devcontainer/`, `.vscode/tasks.json`). Matched case-insensitively at the
workspace root.

The false-positive policy is that this is **not a deny**: an initiative may
legitimately be about CI. A plain `approve` is refused, and the human signs it
off explicitly — `idea review <CHG-n> approve --accept-findings` records
`accepted_risk` against it under the human actor, or the finding is
dispositioned on its own. What it removes is the silent path: a workflow edit
riding along in a change set about something else.

Deliberately not on the list: `Makefile`, `package.json` (npm lifecycle
scripts), `setup.py`, `build.gradle`, `Dockerfile` and other build definitions.
They run code too, but only when someone builds, and a large share of ordinary
tasks edit them; a finding on every one would teach the reviewer to dismiss it.

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
sits between `SECRET` and the separator. Like the generic rule, it accepts an optional `["']?`
right after the keyword (ICP re-run defect #4: without it, a JSON key's own closing quote —
`"aws_secret_access_key": "..."` — sat exactly where the pattern required `\s*[:=]` alone, so a
real founder-journey run's `config/deploy.json` produced no finding at all for the secret key,
while the `aws_access_key_id` line right above it fired, because that pattern matches the value's
own `AKIA…` shape rather than the key name).

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

**Review C3 revised this design after it was found to weaken redaction itself, not just
detection**, and to be unreliable when applied to a provider-prefixed format. Two changes:

1. **Redaction and finding-generation are now two separate gates.** `RedactString` — the blob
   store's own redactor, reached by every persisted model artifact, finding, and agent output —
   masks every raw regex match unconditionally (aside from the generic assignment rule's own
   narrow structural check, below). An earlier version filtered `RedactString` through the SAME
   placeholder/low-entropy gate used to decide whether to raise a finding, which meant a real
   secret that merely *looked* like a placeholder (a Slack token whose numeric ID segments
   produced a 4-digit run by chance, `DB_PASSWORD=latestProdPw9xK2` — "latest" contains "test")
   was never masked at all, not silently downgraded: it passed through in the clear.
2. **The placeholder/low-entropy shape check no longer applies to any provider-specific
   format.** A Monte-Carlo run over 200k random well-formed keys per format found it silently
   missed a REAL secret 1 in 68 times for Slack's `xoxb-` shape (long numeric ID segments make a
   4-digit run likely by chance), 1 in 3,200 for AWS, and 1 in 6,100–9,500 for the rest — a
   run-length or entropy heuristic is not reliable evidence on data this short, and the vendor
   prefix is already the evidence a match is real. A provider-specific match (AWS, GitHub,
   GitLab, Google, npm, OpenAI/Anthropic, Stripe, Slack) now always raises a finding
   (`Overridable: true` — a human can still disposition an individual match "not a secret").

What remains, all in `internal/security` (`redact.go`):

- **Placeholder/low-entropy shape (`isPlaceholderShape`) and vocabulary/sequential fill
  (`containsPlaceholderWord`, `isSequentialFill`, `validGenericAssignment`, `isLikelyPlaceholderValue`).**
  Scoped to GENERIC patterns only — ones with no vendor prefix of their own to serve as evidence:
  the credential-assignment rule, plus (review H11) the bearer-token and URL-userinfo-credential
  rules. A captured value with a run of 4+ identical characters, overall Shannon entropy under
  2.5 bits/char, a value containing the WHOLE WORD (never a substring — see below)
  `example`/`test`/`dummy`/`fake`/`placeholder`/`changeme`/`xxx` (case-insensitive), or one that
  IS essentially a single long ascending/descending run start to end, is not a credential for
  FINDING purposes — but this decides only whether a *finding* is raised, never whether
  `RedactString` masks it.
  `containsPlaceholderWord` compares whole word/segments (splitting on non-letters and camelCase
  transitions), not substrings: an earlier version's bare `strings.Contains` let `"test"`
  silently suppress `latest`/`contest`/`greatest`/`attestation`/`protest`, and `"xxx"` suppress
  any value merely containing it anywhere.
  `isPlaceholderShape` itself (review LOW #13) only ever applies to a captured value of 200
  characters or fewer (`maxPlaceholderShapeLen`) — a short, flat token is exactly the shape a
  repeated-character run or low aggregate entropy is meaningful evidence for. A value LONGER than
  200 characters (a PEM block, a JWT) is exempt from this check entirely, never the reverse: it is
  never *treated* as placeholder-shaped just for being long, it simply never asks the question at
  all, since a repeated-character run or an aggregate entropy score is not a meaningful signal one
  way or the other on a long, multi-part match.
- **Test/fixture paths (`isTestFixturePath`, `CredentialFinding.Advisory`).** A GENERIC pattern
  match (credential assignment, bearer token, or URL userinfo credential — never a
  provider-specific format, never a configured secret value) found in a `*_test.go`,
  `.spec.`/`.test.` file, or a path under `test/`, `tests/`, `testdata/`, `fixtures/`, or
  `__tests__/` is downgraded to **advisory**: recorded as a real finding, visible to the
  reviewer, but `Finding.Advisory` means `review.EvaluateApproval` and `denyReviewOpenBlocking`
  skip it outright regardless of severity (§11/§15) — it never blocks a change set on its own. A
  provider-specific format or a configured secret value keeps blocking in a test path exactly as
  anywhere else. Review H11 extended this from the generic assignment rule alone to bearer token
  and URL userinfo credential, which previously had no test-path (or placeholder, or template)
  gate at all: `Bearer YOUR_TOKEN_HERE_XXXXXXXXXX` and `mysql://root:examplepass@127.0.0.1/db`
  raised a critical BLOCKING finding even inside a `*_test.go` file.
- **Comment/template placeholders (`looksLikeTemplatePlaceholder`).** A value that is (or
  contains) `<...>`, or contains `${`/`{{`, never matches a GENERIC pattern at all — `<secret>`,
  `<id>:<secret>`, `"${API_KEY}"`, `"{{ vault_secret }}"` — for finding generation. The generic
  assignment rule alone also excludes it from REDACTION (`redactGate`,
  `validCredentialAssignmentValue`): a template placeholder, a variable/property reference, or a
  function call is not credential-shaped at all, so masking it would mangle ordinary code or
  prose for no security benefit — a narrower, structural-only check, never the placeholder-word/
  entropy checks above.

None of this weakens detection of a real secret: `not_a_secret`, its HMAC fingerprinting, and its
project-scoped allowlist (below) are unchanged, every provider-specific format now always raises
a finding regardless of shape, and `RedactString` masks every catalog match unconditionally
except the generic assignment rule's own narrow structural exclusion.

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
  (`secret_preview`) reveals nothing at all for a value under 16 characters (masked entirely), at
  most 1 character on each side for a 16–23 character value, and at most `min(4, len/4)`
  characters per side (e.g. `sk-p…Xy9z` for a long key) only from 24 characters up (review LOW).
  The 16-23 character tier was added after review found the previous single `min(4, len/4)`
  formula, applied starting at 16 characters, revealed 50% of a 16-character secret in the clear
  (`Hq7zR2mVx9LpQwEr` → `Hq7z…QwEr`) — and a 16–23 character value (a short password, an API key
  body) is exactly the common case a human is dispositioning here. `Finding.Evidence` carries the
  full source line with **every** credential match on that line replaced by its own such mask
  (not just the finding's own value), and is itself redacted again before being persisted, so a
  reviewer can see roughly where and what without the secret — or a second, different secret on
  the same line — ever being persisted or returned in full.
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
| acp | `local_external` | allow | allow | deny |
| openhands | `remote_external` | allow | allow | deny |
| unknown | none (`TrustUnassigned`) | deny | deny | deny (fail-closed) |

`remote_external` is an agent on another machine. Its read and write are of its
own remote copy of the work root; what it wrote reaches this machine only through
the executor's own download into the local work root, and from there through the
same snapshot, diff, secret sweep and review as any local agent's. An unknown
kind used to land in `remote_external`, which then had to allow nothing; it now
gets no tier, so a real remote kind could be granted read/write without opening
the fail-closed backstop.

Note: `run_command` on the host is separately gated by the deterministic-check
sandbox (`check.image` + `ContainerSandbox`), not by the trust tier.

<!-- claim:executor-isolation -->
## Execution gateway

There is no single "the executor sandbox". Six executor kinds are selectable via
`harness.executor.kind` (plus `auto`, the unset default, which resolves per run to
`hermes` or `internal`), and they do not share an isolation posture. Every row
below is the actual `docker run` argv, not a summary of intent.

| kind | type | network | workspace mount | provider key inside |
|---|---|---|---|---|
| `internal` | `InternalExecutor` | n/a — **no container at all** | runs in the daemon process | n/a |
| `container` | `ContainerExecutor` | `--network none` | `/work` | no |
| `omp`, `hermes` | `SandboxedAgentExecutor` | **unrestricted** | `/work`, **read-write** | **yes**, via `--env-file` |
| `acp` | `ACPExecutor` | **unrestricted** | `/work`, **read-write** | **yes**, via `-e NAME` (value from the docker CLI's environment) |
| `openhands` | `OpenHandsExecutor` | **whatever the Agent Server allows** — no container here | a **copy** of the work root, uploaded to the server | **yes, sent to the server** in the conversation request, unless `withhold_provider_key` |
| (deterministic checks) | `ContainerSandbox` | `--network none` | `/work` + `/repo:ro` | no |
| (verify quality dimensions) | `ContainerSandbox` | `--network none`; **bridge only for `verify.setup.prepare` and scanners marked `network: true`** | `/work` (a fresh copy) | no |
| (outcome metric `command` sources) | `ContainerSandbox` | `--network none` | empty `/work` + `/repo:ro` | no |

**`ContainerSandbox` is not the executor.** It has three non-test callers — the
deterministic build/test check in `internal/workflow/execute.go`; `idea
verify`'s quality dimensions (`internal/workflow/quality_dims.go`), which run the
documented setup/build/test commands, performance budgets and security scanners
against a private copy of the workspace's committable files (no `.env`, no
`.idea-harness/`); and a metric's `command` source in
`internal/workflow/measure.go` (`idea metric measure`, never automatic). Its
`--network none` and read-only `/repo` mount are real, and they apply to those
commands, not to the executors that run model-generated code. A metric command
never falls back to the host: no Docker, no measurement. A metric's `url` source
is not a command at all — the daemon fetches it through the research fetcher's
public-address guard, and only once `outcomes.allow_url_sources` is set.

**The one network exception is opt-in and human-written.** `RunOptions.Network`
swaps `--network none` for Docker's default bridge and keeps every other
restriction (read-only root, dropped capabilities, `no-new-privileges`, the
container user, memory and pid limits). Only two things set it: each command a
human lists under `verify.setup.prepare` in harness.yaml, and each
`verify.security.scanners` entry marked `network: true`. A dependency-fetch
command found in the README is never promoted to it -- it is skipped and
recorded. Be clear about what the opt-in buys: a prepare command such as `npm ci`
runs the project's install hooks, which the agent may have written, with network
egress and the fresh copy of the workspace mounted. That is exfiltration-capable
by construction; list a prepare command only for a project whose dependency
manifests you would install on your own machine.

**Coding agents (`omp`, `hermes`) get filesystem and process isolation only.**
`--read-only` root, `--cap-drop ALL`, `--security-opt no-new-privileges`,
`--user`, `--memory 2g`, `--pids-limit 256`, a `/tmp` tmpfs. A timeout or cancel
genuinely kills the container. The agent never runs code on the host.

**No container runs as root.** Every `docker run` here (agents of every kind, the `container`
executor, `ContainerSandbox`, and the setup probes) passes `--user`, and which user depends on the
daemon (`internal/executor/containeruser.go`):

- **A non-root daemon** passes its own `uid:gid`, so what a container writes into `/work` is the
  daemon's to read, diff and delete.
- **A root daemon** passes the `uid:gid` that owns the project root (it `stat`s it), so a
  container runs as the user whose files these are. A check reads a `0600` file in the read-only
  `/repo` mount as that user would, and nothing a container leaves behind is owned by root. When
  the project root is owned by root, cannot be read, or the container has no project (the setup
  probes), it passes a fixed unprivileged `65532:65532` (the `nonroot` user of distroless images)
  instead. A root group is never passed on: an owner whose gid is `0` runs with gid `65532`.

  The writable mounts — the work root and the agent home — are handed to that user before the run
  (`chown`, symlinks chowned as links and never followed) and handed back once the container has
  been removed: every path to the owner it had, anything the container created to the daemon, so
  the engine's diff, apply and cleanup see ordinary daemon-owned files. A file the ACP client
  writes for the agent is handed over as it is written, so the agent can still edit it. `/repo` is
  never chowned.

  A root daemon killed mid-run cannot hand the mounts back. The next one does, at startup, once
  its sweep has removed every leftover container (`RecoverWorkRoots`): each work root under
  `<state>/projects/*/work/` is chowned back to the daemon wherever a path is owned by someone
  else, and leftover quality-dimension copies (`<state>/projects/*/quality-*`) and metric-command
  scratch roots (`<state>/projects/*/metric-*`) are deleted. Nothing outside those directories is
  touched, and no symlink is followed. If Docker cannot be reached, recovery waits for the next
  start. Metric commands wait for the startup sweep before they run, so recovery never deletes a
  live one's scratch root. Check containers carry the same label as agent containers, so the sweep
  finds one a killed daemon left running.

Refusing to run as root was considered and rejected: root-only CI and Docker-in-Docker setups
would have had no way forward.

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

**`acp` gets the same hardening and the same exposure**, with these differences
(`internal/executor/acp.go`, `ACPExecutor.containerArgs`):

- It runs **attached** (`docker run -i --rm`): the harness speaks JSON-RPC over
  the container's stdin/stdout. `--rm` is safe here because nothing polls a
  finished container. Cancel, timeout and collection still `docker rm -f` it by
  name, and the agent home and container label are the same, so a crashed
  daemon's leftovers are swept the same way.
- The provider key (and any MCP `value_env` value) is passed as `-e NAME` with
  **no value** in the argv; docker reads it from its own process environment.
  It is not written to an env-file. MCP values travel in the `session/new`
  message on the agent's stdin.
- The agent home is mounted as `$HOME` for every ACP agent, holding nothing
  idea-harness wrote.
- The harness is the agent's ACP client and answers on the host: file reads and
  writes are confined to the work root (cleaned and symlink-resolved; anything
  outside is refused), `terminal/*` is not offered, and every permission request
  is auto-answered `allow_once` and recorded on the run manifest. A stdio MCP
  server configured in `executor.mcp_servers` runs inside the agent's container
  with all of the above.

**The default Hermes image is pinned by digest** (`repo:tag@sha256:…`), and the
pulled image's `RepoDigests` must contain that digest or `idea executors setup`
refuses it. That fixes the bytes, not their trustworthiness: there is still no
signature verification.

<!-- /claim -->

<!-- claim:redaction -->
### The remote execution plane (`openhands`)

The `openhands` kind runs no container on this machine: it hands the run to an
OpenHands Agent Server, which is a **trust boundary** — a separate machine, run by
whoever runs it. What leaves this machine, per run:

- **The work root**: the seeded copy of the workspace, which is already stripped
  of `.env` files, files the credential check flags, symbolic links, `.git/` and
  `.idea-harness/` (see `seed.go`), uploaded as one tarball.
- **The implementation brief** for the task.
- **The coding model's provider key**, in the conversation request (`llm.api_key`,
  or a conversation secret for an ACP agent on the server) — unless
  `executor.openhands.withhold_provider_key` is set, in which case the server
  must have its own.
- **The server's session key**, in the `X-Session-API-Key` header and the
  websocket's first frame (never in a URL).

What the harness enforces at that boundary:

- **Transport**: `executor.openhands.url` must be `https` unless the host is
  loopback (`localhost`, `127.0.0.0/8`, `::1` — a server on this machine or an
  SSH tunnel); a plain-http URL to another host, or one carrying credentials, is
  refused when the config loads, by `PUT /executors`, by setup, and again before
  every dispatch.
- **Secrets stay named, not stored**: the session key is read from the variable
  `api_key_env` names (default `OPENHANDS_API_KEY`), from the daemon's
  environment then `.env`, like a provider key. Both keys are removed from
  everything stored or shown from the server's events, which also pass through
  the project's redactor before they are written to `remote_runs.progress`.
- **The way back is untrusted input**: the downloaded tarball is unpacked into a
  staging directory first, entry by entry — absolute paths, `..`, symlinks,
  hard links, devices, `.git/` and `.idea-harness/` entries are skipped, and the
  archive is refused outright past 100,000 files or 1 GiB unpacked — and only
  then mirrored into the work root, never writing through a symlink. A file is
  deleted locally only if it was uploaded and the server no longer has it.
- **The same gates as any agent**: the engine then snapshots the work root and
  computes the diff itself, so the returned files go through the secret sweep
  (`secret_sweep`), the PII and protected-path findings, the deterministic
  checks and review exactly as a local agent's do. The server decides nothing
  about approval or workflow state.

What it does not: anything about the server itself. The Agent Server's own
sandbox, who can read the uploaded work root and the keys on it, how long it
keeps the conversation and its workspace (idea-harness does not delete either
after a run, so they are there for inspection) — those are the server
operator's. `executor.kind: auto` never resolves to `openhands`.

## Remote access

<!-- claim:remote-access -->
The daemon is a loopback service that can, opt-in, be used from another device — a phone
answering the "Needs you" queue, say. This section is the one statement of what that changes.
Setup is in [`configuration.md`](configuration.md#remote-access).

**Loopback is unchanged.** The main listener (`IDEA_HARNESS_ADDR`) must be loopback or the daemon
refuses to start, and a page served to a browser on this machine carries the API token exactly as
before. "On this machine" is decided per request (`isLocalRequest`, `internal/daemon/remote.go`):
the connection arrived on a loopback socket, the `Host` is a loopback name, and no proxy header
(`X-Forwarded-For`, `X-Forwarded-Host`, `X-Forwarded-Proto`, `Forwarded`, `X-Real-IP`,
`Tailscale-User-Login`) is present. Anything else gets the same page with an empty token.

**Two ways in, both opt-in** (`<state root>/config.yaml`, section `daemon:`, or `idea daemon`
flags):

- `allowed_hosts` names hostnames the Host/Origin guard accepts besides loopback — the name a
  TLS-terminating proxy in front of loopback answers to. **`tailscale serve` is the recommended
  path**: HTTPS with a real certificate, reachable only from your tailnet, nothing listening
  beyond loopback.
- `listen` adds a second listener on a non-loopback IP. The policy (`planRemoteListen`) is checked
  before anything binds, and a refused address stops the daemon rather than leaving it quietly
  loopback-only:

  | `daemon.listen` | without `tls_cert`/`tls_key` | with them |
  |---|---|---|
  | a hostname, or no port | refused | refused |
  | loopback | refused (already served) | refused |
  | a Tailscale address (`100.64.0.0/10`, `fd7a:115c:a1e0::/48`) | allowed, warning logged | allowed |
  | a private address (RFC 1918, `fc00::/7`) | allowed, **loud** warning logged | allowed |
  | public, link-local, or every interface (`0.0.0.0`, `::`) | refused | allowed (every interface: warning) |

**A remote browser never sees the token.** It signs in with a **pairing code** that
`idea ui pair` mints — which needs the token, so only someone already holding it on the daemon's
machine can let a device in. A code is 10 characters from a 32-letter alphabet (50 bits), good for
one sign-in within ten minutes, stored as a SHA-256 only, and typed into the page rather than put
in a URL. The master token was considered and rejected as the thing to type
in: it cannot be rotated without re-configuring every CLI and MCP client, and a browser that has
seen it once has it for good.

**Guessing codes.** Wrong codes are counted twice (`internal/daemon/loginlimit.go`):

- **Per client: 10 failures in 15 minutes** refuse *that client* (`429`, with `Retry-After`) for
  the rest of the window. Nobody else is affected and no code is cancelled, so a guesser cannot
  lock the owner out. A success clears the client's count.
- **Globally: 100 failures in 15 minutes**, from any mix of clients, is the backstop: sign-in is
  refused for everyone for the rest of the window and every outstanding code is cancelled.

Why 100 is still safe: a code is 50 bits and lives ten minutes. With *k* codes outstanding each
guess hits one with probability *k*/2⁵⁰; the ceiling caps guessing at 100 per window — about 3.5
million a year — so a year of guessing at the ceiling succeeds with probability about *k* × 3×10⁻⁹.
`TestPairingCodeSpaceArithmetic` keeps these numbers true.

A *client* is the connection's peer address — an IPv6 address counted by its /64, which one host
can otherwise walk through. Proxy headers are believed only for a request that arrived on a
loopback connection **and** whose `Host` is an `allowed_hosts` name, i.e. through the proxy you
configured; then the client is the `Tailscale-User-Login` identity if present (`tailscale serve`
sets it and drops any copy the browser sent), otherwise the **rightmost** `X-Forwarded-For` entry —
the address the proxy itself saw, not one the browser wrote. Everywhere else (the remote
listener, a loopback `Host`) those headers are ignored, so they cannot buy a fresh budget. The
same rule decides the address shown in the session list and a session's default label.

**The session.** A successful sign-in sets one cookie holding a 256-bit random secret:
`HttpOnly`, `SameSite=Strict`, `Path=/`, and — whenever the browser is on HTTPS, directly or
through a TLS-terminating proxy — `Secure` with the `__Host-` prefix. The database keeps the
secret's SHA-256 only. A session ends at `daemon.session_ttl` (default 30 days), after a week
unused, on sign-out, or when revoked (`idea ui sessions revoke <id>|--all`, or Settings → Remote
access). A session can do everything the UI can, except mint pairing codes: a stolen session must
not be able to outlive its own revocation.

**CSRF.** A request that presents the token is judged by the token alone — a header no cross-site
page can attach — and a wrong token is refused even alongside a valid cookie. A request
authenticated by the cookie that changes state (anything but `GET`/`HEAD`/`OPTIONS`) must also
carry `X-Idea-CSRF`, an HMAC of the session secret that page script obtains from the sign-in
response or `GET /api/v1/ui/session` and a cross-site page cannot read (no CORS is ever sent), **and**
an `Origin` header — which the Host/Origin guard has already required to be loopback, an
`allowed_hosts` name, or the remote listener's own address. `SameSite=Strict` means a cross-site
request should not carry the cookie at all; the token and the Origin check are the locks for when
that does not hold. The unauthenticated `POST /api/v1/ui/login` requires a JSON body and an allowed
`Origin`, which an HTML form on another site cannot produce.

**Push.** Web Push payloads are encrypted end to end to the subscribing browser (RFC 8291) and
signed with the daemon's VAPID key (RFC 8292), which lives in `<state root>/push/vapid.json`
(0600) and never in a repository. A notification's text — initiative title, change-set id, a
short reason — passes through the redactor, but it is shown on a lock screen. A subscription's
endpoint must be `https`, and the daemon's push client refuses non-public addresses and redirects.
The endpoint path and keys are never returned by the API.

**Push renewal.** A push service can replace a browser's subscription at any time and tells only the
service worker (`pushsubscriptionchange`), which holds no API token and may have no live session —
so the cookie-and-CSRF route is not available to it. Instead, subscribing returns a
**per-subscription renewal token** (256 bits; the daemon keeps its SHA-256), which the page stores
in IndexedDB for the worker. `POST …/push/subscriptions/{id}/renew` is outside the token/cookie
check and authorised by that token alone, in the JSON body (never the URL), with an allowed
`Origin` required as for sign-in. It can do exactly one thing: point that one subscription at a new
endpoint and keys, which must pass the subscribe validation (https, public address). It cannot read
anything, change which events are sent, or touch another subscription; each use rotates it, and
every refusal is the same `401`. What a stolen token buys: redirecting that subscription's
notifications — the redacted lines above — to a browser of the thief's choosing, until the owner
notices them stop or removes the subscription in Settings. Taking it needs this browser's own
storage, which also holds the session cookie.

What this does **not** cover:

- **Plain HTTP on a private network is readable by that network.** The cookie and every diff
  cross it in clear; the startup warning says so. Use TLS or Tailscale.
- **A proxy that rewrites `Host` to loopback and strips every proxy header** is
  indistinguishable from a local browser, and is handed the token. `tailscale serve`, Caddy and
  nginx's defaults all add `X-Forwarded-For`; a hand-rolled proxy must too.
- **Anyone who can reach the sign-in page can try codes.** The budgets bound guessing. One
  guesser locks out only itself, but someone with ten or more addresses — or ten tailnet
  identities, behind `tailscale serve` — can spend the global ceiling and pause sign-in for
  everyone for fifteen minutes, cancelling your outstanding code. That is the trade the backstop
  makes: availability for a bound on guessing that holds against any number of addresses.
- **Clients behind one shared address share a budget.** Everyone behind the same NAT, and every
  request through a proxy the daemon does not trust (a proxy that rewrites `Host` to loopback,
  or a chain of proxies — only the last hop is read), counts as one client.
- **A pairing code read off your screen** before you use it signs its reader in. It is single use
  and short-lived, and `idea ui sessions` shows every session that exists.
- **A signed-in phone is as powerful as the local UI.** It can approve and apply change sets.
  Revoke it if it is lost.
<!-- /claim -->

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
- **An `openhands` Agent Server sees everything it is sent** — the work root,
  the brief, the provider key unless withheld — and runs the agent in a sandbox
  this harness neither inspects nor enforces. Only the way back is checked
  (above).
- **The raw blob store is deliberately redaction-free.** For *that* store —
  `raw-blobs/`, the one holding file content and uploaded bytes, which must
  round-trip byte-for-byte — redaction happens at the read/display surface
  rather than at write time: `RenderPatch` and the structured diff redact what
  they return, and the bytes underneath stay as written.

  Why it cannot be redacted at write: apply writes a change set's files from it
  (`fsdiff.Commit`), revert restores the workspace pre-image from it
  (`fsdiff.Restore`), and both are keyed on the SHA-256 of the real bytes — the
  same digest `StaleBase`/`StalePost` compare against the workspace. A masked
  copy would apply `[REDACTED]` into the user's source tree and never match a
  digest again.

  What is in it, exactly: the post-image of every file a change set adds or
  modifies (only once the secret sweep passed it — a denied change set's raw
  bytes are never written, only a masked copy for the diff view); the
  pre-image of every path apply touches, read from the user's own workspace;
  patch and snapshot manifests (paths and digests); and uploaded image bytes.
  Uploaded *text* documents and question answers are not in it — they go to
  `blobs/`. What can therefore be unmasked in it: a value a human
  dispositioned `not_a_secret`, a generic credential match downgraded to
  advisory in a test path, a secret the catalog does not recognise, and
  whatever the user's own files already held before apply.

  Who reads it: apply, revert, the diff renderers above (which redact), the
  test-name extraction for verification evidence (which returns only
  `Test…` function names), and `GET …/docs/{docId}` for an **image** doc only,
  which serves the uploaded bytes as uploaded (see the next bullet). No route
  serves a change-set blob by digest. The store's directories are `0700` and
  every blob is `0600`; `blob.New` re-tightens a directory that already existed
  with a wider mode.

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
- **A root daemon's containers run as the project's owner.** That uid is
  unprivileged, but it owns your files, so a container escape lands as you, as it
  does under a daemon you run yourself. Only projects owned by root, and the
  setup probes, run as the shared `65532`; those share no writable mount, so the
  shared uid matters only to something that judges by uid outside the
  containers. Between a root daemon being killed mid-run and its next start, its
  work roots stay owned by the container user.
- **A daemon run as an ordinary user passes that user's uid.** That is
  unprivileged on the host, but it is the same uid that owns your files, so a
  container escape lands as you. Run the daemon as a dedicated user if that
  matters.
- **No agent image signature is verified.** The default Hermes image is pinned
  by digest, so a re-pushed tag no longer changes what is pulled — but an
  operator-supplied tag-only `--image` is used as given, and `omp` and `acp`
  have no default image at all, so the operator supplies one.
- **The control-plane deny covers `.idea-harness/` and `.git/` only.** A change
  set may still touch `.github/workflows/`, `.envrc` or a similar auto-executing
  file. It is not denied; it raises a blocking `protected_path` finding that a
  human must sign off (above). A file outside the list — a `Makefile`, an npm
  `postinstall` script — gets no such finding, and the defence there is the
  ordinary human review gate.
- **Daemon logs are redacted twice, by pattern and by known value — not by
  anything smarter.** The daemon's `slog` handler is wrapped
  (`security.NewRedactingHandler`, installed in `daemon.NewRuntime`) so the
  message and every attribute value — strings, errors, `fmt.Stringer`s,
  arbitrary values, nested groups, and attributes bound with `With` — pass
  through the credential catalog and the known-value list before reaching
  `daemon.log`. An attribute's key is re-joined to its value for that check, so
  `"password", "…"` is masked the way `password=…` in a message would be. The
  known values are every loaded project's resolved secrets plus the daemon's
  own API token; the set only grows while the daemon runs. `GET /api/v1/logs`
  redacts each line again on the way out, which is what covers lines written
  before this handler existed and anything that reached the file as raw stderr
  rather than through `slog` (a panic trace). A secret that is neither
  pattern-shaped nor configured is not recognised by either pass, the same
  limit the blob-store redactor has. `idea logs` reads the file directly, as the
  user who owns it, and does not re-redact.

## Defense in depth

The system-deny decision is an **additive aggregator**, not a replacement. The
deep enforcers remain independent: `security.ResolveWithin` (path containment),
`blob.Store.Get` (digest verification), and the redactor sweep all still run at
their own layer; a regression in one does not disable the others.
<!-- /claim -->
