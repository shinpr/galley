package daemonctl

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shinpr/galley/internal/jsonio"
)

// stopIntent records that a cooperating `galley daemon stop` owns the single
// graceful signal for a verified daemon. Followers match this identity and wait
// without signaling again once Signaled is true. If the coordinator dies before
// Signaled is set, another stop may re-claim and send the signal.
//
// Target identity fields are required so partial or corrupt coordination cannot
// match a live daemon. CoordinatorProcessStartedAt binds reclaim to process
// start identity so a recycled coordinator PID cannot permanently suppress stop.
// ClaimID fences publication and reclaim so an old leader cannot resume after
// replacement and deliver a second graceful signal.
type stopIntent struct {
	TargetPID                   int    `json:"target_pid"`
	ProcessStartedAt            string `json:"process_started_at"`
	TokenHash                   string `json:"token_hash"`
	Executable                  string `json:"executable"`
	Root                        string `json:"root"`
	CoordinatorPID              int    `json:"coordinator_pid"`
	CoordinatorProcessStartedAt string `json:"coordinator_process_started_at,omitempty"`
	StartedAt                   string `json:"started_at"`
	ClaimID                     string `json:"claim_id,omitempty"`
	Signaled                    bool   `json:"signaled"`
}

type stopRole int

const (
	stopRoleLeader stopRole = iota
	stopRoleFollower
	stopRoleRetry
)

// maxUnsignaledCoordinatorLease is the backstop reclaim window for an unsignaled
// claim whose coordinator still appears live but may be wedged or unobservable.
// Signaled coordination is never reclaimed by age: force-stop is the escape hatch.
const maxUnsignaledCoordinatorLease = 30 * time.Second

// signalStopHook is the graceful-signal seam used by CoordinatedStopVerified.
// Production code points at signalStop; tests replace it to count signals.
var signalStopHook = signalStop

// markStopSignaledHook publishes the signaled mark before delivery. Production
// uses markStopIntentSignaled; tests replace it to inject publication failures.
var markStopSignaledHook = markStopIntentSignaled

// afterLeaderClaimHook runs after a successful exclusive claim and before
// identity revalidation / signal publication. Tests use it to interleave reclaim.
var afterLeaderClaimHook func(claim stopIntent)

// claimOrFollowHook, when set, replaces claimOrFollowStop. Tests use it to force
// persistent retry contention without wall-clock races.
var claimOrFollowHook func(coordPath string, meta PIDFile) (stopRole, stopIntent, error)

// revalidateTargetHook, when set, replaces revalidateTargetForSignal.
var revalidateTargetHook func(meta PIDFile) error

// targetStillRunningHook, when set, replaces targetProcessStillRunning.
var targetStillRunningHook func(meta PIDFile) (bool, error)

// processInfoForTargetHook, when set, replaces ProcessInfo inside
// checkTargetStillMatches. Tests inject unavailable process-table metadata.
var processInfoForTargetHook func(pid int) (ProcessInfoResult, error)

// StopCoordinationPath returns the coordination file beside a PID file.
func StopCoordinationPath(pidFile string) string {
	if pidFile == "" {
		return ""
	}
	return pidFile + ".stop"
}

