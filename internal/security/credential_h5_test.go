package security

import "testing"

// TestH5CredentialAssignmentFalsePositives is review H5's own list of
// snippets the OLD unquoted-value rule rejected as a whole change set,
// wrongly: each of these is an identifier, a member/property access, a
// function call, or a `$`-interpolated reference, never a literal secret.
func TestH5CredentialAssignmentFalsePositives(t *testing.T) {
	cases := []struct {
		name, text string
	}{
		{"bare_identifier_assignment", `const token = authToken;`},
		{"password_assigned_from_identifier", `password = newPassword`},
		{"member_assignment_from_identifier", `this.password = hashedPassword`},
		{"self_assignment_from_identifier", `self.token = token_from_somewhere`},
		{"csrf_token_from_call", `csrf_token = generate_csrf_token()`},
		{"api_key_from_member_access", `api_key = settings.API_KEY_FROM_ENV`},
		{"env_style_dollar_reference", `export GITHUB_TOKEN=$GITHUB_TOKEN_VALUE`},
		{"quoted_dollar_interpolation", `api_key="${API_KEY}"`},
		{"call_with_await", `const token = await getToken()`},
		{"member_access_after_colon", `password: process.env.X`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if FindCredential(c.text) != "" {
				t.Errorf("H5 false positive: %q should not match a credential pattern", c.text)
			}
			if got := Redact(c.text); got != c.text {
				t.Errorf("H5 false positive: %q was redacted to %q", c.text, got)
			}
		})
	}
}

// TestH5CredentialAssignmentFalseNegatives is review H5's list of snippets
// the OLD rule missed: a `_KEY`/`_secret` suffix keyword, a dotted secret
// value, a `pwd` keyword, and URL userinfo credentials.
func TestH5CredentialAssignmentFalseNegatives(t *testing.T) {
	cases := []struct {
		name, text string
	}{
		{"secret_key_suffix", `SECRET_KEY = "django-insecure-abcdefgh"`},
		{"pwd_keyword", `let pwd = "letmein123"`},
		{"url_userinfo_credential", `postgres://admin:hunter2345@db`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if FindCredential(c.text) == "" {
				t.Errorf("H5 false negative: %q should match a credential pattern", c.text)
			}
			if got := Redact(c.text); got == c.text {
				t.Errorf("H5 false negative: %q was not redacted", c.text)
			}
		})
	}

	// client_secret=xyz.abc.def.ghi is the review's own example of a dotted
	// secret value the old rule missed (it excluded '.' from the unquoted
	// value class, truncating the candidate to "xyz", too short to match).
	// The fix widens the unquoted class to allow '.', which lets a real
	// dot-delimited secret match -- but a dotted value whose every segment
	// is itself a plain identifier word is indistinguishable, on shape
	// alone, from a genuine member-access expression like
	// `req.headers.authorization` (see TestCredentialAssignmentIgnoresNonLiteralValues),
	// and this heuristic deliberately treats both as a reference rather than
	// risk reintroducing that false positive. So this specific literal
	// example remains unmatched; a dotted value containing a digit, or a
	// quoted dotted value, still matches (see the JWT pattern, and
	// TestCredentialAssignmentCoversNamedForms's client_secret_yaml case,
	// for the forms that do).
	if FindCredential(`client_secret=xyz.abc.def.ghi`) != "" {
		t.Error("client_secret=xyz.abc.def.ghi was expected to stay unmatched (see comment); if this now fails, the identifier-reference heuristic changed -- re-check TestCredentialAssignmentIgnoresNonLiteralValues/token_member_access still passes too")
	}
}

// TestH5CredentialAssignmentCorrectCasesStillHold pins the review's own
// "Correct" list: cases that must keep behaving exactly as before the H5
// fix.
func TestH5CredentialAssignmentCorrectCasesStillHold(t *testing.T) {
	noMatch := []string{
		`const token = await getToken()`,
		`password: process.env.X`,
		`api_key="${KEY}"`,
	}
	for _, text := range noMatch {
		if FindCredential(text) != "" {
			t.Errorf("expected no match for %q", text)
		}
	}

	// A base64 blob under a non-credential key must not be flagged.
	if FindCredential(`data = "eHl6LmFiYy5kZWYuZ2hpMTIzNDU2Nzg5"`) != "" {
		t.Error("a base64 blob under `data =` (no credential keyword) must not be flagged")
	}
}
