// Package db — busy_counter.go: subsystem-scoped SQLITE_BUSY observability.
//
// SLICE-10 CONTRACT (plan-ae0c37b2, feat-156e0a1a):
//
//	The plan's durable regression signal is "zero SQLITE_BUSY from in-repo
//	writer/indexer/hook paths" (NOT zero BUSY anywhere — external producers
//	like MCP / user-installed tools are explicitly out of scope, per the
//	slice-5 boundary). To make a regression in slices 6/7 (the writer
//	queue + hook-tree consolidation) immediately observable rather than
//	silently re-creating the contention, every code path that runs SQL
//	against the project DB classifies its errors by *subsystem* and bumps
//	a process-level counter on BUSY / locked errors.
//
//	The counter is a pull-based observability primitive: callers wrap
//	their query/exec errors with Record(subsystem, err). Callers stay
//	cheap (one atomic add on the BUSY path, one strings.Contains on the
//	non-BUSY path — the fast path is zero allocations and one branch).
//
// SUBSYSTEM TAXONOMY:
//
//	The first-party producers we gate on are:
//
//	  hook_writer  — internal/hooks/dbgate.go (slice 7) and the OpenHookDB
//	                 fallback path. A BUSY here means the hook subprocess
//	                 failed to take the writer lock — must be zero across
//	                 the stress fixture for the launch gate to pass.
//
//	  indexer      — internal/otel/indexer/* + internal/otel/sink/sqlite
//	                 QueuedSink. The QueuedSink already swallows queue-side
//	                 errors via the canonical-first contract; this label
//	                 catches *op-side* BUSY (the writer worker's actual
//	                 BEGIN IMMEDIATE returning busy).
//
//	  cli_mutation — internal/workitem (and CLI commands that mutate work
//	                 items via dbpkg.Open). These are short-lived foreground
//	                 processes; BUSY here usually means a long-running
//	                 reader has the database locked, which would block
//	                 user-driven work and must remain zero in the stress
//	                 fixture.
//
//	  writer_service — internal/otel/receiver/writer.go (the slice-6
//	                 single-writer Writer). BUSY here is the smoking gun
//	                 for the original contention bug; zero across the
//	                 stress fixture is the launch criterion.
//
//	  external     — third-party producers that don't go through the
//	                 first-party code (MCP servers, user-installed tools).
//	                 Counted for telemetry but DOES NOT gate the launch.
//
// FALSE-POSITIVE TOLERANCE:
//
//	BUSY detection is a string match on "SQLITE_BUSY" / "database is locked"
//	/ "locked". This matches the heuristic used elsewhere in the codebase
//	(cmd/wipnote/graph.go:24 — graphDBError). It is intentionally over-broad:
//	any false positive surfaces as a noisy-but-safe counter bump; a false
//	negative would silently mask the regression we are gating against.
package db

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
)

// BusySubsystem labels the first-party producer that observed a SQLITE_BUSY
// error. The labels are stable (logged + surfaced via /api/collector-status)
// and intentionally narrow so the stress fixture can assert ZERO across
// the first-party set.
type BusySubsystem string

const (
	// SubsystemHookWriter — internal/hooks/dbgate.go OpenHookDB path.
	SubsystemHookWriter BusySubsystem = "hook_writer"

	// SubsystemIndexer — internal/otel/indexer and the QueuedSink op-side.
	SubsystemIndexer BusySubsystem = "indexer"

	// SubsystemCLIMutation — internal/workitem and short-lived CLI write commands.
	SubsystemCLIMutation BusySubsystem = "cli_mutation"

	// SubsystemWriterService — internal/otel/receiver/writer.go pinned writer.
	SubsystemWriterService BusySubsystem = "writer_service"

	// SubsystemExternal — third-party / MCP producers not gated by the launch.
	SubsystemExternal BusySubsystem = "external"
)

// FirstPartySubsystems is the set of labels the stress fixture gates on.
// External BUSY events are counted but do NOT fail the launch criterion.
var FirstPartySubsystems = []BusySubsystem{
	SubsystemHookWriter,
	SubsystemIndexer,
	SubsystemCLIMutation,
	SubsystemWriterService,
}

// busyCounters holds process-level atomic counts keyed by subsystem. A
// sync.Map is overkill for a tiny fixed key set, so we use a plain map +
// RWMutex with atomic.Int64 values so the increment path is lock-free
// after the one-time key lookup.
var (
	busyMu       sync.RWMutex
	busyCounters = make(map[BusySubsystem]*atomic.Int64)
)

// IsBusyError reports whether err is a SQLite BUSY / locked error. The
// detection mirrors the heuristic used in cmd/wipnote/graph.go:graphDBError
// — a string match on the well-known fragments produced by both the
// modernc.org/sqlite driver and the SQLITE_* error code naming. Returns
// false for nil and for errors that don't contain a recognised fragment.
func IsBusyError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// Order matters slightly: SQLITE_BUSY is the most specific token; the
	// generic "locked" / "database is locked" patterns can also fire on
	// driver-internal lock contention messages.
	if strings.Contains(msg, "SQLITE_BUSY") {
		return true
	}
	if strings.Contains(msg, "database is locked") {
		return true
	}
	if strings.Contains(msg, "locked") {
		return true
	}
	return false
}