// CoordinatedStopVerified stops a verified daemon while ensuring cooperating
// `galley daemon stop` callers send at most one graceful signal. The first
// successful claimant sends the signal; later callers for the same verified
// identity only wait for exit. Stale coordination for a different process
// identity is discarded so stop/start cannot wedge on recycled PIDs.
//
// When pidFile is empty, behavior matches StopVerified (no coordination).
func CoordinatedStopVerified(meta PIDFile, timeout time.Duration, pidFile string) error {
	if !Verify(meta, meta.Root, meta.Executable) {
		return ErrUnverifiedProcess
	}
	if pidFile == "" {
		return Stop(meta.PID, timeout)
	}

	coordPath := StopCoordinationPath(pidFile)
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}

	for {
		if err := ensureStopDeadline(deadline, timeout); err != nil {
			return err
		}
		role, claim, err := claimOrFollowStopEntry(coordPath, meta)
		if err != nil {
			return err
		}
		remaining := remainingStopTimeout(deadline, timeout)
		switch role {
		case stopRoleLeader:
			if afterLeaderClaimHook != nil {
				afterLeaderClaimHook(claim)
			}
			// Another cooperating stop may have finished and removed coordination
			// while we were retrying; never signal a process that is already gone
			// or no longer matches the verified target identity.
			if err := revalidateTargetForSignal(meta); err != nil {
				_ = releaseStopClaim(coordPath, claim)
				return err
			}
			// Publish signaled under the claim fence before delivery so a crash
			// between claim and signal cannot let a later stop re-signal into the
			// daemon's second-signal immediate-exit path. A crash after this mark
			// and before delivery leaves force-stop as the escape hatch.
			//
			// Mark failure must not deliver a signal. Ownership loss after reclaim
			// must not delete the replacement claim.
			if err := markStopSignaledHook(coordPath, claim); err != nil {
				_ = releaseStopClaim(coordPath, claim)
				return fmt.Errorf("mark stop coordination signaled: %w", err)
			}
			// Revalidate immediately before the OS signal so a recycled PID that
			// appeared after the initial Verify cannot receive SIGTERM.
			if err := revalidateTargetForSignal(meta); err != nil {
				// Already marked signaled: leave coordination so a later normal
				// stop does not re-signal. Target gone/recycled reports as not running.
				if errors.Is(err, ErrNotRunning) || errors.Is(err, ErrUnverifiedProcess) {
					_ = ClearStopCoordination(pidFile, meta)
					return ErrNotRunning
				}
				return err
			}
			if err := signalStopHook(meta.PID); err != nil {
				if errors.Is(err, ErrNotRunning) {
					_ = ClearStopCoordination(pidFile, meta)
					return ErrNotRunning
				}
				// Marked but signal failed: leave signaled coordination so a
				// later normal stop does not re-signal. Force-stop is the
				// operator escape hatch.
				return err
			}
			if err := waitExitForTarget(meta, remaining, "stop"); err != nil {
				// Timeout or wait failure: leave coordination so a later normal
				// stop does not re-signal a shutdown already in progress.
				return err
			}
			// Exit succeeded. Cleanup failure must not flip stop into failure
			// or re-signal; start/stop stale recovery clears orphans later.
			_ = ClearStopCoordination(pidFile, meta)
			return nil
		case stopRoleFollower:
			if err := waitExitForTarget(meta, remaining, "stop"); err != nil {
				return err
			}
			_ = ClearStopCoordination(pidFile, meta)
			return nil
		case stopRoleRetry:
			// Bound busy reclaim loops to the caller deadline.
			if err := ensureStopDeadline(deadline, timeout); err != nil {
				return err
			}
			time.Sleep(10 * time.Millisecond)
			continue
		default:
			return fmt.Errorf("unknown stop coordination role %d", role)
		}
	}
}

// ClearStopCoordination removes stop coordination state when it is absent,
// unreadable, incomplete, targeted at a different process identity, or the
// target is no longer the live process described by meta. Matching in-progress
// coordination for a still-live target is left in place so later normal stops
// stay followers. When meta.PID is zero, any coordination file is removed
// (used by start when no PID record remains).
//
// Cleanup is compare-and-remove under the shared lifecycle lock so a concurrent
// restart or new stop claim cannot lose a replacement .stop record. Callers
// that already hold ReservePID must use ClearStopCoordinationHeld instead.
func ClearStopCoordination(pidFile string, meta PIDFile) error {
	if pidFile == "" {
		return nil
	}
	return withLifecycleLock(pidFile, true, func() error {
		return clearStopCoordinationLocked(pidFile, meta)
	})
}

