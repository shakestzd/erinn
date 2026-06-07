// Package eventsink defines the lifecycle-event emission boundary for core.
//
// Core hooks construct harness-agnostic [Event] values and emit them through a
// [Sink] obtained from [New] — without importing any telemetry (otel) types or
// sinks. Concrete sinks live OUTSIDE core (e.g. an otel-backed adapter) and
// install themselves via [Register]. This inverts the historical
// hooks -> otel dependency: core depends only on this interface, while the
// telemetry implementation depends on core. It keeps core free of telemetry
// coupling and pre-stages the deferred observe/ extraction.
package eventsink

import (
	"context"
	"time"
)

// Canonical event vocabulary emitted by core hooks. These are stable
// cross-harness wire names; an otel-side guard test asserts they match the
// corresponding otel constants so the two never drift.
const (
	// KindLog marks a log-shaped (non-metric, non-span) signal.
	KindLog = "log"
	// CanonicalSessionStart is the canonical name for a session-start event.
	CanonicalSessionStart = "session_start"
)

// Event is a harness-agnostic lifecycle signal emitted by core hooks. It is the
// inversion boundary: callers populate it without knowledge of otel.
type Event struct {
	Harness       string         // emitting harness id, e.g. "wipnote"
	SignalID      string         // stable per-event id
	Kind          string         // e.g. KindLog
	CanonicalName string         // stable cross-harness name, e.g. CanonicalSessionStart
	NativeName    string         // the harness's own name for the event
	Timestamp     time.Time      // event time (UTC)
	SessionID     string         // correlating session id
	Attrs         map[string]any // additional attributes, verbatim
}

// Sink consumes lifecycle [Event]s. Implementations are provided outside core
// and registered via [Register].
type Sink interface {
	EmitEvent(ctx context.Context, ev Event) error
	Close() error
}

// Factory builds a per-session [Sink] rooted at projectDir.
type Factory func(projectDir, sessionID string) (Sink, error)

var registered Factory

// Register installs the process-wide [Sink] factory. It is intended to be
// called once at init time by the telemetry-providing package; the last
// registration wins.
func Register(f Factory) { registered = f }

// New returns a [Sink] for the given session. When no [Factory] is registered
// (pure-core consumers, tests), it returns a no-op Sink so callers never need a
// nil check — emission silently does nothing, matching the best-effort
// semantics of lifecycle telemetry.
func New(projectDir, sessionID string) (Sink, error) {
	if registered == nil {
		return nopSink{}, nil
	}
	return registered(projectDir, sessionID)
}

// nopSink is the zero-behavior Sink used when no Factory is registered.
type nopSink struct{}

func (nopSink) EmitEvent(context.Context, Event) error { return nil }
func (nopSink) Close() error                           { return nil }
