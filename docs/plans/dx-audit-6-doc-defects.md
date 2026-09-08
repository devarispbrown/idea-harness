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
| D3 | `demo-existing-project.md:103`: `idea next --until-human` prints one line, `→ execute …` | It prints `→ advance …` then `→ execute …`; the next sentence claims parity with the greenfield guide, which shows both | `absent:docs/demo-existing-project.md:advance` | open |
| D4 | `demo-greenfield.md:69`: "the open questions with `idea decisions`" | `idea decisions` prints `No decisions yet.` in that state; `idea explain` is what shows them, and no document names it | `present:docs/demo-greenfield.md:open questions with` | open |
| D5 | `demo-greenfield.md:120,135,164`: `idea apply`, `idea verify` and `idea revert` output blocks each omit a line the binary prints | `apply` also prints `cancel with: idea cancel <op>`; `verify` prints `✓ evidence recorded` first; `revert` has no block at all | `manual:each block must be diffed against a real run` | open |
| D6 | `ui.md:14`: "every screen polls" | Five pages never poll — `HealthPage`, `DecisionPage`, `DocReader`, `ArtifactDiffPage`, `Settings` | `present:docs/ui.md:Every screen refreshes` | open |
| D7 | `ui.md:261`: demo mode is "no `providers:` entry and none of the known key vars set" | `defaultProviderName(cfg) == demoProviderName`; env keys are never consulted, and the code comment says the env definition was wrong in both directions | `present:docs/ui.md:key variables set in the environment` | open |
| D8 | `README:159`, `quickstart:47`, `quickstart:258`: `idea doctor` "reports a missing key" | doctor reads the **CLI's** environment, not the daemon's, so it prints `all checks passed` in exactly the state those lines describe. `idea model set` is the detector that works | `manual:three sites, and the fix is a product change not a wording change` | open |
| D9 | `quickstart.md:273`: an `idea doctor` output block showing a `warn` line and the demo-mode line together | They are mutually exclusive — one needs `default_model` set, the other needs it unset | `present:docs/quickstart.md:demo mode — no model configured` | open |
| D10 | `api-reference-hosting.md:141`: "`make api-check` compares paths only" | It also checks request-body fields and 409 declarations | `present:docs/api-reference-hosting.md:compares paths` | open |

## Gaps

| # | Gap | What is missing | Predicate | Status |
|---|---|---|---|---|
| G1 | Leaving `needs_human_judgment` from the CLI | Nothing documented it because nothing could do it | `absent:README.md:idea approve` | fixed 38329d5 |
| G2 | `idea retry` is never explained | Both places describing an initiative going to `failed` omit the recovery command | `absent:README.md:idea retry` | open |
| G3 | Which initiative a bare command acts on | `resolveInitiative` silently targets the newest; no document says so | `absent:docs/cli.md:most recent initiative` | open |
| G4 | `--json` response shapes | `cli.md:78` implies parity with the human form; `idea status --json` drops four fields | `absent:docs/cli.md:--json shape` | open |
| G7 | Nothing said to restart the daemon after upgrading | It is detached and outlives the binary, so the old version keeps serving while `idea version` reports the new one — and `idea doctor` affirmed health through it in both directions | `absent:README.md:outlives the binary` | fixed audit-6 |

## The guard gap itself

| # | Gap | What is missing | Predicate | Status |
|---|---|---|---|---|
| G5 | Nothing diffs a documented command's stdout against the block beside it | D3–D5 and D9 all live there; the greenfield sequence runs in ~5s on the demo provider | `manual:a demo-check guard is proposed, not built` | deferred needs its own round |
| G6 | `ledger-check` covered no audit-5 row | It globbed two files, reported 59 rows, and every audit-5 documentation fix went unguarded; two regressed by omission. A ledger cannot assert its own existence — the row count is the evidence: 59 before, 66 after audit 5 got one | `manual:ledger-check reports the row count it reads` | fixed audit-6 |
