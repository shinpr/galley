package fileutil

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestFileLockHelper(t *testing.T) {
	path := os.Getenv("GALLEY_TEST_LOCK_PATH")
	if path == "" {
		return
	}
	unlock, err := TryLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	fmt.Println("locked")
	_, _ = os.Stdin.Read(make([]byte, 1))
}

func TestFileLockReleasesAfterProcessDeath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "id.lock")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(t.Context(), executable, "-test.run=^TestFileLockHelper$")
	child.Env = append(os.Environ(), "GALLEY_TEST_LOCK_PATH="+path)
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	reader := bufio.NewScanner(stdout)
	if !reader.Scan() || reader.Text() != "locked" {
		t.Fatal("child did not acquire lock")
	}
	if unlock, err := TryLock(path); err == nil {
		unlock()
		t.Fatal("concurrent lock succeeded")
	}
	other, err := TryLock(path + "-other")
	if err != nil {
		t.Fatal(err)
	}
	other()
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	unlock, err := TryLock(path)
	if err != nil {
		t.Fatalf("dead registration still blocks queue: %v", err)
	}
	unlock()
}
