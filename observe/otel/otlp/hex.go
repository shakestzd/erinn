// Package otlp decodes OpenTelemetry protobuf (and JSON-over-HTTP)
// payloads into the protobuf-free intermediate types defined by
// internal/otel/adapter. It isolates every reference to
// go.opentelemetry.io/proto/otlp so adapters, tests, and the writer
// layer do not pull in the proto dependency.
package otlp

import (
	"encoding/hex"
	"errors"
)

// HexEncodeID lowercase-hex-encodes a trace or span ID. The OTLP/HTTP
// JSON spec requires hex; gRPC (protobuf binary) uses raw bytes. The
// receiver normalizes all IDs to lowercase hex before adapters see
// them so downstream code never has to branch on transport.
//
// Returns "" for empty input. A trace_id is always 16 bytes → 32 hex
// chars; a span_id is always 8 bytes → 16 hex chars. Callers validate
// lengths; this helper accepts any length for robustness.
func HexEncodeID(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return hex.EncodeToString(b)
}

// HexDecodeID decodes a lowercase hex string into the original bytes.
// Used for tests that construct spans from known IDs. Returns an error
// for non-hex input; empty input returns nil, nil.
func HexDecodeID(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	return hex.DecodeString(s)
}

// ValidTraceID reports whether b is a valid OTel trace ID. The spec
// requires exactly 16 bytes, nonzero. The all-zero trace ID is the
// "invalid" sentinel and must be rejected — otherwise a buggy sender
// can collapse many traces into one.
func ValidTraceID(b []byte) bool {
	if len(b) != 16 {
		return false
	}
	for _, x := range b {
		if x != 0 {
			return true
		}
	}
	return false
}

// ValidSpanID reports whether b is a valid OTel span ID: exactly 8
// bytes, nonzero.
func ValidSpanID(b []byte) bool {
	if len(b) != 8 {
		return false
	}
	for _, x := range b {
		if x != 0 {
			return true
		}
	}
	return false
}

// ErrInvalidTraceID is returned when a trace_id fails ValidTraceID.
// The receiver logs and drops such signals rather than persisting them.
var ErrInvalidTraceID = errors.New("invalid OTel trace ID (must be 16 nonzero bytes)")

// ErrInvalidSpanID is returned when a span_id fails ValidSpanID.
var ErrInvalidSpanID = errors.New("invalid OTel span ID (must be 8 nonzero bytes)")
