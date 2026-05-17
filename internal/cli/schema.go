package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/task"
	"github.com/spf13/cobra"
)

func newSchemaCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schema",
		Short: "Generate Galley schema files from the Go task contract",
	}
	cmd.AddCommand(newSchemaGenerateCommand())
	cmd.AddCommand(newSchemaCheckCommand())
	return cmd
}

func newSchemaGenerateCommand() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate plugin schema references",
		RunE: func(cmd *cobra.Command, args []string) error {
			targets, err := schemaTargets(output)
			if err != nil {
				return err
			}
			for _, target := range targets {
				data, err := target.Generate()
				if err != nil {
					return err
				}
				if err := os.MkdirAll(filepath.Dir(target.Path), 0o755); err != nil {
					return fmt.Errorf("create schema directory: %w", err)
				}
				if err := os.WriteFile(target.Path, data, 0o644); err != nil {
					return fmt.Errorf("write schema: %w", err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "generated: %s\n", target.Path)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&output, "output", "", "Output directory; defaults to the Galley plugin references directory")
	return cmd
}

func newSchemaCheckCommand() *cobra.Command {
	var path string
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check that plugin schema references match the Go contracts",
		RunE: func(cmd *cobra.Command, args []string) error {
			targets, err := schemaTargets(path)
			if err != nil {
				return err
			}
			for _, target := range targets {
				generated, err := target.Generate()
				if err != nil {
					return err
				}
				current, err := os.ReadFile(target.Path)
				if err != nil {
					return fmt.Errorf("read schema: %w", err)
				}
				equal, err := schemasEqual(current, generated)
				if err != nil {
					return err
				}
				if !equal {
					return fmt.Errorf("%s is out of date; run `galley schema generate`", target.Path)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "schema up to date: %s\n", target.Path)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "Schema directory to check; defaults to the Galley plugin references directory")
	return cmd
}

func schemasEqual(current, generated []byte) (bool, error) {
	if bytes.Equal(current, generated) {
		return true, nil
	}
	var currentJSON any
	if err := json.Unmarshal(current, &currentJSON); err != nil {
		return false, fmt.Errorf("parse current schema: %w", err)
	}
	var generatedJSON any
	if err := json.Unmarshal(generated, &generatedJSON); err != nil {
		return false, fmt.Errorf("parse generated schema: %w", err)
	}
	return reflect.DeepEqual(currentJSON, generatedJSON), nil
}

type schemaTarget struct {
	Path     string
	Generate func() ([]byte, error)
}

func schemaTargets(dir string) ([]schemaTarget, error) {
	if dir == "" {
		root, err := findGalleyRepoRoot()
		if err != nil {
			return nil, err
		}
		return []schemaTarget{
			{Path: filepath.Join(root, task.TaskSchemaPath), Generate: task.TaskJSONSchema},
			{Path: filepath.Join(root, profile.QualitySchemaPath), Generate: profile.QualityJSONSchema},
			{Path: filepath.Join(root, profile.EnvironmentSchemaPath), Generate: profile.EnvironmentJSONSchema},
		}, nil
	}
	return []schemaTarget{
		{Path: filepath.Join(dir, filepath.Base(task.TaskSchemaPath)), Generate: task.TaskJSONSchema},
		{Path: filepath.Join(dir, filepath.Base(profile.QualitySchemaPath)), Generate: profile.QualityJSONSchema},
		{Path: filepath.Join(dir, filepath.Base(profile.EnvironmentSchemaPath)), Generate: profile.EnvironmentJSONSchema},
	}, nil
}

func findGalleyRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		goMod := filepath.Join(dir, "go.mod")
		data, err := os.ReadFile(goMod)
		if err == nil && strings.Contains(string(data), "module github.com/shinpr/galley") {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("galley repository root not found")
		}
		dir = parent
	}
}
