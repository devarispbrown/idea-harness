// Command idea is the idea-harness CLI/daemon entrypoint.
package main

import (
	"fmt"
	"os"

	"github.com/idea-harness/idea-harness/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "idea:", err)
		os.Exit(1)
	}
}
