package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureArgvSupervisor writes a fake CLI that records "$@" (one argument per
// line) to argsPath and emits a minimal accepted verdict on stdout, so tests
// can assert the exact --model argv pairs a supervisor adapter builds.
func captureArgvSupervisor(t *testing.T, bin, argsPath string) {
	t.Helper()
	// Codex writes its verdict to the --output-last-message file; Claude/GLM
	// emit it on stdout. Resolve OUT for codex from argv and fall back to
	// /dev/stdout for the claude transport so a single fake serves all three.
	script := `#!/bin/sh
OUT=/dev/stdout
prev=""
for a in "$@"; do
  if [ "$prev" = "--output-last-message" ]; then OUT="$a"; fi
  prev="$a"
done
: > ` + argsPath + `
for a in "$@"; do
  printf '%s\n' "$a" >> ` + argsPath + `
done
cat >/dev/null
printf '%s\n' '{"status":"accepted","summary":"ok","acceptance_gaps":[],"reviewed_files":["README.md"],"acceptance_evidence":[{"ac_id":"AC1","evidence":["checked"]}],"findings":[],"residual_risks":[],"discussion_items":[],"confidence":"medium","next_work_order":""}' > "$OUT"
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func readArgvLines(t *testing.T, argsPath string) []string {
	t.Helper()
	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// countModelPairs returns how many times a `--model <value>` pair carrying the
// exact wanted value appears in argv, and how many bare `--model` flags exist.
func countModelPairs(argv []string, want string) (matching int, totalFlags int) {
	for i, a := range argv {
		if a != "--model" {
			continue
		}
		totalFlags++
		if i+1 < len(argv) && argv[i+1] == want {
			matching++
		}
	}
	return matching, totalFlags
}

// AC2/AC3: the pure argv builders forward a configured model as exactly one
// native --model pair and drop the flag when unset. Testing them directly runs
// on every platform including Windows, unlike the exec-based cases below.
func TestSupervisorArgvBuildersModelFlag(t *testing.T) {
	t.Parallel()
	const model = "provider-model-x"
	cases := []struct {
		name  string
		model string
		want  int
	}{
		{name: "configured", model: model, want: 1},
		{name: "omitted", model: "", want: 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			codex := codexSupervisorArgv(
				AdapterOptions{Provider: "codex", Model: tc.model, CodexBin: "codex", WorkDir: "/work"},
				"/tmp/schema.json", "/tmp/out.json",
			)
			if codex[len(codex)-1] != "-" {
				t.Fatalf("codex argv must end with stdin marker, got %v", codex)
			}
			assertModelFlag(t, "codex", codex, model, tc.want)
			// Both OS branches are checked so the Windows command path is covered
			// without executing a Windows binary.
			for _, goos := range []string{"linux", "windows"} {
				claude := claudeSupervisorArgv(
					AdapterOptions{Provider: "claude", Model: tc.model, ClaudeBin: "claude"},
					goos, "/tmp/guard", "/tmp/debug.log", "/tmp/system-prompt.md", "{}",
				)
				assertModelFlag(t, "claude/"+goos, claude, model, tc.want)
			}
		})
	}
}

func assertModelFlag(t *testing.T, label string, argv []string, model string, want int) {
	t.Helper()
	matching, totalFlags := countModelPairs(argv, model)
	if totalFlags != want {
		t.Fatalf("%s: want %d --model flag(s), got %d in argv %v", label, want, totalFlags, argv)
	}
	if matching != want {
		t.Fatalf("%s: want %d --model %q pair(s), got %d in argv %v", label, want, model, matching, argv)
	}
}

// AC2: Codex, Claude, and GLM each include the exact configured model once via
// the native --model option.
func TestSupervisorAdaptersEmitConfiguredModelOnce(t *testing.T) {
	skipPOSIXFakeSupervisorOnWindows(t)
	const model = "provider-model-x"
	cases := []struct {
		name  string
		build func(bin, argsPath string) AdapterOptions
	}{
		{
			name: "codex",
			build: func(bin, argsPath string) AdapterOptions {
				return AdapterOptions{Provider: "codex", Model: model, WorkDir: t.TempDir(), ArtifactDir: t.TempDir(), CodexBin: bin}
			},
		},
		{
			name: "claude",
			build: func(bin, argsPath string) AdapterOptions {
				return AdapterOptions{Provider: "claude", Model: model, WorkDir: t.TempDir(), ArtifactDir: t.TempDir(), ClaudeBin: bin}
			},
		},
		{
			name: "glm",
			build: func(bin, argsPath string) AdapterOptions {
				return AdapterOptions{Provider: "glm", Model: model, WorkDir: t.TempDir(), ArtifactDir: t.TempDir(), ClaudeBin: bin, GLMAuthToken: "zai-token"}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), tc.name)
			argsPath := filepath.Join(t.TempDir(), "args")
			captureArgvSupervisor(t, bin, argsPath)
			if _, err := RunAdapterPayload(context.Background(), tc.build(bin, argsPath), []byte(`{"evidence":{"task":{"id":"task"},"diff":""}}`)); err != nil {
				t.Fatal(err)
			}
			argv := readArgvLines(t, argsPath)
			matching, totalFlags := countModelPairs(argv, model)
			if matching != 1 {
				t.Fatalf("%s: want exactly one --model %q pair, got %d in argv %v", tc.name, model, matching, argv)
			}
			if totalFlags != 1 {
				t.Fatalf("%s: want exactly one --model flag, got %d (malformed/duplicate) in argv %v", tc.name, totalFlags, argv)
			}
		})
	}
}

// AC3: an omitted (empty) model produces no --model option for any adapter,
// preserving CLI-default behavior.
func TestSupervisorAdaptersOmitModelWhenUnset(t *testing.T) {
	skipPOSIXFakeSupervisorOnWindows(t)
	cases := []struct {
		name  string
		build func(bin string) AdapterOptions
	}{
		{
			name: "codex",
			build: func(bin string) AdapterOptions {
				return AdapterOptions{Provider: "codex", WorkDir: t.TempDir(), ArtifactDir: t.TempDir(), CodexBin: bin}
			},
		},
		{
			name: "claude",
			build: func(bin string) AdapterOptions {
				return AdapterOptions{Provider: "claude", WorkDir: t.TempDir(), ArtifactDir: t.TempDir(), ClaudeBin: bin}
			},
		},
		{
			name: "glm",
			build: func(bin string) AdapterOptions {
				return AdapterOptions{Provider: "glm", WorkDir: t.TempDir(), ArtifactDir: t.TempDir(), ClaudeBin: bin, GLMAuthToken: "zai-token"}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), tc.name)
			argsPath := filepath.Join(t.TempDir(), "args")
			captureArgvSupervisor(t, bin, argsPath)
			if _, err := RunAdapterPayload(context.Background(), tc.build(bin), []byte(`{"evidence":{"task":{"id":"task"},"diff":""}}`)); err != nil {
				t.Fatal(err)
			}
			for _, a := range readArgvLines(t, argsPath) {
				if a == "--model" {
					t.Fatalf("%s: argv must omit --model when model is unset, got %v", tc.name, readArgvLines(t, argsPath))
				}
			}
		})
	}
}
