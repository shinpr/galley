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

func TestEnvironmentJSONSchemaRequiresConcreteShellForShellPath(t *testing.T) {
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
	allOf := requiredChecks["allOf"].([]any)
	if len(allOf) != 1 {
		t.Fatalf("required_checks allOf got %#v, want one shell_path conditional", allOf)
	}
	rule := allOf[0].(map[string]any)
	thenNode := rule["then"].(map[string]any)
	required := stringSliceFromAny(t, thenNode["required"])
	if !containsString(required, "shell") {
		t.Fatalf("shell_path conditional must require shell, got %#v", required)
	}
	thenProps := thenNode["properties"].(map[string]any)
	shell := thenProps["shell"].(map[string]any)
	got := stringSliceFromAny(t, shell["enum"])
	want := []string{"sh", "bash", "cmd", "powershell", "pwsh"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("shell_path conditional shell enum got %#v, want %#v", got, want)
	}
	shellPath := requiredChecks["properties"].(map[string]any)["shell_path"].(map[string]any)
	if shellPath["pattern"] == "" {
		t.Fatalf("shell_path schema must reject leading/trailing whitespace: %#v", shellPath)
	}
}

// AC4: required_checks.shell_path without an explicit shell kind, or with
// required_checks.shell: "auto", must be rejected with an error that names
// required_checks.shell_path and explains that a concrete shell kind is
// required.
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
