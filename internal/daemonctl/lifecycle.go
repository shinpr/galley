package daemonctl

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Lifecycle lock serializes PID-file and stop-coordination mutations across
// cooperating start and stop callers. The lock file is path+".lock", the same
// exclusive marker ReservePID has always used.
//
// Holding the lock makes identity check + unlink compare-and-remove safe: a
// concurrent restart cannot publish a replacement record between validation and
// removal, and a stale stop cannot delete a new daemon's PID or .stop claim.
//
// Ownership is recorded in the lock payload so a killed stop/start caller cannot
// permanently block later lifecycle operations. Release is compare-and-remove so
// a stale holder cannot delete a lock reclaimed by a later caller.

const (
	lifecycleLockBlockTimeout = 30 * time.Second
	lifecycleLockPollInterval = 10 * time.Millisecond
	// maxLifecycleLockLease is the backstop reclaim window for a lock whose
	// owner still appears live but may be wedged, or whose ownership payload is
	// missing/unparseable (legacy empty .lock files).
	maxLifecycleLockLease = 30 * time.Second
	// recentLifecycleLockGrace protects a concurrent creator still publishing
	// ownership bytes after O_EXCL create.
	recentLifecycleLockGrace = 2 * time.Second
	// maxDropFenceLease is the backstop reclaim window for a generation drop
	// fence (.lock.drop.<claim>) whose dropper appears live but may have exited
	// after O_EXCL create and before rename/removal. Without this, an abandoned
	// fence permanently vetoes reclamation of that generation.
	maxDropFenceLease = 30 * time.Second
	// recentDropFenceGrace protects a concurrent dropper still publishing fence
	// ownership bytes after O_EXCL create.
	recentDropFenceGrace = 2 * time.Second
	// maxDropFenceAttempts bounds retries when reclaiming abandoned drop fences.
	maxDropFenceAttempts = 4
)

// lifecycleLockOwner identifies the process that holds path+".lock".
type lifecycleLockOwner struct {
	OwnerPID              int    `json:"owner_pid"`
	OwnerProcessStartedAt string `json:"owner_process_started_at,omitempty"`
	AcquiredAt            string `json:"acquired_at"`
	ClaimID               string `json:"claim_id,omitempty"`
}

// lifecycleProcessInfoHook, when set, replaces ProcessInfo inside
// lifecycleLockIsStale. Tests inject transient process-table failures.
var lifecycleProcessInfoHook func(pid int) (ProcessInfoResult, error)

func processInfoForLifecycle(pid int) (ProcessInfoResult, error) {
	if lifecycleProcessInfoHook != nil {
		return lifecycleProcessInfoHook(pid)
	}
	return ProcessInfo(pid)
}

// ReservePID creates an exclusive lifecycle lock for path. Call the returned
// function to release it. Fails immediately if another live caller already holds
// the lock so concurrent `daemon start` attempts do not wait on each other.
// Stale locks (dead/recycled owner or expired lease) are reclaimed.
//
// While the reservation is held, use RemovePIDHeld / ClearStopCoordinationHeld
// (or WritePID) rather than the locking public helpers, which would block
// forever waiting for this process's own lock.
func ReservePID(path string) (func(), error) {
	return acquireLifecycleLock(path, false)
}

// LifecycleLockPath returns the exclusive lock path beside a PID file.
func LifecycleLockPath(pidFile string) string {
	if pidFile == "" {
		return ""
	}
	return pidFile + ".lock"
}

func acquireLifecycleLock(path string, block bool) (func(), error) {
	if path == "" {
		return func() {}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create pid dir: %w", err)
	}
	lockPath := LifecycleLockPath(path)
	deadline := time.Now().Add(lifecycleLockBlockTimeout)
	for {
		owner, err := newLifecycleLockOwner()
		if err != nil {
			return nil, err
		}
		claimed, err := tryClaimLifecycleLock(lockPath, owner)
		if err != nil {
			return nil, err
		}
		if claimed {
			return func() {
				releaseLifecycleLock(lockPath, owner)
			}, nil
		}
		reclaimed, err := tryReclaimStaleLifecycleLock(lockPath)
		if err != nil {
			return nil, err
		}
		if reclaimed {
			continue
		}
		if !block {
			return nil, fmt.Errorf("pid file is locked: %s", lockPath)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timeout waiting for pid lifecycle lock: %s", lockPath)
		}
		time.Sleep(lifecycleLockPollInterval)
	}
}