// ClearStopCoordinationHeld is ClearStopCoordination for callers that already
// hold ReservePID for pidFile.
func ClearStopCoordinationHeld(pidFile string, meta PIDFile) error {
	if pidFile == "" {
		return nil
	}
	return clearStopCoordinationLocked(pidFile, meta)
}

// afterStopClearDecisionHook runs after a clear decision is made for an observed
// intent and before the fenced unlink. Tests use it to publish a replacement
// claim between validation and remove.
var afterStopClearDecisionHook func(coordPath string, observed stopIntent)

func clearStopCoordinationLocked(pidFile string, meta PIDFile) error {
	coordPath := StopCoordinationPath(pidFile)
	if meta.PID <= 0 {
		// Start with no PID record: drop any orphan coordination under the lock.
		return removeStopCoordinationFile(coordPath)
	}
	intent, err := readStopIntent(coordPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		// Corrupt/unreadable: only safe to drop under the lifecycle lock so a
		// concurrent complete claim is not clobbered mid-publish.
		return removeStopCoordinationFile(coordPath)
	}
	if !intentHasCompleteTargetIdentity(intent) {
		// Incomplete publication cannot bind a live target; fence-remove only
		// the observed incomplete record.
		return removeStopCoordinationIfClaimLocked(coordPath, intent)
	}
	if !intentMatchesTarget(intent, meta) {
		// Coordination names a different daemon (including a restart that
		// already claimed). Leave it so a stale stop cannot drop the new claim.
		return nil
	}
	still, stillErr := targetProcessStillRunning(meta)
	if stillErr != nil {
		return stillErr
	}
	if !still {
		return removeStopCoordinationIfClaimLocked(coordPath, intent)
	}
	// Live matching target: coordination still meaningful; do not clear.
	return nil
}

func claimOrFollowStopEntry(coordPath string, meta PIDFile) (stopRole, stopIntent, error) {
	if claimOrFollowHook != nil {
		return claimOrFollowHook(coordPath, meta)
	}
	return claimOrFollowStop(coordPath, meta)
}

func claimOrFollowStop(coordPath string, meta PIDFile) (stopRole, stopIntent, error) {
	intent, err := newStopIntent(meta)
	if err != nil {
		return 0, stopIntent{}, err
	}
	var (
		role  stopRole
		claim stopIntent
	)
	// Claim, reclaim, and identity reads share the lifecycle lock with start and
	// PID/.stop cleanup so a stale clear cannot delete a just-published claim.
	err = withLifecycleLock(lifecyclePathFromCoord(coordPath), true, func() error {
		var lockedErr error
		role, claim, lockedErr = claimOrFollowStopLocked(coordPath, meta, intent)
		return lockedErr
	})
	return role, claim, err
}

func claimOrFollowStopLocked(coordPath string, meta PIDFile, intent stopIntent) (stopRole, stopIntent, error) {
	claimed, err := tryClaimStopIntent(coordPath, intent)
	if err != nil {
		return 0, stopIntent{}, err
	}
	if claimed {
		return stopRoleLeader, intent, nil
	}

	existing, err := readStopIntent(coordPath)
	if errors.Is(err, os.ErrNotExist) {
		return stopRoleRetry, stopIntent{}, nil
	}
	if err != nil {
		// A concurrent leader may still be publishing the intent after O_EXCL
		// create. Only drop corrupt files that are old enough to be abandoned.
		info, statErr := os.Stat(coordPath)
		if statErr == nil && time.Since(info.ModTime()) < 2*time.Second {
			return stopRoleRetry, stopIntent{}, nil
		}
		if rmErr := removeStopCoordinationFile(coordPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return 0, stopIntent{}, rmErr
		}
		return stopRoleRetry, stopIntent{}, nil
	}
	if !intentHasCompleteTargetIdentity(existing) {
		// Partial publication cannot safely match a live target; reclaim only
		// the observed incomplete record so a concurrent complete claim is kept.
		if err := removeStopCoordinationIfClaimLocked(coordPath, existing); err != nil {
			return 0, stopIntent{}, err
		}
		return stopRoleRetry, stopIntent{}, nil
	}
	if intentMatchesTarget(existing, meta) {
		if existing.Signaled {
			// Same verified daemon already received its one graceful signal.
			// Observe exit even when the original coordinator has exited.
			return stopRoleFollower, existing, nil
		}
		owns, ownErr := coordinatorStillOwns(existing, coordPath)
		if ownErr != nil {
			return 0, stopIntent{}, ownErr
		}
		if owns {
			// Leader still working on the pre-signal claim; wait as follower.
			return stopRoleFollower, existing, nil
		}
		// Coordinator died, was recycled, or exceeded the unsignaled lease:
		// fenced re-claim so an old leader cannot resume after replacement.
		if err := removeStopCoordinationIfClaimLocked(coordPath, existing); err != nil {
			return 0, stopIntent{}, err
		}
		return stopRoleRetry, stopIntent{}, nil
	}
	// Coordination names a different process identity (including PID reuse).
	if err := removeStopCoordinationIfClaimLocked(coordPath, existing); err != nil {
		return 0, stopIntent{}, err
	}
	return stopRoleRetry, stopIntent{}, nil
}

