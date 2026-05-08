package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/task"
)

type ExternalOptions struct {
	Argv    []string
	WorkDir string
	Timeout time.Duration
}

type ExternalRequest struct {
	Evidence ExternalEvidence `json:"evidence"`
}

type ExternalEvidence struct {
	Task         task.Task           `json:"task"`
	Profiles     profile.Bundle      `json:"profiles"`
	Claude       runner.ClaudeResult `json:"claude"`
	ParseError   string              `json:"parse_error,omitempty"`
	RunError     string              `json:"run_error,omitempty"`
	DiffDirty    bool                `json:"diff_dirty"`
	Diff         string              `json:"diff"`
	DiffError    string              `json:"diff_error,omitempty"`
	Attempt      int                 `json:"attempt"`
	AttemptsLeft int                 `json:"attempts_left"`
}

func RunExternal(ctx context.Context, opts ExternalOptions, evidence Evidence) (Verdict, error) {
	if len(opts.Argv) == 0 {
		return Verdict{}, fmt.Errorf("supervisor command argv is empty")
	}
	runCtx := ctx
	var cancel context.CancelFunc
	if opts.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	payload, err := json.Marshal(NewExternalRequest(evidence))
	if err != nil {
		return Verdict{}, err
	}
	cmd := exec.CommandContext(runCtx, opts.Argv[0], opts.Argv[1:]...)
	cmd.Dir = opts.WorkDir
	cmd.Stdin = bytes.NewReader(payload)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return Verdict{}, fmt.Errorf("external supervisor failed: %w: %s", err, stderr.String())
	}
	var verdict Verdict
	if err := json.Unmarshal(stdout.Bytes(), &verdict); err != nil {
		return Verdict{}, fmt.Errorf("decode external supervisor verdict: %w", err)
	}
	if err := ValidateVerdict(verdict); err != nil {
		return Verdict{}, err
	}
	return verdict, nil
}

func NewExternalRequest(evidence Evidence) ExternalRequest {
	return ExternalRequest{Evidence: ExternalEvidence{
		Task:         evidence.Task,
		Profiles:     evidence.Profiles,
		Claude:       evidence.Claude,
		ParseError:   errorString(evidence.ParseError),
		RunError:     errorString(evidence.RunError),
		DiffDirty:    evidence.DiffDirty,
		Diff:         evidence.Diff,
		DiffError:    errorString(evidence.DiffError),
		Attempt:      evidence.Attempt,
		AttemptsLeft: evidence.AttemptsLeft,
	}}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func ValidateVerdict(verdict Verdict) error {
	switch verdict.Status {
	case "accepted", "needs_revision", "needs_supervisor_review", "hard_stop":
	default:
		return fmt.Errorf("invalid supervisor verdict status %q", verdict.Status)
	}
	if verdict.Summary == "" {
		return fmt.Errorf("supervisor verdict summary is required")
	}
	if verdict.Status == "needs_revision" && verdict.NextWorkOrder == "" {
		return fmt.Errorf("needs_revision verdict requires next_work_order")
	}
	return nil
}
