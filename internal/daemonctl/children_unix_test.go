//go:build darwin || linux

package daemonctl

import (
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/shinpr/galley/internal/proc"
)

// TestCleanupRegisteredChildrenKillsLiveChildProcessGroup verifies force-stop
// kills and confirms a registered process group before returning.
func TestCleanupRegisteredChildrenKillsLiveChildProcessGroup(t *testing.T) {
	t.Parallel()
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not available")
	}
	cmd := exec.CommandContext(t.Context(), sleepPath, "60")
	// Match how proc.RunCommand puts subprocesses in their own pgid so the
	// pgid-targeted SIGKILL path is the one under test.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep child: %v", err)
	}
	// Reap the child concurrently so a SIGKILL'd process becomes a fully-gone
	// process group entry rather than a zombie that signal(0) would still see
	// as alive in its pgid. RunCommand does the same in its done channel.
	waited := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(waited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-waited
	})

	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		// Setpgid should make a group leader; fall back to the PID if Getpgid
		// still fails, matching proc.processGroupID.
		pgid = cmd.Process.Pid
	}

	registryPath := filepath.Join(t.TempDir(), "children.json")
	reg := proc.NewChildRegistry(registryPath)
	if err := reg.Register(proc.ChildRecord{
		PID:   cmd.Process.Pid,
		PGID:  pgid,
		Argv0: "sleep",
	}); err != nil {
		t.Fatalf("register child: %v", err)
	}

	survivors, err := CleanupRegisteredChildren(registryPath, 5*time.Second)
	if err != nil {
		t.Fatalf("CleanupRegisteredChildren: %v", err)
	}
	if len(survivors) != 0 {
		t.Fatalf("survivors got %d, want 0", len(survivors))
	}
	select {
	case <-waited:
	case <-time.After(time.Second):
		t.Fatal("child process not reaped after cleanup; pgid still alive")
	}
	if alive, _ := Alive(cmd.Process.Pid); alive {
		t.Fatal("child process still alive after force cleanup")
	}
}

// TestCleanupRegisteredChildrenKeepsGroupWithDeadLeaderButLiveDescendant covers
// the dead-leader-PID case: when the recorded leader PID has already exited
// (and been reaped) but a descendant is still alive in the same pgid,
// CleanupRegisteredChildren must not prune the record on the dead PID alone. It
// must instead SIGKILL the surviving process group and confirm it is gone.
func TestCleanupRegisteredChildrenKeepsGroupWithDeadLeaderButLiveDescendant(t *testing.T) {
	t.Parallel()
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not available")
	}

	// The leader establishes a fresh process group; the descendant joins that
	// same pgid (setpgid(0, leaderPID)) so the group outlives the leader.
	leader := exec.CommandContext(t.Context(), sleepPath, "60")
	leader.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := leader.Start(); err != nil {
		t.Fatalf("start leader: %v", err)
	}
	pgid, err := syscall.Getpgid(leader.Process.Pid)
	if err != nil {
		pgid = leader.Process.Pid
	}
	leaderPID := leader.Process.Pid

	descendant := exec.CommandContext(t.Context(), sleepPath, "60")
	descendant.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: pgid}
	if err := descendant.Start(); err != nil {
		_ = leader.Process.Kill()
		_, _ = leader.Process.Wait()
		t.Fatalf("start descendant: %v", err)
	}
	// Reap the descendant concurrently so a SIGKILL turns it into a fully-gone
	// process-group member rather than a zombie that signal(0) would still see.
	descendantDone := make(chan struct{})
	go func() {
		_, _ = descendant.Process.Wait()
		close(descendantDone)
	}()
	t.Cleanup(func() {
		_ = descendant.Process.Kill()
		<-descendantDone
	})

	// Kill and reap the leader so its PID is dead while the pgid stays alive
	// through the descendant.
	if err := leader.Process.Kill(); err != nil {
		t.Fatalf("kill leader: %v", err)
	}
	if _, err := leader.Process.Wait(); err != nil {
		t.Fatalf("reap leader: %v", err)
	}
	if alive, _ := Alive(leaderPID); alive {
		t.Skipf("leader pid %d unexpectedly still alive after reap", leaderPID)
	}
	if alive, _ := processGroupAlive(pgid); !alive {
		t.Skipf("process group %d unexpectedly gone before cleanup", pgid)
	}

	registryPath := filepath.Join(t.TempDir(), "children.json")
	reg := proc.NewChildRegistry(registryPath)
	if err := reg.Register(proc.ChildRecord{PID: leaderPID, PGID: pgid, Argv0: "sleep"}); err != nil {
		t.Fatalf("register child: %v", err)
	}

	survivors, err := CleanupRegisteredChildren(registryPath, 5*time.Second)
	if err != nil {
		t.Fatalf("CleanupRegisteredChildren: %v", err)
	}
	if len(survivors) != 0 {
		t.Fatalf("survivors got %d, want 0", len(survivors))
	}
	select {
	case <-descendantDone:
	case <-time.After(time.Second):
		t.Fatal("descendant not reaped after cleanup; process group still alive")
	}
	if alive, _ := processGroupAlive(pgid); alive {
		t.Fatal("process group still alive after force cleanup")
	}
}
