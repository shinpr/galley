package daemonctl

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStopCoordinationPath(t *testing.T) {
	t.Parallel()
	if got := StopCoordinationPath("/tmp/galley-daemon.pid"); got != "/tmp/galley-daemon.pid.stop" {
		t.Fatalf("path got %q", got)
	}
	if got := StopCoordinationPath(""); got != "" {
		t.Fatalf("empty pid file should yield empty coord path, got %q", got)
	}
}

func TestCoordinatedStopVerifiedSendsSingleSignalForConcurrentCallers(t *testing.T) {
	// Not parallel: swaps package-level signalStopHook.
	meta, pidFile, cleanup := startStoppableDaemon(t)
	defer cleanup()

	var signals atomic.Int32
	restore := swapSignalStopHook(func(pid int) error {
		signals.Add(1)
		return signalStop(pid)
	})
	defer restore()

	const callers = 4
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- CoordinatedStopVerified(meta, 5*time.Second, pidFile)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !errors.Is(err, ErrNotRunning) {
			t.Fatalf("coordinated stop: %v", err)
		}
	}
	if got := signals.Load(); got != 1 {
		t.Fatalf("graceful signals got %d, want exactly 1", got)
	}
	alive, err := Alive(meta.PID)
	if err != nil {
		t.Fatal(err)
	}
	if alive {
		t.Fatal("daemon still alive after coordinated stops")
	}
	if _, err := os.Stat(StopCoordinationPath(pidFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("coordination file should be cleared after stop, err=%v", err)
	}
}

func TestCoordinatedStopVerifiedFollowerDoesNotResignalAfterLeaderTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows Stop is TerminateProcess; unresponsive graceful timeout is Unix-specific")
	}
	// Not parallel: swaps package-level signalStopHook.
	meta, pidFile, cleanup := startUnresponsiveStopDaemon(t)
	defer cleanup()

	var signals atomic.Int32
	restore := swapSignalStopHook(func(pid int) error {
		signals.Add(1)
		return signalStop(pid)
	})
	defer restore()

	if err := CoordinatedStopVerified(meta, 200*time.Millisecond, pidFile); err == nil {
		t.Fatal("expected leader stop timeout")
	}
	if got := signals.Load(); got != 1 {
		t.Fatalf("leader signals got %d, want 1", got)
	}
	// Coordination must remain so a later normal stop is a follower.
	if _, err := os.Stat(StopCoordinationPath(pidFile)); err != nil {
		t.Fatalf("coordination should remain after timeout: %v", err)
	}

	err := CoordinatedStopVerified(meta, 200*time.Millisecond, pidFile)
	if err == nil {
		t.Fatal("expected follower stop timeout against unresponsive daemon")
	}
	if got := signals.Load(); got != 1 {
		t.Fatalf("follower must not re-signal, signals=%d", got)
	}
}

func TestForceStopDuringCoordinatedGracefulDoesNotDoubleSignal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no graceful-then-force escalation path on Windows")
	}
	// Not parallel: swaps package-level signalStopHook.
	meta, pidFile, cleanup := startUnresponsiveStopDaemon(t)
	defer cleanup()

	var signals atomic.Int32
	restore := swapSignalStopHook(func(pid int) error {
		signals.Add(1)
		return signalStop(pid)
	})
	defer restore()

	leaderDone := make(chan error, 1)
	go func() {
		leaderDone <- CoordinatedStopVerified(meta, 2*time.Second, pidFile)
	}()
	// Wait until the leader has claimed and marked signaled.
	waitForStopCoordination(t, pidFile, 2*time.Second)

	forced, err := ForceStop(meta, 300*time.Millisecond, pidFile)
	if err != nil {
		t.Fatalf("force stop: %v", err)
	}
	if !forced {
		t.Fatal("expected force kill while graceful stop was in progress")
	}
	if got := signals.Load(); got != 1 {
		t.Fatalf("force must not send a second graceful signal, got %d", got)
	}
	select {
	case <-leaderDone:
	case <-time.After(3 * time.Second):
		t.Fatal("leader coordinated stop did not return after force kill")
	}
	alive, err := Alive(meta.PID)
	if err != nil {
		t.Fatal(err)
	}
	if alive {
		t.Fatal("daemon still alive after force stop")
	}
}

// TestForceStopAfterFollowerTimeoutDoesNotKillWhenProcessInfoUnavailable covers
// AC5 force escalation after a coordinated follower wait: a fresh heartbeat
// must not authorize SIGKILL when process-start identity cannot be confirmed
// (recycled-PID / ProcessInfo-unavailable hazard).
func TestForceStopAfterFollowerTimeoutDoesNotKillWhenProcessInfoUnavailable(t *testing.T) {
	// Not parallel: swaps package-level hooks.
	meta, pidFile, cleanup := startStoppableDaemon(t)
	defer cleanup()

	if err := Heartbeat(pidFile, meta); err != nil {
		t.Fatal(err)
	}
	if !Verify(meta, meta.Root, meta.Executable) {
		t.Fatal("precondition: fresh heartbeat should satisfy Verify")
	}
	// Signaled coordination makes ForceStop's graceful phase a follower.
	writeStopIntent(t, pidFile, stopIntent{
		TargetPID:        meta.PID,
		ProcessStartedAt: meta.ProcessStartedAt,
		TokenHash:        meta.TokenHash,
		Executable:       meta.Executable,
		Root:             meta.Root,
		CoordinatorPID:   os.Getpid(),
		StartedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		ClaimID:          "leader-signaled",
		Signaled:         true,
	})

	// Follower wait observes the target still "running" until timeout.
	prevRunning := targetStillRunningHook
	targetStillRunningHook = func(PIDFile) (bool, error) {
		return true, nil
	}
	defer func() { targetStillRunningHook = prevRunning }()

	// After timeout, force revalidation must fail closed — ProcessInfo unavailable
	// even though Verify would accept the fresh heartbeat.
	prevInfo := processInfoForTargetHook
	processInfoForTargetHook = func(int) (ProcessInfoResult, error) {
		return ProcessInfoResult{}, errors.New("process metadata unavailable")
	}
	defer func() { processInfoForTargetHook = prevInfo }()

	forced, err := ForceStop(meta, 50*time.Millisecond, pidFile)
	if forced {
		t.Fatal("force kill must not escalate when process identity is unverifiable")
	}
	if err == nil {
		t.Fatal("expected force stop to fail closed after follower timeout")
	}
	if !errors.Is(err, ErrUnverifiedProcess) {
		t.Fatalf("expected ErrUnverifiedProcess from force revalidation, got %v", err)
	}
	alive, aliveErr := Alive(meta.PID)
	if aliveErr != nil {
		t.Fatal(aliveErr)
	}
	if !alive {
		t.Fatal("process must not be killed when ProcessInfo is unavailable after follower timeout")
	}
}

