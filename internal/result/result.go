package result

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/shinpr/galley/internal/jsonio"
	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/task"
)

var (
	lookPath = exec.LookPath
	statFile = os.Stat
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
	artifactDir := filepath.Dir(opts.Output)
	for _, check := range requiredChecks(opts.Profiles) {
		checkResult := runRequiredCheck(runCtx, workDir, artifactDir, check, opts.Profiles.Environment)
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

func runRequiredCheck(ctx context.Context, workDir, scratchDir string, check profile.RequiredCheck, env *profile.Environment) verificationRun {
	if len(check.PreferredCommands) == 0 {
		return verificationRun{command: check.ID, err: fmt.Errorf("required check has no preferred commands")}
	}
	var last verificationRun
	for _, command := range check.PreferredCommands {
		if strings.TrimSpace(command) == "" {
			continue
		}
		last = runVerification(ctx, workDir, scratchDir, command, env)
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
	command  string
	shell    string
	shellBin string
	stdout   string
	stderr   string
	err      error
}

func runVerification(ctx context.Context, workDir, scratchDir, command string, env *profile.Environment) verificationRun {
	// Profile commands are trusted operator-authored shell commands. Run them
	// through the shared runner so cancellation, process cleanup, and output
	// bounding behave like the rest of Galley's subprocesses.
	shellKind, shellPath := requiredCheckShellSettings(env)
	argv, cleanup, shell, err := shellArgvForOS(runtime.GOOS, command, scratchDir, shellKind, shellPath)
	if err != nil {
		return verificationRun{command: command, err: err}
	}
	if cleanup != nil {
		defer cleanup()
	}
	result, err := runner.RunCommand(ctx, runner.Command{
		WorkDir: workDir,
		Argv:    argv,
	}, runner.RunOptions{TailBytes: 2048})
	return verificationRun{
		command:  command,
		shell:    shell,
		shellBin: argv[0],
		stdout:   result.Stdout,
		stderr:   result.Stderr,
		err:      err,
	}
}

func requiredCheckShellSettings(env *profile.Environment) (string, string) {
	if env == nil {
		return "", ""
	}
	return env.RequiredChecks.Shell, env.RequiredChecks.ShellPath
}

// shellArgvForOS returns the argv that runVerification should hand to the
// shared runner, plus an optional cleanup function for materialized script
// files. On Windows, an unset shell resolves to a standard Git for Windows
// Bash install when one is discoverable and falls back to cmd.exe; PATH
// entries that point at the WSL launcher (System32\bash.exe) or a
// WindowsApps shim are not selected as Git Bash. An explicit configured
// shell kind paired with a configuredShellPath uses that executable path
// verbatim and skips discovery.
func shellArgvForOS(goos, command, scratchDir, configuredShell, configuredShellPath string) ([]string, func(), string, error) {
	shell, err := resolveShellForOS(goos, configuredShell, configuredShellPath)
	if err != nil {
		return nil, nil, "", err
	}
	if goos != "windows" {
		return shellArgv(shell, command), nil, shell.Kind, nil
	}
	dir := scratchDir
	cleanup := func() {}
	if dir == "" {
		tmp, err := os.MkdirTemp("", "galley-windows-check-*")
		if err != nil {
			return nil, nil, "", fmt.Errorf("create windows verification script dir: %w", err)
		}
		dir = tmp
		cleanup = func() { _ = os.RemoveAll(tmp) }
	} else if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, "", fmt.Errorf("create windows verification script dir %s: %w", dir, err)
	}
	ext := ".cmd"
	body := []byte("@echo off\r\n" + command + "\r\n")
	if shell.Kind == "bash" {
		ext = ".sh"
		body = []byte("#!/usr/bin/env bash\nset -e\n" + command + "\n")
	} else if shell.Kind == "sh" {
		ext = ".sh"
		body = []byte("set -e\n" + command + "\n")
	} else if shell.Kind == "powershell" || shell.Kind == "pwsh" {
		ext = ".ps1"
		body = []byte("$ErrorActionPreference = 'Stop'\r\n" + command + "\r\n")
	}
	scriptPath := filepath.Join(dir, "galley-verification"+ext)
	if err := os.WriteFile(scriptPath, body, 0o600); err != nil {
		cleanup()
		return nil, nil, "", fmt.Errorf("write windows verification script %s: %w", scriptPath, err)
	}
	return shellScriptArgv(shell, scriptPath), cleanup, shell.Kind, nil
}

type resolvedShell struct {
	Kind string
	Bin  string
}

func resolveShellForOS(goos, configured, configuredPath string) (resolvedShell, error) {
	// required_checks.shell_path is the more specific executable selection.
	// When set, infer the shell invocation style from the executable basename
	// so the configured `required_checks.shell` cannot drive an incompatible
	// argv shape against the chosen executable. When the basename is not a
	// recognized shell, fall back to a concrete configured shell kind as
	// fallback metadata. Profile validation rejects the no-fallback case at
	// load time; the resolver guards it again so an unexpectedly-loaded
	// profile surfaces a clear error.
	if configuredPath != "" {
		kind := profile.InferRequiredCheckShellKind(configuredPath)
		if kind == "" {
			switch configured {
			case "sh", "bash", "cmd", "powershell", "pwsh":
				kind = configured
			default:
				return resolvedShell{}, fmt.Errorf("required_checks.shell_path basename is not a recognized shell executable; set an explicit required_checks.shell kind (sh, bash, cmd, powershell, or pwsh) as fallback metadata")
			}
		}
		return resolvedShell{Kind: kind, Bin: configuredPath}, nil
	}
	switch configured {
	case "", "auto":
		if goos == "windows" {
			if bash, ok := discoverWindowsBash(); ok {
				return resolvedShell{Kind: "bash", Bin: bash}, nil
			}
			return resolvedShell{Kind: "cmd", Bin: "cmd.exe"}, nil
		}
		return resolvedShell{Kind: "sh", Bin: "/bin/sh"}, nil
	case "sh", "bash", "cmd", "powershell", "pwsh":
		shell := shellForKind(configured)
		// AC4: When Windows required checks ask for Bash through
		// `required_checks.shell: bash` and no `shell_path` is set, prefer
		// standard Git for Windows Bash discovery over PATH-discovered WSL
		// launchers, WindowsApps shims, or other non-standard Bash entries.
		// When no standard Git for Windows Bash is discoverable, the resolver
		// must NOT fall back to a bare `bash` executable: bare `bash` lets
		// PATH lookup at exec time silently pick up the very entries that
		// discoverWindowsBash just rejected (WSL launcher, WindowsApps shim,
		// MSYS2, Cygwin, Scoop, Chocolatey-managed Bashes), defeating the
		// rejection. Return a clear resolver error that names
		// `required_checks.shell_path` as the explicit override path instead,
		// so the operator opts in to a specific non-standard Bash rather than
		// letting Galley silently launch one.
		if goos == "windows" && configured == "bash" {
			bash, ok := discoverWindowsBash()
			if !ok {
				return resolvedShell{}, fmt.Errorf(
					"required_checks.shell is \"bash\" on Windows but no standard Git for Windows Bash install was discoverable; " +
						"PATH-discovered bash entries (WSL launcher at C:\\Windows\\System32\\bash.exe, WindowsApps shims, MSYS2, Cygwin, Scoop, or Chocolatey-managed Bashes) are not auto-selected to avoid silently switching the required-check shell; " +
						"set required_checks.shell_path to the exact bash executable path you want Galley to launch")
			}
			shell.Bin = bash
		}
		return shell, nil
	default:
		return resolvedShell{}, fmt.Errorf("unsupported required check shell %q", configured)
	}
}

// standardWindowsGitBashPaths lists the canonical Git for Windows install
// layouts that Galley should prefer for required-check shell auto-discovery.
// These are written with Windows backslashes to match what statFile receives
// from the OS path resolver on real Windows hosts.
var standardWindowsGitBashPaths = []string{
	`C:\Program Files\Git\bin\bash.exe`,
	`C:\Program Files\Git\usr\bin\bash.exe`,
	`C:\Program Files (x86)\Git\bin\bash.exe`,
	`C:\Program Files (x86)\Git\usr\bin\bash.exe`,
}

func discoverWindowsBash() (string, bool) {
	// 1) Prefer canonical Git for Windows install locations regardless of
	//    PATH ordering, so a WSL launcher or WindowsApps shim that happens
	//    to be earlier on PATH does not shadow a real Git Bash install.
	for _, candidate := range standardWindowsGitBashPaths {
		if _, err := statFile(candidate); err == nil {
			return candidate, true
		}
	}
	// 2) Infer Git Bash from a discoverable git.exe (cmd/git.exe -> bin/bash.exe).
	//    This covers portable Git layouts that match the cmd/git -> bin/bash
	//    convention without sitting under "Program Files".
	for _, name := range []string{"git.exe", "git"} {
		gitPath, err := lookPath(name)
		if err != nil {
			continue
		}
		if bashPath := gitBashFromGitPath(gitPath); bashPath != "" {
			if _, err := statFile(bashPath); err == nil {
				return bashPath, true
			}
		}
	}
	// 3) As a last resort, accept a PATH-discovered bash.exe only when it
	//    resolves to one of the canonical Git for Windows install paths.
	//    This preserves Git for Windows installs that statFile in step 1
	//    cannot observe (PATH-only or test environments) while refusing
	//    arbitrary PATH entries — including the WSL launcher, WindowsApps
	//    shims, MSYS2, Cygwin, Scoop, and Chocolatey-managed Bashes. Those
	//    non-standard Bashes must be opted into via required_checks.shell
	//    plus required_checks.shell_path, never auto-selected from PATH.
	for _, name := range []string{"bash.exe", "bash"} {
		path, err := lookPath(name)
		if err != nil {
			continue
		}
		if !isStandardGitForWindowsBashPath(path) {
			continue
		}
		return path, true
	}
	return "", false
}

// isStandardGitForWindowsBashPath returns true when the supplied path
// matches one of the canonical Git for Windows install locations in
// standardWindowsGitBashPaths. Matching is case-insensitive and tolerates
// either backslash or forward-slash separators so PATH-discovered entries
// using the alternate separator form still resolve. Any non-matching path
// — including the WSL launcher, WindowsApps shim, MSYS2, Cygwin, Scoop,
// or Chocolatey-managed Bashes — is rejected and the auto resolver falls
// back to cmd.exe so required checks do not silently switch shells.
func isStandardGitForWindowsBashPath(path string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(path, `\`, `/`))
	for _, std := range standardWindowsGitBashPaths {
		if normalized == strings.ToLower(strings.ReplaceAll(std, `\`, `/`)) {
			return true
		}
	}
	return false
}

func gitBashFromGitPath(gitPath string) string {
	normalized := strings.ReplaceAll(gitPath, `\`, `/`)
	lower := strings.ToLower(normalized)
	for _, suffix := range []string{"/cmd/git.exe", "/cmd/git"} {
		if strings.HasSuffix(lower, suffix) {
			return normalized[:len(normalized)-len(suffix)] + "/bin/bash.exe"
		}
	}
	return ""
}

func shellForKind(kind string) resolvedShell {
	switch kind {
	case "bash":
		return resolvedShell{Kind: kind, Bin: "bash"}
	case "cmd":
		return resolvedShell{Kind: kind, Bin: "cmd.exe"}
	case "powershell":
		return resolvedShell{Kind: kind, Bin: "powershell.exe"}
	case "pwsh":
		return resolvedShell{Kind: kind, Bin: "pwsh"}
	default:
		return resolvedShell{Kind: "sh", Bin: "/bin/sh"}
	}
}

func shellArgv(shell resolvedShell, command string) []string {
	switch shell.Kind {
	case "bash":
		return []string{shell.Bin, "-c", command}
	case "cmd":
		return []string{shell.Bin, "/C", command}
	case "powershell":
		return []string{shell.Bin, "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", command}
	case "pwsh":
		return []string{shell.Bin, "-NoProfile", "-Command", command}
	default:
		return []string{shell.Bin, "-c", command}
	}
}

func shellScriptArgv(shell resolvedShell, scriptPath string) []string {
	switch shell.Kind {
	case "bash":
		return []string{shell.Bin, scriptPath}
	case "sh":
		return []string{shell.Bin, scriptPath}
	case "powershell":
		return []string{shell.Bin, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", scriptPath}
	case "pwsh":
		return []string{shell.Bin, "-NoProfile", "-File", scriptPath}
	default:
		return []string{shell.Bin, "/C", scriptPath}
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
		reason := fmt.Sprintf("required check shell=%s bin=%s failed: %s", r.shell, r.shellBin, r.err)
		if guidance := r.shellGuidance(); guidance != "" {
			reason += "; " + guidance
		}
		return reason
	}
	return fmt.Sprintf("verification command passed with shell=%s bin=%s", r.shell, r.shellBin)
}

func (r verificationRun) shellGuidance() string {
	if runtime.GOOS != "windows" || r.shell != "cmd" {
		return ""
	}
	output := strings.ToLower(strings.Join([]string{r.stdout, r.stderr, r.command}, "\n"))
	if strings.Contains(output, "is not recognized as an internal or external command") || looksPOSIXCommand(r.command) {
		return "this Windows required check ran under cmd.exe; if it needs POSIX tools such as grep/find/test/xargs or POSIX shell syntax, install Git for Windows or set environment.required_checks.shell to bash"
	}
	return ""
}

func looksPOSIXCommand(command string) bool {
	tokens := []string{"grep ", "find ", "xargs", "test ", "$(", " | ", " <<", "2>/dev/null", "/bin/"}
	for _, token := range tokens {
		if strings.Contains(command, token) {
			return true
		}
	}
	return false
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
