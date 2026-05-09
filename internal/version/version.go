package version

import "fmt"

var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

func String() string {
	if Commit == "" || Commit == "unknown" {
		return fmt.Sprintf("galley %s", Version)
	}
	return fmt.Sprintf("galley %s (%s, %s)", Version, Commit, Date)
}
