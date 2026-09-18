package config

import (
	"strings"
	"testing"
)

// TestUnknownModelWarns pins O2's product half.
//
// Nothing validated the model half of a spec, so `idea model set
// anthropic/claude-3-7-sonnet` printed success and the first call returned a
// non-retryable 404. Nine such ids shipped in the documentation before an audit
// found them by reading.
func TestUnknownModelWarns(t *testing.T) {
	for _, tc := range []struct {
		spec string
		warn bool
		why  string
	}{
		{"anthropic/claude-3-7-sonnet", true, "the real defect: no such id, dated or undated"},
		{"anthropic/claude-3-7-sonnet-latest", true, "retired: Anthropic answered 404 on 2026-09-17"},
		{"anthropic/claude-sonnet-5", false, "the correct id"},
		{"anthropic/claude-sonnet-4-5", false, "another correct id"},
		{"openrouter/anthropic/claude-3.7-sonnet", false, "openrouter ids are arbitrary upstream strings"},
		{"ollama/whatever-i-pulled", false, "ollama serves whatever is pulled locally"},
		{"madeup/model", false, "an unknown provider is a separate, harder error"},
	} {
		got := UnknownModelProblems("harness.default_model", tc.spec)
		if (len(got) > 0) != tc.warn {
			t.Errorf("%s: warned=%v want %v (%s)", tc.spec, len(got) > 0, tc.warn, tc.why)
			continue
		}
		if tc.warn {
			if got[0].Severity != SeverityWarn {
				t.Errorf("%s: severity %v, want warn — an error would refuse models newer than the catalog",
					tc.spec, got[0].Severity)
			}
			if !strings.Contains(got[0].Message, "claude-sonnet-5") {
				t.Errorf("%s: the warning should name the valid ids, got: %s", tc.spec, got[0].Message)
			}
		}
	}
}

// TestKeyProblemDoesNotShadowTheCatalogWarning: a missing key and a mistyped
// model are independent mistakes and a user can make both at once. Returning
// early on the key problem hid the catalog warning in exactly the case that
// motivated it — someone setting a model whose key they have not exported yet.
//
// t.Setenv, because whether keyProblems fires depends on the ambient
// environment: a first version of this test asserted on it directly and passed
// or failed according to whether the developer happened to have the key set.
func TestKeyProblemDoesNotShadowTheCatalogWarning(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")

	probs := modelSpecProblems("harness.default_model", "anthropic/claude-3-7-sonnet",
		map[string]bool{"anthropic": true}, map[string]ProviderConfig{}, map[string]string{}, true)

	var sawKey, sawCatalog bool
	for _, p := range probs {
		if strings.Contains(p.Message, "ANTHROPIC_API_KEY") {
			sawKey = true
		}
		if strings.Contains(p.Message, "bundled catalog") {
			sawCatalog = true
		}
	}
	if !sawKey {
		t.Error("the missing-key problem disappeared")
	}
	if !sawCatalog {
		t.Error("the catalog warning is shadowed by the missing-key problem")
	}
}
