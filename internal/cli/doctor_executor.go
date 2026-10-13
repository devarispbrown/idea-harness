package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/workflow"
)

// doctorExecutor reports which executor the next run would use. When
// executor.kind is unset (auto) and the run would fall back to the
// placeholder, that is printed as loudly as anything doctor says: a project in
// that state "passes" every other check and writes no real code.
//
// It is a warning, not a failure. The placeholder is a legitimate way to try
// the product, and doctor's exit code is what scripts gate an install on.
//
// The daemon's answer is used when there is one (its environment and .env
// are what a run uses); otherwise this shell's, said so.
func doctorExecutor(cfg config.Config, cfgErr error, dotEnv map[string]string, c *client, projectID string, stdout io.Writer) {
	if cfgErr != nil {
		return
	}
	var res *workflow.ExecutorResolution
	source := "this shell"
	if c != nil {
		// With a daemon running, only its answer counts: the key half of the
		// decision depends on its environment, not this shell's -- the same
		// reason doctor's key check asks it (doctor_keys.go).
		var execs []cliExecutor
		if err := c.do("GET", "/api/v1/projects/"+projectID+"/executors", nil, &execs); err == nil {
			for _, e := range execs {
				if e.Resolution != nil {
					res, source = e.Resolution, "the daemon"
					break
				}
			}
		}
		if res == nil {
			fmt.Fprintln(stdout, muted("• executor: not checked — the daemon did not report which executor the next run uses"))
			return
		}
	}
	if res == nil {
		lookup := func(name string) string {
			if v := os.Getenv(name); v != "" {
				return v
			}
			return dotEnv[name]
		}
		r := workflow.ResolveExecutor(context.Background(), cfg.Harness, nil, lookup)
		res = &r
	}
	switch {
	case res.Warning != "":
		fmt.Fprintln(stdout, errText("⚠ executor: "+res.Warning))
	case res.Note != "":
		fmt.Fprintln(stdout, warn("• executor: ")+res.Note)
	case res.Requested != res.Resolved:
		fmt.Fprintln(stdout, ok("✓ executor")+" "+muted(fmt.Sprintf("%s → %s (%s), as resolved by %s", res.Requested, res.Resolved, res.Image, source)))
	default:
		fmt.Fprintln(stdout, ok("✓ executor")+" "+muted(res.Resolved))
	}
}
