#!/usr/bin/env python3
"""Summarize `go test -json` output: what failed, and what was skipped.

`go test` prints nothing for a skipped test, so a CI job that only shows normal
output cannot tell "this platform ran everything" from "this platform silently
skipped the sandbox tests". That distinction is the whole point of a macOS job:
GitHub's macOS runners have no Docker daemon, so the container-backed tests skip
there, and a green check that hides it claims coverage the run does not have.

Usage: ci-test-summary.py <go-test-json-file> [go-test-exit-status]
Exits non-zero if any test or package failed.

The package-level branch is the load-bearing one. This script used to fail only
on an `action == "fail"` event carrying a `Test`, and three of the four ways a
run can die emit no such event:

    compile error                -> build-fail on the ImportPath, then a
                                    package `fail` with FailedBuild
    panic in TestMain            -> package `fail`, no Test
    panic inside a test          -> `fail` WITH a Test (this one was caught)
    panic in a background        -> package `fail` only; the running test never
    goroutine                       gets a terminal event at all

So a job could go green while printing "All tests passed (0 package(s) with
tests)". Every failing shape emits a package-scoped `fail` with no `Test`, and
this file already discriminated on exactly that shape for the *pass* case and
simply had no `fail` counterpart. It has one now.

The optional exit status is a second, cruder signal: it still catches a
truncated or unwritten JSON file, which no event in the stream can describe.
"""
import collections
import json
import sys


def main() -> int:
    if not 2 <= len(sys.argv) <= 3:
        print(__doc__, file=sys.stderr)
        return 2
    go_status = 0
    if len(sys.argv) == 3:
        try:
            go_status = int(sys.argv[2])
        except ValueError:
            print(f"exit status {sys.argv[2]!r} is not a number", file=sys.stderr)
            return 2

    failures: list[tuple[str, str]] = []
    package_failures: list[str] = []
    build_failures: list[str] = []
    skips: dict[str, list[str]] = collections.defaultdict(list)
    output: dict[tuple[str, str], list[str]] = collections.defaultdict(list)
    packages_ok = 0

    with open(sys.argv[1], encoding="utf-8", errors="replace") as f:
        for line in f:
            try:
                event = json.loads(line)
            except ValueError:
                # go1.24+ emits build diagnostics as JSON on stdout
                # (build-output / build-fail), so a non-JSON line here is a
                # genuine oddity rather than the expected build-error path this
                # comment used to describe.
                continue
            action = event.get("Action")
            pkg = event.get("Package", "")
            test = event.get("Test")

            if action == "output" and test:
                output[(pkg, test)].append(event.get("Output", ""))
            elif action == "fail" and test:
                failures.append((pkg, test))
            elif action == "fail" and not test:
                # A package died: a build error, a TestMain panic, or a panic in
                # a background goroutine that took the binary down. Recorded
                # separately because there is no test name to attribute it to,
                # and because a run can produce this with no test-level failure
                # at all.
                package_failures.append(pkg or event.get("ImportPath", "?"))
            elif action == "build-fail":
                build_failures.append(event.get("ImportPath", pkg or "?"))
            elif action == "skip" and test:
                skips[pkg].append(test)
            elif action == "pass" and not test:
                packages_ok += 1

    if failures:
        print(f"{len(failures)} test(s) FAILED:\n")
        for pkg, test in failures:
            print(f"--- FAIL {pkg} {test}")
            for chunk in output[(pkg, test)]:
                sys.stdout.write("    " + chunk.lstrip())
            print()
    elif not package_failures and not build_failures:
        print(f"All tests passed ({packages_ok} package(s) with tests).\n")
    else:
        # Not "all tests passed": no test failed, but a package died. Saying
        # otherwise is how this script reported a green run over a binary that
        # panicked out from under a test.
        print(f"No test failed, but the run did not complete ({packages_ok} package(s) passed).\n")

    if build_failures:
        print(f"{len(build_failures)} package(s) FAILED TO BUILD:")
        for pkg in sorted(set(build_failures)):
            print(f"  {pkg}")
        print()

    # Reported even when `failures` is non-empty: a package that died after one
    # test failed is a different fact from that test failing, and the count is
    # how a reader tells "one bad assertion" from "the binary went down".
    only_pkg = sorted(set(package_failures) - {p for p, _ in failures})
    if only_pkg:
        print(f"{len(only_pkg)} package(s) FAILED with no failing test named:")
        for pkg in only_pkg:
            print(f"  {pkg}")
        print(
            "\nA package fails without naming a test when it does not compile, when\n"
            "TestMain panics, or when a background goroutine panics and takes the\n"
            "test binary down mid-run."
        )
        print()

    if skips:
        total = sum(len(v) for v in skips.values())
        print(f"{total} test(s) SKIPPED on this platform — NOT exercised by this job:")
        for pkg in sorted(skips):
            for test in sorted(skips[pkg]):
                print(f"  {pkg}  {test}")
        print(
            "\nGitHub macOS runners have no Docker daemon, so the container-backed\n"
            "executor and workflow tests skip here. They run in the ubuntu job."
        )
    else:
        print("No tests skipped on this platform.")

    if failures or package_failures or build_failures:
        return 1
    if go_status != 0:
        # Nothing in the stream said so, which means the stream is not the whole
        # story: a truncated or unwritten file, or a failure outside test2json.
        print(f"`go test` exited {go_status} but reported no failure in its JSON stream;\n"
              "the output file is probably truncated or incomplete.")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