// TestForceStopAfterFollowerTimeoutDoesNotKillRecycledStartIdentity covers the
// recycled-PID force path when ProcessInfo is available but reports a different
// start identity than the daemon metadata.
func TestForceStopAfterFollowerTimeoutDoesNotKillRecycledStartIdentity(t *testing.T) {
	// Not parallel: swaps package-level hooks.
	meta, pidFile, cleanup := startStoppableDaemon(t)
	defer cleanup()

	if err := Heartbeat(pidFile, meta); err != nil {
		t.Fatal(err)
	}
	writeStopIntent(t, pidFile, stopIntent{
		TargetPID:        meta.PID,
		ProcessStartedAt: meta.ProcessStartedAt,
		TokenHash:        meta.TokenHash,
		Executable:       meta.Executable,
		Root:             meta.Root,
		CoordinatorPID:   os.Getpid(),
		StartedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		ClaimID:          "leader-signaled-recycle",
		Signaled:         true,
	})

	prevRunning := targetStillRunningHook
	targetStillRunningHook = func(PIDFile) (bool, error) {
		return true, nil
	}
	defer func() { targetStillRunningHook = prevRunning }()

	// Process table reports a different start identity: PID was recycled.
	prevInfo := processInfoForTargetHook
	processInfoForTargetHook = func(pid int) (ProcessInfoResult, error) {
		return ProcessInfoResult{
			StartedAt:  "unrelated-recycled-process-start",
			Executable: meta.Executable,
			Command:    meta.Executable,
		}, nil
	}
	defer func() { processInfoForTargetHook = prevInfo }()

	forced, err := ForceStop(meta, 50*time.Millisecond, pidFile)
	if forced {
		t.Fatal("force kill must not target a recycled PID")
	}
	if err == nil {
		t.Fatal("expected force stop to fail closed for recycled start identity")
	}
	if !errors.Is(err, ErrUnverifiedProcess) {
		t.Fatalf("expected ErrUnverifiedProcess, got %v", err)
	}
	alive, aliveErr := Alive(meta.PID)
	if aliveErr != nil {
		t.Fatal(aliveErr)
	}
	if !alive {
		t.Fatal("recycled PID must not be killed")
	}
}

