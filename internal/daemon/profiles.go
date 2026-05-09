package daemon

import (
	"os"

	"github.com/shinpr/galley/internal/galleyhome"
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
	if resolved.QualityProfileFile == "" && fileExists(qualityPath) {
		resolved.QualityProfileFile = qualityPath
	}
	if resolved.EnvironmentProfileFile == "" && fileExists(environmentPath) {
		resolved.EnvironmentProfileFile = environmentPath
	}
	return resolved, nil
}

func fileExists(path string) bool {
	stat, err := os.Stat(path)
	return err == nil && !stat.IsDir()
}
