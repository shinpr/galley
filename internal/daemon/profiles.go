package daemon

import (
	"github.com/shinpr/galley/internal/fileutil"
	"github.com/shinpr/galley/internal/galleyhome"
	"github.com/shinpr/galley/internal/profile"
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
