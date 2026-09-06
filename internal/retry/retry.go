// Package retry provides a small internal helper that absorbs transient
// git/gh failures at the six daemon call sites listed in the workspace task
// contract.
//
// Constants in this file (MaxAttempts, JitterRatio, and the baseDelays
// schedule) are intentionally hardcoded. The retry helper is not configurable
// from the CLI, environment, task YAML, or profile YAML; only the bounded
// total attempts and the exponential backoff schedule are guaranteed.
//
// The helper treats every error from fn uniformly. Auth-token failures,
// HTTP 4xx responses, and other non-transient errors still exhaust the bounded
// backoff schedule (~31s total) but do not need error-class detection.
// Callers that must remain one-shot (for example non-idempotent PR comment
// POSTs) simply do not call Do.
package retry

import (
	"context"
	"math/rand"
	"time"
)

// MaxAttempts is the total number of attempts Do makes (one initial attempt
// plus five retries).
const MaxAttempts = 6

// JitterRatio is the ± proportion applied to each baseDelays entry. A jitter
// ratio of 0.25 means every backoff delay is scaled by a random factor in
// [0.75, 1.25].
const JitterRatio = 0.25

// baseDelays is the exponential backoff schedule applied between attempts.
// The schedule has MaxAttempts-1 entries (1s, 2s, 4s, 8s, 16s) and is
// indexed by the attempt that just failed.
var baseDelays = [MaxAttempts - 1]time.Duration{
	1 * time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
}

// sleep blocks until d elapses or ctx is cancelled. It is package-private so
// retry_test.go can replace it with a no-op for deterministic, fast tests.
// Callers outside the package never see this hook.
var sleep = func(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// jitter returns a multiplier in [1-JitterRatio, 1+JitterRatio]. It is
// package-private for the same reason as sleep: retry_test.go pins it to 1.0
// so the schedule asserts against the documented base delays.
var jitter = func() float64 {
	// rand.Float64 returns [0.0, 1.0). (rand*2 - 1) maps to [-1, 1).
	//nolint:gosec // G404: jitter only spreads retry timing; unpredictability is not required
	return 1 + (rand.Float64()*2-1)*JitterRatio
}

// SetHooksForTest replaces the package-private sleep and jitter hooks so
// external-package tests (for example internal/daemon) can disable the real
// backoff. Both arguments must be non-nil; otherwise SetHooksForTest panics.
// The returned restore function reinstates the previous hooks and must be
// called by the caller (typically deferred from TestMain). The retry timing
// semantics themselves remain covered by the internal/retry tests.
func SetHooksForTest(
	sleepFn func(context.Context, time.Duration) error,
	jitterFn func() float64,
) func() {
	if sleepFn == nil || jitterFn == nil {
		panic("retry.SetHooksForTest: sleep and jitter hooks must be non-nil")
	}
	previousSleep := sleep
	previousJitter := jitter
	sleep = sleepFn
	jitter = jitterFn
	return func() {
		sleep = previousSleep
		jitter = previousJitter
	}
}

// Do runs fn up to MaxAttempts times. It returns nil as soon as fn succeeds.
// When fn returns an error, Do sleeps for the next backoff delay (with ±25%
// jitter) and retries. If ctx is cancelled at any point, Do returns ctx.Err()
// without invoking fn again. After all attempts fail Do returns the final
// error from fn unchanged so callers see the same error type and value they
// would see in the no-retry path.
func Do(ctx context.Context, fn func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		//nolint:wrapcheck // Do documents that it returns ctx.Err(); wrapping would change the documented return
		return err
	}
	var lastErr error
	for attempt := 0; attempt < MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			//nolint:wrapcheck // Do documents that it returns ctx.Err(); wrapping would change the documented return
			return err
		}
		err := fn(ctx)
		if err == nil {
			return nil
		}
		lastErr = err
		if attempt == MaxAttempts-1 {
			break
		}
		delay := time.Duration(float64(baseDelays[attempt]) * jitter())
		if sleepErr := sleep(ctx, delay); sleepErr != nil {
			return sleepErr
		}
	}
	return lastErr
}
