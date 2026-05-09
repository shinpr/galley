package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOptionsWithManifestAppliesDefaultsWhenNotExplicit(t *testing.T) {
	t.Parallel()
	manifestPath := writeOptionsManifest(t, `version: 1
defaults:
  system_prompt_file: manifest-prompt.md
  json_schema_file: manifest-schema.json
  max_concurrent_tasks: 4
  max_concurrent_per_repo: 2
  poll_interval: 2s
  open_pr: true
  poll_pr_comments: true
  cleanup_worktrees: true
  pr_base: develop
  supervisor: codex
`)
	opts, err := (Options{ManifestFile: manifestPath}).withManifest()
	if err != nil {
		t.Fatal(err)
	}
	if opts.SystemPromptFile != "manifest-prompt.md" {
		t.Fatalf("system prompt got %q", opts.SystemPromptFile)
	}
	if opts.JSONSchemaFile != "manifest-schema.json" {
		t.Fatalf("schema got %q", opts.JSONSchemaFile)
	}
	if opts.MaxConcurrentTasks != 4 {
		t.Fatalf("max concurrent got %d", opts.MaxConcurrentTasks)
	}
	if opts.MaxConcurrentPerRepo != 2 {
		t.Fatalf("max concurrent per repo got %d", opts.MaxConcurrentPerRepo)
	}
	if opts.PollInterval != 2*time.Second {
		t.Fatalf("poll interval got %s", opts.PollInterval)
	}
	if !opts.OpenPR || !opts.PollPRComments || !opts.CleanupWorktrees || opts.PRBase != "develop" || opts.Supervisor != "codex" {
		t.Fatalf("manifest bool/string defaults not applied: %#v", opts)
	}
}

func TestOptionsWithManifestKeepsExplicitValues(t *testing.T) {
	t.Parallel()
	manifestPath := writeOptionsManifest(t, `version: 1
defaults:
  system_prompt_file: manifest-prompt.md
  json_schema_file: manifest-schema.json
  max_concurrent_tasks: 4
  max_concurrent_per_repo: 2
  poll_interval: 2s
  open_pr: true
  poll_pr_comments: true
  cleanup_worktrees: true
  pr_base: develop
  supervisor: codex
`)
	opts, err := (Options{
		ManifestFile:         manifestPath,
		SystemPromptFile:     "cli-prompt.md",
		MaxConcurrentTasks:   9,
		MaxConcurrentPerRepo: 0,
		PollInterval:         5 * time.Second,
		OpenPR:               false,
		PollPRComments:       false,
		CleanupWorktrees:     false,
		PRBase:               "main",
		Supervisor:           "claude",
		Explicit: ExplicitOptions{
			SystemPromptFile:     true,
			MaxConcurrentTasks:   true,
			MaxConcurrentPerRepo: true,
			PollInterval:         true,
			OpenPR:               true,
			PollPRComments:       true,
			CleanupWorktrees:     true,
			PRBase:               true,
			Supervisor:           true,
		},
	}).withManifest()
	if err != nil {
		t.Fatal(err)
	}
	if opts.SystemPromptFile != "cli-prompt.md" {
		t.Fatalf("system prompt got %q", opts.SystemPromptFile)
	}
	if opts.MaxConcurrentTasks != 9 || opts.MaxConcurrentPerRepo != 0 || opts.PollInterval != 5*time.Second {
		t.Fatalf("numeric explicit values overwritten: %#v", opts)
	}
	if opts.OpenPR || opts.PollPRComments || opts.CleanupWorktrees {
		t.Fatalf("explicit false bools overwritten: %#v", opts)
	}
	if opts.PRBase != "main" {
		t.Fatalf("pr base got %q", opts.PRBase)
	}
	if opts.Supervisor != "claude" {
		t.Fatalf("supervisor got %q", opts.Supervisor)
	}
	if opts.JSONSchemaFile != "manifest-schema.json" {
		t.Fatalf("non-explicit schema should come from manifest, got %q", opts.JSONSchemaFile)
	}
}

func TestPreflightRejectsOpenPRWithExplicitCommitDisabled(t *testing.T) {
	t.Parallel()
	_, err := Preflight(Options{
		Root:           t.TempDir(),
		OpenPR:         true,
		CommitOnAccept: false,
		Explicit: ExplicitOptions{
			CommitOnAccept: true,
		},
	})
	if err == nil {
		t.Fatal("expected open PR commit-on-accept conflict")
	}
}

func writeOptionsManifest(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "repos.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
