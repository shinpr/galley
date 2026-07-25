package pathutil

import (
	"path"
	"strings"
)

// InsideAnyLogicalPath compares slash-authored paths case-sensitively.
func InsideAnyLogicalPath(value string, prefixes []string) bool {
	return insideAnyPath(value, prefixes, false)
}

// InsideAnyProtectedPath compares protected paths case-insensitively.
func InsideAnyProtectedPath(value string, prefixes []string) bool {
	return insideAnyPath(value, prefixes, true)
}

func insideAnyPath(value string, prefixes []string, foldCase bool) bool {
	if value == "" {
		return false
	}
	value = cleanLogicalPath(value)
	if foldCase {
		value = strings.ToLower(value)
	}
	for _, prefix := range prefixes {
		prefix = cleanLogicalPath(prefix)
		if foldCase {
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
