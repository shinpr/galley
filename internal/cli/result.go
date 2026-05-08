package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/shinpr/galley/internal/result"
	"github.com/spf13/cobra"
)

func newResultCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "result",
		Short: "Generate deterministic Galley executor result files",
	}
	cmd.AddCommand(newResultCompleteCommand())
	return cmd
}

func newResultCompleteCommand() *cobra.Command {
	var taskFile string
	var output string
	var workDir string
	var summary string

	cmd := &cobra.Command{
		Use:   "complete",
		Short: "Run task verification and write a validated executor result JSON file",
		RunE: func(cmd *cobra.Command, args []string) error {
			generated, err := result.Complete(context.Background(), result.CompleteOptions{
				TaskFile: taskFile,
				Output:   output,
				WorkDir:  workDir,
				Summary:  summary,
			})
			if err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			if err := enc.Encode(generated); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "wrote result: %s\n", output)
			return nil
		},
	}
	cmd.Flags().StringVar(&taskFile, "task-file", "", "Effective Galley task YAML file")
	cmd.Flags().StringVar(&output, "output", "", "Output Claude result JSON file")
	cmd.Flags().StringVar(&workDir, "workdir", "", "Execution workspace directory; defaults to task scope.cwd")
	cmd.Flags().StringVar(&summary, "summary", "", "Result summary")
	_ = cmd.MarkFlagRequired("task-file")
	_ = cmd.MarkFlagRequired("output")
	_ = cmd.MarkFlagRequired("summary")
	return cmd
}
