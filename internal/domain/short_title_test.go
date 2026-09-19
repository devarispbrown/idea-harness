package domain

import (
	"strings"
	"testing"
)

// TestGenerateShortTitle pins C1's short-title generation (wave 3 contract):
// the first sentence or clause, a common lead-in dropped, trimmed at a word
// boundary to <=60 chars. Mutation-check: flip the 60-char bound, the
// lead-in list, or the clause separators, and one of these must fail.
func TestGenerateShortTitle(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"drops I want to", "I want to add per-user API keys with rate limiting.", "add per-user API keys with rate limiting"},
		{"drops Build", "Build a dashboard, then add export.", "a dashboard"},
		{"lead-in verb dropped even mid-sentence-initial", "Add per-user API keys with rate limiting.", "per-user API keys with rate limiting"},
		{"no recognized lead-in", "Per-user API keys with rate limiting.", "Per-user API keys with rate limiting"},
		{"empty input", "", ""},
		{"whitespace only", "   ", ""},
		{"lead-in swallows everything, falls back to the whole idea", "Build ", "Build"},
		{"case-insensitive lead-in", "I WANT TO ship faster.", "ship faster"},
		{"clause boundary is semicolon", "Ship faster; it matters a lot.", "Ship faster"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := GenerateShortTitle(c.in)
			if got != c.want {
				t.Errorf("GenerateShortTitle(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestGenerateShortTitleNeverExceeds60Chars is the deterministic bound the
// wave 3 contract states explicitly ("<= 60 characters"), checked against a
// long idea with no clause boundary at all -- the case most likely to slip
// past a naive substring cut.
func TestGenerateShortTitleNeverExceeds60Chars(t *testing.T) {
	long := strings.Repeat("word ", 40) // 200 chars, no punctuation
	got := GenerateShortTitle(long)
	if len([]rune(got)) > 60 {
		t.Fatalf("short title is %d runes, want <= 60: %q", len([]rune(got)), got)
	}
	if got == "" {
		t.Fatal("short title is empty for a long, punctuation-free idea")
	}
}

// TestGenerateShortTitleIsDeterministic backs the backfill story (store's
// backfillShortTitle regenerates and compares -- it must always agree with
// what CreateInitiative wrote the first time).
func TestGenerateShortTitleIsDeterministic(t *testing.T) {
	idea := "I want to add per-user API keys with rate limiting to protect my API from abuse."
	a := GenerateShortTitle(idea)
	b := GenerateShortTitle(idea)
	if a != b {
		t.Fatalf("GenerateShortTitle is not deterministic: %q vs %q", a, b)
	}
}
