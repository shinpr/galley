package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

// withTestHooks pins jitter to 1.0 and replaces sleep with a recorder so the
// schedule asserts against documented base delays without waiting.
func withTestHooks(t *testing.T) *[]time.Duration {
	t.Helper()
	originalSleep := sleep
	originalJitter := jitter
	var recorded []time.Duration
	sleep = func(_ context.Context, d time.Duration) error {
		recorded = append(recorded, d)
		return nil
	}
	jitter = func() float64 { return 1.0 }
	t.Cleanup(func() {
		sleep = originalSleep
		jitter = originalJitter
	})
	return &recorded
}

func TestDo_SuccessOnFirstTry(t *testing.T) {
	recorded := withTestHooks(t)
	calls := 0
	err := Do(context.Background(), func(_ context.Context) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("Do returned %v, want nil", err)
	}
	if calls != 1 {
		t.Fatalf("fn called %d times, want 1", calls)
	}
	if len(*recorded) != 0 {
		t.Fatalf("sleep called %d times on first-try success, want 0", len(*recorded))
	}
}

func TestDo_SuccessAfterTransientFailures(t *testing.T) {
	recorded := withTestHooks(t)
	transient := errors.New("transient")
	calls := 0
	err := Do(context.Background(), func(_ context.Context) error {
		calls++
		if calls < 3 {
			return transient
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Do returned %v, want nil", err)
	}
	if calls != 3 {
		t.Fatalf("fn called %d times, want 3", calls)
	}
	wantSleeps := []time.Duration{baseDelays[0], baseDelays[1]}
	if len(*recorded) != len(wantSleeps) {
		t.Fatalf("sleep called %d times, want %d", len(*recorded), len(wantSleeps))
	}
	for i, d := range wantSleeps {
		if (*recorded)[i] != d {
			t.Fatalf("sleep[%d] = %v, want %v", i, (*recorded)[i], d)
		}
	}
}

func TestDo_ExhaustionReturnsLastError(t *testing.T) {
	recorded := withTestHooks(t)
	var lastErr error
	calls := 0
	err := Do(context.Background(), func(_ context.Context) error {
		calls++
		lastErr = errors.New("attempt failure")
		return lastErr
	})
	if err == nil {
		t.Fatalf("Do returned nil, want last error")
	}
	if err != lastErr {
		t.Fatalf("Do returned %v, want %v (identical to fn's last return)", err, lastErr)
	}
	if calls != MaxAttempts {
		t.Fatalf("fn called %d times, want %d", calls, MaxAttempts)
	}
	if len(*recorded) != MaxAttempts-1 {
		t.Fatalf("sleep called %d times, want %d", len(*recorded), MaxAttempts-1)
	}
	wantSchedule := baseDelays[:]
	for i, d := range wantSchedule {
		if (*recorded)[i] != d {
			t.Fatalf("sleep[%d] = %v, want %v", i, (*recorded)[i], d)
		}
	}
}

func TestDo_ContextCancelledBeforeFirstAttempt(t *testing.T) {
	withTestHooks(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	err := Do(ctx, func(_ context.Context) error {
		calls++
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do returned %v, want context.Canceled", err)
	}
	if calls != 0 {
		t.Fatalf("fn called %d times, want 0 after cancelled ctx", calls)
	}
}

func TestDo_ContextCancelledDuringBackoff(t *testing.T) {
	originalSleep := sleep
	originalJitter := jitter
	t.Cleanup(func() {
		sleep = originalSleep
		jitter = originalJitter
	})
	jitter = func() float64 { return 1.0 }

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel the context the first time sleep is asked to wait, so the second
	// attempt is never made and Do surfaces ctx.Err() without further calls.
	sleep = func(c context.Context, _ time.Duration) error {
		cancel()
		return c.Err()
	}

	transient := errors.New("transient")
	calls := 0
	err := Do(ctx, func(_ context.Context) error {
		calls++
		return transient
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do returned %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Fatalf("fn called %d times, want 1 (cancelled before retry)", calls)
	}
}

func TestBaseDelaysSchedule(t *testing.T) {
	want := [MaxAttempts - 1]time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
	}
	if baseDelays != want {
		t.Fatalf("baseDelays = %v, want %v", baseDelays, want)
	}
	if JitterRatio != 0.25 {
		t.Fatalf("JitterRatio = %v, want 0.25", JitterRatio)
	}
	if MaxAttempts != 6 {
		t.Fatalf("MaxAttempts = %d, want 6", MaxAttempts)
	}
}

// TestSetHooksForTestPanicsOnNil pins the documented contract: SetHooksForTest
// requires both hooks to be non-nil, otherwise it panics. This prevents an
// external-package TestMain from silently disabling only one half of the
// retry timing surface.
func TestSetHooksForTestPanicsOnNil(t *testing.T) {
	cases := []struct {
		name     string
		sleepFn  func(context.Context, time.Duration) error
		jitterFn func() float64
	}{
		{name: "sleep nil", sleepFn: nil, jitterFn: func() float64 { return 1 }},
		{name: "jitter nil", sleepFn: func(context.Context, time.Duration) error { return nil }, jitterFn: nil},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatal("SetHooksForTest did not panic on nil hook")
				}
			}()
			SetHooksForTest(tc.sleepFn, tc.jitterFn)
		})
	}
}

// TestSetHooksForTestOverridesAndRestores exercises the override and the
// returned restore function. Verified by behavior: while the override is
// active, the test-supplied counters tick on each Do attempt; after restore
// the previous overrides are detached and only a freshly installed override
// receives the calls.
func TestSetHooksForTestOverridesAndRestores(t *testing.T) {
	firstSleep := 0
	firstJitter := 0
	restoreFirst := SetHooksForTest(
		func(context.Context, time.Duration) error { firstSleep++; return nil },
		func() float64 { firstJitter++; return 1.0 },
	)
	_ = Do(context.Background(), func(_ context.Context) error { return errors.New("transient") })
	if firstSleep == 0 || firstJitter == 0 {
		t.Fatal("override hooks were not invoked while active")
	}

	restoreFirst()

	secondSleep := 0
	secondJitter := 0
	restoreSecond := SetHooksForTest(
		func(context.Context, time.Duration) error { secondSleep++; return nil },
		func() float64 { secondJitter++; return 1.0 },
	)
	t.Cleanup(restoreSecond)

	firstSleep = 0
	firstJitter = 0
	_ = Do(context.Background(), func(_ context.Context) error { return errors.New("transient") })
	if firstSleep != 0 || firstJitter != 0 {
		t.Fatal("restored overrides should be detached but were still invoked")
	}
	if secondSleep == 0 || secondJitter == 0 {
		t.Fatal("freshly installed overrides did not engage after restore")
	}
}

func TestJitterDefaultStaysInRange(t *testing.T) {
	// Sanity-check the default (non-overridden) jitter implementation. We do
	// not depend on a specific value, only on the documented bound.
	for i := 0; i < 256; i++ {
		v := jitter()
		if v < 1-JitterRatio || v > 1+JitterRatio {
			t.Fatalf("jitter()=%v out of [%v,%v]", v, 1-JitterRatio, 1+JitterRatio)
		}
	}
}
