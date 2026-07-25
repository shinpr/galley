package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/provider"
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
	if env.Executor == nil || env.Executor.DefaultCLI != "claude" {
		t.Fatalf("executor default got %#v", env.Executor)
	}
}

func TestLoadProfilesIgnoreUnknownFields(t *testing.T) {
	t.Run("quality", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "quality.yaml")
		body := `id: compatible
required_checks:
  - id: tests
    preferred_commands: ["go test ./..."]
    required: true
    future_check_option: enabled
review_dimensions:
  - id: acceptance
    weight: 1
    required: true
    pass: Acceptance criteria pass.
evidence_requirements:
  file_line_references: true
  command_outputs: true
pass_policy:
  required_dimensions_must_pass: true
  min_score: 85
  unresolved_high_findings_allowed: 0
  blocking_severities: [critical, high]
future_profile_option: enabled
`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		quality, err := LoadQuality(path)
		if err != nil {
			t.Fatalf("unknown quality fields should be ignored: %v", err)
		}
		if result := ValidateQuality(quality); !result.Valid() {
			t.Fatalf("quality errors got %#v", result.Errors)
		}
		if quality.ID != "compatible" || quality.PassPolicy.MinScore != 85 {
			t.Fatalf("known quality fields got %#v", quality)
		}
	})

	t.Run("environment", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "environment.yaml")
		body := `id: compatible
cwd: /tmp/repo
commands:
  test: go test ./...
constraints:
  network: approval_required
  secrets_policy: never_read_env_files
  destructive_commands: deny
  future_constraint: enabled
worktree:
  cleanup: true
  future_worktree_option: enabled
future_profile_option: enabled
`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		env, err := LoadEnvironment(path)
		if err != nil {
			t.Fatalf("unknown environment fields should be ignored: %v", err)
		}
		if result := ValidateEnvironment(env); !result.Valid() {
			t.Fatalf("environment errors got %#v", result.Errors)
		}
		if env.ID != "compatible" || env.Commands["test"] != "go test ./..." {
			t.Fatalf("known environment fields got %#v", env)
		}
	})
}

func TestLoadProfilesRejectMissingRequiredFields(t *testing.T) {
	tests := []struct {
		name string
		body string
		load func(string) error
		want string
	}{
		{
			name: "quality nested boolean",
			body: `id: missing-required
required_checks:
  - id: tests
    preferred_commands: ["go test ./..."]
    required: null
review_dimensions: []
evidence_requirements:
  file_line_references: true
  command_outputs: true
pass_policy:
  required_dimensions_must_pass: true
  min_score: 85
  blocking_severities: [high]
`,
			load: func(path string) error {
				_, err := LoadQuality(path)
				return err
			},
			want: "required_checks[0].required",
		},
		{
			name: "environment nested string",
			body: `id: missing-required
cwd: /tmp/repo
constraints:
  network: approval_required
  destructive_commands: deny
`,
			load: func(path string) error {
				_, err := LoadEnvironment(path)
				return err
			},
			want: "constraints.secrets_policy",
		},
		{
			name: "empty quality document",
			body: "",
			load: func(path string) error {
				_, err := LoadQuality(path)
				return err
			},
			want: "missing required fields: id",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "profile.yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			err := tc.load(path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestLoadProfileRejectsKnownFieldTypeMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quality.yaml")
	body := `id: invalid-type
required_checks: []
review_dimensions: []
evidence_requirements:
  file_line_references: true
  command_outputs: true
pass_policy:
  required_dimensions_must_pass: true
  min_score: high
  blocking_severities: [high]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadQuality(path); err == nil || !strings.Contains(err.Error(), "cannot unmarshal") {
		t.Fatalf("expected known-field type error, got %v", err)
	}
}

func TestUpdateEnvironmentSetupPreservesUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "environment.yaml")
	body := `# repository environment
id: compatible
cwd: /tmp/repo
constraints:
  network: approval_required
  secrets_policy: never_read_env_files
  destructive_commands: deny
  future_constraint: enabled
future_profile_option: enabled
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := SetupPlan{Commands: []SetupCommand{{Run: "go mod download", Why: "prepare dependencies"}}}
	prior, err := UpdateEnvironmentSetup(path, plan)
	if err != nil {
		t.Fatalf("update setup with unknown fields: %v", err)
	}
	if prior != nil {
		t.Fatalf("prior setup got %#v, want nil", prior)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{"# repository environment", "future_constraint: enabled", "future_profile_option: enabled", "run: go mod download"} {
		if !strings.Contains(content, want) {
			t.Fatalf("rewritten environment missing %q:\n%s", want, content)
		}
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
evidence_requirements:
  file_line_references: true
  command_outputs: true
pass_policy:
  required_dimensions_must_pass: true
  min_score: 85
  blocking_severities: [high]
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
	for _, value := range []string{"claude", "codex", "glm", "grok"} {
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
	for _, value := range provider.SupervisorIDs() {
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

func TestValidateEnvironmentSupervisorEffortOptionalityAndValidation(t *testing.T) {
	cases := []struct {
		name       string
		supervisor *SupervisorDefault
		wantValid  bool
		wantErr    string
	}{
		{name: "no supervisor block", supervisor: nil, wantValid: true},
		{name: "empty effort preserves default", supervisor: &SupervisorDefault{DefaultCLI: "codex", Effort: ""}, wantValid: true},
		{name: "codex accepts minimal", supervisor: &SupervisorDefault{DefaultCLI: "codex", Effort: "minimal"}, wantValid: true},
		{name: "claude accepts max", supervisor: &SupervisorDefault{DefaultCLI: "claude", Effort: "max"}, wantValid: true},
		{name: "glm accepts xhigh", supervisor: &SupervisorDefault{DefaultCLI: "glm", Effort: "xhigh"}, wantValid: true},
		{name: "claude rejects minimal", supervisor: &SupervisorDefault{DefaultCLI: "claude", Effort: "minimal"}, wantErr: "supervisor.effort for claude must be one of"},
		{name: "codex rejects unknown", supervisor: &SupervisorDefault{DefaultCLI: "codex", Effort: "turbo"}, wantErr: "supervisor.effort for codex must be one of"},
		{name: "effort without default_cli accepts union value", supervisor: &SupervisorDefault{Effort: "minimal"}, wantValid: true},
		{name: "effort without default_cli rejects unknown", supervisor: &SupervisorDefault{Effort: "turbo"}, wantErr: "supervisor.effort must be one of"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := validEnvironmentForTest()
			env.Supervisor = tc.supervisor
			result := ValidateEnvironment(env)
			if tc.wantValid {
				if !result.Valid() {
					t.Fatalf("expected valid, got errors: %#v", result.Errors)
				}
				return
			}
			if result.Valid() {
				t.Fatal("expected invalid supervisor.effort")
			}
			if !strings.Contains(strings.Join(result.Errors, "\n"), tc.wantErr) {
				t.Fatalf("errors missing %q: %#v", tc.wantErr, result.Errors)
			}
		})
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
