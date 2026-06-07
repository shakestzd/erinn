package db

import (
	"errors"
	"testing"
	"time"
)

// withFakeSleep swaps busySleep for a no-op that records the delays it was
// asked to wait, so RetryOnBusy's backoff schedule can be asserted without
// real wall-clock sleeps. Restored on test cleanup.
func withFakeSleep(t *testing.T) *[]time.Duration {
	t.Helper()
	orig := busySleep
	var slept []time.Duration
	busySleep = func(d time.Duration) { slept = append(slept, d) }
	t.Cleanup(func() { busySleep = orig })
	return &slept
}

func TestRetryOnBusy_SucceedsFirstTryNoSleep(t *testing.T) {
	slept := withFakeSleep(t)
	calls := 0
	err := RetryOnBusy(DefaultBusyBackoff, func() error { calls++; return nil })
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (no retry on success)", calls)
	}
	if len(*slept) != 0 {
		t.Fatalf("slept %v, want no sleeps on first-try success", *slept)
	}
}

func TestRetryOnBusy_NonBusyErrorReturnsImmediately(t *testing.T) {
	slept := withFakeSleep(t)
	calls := 0
	sentinel := errors.New("constraint violation: NOT NULL")
	err := RetryOnBusy(DefaultBusyBackoff, func() error { calls++; return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel (non-BUSY must not retry)", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (non-BUSY error must not retry)", calls)
	}
	if len(*slept) != 0 {
		t.Fatalf("slept %v, want no sleeps on non-BUSY error", *slept)
	}
}

func TestRetryOnBusy_RecoversAfterTransientBusy(t *testing.T) {
	slept := withFakeSleep(t)
	calls := 0
	err := RetryOnBusy(DefaultBusyBackoff, func() error {
		calls++
		if calls < 3 { // BUSY on attempts 1 and 2, succeed on 3
			return errors.New("database is locked (SQLITE_BUSY)")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("err = %v, want nil after transient BUSY", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3 (2 BUSY + 1 success)", calls)
	}
	if len(*slept) != 2 || (*slept)[0] != DefaultBusyBackoff[0] || (*slept)[1] != DefaultBusyBackoff[1] {
		t.Fatalf("slept = %v, want first two backoff steps %v", *slept, DefaultBusyBackoff[:2])
	}
}

func TestRetryOnBusy_ExhaustsBudgetAndReturnsBusy(t *testing.T) {
	slept := withFakeSleep(t)
	calls := 0
	err := RetryOnBusy(DefaultBusyBackoff, func() error {
		calls++
		return errors.New("SQLITE_BUSY: database is locked")
	})
	if !IsBusyError(err) {
		t.Fatalf("err = %v, want terminal BUSY error", err)
	}
	if want := len(DefaultBusyBackoff) + 1; calls != want {
		t.Fatalf("calls = %d, want %d (initial + every backoff retry)", calls, want)
	}
	if len(*slept) != len(DefaultBusyBackoff) {
		t.Fatalf("slept %d times, want %d", len(*slept), len(DefaultBusyBackoff))
	}
}
