package pathutil

import (
	"path"
	"runtime"
	"strings"
)

// InsideAnyLogicalPath compares slash-authored paths using host filesystem case rules.
func InsideAnyLogicalPath(value string, prefixes []string) bool {
	return InsideAnyLogicalPathForOS(value, prefixes, runtime.GOOS)
}

// InsideAnyLogicalPathForOS exposes deterministic target-OS comparison for tests.
func InsideAnyLogicalPathForOS(value string, prefixes []string, goos string) bool {
	if value == "" {
		return false
	}
	value = cleanLogicalPath(value)
	caseInsensitive := goos == "darwin" || goos == "windows"
	if caseInsensitive {
		value = strings.ToLower(value)
	}
	for _, prefix := range prefixes {
		prefix = cleanLogicalPath(prefix)
		if caseInsensitive {
			prefix = strings.ToLower(prefix)
		}
		if prefix == "." || value == prefix || strings.HasPrefix(value, prefix+"/") {
			return true
		}
	}
	return false
}

func cleanLogicalPath(value string) string {
	return path.Clean(strings.ReplaceAll(value, `\`, "/"))
}
