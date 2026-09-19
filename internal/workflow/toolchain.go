package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// detectToolchainImage inspects root's toolchain markers and derives a check
// sandbox image, for when no check.image is configured (review C5). It reads
// only the markers the ICP round named -- Node (.nvmrc or package.json
// engines.node), Go (go.mod's go directive), and Python (.python-version) --
// and refuses to guess: no recognized marker, more than one distinct
// toolchain, or an unparseable version each answer ("", a detail naming why),
// exactly as if nothing were configured. checkCommand's caller must still
// fail closed on that empty result; this function only ever narrows when a
// project's toolchain is genuinely unambiguous, never substitutes a default.
//
// Detected image and evidence are shown together on the check finding, so an
// image mismatch (the ICP journey's Node 24 sandbox against a Node 26
// project) is visible in the same place the check result is, not silently
// wrong.
func detectToolchainImage(root string) (image, evidence string) {
	var found []string // "image (source)", one per recognized, unambiguous toolchain
	if img, src, ok := detectNodeToolchain(root); ok {
		found = append(found, fmt.Sprintf("%s (%s)", img, src))
	}
	if img, src, ok := detectGoToolchain(root); ok {
		found = append(found, fmt.Sprintf("%s (%s)", img, src))
	}
	if img, src, ok := detectPythonToolchain(root); ok {
		found = append(found, fmt.Sprintf("%s (%s)", img, src))
	}
	switch len(found) {
	case 0:
		return "", "no toolchain marker recognized (.nvmrc, package.json engines.node, go.mod, .python-version)"
	case 1:
		img, _, _ := strings.Cut(found[0], " (")
		return img, found[0]
	default:
		return "", "ambiguous toolchain: more than one recognized: " + strings.Join(found, ", ")
	}
}

// detectNodeToolchain reads .nvmrc first (the closer-to-the-metal marker of
// what a developer actually runs), falling back to package.json's
// engines.node. Either alone is enough; this does not cross-check them
// against each other when both are present.
func detectNodeToolchain(root string) (image, source string, ok bool) {
	if data, err := os.ReadFile(filepath.Join(root, ".nvmrc")); err == nil {
		if major, ok := parseLeadingMajorVersion(string(data)); ok {
			return "node:" + major, ".nvmrc", true
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return "", "", false
	}
	var pkg struct {
		Engines struct {
			Node string `json:"node"`
		} `json:"engines"`
	}
	if json.Unmarshal(data, &pkg) != nil || pkg.Engines.Node == "" {
		return "", "", false
	}
	if major, ok := parseLeadingMajorVersion(pkg.Engines.Node); ok {
		return "node:" + major, "package.json engines.node", true
	}
	return "", "", false
}

// nodeVersionSpec matches an unambiguous Node version specifier (review M6):
// an optional "v"/"V" prefix, 1-3 dot-separated numeric components, and an
// optional trailing ".x" wildcard component -- "18", "v18", "18.2", "18.2.0",
// "18.x", "18.2.x". The ENTIRE field must match (anchored both ends), so
// anything else fails closed exactly like an unrecognized marker, never
// guessing at the intended major:
//
//   - a comparison range: ">=24", "<21";
//   - an OR'd set of ranges: "^18||^20";
//   - a caret/tilde semver range: "^18.2.0", "~18.2.0" (both permit more than
//     the single version they name, so neither is "unambiguous");
//   - a non-numeric tag: "lts/*", "*", "latest".
func parseLeadingMajorVersion(spec string) (string, bool) {
	fields := strings.Fields(spec)
	if len(fields) != 1 {
		return "", false
	}
	m := nodeVersionSpec.FindStringSubmatch(fields[0])
	if m == nil {
		return "", false
	}
	return m[1], true
}

var nodeVersionSpec = regexp.MustCompile(`(?i)^v?([0-9]+)(?:\.[0-9]+){0,2}(?:\.x)?$`)

// goDirective matches a go.mod `go` directive's version, e.g. "go 1.24" or
// "go 1.24.0" (the latter form is also a toolchain-requirement version since
// Go 1.21, but the tag is used verbatim either way).
var goDirective = regexp.MustCompile(`^go\s+([0-9]+\.[0-9]+(?:\.[0-9]+)?)\s*$`)

// toolchainDirective matches a go.mod `toolchain` directive's version, e.g.
// "toolchain go1.24.1" (introduced in Go 1.21). Preferred over the `go`
// directive when both are present (review M6): `go` names only the MINIMUM
// version the module requires, while `toolchain` is what a `go` invocation
// actually downloads and builds with when it differs -- a module can declare
// `go 1.21` while `toolchain go1.24.1` is what it really runs on, and the
// check sandbox should match the latter.
var toolchainDirective = regexp.MustCompile(`^toolchain\s+go([0-9]+\.[0-9]+(?:\.[0-9]+)?)\s*$`)

func detectGoToolchain(root string) (image, source string, ok bool) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", "", false
	}
	var goVersion, toolchainVersion string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if m := toolchainDirective.FindStringSubmatch(line); m != nil {
			toolchainVersion = m[1]
			continue
		}
		if m := goDirective.FindStringSubmatch(line); m != nil {
			goVersion = m[1]
		}
	}
	if toolchainVersion != "" {
		return "golang:" + toolchainVersion, "go.mod toolchain", true
	}
	if goVersion != "" {
		return "golang:" + goVersion, "go.mod", true
	}
	return "", "", false
}

// pyVersion matches a bare version in .python-version ("3.12" or "3.12.1").
// pyenv's own file can carry a virtualenv-qualified name ("3.12/envs/foo");
// that shape does not match and is correctly left undetected rather than
// guessed at.
var pyVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+(?:\.[0-9]+)?$`)

func detectPythonToolchain(root string) (image, source string, ok bool) {
	data, err := os.ReadFile(filepath.Join(root, ".python-version"))
	if err != nil {
		return "", "", false
	}
	first, _, _ := strings.Cut(string(data), "\n")
	first = strings.TrimSpace(first)
	if pyVersion.MatchString(first) {
		return "python:" + first, ".python-version", true
	}
	return "", "", false
}
