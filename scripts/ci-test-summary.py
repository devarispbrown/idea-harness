#!/usr/bin/env python3
"""Summarize `go test -json` output: what failed, and what was skipped.

`go test` prints nothing for a skipped test, so a CI job that only shows normal
output cannot tell "this platform ran everything" from "this platform silently
skipped the sandbox tests". That distinction is the whole point of a macOS job:
GitHub's macOS runners have no Docker daemon, so the container-backed tests skip
there, and a green check that hides it claims coverage the run does not have.

Usage: ci-test-summary.py <go-test-json-file>
Exits non-zero if any test or package failed.
"""
import collections
import json
import sys


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2

    failures: list[tuple[str, str]] = []
    skips: dict[str, list[str]] = collections.defaultdict(list)
    output: dict[tuple[str, str], list[str]] = collections.defaultdict(list)
    packages_ok = 0

    with open(sys.argv[1], encoding="utf-8", errors="replace") as f:
        for line in f:
            try:
                event = json.loads(line)
            except ValueError:
                continue  # a build error is printed as plain text, not JSON
            action = event.get("Action")
            pkg = event.get("Package", "")
            test = event.get("Test")

            if action == "output" and test:
                output[(pkg, test)].append(event.get("Output", ""))
            elif action == "fail" and test:
                failures.append((pkg, test))
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
    else:
        print(f"All tests passed ({packages_ok} package(s) with tests).\n")

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

    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
