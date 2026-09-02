# Handoff: idea-harness UI redesign

## Overview

A redesign of the idea-harness web UI — the local-first control plane that takes an idea from
ambiguity to verified software through six phases (Explore, Define, Design, Plan, Build, Verify).

The existing UI (`web/src/App.tsx`) is a single-page app with an eleven-tab row inside one
initiative. The redesign replaces that with a phase-driven information architecture: **the six phases
ARE the navigation**, each phase page shows what it produced and what it needs from the user, and the
product graph (provenance/impact) is surfaced as a hover popover on any id rather than a dedicated
page.

## About the design files

The files in this bundle are **design references created in HTML** — prototypes showing intended look
and behavior, **not production code to copy**. They are Design Components (`.dc.html`), which are
HTML files with a small runtime for templating; the runtime is irrelevant to the implementation.

The task is to **recreate these designs in the existing codebase**: React 18 + TypeScript + Vite,
plain CSS in `web/src/styles.css`, talking to the Go daemon over REST + SSE. Follow the codebase's
established patterns (`get`/`put` from `web/src/api.ts`, the existing SSE subscription, CSS custom
properties in `:root`, the `statusChips.ts` helper). Do not introduce a CSS framework or component
library.

## Fidelity

**High-fidelity.** Colors, typography, spacing, and copy are final. Recreate pixel-for-pixel using the
tokens in `Design System.dc.html`, which is the normative reference — where a mockup and the design
system disagree, the design system wins.

The design is responsive across four breakpoints (see "Responsive"). Build every screen responsive as
you go, not as a pass at the end.

The mockups use inline styles because of how they were authored. **Do not carry that over.** Lift the
values into CSS custom properties and classes following the existing `styles.css` conventions.

## Screens

Each maps to a card id in `Idea Harness Mockups.dc.html`. Open that file in a browser and use the
badge ids to navigate. **Turns 5–8 are the current design.** Turns 0–4 are earlier explorations kept
for context; where they disagree with a later turn, the later turn wins.

| Card | Screen | Route | Purpose |
|---|---|---|---|
| 5a | Product home | `/` | All initiatives in the project, with phase progress |
| 8a | Phase — Explore | `/i/:id/explore` | Context-doc grounding + produced briefs |
| 5b | Phase — Explore, running | `/i/:id/explore` | An expert is working: spinner, elapsed, live log, Stop |
| 5c | Phase — Define | `/i/:id/define` | Judgment items + commit bar (the core screen) |
| 5d | Phase — Design | `/i/:id/design` | Multi-document phase, one review verdict |
| 5e | Phase — Plan | `/i/:id/plan` | Task list, approve-or-revise |
| 4b | Phase — Build | `/i/:id/build` | Tasks + their change sets, diff review inline |
| 8c | Build — change-set states | `/i/:id/build` | All 11 statuses, revert, system deny |
| 8b | Phase — Verify | `/i/:id/verify` | **Criterion-level evidence recording (correct model)** |
| 8e | Decision discussion | `/i/:id/decision/:did` | Discussion thread + What Changed panel |
| 8f | Graph health | `/i/:id/health` | Risk tier + traceability audit + daemon status |
| 6d | Document reader | `/i/:id/doc/:artifact/:version` | Reading any artifact |
| 8d | Settings | `/settings` | Model, providers, **executor**, apply |
| 6a | Empty states | — | No initiatives; phase not started |
| 6b | Error states | — | Daemon down; model failed; missing key |
| 6c | Overflow | — | 38-task plan, 61-file diff |
| 7a–7c | Responsive | — | Laptop 1000px, tablet 760px, phone 390px + phase sheet |
| 0a | **Current UI** | — | Recreation of the old UI, for comparison only |

### Correction: card 8b supersedes 4c and 3d

The earlier Verify mockups (4c, 3d) showed test names, pass counts, and failure output
(`test_invoice_proration FAILED`). **The daemon does not produce that.** Verification is
evidence-based and human-driven: `POST /verify` with `{requirement, pass, note}` records a
`verification_evidence` row (kind: acceptance/security/ux; status: pass/fail/blocked/needs_manual),
and coverage is passed-criterion-keys over total. Build **8b**, not 4c.

## Components

Ten recurring components. Full spec with states in `Design System.dc.html`.

| Component | States |
|---|---|
| PhaseNav | 6 phases × done/current/future; inbox 0–n (hidden at 0) |
| PhaseHeader | at rest · running · waiting on you · approved · not started |
| DocumentRow | ready · drafting · superseded |
| JudgmentCard | decision / finding / question × open / resolved |
| CommitBar | always live — never disabled |
| TaskRow | proposed · ready · running · blocked · done · cancelled |
| DiffView | checks passing · failing · truncated (>20 files) |
| IdChip | decision / requirement / finding / task / assertion + lineage popover |
| StatusPill | 11 variants |
| InitiativeRow | needs you · running · shipped · paused |