func newStopIntent(meta PIDFile) (stopIntent, error) {
	if meta.PID <= 0 || meta.Executable == "" || meta.Root == "" {
		return stopIntent{}, fmt.Errorf("stop coordination requires complete target identity (pid, executable, root)")
	}
	intent := stopIntent{
		TargetPID:        meta.PID,
		ProcessStartedAt: meta.ProcessStartedAt,
		TokenHash:        meta.TokenHash,
		Executable:       meta.Executable,
		Root:             meta.Root,
		CoordinatorPID:   os.Getpid(),
		StartedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		ClaimID:          newClaimID(),
	}
	if info, err := ProcessInfo(os.Getpid()); err == nil {
		intent.CoordinatorProcessStartedAt = info.StartedAt
	}
	if !intentHasCompleteTargetIdentity(intent) {
		// ProcessStartedAt or TokenHash may be unavailable on some platforms;
		// require the durable identity fields that Verify also needs.
		if intent.TargetPID <= 0 || intent.Executable == "" || intent.Root == "" {
			return stopIntent{}, fmt.Errorf("stop coordination requires complete target identity")
		}
	}
	return intent, nil
}

func newClaimID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fall back to time-based uniqueness if the platform CSPRNG fails.
		return fmt.Sprintf("t-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func markStopIntentSignaled(path string, claim stopIntent) error {
	return withLifecycleLock(lifecyclePathFromCoord(path), true, func() error {
		intent, err := readStopIntent(path)
		if err != nil {
			return err
		}
		// Fence: only the original claim owner may publish Signaled. After reclaim
		// replaces the file, an old leader must fail here and must not signal.
		if !sameStopClaim(intent, claim) {
			return fmt.Errorf("stop coordination claim lost before signal")
		}
		if !intentHasCompleteTargetIdentity(intent) {
			return fmt.Errorf("stop coordination incomplete at %s", path)
		}
		if intent.TargetPID != claim.TargetPID ||
			cleanPath(intent.Executable) != cleanPath(claim.Executable) ||
			cleanPath(intent.Root) != cleanPath(claim.Root) {
			return fmt.Errorf("stop coordination identity changed before signal")
		}
		if intent.ProcessStartedAt != claim.ProcessStartedAt || intent.TokenHash != claim.TokenHash {
			return fmt.Errorf("stop coordination target identity changed before signal")
		}
		if intent.Signaled {
			// Same claim already marked (retry of mark after crash); keep fencing.
			return nil
		}
		intent.Signaled = true
		// Atomic replace: temp write + rename so readers never observe a partial
		// JSON rewrite that could be parsed as unsignaled after a crash.
		return writeStopIntentAtomic(path, intent)
	})
}

