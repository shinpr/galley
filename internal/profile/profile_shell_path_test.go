package profile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// AC3 (profile-side): Setting required_checks.shell_path with an explicit
// non-auto required_checks.shell must round-trip through the Environment
// struct and pass ValidateEnvironment.
func TestValidateEnvironmentAcceptsRequiredCheckShellPathWithExplicitShellKind(t *testing.T) {
	kinds := []struct {
		shell string
		path  string
	}{
		{"sh", "/opt/galley/sh"},
		{"bash", "/opt/galley/bash"},
		{"cmd", `C:\custom\cmd.exe`},
		{"powershell", `C:\custom\powershell.exe`},
		{"pwsh", `C:\custom\pwsh.exe`},
	}
	for _, tc := range kinds {
		tc := tc
		t.Run("struct/"+tc.shell, func(t *testing.T) {
			env := validEnvironmentForTest()
			env.RequiredChecks = RequiredCheckEnvironment{Shell: tc.shell, ShellPath: tc.path}
			result := ValidateEnvironment(env)
			if !result.Valid() {
				t.Fatalf("errors got %#v", result.Errors)
			}
		})
		t.Run("yaml/"+tc.shell, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "environment.yaml")
			body := fmt.Sprintf(`id: "local"
cwd: "/tmp/repo"
commands:
  test: "go test ./..."
required_checks:
  shell: %q
  shell_path: %q
constraints:
  network: "approval_required"
  secrets_policy: "never_read_env_files"
  destructive_commands: "deny"
`, tc.shell, tc.path)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			env, err := LoadEnvironment(path)
			if err != nil {
				t.Fatal(err)
			}
			if env.RequiredChecks.ShellPath != tc.path {
				t.Fatalf("YAML decode lost shell_path: got %q want %q", env.RequiredChecks.ShellPath, tc.path)
			}
			if env.RequiredChecks.Shell != tc.shell {
				t.Fatalf("YAML decode lost shell: got %q want %q", env.RequiredChecks.Shell, tc.shell)
			}
			result := ValidateEnvironment(env)
			if !result.Valid() {
				t.Fatalf("errors got %#v", result.Errors)
			}
		})
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

// AC1 (profile-side): When required_checks.shell_path uses a recognized
// shell executable basename, the profile is valid even without
// required_checks.shell. Galley will infer the invocation style from the
// executable name. Both forward-slash Unix paths and backslash Windows
// paths are accepted, plus the optional .exe suffix.
func TestValidateEnvironmentAcceptsRecognizedShellPathWithoutExplicitShellKind(t *testing.T) {
	cases := []struct {
		name      string
		shell     string
		shellPath string
	}{
		{name: "UnixBashStandalone", shell: "", shellPath: "/opt/galley/bash"},
		{name: "UnixShStandalone", shell: "", shellPath: "/opt/galley/sh"},
		{name: "WindowsBashStandalone", shell: "", shellPath: `C:\Program Files\Git\bin\bash.exe`},
		{name: "WindowsCmdStandalone", shell: "", shellPath: `C:\Windows\System32\cmd.exe`},
		{name: "WindowsPowerShellStandalone", shell: "", shellPath: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`},
		{name: "WindowsPwshStandalone", shell: "", shellPath: `C:\Program Files\PowerShell\7\pwsh.exe`},
		{name: "AutoShellRecognizedBasename", shell: "auto", shellPath: "/opt/galley/bash"},
		{name: "MixedSeparatorBashExe", shell: "", shellPath: "C:/tools/Git/bin/bash.exe"},
		{name: "UppercaseExtension", shell: "", shellPath: `C:\tools\pwsh.EXE`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := validEnvironmentForTest()
			env.RequiredChecks = RequiredCheckEnvironment{Shell: tc.shell, ShellPath: tc.shellPath}
			result := ValidateEnvironment(env)
			if !result.Valid() {
				t.Fatalf("recognized shell_path must validate without explicit shell, errors got %#v", result.Errors)
			}
		})
	}
}

// AC5 (profile-side): When required_checks.shell_path uses a recognized
// shell executable basename, the configured required_checks.shell is allowed
// to disagree without rejection because shell_path is the more specific
// selection and shell is only fallback kind metadata. Resolver behavior
// validates that the inferred kind wins; profile validation only rejects
// when no usable kind can be determined.
func TestValidateEnvironmentAcceptsShellPathInferredKindOverridingConfiguredShell(t *testing.T) {
	env := validEnvironmentForTest()
	env.RequiredChecks = RequiredCheckEnvironment{Shell: "cmd", ShellPath: `C:\tools\Git\bin\bash.exe`}
	result := ValidateEnvironment(env)
	if !result.Valid() {
		t.Fatalf("recognized shell_path with disagreeing configured shell must validate, errors got %#v", result.Errors)
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