## Design rules

These are load-bearing. Violating them breaks the IA.

1. **The phases are the navigation.** Six phases, one optional cross-initiative inbox, settings.
   Nothing else in the sidebar. New surfaces go inside the phase that owns them.
2. **One advancing action per screen** — one indigo button, in the commit bar, labeled with its
   consequence ("Revise PRD to v4", not "Submit").
3. **No disabled controls, ever.** Either the action is absent, or it is live and its label explains
   what happens to the unresolved remainder.
4. **Lineage is a popover, never a page.** There is no graph screen.
5. **Provenance reads as prose** — "Research found X, which required Y, leaving Z unsettled."
6. **Mono means machine-generated** — ids, timestamps, paths, counts, diffs, digests, model names.
7. **Completed phases state what they produced** — "PRD v4 approved", not a checkmark.
8. **Running is never silent** — spinner, elapsed time, live log, Stop, safe-to-leave.
9. **The inbox is cross-initiative, hidden when empty**, and never duplicates the current page.
10. **System deny is not a permission error.** A `DenyCode` finding cannot be dispositioned,
    overridden, or approved past. Render it as a fact about the change set, with no action that
    implies the user could allow it.

## API surface that already exists

Verified against `internal/daemon/routes.go`. Use these; do not invent parallel endpoints.

**Projects / initiatives**
```
GET    /api/v1/projects                                        list projects
POST   /api/v1/projects                                        create (root_path)
GET    /api/v1/projects/{p}                                    one project
GET    /api/v1/projects/{p}/initiatives                        list initiatives
POST   /api/v1/projects/{p}/initiatives                        create (title, description, docs[])
GET    /api/v1/projects/{p}/initiatives/{i}                    one initiative
GET    …/{i}/understanding                                     current-understanding projection
GET    …/{i}/next-move                                         recommended next move
GET    …/{i}/next                                              deterministic next action
GET    …/{i}/changed                                           What Changed since the UI read cursor
GET    …/{i}/diff                                              semantic requirement/decision diff
POST   …/{i}/resume | approve | revise | advance | execute | verify
POST   …/{i}/suggest-answers
GET    /api/v1/projects/{p}/suggested-answers
```

**Graph / judgment**
```
GET    …/{i}/why | impact | trace        ?type=&id=   provenance for ONE node
GET    /api/v1/projects/{p}/requirements | assertions | decisions | questions | findings
POST   …/decisions/{id}/resolve                        (resolution)
GET    …/decisions/{id}/discussion
POST   …/decisions/{id}/discuss                        (message) → AI reply
POST   …/questions/{id}/answer                         (answer)
POST   …/findings/{id}/disposition                     7 values, see below
POST   …/findings/{id}/note
GET    /api/v1/projects/{p}/doctor                     traceability audit
GET    /api/v1/projects/{p}/risk                       deterministic risk profile
```

**Artifacts / context docs**
```
GET    /api/v1/projects/{p}/artifacts
GET    …/artifacts/{id}/body                           sanitized markdown
GET    …/artifacts/{id}/raw                            download .md
POST   …/{i}/docs                                      JSON paste OR multipart file
GET    …/{i}/docs                                      list context docs
GET    …/{i}/docs/{docId}                              download (image = raw bytes)
```

**Build / change sets**
```
GET    …/{i}/tasks
GET    /api/v1/projects/{p}/change-sets
GET    …/change-sets/{id}
GET    …/change-sets/{id}/diff                         → { patch: "<one string>" }
POST   …/change-sets/{id}/review                       (verdict)
POST   …/change-sets/{id}/apply
POST   …/change-sets/{id}/revert
```

**Config / ops**
```
GET    /api/v1/projects/{p}/providers   PUT (default_model, providers, models, git_commit)
GET    /api/v1/projects/{p}/models      ?provider=
GET    /api/v1/projects/{p}/executors   PUT (kind, image, model)
GET    /api/v1/projects/{p}/activity
GET    /api/v1/projects/{p}/events                     SSE
GET    /api/v1/operations/{id}
POST   /api/v1/operations/{id}/cancel                  ← powers every Stop button
GET    /api/v1/health | version | logs
```

**Controlled vocabularies to render completely** (from `internal/domain/enums.go`):

- ChangeSetStatus — 11 values: draft, checks_running, review_required, rejected, changes_requested,
  superseded, approved, applying, applied, reverted, conflicted. Card 8c shows the ones a user meets.
