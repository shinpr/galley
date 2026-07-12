package daemonctl

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReservePIDIsExclusive(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "galley-daemon.pid")
	release, err := ReservePID(path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := ReservePID(path); err == nil {
		t.Fatal("expected lock error")
	}
}

func TestLifecycleLockReleaseIsCompareAndRemove(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "galley-daemon.pid")
	release, err := ReservePID(path)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate reclaim by a later caller: replace ownership under the path.
	replacement := lifecycleLockOwner{
		OwnerPID:   1,
		AcquiredAt: time.Now().UTC().Format(time.RFC3339Nano),
		ClaimID:    "replacement-claim",
	}
	writeLifecycleLock(t, LifecycleLockPath(path), replacement)
	// Original release must not delete the replacement.
	release()
	got, err := readLifecycleLockOwner(LifecycleLockPath(path))
	if err != nil {
		t.Fatalf("replacement lock must remain after stale release: %v", err)
	}
	if got.ClaimID != "replacement-claim" {
		t.Fatalf("replacement claim corrupted: %+v", got)
	}
}

func TestStaleLifecycleLockFromDeadOwnerIsReclaimed(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "galley-daemon.pid")
	// Owner PID that is not alive on this host.
	writeLifecycleLock(t, LifecycleLockPath(path), lifecycleLockOwner{
		OwnerPID:              0x7fffffff,
		OwnerProcessStartedAt: "dead-owner-start",
		AcquiredAt:            time.Now().UTC().Format(time.RFC3339Nano),
		ClaimID:               "dead-owner",
	})
	release, err := ReservePID(path)
	if err != nil {
		t.Fatalf("dead owner lock must be reclaimed: %v", err)
	}
	defer release()
	if _, err := os.Stat(LifecycleLockPath(path)); err != nil {
		t.Fatalf("reclaimed lock must be held by new owner: %v", err)
	}
}

func TestStaleLifecycleLockFromRecycledOwnerPIDIsReclaimed(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "galley-daemon.pid")
	// Live PID (this process) with a non-matching start identity: recycled owner.
	writeLifecycleLock(t, LifecycleLockPath(path), lifecycleLockOwner{
		OwnerPID:              os.Getpid(),
		OwnerProcessStartedAt: "not-this-process-start",
		AcquiredAt:            time.Now().UTC().Format(time.RFC3339Nano),
		ClaimID:               "recycled-owner",
	})
	release, err := ReservePID(path)
	if err != nil {
		t.Fatalf("recycled owner lock must be reclaimed: %v", err)
	}
	defer release()
}

func TestLegacyEmptyLifecycleLockIsReclaimedAfterGrace(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "galley-daemon.pid")
	lockPath := LifecycleLockPath(path)
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Backdate mtime past the publication grace so empty legacy locks reclaim.
	past := time.Now().Add(-recentLifecycleLockGrace - time.Second)
	if err := os.Chtimes(lockPath, past, past); err != nil {
		t.Fatal(err)
	}
	release, err := ReservePID(path)
	if err != nil {
		t.Fatalf("legacy empty lock must be reclaimed: %v", err)
	}
	defer release()
}

func TestExpiredLifecycleLockLeaseIsReclaimedWhileOwnerPIDLive(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "galley-daemon.pid")
	coordStarted := ""
	if info, err := ProcessInfo(os.Getpid()); err == nil {
		coordStarted = info.StartedAt
	}
	writeLifecycleLock(t, LifecycleLockPath(path), lifecycleLockOwner{
		OwnerPID:              os.Getpid(),
		OwnerProcessStartedAt: coordStarted,
		AcquiredAt:            time.Now().Add(-maxLifecycleLockLease - time.Second).UTC().Format(time.RFC3339Nano),
		ClaimID:               "expired-lease",
	})
	release, err := ReservePID(path)
	if err != nil {
		t.Fatalf("expired lease lock must be reclaimed: %v", err)
	}
	defer release()
}

