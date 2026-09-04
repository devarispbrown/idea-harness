# DX Audit 3 — documentation defects, below the cut

The audit's documentation dive ranked 15 findings and catalogued ~30 more. Track
R covers the ranked 15. **This file is the rest**, checked in so "Track R
addresses the documentation findings" is falsifiable by someone other than its
author.

Why it exists: audit 2 recorded its below-the-cut list in a plan paragraph and
lost it. One item — README's provider list omitting DeepSeek — is *still*
unfixed, and reappeared in this round's plan as R13, presented as new work. The
mechanism was "whatever survives into a numbered row gets done; whatever stays in
prose is lost."

Status values: `open`, `fixed <commit>`, `deferred <reason>`.

## docs/ui.md — Track O rewrites this file wholesale (O3)

Every row here is subsumed by the rewrite; listed so the rewrite can be checked
against them rather than trusted.

| # | Claim | Truth | Status |
|---|---|---|---|
| U1 | Top bar with a project selector | No top bar; project is hardcoded | open |
| U2 | "Blocked"/"locked" stepper states | Not in `phases.ts` | open |
| U3 | Counts under the stepper | `PhaseNav` renders none | open |
| U4 | Trace link opens a trace view | Goes to the health page | open |
| U5 | "✓ complete" badge / "needs manual" flag on verify | Neither exists | open |
| U6 | Git SHA rendered on the build page | Never rendered | open |
| U7 | Demo banner is global | Settings-only | open |
| U8 | Build-done criterion | Does not match `phases.ts` | open |
| U9 | Context docs enter one stage | They enter every stage | open |
| U10 | "Live activity feed via SSE" | No SSE client in `web/src`; the UI polls on timers | open |

## docs/quickstart.md

| # | Claim | Truth | Status |
|---|---|---|---|
| Q1 | "the scripted demo waits 3" | No scripted demo exists in `scripts/` | open |
| Q2 | "the Context tab" | Explore → Grounding | open |
| Q3 | Decisions can be resolved from the CLI | No CLI path resolves a decision | open |
| Q4 | `idea docs add` shown before `idea new` | Requires an initiative to exist | open |
| Q5 | Review-budget semantics | Contradicts `configuration.md`; both wrong | open (R10) |

## docs/api.md

| # | Claim | Truth | Status |
|---|---|---|---|
| A1 | "Three cases" for `writeErr` | There are four | open (R8) |
| A2 | "the one list endpoint with an envelope" | Stale after the audit-2 decision to keep it | open |
| A3 | doc-upload row omits `.gif`/`.webp` | `docparse` accepts them | open (R14) |
| A4 | `artifacts/diff` "Structured diff" | Unified line diff | open (R6) |
| A5 | diff response schema omits the `files` key | Handler writes it | open (R7) |
| A6 | verify's `fail`/`waived` | Now in `openapi.yaml`; api.md still thin | open |

## docs/architecture.md

| # | Claim | Truth | Status |
|---|---|---|---|
| H1 | "redaction before Put" | Redaction is at read; the raw store is deliberately redaction-free | open (S3) |
| H2 | Data tree omits `raw-blobs/` | The redaction-free store is not shown | open (S3) |

## docs/security-model.md

| # | Claim | Truth | Status |
|---|---|---|---|
| M1 | `DenyCodePathEscape` / `DenyCodeTrustDeny` documented | Declared in the enum, never produced | open |

## docs/configuration.md / docs/agents.md

| # | Claim | Truth | Status |
|---|---|---|---|
| C1 | `max_cycles` default condition | Wrong | open (R11) |
| C2 | Agent key read from the project `.env` | It is not | open (R12) |

## internal (comments, not published docs)

| # | Claim | Truth | Status |
|---|---|---|---|
| I1 | `routes.go` comment says the flat routes are still live | They were removed; `api.md` is right | open (R15) |

## Not yet transcribed

The audit's dive listed a handful more below these, in the rendered report at
https://claude.ai/code/artifact/d556ab02-6d80-48ee-a6de-3f61c97b3766. Anything
found later that is not in this table should be appended here rather than fixed
silently, so the count stays checkable.
