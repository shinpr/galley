package task

import (
	"bytes"
	"fmt"
	"os"

	"github.com/shinpr/galley/internal/fileutil"
	"go.yaml.in/yaml/v3"
)

// Load reads a task YAML file and decodes known fields. Unknown YAML fields are
// ignored at runtime; schema/validator tooling owns the full authoring
// contract. Type mismatches for known fields still surface as decode errors.
func Load(path string) (Task, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Task{}, fmt.Errorf("read %s: %w", path, err)
	}

	var task Task
	decoder := yaml.NewDecoder(bytes.NewReader(data))
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
	if err := fileutil.WriteFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
