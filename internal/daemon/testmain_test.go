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

// TestMain disables retry backoff once for the whole package so that daemon
// tests which deliberately fail a retry-wrapped git/gh call (for example the
// stale-origin/git-fetch failure path) do not pay the real exponential
// schedule. Retry timing and cancel semantics are covered by the
// internal/retry tests; daemon tests only need the retry loop to advance
// instantly.
//
// The invocation-scoped setup runner is also stubbed to a ready result so the
// majority of daemon tests, which do not exercise the setup executor preflight,
// do not need to spawn a real Claude/Codex subprocess. Tests that exercise the
// setup preflight contract override the test runner explicitly via
// withSetupExecutorRunner.
func TestMain(m *testing.M) {
	restoreRetry := retry.SetHooksForTest(
		func(_ context.Context, _ time.Duration) error { return nil },
		func() float64 { return 1.0 },
	)
	code := m.Run()
	restoreRetry()
	os.Exit(code)
}

// defaultTestSetupExecutorRunner returns a successful no-op setup result so
// daemon tests that do not exercise the setup executor preflight do not have
// to wire a real subprocess. The result carries an explicit readiness evidence
// string so an accidental dependency would surface clearly.
//
// The stub returns one SuccessfulCommand (`true`) so the learned-plan contract
// enforced by setuppreflight.EnforceLearnedPlanContract passes: a real setup executor
// that returns status=ready with no successful_commands fails the setup
// phase, and using an empty stub here would either mask that contract or
// inject false-positive successes into daemon tests that exercise the setup
// preflight indirectly through Run.
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

// withSetupExecutorRunner installs a setup executor runner for the duration of
// a test. The previous runner is restored when the cleanup fires.
func withSetupExecutorRunner(t interface{ Cleanup(func()) }, runner func(context.Context, setuppreflight.Options) (*setuppreflight.Result, error)) {
	prev := testSetupExecutorRunner
	testSetupExecutorRunner = runner
	t.Cleanup(func() { testSetupExecutorRunner = prev })
}
