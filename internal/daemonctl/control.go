package daemonctl

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/shinpr/galley/internal/jsonio"
	"github.com/shinpr/galley/internal/pathutil"
)

// ErrNotRunning indicates the PID file is absent or points at a dead process.
var ErrNotRunning = errors.New("daemon is not running")

// ErrUnverifiedProcess indicates the PID exists but cannot be identified as the Galley daemon.
var ErrUnverifiedProcess = errors.New("pid file process identity is not verified")

// EnvToken carries the daemon control token from `galley daemon start` to the
// foreground daemon process without exposing it in argv.
const EnvToken = "GALLEY_DAEMON_TOKEN"

// Paths contains daemon control file paths.
type Paths struct {
	PIDFile string
	LogFile string
}

// PIDFile records enough process identity to avoid stopping a reused PID.
type PIDFile struct {
	PID              int      `json:"pid"`
	Executable       string   `json:"executable"`
	Root             string   `json:"root"`
	Argv             []string `json:"argv"`
	StartedAt        string   `json:"started_at"`
	ProcessStartedAt string   `json:"process_started_at,omitempty"`
	TokenHash        string   `json:"token_hash,omitempty"`
	Token            string   `json:"-"`
	HeartbeatAt      string   `json:"heartbeat_at,omitempty"`
}

// Status describes the state represented by a PID file.
type Status struct {
	Meta     PIDFile
	Exists   bool
	Alive    bool
	Verified bool
}

// ResolvePaths returns default control paths under root when explicit paths are empty.
func ResolvePaths(root, pidFile, logFile string) Paths {
	if pidFile == "" {
		pidFile = filepath.Join(root, "galley-daemon.pid")
	}
	if logFile == "" {
		logFile = filepath.Join(root, "galley-daemon.log")
	}
	return Paths{PIDFile: pidFile, LogFile: logFile}
}

// NewPIDFile builds PID metadata for a background daemon process.
func NewPIDFile(pid int, executable, root string, argv []string) PIDFile {
	processStartedAt := ""
	if info, err := ProcessInfo(pid); err == nil {
		processStartedAt = info.StartedAt
	}
	return PIDFile{
		PID:              pid,
		Executable:       cleanPath(executable),
		Root:             cleanPath(root),
		Argv:             sanitizeArgv(argv),
		StartedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		ProcessStartedAt: processStartedAt,
	}
}

// WithToken returns metadata with a daemon control token.
func (p PIDFile) WithToken(token string) PIDFile {
	p.Token = token
	p.TokenHash = tokenHash(token)
	return p
}

// WritePID writes PID metadata. Callers should hold ReservePID.
func WritePID(path string, meta PIDFile) error {
	if meta.PID <= 0 {
		return fmt.Errorf("invalid pid %d", meta.PID)
	}
	return jsonio.Write(path, meta)
}

// Heartbeat refreshes PID metadata if the PID and token still match.
//
// Heartbeat deliberately does not acquire the lifecycle lock: `daemon start`
// holds ReservePID across readiness wait while the child heartbeats, so a
// blocking lock here would deadlock. Shutdown stops the heartbeat goroutine
// before RemovePID so an in-flight refresh cannot recreate the PID file.
// A missing or replaced PID file fails closed without rewriting identity.
func Heartbeat(path string, meta PIDFile) error {
	current, err := ReadPIDFile(path)
	if err != nil {
		return err
	}
	if current.PID != meta.PID || current.TokenHash == "" || current.TokenHash != tokenHash(meta.Token) {
		return ErrUnverifiedProcess
	}
	current.HeartbeatAt = time.Now().UTC().Format(time.RFC3339Nano)
	// Re-check immediately before write so a concurrent RemovePID that already
	// unlinked (or a replacement identity) is not overwritten into a stale file.
	latest, err := ReadPIDFile(path)
	if err != nil {
		return err
	}
	if latest.PID != meta.PID || latest.TokenHash == "" || latest.TokenHash != tokenHash(meta.Token) {
		return ErrUnverifiedProcess
	}
	if latest.ProcessStartedAt != "" && meta.ProcessStartedAt != "" && latest.ProcessStartedAt != meta.ProcessStartedAt {
		return ErrUnverifiedProcess
	}
	current.ProcessStartedAt = latest.ProcessStartedAt
	current.Executable = latest.Executable
	current.Root = latest.Root
	current.TokenHash = latest.TokenHash
	return WritePID(path, current)
}