// coordinatorStillOwns reports whether the recorded coordinator still owns an
// unsignaled claim. Ownership requires a live PID, a parseable claim lease
// (StartedAt) or a bounded filesystem-time lease for legacy payloads, and a
// matching coordinator process start identity when recorded. A recycled PID,
// missing/invalid lease identity, ProcessInfo failure, or expired lease is not
// ownership and must not permanently suppress later normal stops.
func coordinatorStillOwns(intent stopIntent, coordPath string) (bool, error) {
	if intent.CoordinatorPID <= 0 {
		return false, nil
	}
	alive, err := Alive(intent.CoordinatorPID)
	if err != nil {
		return false, err
	}
	if !alive {
		return false, nil
	}

	// Lease clock: prefer parseable StartedAt. Malformed/legacy claims without
	// a usable StartedAt fall back to the coordination file mtime so reclaim is
	// bounded rather than indefinite.
	startedAtParseable := false
	var claimAge time.Duration
	if started, parseErr := time.Parse(time.RFC3339Nano, intent.StartedAt); parseErr == nil {
		startedAtParseable = true
		claimAge = time.Since(started)
	} else {
		info, statErr := os.Stat(coordPath)
		if statErr != nil {
			// Cannot establish a lease clock: not durable ownership.
			return false, nil
		}
		claimAge = time.Since(info.ModTime())
	}
	if claimAge > maxUnsignaledCoordinatorLease {
		return false, nil
	}

	if intent.CoordinatorProcessStartedAt != "" {
		info, infoErr := ProcessInfo(intent.CoordinatorPID)
		if infoErr != nil {
			// Cannot confirm the live PID is still the original coordinator.
			// Prefer reclaim over permanent suppression of stop.
			return false, nil
		}
		if info.StartedAt == "" || info.StartedAt != intent.CoordinatorProcessStartedAt {
			// Missing OS start identity or recycled coordinator PID.
			return false, nil
		}
		return true, nil
	}

	// No coordinator start fence recorded. Require a parseable StartedAt lease
	// so a live recycled PID without start identity cannot own indefinitely via
	// mtime alone. Legacy claims with missing/invalid StartedAt are reclaimed.
	if !startedAtParseable {
		return false, nil
	}
	return true, nil
}

func tryClaimStopIntent(path string, intent stopIntent) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("create stop coordination dir: %w", err)
	}
	// O_EXCL creates the coordination key atomically. Payload is written fully
	// before close; any write failure removes the exclusive create so another
	// stop can reclaim rather than follow a partial unsignaled file.
	data, err := json.MarshalIndent(intent, "", "  ")
	if err != nil {
		return false, err
	}
	data = append(data, '\n')
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return false, fmt.Errorf("write stop coordination: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return false, fmt.Errorf("sync stop coordination: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return false, fmt.Errorf("close stop coordination: %w", err)
	}
	return true, nil
}

func writeStopIntentAtomic(path string, intent stopIntent) error {
	return jsonio.Write(path, intent)
}

func readStopIntent(path string) (stopIntent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return stopIntent{}, err
	}
	var intent stopIntent
	if err := json.Unmarshal(data, &intent); err != nil {
		return stopIntent{}, fmt.Errorf("invalid stop coordination %s: %w", path, err)
	}
	if intent.TargetPID <= 0 {
		return stopIntent{}, fmt.Errorf("invalid stop coordination %s: missing target_pid", path)
	}
	return intent, nil
}

// intentHasCompleteTargetIdentity requires the durable target fields that bind
// a coordination file to one verified daemon. ProcessStartedAt and TokenHash
// are included when present on the claim; a file missing executable/root/pid is
// never treated as a live match.
func intentHasCompleteTargetIdentity(intent stopIntent) bool {
	return intent.TargetPID > 0 && intent.Executable != "" && intent.Root != ""
}

