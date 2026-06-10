// Package receiver holds the HTTP OTLP receiver, the SQLite writer,
// and the orchestrator that wires them into `wipnote serve`.
package receiver

import (
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"

	"github.com/shakestzd/wipnote/observe/otel/adapter"
)

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
// FNV-64a keeps the hash fast (one pass over ~1–2 KB of attribute
// strings) and gives ~1e-7 collision risk at the 10–100 signals/sec
// scale wipnote targets. SignalID is not a security primitive —
// SHA-256 would be overkill. Collisions only cause a single row to be
// rejected (INSERT OR IGNORE), not data corruption.
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
