package cli

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/devarispbrown/idea-harness/internal/config"
)

// validateConfig loads the portable config under root and renders its problems.
// It returns an error only when a problem is error-severity, so a warning (an
// unset API key — demo mode is a supported way to run) still exits 0.
//
// config.Load keeps loading a config it disagrees with, so this reports on a
// wrong config rather than refusing to open it (§D5).
//
// TODO(track-D): wire this into cmdConfig (internal/cli/cli.go:343) — replace
// the body of the `validate` branch with:
//
//	return validateConfig(cwd, stdout)
func validateConfig(root string, stdout io.Writer) error {
	cfg, err := config.Load(filepath.Join(root, config.Dir))
	if err != nil {
		return err
	}
	problems := cfg.ProblemsWithEnv(config.LoadDotEnv(root))
	renderConfigProblems(problems, stdout)
	if config.HasErrors(problems) {
		return fmt.Errorf("config: %d problem(s) must be fixed", countSeverity(problems, config.SeverityError))
	}
	return nil
}

// renderConfigProblems writes one line per problem, errors first so the thing
// that must be fixed is not buried under demo-mode warnings.
func renderConfigProblems(problems []config.Problem, stdout io.Writer) {
	if len(problems) == 0 {
		fmt.Fprintln(stdout, ok("config: valid"))
		return
	}
	for _, sev := range []config.Severity{config.SeverityError, config.SeverityWarn} {
		for _, p := range problems {
			if p.Severity != sev {
				continue
			}
			label := warn("warn ")
			if sev == config.SeverityError {
				label = errText("error")
			}
			fmt.Fprintf(stdout, "%s  %s: %s\n", label, code(p.Field), p.Message)
		}
	}
	if !config.HasErrors(problems) {
		fmt.Fprintln(stdout, ok("config: valid (with warnings)"))
	}
}

func countSeverity(problems []config.Problem, sev config.Severity) int {
	n := 0
	for _, p := range problems {
		if p.Severity == sev {
			n++
		}
	}
	return n
}