func intentMatchesTarget(intent stopIntent, meta PIDFile) bool {
	if !intentHasCompleteTargetIdentity(intent) {
		return false
	}
	if intent.TargetPID != meta.PID {
		return false
	}
	if cleanPath(intent.Executable) != cleanPath(meta.Executable) {
		return false
	}
	if cleanPath(intent.Root) != cleanPath(meta.Root) {
		return false
	}
	// When either side records start time or token hash, both must agree so a
	// recycled target PID cannot inherit a prior signaled mark.
	if intent.ProcessStartedAt != "" || meta.ProcessStartedAt != "" {
		if intent.ProcessStartedAt != meta.ProcessStartedAt {
			return false
		}
	}
	if intent.TokenHash != "" || meta.TokenHash != "" {
		if intent.TokenHash != meta.TokenHash {
			return false
		}
	}
	return true
}

// sameStopClaim reports whether two intents are the same exclusive claim.
// ClaimID is preferred; older records without ClaimID fall back to coordinator
// and target field equality so tests that write raw intents still fence reclaim.
func sameStopClaim(a, b stopIntent) bool {
	if a.ClaimID != "" && b.ClaimID != "" {
		return a.ClaimID == b.ClaimID
	}
	return a.TargetPID == b.TargetPID &&
		a.CoordinatorPID == b.CoordinatorPID &&
		a.CoordinatorProcessStartedAt == b.CoordinatorProcessStartedAt &&
		a.StartedAt == b.StartedAt &&
		a.ProcessStartedAt == b.ProcessStartedAt &&
		a.TokenHash == b.TokenHash &&
		cleanPath(a.Executable) == cleanPath(b.Executable) &&
		cleanPath(a.Root) == cleanPath(b.Root)
}

// removeStopCoordinationIfClaim deletes path only when it still holds the
// observed claim. If a replacement claim was published, the new file is kept.
// A claim that transitions unsignaled -> signaled under the same ClaimID is
// not removed by a stale reclaim decision.
//
// Acquires the shared lifecycle lock. Callers that already hold the lock must
// use removeStopCoordinationIfClaimLocked.
func removeStopCoordinationIfClaim(path string, expected stopIntent) error {
	if path == "" {
		return nil
	}
	return withLifecycleLock(lifecyclePathFromCoord(path), true, func() error {
		return removeStopCoordinationIfClaimLocked(path, expected)
	})
}

func removeStopCoordinationIfClaimLocked(path string, expected stopIntent) error {
	if path == "" {
		return nil
	}
	current, err := readStopIntent(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		// Unreadable payload: only safe when reclaiming incomplete/corrupt state
		// that could not parse as a claim. Leave concurrent writers alone.
		return nil
	}
	if !sameStopClaim(current, expected) {
		return nil
	}
	if !expected.Signaled && current.Signaled {
		// Original leader marked signaled after we observed unsignaled ownership loss.
		return nil
	}
	if afterStopClearDecisionHook != nil {
		// Tests publish a replacement claim after the identity match and before
		// the re-check below. Production holds the lifecycle lock across this
		// window so real concurrent writers serialize instead.
		afterStopClearDecisionHook(path, expected)
	}
	// Compare-and-remove: re-read under the same lifecycle lock immediately
	// before unlink so a replacement claim is preserved.
	current, err = readStopIntent(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return nil
	}
	if !sameStopClaim(current, expected) {
		return nil
	}
	if !expected.Signaled && current.Signaled {
		return nil
	}
	return removeStopCoordinationFile(path)
}

// releaseStopClaim drops an unsignaled claim only if this caller still owns it.
// After reclaim replacement, this is a no-op so the new leader is not disrupted.
func releaseStopClaim(path string, claim stopIntent) error {
	claim.Signaled = false
	return removeStopCoordinationIfClaim(path, claim)
}