// withLifecycleLock runs fn while holding the exclusive lifecycle lock for path.
// When block is true, waits up to lifecycleLockBlockTimeout for the lock and
// reclaims provably stale ownership along the way.
func withLifecycleLock(path string, block bool, fn func() error) error {
	release, err := acquireLifecycleLock(path, block)
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

// lifecyclePathFromCoord maps a stop-coordination path back to its PID file so
// claim and cleanup share the same lifecycle lock as start/stop.
func lifecyclePathFromCoord(coordPath string) string {
	if coordPath == "" {
		return ""
	}
	const suffix = ".stop"
	if len(coordPath) > len(suffix) && coordPath[len(coordPath)-len(suffix):] == suffix {
		return coordPath[:len(coordPath)-len(suffix)]
	}
	return coordPath
}

func newLifecycleLockOwner() (lifecycleLockOwner, error) {
	owner := lifecycleLockOwner{
		OwnerPID:   os.Getpid(),
		AcquiredAt: time.Now().UTC().Format(time.RFC3339Nano),
		ClaimID:    newClaimID(),
	}
	if info, err := ProcessInfo(os.Getpid()); err == nil {
		owner.OwnerProcessStartedAt = info.StartedAt
	}
	return owner, nil
}

func tryClaimLifecycleLock(lockPath string, owner lifecycleLockOwner) (bool, error) {
	data, err := json.MarshalIndent(owner, "", "  ")
	if err != nil {
		return false, err
	}
	data = append(data, '\n')
	file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(lockPath)
		return false, fmt.Errorf("write lifecycle lock: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(lockPath)
		return false, fmt.Errorf("sync lifecycle lock: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(lockPath)
		return false, fmt.Errorf("close lifecycle lock: %w", err)
	}
	return true, nil
}

// tryReclaimStaleLifecycleLock removes a lock only when ownership is provably
// stale. Live matching owners within the lease are left alone. Transient
// ProcessInfo failure for a live PID is not treated as proof of staleness.
func tryReclaimStaleLifecycleLock(lockPath string) (bool, error) {
	owner, err := readLifecycleLockOwner(lockPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		// Empty/corrupt/legacy lock: reclaim only after a short grace so a
		// concurrent creator can finish publishing ownership bytes.
		info, statErr := os.Stat(lockPath)
		if errors.Is(statErr, os.ErrNotExist) {
			return false, nil
		}
		if statErr != nil {
			return false, statErr
		}
		if time.Since(info.ModTime()) < recentLifecycleLockGrace {
			return false, nil
		}
		// Re-read after grace: if a concurrent creator finished publishing a
		// valid owner, do not blind-drop their claim.
		if owner2, readErr := readLifecycleLockOwner(lockPath); readErr == nil {
			stale, staleErr := lifecycleLockIsStale(owner2, lockPath)
			if staleErr != nil {
				return false, staleErr
			}
			if !stale {
				return false, nil
			}
			return removeLifecycleLockIfOwner(lockPath, owner2)
		}
		// Still unreadable: generation-fenced drop of the corrupt generation.
		return dropLifecycleLockGeneration(lockPath, "corrupt", nil)
	}
	stale, err := lifecycleLockIsStale(owner, lockPath)
	if err != nil {
		return false, err
	}
	if !stale {
		return false, nil
	}
	// Generation-fenced handoff: only retire the observed ownership payload.
	return removeLifecycleLockIfOwner(lockPath, owner)
}

func lifecycleLockIsStale(owner lifecycleLockOwner, lockPath string) (bool, error) {
	if owner.OwnerPID <= 0 {
		return true, nil
	}
	alive, err := Alive(owner.OwnerPID)
	if err != nil {
		// Cannot confirm liveness: treat as stale so lifecycle cannot wedge.
		return true, nil
	}
	if !alive {
		return true, nil
	}
	if owner.OwnerProcessStartedAt != "" {
		info, infoErr := processInfoForLifecycle(owner.OwnerPID)
		if infoErr != nil {
			// Live PID with a transient process-table lookup failure is NOT
			// proof the owner is stale. Fall through to the lease clock so a
			// healthy holder is not reclaimed solely because ProcessInfo failed.
		} else if info.StartedAt != "" && info.StartedAt != owner.OwnerProcessStartedAt {
			// Owner PID recycled by an unrelated process.
			return true, nil
		}
	}
	// Lease clock: parseable AcquiredAt, else filesystem mtime for legacy.
	var age time.Duration
	if acquired, parseErr := time.Parse(time.RFC3339Nano, owner.AcquiredAt); parseErr == nil {
		age = time.Since(acquired)
	} else {
		info, statErr := os.Stat(lockPath)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				return false, nil
			}
			return false, statErr
		}
		age = time.Since(info.ModTime())
	}
	if age > maxLifecycleLockLease {
		return true, nil
	}
	// Live owner without start identity and without parseable AcquiredAt already
	// used mtime above. A live owner with only PID and a parseable lease is
	// treated as holding the lock until the lease expires. ProcessInfo failure
	// alone never short-circuits that lease.
	return false, nil
}

