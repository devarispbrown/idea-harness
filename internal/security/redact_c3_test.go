package security

import (
	"strings"
	"testing"
)

// TestReviewC3ReportedRealSecretsMissed reproduces every concrete miss the
// review measured against the pre-fix RedactString (which redacted only
// p.valid's matches -- the FINDING validity gate -- instead of every raw
// regex match): a real AWS key, Stripe key and Slack token that happened to
// contain a repeated-character run or low-entropy shape, plus generic
// assignment values that tripped containsPlaceholderWord's old substring bug
// ("test" inside "latest"/"contested", "xxx" inside a hex-looking value) or
// its run-length placeholder check ("!!!!" tail). Every one of these must be
// masked by RedactString/Redact, whether or not it also raises a finding.
//
// Mutation-check: revert RedactString to filter by p.valid (or p.redact to
// call p.validMatches) instead of redacting every raw match, and every case
// here fails.
func TestReviewC3ReportedRealSecretsMissed(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"aws_key_with_run", `awsKey := "AKIA0000QWERTYU2XNBV"`},
		{"stripe_key_with_run", `key := "sk_live_51H8xQ2KZ9vTpLmN0000"`},
		{"slack_token_numeric_run", `token := "xoxb-1111111111-4871263905-Xy7QpLmNv3RtYu9W"`},
		{"db_password_contains_test_substring", `DB_PASSWORD=latestProdPw9xK2`},
		{"password_contains_test_substring", `password: "contested-harbor-77"`},
		{"api_key_contains_xxx_substring", `api_key = "9f8xxx7d6c5b4a3210fedcba"`},
		{"password_trailing_punctuation_run", `password: "Passw0rd!!!!"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Redact(c.value)
			if got == c.value {
				t.Fatalf("Redact left the value completely unchanged (real secret passed through in the clear): %q", c.value)
			}
			if strings.Contains(got, "[REDACTED]") == false {
				t.Fatalf("Redact did not insert a redaction marker: got %q from %q", got, c.value)
			}
		})
	}
}

// TestRedactionUnconditionalEvenWhenFindingSuppressed pins C3's fix contract
// directly: RedactString must mask a match even in the cases where
// FindCredential/FindPatternsInFile correctly stays quiet (a genuine
// placeholder). Redaction and finding-generation are two different
// questions with two different gates (review C3): the placeholder gate only
// ever controls whether a REVIEWER sees a finding, never whether a value is
// masked before persistence.
func TestRedactionUnconditionalEvenWhenFindingSuppressed(t *testing.T) {
	cases := []struct {
		name, value string
	}{
		// Generic assignment: an obvious placeholder that legitimately stays
		// quiet for findings (placeholder vocabulary / low entropy) -- but
		// must still be redacted.
		{"placeholder_word", `password: "changeme_changeme"`},
		{"low_entropy_alternation", `token: "abababababababab"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if FindCredential(c.value) != "" {
				t.Fatalf("test setup: %q unexpectedly raised a finding (no longer a suppressed-placeholder case)", c.value)
			}
			if got := Redact(c.value); got == c.value {
				t.Fatalf("a placeholder value quiet for findings was NOT redacted: %q", c.value)
			}
		})
	}
}

// TestEveryCatalogKindRedactsARealisticSecret is the review's own required
// coverage: a table test with a realistic secret for every catalog kind,
// proving RedactString masks it. Includes a placeholder-shaped variant for
// every PROVIDER-PREFIXED kind (review C3 item 2): the vendor prefix is
// itself the evidence a match is real, so these kinds no longer have any
// placeholder/shape gate at all, and now raise a finding too -- but even
// before that fix, redaction must never have depended on it.
func TestEveryCatalogKindRedactsARealisticSecret(t *testing.T) {
	cases := []struct {
		kind, value string
	}{
		{"PEM private key", "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA\n-----END RSA PRIVATE KEY-----"},
		{"JWT", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dQw4w9WgXcQjSKgYVw_Adg6zB1oXHyKFtq9OQ"},
		{"AWS access key ID (realistic)", "AKIAIOSFODNN7EXAMPLE"},
		{"AWS access key ID (placeholder shape)", "AKIA0000000000000000"},
		{"GitHub personal access token", "ghp_aB3dE9fGhJ2kL5mN8pQrS1tUvWxYz0"},
		{"GitHub fine-grained personal access token", "github_pat_aB3dE9fGhJ2kL5mN8pQrS1tUvWxYz01234567890"},
		{"GitHub token", "gho_aB3dE9fGhJ2kL5mN8pQrS1tUvWxYz0"},
		{"GitLab personal access token", "glpat-aB3dE9fGhJ2kL5mN8pQrS1"},
		{"Google API key", "AIzaSyD-canary-google-key-1234567890abcd"},
		{"npm access token", "npm_aB3dE9fGhJ2kL5mN8pQrS1tUvWxYz012345"},
		{"OpenAI/Anthropic API key", "sk-ant-api03-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789ABCDEF"},
		{"OpenAI API key (legacy)", "sk-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789ABCDEFGHIJKL"},
		{"Stripe secret key (realistic)", "sk_live_4eC39HqLyjWDarjtT1zdp7dc"},
		{"Stripe secret key (placeholder shape)", "sk_live_00000000000000000000"},
		{"Slack token (realistic)", "xoxb-123456789012-123456789012-abcdefghijklmnop"},
		{"Slack token (numeric-run placeholder shape)", "xoxb-1111111111-1111111111-Xy7QpLmNv3RtYu9W"},
		{"bearer token", "Bearer aB3dE9fGhJ2kL5mN8pQrS1tUvWxYz0123456"},
		{"URL userinfo credential", "postgres://admin:hunter23456789@db.example.com/prod"},
		{"AWS secret access key", `AWS_SECRET_ACCESS_KEY=AbCdEfGhIjKlMnOpQrStUvWxYz0123456789+/AB`},
		{"credential assignment", `password: "k3jQ9zPmT7xR2wLnB6"`},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			if got := Redact(c.value); got == c.value {
				t.Fatalf("kind %q: value not redacted: %q", c.kind, c.value)
			}
		})
	}
}
