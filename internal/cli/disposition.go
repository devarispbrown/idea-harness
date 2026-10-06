package cli

import (
	"fmt"
	"io"
	"strings"
)

// ---------- dispositioning a review finding ----------
//
// POST .../findings/{id}/disposition (handleFindingDisposition) had no CLI
// caller at all (audit 6 / D1, docs/plans/deferred.md): the only route out of
// a blocking finding was the web UI or raw HTTP. Worse, the daemon's own
// guidance told a user the wrong thing -- secretSweepGuidanceDetail
// (handlers.go) said `idea reviews lets you disposition it "not a secret"`,
// and `idea reviews` only ever listed findings. A user hitting a secret
// denial was told to use a capability that did not exist.

// validDispositions mirrors domain.Disposition's full enum. Mirrored here,
// not imported, the same way taskPriorities/reopenPhases are: refusing a
// typo before the round trip needs the list, not the type.
var validDispositions = []string{
	"fixed", "accepted_risk", "not_applicable", "duplicate", "deferred", "rejected", "mitigate", "not_a_secret",
}

// resolveFindingRef resolves a finding reference -- a full id or an
// unambiguous prefix of one (findings, like questions, have no human code,
// only a content-hash id) -- against one initiative's findings. Mirrors
// resolveQuestionRef (judgment.go).
func resolveFindingRef(c *client, projectID, iniID, ref string) (*cliFinding, error) {
	var findings []cliFinding
	if err := c.do("GET", initiativeResource(projectID, iniID, "findings"), nil, &findings); err != nil {
		return nil, err
	}
	for i := range findings {
		if findings[i].ID == ref {
			return &findings[i], nil
		}
	}
	var matches []int
	for i := range findings {
		if strings.HasPrefix(findings[i].ID, ref) {
			matches = append(matches, i)
		}
	}
	switch len(matches) {
	case 1:
		return &findings[matches[0]], nil
	case 0:
		return nil, fmt.Errorf("no finding matching %q; `idea reviews` lists them", ref)
	default:
		return nil, fmt.Errorf("%q matches more than one finding; use more of the id shown by `idea reviews`", ref)
	}
}

// cmdDisposition dispositions a review finding, and optionally records a
// free-text note alongside it (handleFindingNote, the "discuss" route) in the
// same command -- a disposition with no explanation is often half the answer.
//
// not_a_secret is pre-validated client-side once the finding is resolved
// locally: refused on anything but a secret_sweep finding, and refused again,
// unconditionally, when the match was drawn from the project's own configured
// secrets rather than a pattern (SecretOverridable == false) -- that one is a
// real credential by construction, and no disposition makes it eligible. Both
// mirror SetFindingDisposition's own refusals (internal/store/repo.go) so the
// CLI says the same thing before the round trip that the server would say
// after it.
func cmdDisposition(a cmdArgs, stdout, stderr io.Writer) error {
	if len(a.positional) < 2 {
		return fmt.Errorf(`usage: idea disposition <finding> <%s> [--note "<text>"] [<initiative>]`,
			strings.Join(validDispositions, "|"))
	}
	ref, disp := a.positional[0], a.positional[1]
	if !contains(validDispositions, disp) {
		return fmt.Errorf("unknown disposition %q; use one of: %s", disp, strings.Join(validDispositions, ", "))
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, a.positional[2:])
	if err != nil {
		return err
	}
	finding, err := resolveFindingRef(c, projectID, iniID, ref)
	if err != nil {
		return err
	}
	if disp == "not_a_secret" {
		if finding.Category != "secret_sweep" {
			return fmt.Errorf("not_a_secret is only valid on a secret_sweep finding (this finding is %q)", finding.Category)
		}
		if !finding.SecretOverridable {
			return fmt.Errorf("not_a_secret is refused for %q: this value was drawn from the project's own configured "+
				"secrets, not a pattern match, and is never eligible for the not_a_secret override", finding.SecretKind)
		}
	}
	var out map[string]string
	if err := c.do("POST", findingResource(projectID, finding.ID, "disposition"),
		map[string]string{"disposition": disp}, &out); err != nil {
		return err
	}
	fmt.Fprintln(stdout, ok("✓ dispositioned")+" "+code(shortRef(finding.ID))+" "+muted(disp))
	// The server's own warning, printed rather than swallowed: `rejected`
	// resolves a reviewer's judgment call, but a finding carrying a system
	// deny code still blocks afterward ("system deny beats human approve").
	if w := out["warning"]; w != "" {
		fmt.Fprintln(stdout, warn("⚠ ")+w)
	}
	if note := a.value("--note"); note != "" {
		if err := c.do("POST", findingResource(projectID, finding.ID, "note"),
			map[string]string{"note": note}, nil); err != nil {
			return fmt.Errorf("dispositioned, but the note failed: %w", err)
		}
		fmt.Fprintln(stdout, muted("  note recorded"))
	}
	return nil
}
