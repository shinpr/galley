package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shinpr/galley/internal/galleyhome"
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
	cmd.AddCommand(newTaskListCommand())
	cmd.AddCommand(newTaskShowCommand())
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

type taskListItem struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	State         string `json:"state"`
	File          string `json:"file"`
	PRURL         string `json:"pr_url,omitempty"`
	LatestVerdict string `json:"latest_verdict,omitempty"`
	LatestSummary string `json:"latest_summary,omitempty"`
}

func newTaskListCommand() *cobra.Command {
	var root string
	var state string
	var output string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List tasks under a Galley workflow root",
		RunE: func(cmd *cobra.Command, args []string) error {
			items, err := listTaskItems(root, state)
			if err != nil {
				return err
			}
			switch output {
			case "json":
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(items)
			case "text":
				for _, item := range items {
					fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s", item.State, item.Status, item.ID)
					if item.PRURL != "" {
						fmt.Fprintf(cmd.OutOrStdout(), "\t%s", item.PRURL)
					}
					if item.LatestVerdict != "" {
						fmt.Fprintf(cmd.OutOrStdout(), "\t%s", item.LatestVerdict)
					}
					if item.LatestSummary != "" {
						fmt.Fprintf(cmd.OutOrStdout(), "\t%s", item.LatestSummary)
					}
					fmt.Fprintln(cmd.OutOrStdout())
				}
				return nil
			default:
				return fmt.Errorf("unsupported output format %q", output)
			}
		},
	}
	cmd.Flags().StringVar(&root, "root", galleyhome.DefaultRoot(), "Galley daemon root directory")
	cmd.Flags().StringVar(&state, "state", "", "Filter by task directory/state")
	cmd.Flags().StringVarP(&output, "output", "o", "text", "Output format: text or json")
	return cmd
}

func newTaskShowCommand() *cobra.Command {
	var root string
	var output string

	cmd := &cobra.Command{
		Use:   "show TASK.yaml|TASK_ID",
		Short: "Show a task summary and latest failure/review context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			if !strings.Contains(path, string(os.PathSeparator)) {
				resolved, err := findTaskByID(root, path)
				if err != nil {
					if _, statErr := os.Stat(path); statErr != nil {
						return err
					}
				} else {
					path = resolved
				}
			}
			loaded, err := task.Load(path)
			if err != nil {
				return err
			}
			item := taskSummary(path, loaded)
			switch output {
			case "json":
				payload := struct {
					Summary taskListItem `json:"summary"`
					Task    task.Task    `json:"task"`
				}{Summary: item, Task: loaded}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(payload)
			case "text":
				fmt.Fprintf(cmd.OutOrStdout(), "id: %s\n", loaded.ID)
				fmt.Fprintf(cmd.OutOrStdout(), "status: %s\n", loaded.Status)
				fmt.Fprintf(cmd.OutOrStdout(), "state: %s\n", item.State)
				fmt.Fprintf(cmd.OutOrStdout(), "file: %s\n", item.File)
				if loaded.PR.URL != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "pr: %s (%s)\n", loaded.PR.URL, loaded.PR.Status)
				}
				if len(loaded.Attempts) > 0 {
					last := loaded.Attempts[len(loaded.Attempts)-1]
					fmt.Fprintf(cmd.OutOrStdout(), "latest_attempt: %d\n", last.Number)
					fmt.Fprintf(cmd.OutOrStdout(), "latest_claude_status: %s\n", last.ClaudeStatus)
					fmt.Fprintf(cmd.OutOrStdout(), "latest_supervisor_verdict: %s\n", last.SupervisorVerdict)
					fmt.Fprintf(cmd.OutOrStdout(), "latest_summary: %s\n", last.Summary)
				}
				if len(loaded.Risks) > 0 {
					last := loaded.Risks[len(loaded.Risks)-1]
					fmt.Fprintf(cmd.OutOrStdout(), "latest_risk: %s %s: %s\n", last.ID, last.Type, last.Detail)
				}
				for _, command := range loaded.Verification.Commands {
					if command.Status == "failed" {
						fmt.Fprintf(cmd.OutOrStdout(), "failed_verification: %s\n", command.Cmd)
						if command.OutputExcerpt != "" {
							fmt.Fprintf(cmd.OutOrStdout(), "failed_output: %s\n", command.OutputExcerpt)
						}
					}
				}
				return nil
			default:
				return fmt.Errorf("unsupported output format %q", output)
			}
		},
	}
	cmd.Flags().StringVar(&root, "root", galleyhome.DefaultRoot(), "Galley daemon root directory")
	cmd.Flags().StringVarP(&output, "output", "o", "text", "Output format: text or json")
	return cmd
}

func listTaskItems(root, state string) ([]taskListItem, error) {
	states := []string{"draft", "queued", "running", "done", "failed", "archived"}
	if state != "" {
		states = []string{state}
	}

	var items []taskListItem
	for _, currentState := range states {
		paths, err := taskFiles(filepath.Join(root, "tasks", currentState))
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			loaded, err := task.Load(path)
			if err != nil {
				return nil, err
			}
			items = append(items, taskSummary(path, loaded))
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].State != items[j].State {
			return items[i].State < items[j].State
		}
		if items[i].ID != items[j].ID {
			return items[i].ID < items[j].ID
		}
		return items[i].File < items[j].File
	})
	return items, nil
}

func findTaskByID(root, id string) (string, error) {
	items, err := listTaskItems(root, "")
	if err != nil {
		return "", err
	}
	var matches []string
	for _, item := range items {
		if item.ID == id {
			matches = append(matches, item.File)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("task %q not found under %s", id, filepath.Join(root, "tasks"))
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("task %q is ambiguous under %s: %s", id, filepath.Join(root, "tasks"), strings.Join(matches, ", "))
	}
}

func taskFiles(dir string) ([]string, error) {
	var paths []string
	for _, pattern := range []string{"*.yaml", "*.yml"} {
		matches, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			return nil, err
		}
		paths = append(paths, matches...)
	}
	sort.Strings(paths)
	return paths, nil
}

func taskSummary(path string, loaded task.Task) taskListItem {
	item := taskListItem{
		ID:     loaded.ID,
		Status: loaded.Status,
		State:  filepath.Base(filepath.Dir(path)),
		File:   path,
		PRURL:  loaded.PR.URL,
	}
	if len(loaded.Attempts) > 0 {
		last := loaded.Attempts[len(loaded.Attempts)-1]
		item.LatestVerdict = last.SupervisorVerdict
		item.LatestSummary = last.Summary
	}
	return item
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
		Short: "Move a reviewed task back to queued for another daemon run",
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
