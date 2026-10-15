package eval

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/daemon"
)

// RunScheduled is the daemon's scheduled eval (harness.yaml
// automations.eval): one capped run against a real model, its report written
// as <stamp>.json and <stamp>.md under req.ReportDir, compared with
// req.Baseline when there is one. The daemon registers it with
// daemon.SetEvalRunner; it lives here because this package drives a daemon
// in-process and so cannot be imported by it.
func RunScheduled(ctx context.Context, req daemon.EvalRequest) (daemon.EvalResult, error) {
	var out daemon.EvalResult
	if req.MaxCostUSD <= 0 {
		return out, fmt.Errorf("eval: a scheduled run needs a cost cap")
	}
	suitePath := ""
	if req.Suite != "" {
		suitePath = filepath.Join(req.ProjectRoot, req.Suite)
	}
	suite, err := LoadSuite(suitePath)
	if err != nil {
		return out, err
	}
	if suite, err = suite.Filter(req.Cases); err != nil {
		return out, err
	}
	rep, err := Run(ctx, Options{Suite: suite, Model: req.Model, Policy: PolicyApprove, MaxCostUSD: req.MaxCostUSD})
	if err != nil {
		return out, err
	}
	if req.Baseline != "" {
		if base, err := LoadReport(req.Baseline); err == nil {
			rep.Baseline = req.Baseline
			if regs, err := Compare(base, rep); err == nil {
				rep.Regressions = regs
			}
		}
	}
	out.CostUSD = rep.Aggregate.CostUSD
	out.StoppedAtCap = rep.StoppedAtCap
	for _, r := range rep.Regressions {
		out.Regressions = append(out.Regressions, fmt.Sprintf("%s %s: %s → %s", r.Case, r.Metric, r.Baseline, r.Current))
	}
	out.Summary = fmt.Sprintf("%d case(s): %d complete, %d human stop(s), %d error(s); $%.4f",
		rep.Aggregate.Cases, rep.Aggregate.Completed, rep.Aggregate.HumanStops, rep.Aggregate.Errors, rep.Aggregate.CostUSD)
	if n := len(rep.Regressions); n > 0 {
		out.Summary += fmt.Sprintf("; %d regression(s)", n)
	}
	stamp := strings.TrimSpace(req.Stamp)
	if stamp == "" {
		stamp = rep.StartedAt.UTC().Format("20060102T150405Z")
	}
	jsonPath := filepath.Join(req.ReportDir, stamp+".json")
	if err := writeReportFile(jsonPath, rep.WriteJSON); err != nil {
		return out, err
	}
	if err := writeReportFile(filepath.Join(req.ReportDir, stamp+".md"), rep.WriteMarkdown); err != nil {
		return out, err
	}
	out.ReportPath = jsonPath
	return out, nil
}

func writeReportFile(path string, write func(io.Writer) error) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := write(f); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
