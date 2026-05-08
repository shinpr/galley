package cli

import (
	"encoding/json"
	"fmt"

	"github.com/shinpr/galley/internal/task"
	"github.com/spf13/cobra"
)

func newTaskCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Manage Galley task YAML files",
	}

	cmd.AddCommand(newTaskValidateCommand())
	cmd.AddCommand(newTaskWorkOrderCommand())
	cmd.AddCommand(newTaskQueueCommand())
	cmd.AddCommand(newTaskRequeueCommand())
	cmd.AddCommand(newTaskArchiveCommand())

	return cmd
}

func newTaskArchiveCommand() *cobra.Command {
	var output string
	var reason string

	cmd := &cobra.Command{
		Use:   "archive TASK.yaml",
		Short: "Move a reviewed Galley task YAML into the archived state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := task.Archive(args[0], task.ArchiveOptions{Reason: reason})
			if err != nil {
				return err
			}
			switch output {
			case "json":
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			case "text":
				fmt.Fprintf(cmd.OutOrStdout(), "archived: %s\n", result.Task.ID)
				if result.From != result.To {
					fmt.Fprintf(cmd.OutOrStdout(), "moved: %s -> %s\n", result.From, result.To)
				}
				return nil
			default:
				return fmt.Errorf("unsupported output format %q", output)
			}
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "text", "Output format: text or json")
	cmd.Flags().StringVar(&reason, "reason", "", "Reason to record in the task YAML")
	return cmd
}

func newTaskQueueCommand() *cobra.Command {
	var output string
	var reason string

	cmd := &cobra.Command{
		Use:   "queue TASK.yaml",
		Short: "Validate and move a task YAML into the queued state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := task.Queue(args[0], task.QueueOptions{Reason: reason})
			if err != nil {
				return err
			}
			switch output {
			case "json":
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			case "text":
				fmt.Fprintf(cmd.OutOrStdout(), "queued: %s\n", result.Task.ID)
				if result.From != result.To {
					fmt.Fprintf(cmd.OutOrStdout(), "moved: %s -> %s\n", result.From, result.To)
				}
				return nil
			default:
				return fmt.Errorf("unsupported output format %q", output)
			}
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "text", "Output format: text or json")
	cmd.Flags().StringVar(&reason, "reason", "", "Reason to record in the task YAML")
	return cmd
}

func newTaskRequeueCommand() *cobra.Command {
	var output string
	var reason string

	cmd := &cobra.Command{
		Use:   "requeue TASK.yaml",
		Short: "Return a reviewed Galley task YAML to the queued state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := task.Requeue(args[0], task.RequeueOptions{Reason: reason})
			if err != nil {
				return err
			}
			switch output {
			case "json":
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			case "text":
				fmt.Fprintf(cmd.OutOrStdout(), "requeued: %s\n", result.Task.ID)
				if result.From != result.To {
					fmt.Fprintf(cmd.OutOrStdout(), "moved: %s -> %s\n", result.From, result.To)
				}
				return nil
			default:
				return fmt.Errorf("unsupported output format %q", output)
			}
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "text", "Output format: text or json")
	cmd.Flags().StringVar(&reason, "reason", "", "Reason to record in the task YAML")
	return cmd
}

func newTaskValidateCommand() *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "validate TASK.yaml",
		Short: "Validate a Galley task YAML file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := task.LoadAndValidate(args[0])
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
					fmt.Fprintf(cmd.OutOrStdout(), "valid: %s\n", result.Task.ID)
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
				return fmt.Errorf("task validation failed")
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "text", "Output format: text or json")
	return cmd
}

func newTaskWorkOrderCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "work-order TASK.yaml",
		Short: "Render the initial Claude executor work order for a task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := task.LoadAndValidate(args[0])
			if err != nil {
				return err
			}
			if !result.Valid() {
				return fmt.Errorf("task validation failed")
			}
			fmt.Fprint(cmd.OutOrStdout(), task.RenderWorkOrder(result.Task))
			return nil
		},
	}

	return cmd
}
