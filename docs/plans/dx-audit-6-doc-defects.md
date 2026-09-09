# DX Audit 6 — documentation defects

Audit 6's documentation dive found 0 broken links and all seven guards green,
with a P0 and two fabricated output blocks already in the tree. Everything below
sits in the space no guard looks at.

The systemic gap the dive named: **not one guard executes a documented command
and compares its stdout to the fenced block beside it.** Findings D3–D6 are all
of that kind, and they are in the two documents the README now tells everyone to
start with — written in the round before, by me, with the behavioural assertions
verified by running them and the output blocks hand-written.

See [`dx-audit-3-doc-defects.md`](dx-audit-3-doc-defects.md) for the predicate
format. The predicate is true exactly when the defect is still present.

Status values: `open`, `fixed <ref>`, `deferred <reason>`, `not reproduced`.

## Claims contradicted by the binary

| # | Claim | Truth | Predicate | Status |
|---|---|---|---|---|
| D1 | `api.md:184`: `/next` "collapses `running` and `failed` to the same `wait`" | It distinguishes them and carries `failure_reason` | `present:docs/api.md:collapses` | fixed b320a6b |
| D2 | `openapi.yaml`: the same claim, as the operation description | Same | `absent:docs/openapi.yaml:not steps` | fixed b320a6b |
| D3 | `demo-existing-project.md:103`: `idea next --until-human` prints one line, `→ execute …` | It prints `→ advance …` then `→ execute …`; the next sentence claims parity with the greenfield guide, which shows both | `absent:docs/demo-existing-project.md:→ advance` | fixed audit-6 |
| D4 | `demo-greenfield.md:69`: "the open questions with `idea decisions`" | `idea decisions` prints `No decisions yet.` in that state; `idea explain` is what shows them, and no document names it | `present:docs/demo-greenfield.md:open questions with` | open |
| D5a | `demo-greenfield.md:120`: the `idea apply` block omits the cancel hint | The binary prints `cancel with: idea cancel <op>` | `absent:docs/demo-greenfield.md:apply started CHG-1 cancel with` | fixed audit-6 |
| D5b | `demo-greenfield.md:135`: the `idea verify` block omits its first line | The binary prints `✓ evidence recorded for REQ-1` before the verdict | `absent:docs/demo-greenfield.md:evidence recorded for REQ-1` | fixed audit-6 |
| D5c | `demo-greenfield.md:164`: `idea revert` has no output block at all | It prints two lines, the second naming the cancel command | `absent:docs/demo-greenfield.md:✓ revert started` | fixed audit-6 |
| D11 | `demo-greenfield.md:61`: the `idea status` block shows two of six lines | It omits the demo banner, both blank lines and the whole two-line next-step section | `absent:docs/demo-greenfield.md:The reviewed PRD is ready. Advance to architecture` | fixed audit-6 |
| D12 | `demo-greenfield.md:80` and `demo-existing-project.md:105`: the executor warning is hand-wrapped over two lines with a continuation indent | The binary prints it as one line | `manual:ledger-check collapses a newline plus indent to one space, so the wrapped and unwrapped forms are identical to it` | fixed audit-6 |
| D13 | `demo-existing-project.md:61`: the `idea new` block omits the demo banner | The greenfield guide shows it for the same command | `absent:docs/demo-existing-project.md:demo mode — synthetic output` | fixed audit-6 |
| D14 | `demo-existing-project.md:87`: the `idea docs` preview shows three lines | The preview is 200 runes of the file with one leading indent, so everything past line 1 sits flush left | `absent:docs/demo-existing-project.md:def charge(calls: int) -> float:` | fixed audit-6 |
| D15 | `demo-existing-project.md:152`: the revert block shows only its `git status` half | `idea revert` prints two lines of its own first | `absent:docs/demo-existing-project.md:✓ revert started` | fixed audit-6 |
| D6 | `ui.md:14`: "every screen polls" | Five screens never poll — Health, Decision, the doc reader, the artifact diff and Settings load once and go stale until reload. The cited literal was also wrong for `App.tsx`, which uses an inline closure | `present:docs/ui.md:Every screen refreshes` | fixed audit-6 |
| D7 | `ui.md:261`: demo mode is "no `providers:` entry and none of the known key vars set" | `isDemo` is `defaultProviderName(cfg) == demoProviderName`; environment variables are never consulted, and the old wording was wrong in both directions | `present:docs/ui.md:key variables set in the environment` | fixed audit-6 |
| D8 | `README:171`, `quickstart:47`, `:74`, `:258`, `demo-greenfield:157`, `security.md:20`: `idea doctor` "reports a missing key" | doctor read the **CLI's** environment, not the daemon's, so it printed `all checks passed` in exactly the state those lines describe — and warned spuriously in the reverse setup. Six sites, not three; the daemon now answers, via a `key_set` field that does not conflate a missing key with a refused entry | `absent:README.md:asks the **daemon** whether it has the key` | fixed audit-6 |
| D9 | `quickstart.md:273`: an `idea doctor` output block showing a `warn` line and the demo-mode line together | They are mutually exclusive — one needs `default_model` set, the other needs it unset. Replaced with the real failing-check output, since D8 rewrote it | `present:docs/quickstart.md:demo mode — no model configured; run` | fixed audit-6 |
| A13 | `openapi.yaml`: cancel is "the only path where a missing operation reads as 409 rather than 404" | Five endpoints did it, and cancel no longer does — an unknown id is 404 on all of them | `present:docs/openapi.yaml:only path where a missing operation` | fixed audit-6 |
| D8d | `security.md:20` claimed a refused entry's reason is "shown against that provider in the UI" | The UI rendered every non-ready provider as "missing key" and did not parse `problem` at all — its `Provider` type had no such field. `idea model ls` had the same conflation and reported a key that is set as `unset` | `absent:web/src/Settings.tsx:providerStatusLabel` | fixed audit-6 |
| D10 | `api-reference-hosting.md:141`: "`make api-check` compares paths only" | Three axes — routes to paths, request-body fields to handlers, 409s to the preconditions that answer them. None of them reads `info.version`, which is the point the sentence was making | `present:docs/api-reference-hosting.md:compares paths only` | fixed audit-6 |