// RemovePID removes a PID file only when it still records the same process
// identity described by expected. PID must match. Any non-empty identity fields
// on expected (ProcessStartedAt, TokenHash, Executable, Root) must also match
// the on-disk record when those fields are present on disk. This prevents
// deleting a newly written daemon PID file after PID reuse.
//
// Callers that only know the integer PID may pass PIDFile{PID: pid}. Callers
// that observed full metadata (stop/start inspect) must pass it.
//
// Removal is compare-and-remove under the shared lifecycle lock so a concurrent
// daemon restart cannot lose its newly written PID file. Callers that already
// hold ReservePID must use RemovePIDHeld instead.
func RemovePID(path string, expected PIDFile) error {
	return withLifecycleLock(path, true, func() error {
		return removePIDLocked(path, expected)
	})
}

// RemovePIDHeld is RemovePID for callers that already hold ReservePID for path.
// Using RemovePID while holding the reservation would deadlock on the lock.
func RemovePIDHeld(path string, expected PIDFile) error {
	return removePIDLocked(path, expected)
}

// afterPIDIdentityCheckHook runs after a matching identity is observed and
// before the final re-check + unlink. Tests use it to interleave a replacement
// PID write between the initial identity check and unlink.
var afterPIDIdentityCheckHook func(path string, expected PIDFile)

func removePIDLocked(path string, expected PIDFile) error {
	meta, err := ReadPIDFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !pidFileMatchesExpected(meta, expected) {
		return nil
	}
	if afterPIDIdentityCheckHook != nil {
		afterPIDIdentityCheckHook(path, expected)
	}
	// Re-validate under the same lifecycle lock immediately before unlink so a
	// replacement published after the first read (for example via a test hook
	// that writes without the lock, or a future unlocked writer) is preserved.
	meta, err = ReadPIDFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !pidFileMatchesExpected(meta, expected) {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// pidFileMatchesExpected reports whether on-disk meta is still the record
// described by expected. Empty identity fields on expected are treated as
// wildcards so PID-only callers keep working; when expected carries identity,
// a mismatched recycled record is left in place.
func pidFileMatchesExpected(meta, expected PIDFile) bool {
	if expected.PID <= 0 || meta.PID != expected.PID {
		return false
	}
	if expected.ProcessStartedAt != "" && meta.ProcessStartedAt != "" && expected.ProcessStartedAt != meta.ProcessStartedAt {
		return false
	}
	if expected.TokenHash != "" && meta.TokenHash != "" && expected.TokenHash != meta.TokenHash {
		return false
	}
	if expected.Executable != "" && meta.Executable != "" && cleanPath(expected.Executable) != cleanPath(meta.Executable) {
		return false
	}
	if expected.Root != "" && meta.Root != "" && cleanPath(expected.Root) != cleanPath(meta.Root) {
		return false
	}
	return true
}

// ReadPID returns the PID from a PID file.
func ReadPID(path string) (int, error) {
	meta, err := ReadPIDFile(path)
	if err != nil {
		return 0, err
	}
	return meta.PID, nil
}

// ReadPIDFile reads PID metadata.
func ReadPIDFile(path string) (PIDFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return PIDFile{}, err
	}
	var meta PIDFile
	if err := json.Unmarshal(data, &meta); err == nil && meta.PID > 0 {
		meta.Executable = cleanPath(meta.Executable)
		meta.Root = cleanPath(meta.Root)
		meta.Argv = sanitizeArgv(meta.Argv)
		return meta, nil
	}
	return PIDFile{}, fmt.Errorf("invalid pid file %s", path)
}

// Inspect returns PID file state and verifies live process identity when possible.
func Inspect(path, expectedRoot, expectedExecutable string) (Status, error) {
	meta, err := ReadPIDFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Status{}, ErrNotRunning
	}
	if err != nil {
		return Status{}, err
	}
	alive, err := Alive(meta.PID)
	if err != nil {
		return Status{}, err
	}
	status := Status{Meta: meta, Exists: true, Alive: alive}
	if !alive {
		return status, nil
	}
	status.Verified = Verify(meta, expectedRoot, expectedExecutable)
	return status, nil
}

// Verify checks whether PID metadata matches the expected daemon identity.
func Verify(meta PIDFile, expectedRoot, expectedExecutable string) bool {
	if meta.PID <= 0 || meta.Executable == "" {
		return false
	}
	if expectedRoot != "" && meta.Root != "" && meta.Root != cleanPath(expectedRoot) {
		return false
	}
	if expectedExecutable != "" && meta.Executable != cleanPath(expectedExecutable) {
		return false
	}
	if freshHeartbeat(meta, 10*time.Second) {
		return true
	}
	info, err := ProcessInfo(meta.PID)
	if err != nil {
		return false
	}
	if meta.ProcessStartedAt != "" && info.StartedAt != "" && meta.ProcessStartedAt != info.StartedAt {
		return false
	}
	if info.Executable != "" {
		if cleanPath(info.Executable) == meta.Executable || filepath.Base(info.Executable) == filepath.Base(meta.Executable) {
			return true
		}
	}
	first := strings.Fields(info.Command)
	if len(first) == 0 {
		return false
	}
	return cleanPath(first[0]) == meta.Executable || filepath.Base(first[0]) == filepath.Base(meta.Executable)
}

