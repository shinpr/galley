package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/shinpr/galley/internal/inputfiles"
	"github.com/shinpr/galley/internal/runartifact"
)

func priorPreparedInputs(root, taskID, runDir, cwd string) ([]inputfiles.Prepared, error) {
	dirs, err := priorTaskRunDirs(root, taskID, runDir)
	if err != nil {
		return nil, err
	}
	for _, dir := range dirs {
		data, err := os.ReadFile(runartifact.Path(dir, runartifact.InputFilesFilename))
		if err != nil {
			continue
		}
		var prior []inputfiles.Prepared
		if json.Unmarshal(data, &prior) != nil {
			continue
		}
		var matched []inputfiles.Prepared
		for _, file := range prior {
			if filepath.Clean(file.Path) == filepath.Join(cwd, file.Destination) {
				matched = append(matched, file)
			}
		}
		if len(matched) > 0 {
			return matched, nil
		}
	}
	return nil, nil
}
