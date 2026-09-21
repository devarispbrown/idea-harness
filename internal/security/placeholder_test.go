package security

import (
	"testing"
)

// TestGenericAssignmentInTestFixtureIsAdvisory reproduces the ICP re-run's
// defect #1, case 1: a *_test.go fixture assigning a "secret" field (e.g. a
// JSON literal used to build a request in a table test) trips the generic
// "credential assignment" pattern, but a human did not write a real
// credential into test code -- it should be visible to a reviewer but never
// block a change set on its own. Outside a test/fixture path, the identical
// value still blocks (Advisory stays false).
func TestGenericAssignmentInTestFixtureIsAdvisory(t *testing.T) {
	content := `func TestCreateKey(t *testing.T) {
	body := ` + "`" + `{"secret": "k3jQ9zPmT7xR2wLn"}` + "`" + `
}
`
	testMatches := FindCredentialsInFile("handlers_test.go", content)
	if len(testMatches) == 0 {
		t.Fatal("expected the generic credential-assignment pattern to still match in a _test.go file")
	}
	for _, m := range testMatches {
		if m.Kind != genericAssignmentKind {
			continue
		}
		if !m.Advisory {
			t.Errorf("generic assignment match in a _test.go file: Advisory = false, want true: %+v", m)
		}
	}

	nonTestMatches := FindCredentialsInFile("internal/api/handlers.go", content)
	found := false
	for _, m := range nonTestMatches {
		if m.Kind != genericAssignmentKind {
			continue
		}
		found = true
		if m.Advisory {
			t.Errorf("generic assignment match outside a test path: Advisory = true, want false: %+v", m)
		}
	}
	if !found {
		t.Fatal("expected the generic credential-assignment pattern to match outside a test path too")
	}
}

// TestFixturePathVariants pins isTestFixturePath's recognized shapes (ICP
// re-run defect #1): a Go test file, a JS/TS spec/test file, and every named
// fixture directory segment the defect names.
func TestFixturePathVariants(t *testing.T) {
	fixturePaths := []string{
		"internal/api/handlers_test.go",
		"web/src/components/Button.spec.tsx",
		"web/src/components/Button.test.tsx",
		"test/fixtures/keys.json",
		"tests/api_test.py",
		"testdata/config.json",
		"fixtures/sample.json",
		"web/src/__tests__/App.test.tsx",
	}
	for _, p := range fixturePaths {
		if !isTestFixturePath(p) {
			t.Errorf("isTestFixturePath(%q) = false, want true", p)
		}
	}
	nonFixturePaths := []string{
		"internal/api/handlers.go",
		"config.json",
		"web/src/components/Button.tsx",
		"cmd/idea/main.go",
	}
	for _, p := range nonFixturePaths {
		if isTestFixturePath(p) {
			t.Errorf("isTestFixturePath(%q) = true, want false", p)
		}
	}
}

// TestCommentPlaceholderNeverMatches reproduces case 2 of defect #1: a doc
// comment naming the SHAPE of a header, `X-API-Key: <id>:<secret>`, must
// never be treated as a literal credential assignment.
func TestCommentPlaceholderNeverMatches(t *testing.T) {
	cases := []string{
		"// X-API-Key: <id>:<secret>",
		`api_key: "<secret>"`,
		`api_key: <secret>`,
		`token = "${API_KEY}"`,
		`secret: "{{ vault_secret }}"`,
		`password = <your-password-here>`,
	}
	for _, c := range cases {
		if FindCredential(c) != "" {
			t.Errorf("comment/template placeholder %q was matched as a credential", c)
		}
		if got := Redact(c); got != c {
			t.Errorf("comment/template placeholder %q was redacted to %q", c, got)
		}
	}
}

// TestStripeKeysAlwaysRaiseAFindingRegardlessOfShape is review C3's revision
// of defect #1 case 3. An earlier version of this catalog ran a Stripe key's
// value through the placeholder-SHAPE gate (isPlaceholderShape) and
// suppressed the finding entirely for an obviously-placeholder body (a
// trailing run of the same digit). Review C3 found that gate dangerously
// unreliable when applied to a PROVIDER-PREFIXED format: a Monte-Carlo run
// over 200k random well-formed Stripe-shaped keys missed a REAL secret 1 in
// ~8,000-9,500 times purely by chance shape -- the "sk_live_" prefix is
// already the evidence a match is real, so the shape gate no longer applies
// to any provider-specific pattern. Both an obviously-placeholder key and a
// real-looking one now raise a finding (Overridable: a human can dismiss an
// obvious placeholder), in a test/fixture path or not (a provider-specific
// format is never downgraded to advisory there either) -- and RedactString
// masks both regardless of the finding.
func TestStripeKeysAlwaysRaiseAFindingRegardlessOfShape(t *testing.T) {
	keys := []string{
		"sk_live_00000000000000000000", // obviously-placeholder body
		"sk_live_11111111111111111111",
		"sk_live_22222222222222222222",
		"sk_live_4eC39HqLyjWDarjtT1zdp7dc", // realistic entropy
	}
	for _, key := range keys {
		content := `{"stripe_key_example": "` + key + `"}`
		if FindCredential(content) == "" {
			t.Errorf("Stripe key %q raised no finding at all", key)
		}
		if got := Redact(content); got == content {
			t.Errorf("Redact left Stripe key %q in place: %q", key, got)
		}
		for _, path := range []string{"config.json", "stripe_test.go"} {
			matches := FindCredentialsInFile(path, content)
			found := false
			for _, m := range matches {
				if m.Kind == "Stripe secret key" {
					found = true
					if !m.Overridable {
						t.Errorf("Stripe key %q finding is not Overridable", key)
					}
					if m.Advisory {
						t.Errorf("Stripe key %q in %q: Advisory = true, want false (provider formats always block)", key, path)
					}
				}
			}
			if !found {
				t.Errorf("expected a Stripe secret key match for %q in %q", key, path)
			}
		}
	}
}

