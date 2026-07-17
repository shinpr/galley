package executorflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/shinpr/galley/internal/jsonio"
	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/workspace"
)

type CommandAttemptOptions struct {
	AttemptDir  string
	CommandPlan runner.Command
	Timeout     time.Duration
	IdleTimeout time.Duration
	StdoutPath  string
	StderrPath  string
}

type CommandAttemptResult struct {
	Started   time.Time
	Completed time.Time
	RunResult runner.RunResult
	RunErr    error
}

// RunCommandAttempt persists the audit command plan (a pre-run failure that
// aborts the attempt), runs the executor command, and returns the raw runner
// outcome. Persisting run_result.json is deliberately left to the caller so the
// terminal classification and every post-run artifact write share one set of
// separate, non-discarding availability flags.
func RunCommandAttempt(ctx context.Context, opts CommandAttemptOptions) (CommandAttemptResult, error) {
	auditPlan := opts.CommandPlan
	if err := jsonio.Write(runartifact.Path(opts.AttemptDir, runartifact.CommandPlanFilename), auditPlan); err != nil {
		return CommandAttemptResult{}, err
	}

	started := time.Now().UTC()
	runResult, runErr := runner.RunCommand(ctx, opts.CommandPlan, runner.RunOptions{
		Timeout:     opts.Timeout,
		IdleTimeout: opts.IdleTimeout,
		StdoutPath:  opts.StdoutPath,
		StderrPath:  opts.StderrPath,
	})
	completed := time.Now().UTC()
	return CommandAttemptResult{Started: started, Completed: completed, RunResult: runResult, RunErr: runErr}, nil
}

type DiffArtifacts struct {
	Snapshot workspace.Snapshot
	Dirty    bool
	Diff     string
	// Err combines every capture and write failure for the normal-terminal
	// hard-fail path. GitStatusErr and DiffPatchErr report each component
	// independently so an interruption inventory can name the exact artifact that
	// was preserved versus unavailable.
	Err          error
	GitStatusErr error
	DiffPatchErr error
}

func CaptureDiffArtifacts(ctx context.Context, workDir, baseSHA, attemptDir string, opts workspace.Options) (DiffArtifacts, error) {
	snapshot, err := workspace.CaptureSnapshotFromBase(ctx, workDir, baseSHA, opts)
	if err != nil {
		// A snapshot-capture failure leaves neither artifact writable.
		return DiffArtifacts{Snapshot: snapshot, Err: err, GitStatusErr: err, DiffPatchErr: err}, nil
	}
	dirty := snapshot.BranchDiff != "" || snapshot.StagedDiff != "" || snapshot.UnstagedDiff != ""
	// Both writes are attempted independently so one failure never suppresses the
	// other retained artifact.
	artifacts := DiffArtifacts{Snapshot: snapshot, Dirty: dirty, Diff: snapshot.Diff}
	var writeErrs []error
	if err := jsonio.Write(runartifact.Path(attemptDir, runartifact.GitStatusFilename), snapshot); err != nil {
		artifacts.GitStatusErr = fmt.Errorf("write git_status.json: %w", err)
		writeErrs = append(writeErrs, artifacts.GitStatusErr)
	}
	if err := os.WriteFile(runartifact.Path(attemptDir, runartifact.DiffPatchFilename), []byte(snapshot.Diff), 0o600); err != nil {
		artifacts.DiffPatchErr = fmt.Errorf("write diff.patch: %w", err)
		writeErrs = append(writeErrs, artifacts.DiffPatchErr)
	}
	if len(writeErrs) > 0 {
		artifacts.Err = errors.Join(writeErrs...)
		return artifacts, artifacts.Err
	}
	return artifacts, nil
}

type RequiredCheckContext struct {
	Profiles profile.Bundle
}
