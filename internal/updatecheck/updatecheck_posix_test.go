//go:build !windows

package updatecheck

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestFileIsTerminalRejectsRedirectedDescriptors(t *testing.T) {
	t.Parallel()
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer func() { _ = devNull.Close() }()
	regular, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatalf("create regular file: %v", err)
	}
	defer func() { _ = regular.Close() }()
	pipeR, pipeW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	defer func() { _ = pipeR.Close() }()
	defer func() { _ = pipeW.Close() }()

	for name, f := range map[string]*os.File{
		"null device":  devNull,
		"regular file": regular,
		"pipe reader":  pipeR,
		"pipe writer":  pipeW,
	} {
		if fileIsTerminal(f) {
			t.Fatalf("%s reported as a terminal", name)
		}
	}

	// When the test process has a controlling terminal, it must stay eligible.
	if tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
		defer func() { _ = tty.Close() }()
		if !fileIsTerminal(tty) {
			t.Fatal("controlling terminal reported as non-TTY")
		}
	}
}

func TestRedirectedStderrMakesNoRequestAndNoState(t *testing.T) {
	t.Parallel()
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer func() { _ = devNull.Close() }()

	root := filepath.Join(t.TempDir(), "root")
	transport := &fakeTransport{handler: func(_ *http.Request) (*http.Response, error) {
		return releaseResponse(t, "v0.13.0"), nil
	}}
	opts := baseOptions(root, transport)
	opts.IsTTY = func() bool { return fileIsTerminal(devNull) }

	Run(t.Context(), opts)

	if transport.calls != 0 {
		t.Fatalf("redirected stderr made %d requests", transport.calls)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("redirected stderr created update state, err=%v", err)
	}
}

func TestWriteStateCreatesOwnerOnlyRoot(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "galley-root")
	transport := &fakeTransport{handler: func(_ *http.Request) (*http.Response, error) {
		return releaseResponse(t, "v0.13.0"), nil
	}}
	opts := baseOptions(root, transport)

	Run(t.Context(), opts)

	if transport.calls != 1 {
		t.Fatalf("first start made %d requests, want 1", transport.calls)
	}
	if got := readAttemptTime(t, root); !got.Equal(testNow) {
		t.Fatalf("attempt time got %v, want %v", got, testNow)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("stat new root: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("new root permissions %04o, want 0700", perm)
	}
}
