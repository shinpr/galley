package daemoncmd

import (
	"bytes"
	"errors"
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

func TestTimedOutStopIntentPreventsAnotherSignal(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "galley-daemon.pid")
	target := daemonctl.PIDFile{PID: 42, StartedAt: "first", TokenHash: "token"}
	path, leader, err := claimStopIntent(pidFile, target)
	if err != nil {
		t.Fatal(err)
	}
	if !leader {
		t.Fatal("first caller must own the stop intent")
	}
	if _, leader, err := claimStopIntent(pidFile, target); err != nil || leader {
		t.Fatalf("second claim = leader:%v err:%v, want follower", leader, err)
	}

	replacement := target
	replacement.TokenHash = "replacement"
	replacementPath, leader, err := claimStopIntent(pidFile, replacement)
	if err != nil {
		t.Fatal(err)
	}
	if !leader || replacementPath == path {
		t.Fatal("replacement daemon must have an independent stop intent")
	}
}

func TestStopTimeoutKeepsIntentForLaterNormalStop(t *testing.T) {
	root := t.TempDir()
	pidFile := filepath.Join(root, "galley-daemon.pid")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	meta := daemonctl.NewPIDFile(os.Getpid(), exe, root, []string{exe}).WithToken("timeout-test")
	if err := daemonctl.WritePID(pidFile, meta); err != nil {
		t.Fatal(err)
	}
	if err := daemonctl.Heartbeat(pidFile, meta); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	previous := stopVerifiedForCommand
	stopVerifiedForCommand = func(daemonctl.PIDFile, time.Duration) error {
		calls.Add(1)
		return errors.New("stop timed out")
	}
	defer func() { stopVerifiedForCommand = previous }()

	runStop := func() error {
		cmd := NewCommand("daemon")
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"--root", root, "--stop-timeout", "25ms", "stop"})
		return cmd.Execute()
	}
	if err := runStop(); err == nil {
		t.Fatal("first stop must report its timeout")
	}
	if err := runStop(); err == nil {
		t.Fatal("later stop must time out while observing the original shutdown")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("stop operation count = %d, want 1", got)
	}
}
