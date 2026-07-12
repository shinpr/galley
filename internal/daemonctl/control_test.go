package daemonctl

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolvePathsDefaultsUnderRoot(t *testing.T) {
	t.Parallel()
	paths := ResolvePaths("/tmp/galley", "", "")
	if paths.PIDFile != filepath.Join("/tmp/galley", "galley-daemon.pid") {
		t.Fatalf("pid file got %q", paths.PIDFile)
	}
	if paths.LogFile != filepath.Join("/tmp/galley", "galley-daemon.log") {
		t.Fatalf("log file got %q", paths.LogFile)
	}
}

func TestWriteReadPIDFileJSON(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "galley-daemon.pid")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	meta := NewPIDFile(os.Getpid(), exe, t.TempDir(), []string{exe})
	if err := WritePID(path, meta); err != nil {
		t.Fatal(err)
	}
	read, err := ReadPIDFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if read.PID != os.Getpid() || read.Executable != meta.Executable || read.Root != meta.Root {
		t.Fatalf("metadata got %#v, want %#v", read, meta)
	}
}

func TestWritePIDFileStoresTokenHashOnly(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "galley-daemon.pid")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	meta := NewPIDFile(os.Getpid(), exe, t.TempDir(), []string{exe, "--daemon-token", "secret"}).WithToken("secret")
	if err := WritePID(path, meta); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("secret")) || bytes.Contains(data, []byte("--daemon-token")) {
		t.Fatalf("pid file leaked token material: %s", data)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["token_hash"] == "" || raw["token"] != nil {
		t.Fatalf("token fields got %#v", raw)
	}
}

func TestInspectVerifiesCurrentProcess(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "galley-daemon.pid")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	meta := NewPIDFile(os.Getpid(), exe, root, []string{exe}).WithToken("test-token")
	if err := WritePID(path, meta); err != nil {
		t.Fatal(err)
	}
	if err := Heartbeat(path, meta); err != nil {
		t.Fatal(err)
	}
	status, err := Inspect(path, root, exe)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Alive || !status.Verified {
		t.Fatalf("status got %#v", status)
	}
}

func TestHeartbeatRequiresMatchingToken(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "galley-daemon.pid")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	meta := NewPIDFile(os.Getpid(), exe, t.TempDir(), []string{exe}).WithToken("token-a")
	if err := WritePID(path, meta); err != nil {
		t.Fatal(err)
	}
	wrong := meta
	wrong.Token = "token-b"
	if err := Heartbeat(path, wrong); !errors.Is(err, ErrUnverifiedProcess) {
		t.Fatalf("expected unverified heartbeat, got %v", err)
	}
	if err := Heartbeat(path, meta); err != nil {
		t.Fatal(err)
	}
}

func TestInspectRejectsMismatchedRoot(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "galley-daemon.pid")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := WritePID(path, NewPIDFile(os.Getpid(), exe, t.TempDir(), []string{exe})); err != nil {
		t.Fatal(err)
	}
	status, err := Inspect(path, t.TempDir(), exe)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Alive || status.Verified {
		t.Fatalf("status got %#v", status)
	}
}

func TestReadPIDFileRejectsBarePID(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "galley-daemon.pid")
	if err := os.WriteFile(path, []byte("123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPIDFile(path); err == nil {
		t.Fatal("expected bare PID file to be rejected")
	}
}

func TestRemovePIDOnlyRemovesMatchingPID(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "galley-daemon.pid")
	if err := WritePID(path, PIDFile{PID: 123}); err != nil {
		t.Fatal(err)
	}
	if err := RemovePID(path, PIDFile{PID: 456}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("pid file should remain: %v", err)
	}
	if err := RemovePID(path, PIDFile{PID: 123}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pid file should be removed, err=%v", err)
	}
}

func TestRemovePIDPreservesIdentityMismatch(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "galley-daemon.pid")
	onDisk := PIDFile{
		PID:              123,
		Executable:       "/usr/bin/galley",
		Root:             "/tmp/galley-a",
		ProcessStartedAt: "start-a",
		TokenHash:        "hash-a",
	}
	if err := WritePID(path, onDisk); err != nil {
		t.Fatal(err)
	}
	// Same PID, different start identity: must not delete a recycled record.
	if err := RemovePID(path, PIDFile{
		PID:              123,
		Executable:       "/usr/bin/galley",
		Root:             "/tmp/galley-a",
		ProcessStartedAt: "start-b",
		TokenHash:        "hash-a",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("identity-mismatched pid file should remain: %v", err)
	}
	if err := RemovePID(path, onDisk); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("matching identity should remove pid file, err=%v", err)
	}
}

// TestRemovePIDPreservesReplacementBetweenCheckAndUnlink covers the restart
// race: after an old stop validates the stale identity, a new daemon record is
// published before unlink. Compare-and-remove must keep the replacement.
func TestRemovePIDPreservesReplacementBetweenCheckAndUnlink(t *testing.T) {
	// Not parallel: swaps package-level afterPIDIdentityCheckHook.
	path := filepath.Join(t.TempDir(), "galley-daemon.pid")
	old := PIDFile{
		PID:              111,
		Executable:       "/usr/bin/galley",
		Root:             "/tmp/galley-old",
		ProcessStartedAt: "start-old",
		TokenHash:        "hash-old",
	}
	replacement := PIDFile{
		PID:              222,
		Executable:       "/usr/bin/galley",
		Root:             "/tmp/galley-new",
		ProcessStartedAt: "start-new",
		TokenHash:        "hash-new",
	}
	if err := WritePID(path, old); err != nil {
		t.Fatal(err)
	}

	prev := afterPIDIdentityCheckHook
	afterPIDIdentityCheckHook = func(p string, expected PIDFile) {
		if p != path || expected.PID != old.PID {
			t.Fatalf("hook path/expected mismatch: path=%q expected=%+v", p, expected)
		}
		// Simulate daemon restart publishing a new PID record after the old
		// stop's identity check and before unlink (unlocked write models a
		// hostile interleaving; production writers hold the lifecycle lock).
		if err := WritePID(p, replacement); err != nil {
			t.Errorf("replacement write: %v", err)
		}
	}
	defer func() { afterPIDIdentityCheckHook = prev }()

	if err := RemovePID(path, old); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPIDFile(path)
	if err != nil {
		t.Fatalf("replacement PID file must remain: %v", err)
	}
	if got.PID != replacement.PID || got.ProcessStartedAt != replacement.ProcessStartedAt || got.TokenHash != replacement.TokenHash {
		t.Fatalf("replacement identity lost: got %+v want %+v", got, replacement)
	}
}

