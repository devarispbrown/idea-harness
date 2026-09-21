package security

import (
	"strings"
	"testing"
)

// TestCredentialAssignmentCoversNamedForms is review M3: the generic
// credential-assignment rule must match a JSON key with a quote before the
// colon, SCREAMING_SNAKE_CASE env-var assignments, and a snake_case YAML/JS
// key -- none of which the old `\b(?:...)…` pattern matched, since `_` is a
// word character and `\b` never fires between it and the keyword.
func TestCredentialAssignmentCoversNamedForms(t *testing.T) {
	cases := []struct {
		name, text string
	}{
		{"json_quoted_key", `{"password": "hunter2345"}`},
		{"db_password_env", `DB_PASSWORD=supersecret123`},
		{"aws_secret_access_key_env", `AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY`},
		{"client_secret_yaml", `client_secret: oauth-client-secret-value-1`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if FindCredential(c.text) == "" {
				t.Errorf("credential not detected in %q", c.text)
			}
			matches := FindCredentialsInFile("f", c.text)
			if len(matches) == 0 {
				t.Fatalf("FindCredentialsInFile found nothing in %q", c.text)
			}
			for _, m := range matches {
				if strings.Contains(m.Fingerprint, "hunter2345") {
					t.Errorf("fingerprint should never be the raw value")
				}
			}
			if got := Redact(c.text); got == c.text {
				t.Errorf("Redact left %q unchanged", c.text)
			}
		})
	}
}

// TestCredentialAssignmentIgnoresNonLiteralValues is review M3's other half:
// an empty quoted value, and an unquoted value that looks like an identifier
// or a function call rather than a literal secret, must not match.
func TestCredentialAssignmentIgnoresNonLiteralValues(t *testing.T) {
	cases := []struct {
		name, text string
	}{
		{"empty_quoted_password", `password: ""`},
		{"token_from_getenv_call", `token = getenv("TOKEN")`},
		{"token_member_access", `const token = req.headers.authorization`},
		{"prose_mentioning_token", `the access token is refreshed automatically by the client library`},
		{"prose_mentioning_secret", `there is no secret here, just a long sentence about keeping things confidential`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if FindCredential(c.text) != "" {
				t.Errorf("false positive: %q matched as a credential", c.text)
			}
			if got := Redact(c.text); got != c.text {
				t.Errorf("false positive: %q was redacted to %q", c.text, got)
			}
		})
	}
}
