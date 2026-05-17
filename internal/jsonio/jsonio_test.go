package jsonio

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteCreatesPrivateDirectoryAndJSONFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "value.json")
	if err := Write(path, map[string]string{"status": "ok"}); err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if got, want := dirInfo.Mode().Perm(), os.FileMode(0o700); got != want {
			t.Fatalf("dir mode got %o, want %o", got, want)
		}
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if got, want := fileInfo.Mode().Perm(), os.FileMode(0o600); got != want {
			t.Fatalf("file mode got %o, want %o", got, want)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if got, want := decoded["status"], "ok"; got != want {
		t.Fatalf("decoded status got %q, want %q", got, want)
	}
}
