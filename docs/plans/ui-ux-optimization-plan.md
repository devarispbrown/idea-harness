# UI/UX optimization plan (Principal UI/UX Red Team Review)

Status: proposed. Supersedes nothing; the per-slice status and env-var prompting
work (Feature 1 + Feature 2) is already landed and is a dependency, not an item.

Source: the red-team review of 2026-10-20. Findings are cited as F1–F12.

## 0. North star and principles

The product is a control plane whose value is: *turn an idea into verified,
reviewed, traced software, stopping exactly where a human must decide.* The UI
does not currently express that — it expresses the engine's stages.

**North star:** from any screen, a user can answer three questions — *what is
happening, what is waiting on me, what is the one next action* — and a
first-time user can complete one slice end to end without opening the CLI.

**Principles:**
1. The IA reflects user jobs (shape → decide → build → ship), not engine phases.
2. A human judgment is a first-class screen, not a pill or a sidebar count.
3. Every "system deny" renders as *what happened / why / what to do next*.
4. Blocking-first: show what stops progress; hide the rest behind disclosure.
5. Terminology is taught progressively; power users keep every shortcut.

## 1. Target information architecture

Replace the six backend phases (`Explore → Define → Design → Plan → Build →
Verify`) with four goal-shaped stages:

| New stage | Absorbs | The user's question it answers |
|---|---|---|
| **Shape** | Explore, Define, Design | "Do I understand the idea and its reviewed PRD?" |
| **Decide** | PRD review, decisions, questions, plan review | "What calls do I need to make to unblock this?" |
| **Build** | Plan (as the build's input), execute, change-set review/apply | "Is the code written, safe, and applied?" |
| **Ship** | Verify | "Is it verified — and what is NOT yet verified?" |

The fine-grained stages are **kept** as a collapsible "detail" under each stage
(expert users lose nothing; novices stop seeing a wall).

**Persistent strip** (top of every initiative view): `Now` · `Needs you (n)` ·
`Next`. `Now` = the running agent or current stage; `Needs you` = the ranked
judgment queue (blocking items only, then "show all"); `Next` = the single next
action, stated as an imperative and carrying its CLI command. This is F1 + F2 +
F12 in one surface.

## 2. Phased work items

### Phase 0 — release blockers (before "easy and intuitive" is claimed)

**P0.1 — Goal-shaped IA + Now/Needs-you/Next strip** (F1, F2, F12)
- Change: `phases.ts` (stage model), `App.tsx` (shell), `PhaseNav.tsx`, new
  `NowNext.tsx`. `Needs you` becomes a top-level queue page (replacing the
  sidebar count), not an addition beside it.
- Effort: L. Depends on: F1/F2/F12 findings + the existing `GET /attention` rollup.
- Acceptance: a novice states the next action from Home; the strip updates
  live (event stream) as state changes.

**P0.2 — Judgment surfaced inline, blocking-first** (F3)
- Change: `DecisionPage.tsx`, `JudgmentCard.tsx`, the Plan/Define findings lists.
  Blocking items render with the recommendation and a one-click **Accept**;
  `accept-recommendations` gets a primary button. Non-blocking items collapse
  under "show all".
- Effort: M. Acceptance: a user disposes 15 decisions in one click and can say
  why the reviewer blocked the PRD.

**P0.3 — System-deny → what/why/next** (F4)
- Change: `FailedBanner.tsx` + a shared `DenyExplanation` component + the
  daemon's deny responses carrying a stable machine code (`blast_radius`,
  `review_open_blocking`, `secret_sweep`, `protected_path`, …) plus the human
  text. Reuses the enumerated system-deny set from
  `docs/plans/milestone-6-system-deny.md`.
- Effort: M. Acceptance: every deny the reviewer can trigger shows *what
  happened / why / the exact next command*, one line by default, expandable.

**P0.4 — Accessibility audit + fixes** (F11)
- Change: screen-reader + keyboard audit of the primary loop; aria-labels on
  every icon/text-only button; focus return after dialogs; target sizes; the
  Plan-row action grouping (P2.1) is a prerequisite for sane targets.
- Effort: M (audit) + M (fixes). Acceptance: keyboard-only and VoiceOver complete
  Shape→Decide→Build→Ship; no unlabeled control.

### Phase 1 — high-impact workflow

**P1.1 — Home teaches the loop** (F5): a 3-step "what happens next" strip
(Shape → Decide → Build → Ship) after `Start`, and a first-slice progress rail on
each initiative card. Effort S.

