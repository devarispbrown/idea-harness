# Milestone 6 — System-Deny Invariant + OpenClaw 2.0 Lessons

Goal: make the security boundary an **explicit, tested, documented invariant**
rather than a set of scattered checks, and fold in the lessons from OpenClaw 2.0.

## Background: what we learned from OpenClaw 2.0

OpenClaw's latest release (polished UI + credential protection + exec-approval
matrix) surfaced four transferable lessons, in priority order:

1. **"System deny beats YOLO."** Even in auto-approve ("YOLO") mode, a sandboxed
   session cannot override Gateway routing, and certain commands stay
   `SYSTEM_RUN_DENIED`. The principle: a small set of hard, non-overridable
   denials that no user approval or executor trust tier can bypass.
2. **"Polished UI ≠ secure."** Outlets reviewed 2.0 as "polished UI, security
   holes stay open." For v1.0: do not let the surface outpace the boundary; the
   security invariants must be verified, not just claimed.
3. **Credential protection as a first-class feature.** We already did this
   (preset + arbitrary `.env` secrets, write-boundary redaction, generic sweep).
4. **Release/rollback discipline.** Their "three releases, one rollback" is a
   caution; our per-phase green commits + byte-for-byte revert are the answer.

## Part A — The system-deny tier

### A1. The invariant (to document + test)
Define a named, enumerated set of **system-deny conditions** that ALWAYS reject
or deny, regardless of executor trust tier, human `approve`, or config. The
current conditions are already enforced but scattered; the work is to make them
one explicit, auditable, tested surface.

