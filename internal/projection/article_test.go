package projection

import (
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestIndefiniteArticle reproduces the ICP re-run's own defect #5 timeline
// copy bug ("a API key") and pins the fix across the catalog's actual kind
// vocabulary, including the two directions a naive first-letter check gets
// wrong: "AWS"/"API" need "an" (a vowel SOUND), "URL" needs "a" (a consonant
// "you" sound despite the vowel letter).
func TestIndefiniteArticle(t *testing.T) {
	cases := []struct {
		kind, want string
	}{
		{"API key", "an"},
		{"AWS access key ID", "an"},
		{"AWS secret access key", "an"},
		{"OpenAI API key", "an"},
		{"OpenAI/Anthropic API key", "an"},
		{"npm access token", "an"},
		{"URL userinfo credential", "a"},
		{"JWT", "a"},
		{"PEM private key", "a"},
		{"GitHub personal access token", "a"},
		{"Stripe secret key", "a"},
		{"Slack token", "a"},
		{"credential", "a"},
		{"", "a"},
	}
	for _, c := range cases {
		if got := indefiniteArticle(c.kind); got != c.want {
			t.Errorf("indefiniteArticle(%q) = %q, want %q", c.kind, got, c.want)
		}
	}
}

// TestDenialDetailUsesCorrectArticle is the same fix at denialDetail's own
// call site: "a API key at src/keys.js:14" must read "an API key at
// src/keys.js:14".
func TestDenialDetailUsesCorrectArticle(t *testing.T) {
	got := denialDetail([]domain.Finding{{
		Code:              domain.DenyCodeSecretSweep,
		SecretKind:        "API key",
		SecretPath:        "src/keys.js",
		SecretOverridable: true,
	}})
	want := "an API key at src/keys.js"
	if got != want {
		t.Errorf("denialDetail() = %q, want %q", got, want)
	}
}

// TestDenialDetailFallsBackToCredentialWithArticle covers the no-SecretKind
// case: "a credential at ..." (not "an", since "credential" starts with a
// consonant sound).
func TestDenialDetailFallsBackToCredentialWithArticle(t *testing.T) {
	got := denialDetail([]domain.Finding{{
		Code:       domain.DenyCodeSecretSweep,
		SecretPath: "src/keys.js",
	}})
	want := "a credential at src/keys.js"
	if got != want {
		t.Errorf("denialDetail() = %q, want %q", got, want)
	}
}
