package cli

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata golden files")

// TestHelpOutputIsStable locks `idea help`.
//
// cli.go's inHelp field carries a comment saying it exists so help output "stays
// byte-for-byte what it has always been" -- but nothing enforced that, and this
// round changes it twice: `idea init` gained a directory argument, and `verify`
// advertises --fail/--waive. An invariant asserted only in a comment is not an
// invariant, so it gets a golden file. Run with -update-golden to accept a
// deliberate change.
func TestHelpOutputIsStable(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	initColor(&bytes.Buffer{})

	var out bytes.Buffer
	usage(&out)

	golden := filepath.Join("testdata", "help.txt")
	if *updateGolden {
		if err := os.WriteFile(golden, out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", golden)
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run: go test ./internal/cli -update-golden)", err)
	}
	if got := out.String(); got != string(want) {
		t.Errorf("`idea help` changed.\n--- want ---\n%s\n--- got ---\n%s\n"+
			"If deliberate: go test ./internal/cli -update-golden", want, got)
	}
}
