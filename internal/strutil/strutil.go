// Package strutil contains small string helpers shared across packages.
package strutil

import "strings"

// FirstNonEmpty returns the first string with non-whitespace content.
func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
