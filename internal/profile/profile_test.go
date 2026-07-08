package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/daemonconfig"
)

func TestLoadAndValidateQualityExample(t *testing.T) {
	path, err := filepath.Abs("../../examples/quality-default.yaml")
	if err != nil {
		t.Fatal(err)
	}
	quality, err := LoadQuality(path)
	if err != nil {
		t.Fatal(err)
	}
	result := ValidateQuality(quality)
	if !result.Valid() {
		t.Fatalf("errors got %#v", result.Errors)
	}
	if quality.ID != "default" || len(quality.RequiredChecks) == 0 {
		t.Fatalf("quality got %#v", quality)
	}
}

func TestLoadAndValidateEnvironmentExample(t *testing.T) {
	path, err := filepath.Abs("../../examples/environment-local.yaml")
	if err != nil {
		t.Fatal(err)
	}
	env, err := LoadEnvironment(path)
	if err != nil {
		t.Fatal(err)
	}
	result := ValidateEnvironment(env)
	if !result.Valid() {
		t.Fatalf("errors got %#v", result.Errors)
	}
	if env.Commands["test_unit"] == "" {
		t.Fatalf("commands got %#v", env.Commands)
	}
	if env.Executor == nil || env.Executor.DefaultCLI != "codex" {
		t.Fatalf("executor default got %#v", env.Executor)
	}
}

func TestLoadBundleRejectsInvalidQuality(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quality.yaml")
	body := `id: bad
required_checks:
  - id: tests
    preferred_commands: []
    required: true
review_dimensions: []
pass_policy:
  min_score: 85
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadBundle(path, "")
	if err == nil {
		t.Fatal("expected invalid profile error")
	}
	if !strings.Contains(err.Error(), "preferred_commands") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateQualityRejectsInvalidPassPolicyReviewStrength(t *testing.T) {
	result := ValidateQuality(Quality{
		ID: "bad",
		PassPolicy: PassPolicy{
			BlockingSeverities: []string{"critical", "minor"},
		},
	})
	if result.Valid() {
		t.Fatal("expected invalid profile")
	}
	text := strings.Join(result.Errors, "\n")
	if !strings.Contains(text, "blocking_severities") {
		t.Fatalf("errors missing blocking_severities: %#v", result.Errors)
	}
}

func TestLoadBundleRejectsInvalidEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "environment.yaml")
	body := `id: bad
cwd: /tmp
commands: {}
constraints:
  network: ""
  secrets_policy: never_read_env_files
  destructive_commands: deny
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadBundle("", path)
	if err == nil {
		t.Fatal("expected invalid profile error")
	}
	if !strings.Contains(err.Error(), "constraints.network") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateEnvironmentAcceptsExecutorDefaultCLI(t *testing.T) {
	for _, value := range []string{"claude", "codex", "glm"} {
		value := value
		t.Run(value, func(t *testing.T) {
			env := validEnvironmentForTest()
			env.Executor = &ExecutorDefault{DefaultCLI: value}
			result := ValidateEnvironment(env)
			if !result.Valid() {
				t.Fatalf("errors got %#v", result.Errors)
			}
		})
	}
}

func TestValidateEnvironmentRejectsInvalidExecutorDefault(t *testing.T) {
	env := Environment{
		ID:       "local",
		CWD:      "/tmp/repo",
		Commands: map[string]string{"test": "go test ./..."},
		Executor: &ExecutorDefault{DefaultCLI: "other"},
		Constraints: Constraints{
			Network:             "approval_required",
			SecretsPolicy:       "never_read_env_files",
			DestructiveCommands: "deny",
		},
	}
	result := ValidateEnvironment(env)
	if result.Valid() {
		t.Fatal("expected invalid executor default")
	}
	if !strings.Contains(strings.Join(result.Errors, "\n"), "executor.default_cli") {
		t.Fatalf("errors missing executor.default_cli: %#v", result.Errors)
	}
}

func TestValidateEnvironmentAcceptsSupervisorDefaultCLI(t *testing.T) {
	// Parameterized over the single supervisor-CLI source so a new value forces
	// this profile-validation site to accept it too — no silent drift.
	for _, value := range daemonconfig.SupervisorCLIs() {
		value := value
		t.Run(value, func(t *testing.T) {
			env := validEnvironmentForTest()
			env.Supervisor = &SupervisorDefault{DefaultCLI: value}
			result := ValidateEnvironment(env)
			if !result.Valid() {
				t.Fatalf("errors got %#v", result.Errors)
			}
		})
	}
}

func TestValidateEnvironmentRejectsInvalidSupervisorDefault(t *testing.T) {
	env := validEnvironmentForTest()
	env.Supervisor = &SupervisorDefault{DefaultCLI: "opus"}
	result := ValidateEnvironment(env)
	if result.Valid() {
		t.Fatal("expected invalid supervisor default")
	}
	if !strings.Contains(strings.Join(result.Errors, "\n"), "supervisor.default_cli") {
		t.Fatalf("errors missing supervisor.default_cli: %#v", result.Errors)
	}
}

func TestValidateEnvironmentAcceptsRequiredCheckShell(t *testing.T) {
	env := validEnvironmentForTest()
	env.RequiredChecks.Shell = "bash"
	result := ValidateEnvironment(env)
	if !result.Valid() {
		t.Fatalf("errors got %#v", result.Errors)
	}
}

func TestValidateEnvironmentRejectsInvalidRequiredCheckShell(t *testing.T) {
	env := validEnvironmentForTest()
	env.RequiredChecks.Shell = "fish"
	result := ValidateEnvironment(env)
	if result.Valid() {
		t.Fatal("expected invalid required check shell")
	}
	if !strings.Contains(strings.Join(result.Errors, "\n"), "required_checks.shell") {
		t.Fatalf("errors missing required_checks.shell: %#v", result.Errors)
	}
}

func TestValidateEnvironmentRejectsUnsafeSetupCommandTextShape(t *testing.T) {
	env := validEnvironmentForTest()
	env.Setup = &SetupPlan{Commands: []SetupCommand{{
		Run: strings.Repeat("x", MaxSetupCommandRunLength+1),
		Why: "too long run",
	}}}
	result := ValidateEnvironment(env)
	if result.Valid() {
		t.Fatal("expected oversized setup command to be invalid")
	}
	if !strings.Contains(strings.Join(result.Errors, "\n"), "setup.commands[0].run") {
		t.Fatalf("expected setup command run error, got %#v", result.Errors)
	}

	env = validEnvironmentForTest()
	env.Setup = &SetupPlan{Commands: []SetupCommand{{
		Run: "printf ok\x00",
		Why: "contains nul",
	}}}
	result = ValidateEnvironment(env)
	if result.Valid() {
		t.Fatal("expected setup command with control char to be invalid")
	}
	if !strings.Contains(strings.Join(result.Errors, "\n"), "control characters") {
		t.Fatalf("expected control character error, got %#v", result.Errors)
	}
}

func validEnvironmentForTest() Environment {
	return Environment{
		ID:       "local",
		CWD:      "/tmp/repo",
		Commands: map[string]string{"test": "go test ./..."},
		Constraints: Constraints{
			Network:             "approval_required",
			SecretsPolicy:       "never_read_env_files",
			DestructiveCommands: "deny",
		},
	}
}
