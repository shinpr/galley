package task

import "path/filepath"

func siblingTaskPath(path string, state WorkflowState) string {
	dir := filepath.Dir(path)
	if filepath.Base(dir) == string(state) {
		return path
	}
	parent := filepath.Dir(dir)
	if filepath.Base(parent) == "tasks" {
		return filepath.Join(parent, string(state), filepath.Base(path))
	}
	return path
}
