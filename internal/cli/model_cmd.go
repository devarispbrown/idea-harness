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
func cmdModel(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list":
		return listModels(stdout)
	case "ls":
		if len(args) > 1 {
			return listProviderModels(args[1], stdout)
		}
		return listModels(stdout)
	case "set":
		if len(args) < 2 {
			return fmt.Errorf("usage: idea model set <provider/model> [--role <role>]")
		}
		spec := args[1]
		role := ""
		for i := 2; i < len(args); i++ {
			if args[i] == "--role" && i+1 < len(args) {
				role = args[i+1]
				i++
			}
		}
		return setModel(spec, role, stdout)
	case "add":
		return addProvider(args[1:], stdout)
	default:
		return fmt.Errorf("usage: idea model [list|ls <provider>|set <provider/model> [--role <role>]|add <name>]")
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

func addProvider(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: idea model add <name> [--type <type>] [--base-url <url>] [--key-env <ENV>]")
	}
	name := args[0]
	pc := map[string]any{}
	flags := args[1:]
	for i := 0; i < len(flags); i++ {
		switch flags[i] {
		case "--type":
			if i+1 < len(flags) {
				pc["type"] = flags[i+1]
				i++
			}
		case "--base-url":
			if i+1 < len(flags) {
				pc["base_url"] = flags[i+1]
				i++
			}
		case "--key-env":
			if i+1 < len(flags) {
				pc["api_key_env"] = flags[i+1]
				i++
			}
		}
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
