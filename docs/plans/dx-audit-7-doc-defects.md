# DX Audit 7 — documentation defects

Audit 7's method finding is about this file's own format, so it goes first.

**A green ledger row is not evidence of a correct fix.** Three of audit 6's
~19 `fixed audit-6` rows were green over replacement text that was itself false
(D7, G3, D9 in the audit 7 report), and one — D8d — carried a predicate whose
file was not the file its claim cited, so it passed on an import line. A
`present:` predicate proves a specific false sentence is *gone*. It cannot prove
the sentence that replaced it is *true*.

So every row this round carries **both directions**: a `present:` predicate
asserting the false claim is gone, and an `absent:` predicate asserting the
correct claim is now stated. Audit 6's D8 row got this right by accident; the
three that failed had only the `present:` half.

See [`dx-audit-3-doc-defects.md`](dx-audit-3-doc-defects.md) for the predicate
format. The predicate is true exactly when the defect is still present.

Status values: `open`, `fixed <ref>`, `deferred <reason>`, `not reproduced`.

## Release and compatibility

| # | Claim | Truth | Predicate | Status |
|---|---|---|---|---|
| R2a | `docs/api-reference-hosting.md`: "no guard reads `info.version` at all … three axes, none of them the version" | `make version-check` reads it on two axes — staleness against `CHANGELOG.md` on every push, and exactness against the tag in the release job | `present:docs/api-reference-hosting.md:three axes, none of them the version` | fixed audit-7 |
| R2b | The same document's instruction "Bump `info.version` when you tag" | The bump moves to merge time, which is what makes the every-push check passable. Three consecutive tags missed the tag-time instruction | `absent:docs/api-reference-hosting.md:The bump moves at merge, not at tag` | fixed audit-7 |
| R2c | `docs/openapi.yaml`'s `info.version` | Sat at `0.1.0` from the file's creation through v0.1.0, v0.2.0 and v0.3.0, while the spec gained four response codes, a schema, two properties and an enum in audit 7's round alone | `present:docs/openapi.yaml:version: 0.1.0` | fixed audit-7 |
| R8a | `CONTRIBUTING.md`'s "Build & test" set is `make build/test/vet/embed-web` | CI additionally gates on nine targets a contributor is never told about, so following the file exactly produces a red PR | `absent:CONTRIBUTING.md:CI gates on nine more targets than those four` | fixed audit-7 |
| R8b | `CONTRIBUTING.md`: "the Makefile and scripts honor `GOPATH`/`GOCACHE`/`GOMODCACHE`" | `grep GOPATH Makefile` returns nothing; only the scripts do | `present:CONTRIBUTING.md:the Makefile and scripts honor` | fixed audit-7 |
| R8c | No release checklist exists anywhere: `CONTRIBUTING.md` contains no occurrence of "release", there is no `RELEASING.md`, and `git tag`/`goreleaser` appear only in `CHANGELOG.md` and two archived audit plans | The one instruction that existed lived in a hosting document nobody reads at tag time | `absent:RELEASING.md:What gates a tag` | fixed audit-7 |
| R8d | (report P1-8) `.github/workflows/release.yml` gates a tag on `go vet` and `go test` on one OS: none of the eight guards, no smoke, no macOS, no web tests, no `needs:` | A red `enum-check` or `check-embed` shipped | `manual:the false state is the ABSENCE of a needs: in a file this round deletes, so there is no literal to match — a predicate naming a deleted path is an error in evalPredicate and would turn ledger-check permanently red. internal/ciguard.TestReleaseGatesOnEveryCIJob is the mechanical check that holds it, and it fails when a job is dropped from needs` | fixed audit-7 |
| V1 | `web/src/HealthPage.tsx`: `{health?.version ?? "v0.1.0"}` | A frozen `v0.1.0` rendered in the UI whenever `/health` had not resolved — `info.version`'s defect, user-visible | `present:web/src/HealthPage.tsx:?? "v0.1.0"` | fixed audit-7 |

## Testing and quality

| # | Claim | Truth | Predicate | Status |
|---|---|---|---|---|
| T1 | `scripts/ci-test-summary.py`'s docstring: "Exits non-zero if any test or package failed" | It failed only on an `action == "fail"` carrying a `Test`. A compile error, a `TestMain` panic and a background-goroutine panic all emit no such event, so the macOS job could go green printing "All tests passed (0 package(s) with tests)" | `absent:scripts/ci-test-summary.py:panic in a background` | fixed audit-7 |
| T2 | The same file: "a build error is printed as plain text, not JSON" | go1.24+ emits `build-output`/`build-fail` as JSON on stdout | `present:scripts/ci-test-summary.py:a build error is printed as plain text` | fixed audit-7 |

## Still open

| # | Claim | Truth | Predicate | Status |
|---|---|---|---|---|
| D4 | `demo-greenfield.md`: "the open questions with `idea decisions`" | `idea decisions` prints `No decisions yet.` in that state; `idea explain` is what shows them, and no prose names it | `present:docs/demo-greenfield.md:open questions with` | open |
| V2 | `web/package.json`: `"version": "0.1.0"`, frozen since creation | Nothing reads it and it is not published to a registry, so it is cosmetic — but it is the same class | `present:web/package.json:"version": "0.1.0"` | deferred not published, nothing reads it |
| V3 | `README.md`: `idea version  # v0.3.0 (a1b2c3d)` | An example that has to move at each tag, with no guard. `make docs-lint` is the family that would own it | `present:README.md:# v0.3.0 (a1b2c3d)` | deferred needs a docs-lint axis for version examples |
