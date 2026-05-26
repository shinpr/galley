package galleyhome

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/shinpr/galley/internal/pathutil"
)

var repoKeySanitizer = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// DefaultRoot returns the default Galley daemon root.
func DefaultRoot() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".galley"
	}
	return filepath.Join(home, ".galley")
}

// RepoKey returns a stable key for a repository path.
func RepoKey(cwd string) (string, error) {
	absolute, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve repo cwd: %w", err)
	}
	absolute = pathutil.CleanPhysical(absolute)
	base := repoKeySanitizer.ReplaceAllString(filepath.Base(absolute), "-")
	base = strings.Trim(base, "-._")
	if base == "" {
		base = "repo"
	}
	sum := sha256.Sum256([]byte(absolute))
	return fmt.Sprintf("%s-%s", base, hex.EncodeToString(sum[:])[:8]), nil
}

// RepoProfilePaths returns the conventional profile paths for a repository.
func RepoProfilePaths(root, cwd string) (key, qualityPath, environmentPath string, err error) {
	key, err = RepoKey(cwd)
	if err != nil {
		return "", "", "", err
	}
	dir := filepath.Join(root, "profiles", key)
	return key, filepath.Join(dir, "quality.yaml"), filepath.Join(dir, "environment.yaml"), nil
}