- Disposition — 7 values: fixed, accepted_risk, not_applicable, duplicate, deferred, rejected,
  mitigate. **The mockups only show four buttons.** Put the remaining three behind the "…" overflow.
- TaskStatus — proposed, ready, blocked, done, cancelled.
- VerificationStatus — pass, fail, blocked, needs_manual. AssertionStatus — candidate, accepted,
  disputed, retracted, superseded. RiskTier — low, medium, high, critical.
- Severity — critical, high, medium, low, observation; `critical` and `high` block approval.
- DenyCode — secret_sweep, pii, path_escape, host_exec, blob_digest_mismatch, trust_deny,
  review_open_blocking. See rule 10.

## Backend gaps

Everything below is UI in the mockups with no endpoint behind it. Ordered by how much design breaks
without it. **Several items from the previous handoff are now resolved** — verification evidence,
operation cancel, decision discussion, suggested answers, revert, risk, doctor, and context docs all
exist and are designed against in turn 8.

### Blocking — the designed screen cannot work

| # | Need | Used by | Notes |
|---|---|---|---|
| 1 | **Task mutations** — reorder, edit, add, delete, cancel a task | 5e Plan | The plan is presented as editable before approval. No endpoint exists; `/tasks` is read-only. |
| 2 | **Plan revision request** — "Ask for a revision" on the execution plan | 5e Plan | `/revise` regenerates the PRD only. |
| 3 | **Check results per change set** — test/vet outcomes, pass counts | 4b, 6c, 8c | `checks_running` is a status with no results endpoint. Cards show "tests 41/41 · go vet clean". |
| 4 | **Conflict resolution** — act on a `conflicted` change set | 8c | The status exists; nothing can move it. |
| 5 | **Read-cursor write** — mark What Changed as seen | 8e | `/changed` reads `GetReadCursor("ui")`; no endpoint sets it, so the panel can never clear. |

### Important — degrades the design

| # | Need | Used by | Notes |
|---|---|---|---|
| 6 | **Structured per-file diff** | 4b, 6c | `/change-sets/{id}/diff` returns one `patch` string. Per-file headers, line numbers, and +/− counts must be parsed client-side, or better, returned structured. |
| 7 | **Cross-initiative attention query** | 5a, PhaseNav inbox | Requires fanning out over initiatives × (decisions, questions, findings). N+1 on every page load. |
| 8 | **Batch impact counts** — "blocks 3" per open item | 5c, 8b, task rows | `/impact` is one node per call. |
| 9 | **Artifact version diff (v2 ↔ v3)** | 5c, 6d | `/{i}/diff` is a semantic requirement/decision diff, not artifact text. The "Diff v2 → v3" button has nothing to call. |
| 10 | **Run timing** — started_at / elapsed on operations and SSE | 5b, PhaseNav | Powers "40s ago" and "done in 2m 10s". |
| 11 | **Waive or fail a requirement with a note** | 8b | `/verify` records only when `pass: true`. `fail`, `blocked`, and `needs_manual` exist in the enum but cannot be written by a human. |
| 12 | **Undo a resolved decision** | 3a, 5c | `ResolveDecision` has no inverse. Either add one or **remove the Undo affordance** — do not leave a control that silently fails. |

### Nice to have

| # | Need | Used by |
|---|---|---|
| 13 | Document comments anchored to a section, promoted to questions | 6d |
| 14 | Generate verification tasks for an uncovered requirement | 8b |
| 15 | "Reopen this phase" with staleness cascade | 5d |
| 16 | Retry a failed run with a different model | 6b |
| 17 | Per-phase summary strings ("PRD v4 approved") | PhaseNav — derivable client-side from status + artifacts; define the mapping once |

**Recommendation:** items 1–5 before building Plan, Build, or the What Changed panel. Items 6–12 can
be worked around client-side, but 6 and 7 will cost real performance at N+1, and 12 must be either
built or removed from the UI.

## Interactions

- **Lineage popover** — hover or focus an IdChip, 150ms in, 300ms out; dismiss on Escape or scroll.
  Calls `/why` + `/impact` for that one node. Chips are focusable; popover content is reachable.
  On phone it becomes a bottom sheet (hover does not exist).
- **Commit bar** — `position: sticky; bottom: 0`; single shadow `0 -4px 16px rgba(28,25,23,.05)`.
- **Running state** — spinner `animation: spin .8s linear infinite`. Live log appends from the SSE
  stream, ~8 visible lines, scrolls internally. Stop calls `POST /operations/{id}/cancel`.
