package apply

import (
	"context"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/daemon"
)

// TestAckMeansDurable_VersionSkewIsFallback is the roborev-480 finding 2 unit
// test for the route decision. A version-skew error ack (the daemon rejected an
// op whose op_format_version did not match what it speaks) must map to FALSE so
// the caller falls back to the bounded direct write — never a silent mis-apply.
// The table also pins the rest of the ack→decision contract so the version-skew
// case can't regress by accident.
func TestAckMeansDurable_VersionSkewIsFallback(t *testing.T) {
	cases := []struct {
		name  string
		ack   daemon.Ack
		async bool
		want  bool
	}{
		{"version-skew-error", daemon.Ack{Status: daemon.AckError, Error: "unsupported op_format_version 1 (daemon speaks 2)"}, false, false},
		{"version-skew-error-async", daemon.Ack{Status: daemon.AckError, Error: "unsupported op_format_version 1 (daemon speaks 2)"}, true, false},
		{"generic-error", daemon.Ack{Status: daemon.AckError, Error: "writequeue: queue full"}, false, false},
		{"applied", daemon.Ack{Status: daemon.AckApplied}, false, true},
		{"duplicate", daemon.Ack{Status: daemon.AckDuplicate}, false, true},
		{"enqueued-async-ok", daemon.Ack{Status: daemon.AckEnqueued}, true, true},
		{"enqueued-sync-not-ok", daemon.Ack{Status: daemon.AckEnqueued}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ackMeansDurable(tc.ack, tc.async); got != tc.want {
				t.Fatalf("ackMeansDurable(%q, async=%v) = %v, want %v", tc.ack.Status, tc.async, got, tc.want)
			}
		})
	}
}

// TestVersionSkew_LiveDaemon_FallsBack is the finding-2 mixed-version round-trip
// integration test. It stands up a live current-version daemon, then dials it
// with a client envelope carrying a DELIBERATELY mismatched op_format_version
// (simulating a stale OLD client, or a new client hitting an old daemon at the
// same version number). The daemon must error-ack, the op must NOT apply, and
// feeding that ack through the route decision must yield false (→ direct-write
// fallback). A control op at the matching version still applies, proving only
// the skew is rejected.
func TestVersionSkew_LiveDaemon_FallsBack(t *testing.T) {
	wDB, projectRoot, sock := startSQLListener(t, nil)
	_ = wDB
	_ = projectRoot

	client := daemon.NewWriterClientForSocket(sock)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	op := DerivedOp{Type: OpTypeSQL, SQL: `INSERT INTO kv (k, n) VALUES (?, ?)`, Args: []any{"skew", 1}}
	payload, err := Encode(op)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	// Explicitly mismatched version — the client would otherwise default it to
	// the current OpFormatVersion. This is the cross-version frame.
	skewEnv := daemon.Envelope{
		OpFormatVersion: daemon.OpFormatVersion + 1,
		OpID:            "skew-live",
		OpType:          OpTypeSQL,
		Payload:         payload,
	}
	ack, err := client.Submit(ctx, skewEnv)
	if err != nil {
		t.Fatalf("skew submit: %v", err)
	}
	if ack.Status != daemon.AckError {
		t.Fatalf("skew ack = %q, want %q (version mismatch must error-ack)", ack.Status, daemon.AckError)
	}
	// The route decision must treat the skew error-ack as a miss → fall back.
	if ackMeansDurable(ack, false) {
		t.Fatal("version-skew error ack mapped to durable=true — caller would skip its safe direct-write fallback")
	}

	// The skewed op must NOT have applied: the row is absent.
	var n int
	if err := wDB.QueryRow(`SELECT n FROM kv WHERE k = ?`, "skew").Scan(&n); err == nil {
		t.Fatalf("skewed op applied a row (n=%d) — a mis-apply across versions", n)
	}

	// Control: a matching-version op (version defaulted by the client) applies.
	matchEnv := daemon.Envelope{OpID: "match-live", OpType: OpTypeSQL, Payload: payload}
	matchAck, err := client.Submit(ctx, matchEnv)
	if err != nil {
		t.Fatalf("matched submit: %v", err)
	}
	if matchAck.Status != daemon.AckApplied {
		t.Fatalf("matched ack = %q, want %q", matchAck.Status, daemon.AckApplied)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := wDB.QueryRow(`SELECT n FROM kv WHERE k = ?`, "skew").Scan(&n); err == nil && n == 1 {
			return // control op applied — only the skewed frame was rejected
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("matched-version control op never applied — the row was not written")
}
