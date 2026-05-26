package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shinpr/galley/internal/daemonctl"
	"github.com/shinpr/galley/internal/galleyhome"
	skeletonpreflight "github.com/shinpr/galley/internal/preflight/skeleton"
	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/runlog"
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
			return renderOutput(cmd, output, result, func() error {
				archivedLabel := result.Task.ID
				if archivedLabel == "" {
					archivedLabel = result.To
				}
				fmt.Fprintf(cmd.OutOrStdout(), "archived: %s\n", archivedLabel)
				if result.From != result.To {
					fmt.Fprintf(cmd.OutOrStdout(), "moved: %s -> %s\n", result.From, result.To)
				}
				// Surface lenient-fallback context so operators see that the
				// archived file kept its pre-current-schema bytes. The JSON
				// output already exposes Mode and Warning through
				// ArchiveResult, so the text branch is the only place that
				// must echo them explicitly. Modes "lenient_status_edit" and
				// "move_unreadable_unchanged" both carry a non-empty Warning;
				// the current-schema path leaves both empty.
				if result.Mode != "" && result.Mode != "current_schema" {
					fmt.Fprintf(cmd.OutOrStdout(), "mode: %s\n", result.Mode)
				}
				if result.Warning != "" {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", result.Warning)
				}
				return nil
			})
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
	// DecodeError is set on entries that could not be decoded under the
	// current task schema. Tolerant scans (`galley task list`, `task show`'s
	// ID lookup, daemon helper sweeps) surface these strict-decode-incompatible or unreadable
	// task files as non-fatal entries instead of failing the whole command.
	// Active task intake (validate/queue/requeue/daemon execution) still
	// rejects unknown fields through the strict loader.
	DecodeError string `json:"decode_error,omitempty"`
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
			return renderOutput(cmd, output, items, func() error {
				for _, item := range items {
					if item.DecodeError != "" {
						// Strict-decode-incompatible or unreadable task files surface as
						// non-fatal entries so the rest of the list still
						// renders. The "decode_error" sentinel in the
						// status column lets shell pipelines filter these
						// entries explicitly. The file path is the third
						// column (matching the readable layout) and the
						// decode error message follows.
						fmt.Fprintf(cmd.OutOrStdout(), "%s\tdecode_error\t%s\t%s\n", item.State, item.File, item.DecodeError)
						continue
					}
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
			})
		},
	}
	cmd.Flags().StringVar(&root, "root", galleyhome.DefaultRoot(), "Galley daemon root directory; defaults to the running daemon root when available")
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
			path, err := resolveTaskPathOrID(root, args[0])
			if err != nil {
				return err
			}
			loaded, err := task.Load(path)
			if err != nil {
				return err
			}
			item := taskSummary(path, loaded)
			preflight := preflightSummary(loaded)
			if preflight != nil {
				applyRuntimePreflight(preflight, root, loaded.ID)
			}
			payload := struct {
				Summary   taskListItem          `json:"summary"`
				Task      task.Task             `json:"task"`
				Preflight *preflightSummaryView `json:"preflight,omitempty"`
			}{Summary: item, Task: loaded, Preflight: preflight}
			return renderOutput(cmd, output, payload, func() error {
				fmt.Fprintf(cmd.OutOrStdout(), "id: %s\n", loaded.ID)
				fmt.Fprintf(cmd.OutOrStdout(), "status: %s\n", loaded.Status)
				fmt.Fprintf(cmd.OutOrStdout(), "state: %s\n", item.State)
				fmt.Fprintf(cmd.OutOrStdout(), "file: %s\n", item.File)
				if loaded.PR.URL != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "pr: %s (%s)\n", loaded.PR.URL, loaded.PR.Status)
				}
				if len(loaded.Attempts) > 0 {
					last := loaded.Attempts[len(loaded.Attempts)-1]
					// Once the supervisor has accepted the task, the last attempt's
					// raw claude status and error fields are auditable history rather
					// than the active runtime state. Relabel them under a
					// prior_attempt_* prefix so accepted/pr_opened tasks no longer
					// surface "failed" framing as if it were the current state.
					prefix := "latest"
					if isAcceptedTerminalStatus(loaded.Status) {
						prefix = "prior_attempt"
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%s_attempt: %d\n", prefix, last.Number)
					fmt.Fprintf(cmd.OutOrStdout(), "%s_claude_status: %s\n", prefix, last.ClaudeStatus)
					fmt.Fprintf(cmd.OutOrStdout(), "%s_supervisor_verdict: %s\n", prefix, last.SupervisorVerdict)
					fmt.Fprintf(cmd.OutOrStdout(), "%s_summary: %s\n", prefix, last.Summary)
					if last.Error != nil {
						fmt.Fprintf(cmd.OutOrStdout(), "%s_error_phase: %s\n", prefix, last.Error.Phase)
						fmt.Fprintf(cmd.OutOrStdout(), "%s_error_kind: %s\n", prefix, last.Error.Kind)
						fmt.Fprintf(cmd.OutOrStdout(), "%s_error_message: %s\n", prefix, last.Error.Message)
						if last.Error.ArtifactDir != "" {
							fmt.Fprintf(cmd.OutOrStdout(), "%s_error_artifact_dir: %s\n", prefix, last.Error.ArtifactDir)
						}
					}
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
				if preflight != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "preflight_enabled: %t\n", preflight.Enabled)
					fmt.Fprintf(cmd.OutOrStdout(), "preflight_required: %t\n", preflight.Required)
					fmt.Fprintf(cmd.OutOrStdout(), "preflight_declared_outputs: %d\n", preflight.DeclaredOutputs)
					if preflight.RuntimeStatus != "" {
						fmt.Fprintf(cmd.OutOrStdout(), "preflight_runtime_status: %s\n", preflight.RuntimeStatus)
					}
					for _, o := range preflight.RuntimeOutputs {
						fmt.Fprintf(cmd.OutOrStdout(), "preflight_output: ac=%s path=%s kind=%s implementation_required=%t\n", o.ACID, o.Path, o.Kind, o.ImplementationRequired)
					}
					if preflight.FailureSummary != "" {
						fmt.Fprintf(cmd.OutOrStdout(), "preflight_failure: %s\n", preflight.FailureSummary)
					}
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&root, "root", galleyhome.DefaultRoot(), "Galley daemon root directory; defaults to the running daemon root when available")
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
			// Tolerant scan: strict-decode-incompatible task files that
			// pre-date the current schema still render as best-effort
			// entries. Truly unreadable files surface as decode-error
			// entries instead of failing the whole command. Active task
			// intake paths (validate/queue/requeue/daemon execution) still
			// use the strict Load.
			loaded, err := task.LoadLenient(path)
			if err != nil {
				items = append(items, taskListItem{
					State:       currentState,
					File:        path,
					DecodeError: err.Error(),
				})
				continue
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
		// Skip decode-error entries: tolerant scans surface unreadable
		// strict-decode-incompatible task files but they cannot drive an ID lookup
		// because their ID could not be decoded.
		if item.DecodeError != "" {
			continue
		}
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

func resolveTaskPathOrID(root, arg string) (string, error) {
	if strings.Contains(arg, string(os.PathSeparator)) {
		return arg, nil
	}
	resolved, err := findTaskByID(root, arg)
	if err == nil {
		return resolved, nil
	}
	if _, statErr := os.Stat(arg); statErr != nil {
		return "", fmt.Errorf("resolve %q as task ID under %s failed: %w; as file path failed: %v", arg, root, err, statErr)
	}
	return arg, nil
}

func resolveTaskRoot(root string, explicit bool) (string, error) {
	if explicit {
		return root, nil
	}
	defaultRoot := galleyhome.DefaultRoot()
	paths := daemonctl.ResolvePaths(defaultRoot, "", "")
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	status, err := daemonctl.Inspect(paths.PIDFile, "", exe)
	if errors.Is(err, daemonctl.ErrNotRunning) {
		return root, nil
	}
	if err != nil {
		return "", err
	}
	if !status.Alive {
		return root, nil
	}
	if !status.Verified {
		return "", fmt.Errorf("%w: pid=%d; pass --root to target a root explicitly", daemonctl.ErrUnverifiedProcess, status.Meta.PID)
	}
	if status.Meta.Root != "" {
		return status.Meta.Root, nil
	}
	return root, nil
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

// isAcceptedTerminalStatus reports whether the task has reached a supervisor
// accepted terminal state. Callers use it to suppress "active failure" framing
// for the last attempt's error fields when the supervisor already accepted
// the work. The set covers the full accepted lifecycle: the initial accepted
// status, the pr_opened status set when the daemon opens a PR, and the
// closed/merged statuses that the daemon's PR cleanup loop applies after the
// PR is closed or merged. Without those tail states a previously accepted
// task would regress to "active failure" framing once cleanup ran, even
// though the supervisor already approved the work.
func isAcceptedTerminalStatus(status string) bool {
	return task.IsAcceptedTerminal(status)
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
	var root string
	var moveSource bool

	cmd := &cobra.Command{
		Use:   "queue TASK.yaml",
		Short: "Validate and move a task YAML into the queued state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedRoot, err := resolveTaskRoot(root, cmd.Flags().Changed("root"))
			if err != nil {
				return err
			}
			result, err := task.Queue(args[0], task.QueueOptions{Reason: reason, Root: resolvedRoot, MoveSource: moveSource})
			if err != nil {
				return err
			}
			return renderOutput(cmd, output, result, func() error {
				fmt.Fprintf(cmd.OutOrStdout(), "queued: %s\n", result.Task.ID)
				if result.From != result.To {
					fmt.Fprintf(cmd.OutOrStdout(), "moved: %s -> %s\n", result.From, result.To)
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "text", "Output format: text or json")
	cmd.Flags().StringVar(&reason, "reason", "", "Reason to record in the task YAML")
	cmd.Flags().StringVar(&root, "root", galleyhome.DefaultRoot(), "Galley daemon root directory; defaults to the running daemon root when available")
	cmd.Flags().BoolVar(&moveSource, "move", false, "Remove the source task file after copying it into the daemon root")
	return cmd
}

func newTaskRequeueCommand() *cobra.Command {
	var output string
	var reason string
	var root string

	cmd := &cobra.Command{
		Use:   "requeue TASK.yaml|TASK_ID",
		Short: "Move a reviewed task back to queued for another daemon run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedRoot, err := resolveTaskRoot(root, cmd.Flags().Changed("root"))
			if err != nil {
				return err
			}
			path, err := resolveTaskPathOrID(resolvedRoot, args[0])
			if err != nil {
				return err
			}
			result, err := task.Requeue(path, task.RequeueOptions{Reason: reason, Root: resolvedRoot})
			if err != nil {
				return err
			}
			return renderOutput(cmd, output, result, func() error {
				fmt.Fprintf(cmd.OutOrStdout(), "requeued: %s\n", result.Task.ID)
				if result.From != result.To {
					fmt.Fprintf(cmd.OutOrStdout(), "moved: %s -> %s\n", result.From, result.To)
				}
				return nil
			})
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "text", "Output format: text or json")
	cmd.Flags().StringVar(&reason, "reason", "", "Reason to record in the task YAML")
	cmd.Flags().StringVar(&root, "root", galleyhome.DefaultRoot(), "Galley daemon root directory; defaults to the running daemon root when available")
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

			if err := renderOutput(cmd, output, result, func() error {
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
				return nil
			}); err != nil {
				return err
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

// preflightSummaryView is the JSON/text view shown by `galley task show` for
// the optional acceptance skeleton preflight stage. Runtime fields are loaded
// from runs/<run-id>/preflight_result.json when available.
type preflightSummaryView struct {
	Enabled         bool                  `json:"enabled"`
	Required        bool                  `json:"required"`
	DeclaredOutputs int                   `json:"declared_outputs"`
	RuntimeStatus   string                `json:"runtime_status,omitempty"`
	RuntimeOutputs  []preflightOutputView `json:"runtime_outputs,omitempty"`
	FailureSummary  string                `json:"failure_summary,omitempty"`
}

type preflightOutputView struct {
	ACID                   string `json:"ac_id"`
	Path                   string `json:"path"`
	Kind                   string `json:"kind"`
	ImplementationRequired bool   `json:"implementation_required"`
}

func preflightSummary(t task.Task) *preflightSummaryView {
	if t.Preflight == nil || t.Preflight.AcceptanceSkeleton == nil {
		return nil
	}
	cfg := t.Preflight.AcceptanceSkeleton
	view := &preflightSummaryView{
		Enabled:         cfg.IsEnabled(),
		Required:        cfg.IsRequired(),
		DeclaredOutputs: len(cfg.Outputs),
	}
	return view
}

// applyRuntimePreflight resolves the latest run directory for the task under
// <root>/runs and folds the runtime preflight_result.json into the view
// (AC-015). When no run directory exists the static declared fields are left
// unchanged.
func applyRuntimePreflight(view *preflightSummaryView, root, taskID string) {
	runDir, _ := runlog.LatestTaskRunDir(root, taskID)
	if runDir == "" {
		return
	}
	if pf, err := runartifact.Read[skeletonpreflight.Result](runDir, runartifact.PreflightResultFilename); err == nil && pf != nil {
		view.RuntimeStatus = pf.Status
		for _, o := range pf.Outputs {
			view.RuntimeOutputs = append(view.RuntimeOutputs, preflightOutputView{ACID: o.ACID, Path: o.Path, Kind: o.Kind, ImplementationRequired: o.ImplementationRequired})
		}
		if pf.Error != nil {
			if pf.Error.Phase != "" {
				view.FailureSummary = pf.Error.Phase + ": " + pf.Error.Message
			} else {
				view.FailureSummary = pf.Error.Message
			}
		}
	}
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
