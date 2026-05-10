package result

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/shinpr/galley/internal/jsonio"
	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/task"
)

// CompleteOptions controls executor result generation from verification evidence.
type CompleteOptions struct {
	TaskFile string
	Output   string
	WorkDir  string
	Summary  string
	Profiles profile.Bundle
	GitBin   string
}

// Complete runs required profile checks and writes a validated Claude result
// JSON file. Acceptance criterion verification strings are evidence guidance
// for the executor and supervisor; Galley does not execute them as shell.
func Complete(ctx context.Context, opts CompleteOptions) (runner.ClaudeResult, error) {
	if opts.TaskFile == "" {
		return runner.ClaudeResult{}, fmt.Errorf("task file is required")
	}
	if opts.Output == "" {
		return runner.ClaudeResult{}, fmt.Errorf("output is required")
	}
	if opts.Summary == "" {
		return runner.ClaudeResult{}, fmt.Errorf("summary is required")
	}

	loaded, err := task.Load(opts.TaskFile)
	if err != nil {
		return runner.ClaudeResult{}, err
	}
	task.ApplyDefaults(&loaded)
	if validation := task.Validate(loaded); !validation.Valid() {
		return runner.ClaudeResult{}, fmt.Errorf("invalid task %s: %s", opts.TaskFile, strings.Join(validation.Errors, "; "))
	}
	workDir := opts.WorkDir
	if workDir == "" {
		workDir = loaded.Scope.CWD
	}
	if workDir == "" {
		return runner.ClaudeResult{}, fmt.Errorf("workdir is required")
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if loaded.ExecutionPolicy.TimeoutMS > 0 && !contextHasDeadline(ctx) {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(loaded.ExecutionPolicy.TimeoutMS)*time.Millisecond)
		defer cancel()
	}

	criteria := make([]runner.ClaudeAcceptanceCriterion, 0, len(loaded.AcceptanceCriteria))
	verification := []runner.ClaudeVerification{}
	risks := []runner.ClaudeRisk{}
	status := "completed"
	for _, ac := range loaded.AcceptanceCriteria {
		criteria = append(criteria, runner.ClaudeAcceptanceCriterion{
			ID:       ac.ID,
			Status:   "not_satisfied",
			Evidence: []string{},
			Notes:    fmt.Sprintf("Acceptance verification guidance: %s", ac.Verification),
		})
	}
	for _, check := range requiredChecks(opts.Profiles) {
		checkResult := runRequiredCheck(runCtx, workDir, check)
		if checkResult.err != nil {
			status = "completed_with_risks"
			risks = append(risks, runner.ClaudeRisk{
				Type:             "partial_verification",
				Detail:           fmt.Sprintf("quality check %s failed: %s", check.ID, checkResult.reason()),
				Mitigation:       "Inspect the executor workspace and rerun the required quality check after fixing the task output.",
				NeedsHumanReview: true,
			})
		}
		verification = append(verification, runner.ClaudeVerification{
			Command:       checkResult.command,
			Status:        checkResult.status(),
			Reason:        checkResult.reason(),
			OutputExcerpt: checkResult.outputExcerpt(),
		})
	}

	files, err := gitChangedFiles(runCtx, workDir, opts.GitBin)
	if err != nil {
		status = "completed_with_risks"
	}
	if files == nil {
		files = []string{}
	}

	result := runner.ClaudeResult{
		Status:             status,
		Summary:            opts.Summary,
		FilesModified:      files,
		AcceptanceCriteria: criteria,
		Verification:       verification,
		Decisions:          []runner.ClaudeDecision{},
		Risks:              risks,
	}
	if err != nil {
		result.Risks = append(result.Risks, runner.ClaudeRisk{
			Type:             "partial_verification",
			Detail:           err.Error(),
			Mitigation:       "Inspect git status manually in the execution workspace.",
			NeedsHumanReview: true,
		})
	}
	if err := result.Validate(); err != nil {
		return runner.ClaudeResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(opts.Output), 0o700); err != nil {
		return runner.ClaudeResult{}, fmt.Errorf("create result dir: %w", err)
	}
	if err := jsonio.Write(opts.Output, result); err != nil {
		return runner.ClaudeResult{}, fmt.Errorf("write result file %s: %w", opts.Output, err)
	}
	return result, nil
}

func contextHasDeadline(ctx context.Context) bool {
	_, ok := ctx.Deadline()
	return ok
}

func requiredChecks(profiles profile.Bundle) []profile.RequiredCheck {
	if profiles.Quality == nil {
		return nil
	}
	var checks []profile.RequiredCheck
	for _, check := range profiles.Quality.RequiredChecks {
		if check.Required {
			checks = append(checks, check)
		}
	}
	return checks
}

func runRequiredCheck(ctx context.Context, workDir string, check profile.RequiredCheck) verificationRun {
	if len(check.PreferredCommands) == 0 {
		return verificationRun{command: check.ID, err: fmt.Errorf("required check has no preferred commands")}
	}
	var last verificationRun
	for _, command := range check.PreferredCommands {
		if strings.TrimSpace(command) == "" {
			continue
		}
		last = runVerification(ctx, workDir, command)
		if last.err == nil {
			return last
		}
	}
	if last.command == "" {
		return verificationRun{command: check.ID, err: fmt.Errorf("required check has no runnable preferred commands")}
	}
	return last
}

type verificationRun struct {
	command string
	stdout  string
	stderr  string
	err     error
}

func runVerification(ctx context.Context, workDir, command string) verificationRun {
	// Profile commands are trusted operator-authored shell commands. Run them
	// through the shared runner so cancellation, process cleanup, and output
	// bounding behave like the rest of Galley's subprocesses.
	result, err := runner.RunCommand(ctx, runner.Command{
		WorkDir: workDir,
		Argv:    []string{"/bin/sh", "-c", command},
	}, runner.RunOptions{TailBytes: 2048})
	return verificationRun{
		command: command,
		stdout:  result.Stdout,
		stderr:  result.Stderr,
		err:     err,
	}
}

func (r verificationRun) status() string {
	if r.err != nil {
		return "failed"
	}
	return "passed"
}

func (r verificationRun) reason() string {
	if r.err != nil {
		return r.err.Error()
	}
	return "verification command passed"
}

func (r verificationRun) outputExcerpt() string {
	output := strings.TrimSpace(strings.Join([]string{r.stdout, r.stderr}, "\n"))
	if len(output) > 2048 {
		return output[:2048]
	}
	return output
}

func gitChangedFiles(ctx context.Context, workDir, gitBin string) ([]string, error) {
	if gitBin == "" {
		gitBin = "git"
	}
	cmd := exec.CommandContext(ctx, gitBin, "status", "--porcelain", "-z")
	cmd.Dir = workDir
	output, err := cmd.Output()
	if err != nil {
		return []string{}, fmt.Errorf("git status --porcelain -z: %w", err)
	}
	var files []string
	records := bytes.Split(bytes.TrimRight(output, "\x00"), []byte{0})
	for i := 0; i < len(records); i++ {
		record := string(records[i])
		if strings.TrimSpace(record) == "" || len(record) < 4 {
			continue
		}
		status := record[:2]
		path := strings.TrimSpace(record[3:])
		files = append(files, path)
		if (status[0] == 'R' || status[0] == 'C' || status[1] == 'R' || status[1] == 'C') && i+1 < len(records) {
			i++
		}
	}
	return files, nil
}
