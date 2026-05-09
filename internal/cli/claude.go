package cli

import (
	"encoding/json"
	"fmt"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/task"
	"github.com/spf13/cobra"
)

func newClaudeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "claude",
		Short: "Prepare Claude Code executor invocations",
	}

	cmd.AddCommand(newClaudeArgsCommand())
	return cmd
}

func newClaudeArgsCommand() *cobra.Command {
	var promptPath string
	var schemaPath string
	var settingsPath string
	var qualityPath string
	var environmentPath string
	var output string
	var includeHookEvents bool

	cmd := &cobra.Command{
		Use:   "args TASK.yaml",
		Short: "Print the Claude Code argv for a task without running it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := task.LoadAndValidate(args[0])
			if err != nil {
				return err
			}
			if !result.Valid() {
				return fmt.Errorf("task validation failed")
			}

			opts := runner.FromTask(result.Task)
			opts.SystemPromptFile = promptPath
			opts.JSONSchemaFile = schemaPath
			opts.SettingsFile = settingsPath
			opts.IncludeHookEvents = includeHookEvents
			profiles, err := profile.LoadBundle(qualityPath, environmentPath)
			if err != nil {
				return err
			}
			opts.Prompt = task.RenderWorkOrderWithProfiles(result.Task, profiles)

			switch output {
			case "json":
				commandPlan, err := runner.ClaudeCommandPlan(opts)
				if err != nil {
					return err
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(commandPlan)
			case "shell":
				preview, warnings, err := runner.ClaudeShellPreview(opts)
				if err != nil {
					return err
				}
				for _, warning := range warnings {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", warning)
				}
				fmt.Fprintln(cmd.OutOrStdout(), preview)
				return nil
			default:
				return fmt.Errorf("unsupported output format %q", output)
			}
		},
	}

	cmd.Flags().StringVar(&promptPath, "system-prompt-file", "", "Claude replacement system prompt file; defaults to the embedded Galley executor prompt")
	cmd.Flags().StringVar(&schemaPath, "json-schema-file", "", "Claude JSON schema file; defaults to the embedded Galley result schema")
	cmd.Flags().StringVar(&settingsPath, "settings-file", "", "Optional Claude settings file")
	cmd.Flags().StringVar(&qualityPath, "quality-profile-file", "", "Optional Galley quality profile YAML file")
	cmd.Flags().StringVar(&environmentPath, "environment-profile-file", "", "Optional Galley environment profile YAML file")
	cmd.Flags().BoolVar(&includeHookEvents, "include-hook-events", false, "Include Claude hook lifecycle events in stream-json output")
	cmd.Flags().StringVarP(&output, "output", "o", "shell", "Output format: shell or json")

	return cmd
}
