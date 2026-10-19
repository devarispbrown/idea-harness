package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/daemon"
)

// tailscaleRun executes a `tailscale` subcommand and returns its combined
// output. A package var so tests can replace it with a fake.
var tailscaleRun = func(args ...string) (string, error) {
	cmd := exec.Command("tailscale", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// cmdRemote implements `idea remote enable|disable|status`: the one-command
// setup/teardown for remote access (docs/self-hosting.md). It writes
// config.yaml's daemon.allowed_hosts and stops the daemon so the next start
// reads the change; the `tailscale serve`/`status` calls are the only things
// that touch the Tailscale CLI.
func cmdRemote(a cmdArgs, stdout, stderr io.Writer) error {
	switch a.sub {
	case "enable":
		return remoteEnable(stdout, stderr)
	case "disable":
		return remoteDisable(stdout, stderr)
	case "status":
		return remoteStatus(stdout)
	default:
		return errorf(kindUsage, "unknown remote subcommand %q", a.sub)
	}
}

// remoteEnable turns remote access on: detect the MagicDNS name, run
// `tailscale serve`, record the name in allowed_hosts, restart the daemon.
func remoteEnable(stdout, stderr io.Writer) error {
	name, err := tailscaleMagicDNSName()
	if err != nil {
		return errorf(kindError, "tailscale: %v", err)
	}
	// Best-effort: the name already proved Tailscale is installed and signed
	// in, and "already serving" is a normal re-run, so a failure here is a
	// warning, not a stop. The allowed_hosts write below is what turns remote
	// access on; tailscale serve provides the HTTPS address.
	if _, err := tailscaleRun("serve", "--bg", "8787"); err != nil {
		fmt.Fprintln(stderr, warn("note: `tailscale serve --bg 8787`: "+strings.TrimSpace(err.Error())))
	}

	stateRoot := daemon.DefaultStateRoot()
	cfg, err := daemon.LoadDaemonConfig(stateRoot)
	if err != nil {
		return err
	}
	cfg.AllowedHosts = []string{name}
	if err := daemon.WriteDaemonConfig(stateRoot, cfg); err != nil {
		return err
	}
	stopForConfigRestart(stderr)

	fmt.Fprintln(stdout, ok("remote access on: ")+bold("https://"+name))
	fmt.Fprintln(stdout, muted("pair a device: ")+bold("idea ui pair --qr"))
	return nil
}

// remoteDisable turns remote access off: clear allowed_hosts (the authoritative
// switch) and, best-effort, stop tailscale serve.
func remoteDisable(stdout, stderr io.Writer) error {
	stateRoot := daemon.DefaultStateRoot()
	cfg, err := daemon.LoadDaemonConfig(stateRoot)
	if err != nil {
		return err
	}
	cfg.AllowedHosts = nil
	if err := daemon.WriteDaemonConfig(stateRoot, cfg); err != nil {
		return err
	}
	if _, err := tailscaleRun("serve", "off"); err != nil {
		fmt.Fprintln(stderr, warn("note: `tailscale serve off`: "+strings.TrimSpace(err.Error())))
	}
	stopForConfigRestart(stderr)
	fmt.Fprintln(stdout, ok("remote access off"))
	return nil
}

// remoteStatus prints the current setup: allowed hosts, a non-loopback
// listener, and (best-effort) what tailscale serve says.
func remoteStatus(stdout io.Writer) error {
	cfg, err := daemon.LoadDaemonConfig(daemon.DefaultStateRoot())
	if err != nil {
		return err
	}
	if len(cfg.AllowedHosts) == 0 && cfg.Listen == "" {
		fmt.Fprintln(stdout, muted("remote access is off (loopback only)"))
		return nil
	}
	if len(cfg.AllowedHosts) > 0 {
		fmt.Fprintln(stdout, "allowed hosts: "+strings.Join(cfg.AllowedHosts, ", "))
	}
	if cfg.Listen != "" {
		fmt.Fprintln(stdout, "listen: "+cfg.Listen)
	}
	if out, err := tailscaleRun("serve", "status"); err == nil {
		if s := strings.TrimSpace(out); s != "" {
			fmt.Fprintln(stdout, muted(s))
		}
	}
	return nil
}

// tailscaleMagicDNSName returns the machine's MagicDNS name
// (laptop.tail1234.ts.net) from `tailscale status --json`.
func tailscaleMagicDNSName() (string, error) {
	out, err := tailscaleRun("status", "--json")
	if err != nil {
		return "", fmt.Errorf("is tailscale installed and signed in? run `tailscale up` (%w)", err)
	}
	var st struct {
		Self struct {
			DNSName string `json:"DNSName"`
		} `json:"Self"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return "", fmt.Errorf("parse `tailscale status --json`: %w", err)
	}
	name := strings.TrimSuffix(st.Self.DNSName, ".")
	if name == "" || !strings.HasSuffix(name, ".ts.net") {
		return "", fmt.Errorf("no MagicDNS name: enable MagicDNS (https://tailscale.com/kb/1081) so `tailscale serve` has a name to serve on")
	}
	return name, nil
}

// stopForConfigRestart stops a running daemon so its next start re-reads
// config.yaml. "Already stopped" is the common, harmless case; a real failure
// is a warning, not an error, because the config is already written and only a
// stale daemon is left serving it.
func stopForConfigRestart(stderr io.Writer) {
	_, err := stopDaemon()
	if err == nil {
		return
	}
	var ce *cliError
	if errors.As(err, &ce) && ce.kind == kindDaemonUnavailable {
		return
	}
	fmt.Fprintln(stderr, warn("note: "+err.Error()))
}
