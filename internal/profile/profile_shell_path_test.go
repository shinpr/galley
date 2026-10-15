package profile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An unrecognized shell_path basename is valid when an explicit shell kind
// supplies the invocation style.
func TestValidateEnvironmentAcceptsUnrecognizedShellPathWithExplicitShellKind(t *testing.T) {
	const shellPath = "/opt/galley/custom-bash"
	for _, shell := range []string{"sh", "bash", "cmd", "powershell", "pwsh"} {
		shell := shell
		t.Run(shell, func(t *testing.T) {
			env := validEnvironmentForTest()
			env.RequiredChecks = RequiredCheckEnvironment{Shell: shell, ShellPath: shellPath}
			result := ValidateEnvironment(env)
			if !result.Valid() {
				t.Fatalf("errors got %#v", result.Errors)
			}
		})
	}
}

func TestLoadEnvironmentDecodesRequiredCheckShellAndShellPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "environment.yaml")
	body := `id: "local"
cwd: "/tmp/repo"
commands:
  test: "go test ./..."
required_checks:
  shell: "bash"
  shell_path: "/opt/galley/custom-bash"
constraints:
  network: "approval_required"
  secrets_policy: "never_read_env_files"
  destructive_commands: "deny"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := LoadEnvironment(path)
	if err != nil {
		t.Fatal(err)
	}
	if env.RequiredChecks.Shell != "bash" || env.RequiredChecks.ShellPath != "/opt/galley/custom-bash" {
		t.Fatalf("YAML decode lost required_checks: %#v", env.RequiredChecks)
	}
}

func TestValidateEnvironmentRejectsRequiredCheckShellPathWithOuterWhitespace(t *testing.T) {
	cases := []string{
		" /opt/galley/bash",
		"/opt/galley/bash ",
		" \t",
	}
	for _, shellPath := range cases {
		shellPath := shellPath
		t.Run(fmt.Sprintf("%q", shellPath), func(t *testing.T) {
			env := validEnvironmentForTest()
			env.RequiredChecks = RequiredCheckEnvironment{Shell: "bash", ShellPath: shellPath}
			result := ValidateEnvironment(env)
			if result.Valid() {
				t.Fatal("expected invalid required_checks.shell_path with outer whitespace")
			}
			if !strings.Contains(strings.Join(result.Errors, "\n"), "required_checks.shell_path") {
				t.Fatalf("error must name required_checks.shell_path, got %#v", result.Errors)
			}
		})
	}
}

// AC5 (schema): The environment schema describes `shell_path` as an
// executable override that may stand alone for recognized executable names
// and takes precedence over `shell` when both are present. Structural
// enforcement of "unrecognized shell_path requires fallback shell" lives in
// Go-side ValidateEnvironment because JSON Schema regex (ECMA-262) cannot
// express the recognized-basename check cleanly across separators and the
// optional .exe suffix. The schema therefore must not declare an
// if/then/required relationship between shell_path and shell.
func TestEnvironmentJSONSchemaAllowsShellPathStandaloneStructurally(t *testing.T) {
	data, err := EnvironmentJSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	props := schema["properties"].(map[string]any)
	requiredChecks := props["required_checks"].(map[string]any)
	if _, ok := requiredChecks["allOf"]; ok {
		t.Fatalf("required_checks must not declare an if/then required:shell rule; shell_path may stand alone for recognized basenames: %#v", requiredChecks["allOf"])
	}
	rcProps := requiredChecks["properties"].(map[string]any)
	shell := rcProps["shell"].(map[string]any)
	shellEnum := stringSliceFromAny(t, shell["enum"])
	wantShell := []string{"auto", "sh", "bash", "cmd", "powershell", "pwsh"}
	if strings.Join(shellEnum, ",") != strings.Join(wantShell, ",") {
		t.Fatalf("shell enum got %#v, want %#v", shellEnum, wantShell)
	}
	shellPath := rcProps["shell_path"].(map[string]any)
	if shellPath["pattern"] == "" {
		t.Fatalf("shell_path schema must reject leading/trailing whitespace: %#v", shellPath)
	}
}

// A recognized shell_path basename stands alone and overrides any configured
// shell kind; basename recognition itself is pinned by TestInferRequiredCheckShellKind.
func TestValidateEnvironmentAcceptsRecognizedShellPathWithoutExplicitShellKind(t *testing.T) {
	cases := []struct {
		name      string
		shell     string
		shellPath string
	}{
		{name: "Standalone", shell: "", shellPath: "/opt/galley/bash"},
		{name: "OverridesDisagreeingShell", shell: "cmd", shellPath: `C:\tools\Git\bin\bash.exe`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := validEnvironmentForTest()
			env.RequiredChecks = RequiredCheckEnvironment{Shell: tc.shell, ShellPath: tc.shellPath}
			result := ValidateEnvironment(env)
			if !result.Valid() {
				t.Fatalf("recognized shell_path must validate, errors got %#v", result.Errors)
			}
		})
	}
}

// AC3 (profile-side): required_checks.shell_path with an unrecognized
// executable basename and no explicit non-auto required_checks.shell must
// be rejected with an error that names required_checks.shell_path and
// explains that an explicit shell kind is required as fallback metadata.
func TestValidateEnvironmentRejectsRequiredCheckShellPathWithoutExplicitShellKind(t *testing.T) {
	cases := []struct {
		name  string
		shell string
	}{
		{"empty_shell", ""},
		{"auto_shell", "auto"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := validEnvironmentForTest()
			env.RequiredChecks = RequiredCheckEnvironment{
				Shell:     tc.shell,
				ShellPath: "/opt/galley/custom-bash",
			}
			result := ValidateEnvironment(env)
			if result.Valid() {
				t.Fatal("expected invalid required_checks.shell_path without explicit shell kind")
			}
			joined := strings.Join(result.Errors, "\n")
			if !strings.Contains(joined, "required_checks.shell_path") {
				t.Fatalf("error must name required_checks.shell_path, got %q", joined)
			}
			if !strings.Contains(joined, "shell") || !strings.Contains(joined, "explicit") {
				t.Fatalf("error must communicate that an explicit shell kind is required, got %q", joined)
			}
		})
	}
}

// AC1+AC3 helper coverage: InferRequiredCheckShellKind decides whether a
// shell_path may stand alone. Pin the recognized basenames here so future
// changes to validation/resolver code share the same recognition table.
func TestInferRequiredCheckShellKind(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{in: "/bin/bash", want: "bash"},
		{in: "/usr/local/bin/bash", want: "bash"},
		{in: "/bin/sh", want: "sh"},
		{in: `C:\Windows\System32\cmd.exe`, want: "cmd"},
		{in: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, want: "powershell"},
		{in: `C:\Program Files\PowerShell\7\pwsh.exe`, want: "pwsh"},
		{in: "C:/Program Files/Git/bin/bash.exe", want: "bash"},
		{in: "BASH", want: "bash"},
		{in: "Pwsh.EXE", want: "pwsh"},
		{in: "/opt/galley/custom-bash", want: ""},
		{in: "/opt/galley/zsh", want: ""},
		{in: "C:\\tools\\fish.exe", want: ""},
		{in: "", want: ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.in, func(t *testing.T) {
			if got := InferRequiredCheckShellKind(tc.in); got != tc.want {
				t.Fatalf("InferRequiredCheckShellKind(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func stringSliceFromAny(t *testing.T, raw any) []string {
	t.Helper()
	values, ok := raw.([]any)
	if !ok {
		t.Fatalf("got %#v, want []any", raw)
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		s, ok := value.(string)
		if !ok {
			t.Fatalf("got %#v, want string", value)
		}
		out = append(out, s)
	}
	return out
}
