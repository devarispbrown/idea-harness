package main

// Single-sourcing for load-bearing security claims.
//
// Four audits have found the same defect: a security property stated in several
// documents, corrected in some of them, left false in the rest. Audit 3 fixed
// security.md and security-model.md and left configuration.md promising
// redaction of logs and blobs that does not happen. Audit 4 found the
// "independent reviewer" claim in six sentences across four files.
//
// No check can decide whether a claim is TRUE -- that is what the reviews are
// for. This enforces the weaker property that makes truth maintainable: each
// listed property is stated in exactly one place, and every other document that
// raises the subject links there instead of restating it. That turns "four
// documents drifted apart" into "one document to check".
//
// The design deliberately keeps the judgement out of the checker. An earlier
// version scanned prose for sentences "asserting a security property", which
// needed natural-language classification, was majority-noise on words like
// "token", and -- worse -- would have been tuned by the same author, in the same
// round, as the documents it judges. Here the canonical file marks its own
// claim with an anchor comment, and the check is a link lookup.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// securityClaim is one property that must be stated once.
type securityClaim struct {
	// anchor is the marker in the canonical file: <!-- claim:<anchor> -->.
	anchor string
	// canonical is the file allowed to state it.
	canonical string
	// keywords are phrases, not bare words. "token" alone is majority-noise --
	// most uses in these docs are review.max_tokens or "command token".
	keywords []string
}

var securityClaims = []securityClaim{
	{
		anchor:    "executor-isolation",
		canonical: "docs/security-model.md",
		keywords:  []string{"--network none", "network egress", "cap-drop", "read-only root", "sandboxed agent"},
	},
	{
		anchor:    "redaction",
		canonical: "docs/security-model.md",
		keywords:  []string{"redacted before", "redaction-free", "before any persistence", "redacted at write"},
	},
	{
		anchor:    "reviewer-separation",
		canonical: "docs/security.md",
		keywords:  []string{"distinct actor", "independent reviewer", "independently reviewed", "independent adversarial"},
	},
}

// publishedDocs are the files a reader is expected to trust. docs/plans/ is
// excluded: those are working records, and rewriting history to satisfy a lint
// would defeat the point of keeping them.
func publishedDocs(root string) ([]string, error) {
	var out []string
	entries, err := os.ReadDir(filepath.Join(root, "docs"))
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			out = append(out, filepath.Join("docs", e.Name()))
		}
	}
	out = append(out, "README.md")
	sort.Strings(out)
	return out, nil
}

func runClaimsCheck() error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	docs, err := publishedDocs(root)
	if err != nil {
		return err
	}

	var problems []string
	for _, claim := range securityClaims {
		anchor := "<!-- claim:" + claim.anchor + " -->"
		canonicalBody, err := os.ReadFile(filepath.Join(root, claim.canonical))
		if err != nil {
			return err
		}
		if !strings.Contains(string(canonicalBody), anchor) {
			problems = append(problems, fmt.Sprintf(
				"  %s is the canonical home of %q but has no %s marker",
				claim.canonical, claim.anchor, anchor))
			continue
		}
		// A link to the canonical file is enough: anchors are for humans, and
		// requiring the fragment would fail on a heading rename.
		linkRe := regexp.MustCompile(regexp.QuoteMeta(filepath.Base(claim.canonical)))
		for _, doc := range docs {
			if doc == claim.canonical {
				continue
			}
			body, err := os.ReadFile(filepath.Join(root, doc))
			if err != nil {
				return err
			}
			text := string(body)
			var hit string
			for _, kw := range claim.keywords {
				if strings.Contains(strings.ToLower(text), strings.ToLower(kw)) {
					hit = kw
					break
				}
			}
			if hit == "" || linkRe.MatchString(text) {
				continue
			}
			problems = append(problems, fmt.Sprintf(
				"  %s states %q (%q) without linking to %s",
				doc, claim.anchor, hit, claim.canonical))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("security claims stated in more than one place:\n%s\n\n"+
		"Each of these properties has one canonical statement. A second copy is how\n"+
		"three audits' worth of documentation P0s happened: the canonical file gets\n"+
		"corrected and the copies do not. Link instead of restating.\n\n"+
		"This proves a property is single-sourced. It cannot tell whether it is true.",
		strings.Join(problems, "\n"))
}
