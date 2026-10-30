// Command mig-est diagnoses MySQL migrations without executing them.
package main

import (
	"os"

	"github.com/eterny13/mig-est/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
