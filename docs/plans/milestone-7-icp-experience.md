# Milestone 7 — ICP Experience

Close the six experience gaps that would make the ideal customer choose another
platform. Ordered by leverage.

## A. README + positioning rewrite (gaps 2, 5-partial, 6)

- Rewrite `README.md`: status line → full M1–M4 ("Idea → verified product");
  quick start → the complete loop (`init → model set → new → advance → execute →
  review approve → apply → verify → complete`, driven by `idea next`).
- Lead with the moat: **provable provenance (`why`/`impact`/`trace`) + safe
  byte-for-byte revert**, then the governed team-of-experts pipeline. "Control
  plane" becomes a subtitle, not the headline.
- Add an honest **execution model** note: serial per initiative, dependency-ordered
  tasks; multiple initiatives are supported (one task at a time each).

## B. Wire the UI progress to real signals (gap 3)

- In `web/src/App.tsx`, replace the hardcoded `const verifyDone = false` (and
  audit the other `*Done` steps) with real signals: `verifyDone` ← initiative
  status `complete`; `buildDone` ← a change set `applied`; `exploreDone`/`defineDone`
  ← PRD artifact present + status `approved`; `planDone` ← status `plan_approved`.
- The Verify step must turn green exactly when the initiative reaches `complete`.

## C. Make the stub implementer explicit + the coding-agent path first-class (gap 1)

- **Stub honesty:** the internal executor writes a clearly-labeled placeholder
  (`hello.txt` header `# idea-harness placeholder — configure a coding executor`),
  and `idea execute` + the UI emit a visible notice: "the internal executor
  produces a placeholder; set executor.kind = omp|hermes to generate real code."
- **First-class agent path:** add `docs/agents.md` ("Wire a real coding agent")
  with concrete steps — obtain/build an OMP or Hermes image, then set
  `executor.kind`, `executor.image`, `executor.model` in `harness.yaml` (or the
  UI Settings). Reference the existing `scripts/agent-smoke.sh`.
- Explicitly **out of scope for this milestone:** shipping a new real coding
  executor by default (needs a coding model + reliable file-diff generation).

## D. First-class Git integration (gap 4)

- Add a `gitcommit` helper: on `apply` success, if the workspace is a Git repo,
  `git add -A && git commit` with a message referencing the change set
  (`CHG-N: <task title>`), and persist the SHA via `SetChangeSetCommit`.
- On `revert`, create a revert commit (or at minimum surface the SHA).
- Surface the commit SHA in `idea change CHG-N` and the UI "changes" tab.
- Not-a-repo / nothing-to-commit is a no-op (no error).

## E. First-run guidance (gap 5)

- `idea init` prints the next step; `idea doctor` reports "demo mode — no model
  configured; run `idea model set …`"; `idea new` in demo mode prints a one-line
  notice that output is synthetic.
- No new wizard; just clear, always-visible next-step hints.

## Definition of done

- A–E implemented; `go vet ./...`, `go test ./...`, `scripts/smoke.sh`, and
  `cd web && pnpm build && pnpm test` green.
- Plan reviewed (adversarial) before implementation; commits reviewed after.

---

## Plan review corrections (v2)

Six-perspective review (Product, UX/API, Reliability, Quality, Architecture,
Security): **1 blocker, several highs.** Corrections adopted below; they supersede
the v1 text.

### A — README (corrected)
- Do **not** headline "Idea → verified product" unqualified. The default executor
  is a placeholder and the no-model path is synthetic. Honest headline: "M1–M4
  pipeline implemented end-to-end (governed team-of-experts + provable provenance
  + byte-for-byte revert); the build step uses a placeholder executor by default —
  wire OMP/Hermes for real code." Present the full flow as the *artifact/governance*
  flow, not a working-product claim.

### B — UI progress (corrected)
- Only `verifyDone` is hardcoded; **do not** rewrite explore/define/design. Keep:
  exploreDone = `idea_brief`+`research_brief`; defineDone = `prd`; designDone =
  three design artifacts; planDone = `plan_approved`; buildDone = a change set
  `applied`. **Only ADD** `verifyDone = status === "complete"`.
- Make `planDone` monotonic: `status ∈ {plan_approved, complete}` so it does not
  un-green at terminal state.
- Verify **action** is CLI-only for this milestone (the UI has no verify endpoint);
  the Overview shows "run `idea verify REQ-1 --pass` to complete" as a hint.
- Document that progress is **revocable** on revert (the stepper retreats).

### C — stub executor (corrected)
- **Blocker:** the relabel breaks `internal/executor/internal_test.go:134` and
  `internal/workflow/execute_test.go:208` — update both assertions in the same
  change.
- Keep the placeholder **task-specific** (retain `task: <title>`), e.g.
  `# idea-harness placeholder (task: <title>) — configure executor.kind = omp|hermes`,
  so each task still yields a distinct diff.
- The notice is **synchronous**: derive it from `config.Executor.Kind` and surface
  via the execute response / `idea change` / UI "changes" tab (execute is async,
  so no CLI print at launch). Header lives in `internal.go`; the notice is
  presentation copy at the CLI/UI layer (do not extend the Executor interface).
- Executor config is **YAML-only** — drop the "or UI Settings" claim.
- Reword C to "document + surface the **already-implemented** omp/hermes path"
  (not "first-class plumbing"). `docs/agents.md` must state the egress/key risk:
  the agent sandbox has no network isolation and forwards its provider key.

### D — Git (corrected)
- **Security blocker:** never run repo hooks from the daemon. `git commit` with
  `-c core.hooksPath=<empty-dir>`; make commit-on-apply **opt-in** (harness flag,
  default off); scrub `GIT_*`/provider-key env before exec.
- **Path-scoped only:** stage exactly `patch.Files` (`git add -- <paths>`,
  `git add -u -- <paths>`/`git rm` for deletes) — never `git add -A`.
- **Ordering:** fold `git add + commit` into Phase 2 BEFORE `MarkChangeSetApplied`;
  on commit failure, `fsdiff.Restore` + `conflicted` (mirror the fsdiff.Commit
  failure path). Commit is **non-fatal**: persist applied status+journal first, then
  record SHA (or a "commit failed" marker) durably.
- **Revert:** treat git history separately from the workspace restore; only
  auto-commit a revert when the tree is otherwise clean. Document "byte-for-byte
  revert = files, not git history".
- New `internal/git` package (workspace-root arg + dockerPath-style override for
  tests); **replace** the dead `execute.go:962-966` block; sanitize the message
  (strip newlines/leading dashes, sweep the title); render `git_commit_sha` in the
  UI "changes" tab. Commit message uses a task-title lookup (cs.TaskID → Task.Title).

### E — first-run (corrected)
- `idea init`/`idea new` notices already exist. Scope E to: add a demo-mode line to
  `idea doctor` (reuse `isDemoMode`), and the execute placeholder notice (shared
  with C).