- **Context upload** — drag-drop or browse; multipart to `POST /{i}/docs`. A credential rejection
  returns 400 and **must not echo the content**. Images return immediately and describe async, so the
  row needs a "describing…" state that resolves over SSE.
- **Diff truncation** — above 20 files, show the most-changed 3–6 with "show all", ranked by lines
  changed, deprioritizing generated fixtures and snapshots.
- **Phase transitions** — no animation, content swap only.

## Responsive

Four breakpoints, all mocked in turn 7. The phase rail is the navigation, so it never simply
collapses away — at every width the user must see which phase they are in and be able to jump.

| Width | Card | Sidebar treatment |
|---|---|---|
| ≥ 1180px | 5a–6d, 8a–8f | Full sidebar, 232px — the reference design |
| 1000–1179px | 7a | Sidebar narrows to 168px; phase notes and the model chip drop |
| 760–999px | 7b | Sidebar becomes a horizontal six-column phase strip under the header |
| < 760px | 7c | Status bar ("Define · step 2 of 6 · waiting on you" + Change ▾) opening a bottom-sheet phase picker |

**Phone rules (< 760px)**

- Every tap target ≥ 44px. Judgment actions stack full-width; primary on its own row, secondaries
  two-up.
- Commit bar pins to the viewport, not the document, with its explanatory line above the button.
- Bottom sheet lists all six phases at 52px per row, current one tinted, plus "All initiatives" and
  "Settings" in a footer row.
- Two-column layouts (discussion + What Changed, master/detail change sets) become one column with
  the detail as a full-screen view and a back affordance.
- Diffs switch to unified; the file list collapses behind a count.
- The lineage popover becomes a bottom sheet.

Test at 390px, 768px, 1024px, and 1440px.

## Accessibility notes

Not audited in the mockups. Before shipping: focus states on every control (the mocks show none), the
lineage popover keyboard-reachable, status never conveyed by color alone (most pills pair color with a
word — keep that), text equivalents for the diff +/− markers, and a keyboard path for task reorder
once item 1 exists.

## Design tokens

Normative source: `Design System.dc.html`. Summary:

**Surface & text**
```
--canvas:      #faf9f7    app background
--surface:     #ffffff    cards, sidebar
--sunken:      #f5f4f1    inset, hover rows
--line:        #e8e6e1    all borders
--line-soft:   #f0eeea    dividers inside cards
--text:        #1c1917    headings, primary
--text-body:   #44403c    body, button labels
--text-muted:  #78716c    secondary copy
--text-faint:  #a8a29e    metadata, mono
--accent:      #4f46e5    the advancing action
--accent-bg:   #eef2ff    active nav, current phase
--accent-line: #c7d2fe    accent borders
```

**Semantic states** (fg / bg / border)
```
needs you    #92400e / #fffbeb / #fde68a
running      #3730a3 / #f8f8ff / #c7d2fe
verified     #166534 / #ffffff / #bbf7d0
failing      #991b1b / #fef2f2 / #fecaca
unverified   #92400e / #ffffff / #e8c96e   (dashed border)
inactive     #78716c / #faf9f7 / #e8e6e1
system deny  #ffffff on #1c1917 chip, #fef2f2 / #fecaca card
```

**Type** — system sans; `ui-monospace, Menlo, monospace` for machine-generated text.
```
page-title   20px / 650 / -.02em
section      14px / 650
item-title   13.5px / 600
body         12.5px / 400 / 1.55
meta         11.5px mono
eyebrow      10.5px / 650 / .07em / uppercase
chip         10.5px mono / 600
```

**Spacing** 4, 8, 12, 18, 24, 34px · **Radius** 5 (chips), 7 (buttons/inputs), 12 (cards), 999 (pills)

**Elevation** — borders do the work. Two shadows in the entire product:
`0 -4px 16px rgba(28,25,23,.05)` (commit bar), `0 10px 30px rgba(28,25,23,.28)` (popover).

## Assets

None. No images, no icon library. The glyphs (✓ ✕ ▶ ! ? ○ ◉ ⠿ ↗ ✨ ⋯) are text characters. The "ih"
logo is a 22px rounded square, `#4f46e5`, letters in 11px mono 700 white.

## Files in this bundle

- `Idea Harness Mockups.dc.html` — all screens by turn (newest first). **Turns 5–8 are current.**
- `Design System.dc.html` — tokens, components, rules. **Normative.**
- `PhaseNav.dc.html` — the sidebar component used by turns 4–8.
- `Sidebar.dc.html` — earlier sidebar for turns 2–3. Superseded; included so those turns render.
- `support.js` — the Design Component runtime. Required to open the HTML. Not part of the design.

Open the HTML files directly in a browser. No build step, no server.
