// Package convert provides signal-conversion helpers shared between the
// embedded OTLP receiver (now deleted) and the per-session otel-collect
// subprocess handler. Splitting these out avoids the import cycle that
// would result from placing them directly in internal/otel (which is
// already imported by internal/otel/adapter).
package convert

import (
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"

	"github.com/shakestzd/wipnote/observe/otel"
	"github.com/shakestzd/wipnote/observe/otel/adapter"
	"github.com/shakestzd/wipnote/observe/otel/otlp"
)

// ConvertAll runs every signal in the decoded batch through the adapter
// and assigns a stable SignalID. The resulting slice is ready for a
// SignalSink.WriteBatch call.
func ConvertAll(a adapter.Adapter, d otlp.Decoded) []otel.UnifiedSignal {
	out := make([]otel.UnifiedSignal, 0, len(d.Metrics)+len(d.Logs)+len(d.Spans))

	for _, sm := range d.Metrics {
		for _, sig := range a.ConvertMetric(d.Resource, sm.Scope, sm.Metric) {
			sig.SignalID = DeriveSignalID(d.Resource, sm.Scope, sm.Metric.Name,
				sm.Metric.Timestamp.UnixNano(), sm.Metric.Attrs)
			out = append(out, sig)
		}
	}
	for _, sl := range d.Logs {
		for _, sig := range a.ConvertLog(d.Resource, sl.Scope, sl.Log) {
			sig.SignalID = DeriveSignalID(d.Resource, sl.Scope, sl.Log.Name,
				sl.Log.Timestamp.UnixNano(), sl.Log.Attrs)
			out = append(out, sig)
		}
	}
	for _, ss := range d.Spans {
		for _, sig := range a.ConvertSpan(d.Resource, ss.Scope, ss.Span) {
			sig.SignalID = DeriveSignalID(d.Resource, ss.Scope, ss.Span.Name,
				ss.Span.StartTime.UnixNano(), ss.Span.Attrs)
			out = append(out, sig)
		}
	}
	// bug-a0143c2c: adapters that need to reconcile two signals from the same
	// batch (e.g. Claude's beta tool.execution span vs. its stable
	// tool_result log — see ClaudeAdapter.CrossValidateToolOutcomes) get one
	// pass over the full converted batch here, harness-agnostically via a
	// type assertion so the shared Adapter interface stays untouched for
	// adapters that don't need it. Kept identical to the same hook in
	// observe/otel/receiver/http.go's ConvertAll — this package and that one
	// are two independent, both-live collection topologies (the embedded
	// daemon receiver vs. the per-session otel-collect subprocess) with their
	// own copy of this function; see that file's comment if this one ever
	// needs to change.
	if cv, ok := a.(interface {
		CrossValidateToolOutcomes([]otel.UnifiedSignal) []otel.UnifiedSignal
	}); ok {
		out = cv.CrossValidateToolOutcomes(out)
	}
	return out
}

// DeriveSignalID returns a stable idempotency key for an OTLP signal.
// OTel SDK exporters retry on transport failure; without a stable key
// every retry would produce a duplicate row.
//
// The key is derived from:
//   - resource service.name (the harness)
//   - scope name + version
//   - signal name
//   - timestamp in nanoseconds
//   - sorted flat attribute key=value pairs
//
// FNV-64a keeps the hash fast (one pass over ~1-2 KB of attribute
// strings) and gives ~1e-7 collision risk at the 10-100 signals/sec
// scale wipnote targets. SignalID is not a security primitive.
// Collisions only cause a single row to be rejected (INSERT OR IGNORE),
// not data corruption.
//
// Output is 16 hex chars for compactness. Callers treat SignalID as
// opaque; never parse it.
func DeriveSignalID(
	res adapter.OTLPResource,
	scope adapter.OTLPScope,
	signalName string,
	timestampNanos int64,
	attrs map[string]any,
) string {
	h := fnv.New64a()

	// service.name first — partitions by harness.
	writeAttr(h, "service.name", adapter.AttrString(res.Attrs, "service.name"))
	writeAttr(h, "scope.name", scope.Name)
	writeAttr(h, "scope.version", scope.Version)
	writeAttr(h, "signal.name", signalName)

	// Timestamp in nanoseconds. Decimal, not binary, so two timestamps
	// that differ by one nanosecond hash to different values.
	h.Write([]byte{0xff})
	h.Write([]byte(strconv.FormatInt(timestampNanos, 10)))

	// Sorted attributes. Go map iteration is nondeterministic, so we
	// sort keys and serialize deterministically to produce stable hashes.
	if len(attrs) > 0 {
		keys := make([]string, 0, len(attrs))
		for k := range attrs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			writeAttr(h, k, fmt.Sprintf("%v", attrs[k]))
		}
	}

	sum := h.Sum64()
	out := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		out[i] = byte(sum)
		sum >>= 8
	}
	return hex.EncodeToString(out)
}

// writeAttr feeds a key/value pair into the hasher with separators that
// cannot appear in either value, so "ab" "cd" and "a" "bcd" don't
// collide.
func writeAttr(h interface{ Write([]byte) (int, error) }, k, v string) {
	h.Write([]byte{0x01})
	h.Write([]byte(k))
	h.Write([]byte{0x02})
	h.Write([]byte(v))
	h.Write([]byte{0x03})
}
