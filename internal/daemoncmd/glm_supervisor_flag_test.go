package daemoncmd

// Drives the real --supervisor flag over daemonconfig.SupervisorCLIs() so the
// flag can never reject a value the daemon core accepts (the glm drift).

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/daemonconfig"
)

func TestDaemonSupervisorFlagAcceptsEveryCanonicalValue(t *testing.T) {
	for _, supervisor := range daemonconfig.SupervisorCLIs() {
		supervisor := supervisor
		t.Run(supervisor, func(t *testing.T) {
			root := t.TempDir()
			if supervisor == "glm" { // glm needs a token to pass Preflight

				if err := os.WriteFile(filepath.Join(root, daemonconfig.Filename), []byte("glm_api_key: \"test-token\"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cmd := NewCommand("daemon")
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			// run --once on an empty queue exits cleanly iff the flag was accepted.
			cmd.SetArgs([]string{"--root", root, "run", "--supervisor", supervisor, "--once"})
			if err := cmd.Execute(); err != nil {
				t.Fatalf("--supervisor %s should be accepted end-to-end, got: %v", supervisor, err)
			}
		})
	}
}

func TestDaemonSupervisorFlagRejectsOffEnumValue(t *testing.T) {
	t.Parallel()
	cmd := NewCommand("daemon")
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--root", t.TempDir(), "run", "--supervisor", "bogus", "--once"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("off-enum --supervisor value must be rejected")
	}
	if !strings.Contains(err.Error(), "--supervisor must be one of") {
		t.Fatalf("rejection must name the accepted set, got: %v", err)
	}
	for _, supervisor := range daemonconfig.SupervisorCLIs() {
		if !strings.Contains(err.Error(), supervisor) {
			t.Fatalf("error message missing canonical value %q: %v", supervisor, err)
		}
	}
}