func readLifecycleLockOwner(lockPath string) (lifecycleLockOwner, error) {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return lifecycleLockOwner{}, err
	}
	if len(data) == 0 {
		return lifecycleLockOwner{}, fmt.Errorf("empty lifecycle lock %s", lockPath)
	}
	var owner lifecycleLockOwner
	if err := json.Unmarshal(data, &owner); err != nil {
		return lifecycleLockOwner{}, fmt.Errorf("invalid lifecycle lock %s: %w", lockPath, err)
	}
	if owner.OwnerPID <= 0 {
		return lifecycleLockOwner{}, fmt.Errorf("invalid lifecycle lock %s: missing owner_pid", lockPath)
	}
	return owner, nil
}

// releaseLifecycleLock compare-and-removes the lock only when this owner still
// holds it. After reclaim by another caller, this is a no-op.
func releaseLifecycleLock(lockPath string, owner lifecycleLockOwner) {
	_, _ = removeLifecycleLockIfOwner(lockPath, owner)
}

// afterLifecycleOwnerCheckHook runs after a matching ownership payload is
// observed and before the generation-fenced drop. Tests interleave a
// replacement claim between the identity match and the handoff rename.
var afterLifecycleOwnerCheckHook func(lockPath string, expected lifecycleLockOwner)

func removeLifecycleLockIfOwner(lockPath string, expected lifecycleLockOwner) (bool, error) {
	current, err := readLifecycleLockOwner(lockPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		// Unreadable: do not blindly unlink; let the next acquire use the
		// corrupt-path grace + reclaim once the file is old enough.
		return false, nil
	}
	if !sameLifecycleLockOwner(current, expected) {
		return false, nil
	}
	if afterLifecycleOwnerCheckHook != nil {
		afterLifecycleOwnerCheckHook(lockPath, expected)
	}
	generation := expected.ClaimID
	if generation == "" {
		generation = fmt.Sprintf("%d-%s", expected.OwnerPID, expected.AcquiredAt)
	}
	return dropLifecycleLockGeneration(lockPath, generation, &expected)
}

// dropFenceOwner identifies the process holding a generation drop fence so an
// abandoned .lock.drop.<claim> can be reclaimed without guessing.
type dropFenceOwner struct {
	DropperPID              int    `json:"dropper_pid"`
	DropperProcessStartedAt string `json:"dropper_process_started_at,omitempty"`
	AcquiredAt              string `json:"acquired_at"`
	Generation              string `json:"generation,omitempty"`
}

