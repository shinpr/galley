package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSupervisorCLI writes a shell stub that records its argv to capturePath
// and prints a minimal accepted verdict. The stub name (codex/claude) is the
// caller's responsibility so a single helper serves every adapter.
func fakeSupervisorCLI(t *testing.T, name, capturePath string) string {
	t.Helper()
	binDir := t.TempDir()
	path := filepath.Join(binDir, name)
	script := `#!/bin/sh
printf '%s\n' "$*" > ` + capturePath + `
out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-last-message)
      out="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
cat >/dev/null
verdict='{"status":"accepted","summary":"ok","acceptance_gaps":[],"reviewed_files":["README.md"],"acceptance_evidence":[{"ac_id":"AC1","evidence":["checked"]}],"findings":[],"residual_risks":[],"discussion_items":[],"confidence":"medium","next_work_order":""}'
if [ -n "$out" ]; then
  printf '%s\n' "$verdict" > "$out"
  printf '%s\n' '{"event":"done"}'
else
  printf '%s\n' "$verdict"
fi
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// modelPair returns true when args contains the exact `--model <value>` option
// exactly once, so a test can prove a single, well-formed option was emitted
// rather than a duplicate or a malformed flag.
func modelPair(args, value string) bool {
	fields := strings.Fields(args)
	count := 0
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] == "--model" && fields[i+1] == value {
			count++
		}
	}
	return count == 1 && strings.Count(args, "--model") == 1
}

// TestSupervisorAdaptersApplyConfiguredModel covers AC2: Codex, Claude, and GLM
// each forward the configured model exactly once through the native
// `--model <value>` option.
func TestSupervisorAdaptersApplyConfiguredModel(t *testing.T) {
	skipPOSIXFakeSupervisorOnWindows(t)
	const model = "provider-model-x"
	cases := []struct {
		name     string
		provider string
		binName  string
		opts     func(bin string) AdapterOptions
	}{
		{
			name:     "codex",
			provider: "codex",
			binName:  "codex",
			opts: func(bin string) AdapterOptions {
				return AdapterOptions{Provider: "codex", CodexBin: bin, Model: model}
			},
		},
		{
			name:     "claude",
			provider: "claude",
			binName:  "claude",
			opts: func(bin string) AdapterOptions {
				return AdapterOptions{Provider: "claude", ClaudeBin: bin, Model: model}
			},
		},
		{
			name:     "glm",
			provider: "glm",
			binName:  "claude",
			opts: func(bin string) AdapterOptions {
				return AdapterOptions{Provider: "glm", ClaudeBin: bin, GLMAuthToken: "zai-token", Model: model}
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			capturePath := filepath.Join(t.TempDir(), "args")
			bin := fakeSupervisorCLI(t, tc.binName, capturePath)
			opts := tc.opts(bin)
			opts.WorkDir = t.TempDir()
			opts.ArtifactDir = t.TempDir()
			if _, err := RunAdapterPayload(context.Background(), opts, []byte(`{"evidence":{"task":{"id":"task"},"diff":""}}`)); err != nil {
				t.Fatal(err)
			}
			args, err := os.ReadFile(capturePath)
			if err != nil {
				t.Fatal(err)
			}
			if !modelPair(string(args), model) {
				t.Fatalf("%s args must include exactly one `--model %s`:\n%s", tc.name, model, args)
			}
		})
	}
}

// TestSupervisorAdaptersOmitModelWhenUnset covers AC3: an omitted override
// leaves the argv free of any `--model` option for all three adapters, so the
// supervisor CLI keeps its own default model.
func TestSupervisorAdaptersOmitModelWhenUnset(t *testing.T) {
	skipPOSIXFakeSupervisorOnWindows(t)
	cases := []struct {
		name    string
		binName string
		opts    func(bin string) AdapterOptions
	}{
		{
			name:    "codex",
			binName: "codex",
			opts:    func(bin string) AdapterOptions { return AdapterOptions{Provider: "codex", CodexBin: bin} },
		},
		{
			name:    "claude",
			binName: "claude",
			opts:    func(bin string) AdapterOptions { return AdapterOptions{Provider: "claude", ClaudeBin: bin} },
		},
		{
			name:    "glm",
			binName: "claude",
			opts: func(bin string) AdapterOptions {
				return AdapterOptions{Provider: "glm", ClaudeBin: bin, GLMAuthToken: "zai-token"}
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			capturePath := filepath.Join(t.TempDir(), "args")
			bin := fakeSupervisorCLI(t, tc.binName, capturePath)
			opts := tc.opts(bin)
			opts.WorkDir = t.TempDir()
			opts.ArtifactDir = t.TempDir()
			if _, err := RunAdapterPayload(context.Background(), opts, []byte(`{"evidence":{"task":{"id":"task"},"diff":""}}`)); err != nil {
				t.Fatal(err)
			}
			args, err := os.ReadFile(capturePath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(args), "--model") {
				t.Fatalf("%s args must omit `--model` when no model is configured:\n%s", tc.name, args)
			}
		})
	}
}
