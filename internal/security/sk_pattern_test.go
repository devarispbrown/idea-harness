package security

import "testing"

// TestSKPatternCoversModernKeysWithoutEatingProse pins a pattern that has now
// been wrong in both directions.
//
// It first required 20+ contiguous alphanumerics right after "sk-", so it
// matched only the legacy flat OpenAI key and passed every modern one. The fix
// for that constrained the body to alphanumerics after up to three hyphen
// groups — but provider key bodies are base64url, so a "_" or "-" in the first
// 16 body characters broke the match. Measured over random base64url bodies,
// that missed 27–30% of every modern shape: a coin flip on the credential
// forwarded into the agent container.
//
// The over-wide intermediate version matched any hyphenated lowercase string
// starting "sk-", so a document containing an ordinary URL slug was rejected
// wholesale on upload. Both directions are asserted here.
func TestSKPatternCoversModernKeysWithoutEatingProse(t *testing.T) {
	red := NewRedactor()

	secrets := []struct{ name, key string }{
		// Underscores and hyphens deliberately placed early in the body — the
		// exact position that defeated the previous pattern.
		{"anthropic", "sk-ant-api03-_a-Bc" + "dEfGhIjKlMnOpQrStUvWxYz0123456789AbCdEfGhIjKlMnOpQrStUvWxYz012345678901234567890AA"},
		{"anthropic plain", "sk-ant-api03-R2d2C3poAbCdEfGhIjKlMnOpQrStUvWxYz0123456789AbCdEfGhIjKlMnOpQrStUvWxYz01234567890"},
		{"openai project", "sk-proj-_-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789"},
		{"openai service account", "sk-svcacct-__AbCdEfGhIjKlMnOpQrStUvWxYz0123456789"},
		{"openai legacy flat", "sk-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789AbCdEfGh"},
		{"unknown vendor, mixed case body", "sk-zz_9-QrStUvWxYz0123456789abcdef"},
	}
	for _, c := range secrets {
		t.Run("redacts/"+c.name, func(t *testing.T) {
			if !red.ContainsCredential(c.key) {
				t.Errorf("ContainsCredential(%s) = false; the key passes every choke point in the clear", c.key)
			}
			if got := red.RedactString("key is " + c.key + " ok"); got == "key is "+c.key+" ok" {
				t.Errorf("RedactString left the key intact: %s", got)
			}
		})
	}

	prose := []struct{ name, text string }{
		{"a URL slug", "see https://example.com/sk-decomposition-of-matrices-and-linear-algebra for details"},
		{"a slug with digits", "the sk-lesson-3-linear-algebra-part-2-notes chapter"},
		{"a hyphenated identifier", "sk-this-is-an-ordinary-lowercase-identifier-here"},
		{"a short token", "sk-abc"},
		{"Slovak locale code in a path", "content/sk-sk/index.md"},
	}
	for _, c := range prose {
		t.Run("leaves/"+c.name, func(t *testing.T) {
			if red.ContainsCredential(c.text) {
				t.Errorf("ContainsCredential(%q) = true; a document containing this is rejected on upload", c.text)
			}
			if got := red.RedactString(c.text); got != c.text {
				t.Errorf("RedactString rewrote ordinary prose:\n  in:  %s\n  out: %s", c.text, got)
			}
		})
	}
}
