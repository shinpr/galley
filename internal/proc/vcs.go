package proc

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// RunVCSCommand bounds Galley-owned Git/gh operations without limiting LLM commands.
func RunVCSCommand(ctx context.Context, command Command, opts RunOptions) (RunResult, error) {
	return runVCSCommand(ctx, command, opts, 2*time.Minute)
}

func runVCSCommand(ctx context.Context, command Command, opts RunOptions, timeout time.Duration) (RunResult, error) {
	if opts.Timeout <= 0 || opts.Timeout > timeout {
		opts.Timeout = timeout
	}
	result, err := RunCommand(ctx, command, opts)
	if err != nil {
		return result, fmt.Errorf("%s in %s: %w: %s", strings.Join(command.Argv, " "), command.WorkDir, err, strings.TrimSpace(result.Stderr))
	}
	return result, nil
}
