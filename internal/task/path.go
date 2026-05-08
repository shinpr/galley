package task

import "path/filepath"

func siblingTaskPath(path, state string) string {
	dir := filepath.Dir(path)
	if filepath.Base(dir) == state {
		return path
	}
	parent := filepath.Dir(dir)
	if filepath.Base(parent) == "tasks" {
		return filepath.Join(parent, state, filepath.Base(path))
	}
	return path
}
