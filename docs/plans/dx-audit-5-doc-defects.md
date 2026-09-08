# DX Audit 5 — documentation defects

**Written retrospectively, during audit 6.** Audit 5 shipped a report and a fix
plan and **no ledger at all**, which is why this file exists and why two of the
rows below say `open` on defects audit 5's own CHANGELOG announced as fixed.

`ledger-check` globs `docs/plans/dx-audit-*-doc-defects.md`. Audits 3 and 4 have
one; audit 5 did not. Two files matched, the check passed — `59 rows checked`,
none of them audit 5's — and every audit-5 documentation fix went unguarded for
a round. E1 and E4 below then regressed by omission: both were corrected in
`docs/openapi.yaml` and never in `docs/api.md`, while `CHANGELOG.md` announced
them as done.

That is the third time this repository has planned a fix, not made it, and not
recorded it. [`deferred.md`](deferred.md) names the pattern in three separate
rows. The mechanical fix is this file.

See [`dx-audit-3-doc-defects.md`](dx-audit-3-doc-defects.md) for the predicate
format. Briefly: the predicate is true exactly when the defect is still present,
so an `open` row whose predicate is false is a stale-open, and a `fixed` row
whose predicate is true is a regression.

Status values: `open`, `fixed <commit>`, `deferred <reason>`, `not reproduced`.

## Track E — the documentation findings audit 5 ranked

| # | Claim | Truth | Predicate | Status |
|---|---|---|---|---|
| E1 | `api.md:316` documented review verdicts `approve`/`reject` | `execute.go:1255` switches on `approved`/`rejected` and 400s anything else | `absent:docs/api.md:a client that posts` | fixed audit-6 |
| E2 | Demo mode described by a condition that is not the code's | `defaultProviderName(cfg) == demoProviderName`; the env vars are never consulted | `absent:docs/quickstart.md:default_model` | fixed 203b377 |
| E3 | `architecture.md` claimed to describe the present and stopped at M1 | Eleven stages ship; the graph now carries M1–M4 and both `needs_human_judgment` exits | `absent:docs/architecture.md:execution_plan_review` | fixed audit-6 |
| E4 | `api.md:305`'s two disposition examples were both invalid values | The enum is the seven values in `internal/domain/enums.go:124` | `absent:docs/api.md:accepted_risk` | fixed audit-6 |
| E5 | `quickstart.md:339` said "`0` disables each" of three caps, `max_cycles` among them | The engine coerces `<= 0` to 3 (`engine.go:115`); `configuration.md:68` says so explicitly, so the two documents contradicted each other | `present:docs/quickstart.md:disables each` | fixed audit-6 |

**Four of these five were still open when this file was written, a round after
audit 5 shipped and its CHANGELOG announced them.** E1 and E4 were corrected in
`docs/openapi.yaml` and never in `docs/api.md` — `CHANGELOG.md:313` says
*"documents the verdicts it actually accepts"*, which was true of one file of
two. E3 and E5 were planned in Track E and never done at all: the stage graph
still stopped at `approved`, and `quickstart.md:339` still said "`0` disables
each" of three caps while `configuration.md:68` said the opposite about one of
them. All four are fixed in the audit-6 round, which is also when this ledger
first existed to hold them.

## Found while implementing audit 5's plan

| # | Claim | Truth | Predicate | Status |
|---|---|---|---|---|
| E6 | `engine.go` asserted stage budgets "stay within the providers' declared MaxOutput ceilings" four lines above a 32000 that exceeded all three | The ceiling is enforced now, in two places, and the comment describes a *want* | `absent:internal/workflow/engine.go:want` | fixed 203b377 |
| E7 | Nothing documented that `idea new` waits for the run it starts | The README's own block depends on it | `absent:README.md:waits for the run it starts` | fixed 203b377 |