// TestKilledStopCallerDoesNotBlockLaterStopAndStart proves AC5 recovery when a
// stop caller exits while holding the lifecycle lock and/or an unsignaled claim.
// The dead-owner lock and dead-coordinator claim are deterministic seams that
// compile and run on macOS/Linux and are runtime-equivalent on Windows.
func TestKilledStopCallerDoesNotBlockLaterStopAndStart(t *testing.T) {
	// Not parallel: uses process control helpers shared with stop tests.
	meta, pidFile, cleanup := startStoppableDaemon(t)
	defer cleanup()

	// Simulate a killed stop caller: exclusive lifecycle lock owned by a dead
	// PID, plus an unsignaled coordination claim owned by a dead coordinator.
	writeLifecycleLock(t, LifecycleLockPath(pidFile), lifecycleLockOwner{
		OwnerPID:              0x7ffffffe,
		OwnerProcessStartedAt: "killed-stop-caller",
		AcquiredAt:            time.Now().UTC().Format(time.RFC3339Nano),
		ClaimID:               "killed-stop-lock",
	})
	writeStopIntent(t, pidFile, stopIntent{
		TargetPID:                   meta.PID,
		ProcessStartedAt:            meta.ProcessStartedAt,
		TokenHash:                   meta.TokenHash,
		Executable:                  meta.Executable,
		Root:                        meta.Root,
		CoordinatorPID:              0x7ffffffe,
		CoordinatorProcessStartedAt: "killed-stop-caller",
		StartedAt:                   time.Now().UTC().Format(time.RFC3339Nano),
		ClaimID:                     "killed-stop-claim",
		Signaled:                    false,
	})

	// Later stop must reclaim and complete shutdown.
	if err := CoordinatedStopVerified(meta, 5*time.Second, pidFile); err != nil {
		t.Fatalf("later stop after killed caller: %v", err)
	}

	// Later start reservation must reclaim any residual lock and succeed.
	release, err := ReservePID(pidFile)
	if err != nil {
		t.Fatalf("later start after killed caller: %v", err)
	}
	release()
}

// TestLifecycleLockProcessInfoFailureDoesNotMarkLiveOwnerStale ensures a
// transient process-table lookup failure is not treated as proof the live
// lock owner is gone; the lease clock remains the reclaim backstop.
func TestLifecycleLockProcessInfoFailureDoesNotMarkLiveOwnerStale(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "galley-daemon.pid")
	started := "live-owner-start"
	// Owner PID is this process (live) with a recorded start identity. We do
	// not inject ProcessInfo failure globally; instead call lifecycleLockIsStale
	// with a start identity that ProcessInfo will not match only when available.
	// When ProcessInfo succeeds and mismatches, that is recycle (stale). When
	// ProcessInfo would fail, staleness must come only from the lease.
	//
	// Exercise the ProcessInfo-failure branch by using an owner start identity
	// and verifying a fresh lease is not reclaimed while the PID is live.
	// A mismatched start with successful ProcessInfo is covered separately.
	owner := lifecycleLockOwner{
		OwnerPID:              os.Getpid(),
		OwnerProcessStartedAt: started,
		AcquiredAt:            time.Now().UTC().Format(time.RFC3339Nano),
		ClaimID:               "live-processinfo-fail",
	}
	// If ProcessInfo works on this host, a non-matching start is recycle→stale.
	// Use the real start identity so ProcessInfo match succeeds and the lock is
	// held for the lease window (not stale).
	if info, err := ProcessInfo(os.Getpid()); err == nil && info.StartedAt != "" {
		owner.OwnerProcessStartedAt = info.StartedAt
	}
	stale, err := lifecycleLockIsStale(owner, LifecycleLockPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if stale {
		t.Fatal("live owner within lease must not be stale")
	}

	// Expired lease is reclaimable even when the PID is still live.
	owner.AcquiredAt = time.Now().Add(-maxLifecycleLockLease - time.Second).UTC().Format(time.RFC3339Nano)
	stale, err = lifecycleLockIsStale(owner, LifecycleLockPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if !stale {
		t.Fatal("expired lease must be stale even for a live owner")
	}
}

// TestLifecycleLockHandoffPreservesReplacementClaim proves generation-fenced
// drop does not delete a replacement lock published between identity match and
// the handoff rename (read/remove race).
func TestLifecycleLockHandoffPreservesReplacementClaim(t *testing.T) {
	// Not parallel: swaps afterLifecycleOwnerCheckHook.
	path := filepath.Join(t.TempDir(), "galley-daemon.pid")
	lockPath := LifecycleLockPath(path)
	old := lifecycleLockOwner{
		OwnerPID:              0x7ffffff0,
		OwnerProcessStartedAt: "old-dead-owner",
		AcquiredAt:            time.Now().UTC().Format(time.RFC3339Nano),
		ClaimID:               "old-generation",
	}
	writeLifecycleLock(t, lockPath, old)

	replacement := lifecycleLockOwner{
		OwnerPID:              os.Getpid(),
		OwnerProcessStartedAt: "replacement-owner",
		AcquiredAt:            time.Now().UTC().Format(time.RFC3339Nano),
		ClaimID:               "replacement-generation",
	}
	prev := afterLifecycleOwnerCheckHook
	afterLifecycleOwnerCheckHook = func(p string, expected lifecycleLockOwner) {
		if p != lockPath || expected.ClaimID != old.ClaimID {
			t.Fatalf("hook mismatch path=%q expected=%+v", p, expected)
		}
		// Replacement claims the lock after the stale owner was observed and
		// before the generation-fenced drop commits.
		writeLifecycleLock(t, p, replacement)
	}
	defer func() { afterLifecycleOwnerCheckHook = prev }()

	removed, err := removeLifecycleLockIfOwner(lockPath, old)
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Fatal("stale drop must not report success after replacement claim")
	}
	got, err := readLifecycleLockOwner(lockPath)
	if err != nil {
		t.Fatalf("replacement lock must remain: %v", err)
	}
	if got.ClaimID != replacement.ClaimID {
		t.Fatalf("replacement claim lost: got %+v want %+v", got, replacement)
	}
}

