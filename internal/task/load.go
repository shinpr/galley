package task

import (
	"bytes"
	"fmt"
	"os"

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
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
