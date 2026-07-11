package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// verdictJSON is a minimal accepted verdict the fake supervisor CLIs emit so
// RunAdapterPayload returns without error and the test can assert only on argv.
const verdictJSON = `{"status":"accepted","summary":"ok","acceptance_gaps":[],"reviewed_files":["README.md"],"acceptance_evidence":[{"ac_id":"AC1","evidence":["checked"]}],"findings":[],"residual_risks":[],"discussion_items":[],"confidence":"medium","next_work_order":""}`

// writeFakeCodex writes a codex stub that records argv and emits the verdict to
// the --output-last-message path so runCodexAdapter reads a valid verdict.
func writeFakeCodex(t *testing.T, capturePath string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "codex")
	script := `#!/bin/sh
printf '%s\n' "$*" > ` + capturePath + `
out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-last-message) out="$2"; shift 2 ;;
    *) shift ;;
  esac
done
cat >/dev/null
printf '%s\n' '` + verdictJSON + `' > "$out"
printf '%s\n' '{"event":"done"}'
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin
}

// writeFakeClaude writes a claude stub that records argv and emits the verdict
// on stdout so both the claude and glm adapters can read a valid verdict.
func writeFakeClaude(t *testing.T, capturePath string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
printf '%s\n' "$*" > ` + capturePath + `
cat >/dev/null
printf '%s\n' '` + verdictJSON + `'
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin
}

// AC2: each adapter includes the exact configured model exactly once through
// the native --model option.
func TestSupervisorAdaptersApplyConfiguredModelOnce(t *testing.T) {
	skipPOSIXFakeSupervisorOnWindows(t)
	const model = "pinned-model-42"
	cases := []struct {
		name  string
		build func(t *testing.T, capture string) AdapterOptions
	}{
		{
			name: "codex",
			build: func(t *testing.T, capture string) AdapterOptions {
				return AdapterOptions{Provider: "codex", WorkDir: t.TempDir(), ArtifactDir: t.TempDir(), CodexBin: writeFakeCodex(t, capture), Model: model}
			},
		},
		{
			name: "claude",
			build: func(t *testing.T, capture string) AdapterOptions {
				return AdapterOptions{Provider: "claude", WorkDir: t.TempDir(), ArtifactDir: t.TempDir(), ClaudeBin: writeFakeClaude(t, capture), Model: model}
			},
		},
		{
			name: "glm",
			build: func(t *testing.T, capture string) AdapterOptions {
				return AdapterOptions{Provider: "glm", WorkDir: t.TempDir(), ArtifactDir: t.TempDir(), ClaudeBin: writeFakeClaude(t, capture), GLMAuthToken: "zai-token", Model: model}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := filepath.Join(t.TempDir(), "args")
			opts := tc.build(t, capture)
			if _, err := RunAdapterPayload(context.Background(), opts, []byte(`{"evidence":{"task":{"id":"task"},"diff":""}}`)); err != nil {
				t.Fatal(err)
			}
			args := readArgs(t, capture)
			if !strings.Contains(args, "--model "+model) {
				t.Fatalf("%s argv missing exact configured model:\n%s", tc.name, args)
			}
			if got := strings.Count(args, "--model"); got != 1 {
				t.Fatalf("%s argv has %d --model options, want exactly 1:\n%s", tc.name, got, args)
			}
		})
	}
}

// AC3: an omitted (or whitespace-only) model omits the --model option so each
// adapter preserves the supervisor CLI default.
func TestSupervisorAdaptersOmitModelWhenUnset(t *testing.T) {
	skipPOSIXFakeSupervisorOnWindows(t)
	cases := []struct {
		name  string
		model string
		build func(t *testing.T, capture, model string) AdapterOptions
	}{
		{
			name:  "codex-empty",
			model: "",
			build: func(t *testing.T, capture, model string) AdapterOptions {
				return AdapterOptions{Provider: "codex", WorkDir: t.TempDir(), ArtifactDir: t.TempDir(), CodexBin: writeFakeCodex(t, capture), Model: model}
			},
		},
		{
			name:  "claude-empty",
			model: "",
			build: func(t *testing.T, capture, model string) AdapterOptions {
				return AdapterOptions{Provider: "claude", WorkDir: t.TempDir(), ArtifactDir: t.TempDir(), ClaudeBin: writeFakeClaude(t, capture), Model: model}
			},
		},
		{
			name:  "glm-empty",
			model: "",
			build: func(t *testing.T, capture, model string) AdapterOptions {
				return AdapterOptions{Provider: "glm", WorkDir: t.TempDir(), ArtifactDir: t.TempDir(), ClaudeBin: writeFakeClaude(t, capture), GLMAuthToken: "zai-token", Model: model}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := filepath.Join(t.TempDir(), "args")
			opts := tc.build(t, capture, tc.model)
			if _, err := RunAdapterPayload(context.Background(), opts, []byte(`{"evidence":{"task":{"id":"task"},"diff":""}}`)); err != nil {
				t.Fatal(err)
			}
			args := readArgs(t, capture)
			if strings.Contains(args, "--model") {
				t.Fatalf("%s argv must omit --model when model is unset:\n%s", tc.name, args)
			}
		})
	}
}

func readArgs(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
