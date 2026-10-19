package cli

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"
)

type cliProvider struct {
	Name       string   `json:"name"`
	Label      string   `json:"label"`
	Type       string   `json:"type"`
	BaseURL    string   `json:"base_url"`
	KeyEnv     string   `json:"key_env"`
	Ready      bool     `json:"ready"`
	KeySet     bool     `json:"key_set"`
	Configured bool     `json:"configured"`
	IsDefault  bool     `json:"is_default"`
	Models     []string `json:"models"`
	// Problem is non-empty when the daemon refused this entry (a preset whose
	// base_url was overridden, say). The key is then not why it is unusable,
	// and `idea daemon stop` would change nothing.
	Problem string `json:"problem,omitempty"`
}

type cliProvidersView struct {
	DefaultModel string            `json:"default_model"`
	Models       map[string]string `json:"models"`
	Demo         bool              `json:"demo"`
	Providers    []cliProvider     `json:"providers"`
}

// isDemoMode reports whether the project will produce synthetic (demo) output.
func isDemoMode(c *client, projectID string) bool {
	var v cliProvidersView
	if err := c.do("GET", "/api/v1/projects/"+projectID+"/providers", nil, &v); err != nil {
		return false
	}
	return v.Demo
}

// cmdModel lists or updates model providers.
func cmdModel(a cmdArgs, stdout, stderr io.Writer) error {
	// Each subcommand's flags come from its own spec in the table. A single
	// union spec would have accepted `idea model ls --key-env FOO`, which is the
	// same silent-swallow defect one level down; and the old hand-rolled scans
	// dropped a misspelled --base_url along with its value, writing a provider
	// with no endpoint and reporting success (DX audit).
	switch a.sub {
	case "ls":
		if provider := a.arg(0); provider != "" {
			return listProviderModels(provider, stdout)
		}
		return listModels(a, stdout)
	case "set":
		spec := a.arg(0)
		if spec == "" {
			return usageErrorf("usage: idea model set <provider/model> [--role <role>]")
		}
		return setModel(spec, a.value("--role"), stdout)
	case "add":
		return addProvider(a, stdout)
	default: // "list" or bare `idea model`
		return listModels(a, stdout)
	}
}

// curatedModelCount is how many of a provider's models `idea model` shows by
// default. Printing every model of every provider (~8k tokens on a real run,
// once even a couple of presets carry a full catalog) is not what "list my
// model providers" needs most of the time -- the default view is the current
// selection plus this many of each provider's models, with a hint pointing at
// `--all` or a single provider for the rest.
const curatedModelCount = 5

// listProviderModels lists live models for one provider (falling back to the
// catalog when the provider cannot enumerate them).
func listProviderModels(provider string, stdout io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	var v struct {
		Provider string   `json:"provider"`
		Models   []string `json:"models"`
	}
	if err := c.do("GET", "/api/v1/projects/"+url.PathEscape(projectID)+"/models?"+url.Values{"provider": {provider}}.Encode(), nil, &v); err != nil {
		return err
	}
	fmt.Fprintln(stdout, heading("Models")+muted("  ("+provider+")"))
	if len(v.Models) == 0 {
		fmt.Fprintln(stdout, muted("  no models returned (provider may need an API key)"))
		return nil
	}
	for _, m := range v.Models {
		fmt.Fprintln(stdout, "  "+accent(provider+"/"+m))
	}
	fmt.Fprintln(stdout, muted("set one with: ")+bold("idea model set "+provider+"/<model>"))
	return nil
}

