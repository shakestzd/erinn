// Package sink defines the SignalSink interface — the single abstraction
// for persisting batches of unified OTel signals. Two implementations ship:
//
//   - sqlite: wraps the existing Writer (placeholder/upgrade logic included)
//   - ndjson: append-only NDJSON file per session (no DB; indexer handles upgrade)
package sink

import (
	"context"

	"github.com/shakestzd/wipnote/observe/otel"
)

// SignalSink is the persistence abstraction for a batch of unified signals.
// WriteBatch must be safe to call concurrently from multiple goroutines.
// Close releases any held resources; callers must not call WriteBatch after Close.
type SignalSink interface {
	// WriteBatch persists one OTLP request's worth of signals.
	// harness identifies the emitting AI coding tool.
	// resourceAttrs are the OTel resource-level attributes for this batch.
	// signals is the normalized set of signals to persist.
	WriteBatch(ctx context.Context, harness otel.Harness, resourceAttrs map[string]any, signals []otel.UnifiedSignal) error

	// Close releases underlying resources (file handles, DB connections).
	Close() error
}
