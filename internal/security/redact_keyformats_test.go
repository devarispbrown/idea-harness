package security

import (
	"strings"
	"testing"
)

// TestModernProviderKeyFormatsAreRedacted pins each provider key shape by name.
//
// The catalog's sk- pattern required 20+ contiguous alphanumerics right after
// "sk-", so it matched only the legacy flat OpenAI key. Every current format
// puts a hyphen in the first few characters -- sk-ant-api03-…, sk-proj-…,
// sk-svcacct-… -- and all of them passed through unredacted. Anthropic is a
// first-class provider here, and its key is the credential forwarded into the
// sandboxed agent container, which has unrestricted network egress.
//
// Named cases rather than one regex assertion: a future catalog change that
// silently stops matching one vendor should name that vendor when it fails.
func TestModernProviderKeyFormatsAreRedacted(t *testing.T) {
	r := NewRedactor()
	for _, tc := range []struct{ name, key string }{
		{"anthropic", "sk-ant-api03-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789ABCDEF"},
		{"openai_project", "sk-proj-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789ABCDEF"},
		{"openai_service_account", "sk-svcacct-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789ABCDE"},
		{"openai_legacy", "sk-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789ABCDEFGHIJKL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := "here is the key " + tc.key + " in some prose"
			if got := r.RedactString(text); strings.Contains(got, tc.key) {
				t.Errorf("%s key survived redaction:\n%s", tc.name, got)
			}
			if !r.ContainsCredential(text) {
				t.Errorf("ContainsCredential did not flag a %s key", tc.name)
			}
		})
	}
}

// TestShortSkStringIsNotACredential keeps the widened pattern from flagging
// ordinary prose that happens to start "sk-".
func TestShortSkStringIsNotACredential(t *testing.T) {
	r := NewRedactor()
	if r.ContainsCredential("the sk-learn library") {
		t.Error("widened sk- pattern now flags ordinary prose")
	}
}

// TestOrdinaryProseIsNotACredential guards the other direction.
//
// The first widening of the sk- pattern admitted "-" anywhere, so any 20+
// character hyphenated string beginning "sk-" matched. That redacted ordinary
// URL slugs, and because ContainsCredential shares the pattern, it rejected an
// entire context-doc upload that merely mentioned one.
func TestOrdinaryProseIsNotACredential(t *testing.T) {
	r := NewRedactor()
	for _, s := range []string{
		"see https://example.com/blog/sk-decomposition-of-matrices-and-linear-algebra",
		"the sk-learn-cheatsheet-for-classifiers-and-regressors-2024 is popular",
		"sk-means-clustering-explained-for-beginners-in-python",
	} {
		if r.ContainsCredential(s) {
			t.Errorf("ordinary prose flagged as a credential: %s", s)
		}
		if got := r.RedactString(s); got != s {
			t.Errorf("ordinary prose redacted:\n  in:  %s\n  out: %s", s, got)
		}
	}
}
