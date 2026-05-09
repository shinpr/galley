package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shinpr/galley/internal/task"
)

type preparedInputFile struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
	Commit      bool   `json:"commit"`
}

func prepareInputFiles(workDir string, files []task.InputFile) ([]preparedInputFile, error) {
	prepared := make([]preparedInputFile, 0, len(files))
	for i, file := range files {
		if file.Source == "" || file.Destination == "" {
			_ = cleanupPreparedInputFiles(prepared)
			return nil, fmt.Errorf("files[%d] source and destination are required", i)
		}
		dst, err := inputDestination(workDir, file.Destination)
		if err != nil {
			_ = cleanupPreparedInputFiles(prepared)
			return nil, fmt.Errorf("files[%d].destination: %w", i, err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			_ = cleanupPreparedInputFiles(prepared)
			return nil, fmt.Errorf("create input file dir %s: %w", filepath.Dir(dst), err)
		}
		if err := copyFileNoOverwrite(file.Source, dst); err != nil {
			_ = cleanupPreparedInputFiles(prepared)
			return nil, fmt.Errorf("copy input file %s to %s: %w", file.Source, dst, err)
		}
		prepared = append(prepared, preparedInputFile{
			Source:      file.Source,
			Destination: filepath.Clean(file.Destination),
			Path:        dst,
			Description: file.Description,
			Commit:      file.Commit,
		})
	}
	return prepared, nil
}

func cleanupPreparedInputFiles(files []preparedInputFile) error {
	var errs []error
	for _, file := range files {
		if err := os.Remove(file.Path); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("remove prepared input file %s: %w", file.Path, err))
		}
	}
	return errors.Join(errs...)
}

func cleanupNonCommittedInputFiles(workDir string, files []task.InputFile) error {
	for i, file := range files {
		if file.Commit {
			continue
		}
		dst, err := inputDestination(workDir, file.Destination)
		if err != nil {
			return fmt.Errorf("files[%d].destination: %w", i, err)
		}
		if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove non-committed input file %s: %w", dst, err)
		}
		if err := pruneEmptyParents(workDir, filepath.Dir(dst)); err != nil {
			return fmt.Errorf("prune input file parent dirs: %w", err)
		}
	}
	return nil
}

func inputDestination(workDir, destination string) (string, error) {
	if destination == "" {
		return "", fmt.Errorf("empty path")
	}
	if !filepath.IsLocal(destination) {
		return "", fmt.Errorf("must be a local relative path: %s", destination)
	}
	return filepath.Join(workDir, filepath.Clean(destination)), nil
}

func copyFileNoOverwrite(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("stat %s: %w", src, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("source must not be a symlink: %s", src)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("source must be a regular file: %s", src)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	file, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("destination already exists: %s", dst)
		}
		return fmt.Errorf("create %s: %w", dst, err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(dst)
		return fmt.Errorf("write %s: %w", dst, err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}

func pruneEmptyParents(root, dir string) error {
	root = filepath.Clean(root)
	for {
		dir = filepath.Clean(dir)
		if dir == root || dir == "." || dir == string(filepath.Separator) {
			return nil
		}
		rel, err := filepath.Rel(root, dir)
		if err != nil || rel == ".." || filepath.IsAbs(rel) || rel == "." || strings.HasPrefix(rel, "../") {
			return nil
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if len(entries) > 0 {
			return nil
		}
		if err := os.Remove(dir); err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		dir = filepath.Dir(dir)
	}
}
