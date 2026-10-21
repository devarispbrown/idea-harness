package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/devarispbrown/idea-harness/internal/config"
)

// cmdEnv lists and sets the environment variables the execution plan declares
// its tasks' code will read. `idea env` lists them (aggregated across tasks,
// deduplicated by name); `idea env set <NAME>` prints how to obtain the value
// and its URL, prompts (or reads --stdin), writes the value to the project's
// .env, and reloads the daemon — the same write path `idea key set` uses for
// provider keys, so a value set here is never shown back and never lands in
// harness.yaml.
func cmdEnv(a cmdArgs, stdout, stderr io.Writer) error {
	if a.sub == "set" {
		return envSet(a, stdout, stderr)
	}
	return envList(a, stdout)
}

// envVarsForInitiative fetches an initiative's tasks and returns every declared
// env var, deduplicated by name (first task that declares it wins).
func envVarsForInitiative(c *client, projectID, iniID string) ([]cliEnvVar, error) {
	var tasks []cliTask
	if err := c.do("GET", initiativeResource(projectID, iniID, "tasks"), nil, &tasks); err != nil {
		return nil, err
	}
	var out []cliEnvVar
	seen := map[string]bool{}
	for _, t := range tasks {
		for _, v := range t.EnvVars {
			if v.Name == "" || seen[v.Name] {
				continue
			}
			seen[v.Name] = true
			out = append(out, v)
		}
	}
	return out, nil
}

func envList(a cmdArgs, stdout io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, a.positional)
	if err != nil {
		return err
	}
	vars, err := envVarsForInitiative(c, projectID, iniID)
	if err != nil {
		return err
	}
	if a.has("--json") {
		return dumpJSON(stdout, vars)
	}
	if len(vars) == 0 {
		fmt.Fprintln(stdout, muted("No environment variables declared — the plan's tasks read none."))
		return nil
	}
	fmt.Fprintln(stdout, heading("Required environment variables"))
	for _, v := range vars {
		fmt.Fprintf(stdout, "  %s\n", code(v.Name))
		if v.How != "" {
			fmt.Fprintf(stdout, "    %s\n", muted(v.How))
		}
		if v.URL != "" {
			fmt.Fprintf(stdout, "    %s\n", muted(v.URL))
		}
	}
	fmt.Fprintf(stdout, "\n%s\n", muted("Set one with: idea env set <NAME>"))
	return nil
}

func envSet(a cmdArgs, stdout, stderr io.Writer) error {
	name := strings.TrimSpace(a.arg(0))
	if name == "" {
		return usageErrorf("usage: idea env set <NAME> [--stdin]")
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	// The variable is declared by the current (newest) initiative's plan; the
	// value itself is project-wide (.env), so no initiative ref is taken here.
	iniID, err := resolveInitiative(c, projectID, nil)
	if err != nil {
		return err
	}
	vars, err := envVarsForInitiative(c, projectID, iniID)
	if err != nil {
		return err
	}
	var target *cliEnvVar
	for i := range vars {
		if vars[i].Name == name {
			target = &vars[i]
			break
		}
	}
	if target == nil {
		return errorf(kindNotFound, "no declared environment variable %q; `idea env` lists them", name)
	}
	// How + where, before the prompt, so the human has the instructions in
	// front of them while they go fetch the value.
	if target.How != "" {
		fmt.Fprintln(stderr, muted(target.How))
	}
	if target.URL != "" {
		fmt.Fprintln(stderr, muted(target.URL))
	}

	// Same read as `key set`: --stdin for scripts (never argv, so the value
	// cannot leak into shell history), otherwise a hidden terminal prompt.
	var value string
	if a.has("--stdin") {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		value = strings.TrimSpace(string(b))
	} else if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintf(stderr, "Paste %s (input is hidden): ", name)
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(stderr)
		if err != nil {
			return err
		}
		value = strings.TrimSpace(string(b))
	} else {
		return usageErrorf("stdin is not a terminal; pass --stdin to read the value from stdin")
	}
	if value == "" {
		return usageErrorf("no value provided")
	}

	cwd, _ := os.Getwd()
	if err := config.SetDotEnv(cwd, name, value); err != nil {
		return err
	}
	// Reload .env (the daemon caches it and only re-reads on harness.yaml
	// change): bump mtime so the next request picks the value up.
	harness := filepath.Join(cwd, config.Dir, "harness.yaml")
	now := time.Now()
	_ = os.Chtimes(harness, now, now)

	fmt.Fprintln(stdout, ok("✓ set "+name))
	return nil
}
