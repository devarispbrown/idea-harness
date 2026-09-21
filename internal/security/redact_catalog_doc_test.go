package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSecurityDocListsTheFullCredentialCatalog pins review LOW #13:
// docs/security.md's prose listed roughly a third of credentialCatalog's
// real entries (PEM keys, JWTs, AWS AKIA, GitHub ghp_, OpenAI sk-, and a
// generic key=/token= pattern) and silently dropped GitHub's other token
// kinds, GitLab, Google, npm, Stripe, Slack, bearer tokens, URL userinfo
// credentials (DSNs), and AWS secret access keys -- a reader trusting that
// list to decide what NOT to hand-roll their own redaction for would have
// been wrong about more than half the real catalog.
//
// This walks credentialCatalog itself (not a hardcoded copy of it), so a
// future pattern added to redact.go without a docs/security.md update fails
// here rather than silently reintroducing the gap.
func TestSecurityDocListsTheFullCredentialCatalog(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "security.md"))
	if err != nil {
		t.Fatal(err)
	}
	// Collapse whitespace (including markdown's own line-wrapping, which
	// otherwise splits a multi-word kind name like "GitHub fine-grained
	// personal access token" across a line break) so this test checks the
	// prose's actual words, not its wrap column.
	docLower := strings.Join(strings.Fields(strings.ToLower(string(doc))), " ")

	seen := map[string]bool{}
	for _, p := range credentialCatalog {
		if seen[p.kind] {
			continue
		}
		seen[p.kind] = true
		if !strings.Contains(docLower, strings.ToLower(p.kind)) {
			t.Errorf("docs/security.md does not mention credentialCatalog's %q pattern", p.kind)
		}
	}
	if len(seen) < 10 {
		t.Fatalf("only found %d distinct catalog kinds; the fixture and credentialCatalog have drifted", len(seen))
	}
}
