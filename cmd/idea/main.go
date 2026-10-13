// Command idea is the idea-harness CLI/daemon entrypoint.
package main

import (
	"os"

	"github.com/devarispbrown/idea-harness/internal/cli"
)

// main only exits. Classifying and rendering a failure is cli.Main's job, so
// the exit-code taxonomy (docs/cli.md, "Exit codes") lives in one package and
// is tested there.
func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