// listModels lists model providers. `--all` shows every model of every
// provider (the old, unconditional behaviour); a filter -- `--provider
// <name>` or a bare positional naming one, both accepted so `idea model list
// <provider>` reads the same as `idea model ls <provider>` -- narrows to one
// provider and shows it in full, since one provider's catalog is never the
// thing that made this command print ~8k tokens on a real run. Neither flag
// given is the default: the current selection (unchanged, already compact)
// plus curatedModelCount of each provider's models, with a hint for the rest
// (DX finding: `idea model` printed every model of every configured provider
// with no way to see less).
func listModels(a cmdArgs, stdout io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	var v cliProvidersView
	if err := c.do("GET", "/api/v1/projects/"+projectID+"/providers", nil, &v); err != nil {
		return err
	}

	all := a.has("--all")
	filter := a.value("--provider")
	if filter == "" {
		filter = a.arg(0)
	}
	providers := v.Providers
	if filter != "" {
		var kept []cliProvider
		for _, p := range v.Providers {
			if p.Name == filter {
				kept = append(kept, p)
			}
		}
		if len(kept) == 0 {
			names := make([]string, 0, len(v.Providers))
			for _, p := range v.Providers {
				names = append(names, p.Name)
			}
			return errorf(kindNotFound, "no provider matching %q; configured: %s", filter, strings.Join(names, ", "))
		}
		providers = kept
	}

	fmt.Fprintln(stdout, heading("Model providers"))
	if v.DefaultModel != "" {
		fmt.Fprintln(stdout, "  "+muted("default:")+" "+accent(v.DefaultModel))
	} else {
		fmt.Fprintln(stdout, "  "+muted("default:")+" "+muted("(auto — first configured provider)"))
	}
	if len(v.Models) > 0 {
		fmt.Fprintln(stdout, "  "+muted("per-role:"))
		roles := make([]string, 0, len(v.Models))
		for r := range v.Models {
			roles = append(roles, r)
		}
		sort.Strings(roles)
		for _, r := range roles {
			fmt.Fprintf(stdout, "    %s → %s\n", code(r), accent(v.Models[r]))
		}
	}
	fmt.Fprintln(stdout)
	truncated := false
	for _, p := range providers {
		status := muted("  ")
		if p.Ready {
			status = ok("✓ ")
		} else if p.Configured {
			status = warn("! ")
		}
		name := p.Label
		if p.Configured {
			name = bold(p.Label)
		}
		if p.IsDefault {
			name += " " + accent("(default)")
		}
		fmt.Fprintln(stdout, status+name)
		fmt.Fprintf(stdout, "      %s  %s\n", muted("type:"), p.Type)
		if p.BaseURL != "" {
			fmt.Fprintf(stdout, "      %s  %s\n", muted("base:"), p.BaseURL)
		}
		if p.KeyEnv != "" {
			// KeySet, not Ready. Ready also folds in whether the entry was
			// refused, so a preset with an overridden base_url and the key
			// present read as `unset` -- a plain false statement about the
			// operator's environment, at the one moment they most need to look
			// at base_url instead.
			keyState := muted("unset")
			if p.KeySet {
				keyState = ok("set")
			}
			fmt.Fprintf(stdout, "      %s  %s (%s)\n", muted("key:"), code(p.KeyEnv), keyState)
		} else {
			fmt.Fprintf(stdout, "      %s  %s\n", muted("key:"), muted("not required (local)"))
		}
		// The reason the entry was refused. docs/security.md promises this is
		// reported; the `!` marker alone says something is wrong and not what.
		if p.Problem != "" {
			fmt.Fprintf(stdout, "      %s  %s\n", warn("problem:"), p.Problem)
		}
		switch {
		case len(p.Models) == 0:
			// nothing to show
		case all || filter != "" || len(p.Models) <= curatedModelCount:
			fmt.Fprintf(stdout, "      %s  %s\n", muted("models:"), strings.Join(p.Models, ", "))
		default:
			truncated = true
			fmt.Fprintf(stdout, "      %s  %s %s\n", muted("models:"),
				strings.Join(p.Models[:curatedModelCount], ", "),
				muted(fmt.Sprintf("(+%d more)", len(p.Models)-curatedModelCount)))
		}
	}
	fmt.Fprintln(stdout)
	if truncated {
		fmt.Fprintln(stdout, muted("showing a curated set of models per provider — ")+
			bold("idea model list --all")+muted(" lists every model, ")+
			bold("idea model list <provider>")+muted(" lists one provider in full"))
	}
	fmt.Fprintln(stdout, muted("set the default: ")+bold("idea model set <provider/model>"))
	fmt.Fprintln(stdout, muted("set per-role:    ")+bold("idea model set <provider/model> --role <role>"))
	fmt.Fprintln(stdout, muted("add a provider:  ")+bold("idea model add <name> --type <type> --base-url <url> --key-env <ENV>"))
	fmt.Fprintln(stdout, muted("set an API key:  ")+bold("idea key set <provider>"))
	return nil
}

