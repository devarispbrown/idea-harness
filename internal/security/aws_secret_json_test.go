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

// TestAWSSecretAccessKeyPlaceholdersStayQuiet pins the other half of defect
// #4's fix: validFlatTokenValue's placeholder-SHAPE gate (isPlaceholderShape:
// a long repeated-character run, or overall entropy too low to plausibly be
// generated secret data) must keep rejecting an obvious placeholder body,
// unaffected by widening the keyword match to also accept a JSON key's
// closing quote. Every value here is 30+ characters and inside the pattern's
// own [A-Za-z0-9/+=] charset, so each would otherwise reach the shape check.
func TestAWSSecretAccessKeyPlaceholdersStayQuiet(t *testing.T) {
	cases := []struct {
		name, text string
	}{
		// hasLongRun: a run of one repeated character.
		{"all_x_run", `{"aws_secret_access_key": "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`},
		{"all_zero_run", `{"aws_secret_access_key": "00000000000000000000000000000000000000"}`},
		{"padding_equals_run", `{"aws_secret_access_key": "================================"}`},
		// Low aggregate entropy: a two-character alternation carries only 1
		// bit/char, well under placeholderEntropyThreshold (2.5).
		{"alternating_low_entropy", `{"aws_secret_access_key": "ABABABABABABABABABABABABABABABABABABAB"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if FindCredential(c.text) != "" {
				t.Errorf("placeholder value flagged as a credential: %q", c.text)
			}
			for _, m := range FindCredentialsInFile("config/deploy.json", c.text) {
				if m.Kind == "AWS secret access key" {
					t.Errorf("placeholder value produced an AWS secret access key finding: %q -> %+v", c.text, m)
				}
			}
		})
	}
}
