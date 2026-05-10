// Package result — skeleton checkpoint execution.
//
// This file extends the existing result.Complete verification path with the
// helpers needed to execute Galley-owned acceptance skeleton checkpoint
// commands inside the executor result completion path. Keeping checkpoint
// execution next to runRequiredCheck/runVerification ensures the two evidence
// streams share the same shell runner, timeout discipline, and output bounding
// rules so attempt evidence cannot drift between code paths.
//
// The CheckpointSpec / CheckpointResult shape and the source tag are
// stable contract values consumed by the daemon supervisor evidence and the
// CLI task-show preflight view. Daemon code converts AcceptanceSkeletonOutput
// records into CheckpointSpecs and persists the returned CheckpointResults
// under attempt-scoped run evidence.
package result

import (
	"context"
	"strings"
	"time"

	"github.com/shinpr/galley/internal/runner"
)

// CheckpointSourceAcceptanceSkeleton is the canonical Source tag stored on
// every skeleton checkpoint result. Operators can grep evidence files for this
// constant to locate Galley-executed skeleton checkpoints (D8).
const CheckpointSourceAcceptanceSkeleton = "acceptance_skeleton"

// CheckpointSpec is the minimum input the result package needs to execute one
// skeleton checkpoint command. The daemon converts each
// AcceptanceSkeletonOutput into a CheckpointSpec; result.RunSkeletonCheckpoints
// then runs the command through the shared runner used by required quality
// checks.
type CheckpointSpec struct {
	ACID    string
	Command string
}

// CheckpointResult mirrors the attempt-scoped checkpoint evidence the daemon
// persists alongside other run evidence. The JSON shape is part of the run
// evidence contract and is consumed by the supervisor adapter and by
// galley task show.
type CheckpointResult struct {
	ACID          string `json:"ac_id"`
	Command       string `json:"command"`
	Status        string `json:"status"`
	ExitCode      int    `json:"exit_code"`
	DurationMS    int64  `json:"duration_ms"`
	StdoutExcerpt string `json:"stdout_excerpt,omitempty"`
	StderrExcerpt string `json:"stderr_excerpt,omitempty"`
	Source        string `json:"source"`
}

// RunSkeletonCheckpoints executes every skeleton checkpoint command via the
// shared runner used by runVerification and returns one CheckpointResult per
// spec. Empty commands are recorded as "skipped" so missing checkpoint
// commands surface in evidence without failing the attempt; non-empty commands
// run inside workDir, capture exit code, duration, and bounded stdout/stderr
// excerpts, and are tagged CheckpointSourceAcceptanceSkeleton so consumers
// distinguish them from required-check verification.
//
// perCommandTimeout bounds each command independently so a stuck checkpoint
// cannot consume the whole attempt budget. A zero value means no per-command
// timeout — the caller's context still applies.
func RunSkeletonCheckpoints(ctx context.Context, workDir string, specs []CheckpointSpec, perCommandTimeout time.Duration) []CheckpointResult {
	results := make([]CheckpointResult, 0, len(specs))
	for _, spec := range specs {
		results = append(results, runOneCheckpoint(ctx, workDir, spec, perCommandTimeout))
	}
	return results
}

func runOneCheckpoint(ctx context.Context, workDir string, spec CheckpointSpec, perCommandTimeout time.Duration) CheckpointResult {
	command := strings.TrimSpace(spec.Command)
	if command == "" {
		return CheckpointResult{
			ACID:    spec.ACID,
			Command: spec.Command,
			Status:  "skipped",
			Source:  CheckpointSourceAcceptanceSkeleton,
		}
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if perCommandTimeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, perCommandTimeout)
		defer cancel()
	}

	started := time.Now()
	out, err := runner.RunCommand(runCtx, runner.Command{
		WorkDir: workDir,
		Argv:    []string{"/bin/sh", "-c", command},
	}, runner.RunOptions{TailBytes: 2048})
	duration := time.Since(started).Milliseconds()
	status := "passed"
	if err != nil {
		status = "failed"
	}
	return CheckpointResult{
		ACID:          spec.ACID,
		Command:       spec.Command,
		Status:        status,
		ExitCode:      out.ExitCode,
		DurationMS:    duration,
		StdoutExcerpt: clipExcerpt(out.Stdout, 2048),
		StderrExcerpt: clipExcerpt(out.Stderr, 2048),
		Source:        CheckpointSourceAcceptanceSkeleton,
	}
}

func clipExcerpt(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit]
}
