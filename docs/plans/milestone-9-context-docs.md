# Milestone 9 — Context docs (upload → Evidence → agent context)

Ground the agents in the user's own material: upload a PRD, design system, tech
design doc, or market/competitive analysis at project start; ingest it as
redacted, content-addressed `Evidence`; feed it into framing/research/PRD; and
surface it with provenance.

## Store decision (no new store, no vectors for v1)

- **Bytes/text** → the existing redacted blob store (content-addressed), exactly
  like artifact bodies. Redaction on ingest so a pasted secret never persists.
- **Metadata** → a new `context_docs` table in SQLite (id, initiative_id, source
  kind, title, digest, text preview, created_at). NOTE: "Evidence" is NOT yet a
  first-class node — the Product Graph currently models evidence as `Assertion`
  (with `evidence_digest` + `source`) and `VerificationEvidence`. The plan must
  decide: a dedicated `context_docs` table (recommended) vs. a new `Artifact`
  kind vs. a new `Evidence` node type. Recommend `context_docs` + optionally
  emit an `Assertion` (type `evidence`) per doc so provenance/`why` works.
- **No tokenization/embeddings in v1.** The corpus is small enough to fit in the
  model context. Design the table so a `chunks`/`embedding` table can be added
  later (FTS5 now, SQLite-vec later) without migration churn.

## Scope

### A. Ingest (CLI + API)
- `idea new "<idea>" --doc <path.md|.txt> [--doc …]` (Markdown + plain text for v1;
  PDF/DOCX deferred).
- Daemon: `POST /projects/{id}/initiatives/{id}/docs` (multipart) and/or a
  "Context docs" upload in the UI. Ingest → redact → blob → `Evidence` row.

### B. Context wiring
- Compile uploaded docs into the model context for `runFraming`, `runResearch`,
  and `runProductDefinition`, tagged by source (mirroring how `researchEvidence`
  injects URL content). Redacted before it reaches the model (egress).

### C. Provenance + surface
- Uploaded docs are `Evidence` nodes; the "Context" tab/panel lists them; `idea
  docs` (or `idea context`) lists them; each is downloadable. `why`/`trace` can
  walk from evidence to the requirements/assertions that cite it (auto-attribution
  of requirement→doc is a stretch goal, not v1).

### D. Security (reuse what we built)
- Untrusted upload → redacted blob store + secret sweep on ingest; never served
  or fed to the model unredacted. Size caps + a bounded number of docs.

## Out of scope
- PDF/DOCX parsing; embeddings/vector retrieval; chunking; auto requirement→doc
  attribution.

## Definition of done
- A–D implemented; `go vet ./...`, `go test ./...`, `scripts/smoke.sh`,
  `cd web && pnpm build && pnpm test` green.
- Plan reviewed (adversarial) before implementation.

---

## Plan review corrections (v2)

Three-perspective review (Product, Security, Architecture): one blocker, several
highs. Corrections adopted below; they supersede the v1 text.

### Terminology + data model (corrected)
- **There is NO `Evidence` node.** Drop "Evidence node" phrasing. Uploaded docs are
  **`context_docs` rows + redacted blobs**, surfaced with provenance via
  `graph_edges` (later) — NOT `Assertion(type=evidence)` (the Assertion enum is
  `{fact,assumption,hypothesis}` and `CreateAssertion` hard-rejects anything else).
- `context_docs` table: id, initiative_id, source kind, title (sanitized),
  digest, text preview, created_at, a `trust`/`human_approved` flag.

### Ingest ordering (corrected — Product high)
- `idea new --doc` races the async `Run`. **Ingest atomically BEFORE `Run` starts**
  (create-with-docs operation), AND add a standalone `idea docs add <path>` /
  `idea context add` that re-ingests and re-runs research/PRD for existing
  initiatives. Paste-text fallback (`--doc -` / stdin, and a UI textarea) so
  non-Markdown material is still usable without a PDF/DOCX parser.

### Context wiring (corrected — Architecture high)
- Use a **distinct selector `context_docs`** — `ccontext.Compile` is
  first-match-per-selector and `untrusted` is already taken by `researchEvidence`.
  Register `context_docs` in `priorityFor` (priority 12) and `TrustFor`
  (`TrustExternalUntrusted`) so `frameUntrusted` wraps it. Inject into
  `runFraming`, `runResearch`, and `runProductDefinition`.

### Egress redaction (corrected — Security blocker)
- `researchEvidence` injects RAW text (redaction only happens at `blob.Put`).
  For docs: **`e.redactor.RedactString()` each doc's content AND title BEFORE it
  enters a `ccontext.Input`**, and run an **ingest-time secret sweep over the raw
  bytes** (`ContainsCredential`) with an explicit policy: reject (400 + non-echoing
  `secret_sweep` finding) rather than silently `[REDACTED]`. Canary test: a secret
  in a doc never appears in the assembled system prompt / never reaches `prov.Complete`.

### Upload handling + budget (corrected — Security high)
- `http.MaxBytesReader`, small `ParseMultipartForm` maxMemory, per-file
  `io.LimitReader` cap (≤2 MiB, mirror the fetch tool), aggregate byte cap + max
  doc count, `.md`/`.txt` allowlist (content-sniffed). Sanitize the filename/title
  (HTML-escape + redact) before storing/rendering.
- **Input-token guard**: per-doc and aggregate byte/token budget + deterministic
  truncation BEFORE it enters `ccontext.Input`; reject oversized docs at ingest.
  (`Review.MaxTokens` is output-only and does NOT protect input/cost.)

### Provenance + prompt-injection (corrected — medium)
- v1 provenance: emit a graph edge (`EdgeDerivedFrom`/`EdgeSupports`) from
  requirement/assertion → `context_doc` node + a `labelCache` entry so `why`/`trace`
  resolves a label — OR defer provenance entirely and say so.
- Docs are a first-class **prompt-injection** vector: inject with the untrusted
  framing (above) and document the residual risk as a known accepted risk with the
  untrusted-framing mitigation; defer instruction isolation.
