// Package sqlite provides a SignalSink that delegates to the existing
// receiver.Writer. All placeholder/upgrade span re-parenting logic remains
// in writer.go untouched — this is a thin adapter, not a rewrite.
//
// To avoid an import cycle (receiver → sink/sqlite → receiver), this package
// accepts a writerBatch interface rather than *receiver.Writer directly.
// Call New(w) where w is a *receiver.Writer — it satisfies writerBatch.
package sqlite

import (
	"context"

	"github.com/shakestzd/wipnote/observe/otel"
	"github.com/shakestzd/wipnote/observe/otel/sink"
)

// WriterCloser is the subset of *receiver.Writer used by this sink.
// Defined here to avoid importing the receiver package (which would cycle).
type WriterCloser interface {
	WriteBatch(ctx context.Context, harness otel.Harness, resourceAttrs map[string]any, signals []otel.UnifiedSignal) (int, error)
	Close() error
}

// Sink adapts a WriterCloser to the sink.SignalSink interface.
// The underlying Writer carries all placeholder/upgrade logic via its
// own WriteBatch method; this adapter simply discards the inserted-count
// return value since SignalSink has no concept of partial insertion.
type Sink struct {
	w WriterCloser
}

// New wraps an existing *receiver.Writer as a SignalSink.
func New(w WriterCloser) sink.SignalSink {
	return &Sink{w: w}
}

// WriteBatch delegates to Writer.WriteBatch and discards the inserted count.
func (s *Sink) WriteBatch(ctx context.Context, harness otel.Harness, resourceAttrs map[string]any, signals []otel.UnifiedSignal) error {
	_, err := s.w.WriteBatch(ctx, harness, resourceAttrs, signals)
	return err
}

// Close delegates to the underlying writer to release DB connections.
func (s *Sink) Close() error { return s.w.Close() }