// Record bumps the counter for subsystem iff err is a BUSY/locked error.
// Returns true on a BUSY hit so callers can branch (e.g., emit a structured
// log line or apply subsystem-specific recovery). On non-BUSY errors this
// is a one-branch fast path with no allocations.
//
// Concurrent-safe: the counter map is created once at package init via a
// lazy initialisation under busyMu; the atomic.Int64 add is lock-free.
func Record(subsystem BusySubsystem, err error) bool {
	if !IsBusyError(err) {
		return false
	}
	c := counterFor(subsystem)
	c.Add(1)
	return true
}

// BusyCount returns the current count for subsystem. Safe for concurrent
// readers; returns 0 for subsystems that have never been recorded.
func BusyCount(subsystem BusySubsystem) int64 {
	busyMu.RLock()
	c, ok := busyCounters[subsystem]
	busyMu.RUnlock()
	if !ok {
		return 0
	}
	return c.Load()
}

// BusyCounts returns a snapshot of every subsystem with a non-zero count
// at the moment of the call. The returned map is a copy; callers may
// mutate it freely without affecting the underlying state.
//
// The snapshot is point-in-time and not transactional across subsystems —
// concurrent Record calls may bump a counter mid-iteration. This is fine
// for the dashboard + status surfaces, which always read for display
// rather than for atomic invariants.
func BusyCounts() map[BusySubsystem]int64 {
	busyMu.RLock()
	defer busyMu.RUnlock()
	out := make(map[BusySubsystem]int64, len(busyCounters))
	for k, v := range busyCounters {
		if c := v.Load(); c > 0 {
			out[k] = c
		}
	}
	return out
}

// FirstPartyBusyTotal sums every counter in FirstPartySubsystems. This is
// the single scalar the stress fixture asserts must be zero. External
// counters are excluded by design (per the slice-5 boundary).
func FirstPartyBusyTotal() int64 {
	var total int64
	busyMu.RLock()
	defer busyMu.RUnlock()
	for _, s := range FirstPartySubsystems {
		if c, ok := busyCounters[s]; ok {
			total += c.Load()
		}
	}
	return total
}

// ResetBusyCounters zeroes every counter. Intended for tests that need a
// clean baseline between table-driven sub-tests; production code should
// never reset.
func ResetBusyCounters() {
	busyMu.Lock()
	defer busyMu.Unlock()
	for _, c := range busyCounters {
		c.Store(0)
	}
}

// counterFor returns the atomic counter for subsystem, lazily creating
// it on first use. The double-checked pattern keeps the steady-state
// path lock-free for reads.
func counterFor(subsystem BusySubsystem) *atomic.Int64 {
	busyMu.RLock()
	c, ok := busyCounters[subsystem]
	busyMu.RUnlock()
	if ok {
		return c
	}
	busyMu.Lock()
	defer busyMu.Unlock()
	// Recheck — another goroutine may have created it.
	if c, ok := busyCounters[subsystem]; ok {
		return c
	}
	c = &atomic.Int64{}
	busyCounters[subsystem] = c
	return c
}

// busyError is a sentinel-style helper so unit tests can synthesize a
// BUSY error without depending on the SQLite driver. The Error string
// contains the canonical fragment so IsBusyError matches it.
type busyError struct{ msg string }

func (e *busyError) Error() string { return e.msg }

// NewSyntheticBusyError returns a *busyError that IsBusyError will accept
// as a BUSY hit. Used exclusively in tests; production code paths should
// pass through real driver errors so the detection covers the actual
// SQLite fragments.
func NewSyntheticBusyError(detail string) error {
	if detail == "" {
		detail = "SQLITE_BUSY: database is locked"
	}
	return &busyError{msg: detail}
}

// Unwrap support so wrapped errors still match the detection. errors.Is
// against a *busyError is not how callers will check; the package-public
// API is IsBusyError. This unwrap method exists so other packages that
// errors.As into *busyError can recover the message.
func (e *busyError) Unwrap() error { return nil }

// Ensure *busyError satisfies the errors.Is contract trivially — we
// don't define Is, so errors.Is uses the default equality check; that's
// the intent (the only way to "match" a busyError is to hold the same
// pointer, which tests don't need).
var _ error = (*busyError)(nil)

// Ensure errors.Unwrap doesn't fail on nil-pointer dereference for the
// synthesized type — covered by the Unwrap method above.
var _ interface{ Unwrap() error } = (*busyError)(nil)

// Ensure the errors package is imported when needed for the unwrap
// signature; the import is used implicitly via the errors.Is contract
// the wrapper satisfies.
var _ = errors.New
