# Milestone 1 Engineering Execution Plan — Idea → Reviewed PRD

**Status:** Revised after adversarial review (blocking findings resolved). Pending approval to implement.
**Repo state at authoring time:** empty directory, no Git repository, no artifacts, no code.
**Milestone:** 1 of 4 (per constitution §66, §82).

This plan covers the first vertical slice only:

```
empty folder → idea init → idea new → framing → research → reviewed PRD
```

It does **not** build the platform horizontally. Everything not required for that slice is
explicitly deferred to Milestones 2–4. Section 24 records the adversarial review and how each
blocking finding was resolved.

---

## 1. Goals

Deliver a local-first control plane that takes a user from an empty directory through a
**reviewed Product Requirements Document** with real provenance, in a way that already feels
useful and coherent.

Concretely, by the end of Milestone 1:

1. A single `idea` binary (Go) that runs a user-level daemon bound to `127.0.0.1`, protected by a
   per-install token (§15).
2. `idea init` initializes a project in an **empty directory** (Git optional, never auto-init'd).
3. `idea new "…"` creates an Initiative and drives an async, durable workflow:
   **Framing → Research → Product Definition → Adversarial Review → Approved PRD**, with a
   specified human-in-the-loop state machine (§11).
4. Structured, schema-validated agent outputs (IdeaBrief, ResearchBrief, PRD) with immutable
   versioned Artifacts and a content-addressed BlobStore.
5. First-class Assertions, Requirements, Decisions (with **recorded dissent**), and Open Questions,
   all with structured provenance; a synthesized **Current Understanding** projection with a
   defined content model.
6. Independent **adversarial review** of the PRD (no self-approval, enforced actor separation), with
   structured findings using the constitution's disposition vocabulary and a defined approval bar.
7. A thin, calm Web UI (React + TypeScript) exposing Ideas, Decisions, Activity, plus a clearly
   secondary Runtime view — with no raw agent/run machinery on primary surfaces.
8. An append-only event log that drives Activity/timeline and (later) causality.
9. Acceptance validated both against a deterministic fake provider **and** one real-provider run.

## 2. Non-Goals (explicitly out of scope for M1)

- Sandbox abstraction, capability broker, OMP/executor integration, ChangeSets, apply journal
  (all Milestone 3). **M1 performs no source-tree mutation** (writes only `.idea-harness/` config).
- Product Graph edges, staleness/impact analysis, semantic diff (Milestone 2/4).
- Architecture, Product Design, Verification Design stages and their specialists (Milestone 2).
- Verification evidence and the final verification gate (Milestone 4).
- `idea why / trace / impact`; statement-level (in-document) provenance anchors (Milestone 2/4).
- Git management beyond passive detection; `idea git …` deferred; Git init/remote/push never automatic.
- Threat model document (produced and approved at start of Milestone 3, per §68).
- Embeddings / semantic retrieval; any marketplace, plugin SDK, multi-user, SaaS, remote fleet.
- Source-file ingestion into model context (M1 core path is empty-dir; no repo content exfiltrated).
- OS keychain secret storage (M1 is env-only; keychain + project-scoped secret adapters deferred).
- Fine-grained egress firewall; numeric risk engine; five coding-harness adapters.
- Orphan-blob garbage collection (deferred; see §16).

## 3. Requirements in scope (from the constitution)

| Source | Requirement |
|---|---|
| §1, §66 | Vertical slice: empty dir → init → new → framing → research → reviewed PRD. |
| §4, §5 | Current Understanding as a provenance-backed projection, not a source of truth; content model specified in §9. |
| §6 | Recommended Next Move from deterministic candidates, ranked/explained by orchestrator; defined precedence (§9). |
| §7 | "What Changed" as a grouped, narrated summary — not a raw event log; timeline behind an explicit drill-down. |
| §8 | Decisions over agent conversations; **dissent preserved, never erased** (modeled in §6, rendered in §9). |
| §9, §10 | Role Contracts (not "act like X" prompts); deterministic specialist invocation. |
| §12 | Security enters early: security-derived concerns are ordinary Requirements. |
| §15 | Structured Findings (6-value disposition enum); ≤3 review cycles then human judgment; defined approval bar (§11). |
| §18–§20 | Assertions (Fact/Assumption/Hypothesis) with states/evidence/confidence/structured provenance; immutable-versioned Artifacts. |
| §26 | Crash-safe writes (atomic state+event now; ChangeSet apply journal is M3). |
| §29, §73 | One daemon, loopback bind, local API **unauthorized requests rejected** (token auth, §15). |
| §30 | Runtime state in `~/.local/share/idea-harness/`; portable config in `.idea-harness/`. |
| §31 | Large content in content-addressed BlobStore, never inline in SQLite. |
| §32 | Relational state authoritative + atomic state-and-event append in one SQLite transaction. |
| §34 | Long requests return Operation IDs; no held HTTP connections. |
| §35–§37 | Generic Run model + RunManifest provenance (incl. usage); deterministic Context Compiler (no embeddings). |
| §38 | Context trust levels; trust assigned by deterministic code, never model-declared. |
| §39–§40 | Central structured-output pipeline (validate → bounded repair); provider capability model; no silent fallback. |
| §41 | Native/internal model execution first; no coding harness yet. |
| §43, §47, §48 | Security enforced outside the LLM; no plaintext secrets persisted (incl. redaction choke point); redact before persistence. |
| §49 | Skills (knowledge) vs Tools (executable) vs Executors separated; M1 ships no executable skills; the one Tool (research fetch) is deterministic-only. |
| §51 | Error taxonomy (infra retryable vs semantic re-plan vs governance escalation). |
| §52–§59 | Primary nav Ideas/Decisions/Activity/Runtime/Settings; Initiative Home hierarchy; Decisions UI; calm command center. |
| §64 | CLI subset with `--json` (schema = API response schemas). |

## 4. Architecture & repository layout

Stack (from §28): Go daemon/CLI, React+TS UI, SQLite, content-addressed BlobStore, REST, SSE,
YAML+JSON Schema config, OpenAPI. Docker/OMP/OTel export are **not** exercised until M3.

### Single-binary daemon + thin HTTP client (Decision D1)

- One Go binary `idea`. `idea daemon` runs the HTTP server (loopback, default `127.0.0.1:8787`);
  `idea dev` starts the daemon and serves the built UI. `idea daemon status` / `idea daemon stop`
  manage the detached daemon.
- State-mutating/reading CLI commands are **thin HTTP clients**. On connect they complete a token
  handshake (§15). If no healthy daemon is reachable, the CLI starts a detached managed daemon
  (pidfile under the global state dir, stale-pidfile detection) and retries once. A **single writer**
  (the daemon) and "one daemon, many projects" (§29) are preserved.
- CLI↔daemon schema/version check on startup; the daemon migrates `state.db`/`global.db` on boot.

### Repository layout (Decision D2)

```text
idea-harness/
  go.mod / go.sum                 # module github.com/idea-harness/idea-harness
  cmd/idea/                       # single binary entrypoint (subcommands)
  internal/
    daemon/                       # HTTP server, auth/token, routes, SSE, lifecycle
    config/                       # YAML + JSON Schema load/validate
    store/                        # SQLite (state.db + global.db), migrations, events, redaction choke point
    blob/                         # content-addressed BlobStore (redaction before Put)
    domain/                       # typed entities + invariants
    workflow/                     # deterministic stage graph + state machine + next-move candidates
    roles/                        # Role Contract loader + versioning
    context/                      # deterministic Context Compiler
    model/                        # provider abstraction + native execution + structured-output pipeline
    review/                       # deterministic checks + adversarial review + findings lifecycle
    security/                     # token auth, redaction, trust assignment, fsutil path guard, sanitization
    tools/                        # controlled research web fetch (network-policy-gated)
    project/                      # project init/registration, .idea-harness layout
  internal/web/                   # embedded built UI (embed.FS)
  web/                            # React + TypeScript app (Vite)
  schemas/                        # JSON Schema (config, role contracts, artifact bodies, CLI output)
  api/                            # OpenAPI 3.x spec (source of truth)
  docs/                           # architecture, configuration, cli, security, plans
  .idea-harness/                  # the project's own portable config (created on init)
  Makefile
```

### Data placement (Decision D3, §30)

- **Global:** `~/.local/share/idea-harness/global.db` (project registry, daemon lifecycle, token
  file), `projects/`. State tree `0700`; DB/pid/token files `0600` (enforced, not umask-dependent).
- **Per project:** `~/.local/share/idea-harness/projects/<project-id>/state.db`, `blobs/`, `cache/`,
  `logs/`. Project runtime state never pollutes the source repo.
- **Portable config** in the user's project: `.idea-harness/project.yaml`, `harness.yaml`,
  `roles/`, `standards/`, `policies/`, `schemas/`. This is the only thing idea-harness writes into
  the working tree; config validation **rejects secret-bearing keys** here (§47).

## 5. Packages / modules (new components)

| Component | Responsibility | Notes |
|---|---|---|
| `internal/config` | Load/validate `project.yaml` + `harness.yaml`; embed JSON Schemas | provider, model, network policy, review budget |
| `internal/store` | SQLite access, migrations, `ApplyStateAndEvent` transaction helper | §32 atomicity; redaction choke point |
| `internal/blob` | `Put(content) → sha256 digest`, `Get`, `Exists`, metadata | redaction before digest; §31 |
| `internal/domain` | Typed entities + invariants | assertion states, requirement codes, decision immutability, dissent |
| `internal/workflow` | Stage-graph policy, state machine, candidate next-move generation | deterministic |
| `internal/roles` | Role Contract schema, loader, versioning | competencies/behaviors/rubrics/checks |
| `internal/context` | Deterministic context compiler + trust levels + entry manifest | §37; untrusted entries data-only-framed |
| `internal/model` | Provider interface, capabilities, structured-output validate+repair | §39–§40; actual_model recorded |
| `internal/review` | Deterministic checks + adversarial review + finding lifecycle + actor separation | §14–§16 |
| `internal/security` | token auth, redaction, trust assignment, fsutil guard, HTML sanitization | §43, §47–§48 |
| `internal/tools` | Controlled research web fetch (public-address-only, capped) | §46; deterministic invocation |
| `internal/daemon` | REST/SSE server, auth middleware, Operation lifecycle, UI serving | §28, §34 |
| `cmd/idea` | CLI subcommands as HTTP client | §64 |
| `web/` | React+TS UI | §52–§59 |

## 6. Data model — SQLite schema required now

Every domain write is wrapped with an `events` append in **one transaction** (§32). Large bodies
are stored as digests; SQLite holds only metadata + digests (§31). Event payloads are **always**
stored as blobs (digest-only column).

```text
global.db
  projects(id, name, root_path, project_id, created_at, updated_at)      # registry
  daemon_meta(key, value)                                                # token, lifecycle

state.db (per project)
  initiatives(id, title, description, status, created_at, updated_at)    # status: running|needs_human_judgment|approved|failed
  workflows(id, name, version, definition_digest, created_at)
  workflow_runs(id, initiative_id, workflow_id, status, created_at, finished_at)
  stage_runs(id, workflow_run_id, stage_key, status, attempt, started_at, finished_at)
  runs(id, kind, stage_run_id, status, started_at, finished_at)          # kind: model|review|check|tool
  operations(id, type, initiative_id, status, result_ref, created_at, finished_at)
  events(id, seq, aggregate_type, aggregate_id, actor, correlation_id, causation_id,
         operation, event_type, payload_digest, created_at)              # digest-only
  assertions(id, initiative_id, type, status, statement, evidence_digest, confidence,
             provenance_json, produced_by_run_id, superseded_by, created_at)      # §18
  requirements(id, initiative_id, code, statement, acceptance_criteria_json, priority,
               status, source_assertion_id, provenance_json, produced_by_run_id,
               superseded_by, created_at)                                          # §17
  decisions(id, initiative_id, code, type, question, options_json, recommendation,
            status, resolution, resolved_by, provenance_json, superseded_by, created_at) # §8, §19
  decision_dissents(id, decision_id, proponent_role, position, reasoning_digest, created_at) # §8 dissent, immutable
  questions(id, initiative_id, question, status, answer_digest, answered_by, created_at, resolved_at) # open|resolved
  artifacts(id, initiative_id, kind, created_at)
  artifact_versions(id, artifact_id, version, status, body_digest, produced_by_run_id,
                    superseded_by, created_at)                                          # §20
  findings(id, review_run_id, target_type, target_id, severity, confidence, category,
           finding, evidence, impact, recommendation, disposition, disposition_actor, created_at)
           # disposition CHECK ∈ fixed|accepted_risk|not_applicable|duplicate|deferred|rejected (§15)
  run_manifests(id, run_id, snapshot_digest, config_digest, workflow_digest, role_version,
                requested_model, actual_model, context_digest, skill_digests_json,
                tool_versions_json, usage_json, started_at, finished_at, result_digest)  # §36; usage_json = tokens/cost/duration
  context_bundles(id, run_id, digest, created_at)
  context_entries(bundle_id, priority, source, digest, trust, inclusion_reason, selector,
                  transformation, size)                                   # §37
  read_cursors(scope, last_event_seq, updated_at)                        # "since last look" state (§7)
  blobs(digest PK, size, created_at)                                     # metadata only; content on disk
```

Notes:

- **Structured provenance:** `provenance_json` is a list of typed references
  `[{kind: run|artifact_version|context_entry, id, digest, trust}]` — not free text. Combined with
  `produced_by_run_id`, this makes reviewer≠producer a **queryable** invariant (§14).
- **No GraphEdges table in M1** (Product Graph is M2). Provenance is carried by the FKs above.
- `review_runs` are `runs(kind='review')`; a review run must reference a producer run with a
  **different actor** (enforced in `internal/review`).
- Assertion/Requirement/Decision/Question codes are project-scoped, monotonically assigned.

## 7. API — endpoints required now

REST under `/api/v1`. **Every** request (including SSE) requires the `X-Idea-Token` header (§15)
and state endpoints are **project-scoped** to prevent cross-project leakage.

```text
Health/System
  GET  /api/v1/health
  GET  /api/v1/version
  POST /api/v1/config/validate
Projects
  POST /api/v1/projects                          # init/register
  GET  /api/v1/projects
  GET  /api/v1/projects/{projectId}
Initiatives  (scoped: /api/v1/projects/{projectId}/…)
  POST   …/initiatives                           # idea new; returns Operation id
  GET    …/initiatives
  GET    …/initiatives/{id}
  GET    …/initiatives/{id}/understanding        # Current Understanding projection
  GET    …/initiatives/{id}/next-move            # Recommended Next Move
  GET    …/initiatives/{id}/changed              # What Changed (grouped narrative)
  POST   …/initiatives/{id}/resume               # resume from needs_human_judgment
  POST   …/initiatives/{id}/approve              # explicit human PRD approval
Operations
  GET  /api/v1/operations/{id}
  GET  /api/v1/operations?project_id=…
Decisions
  GET  /api/v1/projects/{projectId}/decisions
  GET  /api/v1/projects/{projectId}/decisions/{id}
  POST /api/v1/projects/{projectId}/decisions/{id}/resolve   # human; immutable on approval
Questions
  GET  /api/v1/projects/{projectId}/questions
  POST /api/v1/projects/{projectId}/questions/{id}/answer     # records human answer (human_approved)
Requirements / Assertions
  GET  /api/v1/projects/{projectId}/requirements
  GET  /api/v1/projects/{projectId}/assertions
Artifacts
  GET  /api/v1/projects/{projectId}/artifacts
  GET  /api/v1/projects/{projectId}/artifacts/{id}
  GET  /api/v1/projects/{projectId}/artifacts/{id}/body       # sanitized markdown from BlobStore
Reviews / Findings
  GET  /api/v1/projects/{projectId}/reviews
  GET  /api/v1/projects/{projectId}/reviews/{id}
  POST /api/v1/projects/{projectId}/findings/{id}/disposition # disposition ∈ 6-value enum; accept_risk = human-only
Activity / Events
  GET  /api/v1/projects/{projectId}/activity                   # grouped summary + timeline (drill-down)
  GET  /api/v1/projects/{projectId}/events                     # SSE stream (Last-Event-ID resume)
```

OpenAPI 3.x in `api/openapi.yaml` is the source of truth; handlers, TS client, and CLI `--json`
shapes are all generated/checked against it (no drift).

## 8. CLI — commands required now

State-returning commands support `--json` (same schemas as API responses).

```text
idea init                 # initialize project in cwd (empty dir; no git required)
idea dev                  # start daemon + serve UI
idea daemon               # run daemon in foreground
idea daemon status|stop   # manage the detached daemon (attach-if-healthy, stale-pidfile handling)
idea new "<idea>"         # create initiative (errors with "run idea init first" if not initialized)
idea status [--json]      # initiative + stage + current work summary
idea changed [--json]     # grouped "What Changed" summary since last look
idea decisions [--json]   # open/blocked decisions (incl. dissent)
idea reviews [--json]     # review findings
idea config validate      # validate .idea-harness config against schema
idea doctor               # env/daemon/config/state health check
idea inspect run <id>     # RunManifest (incl. requested vs actual model, usage)
```

Deferred: `idea why/trace/impact`, `idea git …`, `idea skill …`, full `idea inspect context`.

## 9. UI — screens/components required now

Thin, calm, keyboard-native (§52–§63); SSE drives live updates. **No raw event rows, run ids,
JSON dumps, or chain-of-thought on primary surfaces** (asserted in UI tests).

- **Shell / Nav:** Ideas, Decisions, Activity, Runtime, Settings. A project indicator resolves
  multi-project scope. Runtime is labeled/treated as the **expert/operator view** (§57), not a
  primary content surface.
- **Empty state:** "What are you thinking about building?" — M1 offers the **Idea** start type
  only. (Existing-folder / Requirement / Bug start types are deferred until the workflow graph can
  branch on them; shown but disabled with a "coming in a later milestone" note.)
- **Initiative Home content model** (§5 order): Current Understanding → What Changed → Open
  Questions / Risks → Needs Your Judgment → Recommended Next Move → Current Work → Artifacts /
  Requirements.
  - **Current Understanding:** deterministic statement rows (text, type, confidence, source,
    evidence digest, supersession status) — no LLM narration; provenance visible per statement.
  - **What Changed:** grouped, narrated summary derived from events (e.g., "Research completed:
    14 assertions, 3 open questions; PRD v1 drafted"). Raw event timeline is behind an explicit
    "Show event log" expander.
  - **Open Questions / Risks:** question list + risk list; answer action feeds the Questions API.
  - **Needs Your Judgment:** blocking decisions, non-convergence, open critical/high findings.
  - **Recommended Next Move:** single ranked action + why + affected; precedence = judgment items
    outrank stage advancement; paused/blocked states force the next move to the blocking item.
  - **Current Work:** human-meaningful ("PRD draft is being written", "review pending"), never a
    stage/progress board.
  - **States:** empty / loading / running / `needs_human_judgment` / approved, each with a defined
    presentation (incl. a "waiting on you" banner).
- **Decisions:** Need Your Decision / Blocked / Recommended Changes / For Awareness. Each decision
  shows the synthesized recommendation **and the dissenting view(s)** (position + rationale, from
  §6). Actions: Accept / Choose alternative / Discuss (Discuss creates a Decision from the open
  question rather than a disposition).
- **Activity:** grouped "What Changed" summaries + event timeline behind an expander (§56).
- **Artifact reader:** clean document surface (PRD/ResearchBrief/IdeaBrief); content rendered via a
  strict sanitizer (no raw HTML/javascript URLs); entity-level provenance shown alongside.
- **Review view:** findings linked to evidence/requirement/location with severity/confidence/
  category/impact/recommendation; actions Fix / Not Applicable / Accept Risk / Discuss (mapped to
  the six-value disposition enum; `accept_risk` requires human). "No agent may approve its own
  work" is enforced and surfaced.
- **Runtime (expert view):** role, model, executor (n/a now), operational action, phase, elapsed,
  heartbeat, task, cost/tokens; **no chain-of-thought**.
- **Command palette ⌘K:** new idea, open decisions, show changed, pause execution, switch project
  (each backed by a real endpoint).
- **Visual direction (§63):** restrained neutral palette, strong typography, subtle borders, limited
  state colors, clean document surfaces, contextual inspectors, keyboard-native; references Linear
  precision / GitHub review structure / Raycast speed / Stripe doc typography. **No** AI gradients,
  robots, sparkles, anthropomorphic experts, cyberpunk, excessive dashboards. Accessibility:
  keyboard focus order, ≥4.5:1 contrast, non-color status indicators.

## 10. Role Contracts required now

Versioned YAML in `.idea-harness/roles/`, bundled in the binary, instantiated on init. Each contract
declares **competencies, required behaviors, artifact expectations, rubrics, required checks, tool
requirements** (§9). M1 requires four:

1. **Product Orchestrator** — runs the stage graph, synthesizes dissent into Decisions, ranks and
   explains Recommended Next Move; may not fabricate policy-violating actions (§6).
2. **Research Analyst** — produces evidence-backed ResearchBrief + Assertions with sources; marks
   assumptions vs facts explicitly; no unattributed claims; offline behavior per §15.
3. **Staff Product Manager** — produces the PRD with verifiable Requirements/Acceptance Criteria,
   draft Decisions, open Questions, Risks; provenance on every statement.
4. **Adversarial Reviewer** — independent review of PRD/artifacts; structured Findings; never
   reviews its own output (§14); runs with an actor distinct from the producer (enforced).

Design/UX, Architecture, Quality, Security, Engineering Lead roles are instantiated in M2. The
"vague acceptance criteria" gate runs in M1 as a **structural deterministic check + adversarial
reviewer judgment** (§11) so quality pressure exists before the Quality Engineer role lands.

## 11. Workflow, review & human-in-the-loop state machine

Deterministic stage graph, orchestrator constrained by it (§6):

```text
framing ──► research ──► product_definition ──► review ──► approved
                ▲                                   │
                └──────────── revise (≤3) ──────────┘
```

**Initiative/workflow statuses:** `running`, `needs_human_judgment`, `approved`, `failed`.

**Review loop (specified):**
- Deterministic checks run first: artifact-body schema valid; unique REQ codes; acceptance criteria
  present; **acceptance criteria structurally verifiable** (each criterion requires a measurable
  predicate — metric + target + condition — enforced via `acceptance_criteria_json` schema, plus a
  deterministic pattern check that rejects bare evaluative adjectives without a quantified target;
  golden pass/fail fixtures unit-tested). Residual semantic vagueness is the **Adversarial
  Reviewer's** job (a model-based check, tracked as `runs(kind='check')`, not mislabeled
  deterministic).
- Then the independent Adversarial Reviewer over the compiled context, producing structured
  Findings (six-value disposition enum; actor-constrained disposition; `accepted_risk` is
  human-only).
- **Approval bar (explicit):** no **open** `critical`/`high` findings; every finding dispositioned;
  `critical`/`high` must be `fixed` or (human) `accepted_risk`.
- **Auto-revise trigger:** open `critical`/`high` findings auto-schedule a bounded revise run.
  `medium`/`low` findings may remain open with a disposition and do not block approval.
- **Cycle cap:** ≤3 revise cycles. On non-convergence the initiative pauses into
  `needs_human_judgment` — never an infinite loop (§15).
- **Resume/approve:** a human resolves Decisions/Questions/dispositions via the API, then
  `POST /initiatives/{id}/resume` re-runs review, and `POST /initiatives/{id}/approve` is the
  explicit human approval (or a clean re-review auto-approves). The terminal `approved` state is
  reachable only through this defined surface.
- **Review budget:** `max_cycles` (default 3) + optional `max_cost_usd`/`max_tokens`; exceeding the
  budget escalates to `needs_human_judgment` (§16 economics).
- **Specialist invocation:** deterministic on project/risk signals only (e.g., PII/auth terms raise
  a security-derived Requirement, §12). No uncontrolled proliferation.

**Recommended Next Move precedence:** judgment items outrank stage advancement; when paused or
blocked, the next move points at the blocking human decision (asserted in e2e).

## 12. Context Compiler (§37)

Deterministic priority ordering only — **no embeddings, no semantic retrieval** in M1:

```text
1 immutable policy → 2 project policy → 3 Role Contract → 4 stage instructions → 5 task
→ 6 direct requirements → 7 direct decisions → 8 required upstream artifacts
→ 9 explicitly relevant source files (not ingested in M1 — empty-dir path) → 10 standards
→ 11 lower-priority history → 12 UNTRUSTED external content (web research)
```

- Untrusted external content (web research, raw model output) is placed at the **lowest priority**
  and **data-only-framed** (delimiters + explicit "data, not instructions" framing in the system
  prompt); it can never be `human_approved`.
- Every entry records `{source, digest, trust, inclusion_reason, selector, priority,
  transformation, size}`.
- **Trust is assigned by deterministic code** (`internal/security`) from persisted run/review
  records only — never by model output. `human_approved` requires a recorded human action via an
  authenticated, Origin-checked endpoint. `reviewed_agent_output` requires a distinct reviewer run
  with a non-self disposition.
- Trust levels: `system / policy / human_approved / reviewed_agent_output / unreviewed_agent_output
  / external_untrusted` (§38).

## 13. Model abstraction & structured output (§39–§41)

- Provider interface: `provider, model, reasoning_effort, max_output, fallbacks, capabilities
  {structured_output, tools, streaming, reasoning_controls, context_limit, max_output}`. Providers:
  OpenAI and Anthropic for M1, plus a generic OpenAI-compatible endpoint; config in `harness.yaml`,
  **API keys from env only** (§47).
- **Fallback semantics:** fallbacks fire **only on capability mismatch**, never on infra errors
  (infra errors → bounded retry → Operation `failed`). `actual_model` is always recorded in the
  RunManifest; any `requested_model ≠ actual_model` is surfaced (no silent fallback, §36).
- Central structured-output pipeline: provider-native structured output → JSON-Schema validation →
  **bounded repair** (single-pass, budget-limited) → validated typed object. Raw and repaired content
  are stored **post-redaction** by digest. No per-role repair logic (§39).
- Every model call produces a Run + RunManifest with `usage_json` (tokens in/out, cost, duration).

## 14. BlobStore, Events, Operations, RunManifest

- **BlobStore:** `blobs/sha256/<digest>`; used for artifact bodies, model outputs, context bundles,
  review bodies, evidence, large logs, event payloads. All content is redacted **before** digest
  computation. SQLite stores digests + size only (§31).
- **Events:** appended atomically with state; fields per §32; payloads stored as blobs (digest-only).
- **Operations:** `idea new`, PRD generation, and reviews are durable async Operations returning an
  Operation id; the daemon reconciles unfinished operations on restart (§34).
- **RunManifest:** recorded per run with the fields in §36 (incl. `usage_json`); enables
  `idea inspect run`.

## 15. Security considerations (M1)

- **Auth token (local API):** a 128-bit unguessable token is generated at first daemon run, stored
  `0600` under the global state dir, and required on **every** `/api/v1` request via
  `X-Idea-Token`. The daemon injects the token into the served UI page; the CLI reads the token
  file. This rejects unauthorized local requests (§73) — including cross-origin browser requests and
  other local users — and gives the CLI a way to detect an impostor daemon (token known only to the
  real daemon + CLI). Loopback-only bind is not configurable to non-loopback in M1.
- **Host/Origin hardening (exact mechanism):** exact-match Host allowlist (`127.0.0.1`,
  `localhost`, `[::1]`, with port; no suffix matching); reject any `Origin` not in the allowlist
  including `Origin: null`; never emit CORS headers for foreign origins; JSON-only content types on
  mutating endpoints (forcing preflight); no Private-Network-Access grant. Test matrix covers
  rebinding domains, `Origin: null`, and loopback bind attempts.
- **Redaction (choke point + catalog):** one redaction choke point invoked at `store` write,
  `blob.Put`, the `slog` handler, error formatting, and SSE serialization. Catalog covers PEM
  private-key blocks, JWTs, AWS `AKIA…`, GitHub `ghp_…`, OpenAI `sk-…`, generic `token=`/`key=` in
  URLs, and configured sensitive markers. **No plaintext secrets** anywhere (§47); "raw" model
  output stored is raw *post-redaction*.
- **Secrets:** env-only resolution in M1 (machine-global, documented warning); keychain +
  project-scoped adapters deferred. Config validation rejects secret-bearing keys in the portable
  `.idea-harness/` tree.
- **Trust & injection containment:** trust assigned by deterministic code (§12); policies/role
  contracts pinned above untrusted content; web-research content data-only-framed at lowest
  priority. The deterministic graph + schema validation + adversarial review are the actual
  containment (ordering alone is not relied upon).
- **Research web tool (SSRF controls):** fetches `http(s)` to **public** addresses only (no
  loopback/link-local/private/`169.254.169.254`), refuses redirects into blocked ranges, caps size
  and timeouts, strips/refuses credentials in URLs, and is invoked **deterministically** (never
  with URLs from raw model output). Network policy default is **`disabled`**; when disabled,
  Research runs offline and must mark every unsourced claim as an **Assumption** with
  `source = "model knowledge, unverified"` — never a Fact.
- **Project isolation:** every state endpoint/SSE is project-scoped (§7); a single `fsutil` path
  guard (absolute + `filepath.Clean` + prefix verification + symlink-escape rejection) is applied
  to blob/config/cache/log access; project ids are opaque, never derived from untrusted input.
- **Output sanitization:** artifact/body content is rendered through a strict sanitizer (no raw
  HTML, no `javascript:`/`data:` URLs, `rel` on links) to prevent stored XSS → control-plane access.
- **Actor separation:** review runs must reference a producer run with a different actor; finding
  dispositions are actor-constrained (`accepted_risk` human-only). Reviewer may be configured on a
  distinct provider/model by default to reduce shared-blind-spot risk.

## 16. Failure modes & rollback/recovery

| Failure | Handling |
|---|---|
| Daemon dies mid-artifact-write | BlobStore write-then-verify digest; Artifact rows commit only after blob verified (orphan-blob GC deferred to M3, documented) |
| Provider timeout / rate limit / unavailable | `provider_*` errors → bounded retry with backoff → Operation `failed` → surfaced as Next Move (infra errors retry; fallback models never fire here) |
| Malformed / malicious structured output | Schema validation → bounded repair → on failure, mark run failed + finding (semantic, no auto-retry-as-network) |
| Review non-convergence | Pause at `needs_human_judgment` after 3 cycles (§11); resume via API |
| State+event atomicity | Single SQLite transaction wrapper; no partial append (§32) |
| DB contention / disk full | WAL mode; write failures surface as Operations `failed`; no silent success |
| Daemon restart | Reconcile `operations` in non-terminal state from persisted events/stage state; migrate schema on boot (§26 spirit) |
| Daemon lifecycle | `idea daemon status/stop`; attach-if-healthy; stale-pidfile detection; CLI↔daemon version check |

ChangeSet apply-journal crash safety (§26) is **Milestone 3** scope; M1 requires atomic state+event
writes and idempotent Operation reconciliation.

## 17. Observability

- Structured logging (Go `slog`) with trace/correlation ids; OpenTelemetry-compatible conventions
  and trace propagation, **no OTLP collector in M1**. The slog handler runs redaction.
- Append-only event log is the primary observability surface (Activity/timeline).
- RunManifest (incl. `usage_json` tokens/cost/duration) + ContextBundle enable `idea inspect run`;
  the Runtime view renders cost/tokens.
- Review economics recorded from day one: review-run latency + cost + finding dispositions
  (`fixed`/`duplicate`/`rejected` enable false-positive tracking). False-positive **rate** and
  defect-escape **rate** are explicitly deferred (they need a larger sample across M2+).

## 18. Testing plan

Each task's definition of done includes its own unit/component tests (quality ships with the code,
not end-loaded); T17 covers cross-cutting integration/e2e/failure-injection.

