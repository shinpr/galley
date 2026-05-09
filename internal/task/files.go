package task

import (
	"path/filepath"
)

// ResolveFileSources makes relative input file sources stable before the task
// YAML is moved between queue directories.
func ResolveFileSources(taskPath string, t *Task) {
	base := filepath.Dir(taskPath)
	for i := range t.Files {
		if t.Files[i].Source == "" || filepath.IsAbs(t.Files[i].Source) {
			continue
		}
		t.Files[i].Source = filepath.Clean(filepath.Join(base, t.Files[i].Source))
	}
}
