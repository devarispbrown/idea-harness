# Adversarial review — DX Audit 2 fix plan

Three independent reviews of `dx-audit-2-fix-plan.md` v1, per CONTRIBUTING §14
(plans are adversarially reviewed before implementation).

| Reviewer | Perspective | Verdict |
|---|---|---|
| R1 | Go correctness and API/runtime semantics | **reject** — 3 blocking |
| R2 | Coverage audit and refactor risk | **reject** — 4 blocking |
| R3 | Guard durability, verification, release | **reject** — 3 blocking |

All three rejected v1. Consensus: the plan's *thesis* was right and its
*diagnosis* was verified independently by R3 — but four of its fixes would have
shipped bugs worse than the defects they targeted, and its central structural
change did not actually create the guard it claimed.

Every finding below was re-verified locally before acceptance, since the plan is
material to hard gates.

## Blocking findings, and what changed

### 1. C5 would have broken ~16 handlers silently (R1, R3)

v1 renamed `{id}` → `{initiativeId}` across 53 route registrations. **Only 32
are initiatives.** The other 21 are operations, decisions, questions, artifact
versions, doc comments, findings and change sets. Renaming those makes
`PathValue` return `""` — no panic, no compile error, permanent 404s.

v1's stated motivation was also false. It claimed the mux panics "without naming
the offending route". Verified directly with a scratch program:

```
pattern "GET /a/{initiativeId}/x" (registered at main.go:9) conflicts with
pattern "GET /a/{id}/x" (registered at main.go:8):
GET /a/{initiativeId}/x matches the same requests as GET /a/{id}/x
```

It names both, with `file:line`.

**Resolved:** unify in the *other* direction. Measured — 10 routes, 13
`openapi.yaml` lines, and exactly **one** `PathValue("initiativeId")` call site,
against 32/64/29 the other way. Keep the anti-collision test; it is the part
with value.

### 2. C4 would have been worse than the bug (R1)

v1: `s.writeErr(w, 409, fmt.Errorf("config written but not applied: %w", err))`.
`IsStorageFailure` uses `errors.As`, so `%w` **preserves** detectability —
`writeErr` takes the storage branch and emits a generic 500, destroying the
message entirely. The user would learn neither that their config was written nor
that the daemon kept the old one.

**Resolved:** classify first, then respond; 409 with a fixed path-free message
for storage failures, `writeErr` otherwise so the legitimate domain error
survives. The leak test must assert the 409 status or it passes trivially on a
500.

### 3. C1 relocated a write nobody reads (R1)

`risk_profiles` has **no `SELECT` anywhere in the tree**. Both consumers of risk
data read the live response. v1's "move persistence to the change-set pipeline"
invented a consumer, and would have written a *different* quantity
(per-change-set, not initiative-aggregate) into the same table.

**Resolved:** delete the write, add no replacement. R1 also supplied a better
acceptance assertion: the write goes through `db.Apply`, so a GET appends an
**event**, which `handleActivity` returns and `handleEvents` streams over SSE —
a read pollutes the live event stream, not merely a table.

### 4. C2's execute precondition would have broken `idea next --until-human` (R1)

`NextAction` returns `"execute"` whenever status is `plan_approved`, **without
checking a ready task exists**. Turning that into a 409 converts a background
failure into a hard exit from the `--until-human` loop. R1 also found that
`nextReadyTask` is unexported and that `Execute` holds `e.mu` across a model call
plus a container run, so a synchronous ready-task check either blocks the handler
for minutes or races.

**Resolved:** scope the precondition to initiative status only, and fix
`NextAction` in the same commit so it never proposes an impossible `execute`.
TOCTOU narrowed, not closed — stated as such.

### 5. Track A's flat spec weakened the guard it was adding (R1, R2, R3)

One `flagSpec` per top-level row cannot express `model ls|set|add`, `docs add`,
`executors setup`, `config validate`, `daemon status|stop`. A union spec would
*accept* `idea model ls --key-env X` and `idea executors --smoke` —
reintroducing the silent-swallow class one level down. It also contradicted B3,
which gives `add` and `set` different specs. All three reviewers found this
independently.

**Resolved:** `subs map[string]flagSpec` per dispatch target. B4 and B6 then fall
out of the table instead of needing hand-written `default:` arms.

### 6. Track A was a refactor, not a guard (R3)

The sharpest finding of the round, and it reshaped the track:

> Track A centralizes *enforcement* while leaving *documentation* keyed to the
> same hand-written `summary` string, so the D3 class survives intact.

`renderCommandTable` emits `usage`, `summary`, `jsonDoc()`. Deriving only the
third column changes nothing about where flag *names* come from — `--waive`
could sit in `verify`'s spec, be enforced, and stay absent from `docs/cli.md`
with `make docs-check` green. That is the finding it was meant to fix, surviving
its own fix.

**Resolved:** new **A5** renders a Flags column from `sorted(keys(c.flags))`.
That is the second axis. It also replaces v1's `TestJSONDocMatchesTheSpec`, which
R2 and R3 both identified as vacuous — `jsonDoc()` is defined as a pure function
of `c.flags`, so asserting it matches `c.flags` asserts nothing.

### 7. Two bugs in v1's own proposed code (R2, R3)