- **Unit:** assertion/requirement/decision/question state transitions; workflow state machine +
  next-move candidate precedence; context priority ordering; trust classification (incl. elevation
  denial); structured-output repair bounds; review cycle cap + approval bar; the **four
  deterministic PRD checks** (artifact schema, REQ uniqueness, acceptance-criteria presence,
  structural-verifiability with golden pass/fail fixtures); redaction pattern catalog; disposition
  enum (CHECK constraint); config schema validation; superseded artifact/decision immutability.
- **Component:** SQLite store + atomic state/event; BlobStore (dedupe, digest verify, redaction);
  provider adapters (fake transport + **one tagged real-provider smoke**); config load/validate;
  `fsutil` path guard (traversal + symlink); auth token middleware (Host/Origin matrix); research
  web tool (SSRF blocklist, redirect, size/timeout); daemon lifecycle (spawn/attach/stale-pidfile).
- **Integration:** stage lifecycle (framing→research→PRD→review→approved) against a **fake
  deterministic provider**; review loop incl. non-convergence → `needs_human_judgment` → resume →
  re-review → approve; disposition→transition mapping; actor-separation guard (producer ≠ reviewer);
  SSE emission + reconnect (Last-Event-ID resume); CLI↔daemon token handshake + version skew;
  two in-flight initiatives / parallel CLI requests (no lost events, no cross-project mixing).
