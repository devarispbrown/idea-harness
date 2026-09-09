package cli

import (
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/devarispbrown/idea-harness/internal/config"
)

// keyFindings reports what the DAEMON can see about the API keys the configured
// models need.
//
// The whole point is which process answers. `keyProblems` in internal/config
// resolves the key with os.Getenv, so it reports whatever process calls it --
// and doctor is the CLI. The daemon reads its environment once, at start, so a
// key exported afterwards is invisible to it while being perfectly visible to
// doctor. That is the documented failure mode four documents credit doctor with
// catching, and it reported `all checks passed`.
//
// Three states, and they need different things said:
//
//   - the daemon has the key: nothing to report, even if this shell does not.
//     doctor used to warn here, which is a false positive on the reverse setup.
//   - neither has it: the plain "no key" case.
//   - the daemon does not and this shell (or ./.env) does: the daemon is stale.
//     `idea daemon stop` fixes it and nothing else does.
//
// Only providers the local config actually routes to are considered. A machine
// with six provider keys exported has six ready presets, none of which the
// project uses.
func keyFindings(view cliProvidersView, referenced map[string]string, dotEnv map[string]string) (stale []string, missing []string) {
	byName := map[string]cliProvider{}
	for _, p := range view.Providers {
		byName[p.Name] = p
	}
	for provider, field := range referenced {
		p, ok := byName[provider]
		if !ok || p.KeyEnv == "" {
			// An unknown provider and a keyless one are both structural, and
			// internal/config reports them from the file, which is the right
			// place: they do not depend on anyone's environment.
			continue
		}
		if p.KeySet {
			continue
		}
		// `problem` means the entry was refused -- a bad base_url, say. The key
		// is not why it is unusable, and `idea daemon stop` would change
		// nothing, so this is somebody else's finding.
		if p.Problem != "" {
			continue
		}
		switch {
		case os.Getenv(p.KeyEnv) != "":
			stale = append(stale, fmt.Sprintf("%s (%s, from %s)", p.KeyEnv, provider, field))
		case dotEnv[p.KeyEnv] != "":
			stale = append(stale, fmt.Sprintf("%s (%s, from %s, set in ./.env)", p.KeyEnv, provider, field))
		default:
			missing = append(missing, fmt.Sprintf("%s (%s, from %s)", p.KeyEnv, provider, field))
		}
	}
	return stale, missing
}

// renderKeyFindings prints them and reports whether doctor should fail.
func renderKeyFindings(stale, missing []string, stdout io.Writer) error {
	for _, s := range stale {
		fmt.Fprintln(stdout, errText("✗ key")+" "+muted("the daemon does not have ")+code(s)+muted(", but you do"))
		fmt.Fprintln(stdout, muted("  It reads the environment once, at start, so a key exported afterwards is"))
		fmt.Fprintln(stdout, muted("  invisible to it. Every model call will fail with a 401 the run cannot retry."))
		fmt.Fprintln(stdout, muted("  Fix: ")+bold("idea daemon stop")+muted(" — the next command restarts it with the key."))
	}
	for _, m := range missing {
		fmt.Fprintln(stdout, errText("✗ key")+" "+muted("no ")+code(m)+muted(" anywhere"))
		fmt.Fprintln(stdout, muted("  A model is configured, so this is not demo mode: the first call fails with a"))
		fmt.Fprintln(stdout, muted("  401 the run cannot retry. Export it, then ")+bold("idea daemon stop")+
			muted(" — or point that field at a provider you have a key for."))
	}
	if len(stale)+len(missing) > 0 {
		return fmt.Errorf("the daemon is missing an API key the configured model needs; see above")
	}
	return nil
}

// referencedProviders is every provider the project's own config routes to:
// the default model, each role override, and the coding executor's model.
//
// The executor's is the one to remember. It is written by
// `idea executors setup --model`, and unlike `idea model set` that path has no
// daemon-side warning at all, so its key check exists nowhere else.
//
// Scoped to the config on purpose: a developer machine with six provider keys
// exported has six ready presets, and none of them is what this project uses.
func referencedProviders(h config.HarnessConfig) map[string]string {
	out := map[string]string{}
	// First writer wins, and the order is fixed: default_model, then roles in
	// sorted order, then the executor. A map keyed by provider with
	// last-writer-wins gave a different "from …" attribution on each run --
	// Go randomises map iteration -- and default_model, the most important
	// site, was always overwritten because it was added first.
	add := func(spec, field string) {
		provider, _ := config.ParseModelSpec(spec)
		if provider == "" {
			return
		}
		if _, seen := out[provider]; !seen {
			out[provider] = field
		}
	}
	add(h.DefaultModel, "default_model")
	roles := make([]string, 0, len(h.Models))
	for role := range h.Models {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	for _, role := range roles {
		add(h.Models[role], "models."+role)
	}
	add(h.Executor.Model, "executor.model")
	return out
}

// doctorConfigProblems is Config.ProblemsWithEnv minus the checks that depend on
// an environment, because doctor is the wrong process to answer those.
//
// Everything structural stays: an unparseable spec, a bare default_model, an
// unknown provider, a refused entry, a provider with no api_key_env at all.
// Those read the file, and the CLI is the right process to read a file. Only
// "is this key present" moves to the daemon, which is the one process whose
// answer decides whether the next model call succeeds.
//
// Only while a daemon is running. With none, `os.Getenv` IS the environment the
// daemon will get -- the next command starts it from this shell -- so the CLI is
// the authoritative process and dropping its answer loses a correct one. That is
// also precisely the state doctor's own remedy creates: it says
// `idea daemon stop`, and re-running doctor to confirm must not answer with
// silence.
func doctorConfigProblems(cfg config.Config, dotEnv map[string]string, daemonUp bool) []config.Problem {
	out := cfg.ProblemsWithEnv(dotEnv)
	if !daemonUp {
		return out
	}
	kept := make([]config.Problem, 0, len(out))
	for _, p := range out {
		if isEnvKeyProblem(p) {
			continue
		}
		kept = append(kept, p)
	}
	return kept
}

// isEnvKeyProblem is the one class whose answer depends on which process asked.
// The sibling it must NOT match is the keyless-provider warning, which is
// structural -- it reads the file -- and stays.
func isEnvKeyProblem(p config.Problem) bool { return p.EnvDependent }
