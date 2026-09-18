package executor

import (
	"os"
	"strings"
	"testing"
)

// TestDocumentedIsolationMatchesTheArgv is the guard for the one class of
// finding no CI check in this repo can otherwise see: a security claim in prose
// that the code contradicts.
//
// docs/security-model.md said the executor sandbox gives "no network" and a
// "read-only /repo mount". That described ContainerSandbox, which has one
// non-test caller (the deterministic check). The executors that run
// model-generated code get neither: no --network flag at all, and /work mounted
// read-write. Three audits' worth of guards -- route sets, request fields, 409
// declarations -- were all green while that stood.
//
// So this asserts the two properties the doc now states, against the argv the
// code actually builds. It is deliberately narrow: it pins the facts that were
// wrong, not the whole document.
func TestDocumentedIsolationMatchesTheArgv(t *testing.T) {
	doc, err := os.ReadFile("../../docs/security-model.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(doc)

	t.Run("agent egress is documented as unrestricted", func(t *testing.T) {
		// The claim only stays true while the agent has no --network flag.
		e := &SandboxedAgentExecutor{name: "hermes", image: "img", dockerPath: "docker"}
		args := e.containerArgs(Request{WorkRoot: "/tmp/work"}, "attempt", "", "")
		if hasFlag(args, "--network") {
			t.Fatal("the agent now sets --network; docs/security-model.md still says egress is unrestricted")
		}
		if !strings.Contains(text, "Network egress is unrestricted") {
			t.Error("docs/security-model.md no longer states that agent egress is unrestricted")
		}
	})

	t.Run("the deterministic sandbox really does isolate", func(t *testing.T) {
		s := NewContainerSandbox("alpine:3.20")
		args := s.isolatedArgs(RunOptions{WorkRoot: "/tmp/work", RepoRoot: "/tmp/repo"})
		if !hasFlagValue(args, "--network", "none") {
			t.Error("ContainerSandbox lost --network none, which security-model.md still claims")
		}
		if !containsArg(args, "/tmp/repo:/repo:ro") {
			t.Error("ContainerSandbox lost its read-only /repo mount, which security-model.md still claims")
		}
	})

	t.Run("the doc distinguishes the two", func(t *testing.T) {
		// The original defect was one section generalising over both.
		if !strings.Contains(text, "ContainerSandbox` is not the executor") {
			t.Error("docs/security-model.md no longer separates ContainerSandbox from the agent executors")
		}
		if !strings.Contains(text, "What this does not protect against") {
			t.Error("the negative-space section is gone; that is where the unscoped claims went last time")
		}
	})
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func hasFlagValue(args []string, flag, value string) bool {
	for i, a := range args {
		if a == flag && i+1 < len(args) && args[i+1] == value {
			return true
		}
	}
	return false
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