// TestRemovePIDBlocksWhileStartHoldsLifecycleLock ensures stop cleanup and
// daemon start share the same exclusive lifecycle lock token.
func TestRemovePIDBlocksWhileStartHoldsLifecycleLock(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "galley-daemon.pid")
	meta := PIDFile{
		PID:              333,
		Executable:       "/usr/bin/galley",
		Root:             "/tmp/galley-lock",
		ProcessStartedAt: "start-lock",
		TokenHash:        "hash-lock",
	}
	if err := WritePID(path, meta); err != nil {
		t.Fatal(err)
	}
	release, err := ReservePID(path)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- RemovePID(path, meta)
	}()

	// While start holds the reservation, public RemovePID must wait rather than
	// unlinking without the shared lock.
	select {
	case err := <-done:
		release()
		t.Fatalf("RemovePID returned while ReservePID held: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	// Start publishes a replacement under the held lock, then releases.
	replacement := PIDFile{
		PID:              444,
		Executable:       "/usr/bin/galley",
		Root:             "/tmp/galley-lock",
		ProcessStartedAt: "start-replacement",
		TokenHash:        "hash-replacement",
	}
	if err := WritePID(path, replacement); err != nil {
		release()
		t.Fatal(err)
	}
	release()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RemovePID did not finish after lifecycle lock release")
	}
	got, err := ReadPIDFile(path)
	if err != nil {
		t.Fatalf("replacement must survive deferred RemovePID: %v", err)
	}
	if got.PID != replacement.PID || got.ProcessStartedAt != replacement.ProcessStartedAt {
		t.Fatalf("got %+v want replacement %+v", got, replacement)
	}
}

// TestRemovePIDHeldRemovesUnderExistingReservation covers the start-path held
// API that must not re-acquire the lifecycle lock.
func TestRemovePIDHeldRemovesUnderExistingReservation(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "galley-daemon.pid")
	meta := PIDFile{PID: 555, Executable: "/usr/bin/galley", Root: "/tmp/r", ProcessStartedAt: "s", TokenHash: "h"}
	if err := WritePID(path, meta); err != nil {
		t.Fatal(err)
	}
	release, err := ReservePID(path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := RemovePIDHeld(path, meta); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("held remove should unlink matching pid file, err=%v", err)
	}
}

// TestShutdownHeartbeatStopBeforeRemovePIDDoesNotRecreateFile proves the
// production cleanup ordering: stop heartbeat (wait for in-flight refresh),
// then RemovePID. A late Heartbeat after stop cannot recreate the PID file.
func TestShutdownHeartbeatStopBeforeRemovePIDDoesNotRecreateFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "galley-daemon.pid")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	meta := NewPIDFile(os.Getpid(), exe, t.TempDir(), []string{exe}).WithToken("hb-shutdown")
	if err := WritePID(path, meta); err != nil {
		t.Fatal(err)
	}
	if err := Heartbeat(path, meta); err != nil {
		t.Fatal(err)
	}

	// Model startPIDHeartbeat: a background refresher that stops on close(done)
	// and only then allows RemovePID (production defer order).
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				_ = Heartbeat(path, meta)
			}
		}
	}()

	// Production shutdown order: stop heartbeat, then remove.
	close(done)
	<-finished
	if err := RemovePID(path, meta); err != nil {
		t.Fatal(err)
	}
	// A post-stop Heartbeat must fail closed and must not recreate the file.
	if err := Heartbeat(path, meta); err == nil {
		t.Fatal("Heartbeat after RemovePID must fail")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("PID file must stay removed after ordered shutdown, err=%v", err)
	}
}

// TestHeartbeatDoesNotRecreateAfterIdentityRemoved covers the interleaving
// where RemovePID wins: Heartbeat's pre-write re-check sees a missing file and
// does not publish a stale record.
func TestHeartbeatDoesNotRecreateAfterIdentityRemoved(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "galley-daemon.pid")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	meta := NewPIDFile(os.Getpid(), exe, t.TempDir(), []string{exe}).WithToken("hb-race")
	if err := WritePID(path, meta); err != nil {
		t.Fatal(err)
	}
	if err := Heartbeat(path, meta); err != nil {
		t.Fatal(err)
	}
	if err := RemovePID(path, meta); err != nil {
		t.Fatal(err)
	}
	if err := Heartbeat(path, meta); !errors.Is(err, os.ErrNotExist) {
		// ReadPIDFile wraps missing files as os.ErrNotExist or path errors.
		if err == nil {
			t.Fatal("expected Heartbeat to fail after RemovePID")
		}
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("Heartbeat must not recreate PID file, stat=%v heartbeat=%v", statErr, err)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("PID file recreated by Heartbeat after RemovePID: %v", err)
	}
}
