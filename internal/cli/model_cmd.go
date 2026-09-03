package cli

import (
	"fmt"
	"io"
	"net/url"
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
	Configured bool     `json:"configured"`
	IsDefault  bool     `json:"is_default"`
	Models     []string `json:"models"`
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
		return listModels(stdout)
	case "set":
		spec := a.arg(0)
		if spec == "" {
			return fmt.Errorf("usage: idea model set <provider/model> [--role <role>]")
		}
		return setModel(spec, a.value("--role"), stdout)
	case "add":
		return addProvider(a, stdout)
	default: // "list" or bare `idea model`
		return listModels(stdout)
	}
}

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

func listModels(stdout io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	var v cliProvidersView
	if err := c.do("GET", "/api/v1/projects/"+projectID+"/providers", nil, &v); err != nil {
		return err
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
	for _, p := range v.Providers {
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
			keyState := muted("unset")
			if p.Ready {
				keyState = ok("set")
			}
			fmt.Fprintf(stdout, "      %s  %s (%s)\n", muted("key:"), code(p.KeyEnv), keyState)
		} else {
			fmt.Fprintf(stdout, "      %s  %s\n", muted("key:"), muted("not required (local)"))
		}
		if len(p.Models) > 0 {
			fmt.Fprintf(stdout, "      %s  %s\n", muted("models:"), strings.Join(p.Models, ", "))
		}
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, muted("set the default: ")+bold("idea model set <provider/model>"))
	fmt.Fprintln(stdout, muted("set per-role:    ")+bold("idea model set <provider/model> --role <role>"))
	fmt.Fprintln(stdout, muted("add a provider:  ")+bold("idea model add <name> --type <type> --base-url <url> --key-env <ENV>"))
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
	if err := c.do("PUT", "/api/v1/projects/"+projectID+"/providers", body, nil); err != nil {
		return err
	}
	if role != "" {
		fmt.Fprintln(stdout, ok("✓ model for role")+" "+code(role)+" "+muted("set to")+" "+accent(spec))
	} else {
		fmt.Fprintln(stdout, ok("✓ default model set to")+" "+accent(spec))
	}
	return nil
}

func addProvider(a cmdArgs, stdout io.Writer) error {
	name := a.arg(0)
	if name == "" {
		return fmt.Errorf("usage: idea model add <name> [--type <type>] [--base-url <url>] [--key-env <ENV>]")
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
	// ValidateProvider returns nil early when base_url is empty, so the daemon
	// accepts an endpoint-less provider and the failure surfaces at the first
	// model call instead of here.
	if pc["type"] == nil && pc["base_url"] == nil {
		return fmt.Errorf("provider %q needs --type (a known preset) or --base-url (a custom endpoint)", name)
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
	return listModels(stdout)
}
