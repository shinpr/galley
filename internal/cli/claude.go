package cli

import (
	"encoding/json"
	"fmt"
	"time"

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
	cmd.AddCommand(newClaudeRunCommand())
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

	cmd.Flags().StringVar(&promptPath, "system-prompt-file", "prompts/claude-executor-full.md", "Claude replacement system prompt file")
	cmd.Flags().StringVar(&schemaPath, "json-schema-file", "schemas/claude-result.schema.json", "Claude JSON schema file")
	cmd.Flags().StringVar(&settingsPath, "settings-file", "", "Optional Claude settings file")
	cmd.Flags().StringVar(&qualityPath, "quality-profile-file", "", "Optional Galley quality profile YAML file")
	cmd.Flags().StringVar(&environmentPath, "environment-profile-file", "", "Optional Galley environment profile YAML file")
	cmd.Flags().BoolVar(&includeHookEvents, "include-hook-events", false, "Include Claude hook lifecycle events in stream-json output")
	cmd.Flags().StringVarP(&output, "output", "o", "shell", "Output format: shell or json")

	return cmd
}

func newClaudeRunCommand() *cobra.Command {
	var promptPath string
	var schemaPath string
	var settingsPath string
	var qualityPath string
	var environmentPath string
	var includeHookEvents bool
	var timeoutMS int
	var stdoutPath string
	var stderrPath string

	cmd := &cobra.Command{
		Use:   "run TASK.yaml",
		Short: "Run Claude Code for a task",
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

			commandPlan, err := runner.ClaudeCommandPlan(opts)
			if err != nil {
				return err
			}
			for _, warning := range commandPlan.Warnings {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", warning)
			}

			if timeoutMS == 0 {
				timeoutMS = result.Task.ExecutionPolicy.TimeoutMS
			}
			runResult, err := runner.RunCommand(cmd.Context(), commandPlan, runner.RunOptions{
				Timeout:    time.Duration(timeoutMS) * time.Millisecond,
				StdoutPath: stdoutPath,
				StderrPath: stderrPath,
			})

			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			if encodeErr := enc.Encode(runResult); encodeErr != nil {
				return encodeErr
			}
			return err
		},
	}

	cmd.Flags().StringVar(&promptPath, "system-prompt-file", "prompts/claude-executor-full.md", "Claude replacement system prompt file")
	cmd.Flags().StringVar(&schemaPath, "json-schema-file", "schemas/claude-result.schema.json", "Claude JSON schema file")
	cmd.Flags().StringVar(&settingsPath, "settings-file", "", "Optional Claude settings file")
	cmd.Flags().StringVar(&qualityPath, "quality-profile-file", "", "Optional Galley quality profile YAML file")
	cmd.Flags().StringVar(&environmentPath, "environment-profile-file", "", "Optional Galley environment profile YAML file")
	cmd.Flags().BoolVar(&includeHookEvents, "include-hook-events", false, "Include Claude hook lifecycle events in stream-json output")
	cmd.Flags().IntVar(&timeoutMS, "timeout-ms", 0, "Execution timeout in milliseconds; defaults to task execution_policy.timeout_ms")
	cmd.Flags().StringVar(&stdoutPath, "stdout-file", "", "Optional path to write raw stdout")
	cmd.Flags().StringVar(&stderrPath, "stderr-file", "", "Optional path to write raw stderr")

	return cmd
}