- **End-to-end:** `empty folder → idea init → idea new → framing → research → reviewed PRD` with
  the fake provider, asserting on: artifact versions + BlobStore body round-trip, the Current
  Understanding projection (per-statement provenance fields), findings, and a
  **human-loop walk** (force non-convergence → pause → resolve a Decision + disposition a finding →
  resume → approve).
- **UI:** React component tests for the four screens + review view + decisions (incl. dissent
  rendering); a Playwright/browser smoke of the vertical slice asserting **no raw event rows/run
  ids/chain-of-thought on primary surfaces** and no rendered raw HTML in the artifact reader.
- **Failure injection:** daemon death during artifact write; provider timeout; malformed structured
  output; provider failure + fallback → assert `actual_model ≠ requested_model` recorded; malicious
  prompt-injection web-research input (treated untrusted, inert); review non-convergence; disk-full
  on blob write; DB contention; malformed config; skill/role digest mismatch; **canary-secret leak
  sweep** (inject a canary secret as input, scan state.db/blobs/events/context bundles/manifests/
  logs/SSE for it → zero hits).

## 19. Migration considerations

- No prior schema exists (greenfield). Adopt a **versioned migration** table (`schema_migrations`)
  from day one; daemon migrates on boot; CLI↔daemon schema-version check.
