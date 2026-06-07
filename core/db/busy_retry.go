// Package db — busy_retry.go: shared SQLITE_BUSY retry-with-backoff helper.
//
// bug-74a7bda7: user-visible contended writers (the CLI completion path and
// the hook subprocess writer) must not surface a transient SQLITE_BUSY /
// "database is locked" as a hard failure. SQLite's busy_timeout handles
// lock-wait at the connection level, but it does NOT retry the
// SHARED→RESERVED upgrade race that a contended DELETE-journal database can
// still produce. This helper layers a small bounded exponential backoff on
// top of busy_timeout so a brief overlap with another writer (or the
// dashboard read pool) resolves transparently instead of failing the user's
// command.
//
// The helper is filesystem-agnostic: it does not look at journal mode. WAL
// hosts rarely hit BUSY at all (so the fast path is a single attempt with no
// sleeps), and DELETE hosts get the retry budget that closes the residual
// race. Only BUSY/locked errors are retried — every other error (and
// success) returns immediately so callers never pay latency for real
// failures.
package db

import "time"

// DefaultBusyBackoff is the exponential delay schedule used between retry
// attempts. Three attempts after the initial try: ~200ms, ~600ms, ~1800ms.
// Total worst-case added latency is ~2.6s, comfortably under the user's
// patience threshold for a `wipnote * complete` and well within the hook
// subprocess timeout budget.
var DefaultBusyBackoff = []time.Duration{
	200 * time.Millisecond,
	600 * time.Millisecond,
	1800 * time.Millisecond,
}

// busySleep is indirected so tests can run the retry logic without real
// sleeps. Production code never reassigns it.
var busySleep = time.Sleep

// RetryOnBusy runs fn, retrying only when it returns a SQLITE_BUSY / "database
// is locked" error. It makes one initial attempt plus len(backoff) retries,
// sleeping the corresponding backoff duration between attempts. The first
// non-BUSY result (success or hard error) is returned immediately. The final
// BUSY error is returned if every attempt is exhausted.
//
// fn MUST be idempotent or safely re-runnable: it can be invoked up to
// len(backoff)+1 times. All current callers wrap whole-statement upserts /
// status transitions that are idempotent by construction.
func RetryOnBusy(backoff []time.Duration, fn func() error) error {
	err := fn()
	if !IsBusyError(err) {
		return err
	}
	for _, d := range backoff {
		busySleep(d)
		err = fn()
		if !IsBusyError(err) {
			return err
		}
	}
	return err
}
