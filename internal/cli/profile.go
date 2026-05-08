package cli

import (
	"encoding/json"
	"fmt"

	"github.com/shinpr/galley/internal/profile"
	"github.com/spf13/cobra"
)

func newProfileCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Validate Galley quality and environment profiles",
	}
	cmd.AddCommand(newProfileValidateCommand())
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
			switch output {
			case "json":
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(result); err != nil {
					return err
				}
			case "text":
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
			default:
				return fmt.Errorf("unsupported output format %q", output)
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