- Every future schema change ships with up/down migrations + a data-integrity test.

## 20. Edge cases

- Empty directory with no Git (core path); directory that later runs `git init` (passive detection
  only — never auto-init/remote/push, §24).
- Duplicate idea submission; initiative restart/resume; project opened from a subdirectory;
  `idea new` before `idea init` → clear "run `idea init` first" error.
- Provider returns valid JSON that fails semantics (schema passes, content wrong) → caught by
  adversarial review, not schema alone.
- Very large research/PRD bodies → BlobStore, never inline.
- Network disabled → offline research marks assumptions (never unattributed facts), §15.

## 21. Dependency-ordered tasks

```text
T1  Scaffold repo: go.mod, cmd/idea skeleton, Makefile, docs/ + schemas/ + api/ dirs
T2  config package + JSON Schemas + `idea config validate` (+ secret-key rejection)
T3  store package: SQLite + migrations + atomic state/event transaction + redaction choke point
T4  blob package: content-addressed BlobStore + digest verification + redaction-before-Put
T5  domain package: typed entities + invariants (assertion/requirement/decision+dissent/question/artifact/finding)
T6  events + Operation model + daemon reconciliation on restart + read_cursors
T7  roles package: Role Contract schema/loader + the 4 M1 contracts
T8  context package: deterministic Context Compiler + trust levels (deterministic assignment) + untrusted framing
T9  model package: provider abstraction + native execution + structured-output validate/repair (fallback semantics)
T10 tools package: controlled research web fetch (network policy + SSRF controls + offline mode)
T11 security package: token auth, redaction catalog, fsutil guard, HTML sanitization
T12 workflow package: M1 stage graph + state machine (approval bar, resume/approve) + next-move precedence
T13 review package: deterministic PRD checks + adversarial review orchestration + findings lifecycle + actor separation
T14 daemon: REST + SSE + auth middleware + OpenAPI; wire domain→workflow→model→review→tools
T15 CLI: init/new/status/changed/decisions/reviews/config validate/doctor/daemon status|stop + --json + managed daemon
T16 projections: Current Understanding + Recommended Next Move + What Changed (grouped narrative)
T17 web UI: shell/nav, empty state, Initiative Home (content model + states), Decisions (dissent), Activity, Artifact reader, Review view, Runtime, ⌘K
T18 tests: cross-cutting integration/e2e/failure-injection/UI/real-provider smoke (fake provider for deterministic CI)
T19 docs: README, docs/architecture.md, docs/configuration.md, docs/cli.md (generated where possible)
```

