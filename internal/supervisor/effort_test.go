package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeVerdictBin writes its argv to capturePath and emits a minimal accepted
// verdict so RunAdapterPayload succeeds. For codex the verdict is written to the
// --output-last-message path; for claude it is printed to stdout.
func fakeVerdictBin(t *testing.T, capturePath string, codex bool) string {
	t.Helper()
	dir := t.TempDir()
	name := "claude"
	if codex {
		name = "codex"
	}
	path := filepath.Join(dir, name)
	verdict := `{"status":"accepted","summary":"ok","acceptance_gaps":[],"reviewed_files":["README.md"],"acceptance_evidence":[{"ac_id":"AC1","evidence":["checked"]}],"findings":[],"residual_risks":[],"discussion_items":[],"confidence":"medium","next_work_order":""}`
	var body string
	if codex {
		body = `#!/bin/sh
printf '%s\n' "$*" > ` + capturePath + `
out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-last-message) out="$2"; shift 2 ;;
    *) shift ;;
  esac
done
cat >/dev/null
printf '%s\n' '` + verdict + `' > "$out"
printf '%s\n' '{"event":"done"}'
`
	} else {
		body = `#!/bin/sh
printf '%s\n' "$*" > ` + capturePath + `
cat >/dev/null
printf '%s\n' '` + verdict + `'
`
	}
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRunAdapterPayloadPassesSupervisorEffort proves AC2: a configured effort
// reaches the effective supervisor through its supported invocation surface —
// --effort for claude/glm and the model_reasoning_effort config override for
// codex.
func TestRunAdapterPayloadPassesSupervisorEffort(t *testing.T) {
	skipPOSIXFakeSupervisorOnWindows(t)
	cases := []struct {
		provider string
		codex    bool
		want     string
	}{
		{provider: "claude", want: "--effort high"},
		{provider: "glm", want: "--effort high"},
		{provider: "codex", codex: true, want: `model_reasoning_effort="high"`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.provider, func(t *testing.T) {
			capturePath := filepath.Join(t.TempDir(), "args")
			bin := fakeVerdictBin(t, capturePath, tc.codex)
			opts := AdapterOptions{
				Provider:    tc.provider,
				Effort:      "high",
				WorkDir:     t.TempDir(),
				ArtifactDir: t.TempDir(),
			}
			if tc.codex {
				opts.CodexBin = bin
			} else {
				opts.ClaudeBin = bin
				opts.GLMAuthToken = "zai-token"
			}
			if _, err := RunAdapterPayload(context.Background(), opts, []byte(`{"evidence":{"task":{"id":"task"},"diff":""}}`)); err != nil {
				t.Fatal(err)
			}
			args, err := os.ReadFile(capturePath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(args), tc.want) {
				t.Fatalf("%s args missing %q:\n%s", tc.provider, tc.want, args)
			}
		})
	}
}

// TestPreflightEffortRejectsUnsupportedCombinations proves AC2's negative path:
// an unsupported provider/effort combination fails with an error that names the
// field, provider, and accepted values, while an empty effort and every
// provider-valid value pass.
func TestPreflightEffortRejectsUnsupportedCombinations(t *testing.T) {
	t.Parallel()
	if err := PreflightEffort("claude", ""); err != nil {
		t.Fatalf("empty effort must preserve the CLI default, got %v", err)
	}
	for _, ok := range []struct{ provider, effort string }{
		{"claude", "max"}, {"glm", "xhigh"}, {"codex", "minimal"}, {"codex", "max"},
	} {
		if err := PreflightEffort(ok.provider, ok.effort); err != nil {
			t.Fatalf("PreflightEffort(%q,%q) = %v; want nil", ok.provider, ok.effort, err)
		}
	}
	for _, bad := range []struct {
		provider, effort string
		wantSubstrings   []string
	}{
		{"claude", "minimal", []string{"supervisor.effort", "claude", "low, medium, high, xhigh, max"}},
		{"glm", "turbo", []string{"supervisor.effort", "glm", "low, medium, high, xhigh, max"}},
		{"codex", "turbo", []string{"supervisor.effort", "codex", "minimal, low, medium, high, xhigh, max"}},
	} {
		err := PreflightEffort(bad.provider, bad.effort)
		if err == nil {
			t.Fatalf("PreflightEffort(%q,%q) must fail", bad.provider, bad.effort)
		}
		for _, want := range bad.wantSubstrings {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q missing %q", err, want)
			}
		}
	}
}

// TestRunAdapterPayloadRejectsInvalidEffortBeforeSubprocess proves the invalid
// combination fails before any supervisor binary runs: the fake binary would
// write to capturePath if invoked, and it must not.
func TestRunAdapterPayloadRejectsInvalidEffortBeforeSubprocess(t *testing.T) {
	skipPOSIXFakeSupervisorOnWindows(t)
	capturePath := filepath.Join(t.TempDir(), "args")
	bin := fakeVerdictBin(t, capturePath, false)
	_, err := RunAdapterPayload(context.Background(), AdapterOptions{
		Provider:    "claude",
		Effort:      "minimal", // claude does not accept minimal
		WorkDir:     t.TempDir(),
		ArtifactDir: t.TempDir(),
		ClaudeBin:   bin,
	}, []byte(`{"evidence":{"task":{"id":"task"},"diff":""}}`))
	if err == nil {
		t.Fatal("expected preflight rejection for claude+minimal")
	}
	if !strings.Contains(err.Error(), "supervisor.effort") {
		t.Fatalf("error must name supervisor.effort, got %v", err)
	}
	if _, statErr := os.Stat(capturePath); statErr == nil {
		t.Fatal("supervisor subprocess ran despite invalid effort")
	}
}
