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
		// Remote use of the UI: the listen policy, the token never reaching a
		// remote browser, the session cookie and its CSRF defence. Phrases a
		// restatement would have to use.
		anchor:    "remote-access",
		canonical: "docs/security-model.md",
		// "pairing code" is deliberately absent: the generated CLI table and
		// the parity ledger name the verb's purpose, not the property.
		keywords:  []string{"session cookie", "x-idea-csrf", "daemon.listen", "allowed_hosts"},
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
		// A link to the canonical file is enough — anchors are for humans, and
		// requiring the fragment would fail on a heading rename — but it has to
		// be *near the claim*, not anywhere in the file.
		//
		// The first version matched the basename against the whole document, so
		// one mention of security-model.md in an unrelated see-also line
		// exempted that file from every claim anchored there, permanently. Three
		// of the ten published docs were already in that state, including
		// configuration.md — the file this guard was written for. Re-adding the
		// audit-4 P0 sentence to it passed.
		link := filepath.Base(claim.canonical)
		for _, doc := range docs {
			if doc == claim.canonical {
				continue
			}
			body, err := os.ReadFile(filepath.Join(root, doc))
			if err != nil {
				return err
			}
			blocks := paragraphs(string(body))
			for bi, block := range blocks {
				lower := strings.ToLower(block.text)
				var hit string
				for _, kw := range claim.keywords {
					if strings.Contains(lower, strings.ToLower(kw)) {
						hit = kw
						break
					}
				}
				if hit == "" {
					continue
				}
				// A list or table is one statement with the sentence that
				// introduces it, so the link may sit in either. Without this the
				// guard demands a link inside a bullet, which authors would
				// route around rather than obey.
				scope := block.text
				if bi > 0 && continuesPrevious(block.text) {
					scope = blocks[bi-1].text + scope
				}
				if strings.Contains(scope, link) {
					continue
				}
				problems = append(problems, fmt.Sprintf(
					"  %s:%d states %q (%q) without linking to %s",
					doc, block.line, claim.anchor, hit, claim.canonical))
			}
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

// block is one paragraph of a Markdown file, with the line it starts on.
type block struct {
	line int
	text string
}

// paragraphs splits on blank lines. The unit matters: the link that exempts a
// restatement has to sit with the restatement, so that a reader who finds the
// sentence also finds the pointer to the canonical wording. A whole-file test
// exempts sentences nobody linked.
func paragraphs(text string) []block {
	var out []block
	cur := block{line: 1}
	var b strings.Builder
	line := 1
	flush := func() {
		if strings.TrimSpace(b.String()) != "" {
			cur.text = b.String()
			out = append(out, cur)
		}
		b.Reset()
	}
	for _, l := range strings.Split(text, "\n") {
		if strings.TrimSpace(l) == "" {
			flush()
			cur = block{line: line + 1}
		} else {
			b.WriteString(l)
			b.WriteString("\n")
		}
		line++
	}
	flush()
	return out
}

// continuesPrevious reports whether a block is a list or table that belongs to
// the paragraph before it.
func continuesPrevious(text string) bool {
	first := text
	if i := strings.IndexByte(first, '\n'); i >= 0 {
		first = first[:i]
	}
	first = strings.TrimSpace(first)
	return strings.HasPrefix(first, "- ") || strings.HasPrefix(first, "* ") ||
		strings.HasPrefix(first, "| ") || strings.HasPrefix(first, "1. ")
}