Dependency edges: T2→T3→T5; T3→T4; T3,T4→T6→T14; T5,T7,T8,T9,T11→T12→T13; T10,T12,T13→T14→T15→T17;
T5,T6,T14→T16→T17; T11 before T14 (redaction/token in path from day one). T18 spans; T19 closes.

## 22. Exit criteria & acceptance

1. In an empty directory, `idea init` succeeds with no Git repo.
2. `idea new "…"` returns an Operation id and produces — **via the fake provider path (deterministic
   CI)** — an IdeaBrief, ResearchBrief, and PRD with evidence-backed Assertions and structurally
   verifiable Requirements.
3. The PRD passes independent adversarial review with structured Findings (six-value disposition)
   under the explicit approval bar (§11); non-convergence pauses at `needs_human_judgment` and is
   resumable via the API (demonstrated by test).
4. **One full `init → new → framing → research → PRD → review` run against a real configured
   provider** (manual or CI-secret) completes; the fake provider is not the sole acceptance bar.
5. The Web UI shows the Initiative Home (with the §9 content model and states), Decisions (with
   dissent), Activity, and the reviewed PRD — with **no raw agent/run machinery on primary
   surfaces** (asserted by UI test).
6. The Current Understanding projection exposes, per statement: text, type, confidence, source,
   evidence digest, and provenance refs (`produced_by_run_id` + `provenance_json`) — and rebuilds
   identically from state/events after a daemon restart.
