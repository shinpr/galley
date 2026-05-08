package daemonctl

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrNotRunning indicates the PID file is absent or points at a dead process.
var ErrNotRunning = errors.New("daemon is not running")

// ErrUnverifiedProcess indicates the PID exists but cannot be identified as galleyd.
var ErrUnverifiedProcess = errors.New("pid file process identity is not verified")

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
	Token            string   `json:"token,omitempty"`
	HeartbeatAt      string   `json:"heartbeat_at,omitempty"`
	Legacy           bool     `json:"-"`
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
		pidFile = filepath.Join(root, "galleyd.pid")
	}
	if logFile == "" {
		logFile = filepath.Join(root, "galleyd.log")
	}
	return Paths{PIDFile: pidFile, LogFile: logFile}
}

// ReservePID creates an exclusive PID lock. Call the returned function to release it.
func ReservePID(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create pid dir: %w", err)
	}
	lockPath := path + ".lock"
	file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("pid file is locked: %s", lockPath)
		}
		return nil, err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(lockPath)
		return nil, err
	}
	return func() {
		_ = os.Remove(lockPath)
	}, nil
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
		Argv:             append([]string(nil), argv...),
		StartedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		ProcessStartedAt: processStartedAt,
	}
}

// WithToken returns metadata with a daemon control token.
func (p PIDFile) WithToken(token string) PIDFile {
	p.Token = token
	return p
}

// WritePID writes PID metadata. Callers should hold ReservePID.
func WritePID(path string, meta PIDFile) error {
	if meta.PID <= 0 {
		return fmt.Errorf("invalid pid %d", meta.PID)
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Heartbeat refreshes PID metadata if the PID and token still match.
func Heartbeat(path string, meta PIDFile) error {
	current, err := ReadPIDFile(path)
	if err != nil {
		return err
	}
	if current.PID != meta.PID || current.Token == "" || current.Token != meta.Token {
		return ErrUnverifiedProcess
	}
	current.HeartbeatAt = time.Now().UTC().Format(time.RFC3339Nano)
	return WritePID(path, current)
}

// RemovePID removes a PID file if it still points at pid.
func RemovePID(path string, pid int) error {
	meta, err := ReadPIDFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if meta.PID != pid {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ReadPID returns the PID from a PID file.
func ReadPID(path string) (int, error) {
	meta, err := ReadPIDFile(path)
	if err != nil {
		return 0, err
	}
	return meta.PID, nil
}

// ReadPIDFile reads PID metadata. Bare PID files are accepted as legacy metadata.
func ReadPIDFile(path string) (PIDFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return PIDFile{}, err
	}
	var meta PIDFile
	if err := json.Unmarshal(data, &meta); err == nil && meta.PID > 0 {
		meta.Executable = cleanPath(meta.Executable)
		meta.Root = cleanPath(meta.Root)
		return meta, nil
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return PIDFile{}, fmt.Errorf("invalid pid file %s", path)
	}
	return PIDFile{PID: pid, Legacy: true}, nil
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
	if meta.Legacy || meta.PID <= 0 || meta.Executable == "" {
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
	if meta.Token == "" || meta.HeartbeatAt == "" {
		return false
	}
	heartbeatAt, err := time.Parse(time.RFC3339Nano, meta.HeartbeatAt)
	if err != nil {
		return false
	}
	return time.Since(heartbeatAt) <= maxAge
}

// Alive reports whether pid exists.
func Alive(pid int) (bool, error) {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false, err
	}
	err = process.Signal(syscall.Signal(0))
	if err == nil {
		if Zombie(pid) {
			return false, nil
		}
		return true, nil
	}
	if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	if errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	return false, err
}

// StopVerified sends SIGTERM only to a process verified against its PID metadata.
func StopVerified(meta PIDFile, timeout time.Duration) error {
	if !Verify(meta, meta.Root, meta.Executable) {
		return ErrUnverifiedProcess
	}
	return Stop(meta.PID, timeout)
}

// Stop sends SIGTERM and waits for process exit until timeout.
func Stop(pid int, timeout time.Duration) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return ErrNotRunning
		}
		return err
	}
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
			return fmt.Errorf("daemon pid %d did not stop within %s", pid, timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func cleanPath(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if evaluated, err := filepath.EvalSymlinks(abs); err == nil {
		return evaluated
	}
	return filepath.Clean(abs)
}
