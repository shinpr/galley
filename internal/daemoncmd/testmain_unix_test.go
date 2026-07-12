//go:build !windows

package daemoncmd

import (
	"os"
	"testing"
)

// TestMain re-execs the package test binary as a lifecycle daemon when the
// lifecycle env flag is set. Otherwise it runs the package tests normally.
func TestMain(m *testing.M) {
	if os.Getenv(lifecycleDaemonEnv) == "1" {
		os.Exit(runLifecycleDaemon())
	}
	os.Exit(m.Run())
}
