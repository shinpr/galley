package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/task"
)

type ExternalOptions struct {
	Argv        []string
	WorkDir     string
	Timeout     time.Duration
	ArtifactDir string
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
	SourceCWD    string              `json:"source_cwd,omitempty"`
	WorktreeCWD  string              `json:"worktree_cwd,omitempty"`
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
	request := NewExternalRequest(evidence)
	request.Evidence.WorktreeCWD = opts.WorkDir
	payload, err := json.Marshal(request)
	if err != nil {
		return Verdict{}, err
	}
	if opts.ArtifactDir != "" {
		if err := os.MkdirAll(opts.ArtifactDir, 0o700); err != nil {
			return Verdict{}, fmt.Errorf("create supervisor artifact dir: %w", err)
		}
		if err := os.WriteFile(filepath.Join(opts.ArtifactDir, "supervisor_request.json"), payload, 0o600); err != nil {
			return Verdict{}, fmt.Errorf("write supervisor request: %w", err)
		}
	}
	cmd := exec.CommandContext(runCtx, opts.Argv[0], opts.Argv[1:]...)
	cmd.Dir = opts.WorkDir
	cmd.Stdin = bytes.NewReader(payload)
	if opts.ArtifactDir != "" {
		cmd.Env = append(os.Environ(), "GALLEY_SUPERVISOR_ARTIFACT_DIR="+opts.ArtifactDir)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		writeSupervisorOutput(opts.ArtifactDir, stdout.Bytes(), stderr.Bytes())
		return Verdict{}, fmt.Errorf("external supervisor failed: %w: %s", err, stderr.String())
	}
	writeSupervisorOutput(opts.ArtifactDir, stdout.Bytes(), stderr.Bytes())
	var verdict Verdict
	if err := json.Unmarshal(stdout.Bytes(), &verdict); err != nil {
		return Verdict{}, fmt.Errorf("decode external supervisor verdict: %w", err)
	}
	if err := ValidateVerdictForEvidence(verdict, evidence); err != nil {
		return Verdict{}, err
	}
	return verdict, nil
}

func writeSupervisorOutput(dir string, stdout, stderr []byte) {
	if dir == "" {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "supervisor_stdout.log"), stdout, 0o600)
	_ = os.WriteFile(filepath.Join(dir, "supervisor_stderr.log"), stderr, 0o600)
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
		SourceCWD:    evidence.Task.Scope.CWD,
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
	if verdict.Confidence == "" {
		return fmt.Errorf("supervisor verdict confidence is required")
	}
	if verdict.Status == "needs_revision" && verdict.NextWorkOrder == "" {
		return fmt.Errorf("needs_revision verdict requires next_work_order")
	}
	switch verdict.Confidence {
	case "high", "medium", "low":
	default:
		return fmt.Errorf("invalid supervisor verdict confidence %q", verdict.Confidence)
	}
	for i, finding := range verdict.Findings {
		if !validSeverity(finding.Severity) {
			return fmt.Errorf("supervisor verdict findings[%d].severity is invalid", i)
		}
		if finding.Category == "" {
			return fmt.Errorf("supervisor verdict findings[%d].category is required", i)
		}
		if finding.Summary == "" {
			return fmt.Errorf("supervisor verdict findings[%d].summary is required", i)
		}
	}
	return nil
}

func ValidateVerdictForEvidence(verdict Verdict, evidence Evidence) error {
	if err := ValidateVerdict(verdict); err != nil {
		return err
	}
	for i, finding := range verdict.Findings {
		shouldBlock := severityBlocksAcceptance(finding.Severity, evidence.Profiles.Quality)
		if finding.BlocksAcceptance != shouldBlock {
			return fmt.Errorf("supervisor verdict findings[%d].blocks_acceptance=%t does not match pass policy for severity %q", i, finding.BlocksAcceptance, finding.Severity)
		}
	}
	if verdict.Status != "accepted" {
		return nil
	}
	if evidence.DiffDirty && len(verdict.ReviewedFiles) == 0 {
		return fmt.Errorf("accepted supervisor verdict requires reviewed_files when diff is present")
	}
	if verdict.Confidence == "low" {
		return fmt.Errorf("accepted supervisor verdict cannot have low confidence")
	}
	for i, finding := range verdict.Findings {
		if finding.BlocksAcceptance {
			return fmt.Errorf("accepted supervisor verdict has blocking finding at findings[%d]: %s", i, finding.Summary)
		}
	}
	for _, missing := range missingAcceptanceEvidence(verdict, evidence) {
		return fmt.Errorf("accepted supervisor verdict missing acceptance evidence for %s", missing)
	}
	return nil
}

func missingAcceptanceEvidence(verdict Verdict, evidence Evidence) []string {
	covered := make(map[string]bool, len(verdict.AcceptanceEvidence))
	for _, item := range verdict.AcceptanceEvidence {
		if item.ACID != "" && len(item.Evidence) > 0 {
			covered[item.ACID] = true
		}
	}
	var missing []string
	for _, ac := range evidence.Task.AcceptanceCriteria {
		if !covered[ac.ID] {
			missing = append(missing, ac.ID)
		}
	}
	for _, request := range evidence.Task.RevisionRequests {
		if request.Status == "addressed" {
			continue
		}
		id := "revision:" + request.ID
		if !covered[id] {
			missing = append(missing, id)
		}
	}
	return missing
}

func severityBlocksAcceptance(severity string, quality *profile.Quality) bool {
	for _, blocking := range blockingSeverities(quality) {
		if severity == blocking {
			return true
		}
	}
	return false
}

func blockingSeverities(quality *profile.Quality) []string {
	if quality != nil && len(quality.PassPolicy.BlockingSeverities) > 0 {
		return quality.PassPolicy.BlockingSeverities
	}
	return []string{"critical", "high", "medium"}
}

func validSeverity(value string) bool {
	switch value {
	case "critical", "high", "medium", "low":
		return true
	default:
		return false
	}
}
