# DX Audit 3 — documentation defects, below the cut

The audit's documentation dive ranked 15 findings and catalogued ~30 more. Track
R covered the ranked 15. **This file is the rest**, checked in so "Track R
addresses the documentation findings" is falsifiable by someone other than its
author.

Why it exists: audit 2 recorded its below-the-cut list in a plan paragraph and
lost it. One item — README's provider list omitting DeepSeek — was *still*
unfixed a round later, and reappeared in that round's plan as R13, presented as
new work. The mechanism was "whatever survives into a numbered row gets done;
whatever stays in prose is lost."

## The predicate column, and why it exists

Every row carries a **predicate**: a mechanical test that is true exactly when
the defect is still present. `make ledger-check` evaluates all of them and fails
on either kind of drift:

- an `open` row whose predicate is false — **stale-open**: the defect was fixed
  and nobody updated the row, which is how a ledger stops being read;
- a `fixed` row whose predicate is true — **regression**: the defect came back
  and the ledger says otherwise.

Predicate forms, all evaluated against the repository, all describing the
**defect** rather than the fix:

| Form | The defect is present when |
|---|---|
| `absent:<path>:<literal>` | `<literal>` does **not** appear in `<path>` — the correction is missing |
| `present:<path>:<literal>` | `<literal>` appears in `<path>` — the false claim is still there |
| `manual:<reason>` | not mechanically checkable; the check only asserts the row has a reason, and prints the count |

`absent:` is the stronger form and is preferred: a row fixed by *adding* a
correction regresses the moment somebody deletes that sentence, and the check
catches it. `present:` can only catch a verbatim reintroduction.

Every row in this file was re-verified against the tree on 2026-09-03 before its
status was changed. Twenty-one had been fixed by earlier rounds and never
recorded; six survived into audit 4 (A2, A6, H1, H2, M1, I1) and were fixed
there — H1/H2 with the security-claim work, the rest with this file. The predicates are what make that re-verification repeatable
rather than a claim.

Status values: `open`, `fixed <commit>`, `deferred <reason>`.

## docs/ui.md — rewritten wholesale by audit 3's Track O

Every row here was subsumed by the rewrite. They are listed with predicates so
the rewrite can be *checked* against them rather than trusted, which is the whole
reason the file survived the round.

