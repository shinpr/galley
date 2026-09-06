package daemon

import (
	"context"
	"fmt"
	"strings"

	"github.com/shinpr/galley/internal/fileutil"
	"github.com/shinpr/galley/internal/galleyhome"
	"github.com/shinpr/galley/internal/proc"
	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/retry"
)

type resolvedProfileFiles struct {
	RepoKey                string `json:"repo_key,omitempty"`
	QualityProfileFile     string `json:"quality_profile_file,omitempty"`
	EnvironmentProfileFile string `json:"environment_profile_file,omitempty"`
}

func resolveProfileFiles(opts Options, repoCWD string) (resolvedProfileFiles, error) {
	resolved := resolvedProfileFiles{
		QualityProfileFile:     opts.QualityProfileFile,
		EnvironmentProfileFile: opts.EnvironmentProfileFile,
	}
	key, qualityPath, environmentPath, err := galleyhome.RepoProfilePaths(opts.Root, repoCWD)
	if err != nil {
		return resolvedProfileFiles{}, err
	}
	resolved.RepoKey = key
	if resolved.QualityProfileFile == "" && fileutil.ExistsFile(qualityPath) {
		resolved.QualityProfileFile = qualityPath
	}
	if resolved.EnvironmentProfileFile == "" && fileutil.ExistsFile(environmentPath) {
		resolved.EnvironmentProfileFile = environmentPath
	}
	return resolved, nil
}

func loadTaskProfiles(opts Options, repoCWD string) (resolvedProfileFiles, profile.Bundle, error) {
	resolved, err := resolveProfileFiles(opts, repoCWD)
	if err != nil {
		return resolvedProfileFiles{}, profile.Bundle{}, err
	}
	bundle, err := profile.LoadBundle(resolved.QualityProfileFile, resolved.EnvironmentProfileFile)
	if err != nil {
		return resolvedProfileFiles{}, profile.Bundle{}, err
	}
	return resolved, bundle, nil
}

// resolveWorktreeStartPoint resolves the git ref name to pass to
// `git worktree add` as the start-point for a brand-new task branch. The
// resolution chain matches the daemon contract documented in the task design:
//
// 1. If the source repository has an origin remote, run
// `git fetch --no-tags --quiet origin <base>` to refresh
// refs/remotes/origin/<base>. A successful fetch means the remote-tracking
// ref now reflects the latest remote tip, so the daemon uses it as the
// start-point. A failed fetch is a hard error: the daemon refuses to use a
// possibly stale refs/remotes/origin/<base> and fails workspace
// preparation with a descriptive message that names the source repo path,
// pr.base, and the failed fetch operation. This matches the PR-review
// requirement that a stale remote-tracking ref must not silently anchor a
// new task branch behind the actual remote tip.
// 2. If the source repository has no origin remote (origin-less local
// checkouts and the smoke test), fall back to refs/heads/<base>. This
// keeps offline/local development paths working while preserving the
// refresh-or-fail guarantee whenever origin is configured.
// 3. If the resolved candidate ref does not exist (no origin and
// refs/heads/<base> missing, or origin successful fetch yet
// refs/remotes/origin/<base> still missing), the daemon fails the claimed
// task with a descriptive error naming both attempted refs and the source
// repository path.
//
// When base is empty (environment profile missing or pr.base set to empty
// string), this returns ("", nil) so the caller passes StartPoint="" to
// workspace.Prepare and preserves today's HEAD-derived behavior.
func resolveWorktreeStartPoint(ctx context.Context, opts Options, sourceCWD, base string) (string, error) {
	if base == "" {
		return "", nil
	}
	originRef := "refs/remotes/origin/" + base
	headsRef := "refs/heads/" + base
	hasOrigin, err := hasOriginRemote(ctx, opts, sourceCWD)
	if err != nil {
		return "", err
	}
	if hasOrigin {
		return resolveOriginStartPoint(ctx, opts, originStartPoint{SourceCWD: sourceCWD, Base: base, OriginRef: originRef, HeadsRef: headsRef})
	}
	// Origin-less local repository: use the local branch as the start-point.
	ok, err := refExists(ctx, opts, sourceCWD, headsRef)
	if err != nil {
		return "", err
	}
	if ok {
		return headsRef, nil
	}
	return "", fmt.Errorf(
		"resolve pr.base %q: neither %s nor %s exists in source repository %s",
		base, originRef, headsRef, sourceCWD,
	)
}

// hasOriginRemote distinguishes a missing remote from a failed repository probe.
func hasOriginRemote(ctx context.Context, opts Options, sourceCWD string) (bool, error) {
	result, err := proc.RunVCSCommand(ctx, proc.Command{
		WorkDir: "",
		Argv:    proc.GitArgs(opts.GitBin, "-C", sourceCWD, "remote", "get-url", "origin"),
	}, proc.RunOptions{TailBytes: -1})
	if err == nil {
		return true, nil
	}
	if result.ExitCode == 2 && strings.Contains(result.Stderr, "No such remote") {
		return false, nil
	}
	return false, fmt.Errorf("inspect origin in %s: %w", sourceCWD, err)
}