// TestLowEntropyGenericAssignmentIsNeverACredential covers the general
// placeholder/low-entropy rule (defect #1 bullet 1) outside any test path: a
// repetitive or low-entropy generic assignment value is not a credential at
// all (not even advisory), while a realistic one at the same location still
// is.
func TestLowEntropyGenericAssignmentIsNeverACredential(t *testing.T) {
	placeholderCases := []string{
		`{"password": "aaaaaaaaaaaaaaaa"}`,  // all-same-char run
		`{"password": "changeme_changeme"}`, // placeholder word
		`{"api_key": "example_example_1"}`,  // placeholder word
		`{"token": "abababababababab"}`,     // low entropy (2-symbol alternation)
	}
	for _, c := range placeholderCases {
		if FindCredential(c) != "" {
			t.Errorf("placeholder-shaped value %q was matched as a credential", c)
		}
	}

	const realistic = `{"password": "k3jQ9zPmT7xR2wLnB6"}`
	if FindCredential(realistic) == "" {
		t.Error("a realistic password value must still be detected")
	}
}

// TestConfiguredSecretAlwaysBlocksRegardlessOfShapeOrPath is the other half
// of defect #1's design: a value drawn from the project's OWN configured
// secrets always blocks, in a test path or not, however placeholder-shaped it
// happens to look -- placeholder detection only ever applies to pattern
// matches (FindPatternsInFile), never to a known value (FindKnownInFile).
func TestConfiguredSecretAlwaysBlocksRegardlessOfShapeOrPath(t *testing.T) {
	const known = "xxxx0000xxxx" // deliberately placeholder-shaped
	r := NewRedactor(known)
	content := "value = " + known
	matches := r.FindKnownInFile("some_test.go", content)
	if len(matches) == 0 {
		t.Fatal("a configured secret value must always be found, whatever its shape")
	}
	for _, m := range matches {
		if m.Overridable {
			t.Error("a configured secret value must never be Overridable")
		}
		if m.Advisory {
			t.Error("a configured secret value must never be Advisory")
		}
	}
}

// TestRealLookingSecretsStillBlockInTestPaths pins the other required
// guarantee: provider-specific formats with realistic entropy keep blocking
// even inside test/fixture code (only the GENERIC assignment pattern is ever
// downgraded to advisory there).
func TestRealLookingSecretsStillBlockInTestPaths(t *testing.T) {
	cases := []struct {
		name, kind, content string
	}{
		{"aws_access_key", "AWS access key ID", `key := "AKIAJ7EXAMPLE9QRSTUV"`},
		{"github_pat", "GitHub personal access token", `token := "ghp_aB3dE9fGhJ2kL5mN8pQrS1tUvWxYz0"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			matches := FindCredentialsInFile("pkg_test.go", c.content)
			found := false
			for _, m := range matches {
				if m.Kind == c.kind {
					found = true
					if m.Advisory {
						t.Errorf("%s in a test file: Advisory = true, want false", c.kind)
					}
				}
			}
			if !found {
				t.Fatalf("expected a %s match", c.kind)
			}
		})
	}
}

// TestIsPlaceholderShape pins the shape-only heuristics directly.
func TestIsPlaceholderShape(t *testing.T) {
	placeholders := []string{
		"00000000",
		"aaaaaaaaaaaaaaaa",
		"xxxxxxxxxxxxxxxx",
		"abababababababab",
	}
	for _, v := range placeholders {
		if !isPlaceholderShape(v) {
			t.Errorf("isPlaceholderShape(%q) = false, want true", v)
		}
	}
	realistic := []string{
		"k3jQ9zPmT7xR2wLnB6",
		"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"AKIA1234567890ABCDEF", // a short sequential tail alone is not low entropy
	}
	for _, v := range realistic {
		if isPlaceholderShape(v) {
			t.Errorf("isPlaceholderShape(%q) = true, want false", v)
		}
	}
}

// TestIsSequentialFillScopedToWholeValue pins isSequentialFill's
// majority-of-value design: a short sequential fragment embedded in an
// otherwise realistic value is not flagged, but a value that IS essentially
// one long run, start to end, is.
func TestIsSequentialFillScopedToWholeValue(t *testing.T) {
	if isSequentialFill("django-insecure-abcdefgh") {
		t.Error("a short embedded sequential fragment must not flag the whole value")
	}
	if !isSequentialFill("abcdefghijklmnop") {
		t.Error("a value that is entirely a sequential run must be flagged")
	}
}

// TestContainsPlaceholderWordCase is a quick sanity check on the word list.
func TestContainsPlaceholderWordCase(t *testing.T) {
	for _, v := range []string{"EXAMPLE123456", "My-Dummy-Value1", "CHANGEME_NOW_1"} {
		if !containsPlaceholderWord(v) {
			t.Errorf("containsPlaceholderWord(%q) = false, want true", v)
		}
	}
	if containsPlaceholderWord("k3jQ9zPmT7xR2wLn") {
		t.Error("containsPlaceholderWord false positive on a realistic value")
	}
}
