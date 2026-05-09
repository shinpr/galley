package task

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shinpr/galley/internal/fileutil"
	"go.yaml.in/yaml/v3"
)

// Load reads a task YAML file and decodes it with strict field checking.
func Load(path string) (Task, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Task{}, fmt.Errorf("read %s: %w", path, err)
	}

	var task Task
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&task); err != nil {
		return Task{}, fmt.Errorf("decode %s: %w", path, err)
	}

	return task, nil
}

// LoadAndValidate loads a task and runs structural and environment validation.
func LoadAndValidate(path string) (ValidationResult, error) {
	loaded, err := Load(path)
	if err != nil {
		return ValidationResult{}, err
	}
	ApplyDefaults(&loaded)
	result := Validate(loaded)
	result.Task = loaded
	return result, nil
}

// Save writes a task YAML file.
func Save(path string, value Task) error {
	data, err := yaml.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := writeFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	return writeFileAtomicMode(path, data, perm, true)
}

func writeFileNoOverwriteAtomic(path string, data []byte, perm os.FileMode) error {
	return writeFileAtomicMode(path, data, perm, false)
}

func writeFileAtomicMode(path string, data []byte, perm os.FileMode, overwrite bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp file %s: %w", tmpPath, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp file %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file %s: %w", tmpPath, err)
	}
	if overwrite {
		if err := os.Rename(tmpPath, path); err != nil {
			return fmt.Errorf("rename temp file %s to %s: %w", tmpPath, path, err)
		}
	} else if err := os.Link(tmpPath, path); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("destination already exists: %s", path)
		}
		return fmt.Errorf("link temp file %s to %s: %w", tmpPath, path, err)
	} else if err := os.Remove(tmpPath); err != nil {
		return fmt.Errorf("remove temp file %s: %w", tmpPath, err)
	}
	cleanup = false
	fileutil.SyncDir(dir)
	return nil
}
