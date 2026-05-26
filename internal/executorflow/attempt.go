package executorflow

import (
	"context"
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

func RunCommandAttempt(ctx context.Context, opts CommandAttemptOptions) (CommandAttemptResult, error) {
	auditPlan := opts.CommandPlan
	auditPlan.Env = nil
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
	if err := jsonio.Write(runartifact.Path(opts.AttemptDir, runartifact.RunResultFilename), runResult); err != nil {
		return CommandAttemptResult{}, err
	}
	return CommandAttemptResult{Started: started, Completed: completed, RunResult: runResult, RunErr: runErr}, nil
}

type DiffArtifacts struct {
	Snapshot workspace.Snapshot
	Dirty    bool
	Diff     string
	Err      error
}

func CaptureDiffArtifacts(ctx context.Context, workDir, baseSHA, attemptDir string, opts workspace.Options) (DiffArtifacts, error) {
	snapshot, err := workspace.CaptureSnapshotFromBase(ctx, workDir, baseSHA, opts)
	if err != nil {
		return DiffArtifacts{Snapshot: snapshot, Err: err}, nil
	}
	dirty := snapshot.BranchDiff != "" || snapshot.StagedDiff != "" || snapshot.UnstagedDiff != ""
	if err := jsonio.Write(runartifact.Path(attemptDir, runartifact.GitStatusFilename), snapshot); err != nil {
		return DiffArtifacts{}, err
	}
	if err := os.WriteFile(runartifact.Path(attemptDir, runartifact.DiffPatchFilename), []byte(snapshot.Diff), 0o600); err != nil {
		return DiffArtifacts{}, fmt.Errorf("write diff.patch: %w", err)
	}
	return DiffArtifacts{Snapshot: snapshot, Dirty: dirty, Diff: snapshot.Diff}, nil
}

type RequiredCheckContext struct {
	Profiles profile.Bundle
}
