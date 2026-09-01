package executor

import (
	"testing"

	"github.com/idea-harness/idea-harness/internal/domain"
)

func TestBrokerAuthorizeAllowed(t *testing.T) {
	b := CapabilityBroker{}
	if err := b.Authorize([]string{"fs.write"}, "write_file"); err != nil {
		t.Fatalf("Authorize(fs.write, write_file) error = %v, want nil", err)
	}
	if err := b.Authorize([]string{"fs.read"}, "read_file"); err != nil {
		t.Fatalf("Authorize(fs.read, read_file) error = %v, want nil", err)
	}
}

func TestBrokerAuthorizeDenied(t *testing.T) {
	b := CapabilityBroker{}
	// An action that requires fs.write is denied when fs.write is absent.
	if err := b.Authorize([]string{"fs.read"}, "write_file"); err == nil {
		t.Error("Authorize(fs.read, write_file) = nil error, want error")
	}
	// The empty declared set denies everything.
	if err := b.Authorize(nil, "write_file"); err == nil {
		t.Error("Authorize(nil, write_file) = nil error, want error")
	}
}

func TestBrokerAuthorizeUnknownAction(t *testing.T) {
	b := CapabilityBroker{}
	if err := b.Authorize([]string{"fs.write"}, "arbitrary_action"); err == nil {
		t.Error("Authorize() for unknown action = nil error, want error")
	}
}

func TestBrokerGrantSuggestedReturnsEmpty(t *testing.T) {
	b := CapabilityBroker{}
	if got := b.GrantSuggested([]string{"fs.write", "exec.run"}); len(got) != 0 {
		t.Fatalf("GrantSuggested() = %v, want empty (models cannot grant capabilities)", got)
	}
}

func TestBrokerModelSuggestedCapabilityRejected(t *testing.T) {
	b := CapabilityBroker{}

	// A model suggests a capability it wants (e.g. arbitrary code execution).
	suggested := []string{"exec.run"}
	granted := b.GrantSuggested(suggested)
	if len(granted) != 0 {
		t.Fatalf("broker granted a model-suggested capability: %v", granted)
	}

	// The executor's declared set is unchanged: it still only has fs.write.
	declared := []string{"fs.write"}
	if err := b.Authorize(declared, "run_command"); err == nil {
		t.Error("Authorize(fs.write, run_command) = nil error, want error: " +
			"the model-suggested exec.run capability must not be granted")
	}
}

func TestTrustAllowsPolicy(t *testing.T) {
	// The engine-assigned trust tier (not the executor's self-declaration)
	// determines which actions may run.
	cases := []struct {
		trust  domain.ExecutorTrust
		action string
		want   bool
	}{
		{domain.ExecutorInternal, "write_file", true},
		{domain.ExecutorInternal, "read_file", true},
		{domain.ExecutorInternal, "run_command", true},
		{domain.ExecutorLocalExternal, "write_file", true},
		{domain.ExecutorLocalExternal, "run_command", false}, // no exec for local_external
		{domain.ExecutorRemoteExternal, "write_file", false}, // fail-closed
		{domain.ExecutorRemoteExternal, "read_file", false},  // fail-closed
		{domain.ExecutorTrust("unknown"), "write_file", false},
	}
	for _, tc := range cases {
		if got := TrustAllows(tc.trust, tc.action); got != tc.want {
			t.Errorf("TrustAllows(%q, %q) = %v, want %v", tc.trust, tc.action, got, tc.want)
		}
	}
}

func TestTrustTierFor(t *testing.T) {
	cases := map[string]domain.ExecutorTrust{
		"internal":  domain.ExecutorInternal,
		"container": domain.ExecutorLocalExternal,
		"omp":       domain.ExecutorLocalExternal,
		"hermes":    domain.ExecutorLocalExternal,
		"bogus":     domain.ExecutorRemoteExternal, // fail-closed
		"":          domain.ExecutorRemoteExternal,
	}
	for name, want := range cases {
		if got := TrustTierFor(name); got != want {
			t.Errorf("TrustTierFor(%q) = %q, want %q", name, got, want)
		}
	}
}
