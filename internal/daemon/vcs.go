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

func workspaceOptions(opts Options) workspace.Options {
	return workspace.Options{GitBin: opts.GitBin}
}
