package daemon

import (
	"context"
	"fmt"

	"github.com/shinpr/galley/internal/fileutil"
	"github.com/shinpr/galley/internal/galleyhome"
	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/runner"
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
//  1. If the source repository has an origin remote, attempt a best-effort
//     `git fetch origin <base>` so that refs/remotes/origin/<base> is
//     refreshed before resolution. A stale local origin/<base> would
//     otherwise anchor the new task branch behind the actual remote tip.
//     Fetch failures (network, missing ref on remote, etc.) are non-fatal
//     and fall through to the existing resolution chain.
//  2. refs/remotes/origin/<base> (matches `gh pr create --base <base>` intent
//     for AFK runs that ultimately push to origin),
//  3. refs/heads/<base> (local fallback so origin-less local repos and the
//     smoke test keep working),
//  4. if base is non-empty and neither ref exists, the daemon must fail the
//     claimed task with a descriptive error.
//
// When base is empty (environment profile missing or pr.base set to empty
// string), this returns ("", nil) so the caller passes StartPoint="" to
// workspace.Prepare and preserves today's HEAD-derived behavior.
func resolveWorktreeStartPoint(ctx context.Context, opts Options, sourceCWD, base string) (string, error) {
	if base == "" {
		return "", nil
	}
	// Best-effort: refresh origin/<base> when an origin remote exists so a
	// stale remote-tracking ref does not silently anchor the new task branch
	// behind the latest remote tip. Errors are ignored; the resolution chain
	// below still picks the best available ref (or surfaces a descriptive
	// failure when none exist).
	if hasOriginRemote(ctx, opts, sourceCWD) {
		_ = fetchOriginRef(ctx, opts, sourceCWD, base)
	}
	candidates := []string{"refs/remotes/origin/" + base, "refs/heads/" + base}
	for _, ref := range candidates {
		ok, err := refExists(ctx, opts, sourceCWD, ref)
		if err != nil {
			return "", err
		}
		if ok {
			return ref, nil
		}
	}
	return "", fmt.Errorf("resolve pr.base %q: neither %s nor %s exists in source repository %s", base, candidates[0], candidates[1], sourceCWD)
}

// hasOriginRemote reports whether the source repository has an "origin"
// remote configured. Detection failures are reported as "no origin" so that
// origin-less local repositories (smoke test, fresh clones without a remote)
// keep using the refs/heads/<base> fallback path.
func hasOriginRemote(ctx context.Context, opts Options, sourceCWD string) bool {
	gitBin := opts.GitBin
	if gitBin == "" {
		gitBin = "git"
	}
	_, err := runner.RunCommand(ctx, runner.Command{
		WorkDir: "",
		Argv:    []string{gitBin, "-C", sourceCWD, "remote", "get-url", "origin"},
	}, runner.RunOptions{TailBytes: -1})
	return err == nil
}

// fetchOriginRef performs a best-effort `git fetch origin <base>` against the
// source repository. Errors are returned for the caller to log/ignore; the
// daemon swallows the error and lets the existing resolution chain pick the
// best available ref.
func fetchOriginRef(ctx context.Context, opts Options, sourceCWD, base string) error {
	gitBin := opts.GitBin
	if gitBin == "" {
		gitBin = "git"
	}
	_, err := runner.RunCommand(ctx, runner.Command{
		WorkDir: "",
		Argv:    []string{gitBin, "-C", sourceCWD, "fetch", "--no-tags", "--quiet", "origin", base},
	}, runner.RunOptions{TailBytes: -1})
	if err != nil {
		return fmt.Errorf("git fetch origin %s: %w", base, err)
	}
	return nil
}

func refExists(ctx context.Context, opts Options, sourceCWD, ref string) (bool, error) {
	gitBin := opts.GitBin
	if gitBin == "" {
		gitBin = "git"
	}
	result, err := runner.RunCommand(ctx, runner.Command{
		WorkDir: "",
		Argv:    []string{gitBin, "-C", sourceCWD, "show-ref", "--verify", "--quiet", ref},
	}, runner.RunOptions{TailBytes: -1})
	if err != nil {
		if result.ExitCode == 1 {
			return false, nil
		}
		return false, fmt.Errorf("git show-ref %s: %w", ref, err)
	}
	return true, nil
}

func effectiveOptionsForProfiles(opts Options, profiles profile.Bundle) Options {
	effective := opts
	effective.CleanupWorktrees = true
	if profiles.Environment != nil {
		env := profiles.Environment
		effective.OpenPR = env.PR.Enabled
		effective.PRBase = env.PR.Base
		effective.PollPRComments = env.PR.Comments.Enabled
		effective.ReplyPRComments = env.PR.Comments.Reply
		if env.Worktree.Cleanup != nil {
			effective.CleanupWorktrees = *env.Worktree.Cleanup
		}
	}
	if effective.OpenPR {
		effective.CommitOnAccept = true
	}
	return effective
}
