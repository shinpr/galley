package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
