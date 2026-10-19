package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/daemon"
)

// fakeTailscale swaps the `tailscale` runner for the duration of the test.
func fakeTailscale(t *testing.T, fn func(args ...string) (string, error)) {
	t.Helper()
	old := tailscaleRun
	tailscaleRun = fn
	t.Cleanup(func() { tailscaleRun = old })
}

func TestRemoteEnable(t *testing.T) {
	state := t.TempDir()
	t.Setenv("IDEA_HARNESS_STATE", state)
	fakeTailscale(t, func(args ...string) (string, error) {
		switch args[0] {
		case "status":
			return `{"Self":{"DNSName":"laptop.tail1234.ts.net."}}`, nil
		case "serve":
			return "", nil
		}
		return "", fmt.Errorf("unexpected tailscale args %v", args)
	})

	var stdout, stderr bytes.Buffer
	if err := remoteEnable(&stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	cfg, err := daemon.LoadDaemonConfig(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.AllowedHosts) != 1 || cfg.AllowedHosts[0] != "laptop.tail1234.ts.net" {
		t.Fatalf("allowed_hosts = %v, want [laptop.tail1234.ts.net]", cfg.AllowedHosts)
	}
	if !strings.Contains(stdout.String(), "https://laptop.tail1234.ts.net") {
		t.Fatalf("output does not name the URL: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "idea ui pair --qr") {
		t.Fatalf("output does not name the pair step: %q", stdout.String())
	}
}

func TestRemoteEnableNoMagicDNS(t *testing.T) {
	state := t.TempDir()
	t.Setenv("IDEA_HARNESS_STATE", state)
	fakeTailscale(t, func(args ...string) (string, error) {
		return `{"Self":{"DNSName":""}}`, nil
	})
	var stdout, stderr bytes.Buffer
	if err := remoteEnable(&stdout, &stderr); err == nil {
		t.Fatal("want an error when MagicDNS is not enabled")
	}
	if _, err := daemon.LoadDaemonConfig(state); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteDisable(t *testing.T) {
	state := t.TempDir()
	t.Setenv("IDEA_HARNESS_STATE", state)
	if err := daemon.WriteDaemonConfig(state, daemon.DaemonConfig{AllowedHosts: []string{"laptop.tail1234.ts.net"}}); err != nil {
		t.Fatal(err)
	}
	fakeTailscale(t, func(args ...string) (string, error) { return "", nil })

	var stdout, stderr bytes.Buffer
	if err := remoteDisable(&stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	cfg, err := daemon.LoadDaemonConfig(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.AllowedHosts) != 0 {
		t.Fatalf("allowed_hosts = %v, want empty", cfg.AllowedHosts)
	}
	if !strings.Contains(stdout.String(), "off") {
		t.Fatalf("output should say off: %q", stdout.String())
	}
}

func TestRemoteStatus(t *testing.T) {
	state := t.TempDir()
	t.Setenv("IDEA_HARNESS_STATE", state)
	if err := daemon.WriteDaemonConfig(state, daemon.DaemonConfig{
		AllowedHosts: []string{"laptop.tail1234.ts.net"},
		Listen:       "100.101.102.103:8787",
	}); err != nil {
		t.Fatal(err)
	}
	fakeTailscale(t, func(args ...string) (string, error) {
		if args[0] == "serve" && args[1] == "status" {
			return "# serve status\n", nil
		}
		return "", nil
	})
	var stdout bytes.Buffer
	if err := remoteStatus(&stdout); err != nil {
		t.Fatal(err)
	}
	out := stdout.String()
	if !strings.Contains(out, "laptop.tail1234.ts.net") || !strings.Contains(out, "100.101.102.103:8787") {
		t.Fatalf("status should name the host and listener: %q", out)
	}
}

func TestRemoteStatusOff(t *testing.T) {
	t.Setenv("IDEA_HARNESS_STATE", t.TempDir())
	var stdout bytes.Buffer
	if err := remoteStatus(&stdout); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "off") {
		t.Fatalf("status should say off: %q", stdout.String())
	}
}

func TestTailscaleMagicDNSName(t *testing.T) {
	fakeTailscale(t, func(args ...string) (string, error) {
		return `{"Self":{"DNSName":"laptop.tail1234.ts.net."}}`, nil
	})
	name, err := tailscaleMagicDNSName()
	if err != nil {
		t.Fatal(err)
	}
	if name != "laptop.tail1234.ts.net" {
		t.Fatalf("name = %q, want laptop.tail1234.ts.net", name)
	}
}

func TestTailscaleMagicDNSNameNotLoggedIn(t *testing.T) {
	fakeTailscale(t, func(args ...string) (string, error) {
		return "", fmt.Errorf("not running")
	})
	if _, err := tailscaleMagicDNSName(); err == nil || !strings.Contains(err.Error(), "tailscale up") {
		t.Fatalf("want an error naming `tailscale up`, got %v", err)
	}
}
