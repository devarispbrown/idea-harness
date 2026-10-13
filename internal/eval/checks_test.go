package eval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Against a real coding agent the acceptance checks run in the project
// directory and a failing one is recorded with its output.
func TestChecksRunAgainstARealExecutor(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := &driver{
		opts:     Options{CheckTimeout: time.Minute},
		ctx:      context.Background(),
		res:      &CaseResult{},
		projDir:  dir,
		executor: "hermes",
		c: Case{Checks: []Check{
			{Name: "file exists", Run: "test -f main.go"},
			{Name: "fails", Run: "echo nope >&2; exit 3"},
		}},
	}
	d.runChecks()
	if d.res.ChecksSkipped || len(d.res.Checks) != 2 {
		t.Fatalf("checks = %+v", d.res.Checks)
	}
	if d.res.Checks[0].Status != CheckPassed || d.res.Checks[1].Status != CheckFailed || d.res.Checks[1].Output == "" {
		t.Fatalf("checks = %+v", d.res.Checks)
	}
}

// The internal executor writes a placeholder, so its checks are skipped --
// reported as skipped, never as passed.
func TestChecksSkippedForThePlaceholderExecutor(t *testing.T) {
	d := &driver{res: &CaseResult{}, executor: "internal", c: Case{Checks: []Check{{Name: "x", Run: "false"}}}}
	d.runChecks()
	if !d.res.ChecksSkipped || d.res.Checks[0].Status != CheckSkipped {
		t.Fatalf("checks = %+v", d.res.Checks)
	}
}

func TestMergeHarness(t *testing.T) {
	p := filepath.Join(t.TempDir(), "harness.yaml")
	if err := os.WriteFile(p, []byte("default_model: \"\"\nreview:\n  max_cycles: 3\n  rigor: auto\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mergeHarness(p, "default_model: x/y", "review:\n  convergence:\n    mode: legacy\n", ""); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	for _, want := range []string{"default_model: x/y", "max_cycles: 3", "mode: legacy"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("merged harness.yaml lost %q:\n%s", want, data)
		}
	}
}
