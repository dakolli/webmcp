// Package version reports the build identity stamped in at release time.
package version

import "fmt"

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String returns the build identity as "<version> (<commit>, <date>)".
func String() string {
	return fmt.Sprintf("%s (%s, %s)", Version, Commit, Date)
}