// dropLifecycleLockGeneration retires the lock via an exclusive generation
// fence and rename. When expected is non-nil, ownership is re-checked after
// the fence is held and immediately before rename so a replacement claim
// published during the handoff is preserved.
//
// If a prior process created the fence and exited before rename/removal, the
// abandoned tombstone is reclaimed via a safe generation/lease check so it
// cannot permanently veto future reclamation of that generation.
func dropLifecycleLockGeneration(lockPath, generation string, expected *lifecycleLockOwner) (bool, error) {
	if generation == "" {
		generation = "unknown"
	}
	sanitized := sanitizeLifecycleGeneration(generation)
	tombPath := lockPath + ".drop." + sanitized

	for attempt := 0; attempt < maxDropFenceAttempts; attempt++ {
		held, err := tryCreateDropFence(tombPath, sanitized)
		if err != nil {
			return false, err
		}
		if !held {
			// Another drop of this generation is in progress, completed, or
			// left an abandoned fence. Reclaim only when the fence is provably
			// stale so a live concurrent dropper is not disrupted.
			reclaimed, recErr := reclaimAbandonedDropFence(tombPath)
			if recErr != nil {
				return false, recErr
			}
			if reclaimed {
				continue
			}
			return false, nil
		}
		return completeDropWithFence(lockPath, tombPath, expected)
	}
	return false, nil
}

// completeDropWithFence runs the ownership re-check and rename while the caller
// holds the exclusive generation fence at tombPath. The fence is always removed
// on exit (success or aborted handoff).
func completeDropWithFence(lockPath, tombPath string, expected *lifecycleLockOwner) (bool, error) {
	cleanupFence := true
	defer func() {
		if cleanupFence {
			_ = os.Remove(tombPath)
		}
	}()

	if expected != nil {
		current, readErr := readLifecycleLockOwner(lockPath)
		if errors.Is(readErr, os.ErrNotExist) {
			return false, nil
		}
		if readErr != nil {
			return false, nil
		}
		if !sameLifecycleLockOwner(current, *expected) {
			// Replacement claim (or other generation) now owns the lock.
			return false, nil
		}
	}

	retiredPath := tombPath + ".retired"
	if err := os.Rename(lockPath, retiredPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		// Fallback when rename cannot place the retired name: only remove when
		// ownership still matches the expected generation.
		if expected != nil {
			current, readErr := readLifecycleLockOwner(lockPath)
			if readErr != nil || !sameLifecycleLockOwner(current, *expected) {
				return false, nil
			}
		}
		if rmErr := os.Remove(lockPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return false, rmErr
		}
		cleanupFence = false
		_ = os.Remove(tombPath)
		_ = os.Remove(retiredPath)
		return true, nil
	}
	cleanupFence = false
	_ = os.Remove(tombPath)
	_ = os.Remove(retiredPath)
	return true, nil
}

