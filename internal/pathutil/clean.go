package pathutil

import "path/filepath"

func CleanPhysical(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if evaluated, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(evaluated)
	}
	return filepath.Clean(abs)
}
