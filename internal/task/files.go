package task

import (
	"path/filepath"
	"sort"
)

// YAMLFiles enumerates both supported task extensions in deterministic order.
func YAMLFiles(dir string) ([]string, error) {
	var matches []string
	for _, ext := range []string{"*.yaml", "*.yml"} {
		paths, err := filepath.Glob(filepath.Join(dir, ext))
		if err != nil {
			return nil, err
		}
		matches = append(matches, paths...)
	}
	sort.Strings(matches)
	return matches, nil
}

// ResolveFileSources makes relative input file sources stable before the task
// YAML is moved between queue directories.
func ResolveFileSources(taskPath string, t *Task) {
	base := filepath.Dir(taskPath)
	if absolute, err := filepath.Abs(base); err == nil {
		base = absolute
	}
	for i := range t.Files {
		if t.Files[i].Source == "" || filepath.IsAbs(t.Files[i].Source) {
			continue
		}
		t.Files[i].Source = filepath.Clean(filepath.Join(base, t.Files[i].Source))
	}
}