// fetchOriginRef runs `git fetch --no-tags --quiet origin <base>` against the
// source repository to refresh refs/remotes/origin/<base> before it is used as
// a worktree start-point. Fetch errors are returned to the caller; they are
// not swallowed. resolveWorktreeStartPoint propagates the error to fail
// workspace preparation so the daemon refuses to anchor a new task branch on a
// possibly stale remote-tracking ref.
func fetchOriginRef(ctx context.Context, opts Options, sourceCWD, base string) error {
	// Retry transient git fetch failures (transport hiccup, brief auth/DNS
	// flake). The retry helper preserves the original error type so the
	// fmt.Errorf wrap below surfaces the same value to callers.
	err := retry.Do(ctx, func(ctx context.Context) error {
		_, runErr := proc.RunVCSCommand(ctx, proc.Command{
			WorkDir: "",
			Argv:    proc.GitArgs(opts.GitBin, "-C", sourceCWD, "fetch", "--no-tags", "--quiet", "origin", base),
		}, proc.RunOptions{TailBytes: -1})
		return runErr
	})
	if err != nil {
		return fmt.Errorf("git fetch origin %s: %w", base, err)
	}
	return nil
}

func refExists(ctx context.Context, opts Options, sourceCWD, ref string) (bool, error) {
	result, err := proc.RunVCSCommand(ctx, proc.Command{
		WorkDir: "",
		Argv:    proc.GitArgs(opts.GitBin, "-C", sourceCWD, "show-ref", "--verify", "--quiet", ref),
	}, proc.RunOptions{TailBytes: -1})
	if err != nil {
		if result.ExitCode == 1 {
			return false, nil
		}
		return false, fmt.Errorf("git show-ref %s: %w", ref, err)
	}
	return true, nil
}

type effectiveTaskOptions struct {
	OpenPR           bool
	CommitOnAccept   bool
	PRBase           string
	PollPRComments   bool
	ReplyPRComments  bool
	CleanupWorktrees bool
	Supervisor       string
	SupervisorSource string
	SupervisorModel  string
	SupervisorEffort string
}

func resolveEffectiveTaskOptions(opts Options, profiles profile.Bundle) effectiveTaskOptions {
	effective := effectiveTaskOptions{
		OpenPR:           opts.OpenPR,
		CommitOnAccept:   opts.CommitOnAccept,
		PRBase:           opts.PRBase,
		PollPRComments:   opts.PollPRComments,
		ReplyPRComments:  opts.ReplyPRComments,
		CleanupWorktrees: true,
		Supervisor:       opts.Supervisor,
		SupervisorSource: opts.SupervisorSource,
		SupervisorModel:  opts.SupervisorModel,
		SupervisorEffort: opts.SupervisorEffort,
	}
	effective.applyEnvironment(profiles.Environment)
	if effective.OpenPR {
		effective.CommitOnAccept = true
	}
	return effective
}

// applyEnvironment overrides the daemon-wide defaults with the repository's
// environment profile. A nil profile leaves the defaults untouched.
func (effective *effectiveTaskOptions) applyEnvironment(env *profile.Environment) {
	if env == nil {
		return
	}
	effective.OpenPR = env.PR.Enabled
	effective.PRBase = env.PR.Base
	effective.PollPRComments = env.PR.Comments.Enabled
	effective.ReplyPRComments = env.PR.Comments.Reply
	if env.Worktree.Cleanup != nil {
		effective.CleanupWorktrees = *env.Worktree.Cleanup
	}
	effective.applySupervisor(env.Supervisor)
}

func (effective *effectiveTaskOptions) applySupervisor(sup *profile.SupervisorDefault) {
	if sup == nil {
		return
	}
	if sup.DefaultCLI != "" {
		effective.Supervisor = sup.DefaultCLI
		effective.SupervisorSource = SupervisorSourceRepoProfile
	}
	if sup.Model != "" {
		effective.SupervisorModel = sup.Model
	}
	if sup.Effort != "" {
		effective.SupervisorEffort = sup.Effort
	}
}

func (effective effectiveTaskOptions) apply(opts Options) Options {
	opts.OpenPR = effective.OpenPR
	opts.CommitOnAccept = effective.CommitOnAccept
	opts.PRBase = effective.PRBase
	opts.PollPRComments = effective.PollPRComments
	opts.ReplyPRComments = effective.ReplyPRComments
	opts.CleanupWorktrees = effective.CleanupWorktrees
	opts.Supervisor = effective.Supervisor
	opts.SupervisorSource = effective.SupervisorSource
	opts.SupervisorModel = effective.SupervisorModel
	opts.SupervisorEffort = effective.SupervisorEffort
	return opts
}

func effectiveOptionsForProfiles(opts Options, profiles profile.Bundle) Options {
	return resolveEffectiveTaskOptions(opts, profiles).apply(opts)
}

// originStartPoint names the refs a pr.base start-point resolves through.
type originStartPoint struct {
	SourceCWD string
	Base      string
	OriginRef string
	HeadsRef  string
}

// resolveOriginStartPoint refuses a possibly stale refs/remotes/origin/<base>;
// a fetch failure surfaces in `galley task show` as latest_error_*.
func resolveOriginStartPoint(ctx context.Context, opts Options, sp originStartPoint) (string, error) {
	if err := fetchOriginRef(ctx, opts, sp.SourceCWD, sp.Base); err != nil {
		return "", fmt.Errorf(
			"refresh %s in source repository %s for pr.base %q failed: %w; "+
				"Galley refused to use the stale remote-tracking ref as the worktree start-point",
			sp.OriginRef, sp.SourceCWD, sp.Base, err,
		)
	}
	ok, err := refExists(ctx, opts, sp.SourceCWD, sp.OriginRef)
	if err != nil {
		return "", err
	}
	if ok {
		return sp.OriginRef, nil
	}
	return "", fmt.Errorf(
		"resolve pr.base %q: %s missing in source repository %s after successful fetch (attempted refs: %s, %s)",
		sp.Base, sp.OriginRef, sp.SourceCWD, sp.OriginRef, sp.HeadsRef,
	)
}
