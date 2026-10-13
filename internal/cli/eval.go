package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/devarispbrown/idea-harness/internal/eval"
)

// evalFlags is `idea eval`'s flag spec, kept beside the command so the table
// row in cli.go stays one line.
var evalFlags = flagSpec{
	"--suite":    flagValue,
	"--case":     flagRepeated,
	"--model":    flagValue,
	"--policy":   flagValue,
	"--harness":  flagValue,
	"--json-out": flagValue,
	"--md-out":   flagValue,
	"--baseline": flagValue,
	"--timeout":  flagValue,
	"--keep":     flagBool,
}

// cmdEval runs an eval suite end to end on an in-process daemon of its own
// (its own state root and project directories, never the user's daemon or
// projects), prints the markdown report, and optionally writes JSON/markdown
// files and compares against a saved baseline.
//
// It needs no running daemon, which is why it does not go through the
// client: a measurement that depended on whatever the user's daemon had
// loaded -- its config generation, its demo queue, its environment -- would
// not be reproducible.
//
// Exit status: non-zero when a scripted case misses its expectation, or when
// the run regresses against --baseline. A real-model run that merely parks
// for a human is a measurement, not a failure.
func cmdEval(a cmdArgs, stdout, stderr io.Writer) error {
	// maxPositional's zero means "unbounded", so "takes none" is checked
	// here: `idea eval todo-cli` must not silently run the whole suite.
	if len(a.positional) > 0 {
		return fmt.Errorf("eval takes no arguments (got %q); name cases with --case", strings.Join(a.positional, " "))
	}
	suite, err := eval.LoadSuite(a.flags.value("--suite"))
	if err != nil {
		return err
	}
	if suite, err = suite.Filter(a.flags.list("--case")); err != nil {
		return err
	}
	opts := eval.Options{
		Suite:  suite,
		Model:  strings.TrimSpace(a.flags.value("--model")),
		Policy: eval.Policy(a.flags.value("--policy")),
		Keep:   a.flags.has("--keep"),
	}
	if p := a.flags.value("--harness"); p != "" {
		data, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("--harness: %w", err)
		}
		opts.Harness = string(data)
	}
	if t := a.flags.value("--timeout"); t != "" {
		d, err := time.ParseDuration(t)
		if err != nil || d <= 0 {
			return fmt.Errorf("--timeout must be a duration such as 20m, got %q", t)
		}
		opts.CaseTimeout = d
	}
	if opts.Keep {
		dir, err := os.MkdirTemp("", "idea-eval-")
		if err != nil {
			return err
		}
		opts.WorkDir = dir
		fmt.Fprintln(stderr, muted("keeping every case's state and project under "+dir))
	}
	var base *eval.Report
	if p := a.flags.value("--baseline"); p != "" {
		if base, err = eval.LoadReport(p); err != nil {
			return fmt.Errorf("--baseline: %w", err)
		}
	}
	opts.Progress = func(c eval.Case, r *eval.CaseResult) {
		if r == nil {
			fmt.Fprintf(stderr, "%s %s …\n", muted("→"), c.Name)
			return
		}
		mark := ok("✓")
		if r.Error != "" || len(r.ExpectationFailures) > 0 {
			mark = warn("✗")
		}
		fmt.Fprintf(stderr, "%s %s: reached %s, %d human stop(s), %.1fs\n", mark, c.Name, r.Reached, len(r.HumanStops), r.WallSeconds)
	}
	if opts.Model == "" {
		fmt.Fprintln(stderr, muted("no --model: running the scripted provider (deterministic, no model calls); pass --model provider/model to measure a real one"))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	rep, err := eval.Run(ctx, opts)
	if err != nil {
		return err
	}
	if base != nil {
		rep.Baseline = a.flags.value("--baseline")
		if rep.Regressions, err = eval.Compare(base, rep); err != nil {
			return err
		}
	}

	if p := a.flags.value("--json-out"); p != "" {
		if err := writeReport(p, rep.WriteJSON); err != nil {
			return err
		}
	}
	if p := a.flags.value("--md-out"); p != "" {
		if err := writeReport(p, rep.WriteMarkdown); err != nil {
			return err
		}
	}
	if err := rep.WriteMarkdown(stdout); err != nil {
		return err
	}
	if rep.Failed() {
		return errors.New(failureSummary(rep))
	}
	return nil
}

func writeReport(path string, write func(io.Writer) error) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := write(f); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func failureSummary(rep *eval.Report) string {
	var parts []string
	if n := rep.Aggregate.ExpectationFailures; n > 0 {
		parts = append(parts, fmt.Sprintf("%d case(s) missed their scripted expectation", n))
	}
	if n := len(rep.Regressions); n > 0 {
		parts = append(parts, fmt.Sprintf("%d regression(s) against %s", n, rep.Baseline))
	}
	return "eval: " + strings.Join(parts, "; ")
}
