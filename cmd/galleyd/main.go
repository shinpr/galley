package main

import (
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/shinpr/galley/internal/daemon"
	"github.com/spf13/cobra"
)

func main() {
	if err := newCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newCommand() *cobra.Command {
	var opts daemon.Options
	var pollInterval time.Duration
	var supervisorCommand []string

	cmd := &cobra.Command{
		Use:           "galleyd",
		Short:         "Run the Galley file-backed task daemon",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			if pollInterval > 0 {
				opts.PollInterval = pollInterval
			}
			opts.SupervisorCommand = supervisorCommand
			opts.Explicit = daemon.ExplicitOptions{
				Root:                   cmd.Flags().Changed("root"),
				SystemPromptFile:       cmd.Flags().Changed("system-prompt-file"),
				JSONSchemaFile:         cmd.Flags().Changed("json-schema-file"),
				QualityProfileFile:     cmd.Flags().Changed("quality-profile-file"),
				EnvironmentProfileFile: cmd.Flags().Changed("environment-profile-file"),
				MaxConcurrentTasks:     cmd.Flags().Changed("max-concurrent-tasks"),
				PollInterval:           cmd.Flags().Changed("poll-interval"),
				ClaimTTL:               cmd.Flags().Changed("claim-ttl"),
				HeartbeatInterval:      cmd.Flags().Changed("heartbeat-interval"),
				CommitOnAccept:         cmd.Flags().Changed("commit-on-accept"),
				OpenPR:                 cmd.Flags().Changed("open-pr"),
				PollPRComments:         cmd.Flags().Changed("poll-pr-comments"),
				ReplyPRComments:        cmd.Flags().Changed("reply-pr-comments"),
				PRBase:                 cmd.Flags().Changed("pr-base"),
				SupervisorCommand:      cmd.Flags().Changed("supervisor-command"),
			}
			return daemon.Run(ctx, opts)
		},
	}

	cmd.Flags().StringVar(&opts.Root, "root", ".agent-workflow", "Agent workflow root directory")
	cmd.Flags().StringVar(&opts.ManifestFile, "manifest-file", "", "Optional Galley repo manifest YAML file")
	cmd.Flags().StringVar(&opts.SystemPromptFile, "system-prompt-file", "prompts/claude-executor-full.md", "Claude replacement system prompt file")
	cmd.Flags().StringVar(&opts.JSONSchemaFile, "json-schema-file", "schemas/claude-result.schema.json", "Claude JSON schema file")
	cmd.Flags().StringVar(&opts.QualityProfileFile, "quality-profile-file", "", "Optional Galley quality profile YAML file")
	cmd.Flags().StringVar(&opts.EnvironmentProfileFile, "environment-profile-file", "", "Optional Galley environment profile YAML file")
	cmd.Flags().BoolVar(&opts.Once, "once", false, "Process available queued tasks once and exit")
	cmd.Flags().IntVar(&opts.MaxConcurrentTasks, "max-concurrent-tasks", 1, "Maximum concurrent tasks")
	cmd.Flags().DurationVar(&pollInterval, "poll-interval", 10*time.Second, "Polling interval for non-once mode")
	cmd.Flags().DurationVar(&opts.ClaimTTL, "claim-ttl", 30*time.Minute, "Recover running task and claim locks older than this duration")
	cmd.Flags().DurationVar(&opts.HeartbeatInterval, "heartbeat-interval", 0, "Running task heartbeat interval; defaults to min(claim-ttl/4, 1m)")
	cmd.Flags().BoolVar(&opts.CommitOnAccept, "commit-on-accept", false, "Commit accepted worktree changes after executor completion")
	cmd.Flags().BoolVar(&opts.OpenPR, "open-pr", false, "Commit, push, and create a pull request for accepted worktree changes")
	cmd.Flags().BoolVar(&opts.PollPRComments, "poll-pr-comments", false, "Poll PR comments for /galley rerun commands and requeue matching tasks")
	cmd.Flags().BoolVar(&opts.ReplyPRComments, "reply-pr-comments", false, "Post PR comments after handling /galley commands")
	cmd.Flags().StringVar(&opts.PRBase, "pr-base", "", "Base branch for pull requests")
	cmd.Flags().StringArrayVar(&supervisorCommand, "supervisor-command", nil, "External supervisor command argv item; repeat for each argv token")

	return cmd
}
