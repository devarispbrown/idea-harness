# Milestone 1 — Acceptance Criteria & Demo Verification

**Milestone 1 = Idea → Reviewed PRD** (PRD v1.1 §50, constitution §66).

This is what "done" means for M1, and exactly how to verify the demo.

## Acceptance criteria

1. **Empty-directory start.** In a folder with no Git repo, `idea init` succeeds and creates
   `.idea-harness/` (no Git init, no remote, no push).
2. **`idea new` produces a reviewed PRD.** Given an idea, the workflow produces an IdeaBrief,
   ResearchBrief, and PRD with **structured, verifiable Requirements** and evidence-backed
   Assertions — through the deterministic path end to end.
3. **Independent adversarial review.** The PRD passes review with structured Findings using the
   6-value disposition vocabulary; non-convergence pauses at `needs_human_judgment` (resumable).
4. **Current Understanding is a provenance-backed projection** — not a separate source of truth,
   and it rebuilds identically from state after a daemon restart.
5. **Decisions preserve dissent** (§8); open decisions/questions surface as "Needs Your Judgment."
6. **Calm UI.** Initiative Home (Current Understanding → Recommended Next Move → What Changed →
   Requirements → Decisions → Artifacts → Review) with no raw agent/run machinery on primary
   surfaces; live updates via SSE.
7. **CLI parity.** `status`, `changed`, `decisions`, `explain`, `prd`, `export`, `model`, `logs`,
   `doctor` all work; state commands support `--json`.
8. **Security.** Unauthorized local-API requests rejected (token); project isolation; no plaintext
   secrets persisted (redaction; `.env`/env keys only).
9. **Tests green.** `go test ./...` and `go vet ./...` clean.

## Demo script (do this to verify)

Two terminals recommended.

**Terminal A — build + run the daemon in the foreground (see live logs):**

```bash
cd idea-harness
go install ./cmd/idea          # or: make build
export PATH="$PATH:$(go env GOPATH)/bin"

mkdir -p /tmp/idea-demo && cd /tmp/idea-demo
idea init                      # empty dir, no git
idea daemon                    # leave running — watch per-request logs here
```

**Terminal B — drive the demo:**

```bash
cd /tmp/idea-demo

# (optional) configure a real provider; skip for the synthetic demo
idea model set ollama/llama3.1     # or openai/gpt-4o-mini, deepseek/deepseek-chat, …

idea new "Build a platform that lets someone buy a domain and get a matching email address"
idea watch                        # live spinner → should end "approved"
idea explain                      # synthesized Current Understanding
idea prd                          # the reviewed PRD, rendered
idea export prd > prd.md          # get it into the working tree
idea status --json                # machine-readable state
idea changed                      # What Changed (product-level summary)
idea decisions                    # open decisions (with Accept / Choose alternative)
idea logs 30                      # daemon log (per-request trace)
```

**Open the web UI:** `idea dev` (or `http://127.0.0.1:8787/`). Verify:

- Initiative Home shows Current Understanding, Recommended Next Move, Requirements, Decisions,
  Artifacts, Review Findings, and a **Live activity** feed.
- **Artifacts → View** opens the PRD/ResearchBrief/IdeaBrief.
- **Settings → Model** lists providers and lets you switch + load live models.
- ⌘K opens the command palette.

## What the demo does NOT verify (needs your environment)

- **Real-model quality** — the fake/demo provider is synthetic; a real provider needs an API key
  (`OPENAI_API_KEY` etc. or `.env`). `scripts/smoke-real.sh` runs the same slice against a live
  model: `IDEA_REAL_MODEL=deepseek/deepseek-chat bash scripts/smoke-real.sh`.
- **Research with real sources** — enable `network: enabled` + `research.urls` in `.idea-harness/harness.yaml`.

## Known M1 limitations (by design)

- No Product Graph edges, staleness, semantic diff, or `why/trace/impact` (M2/M4).
- No sandbox, executor (OMP), or ChangeSets (M3).
- Research falls back to "model knowledge, unverified" assumptions when offline (correct per §14).