func removeStopCoordinationFile(path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func remainingStopTimeout(deadline time.Time, timeout time.Duration) time.Duration {
	if timeout <= 0 || deadline.IsZero() {
		return timeout
	}
	remaining := time.Until(deadline)
	if remaining < 0 {
		return 0
	}
	return remaining
}

func ensureStopDeadline(deadline time.Time, timeout time.Duration) error {
	if timeout <= 0 || deadline.IsZero() {
		return nil
	}
	if !time.Now().Before(deadline) {
		return fmt.Errorf("daemon stop coordination did not complete within %s", timeout)
	}
	return nil
}

// revalidateTargetForSignal confirms the process still matches the verified
// target identity immediately before marking/signaling. Unlike Verify, this
// does not accept a fresh heartbeat as a substitute for process-start identity.
func revalidateTargetForSignal(meta PIDFile) error {
	if revalidateTargetHook != nil {
		return revalidateTargetHook(meta)
	}
	return checkTargetStillMatches(meta)
}

func checkTargetStillMatches(meta PIDFile) error {
	if meta.PID <= 0 || meta.Executable == "" || meta.Root == "" {
		return ErrUnverifiedProcess
	}
	alive, err := Alive(meta.PID)
	if err != nil {
		return err
	}
	if !alive {
		return ErrNotRunning
	}
	info, err := processInfoForTarget(meta.PID)
	if err != nil {
		// Fail closed: process-table metadata is required before authorizing a
		// graceful signal. Fresh heartbeat + Alive must not substitute for
		// start-time / executable identity (recycled PID hazard).
		return ErrUnverifiedProcess
	}
	if meta.ProcessStartedAt != "" && info.StartedAt != "" && meta.ProcessStartedAt != info.StartedAt {
		// PID was recycled by an unrelated process.
		return ErrUnverifiedProcess
	}
	if info.Executable != "" {
		if cleanPath(info.Executable) == cleanPath(meta.Executable) ||
			filepath.Base(info.Executable) == filepath.Base(meta.Executable) {
			return nil
		}
	}
	first := strings.Fields(info.Command)
	if len(first) > 0 {
		if cleanPath(first[0]) == cleanPath(meta.Executable) ||
			filepath.Base(first[0]) == filepath.Base(meta.Executable) {
			return nil
		}
	}
	// Start identity matched but executable strings differ (common for shell
	// wrappers). Trust start identity when both sides have it.
	if meta.ProcessStartedAt != "" && info.StartedAt != "" && meta.ProcessStartedAt == info.StartedAt {
		return nil
	}
	// Alive alone is insufficient; require a confirming identity signal.
	return ErrUnverifiedProcess
}

func processInfoForTarget(pid int) (ProcessInfoResult, error) {
	if processInfoForTargetHook != nil {
		return processInfoForTargetHook(pid)
	}
	return ProcessInfo(pid)
}

// waitExitForTarget waits until the target process is gone or its identity no
// longer matches meta (PID reuse). A recycled PID is treated as exit of the
// original target so followers do not wait on an unrelated process.
func waitExitForTarget(meta PIDFile, timeout time.Duration, action string) error {
	deadline := time.Now().Add(timeout)
	for {
		still, err := targetProcessStillRunning(meta)
		if err != nil {
			return err
		}
		if !still {
			return nil
		}
		if timeout <= 0 || time.Now().After(deadline) {
			return fmt.Errorf("daemon pid %d did not %s within %s", meta.PID, action, timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func targetProcessStillRunning(meta PIDFile) (bool, error) {
	if targetStillRunningHook != nil {
		return targetStillRunningHook(meta)
	}
	alive, err := Alive(meta.PID)
	if err != nil {
		return false, err
	}
	if !alive {
		return false, nil
	}
	if meta.ProcessStartedAt == "" {
		return true, nil
	}
	info, err := ProcessInfo(meta.PID)
	if err != nil {
		// Cannot confirm recycle; keep waiting on Alive to avoid false exit
		// while a shutting-down daemon is still the live PID.
		return true, nil
	}
	if info.StartedAt != "" && info.StartedAt != meta.ProcessStartedAt {
		return false, nil
	}
	return true, nil
}
