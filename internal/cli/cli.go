// Package cli implements the mig-est command line interface.
package cli

import (
	"flag"
	"fmt"
	"io"

	"github.com/eterny13/mig-est/internal/version"
)

const usage = `mig-est - diagnose MySQL migrations before you run them

Usage:
  mig-est check   [flags] [migration.sql...]
  mig-est check   --file migration.sql [flags]
  mig-est check   --dir ./migrations [flags]
  mig-est check   --stdin [flags]
  mig-est record  [flags] [migration.sql...]
  mig-est record  --file migration.sql --actual 8m12s --table orders
  mig-est version
  mig-est help

Run "mig-est check -h" for all check flags.

mig-est never executes migrations. Online mode uses SELECT and SHOW only.
`

// Run dispatches a command and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "check":
		return runCheck(args[1:], stdout, stderr)
	case "record":
		return runRecord(args[1:], stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, version.Full())
		fmt.Fprintln(stdout, "MySQL support: 8.0.x and 8.4.x (InnoDB), read-only diagnostics")
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "mig-est: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

func fail(stderr io.Writer, format string, args ...any) int {
	fmt.Fprintf(stderr, "mig-est: "+format+"\n", args...)
	return 2
}

// parseFlagSet parses flags allowing intermixed positional arguments and flags.
func parseFlagSet(fs *flag.FlagSet, args []string, onPositional func(string)) ([]string, error) {
	var positional []string
	for len(args) > 0 {
		if args[0] == "--" {
			for _, arg := range args[1:] {
				positional = append(positional, arg)
				if onPositional != nil {
					onPositional(arg)
				}
			}
			break
		}
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		remaining := fs.Args()
		if len(remaining) == 0 {
			break
		}
		positional = append(positional, remaining[0])
		if onPositional != nil {
			onPositional(remaining[0])
		}
		args = remaining[1:]
	}
	return positional, nil
}
