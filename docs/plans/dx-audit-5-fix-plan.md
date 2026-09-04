# DX Audit 5 — fix plan

Audit 5 returned **FAIL at 61/100** with the magic-path gate broken. This plan
covers the five P0s and the eight P1s.

## First, the thing that is not true

The score fell from 79 to 61 and that is **not a regression**. Every P0 below
shipped in `v0.1.0`:

| Defect | Introduced | In `v0.1.0` |
|---|---|---|
| `maxOutputForStage` returns 32000 | `f874e21`, the initial commit | yes |
| `--until-human` does not wait for an in-flight operation | `d7af090`, before the tag | yes |
| `handleUpdateProviders` writes from the daemon's cached config | initial commit | yes |
| bare `default_model` accepted | initial commit | yes |
| provider error bodies discarded | initial commit | yes |

Audits 2-4 never touched `maxOutputForStage` or `handleUpdateProviders`'s cached
read. The score moved because audit 5 did two things no previous round did: it
ran the README's block **verbatim on the no-key path**, and it made **real
provider calls**. Documentation went *up*, 62 → 68.

The lesson is about audit method, not about the tree: four rounds of guards
compared artifacts to artifacts. Nothing compared the product to a provider, and
nothing executed the documented sequence as written.

---

## Track A — the magic path (P0)

### A1. `idea next --until-human` does not wait for work already in flight

`internal/cli/next.go:53-57`. `--until-human` waits only for operations *it*
starts. `idea new` is asynchronous, so the very next command sees the initiative
`running`, falls through to `reportNextAction`, and prints
`workflow running — try `idea watch``. The README promises the opposite at
line 164: *"waiting for each to finish"*.

Measured: **5/5 pass with provider keys exported, 0/5 without** — ambient keys
shift the timing enough to win the race.

**Fix.** When `--until-human` sees a non-automatable `wait` caused by a *running*
operation, poll until the operation finishes instead of returning. Bound it by
the existing step budget and a wall-clock cap, and print the same progress line
the loop already prints for its own steps. `wait` for a *failed* initiative is a
different case and is A3.

**Acceptance.** Run README:144-149 verbatim, keys unset, 10x back-to-back; every
run reaches `review CHG-1`. A test drives `next --until-human` against a fixture
whose first `/next` answers `wait`/running and then `advance`.

### A2. `idea next` reports `✓ initiative complete` over an empty initiative

`internal/workflow/verify.go:195-234`. The completion gate requires every active
requirement verified and no open blocking findings. It does not require that
anything was ever built, so hand-recording evidence on the PRD's requirement
completes an initiative with **zero tasks and zero change sets**.

`docs/ui.md:101` already states the right criterion for the Build phase: *"at
least one task and every task is done or cancelled, and at least one change set
and every change set is applied or reverted"*. The engine does not use it.

**Fix.** Extend the gate: an initiative may not reach `complete` while it has no
change set at all, or while any change set is in a non-terminal state. Keep the
existing requirement and findings conditions. Report the reason through the same
"not done yet" path rather than an error.

**Acceptance.** A test completes an initiative with zero change sets and asserts
it does **not** reach `complete`, and that the status message names what is
missing. The existing full-flow tests and `scripts/smoke.sh` must stay green —
they apply a change set, so they should be unaffected.

### A3. `idea next` tells a *failed* initiative the workflow is running

`internal/workflow/next.go:65` — `default: // running / failed` returns `wait`
for both. `idea execute` correctly points the user at `idea next`; `idea next`
points back at `idea watch`; `idea watch` needs a TTY. Closed loop, exit 0.

**Fix.** Split the case. `failed` returns a distinct action that
`reportNextAction` renders with the failure reason and the recovery command.
Exit non-zero, because a failed initiative is not a state a script should treat
as success.

**Acceptance.** A test asserts `nextAction` on a failed initiative is not `wait`,
and that the CLI exits non-zero and prints the failure reason.

---

## Track B — the provider path (P0)

### B1. Every stage budget exceeds every provider's declared ceiling

`internal/workflow/engine.go:203` returns `32000` for `StageProductDefinition`,
four lines under a comment asserting *"Values stay within the providers' declared
MaxOutput ceilings (16k for Anthropic/OpenAI)"*. `openai.go:45` and
`anthropic.go:37` declare `16384`; `gemini.go:38` declares `8192`. Nothing
clamps — `Capabilities.MaxOutput` is declared per provider and **never read**.

Result: the README's own example (`openai/gpt-4o-mini`) fails 100% at
`product_definition` with `max_tokens is too large: 32000`.

**Fix.** Clamp at the point of use: the request's `MaxOutput` becomes
`min(maxOutputForStage(stage), provider.Capabilities().MaxOutput)` when the
provider declares one. Correct the comment to describe what the code does. This
makes the declared ceiling load-bearing instead of decorative.

**Acceptance.** A table test over every stage × every provider asserting
`effectiveMaxOutput(stage, p) <= p.Capabilities().MaxOutput`. It fails today for
`product_definition` on all three real providers and for `review` on Gemini.

### B2. OpenAI and Anthropic discard the API error body

`openai.go:150` and `anthropic.go:141` return `fmt.Errorf("…: status %d")`,
throwing away the response that names the cause. `gemini.go:143` keeps it — which
is why the Gemini failure was diagnosable in minutes and this one was not.

**Fix.** Both read and include the body, bounded in length and passed through the
existing redactor so a provider echoing a key cannot leak it into an artifact.