## Gaps

| # | Gap | What is missing | Predicate | Status |
|---|---|---|---|---|
| G1 | Leaving `needs_human_judgment` from the CLI | Nothing documented it because nothing could do it | `absent:README.md:idea approve` | fixed 38329d5 |
| G2 | `idea retry` is never explained | Both places describing an initiative going to `failed` omitted the recovery command; it appeared only in the generated command table | `absent:README.md:idea retry` | fixed audit-6 |
| G3 | Which initiative a bare command acts on | `resolveInitiative` silently targets the most recently created one — not the last touched, not one you selected | `absent:docs/cli.md:most recently` | fixed audit-6 |
| G4 | `--json` response shapes | `cli.md` implied parity with the human form. `idea status --json` is the wire response — three fields — while the rendered form composes counts and guidance from other endpoints | `absent:docs/cli.md:the wire response, not the rendered view` | fixed audit-6 |
| G7 | Nothing said to restart the daemon after upgrading | It is detached and outlives the binary, so the old version keeps serving while `idea version` reports the new one — and `idea doctor` affirmed health through it in both directions | `absent:README.md:outlives the binary` | fixed audit-6 |

## The guard gap itself

| # | Gap | What is missing | Predicate | Status |
|---|---|---|---|---|
| G5 | Nothing diffs a documented command's stdout against the block beside it | D3–D5, D11–D15 all lived there | `manual:a full block-diffing guard was designed, reviewed twice and judged not worth its surface; the eight blocks are fixed and guarded from both sides instead` | fixed audit-6 |
| G6 | `ledger-check` covered no audit-5 row | It globbed two files, reported 59 rows, and every audit-5 documentation fix went unguarded; two regressed by omission. A ledger cannot assert its own existence — the row count is the evidence: 59 before, 66 after audit 5 got one | `manual:ledger-check reports the row count it reads` | fixed audit-6 |
