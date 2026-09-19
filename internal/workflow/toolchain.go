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

// parseLeadingMajorVersion extracts a single unambiguous leading major
// version number from a version specifier, refusing anything that looks like
// a range (">=18 <21", two space-separated clauses) or a non-numeric tag
// ("lts/*", "*", "latest"): C5 must fail closed on anything but a single
// clear version, never guess at a range's intended major.
func parseLeadingMajorVersion(spec string) (string, bool) {
	fields := strings.Fields(spec)
	if len(fields) != 1 {
		return "", false
	}
	tok := strings.TrimLeft(fields[0], "^~=<>vV ")
	i := 0
	for i < len(tok) && tok[i] >= '0' && tok[i] <= '9' {
		i++
	}
	if i == 0 {
		return "", false
	}
	return tok[:i], true
}

// goDirective matches a go.mod `go` directive's version, e.g. "go 1.24" or
// "go 1.24.0" (the latter form is also a toolchain-requirement version since
// Go 1.21, but the tag is used verbatim either way).
var goDirective = regexp.MustCompile(`^go\s+([0-9]+\.[0-9]+(?:\.[0-9]+)?)\s*$`)

func detectGoToolchain(root string) (image, source string, ok bool) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if m := goDirective.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			return "golang:" + m[1], "go.mod", true
		}
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
