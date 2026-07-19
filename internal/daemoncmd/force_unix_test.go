//go:build darwin || linux || freebsd || netbsd || openbsd

package daemoncmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/shinpr/galley/internal/daemonctl"
	"github.com/shinpr/galley/internal/runner"
)

// spawnTrackedChild starts a long-running child in its own process group,
// registers it in the daemon-root child registry the same way runner.RunCommand
// does, and returns the child PID/PGID plus a channel that closes once the
// child has been reaped. reap controls whether the test reaps the child
// concurrently: when true, a SIGKILL'd child becomes fully gone (cleanup can
// confirm the group exited); when false, the SIGKILL'd child stays a zombie
// that signal(0) still sees alive in its pgid (cleanup must report incomplete).
func spawnTrackedChild(t *testing.T, root string, reap bool) (pid, pgid int, reaped <-chan struct{}) {
	t.Helper()
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not available")
	}
	child := exec.Command(sleepPath, "60")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatalf("start tracked child: %v", err)
	}
	done := make(chan struct{})
	if reap {
		go func() {
			_, _ = child.Process.Wait()
			close(done)
		}()
	} else {
		// Closed only by the cleanup below so callers can still select on it.
		// The zombie is reaped here, after the assertions have run.
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		if reap {
			<-done
		} else {
			_, _ = child.Process.Wait()
		}
	})
	pgid, err = syscall.Getpgid(child.Process.Pid)
	if err != nil {
		pgid = child.Process.Pid
	}
	registryPath := runner.ChildRegistryPath(root)
	if err := os.MkdirAll(filepath.Dir(registryPath), 0o700); err != nil {
		t.Fatalf("create registry dir: %v", err)
	}
	reg := runner.NewChildRegistry(registryPath)
	if err := reg.Register(runner.ChildRecord{PID: child.Process.Pid, PGID: pgid, Argv0: "sleep"}); err != nil {
		t.Fatalf("register tracked child: %v", err)
	}
	return child.Process.Pid, pgid, done
}

// TestStopForceCleansRegisteredChildProcessGroupBeforeRemovingPIDFile exercises
// AC-004 at the command level: galley daemon stop --force must SIGKILL a
// daemon-owned child process group recorded in the registry, wait until that
// group is confirmed gone, and only then remove the PID file. The test asserts
// the child is dead by the time the command returns and that the PID file is
// absent afterwards.
func TestStopForceCleansRegisteredChildProcessGroupBeforeRemovingPIDFile(t *testing.T) {
	root, pidFile, _ := startUnresponsiveDaemon(t)
	childPID, _, _ := spawnTrackedChild(t, root, true)

	cmd := NewCommand("daemon")
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetArgs([]string{"--root", root, "--stop-timeout", "2s", "stop", "--force"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("force stop: %v (stderr=%s)", err, errBuf.String())
	}
	if !strings.Contains(out.String(), "force stopped") {
		t.Fatalf("expected force-stop confirmation, got %q", out.String())
	}
	// The command must not have returned until the registered child group was
	// confirmed gone.
	if alive, _ := daemonctl.Alive(childPID); alive {
		t.Fatal("registered child still alive after force stop returned")
	}
	if _, statErr := os.Stat(pidFile); !os.IsNotExist(statErr) {
		t.Fatalf("PID file must be removed only after successful child cleanup, stat err=%v", statErr)
	}
	if _, statErr := os.Stat(runner.ChildRegistryPath(root)); !os.IsNotExist(statErr) {
		t.Fatalf("child registry must be cleared after successful cleanup, stat err=%v", statErr)
	}
}
