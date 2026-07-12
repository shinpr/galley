package daemoncmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shinpr/galley/internal/daemonctl"
)

func TestRepeatedStopCommandsUseOneStopOperation(t *testing.T) {
	root := t.TempDir()
	pidFile := filepath.Join(root, "galley-daemon.pid")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	meta := daemonctl.NewPIDFile(os.Getpid(), exe, root, []string{exe}).WithToken("stop-test")
	if err := daemonctl.WritePID(pidFile, meta); err != nil {
		t.Fatal(err)
	}
	if err := daemonctl.Heartbeat(pidFile, meta); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	finish := make(chan struct{})
	allInspected := make(chan struct{})
	var calls atomic.Int32
	var inspected atomic.Int32
	previous := stopVerifiedForCommand
	previousInspectHook := afterInitialStopInspectForCommand
	afterInitialStopInspectForCommand = func() {
		if inspected.Add(1) == 4 {
			close(allInspected)
		}
	}
	stopVerifiedForCommand = func(got daemonctl.PIDFile, _ time.Duration) error {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-allInspected
		<-finish
		return daemonctl.RemovePID(pidFile, got.PID)
	}
	defer func() {
		stopVerifiedForCommand = previous
		afterInitialStopInspectForCommand = previousInspectHook
	}()

	runStop := func() error {
		cmd := NewCommand("daemon")
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"--root", root, "--stop-timeout", "5s", "stop"})
		return cmd.Execute()
	}

	leader := make(chan error, 1)
	go func() { leader <- runStop() }()
	<-entered

	const followers = 3
	followerResults := make(chan error, followers)
	var wg sync.WaitGroup
	for i := 0; i < followers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			followerResults <- runStop()
		}()
	}

	<-allInspected
	close(finish)
	if err := <-leader; err != nil {
		t.Fatalf("leader stop: %v", err)
	}
	wg.Wait()
	close(followerResults)
	for err := range followerResults {
		if err != nil {
			t.Fatalf("follower stop: %v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("stop operation count = %d, want 1", got)
	}
}

func TestStopLockReclaimsDeadOwner(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "galley-daemon.pid")
	owner := stopLockOwner{PID: 1 << 30, Claim: "dead"}
	data, err := json.Marshal(owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stopLockPath(pidFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stopLockOwnerPath(stopLockPath(pidFile)), data, 0o600); err != nil {
		t.Fatal(err)
	}
	release, err := acquireStopLock(pidFile, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
}

func TestStopLockDoesNotStealLiveOwner(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "galley-daemon.pid")
	release, err := acquireStopLock(pidFile, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(stopLockPath(pidFile), old, old); err != nil {
		t.Fatal(err)
	}
	stale, err := stopLockIsStale(stopLockPath(pidFile))
	if err != nil {
		t.Fatal(err)
	}
	if stale {
		t.Fatal("live lock must not become stale based on age")
	}
}
