package security

import (
	"strings"
	"testing"
)

// The Slack and Linear integrations read three credentials from the
// environment. The bot token was already covered by the xox pattern; the
// Socket Mode app-level token (xapp-) and the Linear API key (lin_api_) were
// not, so a failure message quoting either would have reached a blob, a log
// line or a Slack post in the clear.
func TestIntegrationTokensAreRedacted(t *testing.T) {
	r := NewRedactor()
	for _, tc := range []struct{ name, key string }{
		{"slack_bot", "xoxb-1234567890123-1234567890123-AbCdEfGhIjKlMnOpQrStUvWx"},
		{"slack_app", "xapp-1-A0123BCDE45-1234567890123-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		{"linear", "lin_api_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789abcd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := "auth failed for " + tc.key + " (revoked?)"
			if got := r.RedactString(text); strings.Contains(got, tc.key) {
				t.Errorf("%s survived redaction: %s", tc.name, got)
			}
			if !r.ContainsCredential(text) {
				t.Errorf("ContainsCredential missed a %s token", tc.name)
			}
		})
	}
	// Prose about the env vars is not a credential.
	for _, s := range []string{"set SLACK_APP_TOKEN to your xapp- token", "lin_api_ keys live in LINEAR_API_KEY"} {
		if r.ContainsCredential(s) {
			t.Errorf("false positive on %q", s)
		}
	}
}