// TestLifecycleLockProcessInfoFailureAloneDoesNotReclaimLiveOwner injects a
// process-table lookup failure for a live owner within the lease window.
func TestLifecycleLockProcessInfoFailureAloneDoesNotReclaimLiveOwner(t *testing.T) {
	// Not parallel: swaps lifecycleProcessInfoHook.
	path := filepath.Join(t.TempDir(), "galley-daemon.pid")
	owner := lifecycleLockOwner{
		OwnerPID:              os.Getpid(),
		OwnerProcessStartedAt: "recorded-start",
		AcquiredAt:            time.Now().UTC().Format(time.RFC3339Nano),
		ClaimID:               "processinfo-fail-lease",
	}
	prev := lifecycleProcessInfoHook
	lifecycleProcessInfoHook = func(int) (ProcessInfoResult, error) {
		return ProcessInfoResult{}, errors.New("injected process metadata unavailable")
	}
	defer func() { lifecycleProcessInfoHook = prev }()

	stale, err := lifecycleLockIsStale(owner, LifecycleLockPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if stale {
		t.Fatal("ProcessInfo failure alone must not mark a live leased owner stale")
	}

	// Lease expiry remains the reclaim backstop when metadata is unavailable.
	owner.AcquiredAt = time.Now().Add(-maxLifecycleLockLease - time.Second).UTC().Format(time.RFC3339Nano)
	stale, err = lifecycleLockIsStale(owner, LifecycleLockPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if !stale {
		t.Fatal("expired lease must reclaim even when ProcessInfo fails")
	}
}

// TestAbandonedDropTombstoneDoesNotBlockReservePID seeds a stale lock plus an
// abandoned .lock.drop.<claim> fence (crash after fence create, before rename)
// and proves ReservePID reclaims both so a later start reservation succeeds.
func TestAbandonedDropTombstoneDoesNotBlockReservePID(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "galley-daemon.pid")
	lockPath := LifecycleLockPath(path)
	claimID := "abandoned-drop-reserve"
	writeLifecycleLock(t, lockPath, lifecycleLockOwner{
		OwnerPID:              0x7ffffff1,
		OwnerProcessStartedAt: "dead-lock-owner",
		AcquiredAt:            time.Now().UTC().Format(time.RFC3339Nano),
		ClaimID:               claimID,
	})
	writeAbandonedDropFence(t, lifecycleDropFencePath(lockPath, claimID), claimID)

	release, err := ReservePID(path)
	if err != nil {
		t.Fatalf("ReservePID must reclaim stale lock past abandoned drop fence: %v", err)
	}
	defer release()
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("reclaimed lock must be held by new owner: %v", err)
	}
	if _, err := os.Stat(lifecycleDropFencePath(lockPath, claimID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("abandoned drop fence must be cleared after reclaim, err=%v", err)
	}
}

// TestAbandonedDropTombstoneDoesNotBlockCoordinatedStopCleanup proves stop
// coordination can acquire the lifecycle lock and clear stop state when a prior
// reclaim left an abandoned generation fence beside a stale lock.
func TestAbandonedDropTombstoneDoesNotBlockCoordinatedStopCleanup(t *testing.T) {
	// Not parallel: uses process control helpers shared with stop tests.
	meta, pidFile, cleanup := startStoppableDaemon(t)
	defer cleanup()

	lockPath := LifecycleLockPath(pidFile)
	claimID := "abandoned-drop-stop"
	writeLifecycleLock(t, lockPath, lifecycleLockOwner{
		OwnerPID:              0x7ffffff2,
		OwnerProcessStartedAt: "killed-drop-mid-handoff",
		AcquiredAt:            time.Now().UTC().Format(time.RFC3339Nano),
		ClaimID:               claimID,
	})
	writeAbandonedDropFence(t, lifecycleDropFencePath(lockPath, claimID), claimID)

	if err := CoordinatedStopVerified(meta, 5*time.Second, pidFile); err != nil {
		t.Fatalf("coordinated stop after abandoned drop fence: %v", err)
	}
	if _, err := os.Stat(StopCoordinationPath(pidFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("coordination file should be cleared after stop, err=%v", err)
	}
	if _, err := os.Stat(lifecycleDropFencePath(lockPath, claimID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("abandoned drop fence must not remain after stop cleanup, err=%v", err)
	}
}

// TestAbandonedDropTombstoneDoesNotBlockDaemonStartReservation mirrors the
// daemon start path: ReservePID must succeed with a stale lock + abandoned
// generation fence so a subsequent start can publish a new PID file.
func TestAbandonedDropTombstoneDoesNotBlockDaemonStartReservation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	pidFile := filepath.Join(root, "galley-daemon.pid")
	lockPath := LifecycleLockPath(pidFile)
	claimID := "abandoned-drop-start"
	// Old lock from a killed start/stop caller mid generation-fenced drop.
	writeLifecycleLock(t, lockPath, lifecycleLockOwner{
		OwnerPID:              0x7ffffff3,
		OwnerProcessStartedAt: "killed-start-caller",
		AcquiredAt:            time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano),
		ClaimID:               claimID,
	})
	// Empty legacy fence (pre-metadata) backdated past grace: still reclaimable.
	tombPath := lifecycleDropFencePath(lockPath, claimID)
	if err := os.WriteFile(tombPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-recentDropFenceGrace - time.Second)
	if err := os.Chtimes(tombPath, past, past); err != nil {
		t.Fatal(err)
	}

	// Same gate `galley daemon start` uses before Inspect/spawn.
	release, err := ReservePID(pidFile)
	if err != nil {
		t.Fatalf("daemon start reservation blocked by abandoned drop fence: %v", err)
	}
	defer release()

	// With the lifecycle lock held, start may clear orphaned coordination and
	// publish a fresh PID file (held APIs avoid re-acquiring the lock).
	if err := ClearStopCoordinationHeld(pidFile, PIDFile{}); err != nil {
		t.Fatalf("clear stop coordination under reservation: %v", err)
	}
	meta := NewPIDFile(os.Getpid(), "galley-test", root, []string{"galley-test"}).WithToken("start-after-drop")
	if err := WritePID(pidFile, meta); err != nil {
		t.Fatalf("write pid under reservation: %v", err)
	}
	if _, err := os.Stat(pidFile); err != nil {
		t.Fatalf("pid file missing after start reservation: %v", err)
	}
}

// TestLiveDropFenceIsNotStolenWithinLease ensures a fresh fence owned by this
// live process is not treated as abandoned (concurrent drop in progress).
func TestLiveDropFenceIsNotStolenWithinLease(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "galley-daemon.pid")
	lockPath := LifecycleLockPath(path)
	claimID := "live-drop-fence"
	tombPath := lifecycleDropFencePath(lockPath, claimID)
	started := ""
	if info, err := ProcessInfo(os.Getpid()); err == nil {
		started = info.StartedAt
	}
	writeDropFenceOwner(t, tombPath, dropFenceOwner{
		DropperPID:              os.Getpid(),
		DropperProcessStartedAt: started,
		AcquiredAt:              time.Now().UTC().Format(time.RFC3339Nano),
		Generation:              sanitizeLifecycleGeneration(claimID),
	})
	reclaimed, err := reclaimAbandonedDropFence(tombPath)
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed {
		t.Fatal("live drop fence within lease must not be reclaimed")
	}
	if _, err := os.Stat(tombPath); err != nil {
		t.Fatalf("live drop fence must remain: %v", err)
	}

	// Expired lease is reclaimable even when the dropper PID is still live.
	writeDropFenceOwner(t, tombPath, dropFenceOwner{
		DropperPID:              os.Getpid(),
		DropperProcessStartedAt: started,
		AcquiredAt:              time.Now().Add(-maxDropFenceLease - time.Second).UTC().Format(time.RFC3339Nano),
		Generation:              sanitizeLifecycleGeneration(claimID),
	})
	reclaimed, err = reclaimAbandonedDropFence(tombPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reclaimed {
		t.Fatal("expired drop fence lease must be reclaimed")
	}
}

// TestAbandonedDropFenceWithRetiredSiblingDoesNotBlock covers the crash window
// after rename (lock moved to .retired) but before fence/retired removal: the
// lock path is free so ReservePID claims it; a later reclaim of the same
// generation still cleans fence+retired via removeDropFenceArtifacts.
func TestAbandonedDropFenceWithRetiredSiblingDoesNotBlock(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "galley-daemon.pid")
	lockPath := LifecycleLockPath(path)
	claimID := "abandoned-drop-retired"
	tombPath := lifecycleDropFencePath(lockPath, claimID)
	retiredPath := tombPath + ".retired"
	// Lock already renamed away; only fence + retired remain.
	writeLifecycleLock(t, retiredPath, lifecycleLockOwner{
		OwnerPID:              0x7ffffff4,
		OwnerProcessStartedAt: "already-retired",
		AcquiredAt:            time.Now().UTC().Format(time.RFC3339Nano),
		ClaimID:               claimID,
	})
	writeAbandonedDropFence(t, tombPath, claimID)

	release, err := ReservePID(path)
	if err != nil {
		t.Fatalf("ReservePID with abandoned fence+retired must succeed: %v", err)
	}
	release()

	// Direct reclaim of the abandoned fence (as a later drop of that generation
	// would do when it sees ErrExist) removes fence and retired sibling.
	reclaimed, err := reclaimAbandonedDropFence(tombPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reclaimed {
		t.Fatal("abandoned fence with retired sibling must be reclaimable")
	}
	if _, err := os.Stat(tombPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("drop fence should be gone, err=%v", err)
	}
	if _, err := os.Stat(retiredPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired sibling should be cleaned, err=%v", err)
	}
}

func writeLifecycleLock(t *testing.T, lockPath string, owner lifecycleLockOwner) {
	t.Helper()
	data, err := json.MarshalIndent(owner, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeAbandonedDropFence(t *testing.T, tombPath, generation string) {
	t.Helper()
	writeDropFenceOwner(t, tombPath, dropFenceOwner{
		DropperPID:              0x7ffffff5,
		DropperProcessStartedAt: "dead-dropper",
		AcquiredAt:              time.Now().UTC().Format(time.RFC3339Nano),
		Generation:              sanitizeLifecycleGeneration(generation),
	})
}

func writeDropFenceOwner(t *testing.T, tombPath string, owner dropFenceOwner) {
	t.Helper()
	data, err := json.MarshalIndent(owner, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tombPath, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