**P1.2 — Terminology glossary + progressive disclosure** (F6): an inline "?" next
to first occurrences of PRD / execution plan / change set / disposition /
convergence / blast radius, backed by a glossary drawer. Effort S.

**P1.3 — Agent status legibility** (F10): `RunningCard.tsx` shows stage + "what
it is doing now" + files written + "Stop / Steer", not just elapsed seconds.
Effort M.

**P1.4 — CLI/UI parity, top five** (F8): surface `approve`, `revise`,
`disposition`, `verify --pass/--waive`, and `env set` in the UI (the first four
are judgment verbs; `env set` writes via a new daemon endpoint). Effort M–L.

### Phase 2 — visual and interaction

**P2.1 — Plan-row action grouping** (F7): one primary action inline; everything
else behind a single `⋯` menu (the code already builds the actions list once —
reuse it). Effort S.

**P2.2 — Build panel sprawl** (F9): progressive disclosure — lead with "run" +
"needs your review"; collapse Budget / git-pr / env / findings behind summary
rows. Effort M.

### Phase 3 — longer-term experiments

**P3.1 — Guided first-slice walkthrough** (F5 extension): a stepper overlay the
first time a user completes a slice.
**P3.2 — Cross-initiative "Needs you" queue** beyond per-initiative (F12 end
state): one queue across projects, ranked by impact.

## 3. Detailed spec — the Now/Needs-you/Next strip (P0.1)

- **Component:** `NowNext.tsx`, rendered by `App.tsx` above every initiative
  route. State feeds from the existing `GET /attention` rollup + the event
  stream (`useProjectChanges`), so it updates without a poll.
- **States:** `Now` = running agent (stage + files) | idle (current stage) |
  failed (with the deny explanation); `Needs you (n)` = blocking count →
  queue page; `Next` = imperative + `idea …` command (or "nothing — initiative
  complete").
- **Edge cases:** no initiative yet (`Next` = "start one"); initiative complete
  (`Next` = "review what shipped"); multiple initiatives (strip is
  per-initiative; Home shows per-card).
- **A11y:** the strip is a `role="status"` live region (already have
  `LiveAnnouncer`); the queue is keyboard-navigable; counts are not the only
  indicator (text labels included).
- **Desktop/mobile:** on narrow viewports the strip collapses to `Now` + `Next`
  with `Needs you` as a badge on the queue icon.
- **Acceptance criteria:** (1) the strip's `Next` always names a concrete
  action; (2) it updates within one poll after any state change; (3) it is
  announced by a screen reader; (4) no state leaves `Next` blank or wrong.

## 4. Validation and measurement

**HEART:** task completion (% initiatives → `complete`); time-on-task (idea →
first applied change set); error rate (denies surfaced without a guided fix);
abandonment (parked >1 day at `needs_human_judgment`); activation (first applied
change set in the first session); retention (second initiative started);
satisfaction (post-slice CSAT); trust ("I understood why it stopped").

**Tests before broad launch:**
- Moderated usability: novice + expert, the Shape→Decide→Build→Ship loop.
- Prototype A/B: goal-shaped IA vs. the six-phase IA (task completion, time).
- Accessibility: screen-reader + keyboard audit (P0.4).
- Production: instrument the HEART metrics; gate "intuitive" on activation + the
  deny-guidance error rate.

## 5. Sequencing and dependencies

```
P0.1 (IA + strip) ─┬─> P0.2 (judgment inline) ─> P0.3 (deny explain)
                   └─> P2.1 (action grouping) ─> P0.4 (a11y fixes)
P0.1 ─> P1.1 (home teaching) ─> P3.1 (walkthrough)
P0.2 ─> P1.4 (CLI/UI parity)
P0.1 ─> P1.3 (agent status) ─> P2.2 (panel disclosure)
```

P0.1 is the keystone: the strip and the goal-shaped stages make every other fix
legible. Ship it first, behind no flag, with the fine-grained stages preserved
as detail.

## 6. Open decisions to confirm before building

1. **Stage naming** — `Shape / Decide / Build / Ship` vs. alternatives
   (`Define / Decide / Build / Verify`). Validate with a prototype; do not bikeshed.
2. **How far to collapse** — keep the six phases as URL-addressable detail (so
   deep links and docs don't break), or retire them outright. Recommendation:
   keep as detail.
3. **`env set` in the UI** — needs a new write endpoint (write-only, mirrors
   `idea env set`); approve the security shape before P1.4.
4. **Cross-project "Needs you"** — in scope for P3.2 only; the per-initiative
   queue (P0.2) ships first.
