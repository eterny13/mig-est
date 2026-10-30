// Package version holds build-time version information.
package version

import "fmt"

// These values are overridden at build time with -ldflags.
var (
	Version = "0.1.0-dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// String returns the short version string.
func String() string { return Version }

// Full returns a human-readable version, commit, and build date.
func Full() string {
	return fmt.Sprintf("mig-est %s (commit %s, built %s)", Version, Commit, Date)
}
