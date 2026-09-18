package executor

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestAgentContractAgainstThePinnedImage runs the REAL production path --
// containerArgs, the generated agent home, HERMES_HOME, the host-gateway
// mapping and the exact Hermes argv -- against the real image, with no API key.
//
// Two defects shipped in this path and neither needed a model to detect. The
// argv used a flag the image does not have (`--in`), so Hermes printed its usage
// and exited; and the hardened container could not boot the image at all,
// because --read-only with a host --user left the entrypoint unable to create
// its home. The only test that exercised the path was the opt-in agent tier,
// which needs a key and had never run.
//
// The endpoint here deliberately refuses connections. Success is that Hermes
// got far enough to accept the query -- it parsed the argv, booted under the
// hardened arguments and read the generated config -- not that a model answered.
//
// Skipped unless IDEA_AGENT_CONTRACT_IMAGE names a local image, because the
// Hermes image is 5.4 GB. CI sets it to "pinned", which pulls
// DefaultAgentImage("hermes") -- the image `idea executors setup` downloads.
func TestAgentContractAgainstThePinnedImage(t *testing.T) {
	image := os.Getenv("IDEA_AGENT_CONTRACT_IMAGE")
	if image == "" {
		t.Skip("IDEA_AGENT_CONTRACT_IMAGE not set; this check pulls a 5.4 GB image")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	if image == "pinned" {
		image = DefaultAgentImage("hermes")
		if _, err := PullImage(ctx, image); err != nil {
			t.Fatalf("pull %s: %v", image, err)
		}
	}

	// Keyless, loopback: exercises the host-gateway rewrite and needs no secret.
	e := NewHermesExecutor(nil, "ollama/contract-probe", image, AgentProvider{Name: "ollama", BaseURL: "http://127.0.0.1:9/v1"})
	work := t.TempDir()
	attempt, err := e.Start(ctx, Request{TaskStatement: "write a file named probe.txt", WorkRoot: work})
	if err != nil {
		t.Fatalf("the container did not start: %v", err)
	}
	defer e.Cancel(context.Background(), attempt.ID)

	for {
		st, err := e.Status(ctx, attempt.ID)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if st.Status != "running" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("the agent did not exit within the test timeout against a refused endpoint")
		case <-time.After(500 * time.Millisecond):
		}
	}

	out := e.tailLogs(ctx, attempt.ID, 200)
	if strings.Contains(out, "usage: hermes") {
		t.Fatalf("Hermes rejected the argv and printed its usage — the argument contract is broken:\n%s", out)
	}
	if strings.Contains(out, "Permission denied") {
		t.Fatalf("the hardened container could not boot the image:\n%s", out)
	}
	if !strings.Contains(out, "Query:") {
		t.Fatalf("Hermes never accepted the query, so the path did not get as far as a model call:\n%s", out)
	}
}