func freshHeartbeat(meta PIDFile, maxAge time.Duration) bool {
	if meta.TokenHash == "" || meta.HeartbeatAt == "" {
		return false
	}
	heartbeatAt, err := time.Parse(time.RFC3339Nano, meta.HeartbeatAt)
	if err != nil {
		return false
	}
	return time.Since(heartbeatAt) <= maxAge
}

func tokenHash(token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func sanitizeArgv(argv []string) []string {
	out := make([]string, 0, len(argv))
	skipNext := false
	for _, arg := range argv {
		if skipNext {
			skipNext = false
			continue
		}
		if arg == "--daemon-token" {
			skipNext = true
			continue
		}
		if strings.HasPrefix(arg, "--daemon-token=") {
			continue
		}
		out = append(out, arg)
	}
	return out
}

// StopVerified sends a graceful stop signal only to a process verified against
// its PID metadata. This is the direct, uncoordinated path used by tests and
// any caller that intentionally bypasses cooperating-stop CLI coordination.
func StopVerified(meta PIDFile, timeout time.Duration) error {
	if !Verify(meta, meta.Root, meta.Executable) {
		return ErrUnverifiedProcess
	}
	return Stop(meta.PID, timeout)
}

// ForceStop requests graceful shutdown and, if the process is still alive after
// timeout, sends a verified SIGKILL. The boolean result reports whether the
// force kill was needed. Immediately before the kill, identity is revalidated
// with a fresh process-start check (not heartbeat) so a recycled PID after a
// follower timeout cannot be SIGKILL'd.
//
// When pidFile is non-empty, the graceful phase uses CoordinatedStopVerified so
// an explicit --force request during an in-progress normal stop does not send a
// second graceful signal; it waits for the existing shutdown window, then kills
// if the process is still alive.
func ForceStop(meta PIDFile, timeout time.Duration, pidFile string) (bool, error) {
	var err error
	if pidFile != "" {
		err = CoordinatedStopVerified(meta, timeout, pidFile)
	} else {
		err = StopVerified(meta, timeout)
	}
	if err == nil || errors.Is(err, ErrNotRunning) {
		return false, nil
	}
	if !errors.Is(err, ErrUnverifiedProcess) {
		// Graceful stop timed out (or another transient failure): fall through to
		// the verified force kill below. Unverified-process errors are returned as
		// is so callers do not escalate to SIGKILL against an unknown process.
		//
		// Revalidate with process-start identity immediately before kill. Alive
		// alone is insufficient after a coordinated follower wait: the PID may
		// have been recycled, and a stale heartbeat must not authorize SIGKILL.
		if revalErr := revalidateTargetForKill(meta); revalErr != nil {
			if errors.Is(revalErr, ErrNotRunning) {
				_ = ClearStopCoordination(pidFile, meta)
				return false, nil
			}
			return false, revalErr
		}
		if killErr := KillVerified(meta, timeout); killErr != nil && !errors.Is(killErr, ErrNotRunning) {
			return false, killErr
		}
		_ = ClearStopCoordination(pidFile, meta)
		return true, nil
	}
	return false, err
}

// KillVerified sends SIGKILL only after a fresh process-start identity check.
// Unlike Verify, a fresh heartbeat alone never authorizes a kill.
func KillVerified(meta PIDFile, timeout time.Duration) error {
	if err := revalidateTargetForKill(meta); err != nil {
		return err
	}
	return Kill(meta.PID, timeout)
}

// revalidateTargetForKill confirms the live process still matches meta using
// process-table start identity. It fails closed when ProcessInfo is unavailable
// so force escalation cannot target a recycled PID.
func revalidateTargetForKill(meta PIDFile) error {
	return checkTargetStillMatches(meta)
}

// Kill sends SIGKILL and waits for process exit until timeout.
func Kill(pid int, timeout time.Duration) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := process.Kill(); err != nil {
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return ErrNotRunning
		}
		return err
	}
	return waitExit(pid, timeout, "exit after SIGKILL")
}

func waitExit(pid int, timeout time.Duration, action string) error {
	deadline := time.Now().Add(timeout)
	for {
		alive, err := Alive(pid)
		if err != nil {
			return err
		}
		if !alive {
			return nil
		}
		if timeout <= 0 || time.Now().After(deadline) {
			return fmt.Errorf("daemon pid %d did not %s within %s", pid, action, timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func cleanPath(path string) string {
	return pathutil.CleanPhysical(path)
}
