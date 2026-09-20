package security

import "testing"

// TestAWSSecretAccessKeyJSONForm is ICP re-run defect #4: a real founder-
// journey run had config/deploy.json with
//
//	"aws_access_key_id": "AKIA...",
//	"aws_secret_access_key": "<40-char high-entropy value>"
//
// The aws_access_key_id line fired (its pattern matches the VALUE's AKIA...
// shape, so JSON quoting around the key never mattered to it), but the
// aws_secret_access_key line next to it produced NO finding at all. The
// AWS_SECRET_ACCESS_KEY pattern matched the keyword name, not the value
// shape, and required `\s*[:=]` immediately after the keyword -- but a JSON
// key's own closing quote sits there instead ("aws_secret_access_key": ...),
// so the match never started. The generic "credential assignment" rule
// doesn't cover it either: it only recognizes "secret" with a bare, direct
// "_key"/"_value" suffix, not "secret_ACCESS_key".
func TestAWSSecretAccessKeyJSONForm(t *testing.T) {
	// A real-shaped, high-entropy 40-character AWS-secret-key-like value:
	// mixed case, digits, and base64 punctuation, no repeated run, no
	// placeholder word, not a sequential fill. Deliberately NOT AWS's own
	// published "...EXAMPLEKEY" docs literal (already covered by
	// TestCredentialAssignmentCoversNamedForms's env-form case), so this test
	// pins the realistic case a scanner must not treat as a placeholder.
	const secret = "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789+/AB"
	if len(secret) != 40 {
		t.Fatalf("test fixture secret is %d chars, want 40", len(secret))
	}

	cases := []struct {
		name, text string
	}{
		{
			"json_deploy_config",
			"{\n  \"aws_access_key_id\": \"AKIAIOSFODNN7EXAMPLE\",\n  \"aws_secret_access_key\": \"" + secret + "\"\n}",
		},
		{
			"json_no_surrounding_whitespace",
			`{"aws_secret_access_key":"` + secret + `"}`,
		},
		{
			"yaml_form_still_matches",
			"aws_secret_access_key: " + secret,
		},
		{
			"env_form_still_matches",
			"AWS_SECRET_ACCESS_KEY=" + secret,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if FindCredential(c.text) == "" {
				t.Fatalf("aws_secret_access_key not detected in %q", c.text)
			}
			matches := FindCredentialsInFile("config/deploy.json", c.text)
			var found bool
			for _, m := range matches {
				if m.Kind == "AWS secret access key" {
					found = true
					if m.Preview == "" {
						t.Error("finding has no masked preview")
					}
				}
			}
			if !found {
				t.Errorf("FindCredentialsInFile did not report an \"AWS secret access key\" finding for %q; got %+v", c.text, matches)
			}
			if got := Redact(c.text); got == c.text {
				t.Errorf("Redact left the secret in place:\n%s", got)
			}
		})
	}
}

// TestAWSSecretAccessKeyObviousPlaceholdersStillRaiseAFinding is review C3's
// revision of defect #4's other half. An earlier version of this catalog ran
// AWS_SECRET_ACCESS_KEY's value through validFlatTokenValue's
// placeholder-SHAPE gate (isPlaceholderShape: a long repeated-character run,
// or overall entropy too low to plausibly be generated secret data) and
// suppressed the finding entirely for an obvious placeholder body. Review C3
// found that gate dangerously unreliable when applied to a PROVIDER-PREFIXED
// format (AWS_SECRET_ACCESS_KEY's own env-var name is exactly that prefix):
// a Monte-Carlo run over 200k random well-formed AWS secret keys missed a
// REAL secret 1 in 3,200 times purely by chance shape. The vendor-named
// keyword is already the evidence a match is real, so the shape gate no
// longer applies to this pattern at all -- every value here now raises a
// finding (Overridable: a human can still disposition an obvious
// placeholder "not a secret"), and -- the part that must never regress --
// RedactString still masks it regardless.
func TestAWSSecretAccessKeyObviousPlaceholdersStillRaiseAFinding(t *testing.T) {
	cases := []struct {
		name, value string
	}{
		// hasLongRun: a run of one repeated character.
		{"all_x_run", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},
		{"all_zero_run", "00000000000000000000000000000000000000"},
		{"padding_equals_run", "================================"},
		// Low aggregate entropy: a two-character alternation carries only 1
		// bit/char, well under placeholderEntropyThreshold (2.5).
		{"alternating_low_entropy", "ABABABABABABABABABABABABABABABABABABAB"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text := `{"aws_secret_access_key": "` + c.value + `"}`
			if FindCredential(text) == "" {
				t.Errorf("obviously-placeholder-shaped value no longer raises ANY finding: %q", text)
			}
			var found bool
			for _, m := range FindCredentialsInFile("config/deploy.json", text) {
				if m.Kind == "AWS secret access key" {
					found = true
					if !m.Overridable {
						t.Errorf("finding for %q is not Overridable; a human must be able to dismiss an obvious placeholder", text)
					}
				}
			}
			if !found {
				t.Errorf("expected an AWS secret access key finding for %q", text)
			}
			// The part that must never regress (review C3): redaction is
			// unconditional regardless of the finding/placeholder gate.
			if got := Redact(text); got == text {
				t.Errorf("Redact left the value in place: %q", got)
			}
		})
	}
}
