package cli

import (
	"io"
	"strings"
	"testing"
)

func TestExecutorSetupValidation(t *testing.T) {
	// These paths return before any daemon or Docker call, so they are testable
	// without a running daemon or Docker.
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--kind", "bogus", "--image", "x", "--yes"}, "unsupported executor kind"},
		{[]string{"--kind", "omp", "--yes"}, "--image is required"},
		{[]string{"--image", "x"}, "pass --yes"}, // missing confirmation, no Docker
	}
	for _, c := range cases {
		// Parsed through the table, so the test exercises the same spec dispatch
		// uses rather than a second one written for the test.
		a, err := parseFor(t, "executors", append([]string{"setup"}, c.args...))
		if err != nil {
			t.Fatalf("args %v: parse: %v", c.args, err)
		}
		err = cmdExecutorSetup(a, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("args %v: got %v, want substring %q", c.args, err, c.want)
		}
	}
}