func TestCoordinatedStopVerifiedTableStaleAndIdentity(t *testing.T) {
	// Not parallel: table cases swap package-level signalStopHook.
	cases := []struct {
		name       string
		mutate     func(t *testing.T, pidFile string, meta PIDFile)
		wantSignal bool
	}{
		{
			name: "stale coordination for other pid is replaced",
			mutate: func(t *testing.T, pidFile string, meta PIDFile) {
				t.Helper()
				writeStopIntent(t, pidFile, stopIntent{
					TargetPID:        meta.PID + 100000,
					ProcessStartedAt: meta.ProcessStartedAt,
					TokenHash:        meta.TokenHash,
					Executable:       meta.Executable,
					Root:             meta.Root,
					CoordinatorPID:   os.Getpid(),
					StartedAt:        time.Now().UTC().Format(time.RFC3339Nano),
					Signaled:         true,
				})
			},
			wantSignal: true,
		},
		{
			name: "signaled coordination for matching identity is follower only",
			mutate: func(t *testing.T, pidFile string, meta PIDFile) {
				t.Helper()
				writeStopIntent(t, pidFile, stopIntent{
					TargetPID:        meta.PID,
					ProcessStartedAt: meta.ProcessStartedAt,
					TokenHash:        meta.TokenHash,
					Executable:       meta.Executable,
					Root:             meta.Root,
					CoordinatorPID:   -1,
					StartedAt:        time.Now().UTC().Format(time.RFC3339Nano),
					Signaled:         true,
				})
				// Deliver the one graceful signal as the "prior" leader would have.
				if err := signalStop(meta.PID); err != nil {
					t.Fatal(err)
				}
			},
			wantSignal: false,
		},
		{
			name: "unsignaled dead coordinator is reclaimed",
			mutate: func(t *testing.T, pidFile string, meta PIDFile) {
				t.Helper()
				writeStopIntent(t, pidFile, stopIntent{
					TargetPID:                   meta.PID,
					ProcessStartedAt:            meta.ProcessStartedAt,
					TokenHash:                   meta.TokenHash,
					Executable:                  meta.Executable,
					Root:                        meta.Root,
					CoordinatorPID:              1 << 30, // almost certainly not a live process
					CoordinatorProcessStartedAt: "dead-coordinator",
					StartedAt:                   time.Now().UTC().Format(time.RFC3339Nano),
					Signaled:                    false,
				})
			},
			wantSignal: true,
		},
		{
			name: "corrupt coordination is replaced",
			mutate: func(t *testing.T, pidFile string, _ PIDFile) {
				t.Helper()
				if err := os.WriteFile(StopCoordinationPath(pidFile), []byte("not-json"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantSignal: true,
		},
		{
			name: "process started_at mismatch is treated as recycled target pid",
			mutate: func(t *testing.T, pidFile string, meta PIDFile) {
				t.Helper()
				writeStopIntent(t, pidFile, stopIntent{
					TargetPID:        meta.PID,
					ProcessStartedAt: "not-the-real-start-time",
					TokenHash:        meta.TokenHash,
					Executable:       meta.Executable,
					Root:             meta.Root,
					CoordinatorPID:   os.Getpid(),
					StartedAt:        time.Now().UTC().Format(time.RFC3339Nano),
					Signaled:         true,
				})
			},
			wantSignal: true,
		},
		{
			name: "partial target identity is reclaimed",
			mutate: func(t *testing.T, pidFile string, meta PIDFile) {
				t.Helper()
				// Missing executable/root: incomplete publication must not
				// suppress a later normal stop.
				writeStopIntent(t, pidFile, stopIntent{
					TargetPID:      meta.PID,
					CoordinatorPID: 1 << 30,
					StartedAt:      time.Now().UTC().Format(time.RFC3339Nano),
					Signaled:       false,
				})
			},
			wantSignal: true,
		},
		{
			name: "unsignaled coordinator pid reuse is reclaimed",
			mutate: func(t *testing.T, pidFile string, meta PIDFile) {
				t.Helper()
				// CoordinatorPID is live (this test process) but the recorded
				// start identity is not ours: simulate PID recycle.
				writeStopIntent(t, pidFile, stopIntent{
					TargetPID:                   meta.PID,
					ProcessStartedAt:            meta.ProcessStartedAt,
					TokenHash:                   meta.TokenHash,
					Executable:                  meta.Executable,
					Root:                        meta.Root,
					CoordinatorPID:              os.Getpid(),
					CoordinatorProcessStartedAt: "not-this-process-start",
					StartedAt:                   time.Now().UTC().Format(time.RFC3339Nano),
					Signaled:                    false,
				})
			},
			wantSignal: true,
		},
		{
			name: "unsignaled expired lease is reclaimed while coordinator pid is live",
			mutate: func(t *testing.T, pidFile string, meta PIDFile) {
				t.Helper()
				// Live coordinator PID with matching start identity, but claim
				// age exceeds the unsignaled lease backstop.
				coordStarted := ""
				if info, err := ProcessInfo(os.Getpid()); err == nil {
					coordStarted = info.StartedAt
				}
				writeStopIntent(t, pidFile, stopIntent{
					TargetPID:                   meta.PID,
					ProcessStartedAt:            meta.ProcessStartedAt,
					TokenHash:                   meta.TokenHash,
					Executable:                  meta.Executable,
					Root:                        meta.Root,
					CoordinatorPID:              os.Getpid(),
					CoordinatorProcessStartedAt: coordStarted,
					StartedAt:                   time.Now().Add(-maxUnsignaledCoordinatorLease - time.Second).UTC().Format(time.RFC3339Nano),
					Signaled:                    false,
				})
			},
			wantSignal: true,
		},
		{
			name: "unsignaled missing started_at is reclaimed for live coordinator pid",
			mutate: func(t *testing.T, pidFile string, meta PIDFile) {
				t.Helper()
				// Live coordinator PID, no parseable StartedAt lease, no start
				// fence: must not own indefinitely.
				writeStopIntent(t, pidFile, stopIntent{
					TargetPID:        meta.PID,
					ProcessStartedAt: meta.ProcessStartedAt,
					TokenHash:        meta.TokenHash,
					Executable:       meta.Executable,
					Root:             meta.Root,
					CoordinatorPID:   os.Getpid(),
					StartedAt:        "",
					Signaled:         false,
				})
				// Backdate mtime so filesystem-time lease is also expired for
				// the missing StartedAt path.
				coordPath := StopCoordinationPath(pidFile)
				past := time.Now().Add(-maxUnsignaledCoordinatorLease - time.Second)
				if err := os.Chtimes(coordPath, past, past); err != nil {
					t.Fatal(err)
				}
			},
			wantSignal: true,
		},
		{
			name: "unsignaled invalid started_at is reclaimed for live coordinator pid",
			mutate: func(t *testing.T, pidFile string, meta PIDFile) {
				t.Helper()
				writeStopIntent(t, pidFile, stopIntent{
					TargetPID:        meta.PID,
					ProcessStartedAt: meta.ProcessStartedAt,
					TokenHash:        meta.TokenHash,
					Executable:       meta.Executable,
					Root:             meta.Root,
					CoordinatorPID:   os.Getpid(),
					StartedAt:        "not-a-timestamp",
					Signaled:         false,
				})
				coordPath := StopCoordinationPath(pidFile)
				past := time.Now().Add(-maxUnsignaledCoordinatorLease - time.Second)
				if err := os.Chtimes(coordPath, past, past); err != nil {
					t.Fatal(err)
				}
			},
			wantSignal: true,
		},
		{
			name: "unsignaled missing coordinator start identity with invalid started_at is reclaimed",
			mutate: func(t *testing.T, pidFile string, meta PIDFile) {
				t.Helper()
				// Live recycled-looking coordinator (this process) without start
				// fence and without parseable lease: reclaim immediately.
				writeStopIntent(t, pidFile, stopIntent{
					TargetPID:        meta.PID,
					ProcessStartedAt: meta.ProcessStartedAt,
					TokenHash:        meta.TokenHash,
					Executable:       meta.Executable,
					Root:             meta.Root,
					CoordinatorPID:   os.Getpid(),
					StartedAt:        "legacy",
					Signaled:         false,
				})
			},
			wantSignal: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta, pidFile, cleanup := startStoppableDaemon(t)
			defer cleanup()

			var signals atomic.Int32
			restore := swapSignalStopHook(func(pid int) error {
				signals.Add(1)
				return signalStop(pid)
			})
			defer restore()

			tc.mutate(t, pidFile, meta)
			if err := CoordinatedStopVerified(meta, 5*time.Second, pidFile); err != nil && !errors.Is(err, ErrNotRunning) {
				t.Fatalf("coordinated stop: %v", err)
			}
			got := signals.Load()
			if tc.wantSignal && got != 1 {
				t.Fatalf("signals got %d, want 1", got)
			}
			if !tc.wantSignal && got != 0 {
				t.Fatalf("signals got %d, want 0", got)
			}
		})
	}
}

func TestCoordinatedStopVerifiedMarkFailureDoesNotSignal(t *testing.T) {
	// Not parallel: swaps package-level hooks.
	meta, pidFile, cleanup := startStoppableDaemon(t)
	defer cleanup()

	var signals atomic.Int32
	restoreSignal := swapSignalStopHook(func(pid int) error {
		signals.Add(1)
		return signalStop(pid)
	})
	defer restoreSignal()

	prevMark := markStopSignaledHook
	markStopSignaledHook = func(string, stopIntent) error {
		return errors.New("injected mark failure")
	}
	defer func() { markStopSignaledHook = prevMark }()

	err := CoordinatedStopVerified(meta, time.Second, pidFile)
	if err == nil {
		t.Fatal("expected mark failure to abort coordinated stop")
	}
	if got := signals.Load(); got != 0 {
		t.Fatalf("mark failure must not deliver a graceful signal, got %d", got)
	}
	// Claim must be released so a later stop can reclaim.
	if _, statErr := os.Stat(StopCoordinationPath(pidFile)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed mark must clear coordination, err=%v", statErr)
	}

	// Recovery path: a subsequent stop without the injected failure succeeds.
	markStopSignaledHook = prevMark
	if err := CoordinatedStopVerified(meta, 5*time.Second, pidFile); err != nil && !errors.Is(err, ErrNotRunning) {
		t.Fatalf("recovery stop: %v", err)
	}
	if got := signals.Load(); got != 1 {
		t.Fatalf("recovery signals got %d, want 1", got)
	}
}

func TestCheckTargetStillMatchesFailsClosedWhenProcessInfoUnavailable(t *testing.T) {
	// Not parallel: swaps processInfoForTargetHook.
	meta, pidFile, cleanup := startStoppableDaemon(t)
	defer cleanup()

	// Fresh heartbeat would make Verify succeed; checkTargetStillMatches must
	// still fail closed when process metadata is unavailable.
	if err := Heartbeat(pidFile, meta); err != nil {
		t.Fatal(err)
	}
	if !Verify(meta, meta.Root, meta.Executable) {
		t.Fatal("precondition: fresh heartbeat should verify")
	}
	prev := processInfoForTargetHook
	processInfoForTargetHook = func(int) (ProcessInfoResult, error) {
		return ProcessInfoResult{}, errors.New("process metadata unavailable")
	}
	defer func() { processInfoForTargetHook = prev }()

	if err := checkTargetStillMatches(meta); !errors.Is(err, ErrUnverifiedProcess) {
		t.Fatalf("expected ErrUnverifiedProcess when ProcessInfo fails, got %v", err)
	}
}

func TestCoordinatedStopVerifiedProcessInfoUnavailableDoesNotSignal(t *testing.T) {
	// Not parallel: swaps package-level hooks.
	meta, pidFile, cleanup := startStoppableDaemon(t)
	defer cleanup()

	var signals atomic.Int32
	restoreSignal := swapSignalStopHook(func(pid int) error {
		signals.Add(1)
		return signalStop(pid)
	})
	defer restoreSignal()

	prev := processInfoForTargetHook
	processInfoForTargetHook = func(int) (ProcessInfoResult, error) {
		return ProcessInfoResult{}, errors.New("process metadata unavailable")
	}
	defer func() { processInfoForTargetHook = prev }()

	err := CoordinatedStopVerified(meta, time.Second, pidFile)
	if !errors.Is(err, ErrUnverifiedProcess) && !errors.Is(err, ErrNotRunning) {
		t.Fatalf("expected identity failure when ProcessInfo unavailable, got %v", err)
	}
	if got := signals.Load(); got != 0 {
		t.Fatalf("ProcessInfo failure must not authorize a signal, got %d", got)
	}
}

func TestCoordinatedStopVerifiedProcessInfoUnavailableRecycledPIDDoesNotSignal(t *testing.T) {
	// Not parallel: swaps package-level hooks.
	meta, pidFile, cleanup := startStoppableDaemon(t)
	defer cleanup()

	// Fresh heartbeat + Alive would previously authorize a signal when
	// ProcessInfo failed, even if the PID had been recycled.
	if err := Heartbeat(pidFile, meta); err != nil {
		t.Fatal(err)
	}
	var signals atomic.Int32
	restoreSignal := swapSignalStopHook(func(pid int) error {
		signals.Add(1)
		return signalStop(pid)
	})
	defer restoreSignal()

	// Inject unavailable metadata so fail-closed path is exercised even when
	// the OS process table would otherwise report a matching identity.
	prevInfo := processInfoForTargetHook
	processInfoForTargetHook = func(int) (ProcessInfoResult, error) {
		return ProcessInfoResult{}, errors.New("injected process metadata unavailable")
	}
	defer func() { processInfoForTargetHook = prevInfo }()

	// Also simulate a recycled start identity on the PID file side.
	meta.ProcessStartedAt = "recycled-start-identity"

	err := CoordinatedStopVerified(meta, time.Second, pidFile)
	if err == nil {
		t.Fatal("expected coordinated stop to fail closed for unavailable metadata")
	}
	if got := signals.Load(); got != 0 {
		t.Fatalf("recycled PID with unavailable ProcessInfo must not be signaled, got %d", got)
	}
}

func TestCoordinatedStopVerifiedTargetRecycleDoesNotSignal(t *testing.T) {
	// Not parallel: swaps package-level hooks.
	meta, pidFile, cleanup := startStoppableDaemon(t)
	defer cleanup()

	var signals atomic.Int32
	restoreSignal := swapSignalStopHook(func(pid int) error {
		signals.Add(1)
		return signalStop(pid)
	})
	defer restoreSignal()

	// Between claim and signal the target PID is recycled: start identity no
	// longer matches. Leader must not deliver a graceful signal.
	prev := revalidateTargetHook
	var checks atomic.Int32
	revalidateTargetHook = func(PIDFile) error {
		if checks.Add(1) == 1 {
			// First check after claim: still the original target so mark can proceed
			// would be wrong — recycle is observed before any signal attempt.
			return ErrUnverifiedProcess
		}
		return ErrUnverifiedProcess
	}
	defer func() { revalidateTargetHook = prev }()

	err := CoordinatedStopVerified(meta, time.Second, pidFile)
	if !errors.Is(err, ErrUnverifiedProcess) && !errors.Is(err, ErrNotRunning) {
		t.Fatalf("expected identity failure on recycled target, got %v", err)
	}
	if got := signals.Load(); got != 0 {
		t.Fatalf("recycled target must not receive a graceful signal, got %d", got)
	}
	// Claim released so a later stop against a new daemon can proceed.
	if _, statErr := os.Stat(StopCoordinationPath(pidFile)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed target revalidation must release claim, err=%v", statErr)
	}
}

func TestCoordinatedStopVerifiedTargetRecycleAfterMarkDoesNotSignal(t *testing.T) {
	// Not parallel: swaps package-level hooks.
	meta, pidFile, cleanup := startStoppableDaemon(t)
	defer cleanup()

	var signals atomic.Int32
	restoreSignal := swapSignalStopHook(func(pid int) error {
		signals.Add(1)
		return signalStop(pid)
	})
	defer restoreSignal()

	// Recycle is detected only on the immediate pre-signal revalidation, after
	// the fenced signaled mark is published.
	prev := revalidateTargetHook
	var checks atomic.Int32
	revalidateTargetHook = func(m PIDFile) error {
		n := checks.Add(1)
		if n == 1 {
			return checkTargetStillMatches(m)
		}
		return ErrUnverifiedProcess
	}
	defer func() { revalidateTargetHook = prev }()

	err := CoordinatedStopVerified(meta, time.Second, pidFile)
	if !errors.Is(err, ErrNotRunning) && !errors.Is(err, ErrUnverifiedProcess) {
		t.Fatalf("expected not-running/unverified after post-mark recycle, got %v", err)
	}
	if got := signals.Load(); got != 0 {
		t.Fatalf("post-mark recycle must not signal, got %d", got)
	}
}

func TestCoordinatedStopVerifiedPersistentRetryRespectsDeadline(t *testing.T) {
	// Not parallel: swaps package-level hooks.
	meta, pidFile, cleanup := startStoppableDaemon(t)
	defer cleanup()

	var signals atomic.Int32
	restoreSignal := swapSignalStopHook(func(pid int) error {
		signals.Add(1)
		return signalStop(pid)
	})
	defer restoreSignal()

	prev := claimOrFollowHook
	var retries atomic.Int32
	claimOrFollowHook = func(string, PIDFile) (stopRole, stopIntent, error) {
		retries.Add(1)
		return stopRoleRetry, stopIntent{}, nil
	}
	defer func() { claimOrFollowHook = prev }()

	start := time.Now()
	err := CoordinatedStopVerified(meta, 80*time.Millisecond, pidFile)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected deadline error under persistent retry contention")
	}
	if got := signals.Load(); got != 0 {
		t.Fatalf("retry contention must not signal, got %d", got)
	}
	if retries.Load() < 1 {
		t.Fatal("expected at least one retry attempt")
	}
	// Must not spin far past the caller deadline.
	if elapsed > 2*time.Second {
		t.Fatalf("retry loop exceeded deadline bound, elapsed=%s", elapsed)
	}
}

func TestCoordinatedStopVerifiedFollowerPIDReuseCompletes(t *testing.T) {
	// Not parallel: swaps package-level hooks.
	meta, pidFile, cleanup := startStoppableDaemon(t)
	defer cleanup()

	var signals atomic.Int32
	restoreSignal := swapSignalStopHook(func(pid int) error {
		signals.Add(1)
		return signalStop(pid)
	})
	defer restoreSignal()

	// Prior leader already signaled; this caller is a follower. While waiting,
	// the PID is reused by an unrelated process (start identity mismatch).
	writeStopIntent(t, pidFile, stopIntent{
		TargetPID:        meta.PID,
		ProcessStartedAt: meta.ProcessStartedAt,
		TokenHash:        meta.TokenHash,
		Executable:       meta.Executable,
		Root:             meta.Root,
		CoordinatorPID:   -1,
		ClaimID:          "prior-leader",
		StartedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Signaled:         true,
	})

	prev := targetStillRunningHook
	var polls atomic.Int32
	targetStillRunningHook = func(PIDFile) (bool, error) {
		// First observation: original still "running"; subsequent: recycled.
		if polls.Add(1) == 1 {
			return true, nil
		}
		return false, nil
	}
	defer func() { targetStillRunningHook = prev }()

	if err := CoordinatedStopVerified(meta, 2*time.Second, pidFile); err != nil {
		t.Fatalf("follower should complete when target identity is gone: %v", err)
	}
	if got := signals.Load(); got != 0 {
		t.Fatalf("follower must not re-signal on PID reuse, got %d", got)
	}
}

func TestCoordinatedStopVerifiedOldLeaderCannotResumeAfterReclaim(t *testing.T) {
	// Not parallel: swaps package-level hooks.
	meta, pidFile, cleanup := startStoppableDaemon(t)
	defer cleanup()

	var signals atomic.Int32
	restoreSignal := swapSignalStopHook(func(pid int) error {
		signals.Add(1)
		return signalStop(pid)
	})
	defer restoreSignal()

	oldClaimed := make(chan stopIntent, 1)
	releaseOld := make(chan struct{})
	var oldEntered atomic.Bool
	prevAfter := afterLeaderClaimHook
	afterLeaderClaimHook = func(claim stopIntent) {
		// Only the first leader blocks after claim so a replacement can reclaim.
		if oldEntered.CompareAndSwap(false, true) {
			oldClaimed <- claim
			<-releaseOld
		}
	}
	defer func() { afterLeaderClaimHook = prevAfter }()

	oldDone := make(chan error, 1)
	go func() {
		oldDone <- CoordinatedStopVerified(meta, 5*time.Second, pidFile)
	}()

	var oldClaim stopIntent
	select {
	case oldClaim = <-oldClaimed:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for old leader claim")
	}

	// Simulate coordinator identity loss (PID reuse of the stop caller) so a
	// second stop reclaims the unsignaled claim while the old leader is paused.
	stale := oldClaim
	stale.CoordinatorProcessStartedAt = "recycled-old-leader"
	stale.CoordinatorPID = os.Getpid() // live PID, wrong start identity
	writeStopIntent(t, pidFile, stale)

	// Replacement leader reclaims, marks, signals, and waits for exit.
	if err := CoordinatedStopVerified(meta, 5*time.Second, pidFile); err != nil && !errors.Is(err, ErrNotRunning) {
		t.Fatalf("replacement stop: %v", err)
	}
	if got := signals.Load(); got != 1 {
		t.Fatalf("replacement leader signals got %d, want 1", got)
	}

	close(releaseOld)
	select {
	case err := <-oldDone:
		// Old leader must fail the fenced mark (claim lost) and must not signal.
		if err == nil {
			// If the daemon already exited, a lost claim may surface as not running
			// after releaseStopClaim; either way no second signal is allowed.
		}
	case <-time.After(5 * time.Second):
		t.Fatal("old leader did not return after reclaim")
	}
	if got := signals.Load(); got != 1 {
		t.Fatalf("old leader resume must not deliver a second signal, got %d", got)
	}
}

func TestCoordinatedStopVerifiedCallerExitBeforeSignalIsReclaimed(t *testing.T) {
	// Not parallel: swaps package-level signalStopHook.
	meta, pidFile, cleanup := startStoppableDaemon(t)
	defer cleanup()

	// Simulate a leader that claimed coordination then exited before marking
	// signaled: dead coordinator PID, unsignaled complete identity.
	writeStopIntent(t, pidFile, stopIntent{
		TargetPID:                   meta.PID,
		ProcessStartedAt:            meta.ProcessStartedAt,
		TokenHash:                   meta.TokenHash,
		Executable:                  meta.Executable,
		Root:                        meta.Root,
		CoordinatorPID:              1 << 30,
		CoordinatorProcessStartedAt: "exited-caller",
		StartedAt:                   time.Now().UTC().Format(time.RFC3339Nano),
		Signaled:                    false,
	})

	var signals atomic.Int32
	restore := swapSignalStopHook(func(pid int) error {
		signals.Add(1)
		return signalStop(pid)
	})
	defer restore()

	if err := CoordinatedStopVerified(meta, 5*time.Second, pidFile); err != nil && !errors.Is(err, ErrNotRunning) {
		t.Fatalf("coordinated stop: %v", err)
	}
	if got := signals.Load(); got != 1 {
		t.Fatalf("dead pre-signal coordinator must be reclaimed, signals=%d", got)
	}
}

func TestClearStopCoordinationRemovesOrphansAndPreservesLiveMatch(t *testing.T) {
	t.Parallel()
	meta, pidFile, cleanup := startStoppableDaemon(t)
	defer cleanup()

	writeStopIntent(t, pidFile, stopIntent{
		TargetPID:        meta.PID,
		ProcessStartedAt: meta.ProcessStartedAt,
		TokenHash:        meta.TokenHash,
		Executable:       meta.Executable,
		Root:             meta.Root,
		CoordinatorPID:   os.Getpid(),
		StartedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Signaled:         true,
	})
	if err := ClearStopCoordination(pidFile, meta); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(StopCoordinationPath(pidFile)); err != nil {
		t.Fatalf("live matching coordination must be preserved: %v", err)
	}

	// Empty meta (start with no PID record) clears any orphan coordination.
	if err := ClearStopCoordination(pidFile, PIDFile{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(StopCoordinationPath(pidFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan coordination should be cleared, err=%v", err)
	}
}

// TestClearStopCoordinationPreservesReplacementClaimBetweenCheckAndUnlink
// covers AC1/AC5: a stale stop cleanup must not delete a replacement .stop
// claim published after validation and before unlink.
func TestClearStopCoordinationPreservesReplacementClaimBetweenCheckAndUnlink(t *testing.T) {
	// Not parallel: swaps package-level afterStopClearDecisionHook.
	pidFile := filepath.Join(t.TempDir(), "galley-daemon.pid")
	oldMeta := PIDFile{
		PID:              9001,
		Executable:       "/usr/bin/galley",
		Root:             "/tmp/old-root",
		ProcessStartedAt: "old-start",
		TokenHash:        "old-hash",
	}
	oldClaim := stopIntent{
		TargetPID:        oldMeta.PID,
		ProcessStartedAt: oldMeta.ProcessStartedAt,
		TokenHash:        oldMeta.TokenHash,
		Executable:       oldMeta.Executable,
		Root:             oldMeta.Root,
		CoordinatorPID:   1,
		ClaimID:          "old-claim",
		StartedAt:        time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
		Signaled:         true,
	}
	replacement := stopIntent{
		TargetPID:        9002,
		ProcessStartedAt: "new-start",
		TokenHash:        "new-hash",
		Executable:       "/usr/bin/galley",
		Root:             "/tmp/new-root",
		CoordinatorPID:   os.Getpid(),
		ClaimID:          "new-claim",
		StartedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Signaled:         false,
	}
	writeStopIntent(t, pidFile, oldClaim)

	prev := afterStopClearDecisionHook
	afterStopClearDecisionHook = func(coordPath string, observed stopIntent) {
		if observed.ClaimID != oldClaim.ClaimID {
			t.Fatalf("hook observed claim %q want %q", observed.ClaimID, oldClaim.ClaimID)
		}
		// Publish a replacement claim for a restarted daemon/new stop before
		// the stale cleanup unlinks. Production serializes this under the
		// lifecycle lock; the hook forces the compare-and-remove re-check path.
		data, err := json.MarshalIndent(replacement, "", "  ")
		if err != nil {
			t.Errorf("marshal replacement: %v", err)
			return
		}
		if err := os.WriteFile(coordPath, append(data, '\n'), 0o600); err != nil {
			t.Errorf("write replacement claim: %v", err)
		}
	}
	defer func() { afterStopClearDecisionHook = prev }()

	// Old target is not live; cleanup should attempt to drop oldClaim only.
	if err := ClearStopCoordination(pidFile, oldMeta); err != nil {
		t.Fatal(err)
	}
	got, err := readStopIntent(StopCoordinationPath(pidFile))
	if err != nil {
		t.Fatalf("replacement claim must remain: %v", err)
	}
	if got.ClaimID != replacement.ClaimID || got.TargetPID != replacement.TargetPID {
		t.Fatalf("replacement claim lost: got %+v want %+v", got, replacement)
	}
}

// TestStaleStopCleanupDoesNotDropRestartClaimUnderLifecycleLock interleaves an
// old stop's .stop cleanup with a new exclusive claim under the shared lock.
func TestStaleStopCleanupDoesNotDropRestartClaimUnderLifecycleLock(t *testing.T) {
	// Not parallel: coordinates package hooks and timing.
	pidFile := filepath.Join(t.TempDir(), "galley-daemon.pid")
	oldMeta := PIDFile{
		PID:              8001,
		Executable:       "/usr/bin/galley",
		Root:             "/tmp/stale-root",
		ProcessStartedAt: "stale-start",
		TokenHash:        "stale-hash",
	}
	oldClaim := stopIntent{
		TargetPID:        oldMeta.PID,
		ProcessStartedAt: oldMeta.ProcessStartedAt,
		TokenHash:        oldMeta.TokenHash,
		Executable:       oldMeta.Executable,
		Root:             oldMeta.Root,
		CoordinatorPID:   1,
		ClaimID:          "stale-claim",
		StartedAt:        time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
		Signaled:         true,
	}
	writeStopIntent(t, pidFile, oldClaim)

	// Hold the lifecycle lock as `daemon start` would, clear the stale claim
	// with Held API, then publish a new stop claim before releasing.
	release, err := ReservePID(pidFile)
	if err != nil {
		t.Fatal(err)
	}

	clearDone := make(chan error, 1)
	go func() {
		// Public ClearStopCoordination must block until start releases.
		clearDone <- ClearStopCoordination(pidFile, oldMeta)
	}()

	select {
	case err := <-clearDone:
		release()
		t.Fatalf("ClearStopCoordination returned while ReservePID held: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	// Start-path held clear drops the orphan while we still own the lock.
	if err := ClearStopCoordinationHeld(pidFile, oldMeta); err != nil {
		release()
		t.Fatal(err)
	}
	newClaim := stopIntent{
		TargetPID:        8002,
		ProcessStartedAt: "fresh-start",
		TokenHash:        "fresh-hash",
		Executable:       "/usr/bin/galley",
		Root:             "/tmp/fresh-root",
		CoordinatorPID:   os.Getpid(),
		ClaimID:          "fresh-claim",
		StartedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Signaled:         false,
	}
	writeStopIntent(t, pidFile, newClaim)
	release()

	select {
	case err := <-clearDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ClearStopCoordination did not finish after lifecycle lock release")
	}
	got, err := readStopIntent(StopCoordinationPath(pidFile))
	if err != nil {
		t.Fatalf("fresh claim must remain after deferred stale clear: %v", err)
	}
	if got.ClaimID != newClaim.ClaimID {
		t.Fatalf("fresh claim overwritten: got %+v", got)
	}
}

func TestCoordinatedStopVerifiedRejectsUnverifiedMeta(t *testing.T) {
	t.Parallel()
	meta := NewPIDFile(os.Getpid(), "/nonexistent/galley-impostor", t.TempDir(), []string{"/nonexistent/galley-impostor"})
	if err := CoordinatedStopVerified(meta, time.Second, filepath.Join(t.TempDir(), "p.pid")); !errors.Is(err, ErrUnverifiedProcess) {
		t.Fatalf("expected ErrUnverifiedProcess, got %v", err)
	}
}

// TestDirectStopVerifiedStillSignalsWithoutCoordination covers AC4: the
// uncoordinated StopVerified path remains a direct signal so terminal/service
// managers and explicit direct callers are unchanged. Coordination is opt-in
// via CoordinatedStopVerified / non-empty pidFile.
func TestDirectStopVerifiedStillSignalsWithoutCoordination(t *testing.T) {
	// Not parallel: swaps package-level signalStopHook.
	meta, pidFile, cleanup := startStoppableDaemon(t)
	defer cleanup()

	// Seed signaled coordination as if a CLI stop already ran. Direct StopVerified
	// must still signal — it does not consult the coordination file.
	writeStopIntent(t, pidFile, stopIntent{
		TargetPID:        meta.PID,
		ProcessStartedAt: meta.ProcessStartedAt,
		TokenHash:        meta.TokenHash,
		Executable:       meta.Executable,
		Root:             meta.Root,
		CoordinatorPID:   os.Getpid(),
		StartedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Signaled:         true,
	})

	// Direct Stop always signals and does not consult CLI coordination.
	if err := Stop(meta.PID, 5*time.Second); err != nil && !errors.Is(err, ErrNotRunning) {
		t.Fatalf("direct Stop: %v", err)
	}
	alive, err := Alive(meta.PID)
	if err != nil {
		t.Fatal(err)
	}
	if alive {
		t.Fatal("direct Stop should terminate the process without coordination")
	}
	if _, err := os.Stat(StopCoordinationPath(pidFile)); err != nil {
		t.Fatalf("direct Stop must not clear CLI coordination: %v", err)
	}
}

func TestMarkStopIntentSignaledIsAtomicReplace(t *testing.T) {
	t.Parallel()
	meta, pidFile, cleanup := startStoppableDaemon(t)
	defer cleanup()

	claim := stopIntent{
		TargetPID:        meta.PID,
		ProcessStartedAt: meta.ProcessStartedAt,
		TokenHash:        meta.TokenHash,
		Executable:       meta.Executable,
		Root:             meta.Root,
		CoordinatorPID:   os.Getpid(),
		ClaimID:          "mark-atomic-claim",
		StartedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Signaled:         false,
	}
	writeStopIntent(t, pidFile, claim)
	if err := markStopIntentSignaled(StopCoordinationPath(pidFile), claim); err != nil {
		t.Fatal(err)
	}
	intent, err := readStopIntent(StopCoordinationPath(pidFile))
	if err != nil {
		t.Fatal(err)
	}
	if !intent.Signaled {
		t.Fatal("expected signaled true after atomic mark")
	}
	if !intentHasCompleteTargetIdentity(intent) {
		t.Fatalf("marked intent lost complete identity: %+v", intent)
	}
	if intent.ClaimID != claim.ClaimID {
		t.Fatalf("claim fence lost on mark: got %q want %q", intent.ClaimID, claim.ClaimID)
	}
}

func TestMarkStopIntentSignaledRejectsReplacedClaim(t *testing.T) {
	t.Parallel()
	meta, pidFile, cleanup := startStoppableDaemon(t)
	defer cleanup()

	oldClaim := stopIntent{
		TargetPID:        meta.PID,
		ProcessStartedAt: meta.ProcessStartedAt,
		TokenHash:        meta.TokenHash,
		Executable:       meta.Executable,
		Root:             meta.Root,
		CoordinatorPID:   os.Getpid(),
		ClaimID:          "old-claim",
		StartedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Signaled:         false,
	}
	replacement := oldClaim
	replacement.ClaimID = "new-claim"
	replacement.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	writeStopIntent(t, pidFile, replacement)
	if err := markStopIntentSignaled(StopCoordinationPath(pidFile), oldClaim); err == nil {
		t.Fatal("expected mark to fail after claim replacement")
	}
	intent, err := readStopIntent(StopCoordinationPath(pidFile))
	if err != nil {
		t.Fatal(err)
	}
	if intent.Signaled {
		t.Fatal("replaced claim must not be marked by old leader")
	}
	if intent.ClaimID != "new-claim" {
		t.Fatalf("replacement claim corrupted: %+v", intent)
	}
}

func startStoppableDaemon(t *testing.T) (PIDFile, string, func()) {
	t.Helper()
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not available")
	}
	cmd := exec.Command(sleepPath, "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	root := t.TempDir()
	pidFile := filepath.Join(root, "galley-daemon.pid")
	// Use the real process executable so Verify can use ProcessInfo identity,
	// not only the fresh-heartbeat bypass.
	meta := NewPIDFile(cmd.Process.Pid, sleepPath, root, []string{sleepPath}).WithToken("coord-test")
	if err := WritePID(pidFile, meta); err != nil {
		cleanup()
		t.Fatal(err)
	}
	if err := Heartbeat(pidFile, meta); err != nil {
		cleanup()
		t.Fatal(err)
	}
	// Refresh meta from disk so ProcessStartedAt/TokenHash match Verify + intent.
	meta, err = ReadPIDFile(pidFile)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	meta.Token = "coord-test"
	if !Verify(meta, meta.Root, meta.Executable) {
		cleanup()
		t.Skip("process identity verification unavailable on this platform")
	}
	return meta, pidFile, cleanup
}

func startUnresponsiveStopDaemon(t *testing.T) (PIDFile, string, func()) {
	t.Helper()
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	cmd := exec.Command(shPath, "-c", `trap "" TERM; sleep 30`)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	root := t.TempDir()
	pidFile := filepath.Join(root, "galley-daemon.pid")
	// Heartbeat-based Verify uses the test binary path when ProcessInfo cannot
	// match a shell one-liner; keep that stable fixture for unresponsive tests.
	exe, err := os.Executable()
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	meta := NewPIDFile(cmd.Process.Pid, exe, root, []string{exe}).WithToken("coord-unresponsive")
	if err := WritePID(pidFile, meta); err != nil {
		cleanup()
		t.Fatal(err)
	}
	if err := Heartbeat(pidFile, meta); err != nil {
		cleanup()
		t.Fatal(err)
	}
	meta, err = ReadPIDFile(pidFile)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	meta.Token = "coord-unresponsive"
	if !Verify(meta, meta.Root, meta.Executable) {
		cleanup()
		t.Skip("process identity verification unavailable on this platform")
	}
	return meta, pidFile, cleanup
}

func swapSignalStopHook(fn func(int) error) func() {
	prev := signalStopHook
	signalStopHook = fn
	return func() { signalStopHook = prev }
}

func writeStopIntent(t *testing.T, pidFile string, intent stopIntent) {
	t.Helper()
	data, err := json.MarshalIndent(intent, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(StopCoordinationPath(pidFile), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitForStopCoordination(t *testing.T, pidFile string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	path := StopCoordinationPath(pidFile)
	for time.Now().Before(deadline) {
		intent, err := readStopIntent(path)
		if err == nil && intent.Signaled {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for stop coordination to become signaled")
}
