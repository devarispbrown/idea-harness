package executor

import "testing"

func TestDefaultAgentImage(t *testing.T) {
	if DefaultAgentImage("hermes") == "" {
		t.Fatal("hermes should have a pinned default image (auto-download)")
	}
	if DefaultAgentImage("omp") != "" {
		t.Fatal("omp should have NO default image (bring your own)")
	}
	if DefaultAgentImage("bogus") != "" {
		t.Fatal("unknown kind should have no default image")
	}
}
