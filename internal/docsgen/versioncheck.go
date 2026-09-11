// The spec's own version number, which no guard has ever read.
//
// docs/openapi.yaml has said `version: 0.1.0` since the file was created --
// `git log -L '/^info:/,+5:docs/openapi.yaml'` returns exactly one commit. It
// did not move for v0.2.0 or v0.3.0, while the spec itself gained response
// codes, schemas, properties and an enum. docs/api-reference-hosting.md carries
// the instruction "Bump `info.version` when you tag", in a document nobody reads
// at tag time, and three consecutive tags missed it. That is what an unowned
// manual step looks like, and the remedy for an unowned manual step is not a
// louder instruction.
//
// Two checks, because they catch different things and one of them can run far
// earlier than the other:
//
//   - Staleness (every push). `info.version` must be at least the newest
//     released heading in CHANGELOG.md. This never predicts the next version --
//     it only refuses a spec that describes a release already superseded -- so
//     it runs beside the other guards on every push. Against this tree's
//     history it would have gone red the first push after v0.2.0 shipped, four
//     rounds before a tag-time check could have fired.
//
//   - Exactness (tag time). `info.version` must equal the tag being built. This
//     is the only thing that catches "bumped to 0.4.0, tagged v0.5.0". It fires
//     late, but before goreleaser publishes anything.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// changelogPath is the release record the staleness check reads.
const changelogPath = "CHANGELOG.md"

// specVersionRe reads `version:` from the spec's info block. Anchored to a
// two-space indent so it cannot match a `version:` nested deeper in the
// document (the spec has several, in schema examples).
// The trailing `.*` tolerates a comment after the value. `\s*$` reported
// `version: 0.4.0  # bumped per RELEASING.md` as "no version line at all",
// which fails closed but diagnoses the wrong thing.
var specVersionRe = regexp.MustCompile(`(?m)^  version:\s*"?([0-9]+\.[0-9]+\.[0-9]+)"?\s*(?:#.*)?$`)

// releasedHeadingRe matches a released CHANGELOG heading. `## [Unreleased]`
// deliberately does not match: it carries no version, and skipping it is what
// keeps the staleness check from demanding a number nobody has chosen yet.
// The `v?` matters: with a bare-number pattern, renaming one heading to
// `## [v0.5.0]` made the check silently fall back to the previous release and
// pass, and prefixing every heading turned the check into a complete no-op that
// still printed a green line. parseSemver strips the prefix either way.
var releasedHeadingRe = regexp.MustCompile(`(?m)^## \[v?([0-9]+\.[0-9]+\.[0-9]+)\]`)

// semver is a parsed major.minor.patch, comparable.
type semver struct{ major, minor, patch int }

func parseSemver(s string) (semver, error) {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(s), "v"), ".")
	if len(parts) != 3 {
		return semver{}, fmt.Errorf("%q is not a major.minor.patch version", s)
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return semver{}, fmt.Errorf("%q is not a major.minor.patch version", s)
		}
		out[i] = n
	}
	return semver{out[0], out[1], out[2]}, nil
}

func (v semver) String() string { return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch) }

// less reports v < o.
func (v semver) less(o semver) bool {
	if v.major != o.major {
		return v.major < o.major
	}
	if v.minor != o.minor {
		return v.minor < o.minor
	}
	return v.patch < o.patch
}

// specVersion reads info.version out of the spec text.
func specVersion(spec string) (semver, error) {
	m := specVersionRe.FindStringSubmatch(spec)
	if m == nil {
		return semver{}, fmt.Errorf("docs/openapi.yaml has no `  version: X.Y.Z` line in its info block")
	}
	return parseSemver(m[1])
}

// newestReleased returns the highest released version in a CHANGELOG.
//
// Highest, not first: the file is newest-first today, but a check that depends
// on ordering silently stops checking the moment an entry is inserted out of
// order, and this one is meant to survive that.
func newestReleased(changelog string) (semver, bool, error) {
	var best semver
	found := false
	for _, m := range releasedHeadingRe.FindAllStringSubmatch(changelog, -1) {
		v, err := parseSemver(m[1])
		if err != nil {
			return semver{}, false, err
		}
		if !found || best.less(v) {
			best, found = v, true
		}
	}
	return best, found, nil
}

// runVersionCheck runs the staleness check always, and the exactness check when
// a version is supplied.
func runVersionCheck(want string) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	specRaw, err := os.ReadFile(filepath.Join(root, specPath))
	if err != nil {
		return err
	}
	have, err := specVersion(string(specRaw))
	if err != nil {
		return err
	}

	clRaw, err := os.ReadFile(filepath.Join(root, changelogPath))
	if err != nil {
		return err
	}
	released, ok, err := newestReleased(string(clRaw))
	if err != nil {
		return fmt.Errorf("%s: %w", changelogPath, err)
	}
	if !ok {
		// Not a pass. Every release this project has cut has a heading, so no
		// heading means the file moved or the format changed -- and the check
		// would otherwise print a green line reporting `>= released 0.0.0`,
		// which is the shape of a guard that has quietly stopped guarding.
		return fmt.Errorf(
			"%s has no released `## [X.Y.Z]` heading, so info.version cannot be checked against it.\n"+
				"    If the heading format changed, releasedHeadingRe in internal/docsgen/versioncheck.go\n"+
				"    has to change with it -- silently skipping the check is what this error exists to prevent.",
			changelogPath)
	}
	if have.less(released) {
		return fmt.Errorf(
			"docs/openapi.yaml says version %s, but %s has already released %s.\n"+
				"    The spec describes a release that has been superseded. Bump info.version to the\n"+
				"    version this tree will ship as. See RELEASING.md.",
			have, changelogPath, released)
	}

	// Released history is checked on the same target rather than a tenth make
	// target: skill 2.9.4 makes a documented check with no CI step its own P1
	// gate (UNVERIFIABLE_CI_PARITY), and version-check is already the release
	// guard that reads CHANGELOG.md.
	histLine, err := runChangelogHistoryCheck()
	if err != nil {
		return err
	}

	if want == "" {
		fmt.Printf("version-check: docs/openapi.yaml info.version = %s (>= released %s)\n", have, released)
		fmt.Println(histLine)
		return nil
	}

	target, err := parseSemver(want)
	if err != nil {
		if strings.ContainsAny(strings.TrimPrefix(want, "v"), "-+") {
			return fmt.Errorf(
				"tag %s is a pre-release, and this guard compares major.minor.patch only.\n"+
					"    docs/openapi.yaml's info.version has no pre-release form to match it against, and\n"+
					"    RELEASING.md documents no pre-release flow. Add one to both before tagging an rc.",
				want)
		}
		return fmt.Errorf("VERSION=%q: %w", want, err)
	}
	if target != have {
		return fmt.Errorf(
			"tag %s does not match docs/openapi.yaml info.version %s.\n"+
				"    Bump info.version to %s, commit, delete the tag and re-push it. Nothing has been\n"+
				"    published: this runs before goreleaser. See RELEASING.md.",
			want, have, target)
	}
	fmt.Printf("version-check: docs/openapi.yaml info.version = %s, matching tag %s\n", have, want)
	fmt.Println(histLine)
	return nil
}
