package daemon

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/shinpr/galley/internal/retry"
)

// TestMain disables retry backoff once for the whole package so that daemon
// tests which deliberately fail a retry-wrapped git/gh call (for example the
// stale-origin/git-fetch failure path) do not pay the real exponential
// schedule. Retry timing and cancel semantics are covered by the
// internal/retry tests; daemon tests only need the retry loop to advance
// instantly.
func TestMain(m *testing.M) {
	restore := retry.SetHooksForTest(
		func(_ context.Context, _ time.Duration) error { return nil },
		func() float64 { return 1.0 },
	)
	code := m.Run()
	restore()
	os.Exit(code)
}
