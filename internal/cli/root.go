package cli

import "github.com/spf13/cobra"

func Execute() error {
	root := NewRootCommand()
	if err := root.Execute(); err != nil {
		return err
	}
	return nil
}

func NewRootCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "galley",
		Short:         "Galley orchestrates supervised local agent task execution",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	cmd.AddCommand(newTaskCommand())
	cmd.AddCommand(newClaudeCommand())
	cmd.AddCommand(newProfileCommand())
	cmd.AddCommand(newResultCommand())

	return cmd
}