- `flagBool` is `iota` — the zero value — so `c.flags["--json"] == flagBool` is
  true for every command that does *not* declare `--json`. v1's `jsonDoc()` would
  have labelled `logs`, `next` and `help` as `yes`, and its `default` branch was
  unreachable. **Resolved:** `flagUnset` sentinel at iota 0, plus comma-ok.
- The switch tested `len(c.flags) == 0` before `rejectsJSON`, silently flipping
  `plan`'s row to `takes no flags` and deleting its deliberate
  "use `idea tasks --json`" pointer. No test locks that string.
  **Resolved:** `rejectsJSON` first; `plan` declares `--json` so it can reject it
  with its own message; a test locks the text.

### 8. The A4 test would have hung rather than failed (R1, R2, R3)

v1 pointed `IDEA_HARNESS_ADDR` at a closed port. `ensureDaemon` does not fail on
a closed port — it spawns `exec.Command(os.Executable(), "daemon")` detached and
polls for 5s. Under `go test` that is the test binary. And `daemon`/`dev` call
`daemon.Run` **in-process**, blocking forever; `new` opens an interactive form.

**Resolved:** use the `newFakeDaemon` + `len(f.requests)` harness that already
exists and proves the stronger property. Blocking rows carry a `noAutoInvoke`
marker with a reason; every case runs under a timeout.

### 9. Track A alone turns CI red (R2)

`make docs-check` gates every commit, and Track A rewrites 24 of 44 table rows
before A5 adds a column to all of them. v1 deferred all regeneration to Track D.

**Resolved:** new **A6** — regenerate inside the Track A commit, and rewrite the
`docs/cli.md` "JSON output" prose section, which documents four cell values and
gains a fifth.

### 10. E3's reasoning was factually wrong (R3)

v1 argued for documenting rather than testing the two untested architectures, on
"zero arch-specific source files". True of the repository; **false of the
artifact.** `modernc.org/sqlite` vendors a machine-translated SQLite per
GOOS/GOARCH and `modernc.org/libc` carries 194 arch-suffixed files. Measured:

```
darwin/arm64: 210 Go files from modernc libc+sqlite
darwin/amd64: 206
```

Four shipped binaries embed four different persistence layers; two never execute
a test. v1 would have put a justification into README that the evidence
contradicts. The cost premise was wrong too — hosted x86-64 macOS runners need no
emulation.

**Resolved:** add the Intel macOS job; document the linux/arm64 gap with the real
reason; drop the false line.

### 11. D3's deferral was under-priced (R3)

v1 deferred widening `api-check` to request schemas, citing anonymous inline
structs. Backwards — the JSON tags are uniform across **20 of 20** decoding
handlers, so an AST pass over files `parseRoutes` already parses can diff field
names against the spec. It would have caught this exact finding.

**Resolved:** implemented this round, not deferred.

## Accepted with modification

- **D1's check design.** R3 confirmed the markdown is parseable as-is (13 tables
  share a byte-identical header) but listed five silent false positives, and
  argued against full generation because the Purpose column is real editorial
  prose. **Resolved:** mirror the `docs`/`docs-check` pair — `make api-docs`
  inserts `TODO:` rows and deletes stale ones, `make api-docs-check` gates. Scope
  restated honestly as routes ↔ *row existence*, not routes ↔ prose.
- **C3's typed error.** Narrowed to `Runtime.*Initiative` precondition checks
  only, with the three retyped returns enumerated and a test that the 14
  `StatusBadRequest` sites still return 400 — R1 showed those are the asymmetric
  hazard, while a 500 cannot be reclassified because `IsStorageFailure` is
  checked first.
- **C6.** Envelope flattening **dropped** on R1's argument: `/attention` is the
  one cross-initiative rollup and the natural candidate for a cursor, so the
  envelope is the better shape; flattening buys consistency with endpoints that
  will never need it, at the cost of a breaking change plus unguarded spec drift.
  Escaping half kept and widened.
- **B9.** "a small threshold" was not mechanically checkable. Now: not-found
  quits immediately, other errors after 5 consecutive failures, with a test
  asserting a non-nil error within 2s.

## Rejected

**R1's path-traversal framing of C6.** R1 wrote that `resolveInitiative`
returning `args[0]` raw into a path segment means `idea tasks '../../health'`
builds a traversing path. Probed against a live daemon:

```
idea tasks '../../health'              exit=1 :: 404 Not Found
idea tasks 'x/../../../openapi.yaml'   exit=1 :: 404 Not Found
```

No traversal. The fix is still correct — ids containing URL-special characters
break the URL, and eight call sites bypass the `initiativeResource` helper added
in audit 1 for exactly this — but it is a correctness bug, not a security one,
and the plan does not claim otherwise.

## Process findings

- **R3:** the audit report existed only as a published artifact, so "addresses
  every finding" was unverifiable by anyone but its author. **Resolved:**
  committed as `dx-audit-2-report.md`, with a traceability table in the plan.
- **R3:** v1's header said "5 P1" while its body labelled eight. **Resolved:**
  reconciled to 3 P0 / 10 P1 / 13 P2 / 4 P3.
- **R2, R3:** `cmdNew` and `cmdExecutorSetup` contain the same hand-rolled flag
  loop B1 removes from `cmdDocsAdd` — including the identical `-`-rejecting arm —
  and appeared in no track. **Resolved:** added to Track A's migration list.
- **R3** noted E1 was implemented while the plan still called it an open
  decision. It was put to the owner and Apache-2.0 was chosen before
  implementation; the plan now records the decision rather than the question.
