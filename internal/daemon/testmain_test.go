package daemon

import (
	"context"
	"os"
	"testing"
	"time"

	setuppreflight "github.com/shinpr/galley/internal/preflight/setup"
	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/retry"
)

func TestMain(m *testing.M) {
	restoreRetry := retry.SetHooksForTest(
		func(_ context.Context, _ time.Duration) error { return nil },
		func() float64 { return 1.0 },
	)
	code := m.Run()
	restoreRetry()
	os.Exit(code)
}

// SuccessfulCommands keeps the stub subject to the learned-plan contract.
func defaultTestSetupExecutorRunner(_ context.Context, _ setuppreflight.Options) (*setuppreflight.Result, error) {
	return &setuppreflight.Result{
		Status:             setuppreflight.StatusReady,
		Commands:           []setuppreflight.CommandAttempt{{Run: "true", Source: setuppreflight.SourceDiscovered, ExitCode: 0}},
		SuccessfulCommands: []profile.SetupCommand{{Run: "true", Why: "test stub no-op"}},
		ReadinessEvidence:  "test stub: setup executor preflight skipped",
		Source:             setuppreflight.SourceDiscovered,
	}, nil
}

var testSetupExecutorRunner = defaultTestSetupExecutorRunner

func testDaemonOptions(opts Options) Options {
	deps := opts.daemonDependencies()
	deps.setupExecutorRunner = testSetupExecutorRunner
	opts.dependencies = &deps
	return opts
}

func runTestDaemon(ctx context.Context, opts Options) error {
	return Run(ctx, testDaemonOptions(opts))
}

func processAvailableForTest(ctx context.Context, opts Options) (int, error) {
	return processAvailable(ctx, testDaemonOptions(opts))
}

func withSetupExecutorRunner(t interface{ Cleanup(func()) }, runner func(context.Context, setuppreflight.Options) (*setuppreflight.Result, error)) {
	prev := testSetupExecutorRunner
	testSetupExecutorRunner = runner
	t.Cleanup(func() { testSetupExecutorRunner = prev })
}
