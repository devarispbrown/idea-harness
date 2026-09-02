# Prompt for Claude Code

Unzip the handoff bundle into the repo first:

    ~/code/idea-harness/design_handoff_idea_harness_ui/

Then paste everything below into Claude Code.

---

Read `design_handoff_idea_harness_ui/README.md` in full, then `Design System.dc.html` in that same
folder. Both are design references, not code to copy — the `.dc.html` format is a prototyping runtime
that is irrelevant to this implementation. Your job is to recreate these designs in this codebase:
React 18 + TypeScript + Vite in `web/`, plain CSS in `web/src/styles.css`, Go daemon in `cmd/` and
`internal/`, REST + SSE between them. No CSS framework, no component library, no new dependencies
without asking.

This is a full IA replacement, not a restyle. Today `web/src/App.tsx` is a single page with an
eleven-tab row inside one initiative. The redesign makes the six workflow phases the navigation.
Before writing anything, read `web/src/App.tsx`, `api.ts`, `Settings.tsx`, `statusChips.ts`,
`styles.css`, and on the Go side `internal/daemon/routes.go`, `internal/daemon/handlers.go`, and
`internal/domain/enums.go`.

The README has a verified map of the API that already exists. **Use those endpoints — do not invent
parallel ones.** It also has a "Backend gaps" section listing what the design needs that the daemon
does not have; those are the only places you should be adding Go code.

## Fidelity bar

Pixel-perfect against the mockups. Colors, type, spacing, and copy are final — do not improve the
wording, do not adjust the palette, do not add sections. `Design System.dc.html` is normative: where a
mockup and the design system disagree, the design system wins.

The design is responsive across four breakpoints — full sidebar at ≥1180px, narrow sidebar at 1000px,
a horizontal phase strip at 760px, and a status bar plus bottom-sheet phase picker below that. All
three narrow layouts are mocked in turn 7. Build every screen responsive as you go, not as a pass at
the end.

To compare against the target, open the `.dc.html` files directly in a browser (no build step) and
diff visually, screen by screen, at 390px, 768px, 1024px, and 1180px.

**Two corrections to note before you start.** Cards 4c and 3d show Verify with test names and failure
output the daemon does not produce — build card **8b** instead, which is evidence-based and matches
`POST /verify`. And the mockups show only four of the seven `Disposition` values; put the remaining
three behind the "…" overflow rather than dropping them.

## Work in this order, and stop for review at each numbered step

1. **Tokens.** Replace the `:root` block in `styles.css` with the palette from the design system.
   Accent moves teal `#0f766e` → indigo `#4f46e5`; neutrals move zinc → warm stone. Update every
   existing usage, including `statusChips.ts`. The app should still work, just recolored. Stop.

2. **Backend, blocking gaps.** README items 1–5, in the Go daemon, with tests:
   - Task mutations — reorder, edit, add, delete, cancel (`/tasks` is read-only today).
   - Plan revision request (`/revise` regenerates the PRD only).
   - Check results per change set (`checks_running` is a status with no results endpoint).
   - Conflict resolution for a `conflicted` change set.
   - Read-cursor write so What Changed can be marked seen.
   Propose the endpoint shapes before implementing. Stop.

3. **Routing + PhaseNav.** Add the routes in the README's screen table. Build the sidebar: initiative
   header, cross-initiative inbox (hidden when empty), six-phase rail with done/current/future states,
   footer. Everything hangs off this. Stop.

4. **Phase page shell.** PhaseHeader, DocumentRow, CommitBar. All six phases share it. Stop.

5. **Define page** (5c) — the judgment flow. JudgmentCard in all its variants, all seven dispositions,
   the live commit bar. Highest-value screen. Stop.

6. **Explore page** (8a) — context-doc grounding: drag-drop upload, paste, the async image-describe
   state, and the credential-rejection error that must not echo content.

7. **Build page** (4b + 8c) — task list with inline diff review, all eleven change-set statuses,
   revert confirmation, and system-deny rendering per rule 10.

8. **Product home** (5a), **Plan** (5e), **Design** (5d), **Explore running** (5b).

9. **Verify page** (8b) — criterion-level evidence recording against `POST /verify`.

10. **Document reader** (6d), **decision discussion + What Changed** (8e), **graph health** (8f).

11. **Settings** (8d) — model, providers, executor with its security warning, apply toggle, demo-mode
    banner.

12. **States** — empty (6a), error (6b), overflow (6c). Do not skip these; they are the difference
    between a demo and a product.

13. **Responsive audit.** Every screen at 390px, 768px, 1024px, 1180px against cards 7a–7c. Verify:
    44px minimum tap targets on phone, commit bar pinned to the viewport, the phase picker sheet,
    IdChip lineage as a tap sheet rather than hover, unified diffs, no horizontal scroll anywhere.

14. **Backend, remaining gaps.** README items 6–17, in that order. Item 12 (undo a resolved decision)
    is a decision point: either build the inverse endpoint or remove the Undo affordance from the UI.
    Do not ship a control that silently fails.

## Design rules — these are load-bearing

Violating any of these breaks the information architecture. Full text in the README; short form:

1. The phases are the navigation. Nothing else in the sidebar.
2. One advancing action per screen — one indigo button, labeled with its consequence.
3. No disabled controls, ever.
4. Lineage is a popover, never a page. There is no graph screen.
5. Provenance reads as prose, never a raw edge list.
6. Mono means machine-generated; sans means human.
7. Completed phases state what they produced.
8. Running is never silent — spinner, elapsed, live log, Stop, safe-to-leave.
9. The inbox is cross-initiative, hidden when empty, never duplicating the current page.
10. System deny is not a permission error — render it as a fact, with no action implying override.

## Not designed — ask before inventing

- Accessibility: focus states, popover keyboard reachability, diff-marker text equivalents.
- The ⌘K command palette and a multi-project switcher are referenced but have no design.
- Phone layouts for the diff review and product home — the README rules cover them, but only Define
  was mocked at 390px.

If a screen needs something the mockups do not show, stop and ask rather than inventing it.