| # | Claim | Truth | Predicate | Status |
|---|---|---|---|---|
| U1 | Top bar with a project selector | No top bar; project is hardcoded | `absent:docs/ui.md:There is no project switcher` | fixed 0213fa3 |
| U2 | "Blocked"/"locked" stepper states | Not in `phases.ts` | `absent:docs/ui.md:There are no blocked or locked states` | fixed 0213fa3 |
| U3 | Counts under the stepper | `PhaseNav` renders none | `absent:docs/ui.md:no counts are rendered under the rail` | fixed 0213fa3 |
| U4 | Trace link opens a trace view | Goes to the health page (documented as such in 0213fa3; the link itself now opens the document's lineage route) | `absent:web/src/ExplorePage.tsx:lineageHref(initiativeId, "context_doc"` | fixed ui-review |
| U5 | "✓ complete" badge / "needs manual" flag on verify | Neither exists | `absent:docs/ui.md:A coverage meter` | fixed 0213fa3 |
| U6 | Git SHA rendered on the build page | Never rendered | `absent:docs/ui.md:commit SHA is not surfaced anywhere in the UI` | fixed 0213fa3 |
| U7 | Demo banner is global | Settings-only (later: both, twice on Settings; now the shell's one banner on every page) | `absent:docs/ui.md:on every page, a **Demo mode** banner` | fixed ui-review |
| U8 | Build-done criterion | Does not match `phases.ts` | `absent:docs/ui.md:at least one change set and` | fixed 0213fa3 |
| U9 | Context docs enter one stage | They enter every stage | `absent:docs/ui.md:not into a single stage` | fixed 0213fa3 |
| U10 | "Live activity feed via SSE" | No SSE client in `web/src`; the UI polled on timers. (Since the live run view the shell follows the stream; ui.md says which pages still poll.) | `absent:docs/ui.md:most pages still poll` | fixed 0213fa3 |

## docs/quickstart.md

| # | Claim | Truth | Predicate | Status |
|---|---|---|---|---|
| Q1 | "the scripted demo waits 3" | No scripted demo exists in `scripts/` | `present:docs/quickstart.md:scripted demo` | fixed 6b275f5 |
| Q2 | "the Context tab" | Explore → Grounding | `absent:docs/quickstart.md:Explore → Grounding` | fixed 6b275f5 |
| Q3 | Decisions can be resolved from the CLI | True as of the round that shipped `answer`/`resolve`/`reopen-decision`/`accept-recommendations`/`triage` (`internal/cli/cli.go`) — the H9 release-gate finding was that this file and `README.md` still said, in bold, that no such verb existed, weeks after it shipped | `absent:docs/quickstart.md:Decisions and questions can be resolved from the CLI` | fixed icp-v06-h9 |
| Q4 | `idea docs add` shown before `idea new` | Requires an initiative to exist | `absent:docs/quickstart.md:needs an initiative to attach to` | fixed 6b275f5 |
| Q5 | Review-budget semantics | Contradicted `configuration.md`; both were wrong | `absent:docs/configuration.md:coerces any value` | fixed 6b275f5 |

## docs/api.md

| # | Claim | Truth | Predicate | Status |
|---|---|---|---|---|
| A1 | "Three cases" for `writeErr` | There are four | `present:docs/api.md:Three cases` | fixed 6b275f5 |
| A2 | "the one list endpoint with an envelope" | `/models` wraps its list too | `absent:docs/api.md:the only *collection* endpoint with an envelope` | fixed 046b3f4 |
| A3 | doc-upload row omits `.gif`/`.webp` | `docparse` accepts them | `absent:docs/api.md:jpg/png/gif/webp` | fixed 0213fa3 |
| A4 | `artifacts/diff` "Structured diff" | Unified line diff | `absent:docs/api.md:Unified line diff` | fixed 6b275f5 |
| A5 | diff response schema omits the `files` key | Handler writes it | `absent:docs/openapi.yaml:is the same change set as a` | fixed 6b275f5 |
| A6 | verify's `fail`/`waived` | In `openapi.yaml`; `api.md` named only `pass` | `absent:docs/api.md:takes the first of` | fixed 046b3f4 |

## docs/architecture.md

| # | Claim | Truth | Predicate | Status |
|---|---|---|---|---|
| H1 | "redaction before Put" | Redaction is at read; the raw store is deliberately redaction-free | `absent:docs/architecture.md:redacts on Put` | fixed 9152317 |
| H2 | Data tree omits `raw-blobs/` | The redaction-free store was not shown | `absent:docs/architecture.md:deliberately NOT redacted` | fixed 9152317 |

## docs/security-model.md

| # | Claim | Truth | Predicate | Status |
|---|---|---|---|---|
| M1 | `path_escape` / `trust_deny` documented as enforced | Declared in the enum, produced by nothing | `absent:docs/security-model.md:declared, never produced` | fixed 046b3f4 |

## docs/configuration.md / docs/agents.md

| # | Claim | Truth | Predicate | Status |
|---|---|---|---|---|
| C1 | `max_cycles` default condition | Wrong | `absent:docs/configuration.md:is **not** unlimited` | fixed 6b275f5 |
| C2 | Agent key read from the project `.env` | It is not | `absent:docs/agents.md:does not reach the agent` | fixed 6b275f5 |

## internal (comments, not published docs)

| # | Claim | Truth | Predicate | Status |
|---|---|---|---|---|
| I1 | `routes.go` comment says the flat routes are still live | They were removed; `api.md` is right | `present:internal/daemon/routes.go:Both forms are live during the migration` | fixed 046b3f4 |

## Not yet transcribed

The audit's dive listed a handful more below these, in the rendered report at
https://claude.ai/code/artifact/d556ab02-6d80-48ee-a6de-3f61c97b3766. Anything
found later that is not in this table should be appended here rather than fixed
silently, so the count stays checkable.