7. `idea status`, `idea changed`, `idea decisions` return `--json` output conforming to the API
   response schemas (schema/golden assertions).
8. No plaintext secrets are persisted anywhere — verified by the canary-secret leak sweep (§18).
9. Every local-API request without the token is rejected; project-scoped endpoints never return
   another project's data (security tests).

---

## 23. Adversarial review & resolutions

Five independent perspectives (CTO, Staff Product, Staff Security, Staff Quality, Head of
Design/UX) reviewed the draft. Blocking findings were consolidated and resolved as follows:

| # | Blocking finding (source) | Resolution |
|---|---|---|
| 1 | No auth token on the local API; trust-elevating/mutating endpoints open to any local process; impostor-daemon risk (Security) | §15: per-install token on every request, loopback-only bind, CLI handshake. |
| 2 | No per-request project scoping → cross-project leakage under "one daemon, many projects" (Security, CTO) | §7: project-scoped paths + scoped SSE + `fsutil` guard; isolation tests. |
| 3 | "Raw model output stored" contradicts redact-before-persistence; redaction sequenced last (Security, CTO) | §13–§15: single redaction choke point (store/blob/slog/SSE), post-redaction storage, T11 before T14. |
| 4 | Dissent (§8) has no data home/UI (CTO, Product, UX) | §6 `decision_dissents` + §9 Decisions render dissent; immutable on resolution. |
| 5 | Provenance is free text; no producer-run link; reviewer≠producer unverifiable (CTO, Product, Quality) | §6 structured `provenance_json` + `produced_by_run_id`; actor separation enforced. |
| 6 | Human-in-the-loop state machine undefined (approve/resume/disposition→transition) (CTO, Product, Quality, UX) | §11 full state machine + statuses + `resume`/`approve` endpoints. |
| 7 | Review loop has no pass bar; "approved" non-falsifiable (Quality) | §11 explicit approval bar (no open critical/high; all dispositioned). |
| 8 | "Vague acceptance criteria" deterministic check undefined/unimplementable (Quality, Security) | §11 structural-verifiability rule + deterministic pattern check + reviewer judgment for residual; golden fixtures. |
| 9 | Findings disposition vocabulary diverges from §15 six-value enum (Quality) | §6/§7/§9 pinned to `fixed/accepted_risk/not_applicable/duplicate/deferred/rejected`; `accepted_risk` human-only. |
| 10 | Acceptance bar is fake-provider-only; real model experience unvalidated (Product, Quality) | §22 exit criterion 4 + tagged real-provider smoke (§18). |
| 11 | "What Changed" is a raw event timeline (invariant d) (Product, CTO) | §7/§9 grouped narrated summary; timeline behind explicit drill-down; `read_cursors`. |
| 12 | Open Questions/Risks has no entity/API (Product, UX) | §6 `questions` table + §7 answer endpoint; §9 answer action. |
| 13 | Initiative Home is names-only; no content model/states; Runtime is machine-level with no expert-view framing (UX) | §9 content model + states + precedence; Runtime labeled expert view. |

Non-blocking findings (daemon lifecycle, `usage_json`, `read_cursors`, events digest-only, web-tool
SSRF controls, output sanitization, trust assignment, disposition actor constraints, reviewer model
independence, secrets env-only, exact Host/Origin matrix, file permissions, path-traversal guard,
dependency-edge fixes, UI test tier, review-economics metrics, no-silent-fallback test, leak sweep,
concurrency tests, e2e artifact assertions, per-task testing) were folded into the relevant sections
above.

---

*This plan is ready for approval. On approval, implementation proceeds as dependency-ordered
ChangeSets (M3-style ChangeSet rigor is not yet available in M1, but each task still ships as a
small, reviewed unit with its own tests).*