**Acceptance.** A test with an `httptest` server returning a 400 and a known body
asserts the message contains it, and that a body containing a credential pattern
is redacted.

---

## Track C — configuration (P0)

### C1. `idea model set` silently reverts hand-edited `harness.yaml`

`internal/daemon/handlers.go:468` — `cfg := pr.cfg.Harness` reads the daemon's
cached config and then writes the whole struct. The daemon caches for its
lifetime, so a hand edit is invisible and gets overwritten at exit 0.

Observed: `max_cycles: 9 → 3`, `research.urls` emptied, `git_commit: true → false`.

`handleUpdateExecutors` at `:2272` already does this correctly and carries the
comment saying why. Same defect, fixed in one handler, not carried to the other.

**Fix.** Re-read from disk with `config.Load` and merge only the supplied fields,
matching the sibling. Keep the existing map-cloning so a rejected write cannot
mutate the running daemon.

**Acceptance.** Hand-edit three unrelated fields, `idea model set`, assert all
three survive. Mutation: reverting to the cached read fails the test.

### C2. A bare `default_model` routes everything to the demo provider, silently

`internal/config/problems.go:92-95` returns `nil` for any spec with no `/`, so
`idea model set gpt-4o` is accepted by `model set`, `config validate` and
`doctor`, all exit 0. `internal/daemon/providers.go:125` (`isDemo`) consults only
the key env vars, so with a key exported the demo banner is suppressed. The user
believes gpt-4o wrote their PRD; `model=gpt-4o actual=demo` appears only in the
daemon log.

**Fix, two parts.** `modelSpecProblems` rejects a non-empty spec with no `/` as an
**error**, naming the provider-qualified form. `isDemo` additionally reports demo
when the configured `default_model` does not resolve to a registered provider, so
the banner tells the truth regardless of which env vars are set.

**Acceptance.** `idea model set gpt-4o` exits non-zero and names the correct form.
A test asserts `isDemo` is true when `default_model` does not resolve, with keys
present.

---

## Track D — diagnostics that report health during failure (P1)

### D1. The state-dir wedge

Delete the state directory while the daemon runs — the natural "start fresh"
move, and the docs say all state lives there. Every command then returns bare
`idea: unauthorized`; `idea daemon stop` refuses with *"no daemon pidfile"*;
`idea doctor` prints *"all checks passed"* and `idea daemon status` prints
*"daemon: running"*, both exit 0. No in-product route out.

**Fix.** `doctor` distinguishes *reachable* from *authenticated*: a 401 from the
daemon is a failed check that names the cause and the recovery. `daemon stop`
falls back to the port when the pidfile is missing but the daemon answers, and
says what it is doing.

**Acceptance.** A test wipes the token, asserts `doctor` exits non-zero and names
the recovery.

### D2-D5, the rest of the P1 backlog

| # | Finding | Fix |
|---|---|---|
| D2 | `POST …/suggest-answers` returns 202 for a nonexistent initiative; every sibling 404s. Already recorded as `C5` in `deferred.md` | Add the existence check and the running guard, matching the other eight entry points |
| D3 | Routing-layer errors are `text/plain`, contradicting *"every error response has this shape and nothing else"*; 405 is never emitted | A JSON not-found/method-not-allowed handler on the mux; declare 405 in the spec |
| D4 | `idea why requirements REQ-1` — a plural typo — answers "No provenance edges found", exit 0 | Validate the node type against the known set; a bad type is an error naming the valid ones |
| D5 | `idea config` is byte-identical to `idea model` while help promises "show or validate .idea-harness config" | Render the resolved config; keep `config validate` as-is |

---

## Track E — documentation (P1)

| # | Finding | Fix |
|---|---|---|
| E1 | `api.md:316` documents review verdicts `approve`/`reject` that the API 400s; `openapi.yaml` is already correct | Correct api.md; extend a guard so prose enums are checked against the spec |
| E2 | Demo mode is described by a condition that is not the code's (`README:230`, `quickstart:30-32`) | State the real predicate, after C2 makes it honest |
| E3 | `architecture.md:14` claims to describe the present and stops at M1 | Update the stage graph to the eleven shipped stages |
| E4 | `api.md:305`'s two disposition examples are both invalid values | Use the real enum |
| E5 | `quickstart.md:337` says `0` disables `max_cycles`; the engine coerces `<= 0` to 3, and `configuration.md` says so correctly | Correct the quickstart |

---

## Track F — the guard gap this round exposed (P2, but it is the real lesson)

Four rounds of guards compare **artifacts to artifacts**. Nothing compares the
product to a provider, and nothing executes the documented sequence. Both P0s
live in that gap.

| # | Guard | Catches |
|---|---|---|
| F1 | A table test: every stage budget ≤ every provider's declared ceiling | B1, mechanically, forever |
| F2 | `scripts/smoke.sh` runs the README's block **verbatim**, back-to-back, with no keys | A1 and A2 |
| F3 | The test suite runs with every provider key unset in CI | The env-dependent test I merged red this session |

F2 is the important one: the quickstart is executable and was never executed.

---

## Order

1. **B1, B2** — no design ambiguity, and B1 is the one that breaks every real run.
2. **C1, C2** — silent data loss and silent demo routing.
3. **A1, A2, A3** — the magic path, with F2 landing alongside A1.
4. **D**, then **E**, then **F1/F3**.

Each track: mutation-checked tests, adversarial review before merge, merge on
green.