func setModel(spec, role string, stdout io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	body := map[string]any{}
	if role != "" {
		body["models"] = map[string]string{role: spec}
	} else {
		body["default_model"] = spec
	}
	var res struct {
		Warnings []string `json:"warnings"`
	}
	if err := c.do("PUT", "/api/v1/projects/"+projectID+"/providers", body, &res); err != nil {
		return err
	}
	for _, w := range res.Warnings {
		fmt.Fprintln(stdout, warn("⚠ ")+w)
	}
	if role != "" {
		fmt.Fprintln(stdout, ok("✓ model for role")+" "+code(role)+" "+muted("set to")+" "+accent(spec))
	} else {
		fmt.Fprintln(stdout, ok("✓ default model set to")+" "+accent(spec))
	}
	warnIfDaemonMissedTheKey(c, projectID, spec, stdout)
	return nil
}

// warnIfDaemonMissedTheKey reports the trap the quickstart used to walk readers
// straight into.
//
// The daemon reads the environment once, when it starts -- and `idea init`
// starts it. A key exported afterwards is invisible to it, so `idea model set`
// reported success and the first model call came back 401, which is not
// retryable, sending the initiative to failed.
//
// No cross-process introspection is needed: the CLI can read its own
// environment, and GET /providers already reports the daemon-side Ready and the
// KeyEnv name. Disagreement between those two facts is exactly this situation.
func warnIfDaemonMissedTheKey(c *client, projectID, spec string, stdout io.Writer) {
	provider, _, _ := strings.Cut(spec, "/")
	if provider == "" {
		return
	}
	var view cliProvidersView
	if err := c.do("GET", "/api/v1/projects/"+url.PathEscape(projectID)+"/providers", nil, &view); err != nil {
		return // advisory only; never fail the command the user asked for
	}
	for _, p := range view.Providers {
		// KeySet, not Ready. Ready also folds in whether the entry was refused,
		// so a preset with an overridden base_url and the key present read as
		// "not ready" and this told the user to restart the daemon -- a fix that
		// changes nothing about a bad base_url. That is the whole reason the
		// daemon reports key_set separately.
		if p.Name != provider || p.KeyEnv == "" || p.KeySet || p.Problem != "" {
			continue
		}
		if os.Getenv(p.KeyEnv) == "" {
			// The key is missing here too, which is the plain "no key" case that
			// idea doctor already reports. Name the fix so the very next thing
			// after `idea model set` is not a dead end.
			fmt.Fprintln(stdout, warn("⚠ "+p.KeyEnv+" is not set."))
			fmt.Fprintln(stdout, muted("  Set it with ")+bold("idea key set "+provider)+muted(" — it prompts, writes the project's .env, and never shows the value."))
			return
		}
		fmt.Fprintln(stdout, warn("⚠ the daemon does not have "+p.KeyEnv+", but your shell does."))
		fmt.Fprintln(stdout, muted("  It reads the environment once, at start, and `idea init` started it"))
		fmt.Fprintln(stdout, muted("  before you exported the key. Run ")+bold("idea daemon stop")+
			muted(" — the next command restarts it."))
		return
	}
}

func addProvider(a cmdArgs, stdout io.Writer) error {
	name := a.arg(0)
	if name == "" {
		return usageErrorf("usage: idea model add <name> [--type <type>] [--base-url <url>] [--key-env <ENV>]")
	}
	pc := map[string]any{}
	if v := a.value("--type"); v != "" {
		pc["type"] = v
	}
	if v := a.value("--base-url"); v != "" {
		pc["base_url"] = v
	}
	if v := a.value("--key-env"); v != "" {
		pc["api_key_env"] = v
	}
	// The daemon refuses an entry that resolves to no endpoint now, so this is a
	// nearer, friendlier message rather than the only thing standing between an
	// unusable entry and the first model call -- which is what the comment here
	// used to say, and what made it a client-side fix for a server-side hole.
	if pc["type"] == nil && pc["base_url"] == nil {
		return usageErrorf("provider %q needs --type (a known preset) or --base-url (a custom endpoint)", name)
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	body := map[string]any{"providers": map[string]any{name: pc}}
	if err := c.do("PUT", "/api/v1/projects/"+projectID+"/providers", body, nil); err != nil {
		return err
	}
	fmt.Fprintln(stdout, ok("✓ provider")+" "+code(name)+" "+muted("configured"))
	return nil
}

// showConfig renders the current model config.
func showConfig(stdout, stderr io.Writer) error {
	return listModels(cmdArgs{}, stdout)
}