// tryCreateDropFence exclusively creates the generation drop fence and records
// dropper identity so a later reclaim can distinguish an abandoned handoff from
// a live concurrent drop.
func tryCreateDropFence(tombPath, generation string) (bool, error) {
	owner := dropFenceOwner{
		DropperPID: os.Getpid(),
		AcquiredAt: time.Now().UTC().Format(time.RFC3339Nano),
		Generation: generation,
	}
	if info, err := ProcessInfo(os.Getpid()); err == nil {
		owner.DropperProcessStartedAt = info.StartedAt
	}
	data, err := json.MarshalIndent(owner, "", "  ")
	if err != nil {
		return false, err
	}
	data = append(data, '\n')
	file, err := os.OpenFile(tombPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(tombPath)
		return false, fmt.Errorf("write drop fence: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(tombPath)
		return false, fmt.Errorf("sync drop fence: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tombPath)
		return false, fmt.Errorf("close drop fence: %w", err)
	}
	return true, nil
}

// reclaimAbandonedDropFence removes a drop fence (and its .retired sibling)
// only when the dropper is provably stale or the fence payload is old enough to
// treat as abandoned. A live concurrent dropper within the lease is left alone.
func reclaimAbandonedDropFence(tombPath string) (bool, error) {
	owner, err := readDropFenceOwner(tombPath)
	if errors.Is(err, os.ErrNotExist) {
		// Fence already gone; caller may retry create.
		return true, nil
	}
	if err != nil {
		// Empty/corrupt fence: reclaim only after grace so a concurrent dropper
		// can finish publishing ownership bytes.
		info, statErr := os.Stat(tombPath)
		if errors.Is(statErr, os.ErrNotExist) {
			return true, nil
		}
		if statErr != nil {
			return false, statErr
		}
		if time.Since(info.ModTime()) < recentDropFenceGrace {
			return false, nil
		}
		return removeDropFenceArtifacts(tombPath)
	}
	stale, staleErr := dropFenceIsStale(owner, tombPath)
	if staleErr != nil {
		return false, staleErr
	}
	if !stale {
		return false, nil
	}
	return removeDropFenceArtifacts(tombPath)
}

func removeDropFenceArtifacts(tombPath string) (bool, error) {
	// Best-effort: a prior handoff may have renamed the lock beside the fence.
	_ = os.Remove(tombPath + ".retired")
	if err := os.Remove(tombPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return true, nil
}

func readDropFenceOwner(tombPath string) (dropFenceOwner, error) {
	data, err := os.ReadFile(tombPath)
	if err != nil {
		return dropFenceOwner{}, err
	}
	if len(data) == 0 {
		return dropFenceOwner{}, fmt.Errorf("empty drop fence %s", tombPath)
	}
	var owner dropFenceOwner
	if err := json.Unmarshal(data, &owner); err != nil {
		return dropFenceOwner{}, fmt.Errorf("invalid drop fence %s: %w", tombPath, err)
	}
	if owner.DropperPID <= 0 {
		return dropFenceOwner{}, fmt.Errorf("invalid drop fence %s: missing dropper_pid", tombPath)
	}
	return owner, nil
}

func dropFenceIsStale(owner dropFenceOwner, tombPath string) (bool, error) {
	if owner.DropperPID <= 0 {
		return true, nil
	}
	alive, err := Alive(owner.DropperPID)
	if err != nil {
		// Cannot confirm liveness: treat as stale so a wedge cannot pin reclaim.
		return true, nil
	}
	if !alive {
		return true, nil
	}
	if owner.DropperProcessStartedAt != "" {
		info, infoErr := processInfoForLifecycle(owner.DropperPID)
		if infoErr != nil {
			// Live dropper with transient ProcessInfo failure is not proof the
			// fence is abandoned; fall through to the lease clock.
		} else if info.StartedAt != "" && info.StartedAt != owner.DropperProcessStartedAt {
			// Dropper PID recycled by an unrelated process.
			return true, nil
		}
	}
	var age time.Duration
	if acquired, parseErr := time.Parse(time.RFC3339Nano, owner.AcquiredAt); parseErr == nil {
		age = time.Since(acquired)
	} else {
		info, statErr := os.Stat(tombPath)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				return false, nil
			}
			return false, statErr
		}
		age = time.Since(info.ModTime())
	}
	return age > maxDropFenceLease, nil
}

// lifecycleDropFencePath returns the generation drop fence path for tests and
// diagnostics. generation is sanitized the same way as production drops.
func lifecycleDropFencePath(lockPath, generation string) string {
	if lockPath == "" {
		return ""
	}
	if generation == "" {
		generation = "unknown"
	}
	return lockPath + ".drop." + sanitizeLifecycleGeneration(generation)
}

func sanitizeLifecycleGeneration(generation string) string {
	// Keep tombstone paths filesystem-safe and bounded.
	const maxLen = 80
	out := make([]byte, 0, len(generation))
	for i := 0; i < len(generation); i++ {
		c := generation[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
		if len(out) >= maxLen {
			break
		}
	}
	if len(out) == 0 {
		return "gen"
	}
	return string(out)
}

func sameLifecycleLockOwner(a, b lifecycleLockOwner) bool {
	if a.ClaimID != "" && b.ClaimID != "" {
		return a.ClaimID == b.ClaimID
	}
	return a.OwnerPID == b.OwnerPID &&
		a.OwnerProcessStartedAt == b.OwnerProcessStartedAt &&
		a.AcquiredAt == b.AcquiredAt
}
