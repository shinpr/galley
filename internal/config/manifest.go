package config

import (
	"bytes"
	"fmt"
	"os"
	"time"

	"go.yaml.in/yaml/v3"
)

// Manifest configures Galley defaults for a repository or workflow root.
type Manifest struct {
	Version  int      `yaml:"version" json:"version"`
	Defaults Defaults `yaml:"defaults" json:"defaults"`
}

type Defaults struct {
	SystemPromptFile       string        `yaml:"system_prompt_file" json:"system_prompt_file"`
	JSONSchemaFile         string        `yaml:"json_schema_file" json:"json_schema_file"`
	QualityProfileFile     string        `yaml:"quality_profile_file" json:"quality_profile_file"`
	EnvironmentProfileFile string        `yaml:"environment_profile_file" json:"environment_profile_file"`
	MaxConcurrentTasks     int           `yaml:"max_concurrent_tasks" json:"max_concurrent_tasks"`
	MaxConcurrentPerRepo   int           `yaml:"max_concurrent_per_repo" json:"max_concurrent_per_repo"`
	PollInterval           time.Duration `yaml:"poll_interval" json:"poll_interval"`
	ClaimTTL               time.Duration `yaml:"claim_ttl" json:"claim_ttl"`
	HeartbeatInterval      time.Duration `yaml:"heartbeat_interval" json:"heartbeat_interval"`
	CommitOnAccept         bool          `yaml:"commit_on_accept" json:"commit_on_accept"`
	OpenPR                 bool          `yaml:"open_pr" json:"open_pr"`
	PollPRComments         bool          `yaml:"poll_pr_comments" json:"poll_pr_comments"`
	ReplyPRComments        bool          `yaml:"reply_pr_comments" json:"reply_pr_comments"`
	CleanupWorktrees       bool          `yaml:"cleanup_worktrees" json:"cleanup_worktrees"`
	PRBase                 string        `yaml:"pr_base" json:"pr_base"`
	Supervisor             string        `yaml:"supervisor" json:"supervisor"`
}

type ValidationResult struct {
	Errors   []string `json:"errors"`
	Warnings []string `json:"warnings"`
}

func (r ValidationResult) Valid() bool {
	return len(r.Errors) == 0
}

func LoadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("read %s: %w", path, err)
	}
	var manifest Manifest
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode %s: %w", path, err)
	}
	if result := ValidateManifest(manifest); !result.Valid() {
		return Manifest{}, fmt.Errorf("invalid manifest %s: %v", path, result.Errors)
	}
	return manifest, nil
}

func ValidateManifest(manifest Manifest) ValidationResult {
	var result ValidationResult
	if manifest.Version <= 0 {
		result.Errors = append(result.Errors, "version must be positive")
	}
	if manifest.Defaults.MaxConcurrentTasks < 0 {
		result.Errors = append(result.Errors, "defaults.max_concurrent_tasks cannot be negative")
	}
	if manifest.Defaults.MaxConcurrentPerRepo < 0 {
		result.Errors = append(result.Errors, "defaults.max_concurrent_per_repo cannot be negative")
	}
	switch manifest.Defaults.Supervisor {
	case "", "codex", "claude":
	default:
		result.Errors = append(result.Errors, "defaults.supervisor must be one of: codex, claude")
	}
	return result
}
