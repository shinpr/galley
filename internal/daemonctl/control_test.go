package daemonctl

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePathsDefaultsUnderRoot(t *testing.T) {
	t.Parallel()
	paths := ResolvePaths("/tmp/galley", "", "")
	if paths.PIDFile != "/tmp/galley/galleyd.pid" {
		t.Fatalf("pid file got %q", paths.PIDFile)
	}
	if paths.LogFile != "/tmp/galley/galleyd.log" {
		t.Fatalf("log file got %q", paths.LogFile)
	}
}

func TestReservePIDIsExclusive(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "galleyd.pid")
	release, err := ReservePID(path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := ReservePID(path); err == nil {
		t.Fatal("expected lock error")
	}
}

func TestWriteReadPIDFileJSON(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "galleyd.pid")
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
	if read.PID != os.Getpid() || read.Executable != meta.Executable || read.Root != meta.Root || read.Legacy {
		t.Fatalf("metadata got %#v, want %#v", read, meta)
	}
}

func TestInspectVerifiesCurrentProcess(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "galleyd.pid")
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
	path := filepath.Join(t.TempDir(), "galleyd.pid")
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
	path := filepath.Join(t.TempDir(), "galleyd.pid")
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

func TestReadPIDFileSupportsLegacyBarePID(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "galleyd.pid")
	if err := os.WriteFile(path, []byte("123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	meta, err := ReadPIDFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.PID != 123 || !meta.Legacy {
		t.Fatalf("legacy metadata got %#v", meta)
	}
}

func TestRemovePIDOnlyRemovesMatchingPID(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "galleyd.pid")
	if err := WritePID(path, PIDFile{PID: 123}); err != nil {
		t.Fatal(err)
	}
	if err := RemovePID(path, 456); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("pid file should remain: %v", err)
	}
	if err := RemovePID(path, 123); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pid file should be removed, err=%v", err)
	}
}
