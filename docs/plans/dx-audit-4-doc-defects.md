# DX Audit 4 — documentation defects, below the cut

Audit 4's documentation dive ranked 15 findings and catalogued roughly ten more.
The plan's Track R and Track S covered the ranked set. **This file is the rest**,
plus every documentation defect found while *implementing* the plan — which is
where several of these actually came from, and which the previous round had
nowhere to put.

The v1 fix plan created no ledger for this round at all. That is the identical
failure [`dx-audit-3-doc-defects.md`](dx-audit-3-doc-defects.md) exists to
prevent, in the round whose own theme was bookkeeping, and a review caught it.

See that file for the predicate format and the two kinds of drift
`make ledger-check` fails on. Briefly: the predicate is true exactly when the
defect is still present, so an `open` row whose predicate is false is a
stale-open and a `fixed` row whose predicate is true is a regression.

Status values: `open`, `fixed <commit>`, `deferred <reason>`, `not reproduced`.

## Stale claims about what the code does

| # | Claim | Truth | Predicate | Status |
|---|---|---|---|---|
| D1 | `api.md`: following the SSE stream "is what `idea watch` and the UI do" | Both shipped clients poll; nothing in-tree opens the stream | `absent:docs/api.md:Both shipped clients poll` | fixed 5823d63 |
| D2 | `cli.md`: the Live activity feed arrives "via SSE" | The UI polls on a `setInterval` timer | `absent:docs/cli.md:polled on a timer` | fixed 5823d63 |
| D3 | `openapi.yaml`: operation history is "newest last" | The store orders `created_at DESC` | `present:docs/openapi.yaml:newest last` | fixed 5823d63 |
| D4 | `configuration.md` promised redaction that does not happen | Logs are not redacted and the raw blob store is deliberately redaction-free | `absent:docs/configuration.md:returns it verbatim` | fixed 9152317 |
| D5 | "Review runs require a distinct actor" | There is one actor identity for every automated write; the separation is a prompt and a run, not a checked identity | `absent:docs/security.md:not a checked identity` | fixed 9152317 |
| D6 | `architecture.md` carried the inverted redaction claim a third time | `blobs/` redacts on Put; `raw-blobs/` does not | `absent:docs/architecture.md:deliberately NOT redacted` | fixed 9152317 |
| D7 | The capability broker was listed as shipped in two documents | `CapabilityBroker` has no non-test caller; `TrustAllows` is the gate that runs | `absent:docs/security.md:has no` | fixed 9152317 |
| D8 | The documented with-key quickstart path dead-ends | The daemon reads the environment once, and `idea init` starts it, so a key exported afterwards is invisible | `absent:docs/quickstart.md:reads the environment once` | fixed 25a96ee |
| D9 | Nine model ids that resolve to nothing | e.g. the catalog spells it `claude-3-7-sonnet-latest`; the first call 404s, non-retryably | `absent:docs/quickstart.md:idea model set openai/gpt-4o-mini` | fixed 25a96ee |

## Contract gaps — the spec describing a shape the code does not produce

| # | Claim | Truth | Predicate | Status |
|---|---|---|---|---|
| D10 | `Coverage` omits `status` | The handler has always returned it | `absent:docs/openapi.yaml:RequirementStatus" }` | fixed 5823d63 |
| D11 | `RequirementStatus` enumerates four values | The domain emits six; `failed` and `waived` have been missing since human evidence landed, so a generated client rejects a valid response | `absent:docs/openapi.yaml:enum: [draft, approved, verified, failed, waived, removed]` | fixed 5823d63 |
| D12 | `api.md`'s verify row names only `pass` | The handler accepts `fail` and `waived` too | `absent:docs/api.md:exactly one of` | fixed 5823d63 |
| D13 | Six response fields marshal as `null`, not `[]` | A bare initiative is the normal first state; `type: array` in the spec makes a generated client reject it | `absent:internal/projection/projection.go:NeedsJudgment: []string{}` | fixed 5823d63 |

Found by writing the test for D10 rather than by reading, which is why D11 is
here at all: the enum had been wrong for two audits and three route-level
guards, none of which compare an enum against the constants that feed it.

## CHANGELOG omissions

The changelog recorded most of audit 3's code round. These three were missing,
and two of them are breaking.

| # | Claim | Truth | Predicate | Status |
|---|---|---|---|---|
| D14 | `idea verify`'s exit code change unrecorded | Breaking: it reported the initiative's status, not the gate's, so a CI step that ran it and checked the exit code could never fail | `absent:CHANGELOG.md:exits non-zero when the gate is unmet` | fixed 5823d63 |
| D15 | The finding-disposition `404` unrecorded | Breaking: the update matched no row, reported success, and wrote the provenance event anyway | `absent:CHANGELOG.md:finding that does not exist returns` | fixed 5823d63 |
| D16 | `docs-lint` missing from the guards list | It ships and runs in CI | `absent:CHANGELOG.md:fails on a documented command that is not a real command` | fixed 5823d63 |

## Not reproduced

Recorded rather than dropped, because a finding that does not survive
verification is evidence about the audit and belongs in the record too.

| # | Claim | What was found instead | Predicate | Status |
|---|---|---|---|---|
| D17 | "`container` is a second placeholder executor listed as a real kind in four documents" | `ContainerExecutor` is fully implemented — `Start` builds a real `docker run` argv with `--network none`, `--read-only`, `--cap-drop ALL`, and it is wired in both `workflow/execute.go` and `daemon/handlers.go`. The four documents are correct | `manual:the finding asserts absence of an implementation that exists; nothing to grep for` | not reproduced |
| D18 | "`/guidance`'s `affected` is a sixth null array" | Every branch of `BuildGuidance` assigns a literal. Pinned by a test that loops over all six initiative statuses rather than by a reading | `absent:internal/projection/null_slices_test.go:settles that rather than a reading` | not reproduced |

## Appending to this file

Anything found later that is not in this table should be appended here rather
than fixed silently, so the count stays checkable — and every row needs a
predicate, or `make ledger-check` cannot see it.
