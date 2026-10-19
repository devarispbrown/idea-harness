package cli

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/devarispbrown/idea-harness/internal/config"
)

// cmdKey sets and lists the API keys the model providers read. Keys are never
// written to harness.yaml or shown back: `set` prompts (or reads --stdin) and
// writes them to the project's .env, then reloads the daemon so the next run
// picks them up without a restart.
func cmdKey(a cmdArgs, stdout, stderr io.Writer) error {
	switch a.sub {
	case "set":
		return keySet(a, stdout, stderr)
	default: // "list" or bare `idea key`
		return keyList(a, stdout)
	}
}

func keyProvider(c *client, projectID, name string) (cliProvider, error) {
	var v cliProvidersView
	if err := c.do("GET", "/api/v1/projects/"+url.PathEscape(projectID)+"/providers", nil, &v); err != nil {
		return cliProvider{}, err
	}
	for _, p := range v.Providers {
		if p.Name == name {
			return p, nil
		}
	}
	var names []string
	for _, p := range v.Providers {
		names = append(names, p.Name)
	}
	return cliProvider{}, errorf(kindNotFound, "no provider matching %q; configured: %s", name, strings.Join(names, ", "))
}

func keySet(a cmdArgs, stdout, stderr io.Writer) error {
	provider := a.arg(0)
	if provider == "" {
		return usageErrorf("usage: idea key set <provider> [--stdin]")
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	p, err := keyProvider(c, projectID, provider)
	if err != nil {
		return err
	}
	if p.KeyEnv == "" {
		fmt.Fprintln(stdout, ok("✓ "+p.Label)+muted(" needs no API key (local provider)"))
		return nil
	}
	// Read the secret: --stdin for scripts (never argv, so it cannot leak into
	// shell history), otherwise a hidden terminal prompt.
	var value string
	if a.has("--stdin") {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		value = strings.TrimSpace(string(b))
	} else if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintf(stderr, "Paste %s (input is hidden): ", p.KeyEnv)
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(stderr)
		if err != nil {
			return err
		}
		value = strings.TrimSpace(string(b))
	} else {
		return usageErrorf("stdin is not a terminal; pass --stdin to read the key from stdin")
	}
	if value == "" {
		return usageErrorf("no key provided")
	}

	cwd, _ := os.Getwd()
	if err := config.SetDotEnv(cwd, p.KeyEnv, value); err != nil {
		return err
	}
	// The daemon caches .env per project and reloads it only when harness.yaml
	// changes (maybeAutoReload). Bump its mtime so the next request re-reads the
	// key — no restart, and no running initiative is disturbed.
	harness := filepath.Join(cwd, config.Dir, "harness.yaml")
	now := time.Now()
	_ = os.Chtimes(harness, now, now)

	// Verify the daemon now sees it. This request is what actually triggers the
	// reload, so it also confirms the write landed.
	fresh, err := keyProvider(c, projectID, provider)
	if err != nil {
		return err
	}
	if !fresh.KeySet {
		fmt.Fprintln(stdout, warn("⚠ the daemon still does not see "+p.KeyEnv+"."))
		fmt.Fprintln(stdout, muted("  Run ")+bold("idea daemon stop")+muted(" and then any command to restart it with the key."))
		return nil
	}
	fmt.Fprintln(stdout, ok("✓ "+p.KeyEnv)+muted(" set in .env — the daemon sees it"))
	return nil
}

func keyList(a cmdArgs, stdout io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	var v cliProvidersView
	if err := c.do("GET", "/api/v1/projects/"+url.PathEscape(projectID)+"/providers", nil, &v); err != nil {
		return err
	}
	fmt.Fprintln(stdout, heading("API keys"))
	found := false
	for _, p := range v.Providers {
		if p.KeyEnv == "" {
			continue
		}
		found = true
		state := muted("unset")
		if p.KeySet {
			state = ok("set")
		}
		fmt.Fprintf(stdout, "  %-24s %s %s\n", code(p.KeyEnv), state, muted("("+p.Label+")"))
	}
	if !found {
		fmt.Fprintln(stdout, muted("  no keyed providers configured"))
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, muted("set one: ")+bold("idea key set <provider>")+muted("  (prompts for the key, writes it to .env, never shows it)"))
	return nil
}