Enumerated system-deny conditions (initial set):
1. `secret_sweep` — known secret / credential pattern in produced file content.
2. `path_escape` — a diff path escaping the workspace (fsdiff/security.ResolveWithin).
3. `host_exec` — deterministic build/test outside the container sandbox (fail-closed).
4. `blob_digest_mismatch` — stored content hash ≠ requested digest.
5. `trust_deny` — executor not authorized for a required capability by the
   engine-assigned trust tier (NOT the executor's self-declared tier).
6. `review_open_blocking` — a change set with an undisposed critical/high finding
   cannot be approved (already exists; fold into the tier).

### A2. Engine-assigned trust tiers (fix the inversion)
Today `Execute` reads `exec.Capabilities(ctx).Trust` — the executor's
**self-declared** tier. Change to a deterministic map keyed by executor kind:

| kind | trust tier | write_file | run_command |
|------|-----------|------------|-------------|
| internal | trusted | allow | allow |
| container | local_external | allow | deny |
| omp | local_external | allow | deny |
| hermes | local_external | allow | deny |

The engine consults `executor.TrustFor(kind)` (a new pure function), never the
executor's own report. This closes the "trust tier is inverted" finding.

### A3. Centralize the deny decision
Introduce a single `workflow.systemDeny(...)` (or `internal/security/deny.go`)
returning a structured `DenyReason{Code, Detail}`. Every mutation path
(Execute's checks, ApplyChangeSet, ReviewChangeSet approve) calls it and maps a
deny to a deterministic rejection with the deny code in the finding. This makes
"what can always deny" one greppable, testable place.

### A4. Tests (prove non-overridability)
For each deny condition, a test asserting it still rejects even when the human
approves or the executor self-declares a high tier:
- `TestSystemDenySecretSweepNotOverridable`
- `TestSystemDenyPathEscapeNotOverridable`
- `TestSystemDenyHostExecFailClosed`
- `TestSystemDenyBlobMismatchNotOverridable`
- `TestTrustTierIsEngineAssignedNotSelfDeclared`
- `TestSystemDenyBlocksApproveDespiteHumanVerdict`

### A5. Documentation
New `docs/security-model.md` enumerating the tier, the deny conditions, the
engine-assigned trust table, and the "system deny beats human approve / YOLO"
principle. The plan review + a follow-up implementation review gate v1.0.

## Part B — OpenClaw lessons (concrete items)

### B1. Release gate: security review before v1.0 tag
Before tagging v1.0, run a six-perspective adversarial review scoped to the
security boundary (not the whole codebase), and require it to pass. This is the
"polished UI ≠ secure" lesson operationalized.

### B2. Rollback/roll-forward documentation
Document the apply/revert journal as the rollback story, and the per-phase green
commit discipline as the roll-forward story. (Doc-only; no code.)

### B3. Credential-protection completeness audit
Add a single end-to-end canary test that plants a secret in each model/human
write path (idea, requirement, decision, question, finding, suggested answer,
task statement, diff content) and asserts none surfaces unredacted in any API
response or SQLite row. (Fills the remaining C-gap surface with one test.)

### B4. Gateway boundary naming
Rename/document the executor sandbox + trust tier as the "execution gateway"
boundary in `docs/security-model.md`, mapping idea-harness concepts to the
OpenClaw Gateway concept for clarity.

## Out of scope (explicitly deferred)
- A plugin/skills marketplace (post-1.0 growth lever).
- Cloud sessions (violates local-first constitution).

## Definition of done
- Parts A and B implemented with tests; `go vet ./...`, `go test ./...`,
  `scripts/smoke.sh` green.
- `docs/security-model.md` written; plan reviewed; implementation reviewed;
  fixes merged on green.

---

## Plan review corrections (v2)

The six-perspective plan review rejected the v1 plan (Security + UX/API reject;
four approve-with-changes). Corrections adopted below; these supersede v1.

### A1 — deny conditions (corrected)
- `review_open_blocking` is **NET-NEW**, not "already exists": it is only enforced
  at initiative completion (`VerifyInitiative` → `hasOpenBlockingFindings`),
  never at the change-set `approve` boundary. `ReviewChangeSet "approved"`
  evaluates only the FRESH adversarial findings, so a re-approve can succeed
  despite a prior open critical/high finding. **Fix:** add the gate to
  `ReviewChangeSet` (before `EvaluateApproval`), keyed on initiative-wide open
  blocking findings.
- Add an explicit **`pii`** deny condition (distinct code) so the enumerated set
  matches `hasBlockingRiskSignal` (which already includes SSN).
- `host_exec` is **two** fail-closed branches (no `check.image`; sandbox launch
  failure) — enumerate both.
- The deny list is enforced at **different stages**; document per-condition where
  it fires (execute vs apply vs approve vs complete), not as one uniform list.

### A2 — engine-assigned trust (corrected)
- Use the real `domain.ExecutorTrust` values: `internal`, `local_external`,
  `remote_external` (no fabricated "trusted"). `internal` → read/write/run_command;
  `local_external` → read/write; `remote_external`/unknown → fail-closed (no actions).
- `TrustFor` is keyed on the **selected executor's `Name()`** (== config kind for
  the four in-tree executors), AFTER `selectExecutor`'s fallback; unknown kinds
  fail closed. Name it `executor.TrustTierFor` (avoid collision with
  `security.TrustFor`). Also update `handleListExecutors` to report the
  engine-assigned tier, not `exec.Capabilities().Trust`.
- The **`run_command` column is currently dead** (`TrustAllows` is only consulted
  for `write_file`; real host-exec is gated by `checkCommand`'s `check.image` +
  sandbox). Document that honestly: `run_command` is enforced by the check
  sandbox, not the tier, until a real command-authorization call site exists.
- Reframe A2 as **hardening/layering**, not a live escalation: the tier is already
  hardcoded per concrete executor and not model-controllable.

### A3 — centralize the deny decision (corrected)
- The `DenyCode` enum + `DenyReason{Code,Detail}` type live in a **leaf package**
  (`domain` or `security`), NOT `workflow`/`fsdiff` (import cycle: `fsdiff` is
  imported by `workflow`, and `path_escape`/`blob_digest_mismatch` are enforced
  inside `fsdiff`/`blob`).
- `systemDeny` is an **additive aggregator, not a replacement**: keep the deep
  enforcers (`security.ResolveWithin`, `blob.Get` digest check, redactor sweep)
  in place as defense-in-depth, and map their failures to `DenyCode`s at the
  boundary via `errors.Is`.
- **Fail closed:** an unrecognized code or an evaluation error ⇒ deny.
- Enumerate ALL mutation entry points (`Execute`, `ApplyChangeSet`,
  `RevertChangeSet`, `ReviewChangeSet`, `VerifyInitiative`).
- **Apply/approve must re-sweep** (secret_sweep is Execute-only today): before
  `ApplyChangeSet` mutates, re-load the patch and re-fetch each file's bytes
  (digest-verified) and re-run the sweep; `ReviewChangeSet "approved"` also
  re-checks `review_open_blocking`.
- **Emit a persisted finding for every deny** (including `path_escape`, which
  today is only a raw error), mapping `DenyCode → Finding` consistently.

### A4 — tests (corrected)
- Add a **condition × path matrix** (each deny at Execute/Apply/Approve), not just
  one test per condition.
- `path_escape` cannot be injected at Execute (diff paths come from
  `filepath.Rel`); test it at the fsdiff/apply layer via a crafted `FileChange`
  with `../`.
- `TestSystemDenyBlocksApproveDespiteHumanVerdict` must assert the **two-approve
  scenario**: approve #1 blocked → approve #2 with a clean fresh review still
  blocked by the open finding.
- Sweep and persist must read each file **once** (close the TOCTOU double-read
  in `renderDiffTextFromWorkRoot` vs `persistDiff`).

### A5 / UX — deny surface (corrected)
- Add a dedicated **`Code`** field to `domain.Finding` (distinct from the model's
  free-text `Category`), populated from `DenyReason.Code`, in the JSON + CLI +
  UI; render a distinct "system deny" style.
- Add a **structured error envelope** (`{"error","code","system_deny":true}`) and
  a distinct HTTP status so the API/CLI can tell "you asked wrong" from "the
  system overruled you".

### Reconcile with the prior "inverted gate" finding (#11)
The "system deny beats human approve" principle must be scoped to **exactly the
enumerated deny conditions** and reconciled with #11 ("adversarial review runs
AFTER human approve and can silently override it"). Resolution: move the change
set adversarial review **before** the human sees the diff (so deny precedes
approve), OR document that the six system-deny conditions are the ONE place the
system may reject despite a human approve — and only there. Do not leave the
contradiction unresolved.

### B1 — release gate (corrected)
Define a concrete pass bar (zero unresolved blocker/high; medium/low triaged to a
backlog with owners), require human sign-off, and bound re-review to the changed
surface.
