package result

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/task"
)

// CompleteOptions controls deterministic executor result generation.
type CompleteOptions struct {
	TaskFile string
	Output   string
	WorkDir  string
	Summary  string
	Profiles profile.Bundle
}

// Complete runs task verification commands and writes a validated Claude result JSON file.
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
	if loaded.ExecutionPolicy.TimeoutMS > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(loaded.ExecutionPolicy.TimeoutMS)*time.Millisecond)
		defer cancel()
	}

	criteria := make([]runner.ClaudeAcceptanceCriterion, 0, len(loaded.AcceptanceCriteria))
	verification := make([]runner.ClaudeVerification, 0, len(loaded.AcceptanceCriteria))
	risks := []runner.ClaudeRisk{}
	status := "completed"
	for _, ac := range loaded.AcceptanceCriteria {
		commandResult := runVerification(runCtx, workDir, ac.Verification)
		acStatus := "satisfied"
		if commandResult.err != nil {
			acStatus = "not_satisfied"
			status = "completed_with_risks"
			risks = append(risks, runner.ClaudeRisk{
				Type:             "partial_verification",
				Detail:           fmt.Sprintf("%s failed: %s", ac.ID, commandResult.reason()),
				Mitigation:       "Inspect the executor workspace and rerun the verification command after fixing the task output.",
				NeedsHumanReview: true,
			})
		}
		criteria = append(criteria, runner.ClaudeAcceptanceCriterion{
			ID:       ac.ID,
			Status:   acStatus,
			Evidence: []string{commandResult.evidence()},
			Notes:    commandResult.reason(),
		})
		verification = append(verification, runner.ClaudeVerification{
			Command:       ac.Verification,
			Status:        commandResult.status(),
			Reason:        commandResult.reason(),
			OutputExcerpt: commandResult.outputExcerpt(),
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

	files, err := gitChangedFiles(runCtx, workDir)
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
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return runner.ClaudeResult{}, err
	}
	data = append(data, '\n')
	if err := os.WriteFile(opts.Output, data, 0o600); err != nil {
		return runner.ClaudeResult{}, fmt.Errorf("write result file %s: %w", opts.Output, err)
	}
	return result, nil
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
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Dir = workDir
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return verificationRun{
		command: command,
		stdout:  stdout.String(),
		stderr:  stderr.String(),
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

func (r verificationRun) evidence() string {
	if r.err != nil {
		return fmt.Sprintf("%s failed: %s", r.command, r.outputExcerpt())
	}
	return fmt.Sprintf("%s passed: %s", r.command, r.outputExcerpt())
}

func (r verificationRun) outputExcerpt() string {
	output := strings.TrimSpace(strings.Join([]string{r.stdout, r.stderr}, "\n"))
	if len(output) > 2048 {
		return output[:2048]
	}
	return output
}

func gitChangedFiles(ctx context.Context, workDir string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = workDir
	output, err := cmd.Output()
	if err != nil {
		return []string{}, fmt.Errorf("git status --porcelain: %w", err)
	}
	var files []string
	for _, line := range strings.Split(string(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		path := strings.TrimSpace(line[3:])
		if strings.Contains(path, " -> ") {
			parts := strings.Split(path, " -> ")
			path = parts[len(parts)-1]
		}
		files = append(files, path)
	}
	return files, nil
}
