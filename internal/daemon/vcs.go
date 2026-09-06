package daemon

import (
	"github.com/shinpr/galley/internal/vcs"
	"github.com/shinpr/galley/internal/workspace"
)

func vcsBinaries(opts Options) vcs.Binaries {
	return vcs.Binaries{
		Git: opts.GitBin,
		GH:  opts.GHBin,
	}
}

// vcsRepo names the worktree a git command runs in and where its evidence lands.
func vcsRepo(opts Options, workDir, runDir string) vcs.Repo {
	return vcs.Repo{Bins: vcsBinaries(opts), WorkDir: workDir, RunDir: runDir}
}

func workspaceOptions(opts Options) workspace.Options {
	return workspace.Options{GitBin: opts.GitBin}
}
