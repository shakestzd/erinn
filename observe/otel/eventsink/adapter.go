// Package eventsink provides the otel-backed implementation of the core
// eventsink.Sink boundary. It maps harness-agnostic core Events onto
// otel.UnifiedSignal and writes them through the per-session NDJSON sink.
//
// Registering the factory in init() inverts the dependency: core hooks depend
// only on core/eventsink, while the telemetry mapping lives here, alongside
// otel. Importing this package (typically a blank import from the binary's main
// package) is what wires real telemetry into core's lifecycle hooks.
//
// It lives under internal/otel/ — not in the base otel package — because it
// imports internal/otel/sink/ndjson, which itself imports internal/otel; doing
// the registration here keeps the base otel package free of that cycle.
package eventsink

import (
	"context"
	"os"
	"path/filepath"

	coreevents "github.com/shakestzd/wipnote/core/eventsink"
	"github.com/shakestzd/wipnote/observe/otel"
	"github.com/shakestzd/wipnote/observe/otel/sink/ndjson"
)

func init() {
	coreevents.Register(newSink)
}

func newSink(projectDir, sessionID string) (coreevents.Sink, error) {
	// The NDJSON sink opens .wipnote/sessions/<id>/events.ndjson immediately and
	// does not create the parent dir, so ensure it exists here. Owning this in
	// the adapter keeps callers (core hooks) ignorant of telemetry storage layout.
	sessDir := filepath.Join(projectDir, ".wipnote", "sessions", sessionID)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		return nil, err
	}
	s, err := ndjson.New(projectDir, sessionID)
	if err != nil {
		return nil, err
	}
	return &otelSink{s: s}, nil
}

// otelSink adapts a core Event into an otel.UnifiedSignal and appends it to the
// session's events.ndjson via the underlying NDJSON sink.
type otelSink struct{ s *ndjson.Sink }

func (o *otelSink) EmitEvent(ctx context.Context, ev coreevents.Event) error {
	harness := otel.Harness(ev.Harness)
	sig := otel.UnifiedSignal{
		Harness:       harness,
		SignalID:      ev.SignalID,
		Kind:          otel.Kind(ev.Kind),
		CanonicalName: ev.CanonicalName,
		NativeName:    ev.NativeName,
		Timestamp:     ev.Timestamp,
		SessionID:     ev.SessionID,
		RawAttrs:      ev.Attrs,
	}
	return o.s.WriteBatch(ctx, harness, nil, []otel.UnifiedSignal{sig})
}

func (o *otelSink) Close() error { return o.s.Close() }
