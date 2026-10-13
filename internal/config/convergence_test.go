package config

import "testing"

// The defaults are stated once, in Effective, and docs/configuration.md
// quotes them; this pins the two together.
func TestConvergenceDefaults(t *testing.T) {
	c := ConvergenceConfig{}.Effective()
	if c.Mode != "guarded" || c.GateSeverity != "high" || !c.CitationRequired() || c.LaterFindings != "changed" || c.StallCycles != 1 {
		t.Fatalf("defaults drifted: %+v (require_citation=%v)", c, c.CitationRequired())
	}
	if c.Legacy() {
		t.Fatal("the default must be guarded")
	}
}

// require_citation is a pointer so an explicit false survives defaulting.
func TestConvergenceExplicitFalseCitationSurvives(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "harness.yaml", "review:\n  convergence:\n    require_citation: false\n    stall_cycles: 2\n")
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	eff := c.Harness.Review.Convergence.Effective()
	if eff.CitationRequired() || eff.StallCycles != 2 {
		t.Fatalf("explicit values lost: %+v", eff)
	}
	if c.Harness.Review.MaxCycles != 3 {
		t.Fatalf("a convergence block must not disturb the max_cycles default, got %d", c.Harness.Review.MaxCycles)
	}
}

func TestConvergenceRejectsUnknownValues(t *testing.T) {
	for _, body := range []string{
		"review:\n  convergence:\n    mode: lenient\n",
		"review:\n  convergence:\n    gate_severity: medium\n",
		"review:\n  convergence:\n    later_findings: never\n",
		"review:\n  convergence:\n    stall_cycles: -1\n",
		"review:\n  convergence:\n    bogus: 1\n",
	} {
		dir := t.TempDir()
		writeFile(t, dir, "harness.yaml", body)
		if _, err := Load(dir); err == nil {
			t.Errorf("accepted %q", body)
		}
	}
}
