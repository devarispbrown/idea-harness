package executor

import (
	"strings"
	"testing"
)

func TestVerifyPinnedDigest(t *testing.T) {
	const d = "sha256:b6e41c155d6bfce5ad83c5d0fec670086db8a43250e4511c9474134be5482d33"
	for _, tc := range []struct {
		ref     string
		digests []string
		ok      bool
	}{
		{"nousresearch/hermes-agent:v2026.5.16@" + d, []string{"nousresearch/hermes-agent@" + d}, true},
		{"docker.io/library/alpine:3.20@" + d, []string{"alpine@" + d}, true},
		{"registry.local:5000/x:tag@" + d, []string{"registry.local:5000/x@" + d}, true},
		// The same digest under another repository is not this image.
		{"nousresearch/hermes-agent:v2026.5.16@" + d, []string{"evil/hermes-agent@" + d}, false},
		{"nousresearch/hermes-agent:v2026.5.16@" + d, []string{"nousresearch/hermes-agent@sha256:0000"}, false},
		{"nousresearch/hermes-agent:v2026.5.16@" + d, nil, false},
		// Nothing pinned, nothing to verify.
		{"ghcr.io/x/omp:latest", nil, true},
	} {
		err := VerifyPinnedDigest(tc.ref, tc.digests)
		if (err == nil) != tc.ok {
			t.Errorf("VerifyPinnedDigest(%q, %v) = %v, want ok=%v", tc.ref, tc.digests, err, tc.ok)
		}
	}
}

// TestACPIsUntestedNotUnknown: no ACP agent has been smoke-verified, so every
// acp combination warns -- with a setup command that names the flags acp
// actually needs.
func TestACPIsUntestedNotUnknown(t *testing.T) {
	c := CheckModelCompat("acp", "anthropic/claude-sonnet-5")
	if c.Blocked || !strings.Contains(c.Message, "untested") || !strings.Contains(c.Message, "--acp-command") {
		t.Fatalf("CheckModelCompat(acp) = %+v", c)
	}
}

// TestDefaultHermesImageIsDigestPinned: a tag alone is mutable, so the image
// `idea executors setup` downloads by default must carry a digest.
func TestDefaultHermesImageIsDigestPinned(t *testing.T) {
	img := DefaultAgentImage("hermes")
	if _, _, ok := splitDigestRef(img); !ok {
		t.Fatalf("default Hermes image %q is not pinned by digest", img)
	}
	if !strings.HasPrefix(img, "nousresearch/hermes-agent:v") {
		t.Fatalf("default Hermes image %q lost its human-readable version tag", img)
	}
}
