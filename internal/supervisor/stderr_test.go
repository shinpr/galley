package supervisor

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFailedSupervisorsPersistStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake provider; Windows compile covers capture routing")
	}
	for _, provider := range []string{"codex", "claude", "grok"} {
		t.Run(provider, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "provider")
			if err := os.WriteFile(bin, []byte("#!/bin/sh\necho provider-auth-failed >&2\nexit 7\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			_, err := RunAdapterPayload(t.Context(), AdapterOptions{Provider: provider, ArtifactDir: dir, WorkDir: dir, CodexBin: bin, ClaudeBin: bin, GrokBin: bin}, []byte("{}"))
			if err == nil {
				t.Fatal("expected provider failure")
			}
			data, err := os.ReadFile(filepath.Join(dir, provider+"_supervisor_stderr.log"))
			if err != nil || !strings.Contains(string(data), "provider-auth-failed") {
				t.Fatalf("lost stderr: %q %v", data, err)
			}
		})
	}
}
