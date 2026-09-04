# Deferred items

One line per thing a round decided not to do, with where it came from. Appended
to, never rewritten — a plan file is superseded by the next round's plan, so
"recorded in the plan" has repeatedly meant "lost".

Started in DX audit 3, after review found that audit 2's below-the-cut list was
dropped and one of its items (README's provider list omitting DeepSeek) was
still unfixed a round later and had reappeared as new work.

| Origin | Item | Why deferred |
|---|---|---|
| audit 3 / S4 | A redacting `slog` handler; daemon logs are unredacted and readable via `GET /api/v1/logs` | Larger than a doc round; the gap is documented instead |
| audit 3 / S6 | Container `--user` passes the daemon's UID, so a root daemon gives a root container | Needs a policy decision on refusing to run as root |
| audit 3 / S6 | Hermes image pinned by tag, not digest; no signature verification | Supply-chain work, separate round |
| audit 3 / S6 | Control-plane deny does not cover `.github/workflows/` and other auto-executing files | Needs a decision on scope vs. false positives |
| audit 3 / G2 | Live-response schema check against the smoke daemon | The `api.md` ↔ `openapi.yaml` axis (built this round) covers these findings more cheaply |
| audit 3 / G1b | A terminal-state assertion in `scripts/smoke.sh` | Not done, and would not have worked as planned: smoke.sh never runs `idea model set`, so it never reaches O1's shape (a configured model with no key). Needs the script to exercise that path first — a separate piece of work, not ten lines |
| audit 3 / C5 | Reload-safety: mutex-snapshot at the ~13 engine entry points, fresh `*projectRuntime` swap | Not attempted this round; the comments still overclaim. Highest-value remaining item |
| audit 3 / C6 | Shrink the engine mutex around model calls; server timeouts; body-size middleware | Not attempted; depends on C5 landing first |
| audit 3 / D4 | `idea dev` clobbers the live daemon's pidfile | Not attempted |
| audit 3 / D5 | A 413 on doc upload writes two JSON bodies | Not attempted |
| audit 3 / D6 | `changed` is project-wide and picks the oldest initiative | Not attempted |
| audit 3 / D7, D8 | `idea init <elsewhere>` next-step hint; trace-type validation | Not attempted |
| audit 3 / Track A | `GET /operations/{id}` reconciles every project as a side effect of a read; stranded `checks_running`; `PATCH /tasks` erases estimate; decision/question 400-vs-404; SSE write deadline | Not attempted |
| audit 3 / C4 | `SuggestAnswersInitiative` has no existence check and no running guard — the only entry point with neither | Not attempted; named in the plan and missed in implementation |
| audit 3 | No SDK | Deliberate; generating one from the spec is a milestone |
| audit 3 | `linux/arm64` untested in CI | Hosted arm64 Linux runners unavailable on this repo's plan; stated in README |
| audit 3 | Cutting `0.2.0` | **No longer deferred** — cut as `v0.2.0` on 2026-09-04, after audit 5. It had been parked since audit 3 as "a decision after this lands", through three more rounds |
| audit 2 | README provider list omitted DeepSeek | **Was** deferred by omission; fixed in audit 3 as R13 |
| audit 3 / S3 | `architecture.md`'s redaction half | **No longer deferred** — fixed in audit 4 (`9152317`). Recorded because audit 3 planned it, did not do it, and did not record it either, which is how it survived a round with no owner |
| audit 3 / R15 | The stale `routes.go` comment claiming the flat routes are still live | **No longer deferred** — fixed in audit 4 (`5823d63`). Same story: planned, undone, unrecorded, and it then survived two rounds |
| audit 3 | The trust-level code change | Still not attempted. Named in audit 3's plan, absent from its implementation and from this file until audit 4 |
| audit 4 / C4c | The success-path terminal test | Needs Runtime-level fixtures; C4a's AST census and C4b's failure-path tests landed without it |
| audit 4 / S2 | Enforcing a distinct reviewer actor | There is no actor identity to compare — every automated write shares one. `security.md` documents the gap instead |
| audit 4 / R3 | Response-schema checking as a guard | **Request** side now guarded — provider wire-format goldens plus an assertion that the emitted token budget never exceeds the declared ceiling, which is where the audit-5 token P0 lived. Response schemas remain unchecked; a live-response check against the smoke daemon is still the same item audit 3 deferred under G2 |
| audit 5 / A2 | Requiring a change set before an initiative may reach `complete` | **Rejected on review, not merely deferred.** The gate would break five existing tests and, more importantly, kill a documented, tested, supported flow: an initiative whose only requirement is *waived* completes today with zero change sets (`verify_test.go:296`, `openapi.yaml:2876`, `ui.md:227`). The predicate the plan borrowed from `ui.md:101` is also wrong — `rejected` and `superseded` are absorbing states minted on ordinary paths (every credential-sweep reject re-opens the task and leaves one behind), so `{applied, reverted}` would permanently block any initiative that ever tripped the sweep. And `VerifyInitiative` returns only `error`, with no channel to say *why* it declined, so the CLI would print `✓ verified` and exit 0 over a refusal — the same false success moved one command left. The false success this was aimed at is fixed at its cause instead (audit 5 / A1) |
| audit 5 / F2 | `scripts/smoke.sh` runs the README block verbatim | Not achievable as specified. The fence is one block of eight commands including `export OPENAI_API_KEY=sk-…` (a literal U+2026) and a `model set` gated only by prose, so "verbatim" means re-encoding a judgment about which two lines to skip. `docslint.go:10-18` already examined and rejected running documented commands live, and `deferred.md`'s audit-3 G1b row records the same conclusion. Superseded in practice: the `idea new` fix makes the block deterministic, and it is pinned by unit tests that assert the operation is polled |
| audit 5 / F3 | Run the test suite with provider keys unset in CI | **Already true.** `.github/` references `ANTHROPIC_API_KEY` only inside the `workflow_dispatch`-only agent job; every push job is key-free, which is how CI caught the flaky test this round. The real gap is local-vs-CI divergence, and there is no pre-push hook — that is the item worth doing |
| audit 5 / D3 | JSON 404/405 on the mux | **404 half done** — unmatched `/api/` paths answer the documented envelope. The 405 half stays: Go synthesizes it only when no pattern matches, and the UI catch-all prevents that, so emitting it means restructuring how the UI handler is registered. |
| audit 5 | `/doctor` returns `null`; `/diff` mixes `[]` and `null` in one response | **No longer deferred** — both fixed, and the test now walks the route set rather than a hand-written list, which is what caused the original miss. |
| audit 5 | Recorded-cassette provider contract tests | **Done in a cheaper form** — wire-format goldens per provider plus a ceiling assertion, needing no key and no network. A true cassette (recorded real responses) would additionally cover response parsing; not attempted |
| audit 5 | Cutting `0.2.0` | **No longer deferred** — cut as `v0.2.0`. It was the load-bearing item: every P0 audits 2-5 fixed had shipped in `v0.1.0`, so until the tag existed the only installable artifact contained all of them |
| audit 5 | No per-command `--help` on any of the 33 subcommands | **No longer deferred** — rendered from the same command table that drives `idea help` and docs/cli.md. |
| audit 5 | `idea version` reports `v0.1.1-0.2026…`, a version that does not exist | **No longer deferred** — fixed in `v0.2.0`. A module version is trusted only when there is no VCS data; the `(devel)` guard it replaced stopped matching when Go began reporting a pseudo-version for local builds |
| audit 4 / D17 | `container`'s "placeholder" status | **Not reproduced.** `ContainerExecutor` is fully implemented and wired in two call sites; see [`dx-audit-4-doc-defects.md`](dx-audit-4-doc-defects.md) |
