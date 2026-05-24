package profile

import (
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
