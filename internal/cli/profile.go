package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/shinpr/galley/internal/fileutil"
	"github.com/shinpr/galley/internal/galleyhome"
	"github.com/shinpr/galley/internal/profile"
	"github.com/spf13/cobra"
)

func newProfileCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Validate Galley quality and environment profiles",
	}
	cmd.AddCommand(newProfileValidateCommand())
	cmd.AddCommand(newProfileResolveCommand())
	return cmd
}

func newProfileResolveCommand() *cobra.Command {
	var root string
	var cwd string
	var output string
	var mkdir bool
	cmd := &cobra.Command{
		Use:   "resolve",
		Short: "Resolve conventional profile paths for a repository",
		RunE: func(cmd *cobra.Command, args []string) error {
			repoCWD := cwd
			if repoCWD == "" {
				current, err := os.Getwd()
				if err != nil {
					return err
				}
				repoCWD = current
			}
			repoCWD, err := filepath.Abs(repoCWD)
			if err != nil {
				return err
			}
			key, qualityPath, environmentPath, err := galleyhome.RepoProfilePaths(root, repoCWD)
			if err != nil {
				return err
			}
			if mkdir {
				if err := os.MkdirAll(filepath.Dir(qualityPath), 0o700); err != nil {
					return fmt.Errorf("create profile directory: %w", err)
				}
			}
			payload := struct {
				Root                   string `json:"root"`
				CWD                    string `json:"cwd"`
				RepoKey                string `json:"repo_key"`
				QualityProfileFile     string `json:"quality_profile_file"`
				EnvironmentProfileFile string `json:"environment_profile_file"`
				QualityExists          bool   `json:"quality_exists"`
				EnvironmentExists      bool   `json:"environment_exists"`
			}{
				Root:                   root,
				CWD:                    filepath.Clean(repoCWD),
				RepoKey:                key,
				QualityProfileFile:     qualityPath,
				EnvironmentProfileFile: environmentPath,
				QualityExists:          fileutil.ExistsFile(qualityPath),
				EnvironmentExists:      fileutil.ExistsFile(environmentPath),
			}
			return renderOutput(cmd, output, payload, func() error {
				fmt.Fprintf(cmd.OutOrStdout(), "root: %s\n", payload.Root)
				fmt.Fprintf(cmd.OutOrStdout(), "repo_key: %s\n", payload.RepoKey)
				fmt.Fprintf(cmd.OutOrStdout(), "quality_profile_file: %s\n", payload.QualityProfileFile)
				fmt.Fprintf(cmd.OutOrStdout(), "environment_profile_file: %s\n", payload.EnvironmentProfileFile)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&root, "root", galleyhome.DefaultRoot(), "Galley daemon root directory")
	cmd.Flags().StringVar(&cwd, "cwd", "", "Repository cwd; defaults to the current directory")
	cmd.Flags().BoolVar(&mkdir, "mkdir", false, "Create the resolved profile parent directory")
	cmd.Flags().StringVarP(&output, "output", "o", "text", "Output format: text or json")
	return cmd
}

func newProfileValidateCommand() *cobra.Command {
	var kind string
	var output string
	cmd := &cobra.Command{
		Use:   "validate PROFILE.yaml",
		Short: "Validate a quality or environment profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := validateProfile(kind, args[0])
			if err != nil {
				return err
			}
			if err := renderOutput(cmd, output, result, func() error {
				if result.Valid() {
					fmt.Fprintf(cmd.OutOrStdout(), "valid: %s %s\n", result.Kind, result.ID)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "invalid: %s\n", args[0])
				}
				for _, warning := range result.Warnings {
					fmt.Fprintf(cmd.OutOrStdout(), "warning: %s\n", warning)
				}
				for _, validationErr := range result.Errors {
					fmt.Fprintf(cmd.OutOrStdout(), "error: %s\n", validationErr)
				}
				return nil
			}); err != nil {
				return err
			}
			if !result.Valid() {
				return fmt.Errorf("profile validation failed")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "Profile kind: quality or environment; inferred from fields when omitted")
	cmd.Flags().StringVarP(&output, "output", "o", "text", "Output format: text or json")
	return cmd
}

func validateProfile(kind, path string) (profile.ValidationResult, error) {
	switch kind {
	case "quality":
		quality, err := profile.LoadQuality(path)
		if err != nil {
			return profile.ValidationResult{}, err
		}
		return profile.ValidateQuality(quality), nil
	case "environment":
		env, err := profile.LoadEnvironment(path)
		if err != nil {
			return profile.ValidationResult{}, err
		}
		return profile.ValidateEnvironment(env), nil
	case "":
		quality, qErr := profile.LoadQuality(path)
		if qErr == nil && (quality.PassPolicy.MinScore != 0 || len(quality.RequiredChecks) > 0 || len(quality.ReviewDimensions) > 0) {
			return profile.ValidateQuality(quality), nil
		}
		env, eErr := profile.LoadEnvironment(path)
		if eErr == nil && (env.CWD != "" || len(env.Commands) > 0) {
			return profile.ValidateEnvironment(env), nil
		}
		if qErr != nil {
			return profile.ValidationResult{}, qErr
		}
		return profile.ValidationResult{}, eErr
	default:
		return profile.ValidationResult{}, fmt.Errorf("unsupported profile kind %q", kind)
	}
}
