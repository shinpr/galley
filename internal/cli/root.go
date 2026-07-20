package cli

import (
	"github.com/shinpr/galley/internal/daemoncmd"
	"github.com/shinpr/galley/internal/updatecheck"
	"github.com/shinpr/galley/internal/version"
	"github.com/spf13/cobra"
)

func Execute() error {
	updatecheck.Run(updatecheck.Options{})
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
		Version:       version.String(),
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	cmd.SetVersionTemplate("{{.Version}}\n")

	cmd.AddCommand(newTaskCommand())
	cmd.AddCommand(newClaudeCommand())
	cmd.AddCommand(newProfileCommand())
	cmd.AddCommand(newSchemaCommand())
	cmd.AddCommand(newVersionCommand())
	cmd.AddCommand(daemoncmd.NewCommand("daemon"))

	return cmd
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:           "version",
		Short:         "Print Galley version information",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.Println(version.String())
			return nil
		},
	}
}
