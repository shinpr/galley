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
	if !overwrite {
		// Cross-platform exclusive create: opening with O_CREATE|O_EXCL fails
		// when the destination already exists and creates it atomically when
		// it does not. The previous implementation wrote a temp file and then
		// `os.Link`-ed it to the destination, which used CreateHardLink on
		// Windows and surfaced as a raw "not supported by windows" error on
		// filesystems that do not implement hardlinks (FAT32, ReFS, cross-
		// volume temp dirs). Writing directly to the destination removes that
		// platform-specific assumption while preserving no-overwrite
		// semantics on every supported OS. The write is bounded (queued task
		// YAML is small) and the destination directory is fsynced below so a
		// crash mid-write leaves a partial file that the daemon recovery path
		// already treats as invalid YAML.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		if err != nil {
			if os.IsExist(err) {
				return fmt.Errorf("destination already exists: %s", path)
			}
			return fmt.Errorf("create file %s: %w", path, err)
		}
		writeErr := writeAndCloseFile(f, path, data)
		if writeErr != nil {
			_ = os.Remove(path)
			return writeErr
		}
		fileutil.SyncDir(dir)
		return nil
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
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp file %s to %s: %w", tmpPath, path, err)
	}
	cleanup = false
	fileutil.SyncDir(dir)
	return nil
}

func writeAndCloseFile(f *os.File, path string, data []byte) error {
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write file %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync file %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close file %s: %w", path, err)
	}
	return nil
}
