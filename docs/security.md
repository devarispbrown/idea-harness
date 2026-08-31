# Security

Security is enforced **outside the LLM** (§43). The control plane assumes model output, repository
content, web content, prior agent output, skills, tools, and executors may all be malicious, and
remains authoritative.

## Threat model (Milestone 1)

M1 threats and mitigations:

| Threat | Mitigation |
|---|---|
| Unauthorized local API requests (§73) | per-install token (0600) required on every `/api/v1` request; loopback-only bind |
| CSRF / DNS rebinding | exact-match loopback Host/Origin allowlist; `Origin: null` rejected; no CORS for foreign origins |
| Cross-project leakage | project-scoped API paths + SSE; per-project `state.db` + blob namespaces; path guard |
| Secret leakage to persistence (§47) | secrets from env only; redaction choke point (store/blob/slog/SSE) with pattern catalog |
| Prompt injection from untrusted content | trust levels assigned by deterministic code; untrusted content data-only-framed at lowest context priority |
| Stored XSS via artifact bodies | artifact bodies sanitized before serving; UI renders markdown without raw HTML |
| SSRF via research web tool | public-address-only fetch (loopback/private/link-local/metadata blocked), 5-redirect cap, 2 MiB/30s limits, deterministic config-supplied URLs (§46) |
| Agent self-approval (§14) | review runs require a distinct actor; `accepted_risk` disposition is human-only |

## Secrets

- API keys are resolved from environment variables only; never stored in SQLite, BlobStore,
  events, artifacts, RunManifests, logs, or contexts.
- The redactor covers PEM private keys, JWTs, AWS `AKIA…`, GitHub `ghp_…`, OpenAI `sk-…`, and
  generic `key=`/`token=` patterns, plus any known secret values.
- The canary-secret leak sweep (a canary secret injected as input must not appear in any store)
  is a required failure-injection test.

## Trust levels (§38)

```text
system → policy → human_approved → reviewed_agent_output → unreviewed_agent_output → external_untrusted
```

Trust is assigned by deterministic code from persisted run/review records — never declared by
model output. `human_approved` requires a recorded human action; `reviewed_agent_output` requires
a distinct reviewer run.

## Out of scope for M1 (deferred to M3)

Sandbox abstraction, capability broker, executor isolation, ChangeSet apply journal, and threat
model document (produced and approved at the start of M3, per §68).
