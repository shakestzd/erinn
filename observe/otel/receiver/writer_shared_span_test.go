package receiver_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/observe/otel"
)

// sharedSpanSignals builds the shape that bug-0fc17d53 destroyed: several
// distinct signals correlated to ONE span. This is ordinary OTel — a span_id
// identifies a span, and every log record emitted inside it carries that id.
// Claude Code does exactly this, hanging hook, api_request, tool_result and
// assistant_response logs off a single interaction span.
func sharedSpanSignals(session, span string) []otel.UnifiedSignal {
	natives := []string{
		"hook_execution_start",
		"hook_execution_complete",
		"api_request",
		"tool_result",
		"assistant_response",
	}
	out := make([]otel.UnifiedSignal, 0, len(natives)+1)

	// The span itself.
	out = append(out, otel.UnifiedSignal{
		Harness:       otel.HarnessClaude,
		SignalID:      "sig-span-" + span,
		Kind:          otel.KindSpan,
		CanonicalName: otel.CanonicalInteraction,
		NativeName:    "claude_code.interaction",
		Timestamp:     time.Unix(0, 1735000000000000000),
		SessionID:     session,
		SpanID:        span,
		RawAttrs:      map[string]any{"n": 0},
	})

	// Log records correlated to it.
	for i, native := range natives {
		out = append(out, otel.UnifiedSignal{
			Harness:       otel.HarnessClaude,
			SignalID:      fmt.Sprintf("sig-%s-%d", span, i),
			Kind:          otel.KindLog,
			CanonicalName: otel.CanonicalUnknown,
			NativeName:    native,
			Timestamp:     time.Unix(0, 1735000000000000000+int64(i+1)*1_000_000),
			SessionID:     session,
			SpanID:        span,
			RawAttrs:      map[string]any{"n": i + 1},
		})
	}
	return out
}

// TestSignalsSharingOneSpanAllPersist is the regression test for bug-0fc17d53.
//
// NON-VACUITY: this test only means something because the fixture's signals
// share a span_id. Confirmed to FAIL against the pre-fix schema by recreating
// the UNIQUE index the migration drops — see
// TestSignalsSharingOneSpanAreLostUnderTheOldUniqueIndex below, which asserts
// the loss actually happens under the old shape. A fixture whose signals each
// had their own span would pass either way, which is precisely how a
// 56%-data-loss defect survived in this write path unnoticed.
func TestSignalsSharingOneSpanAllPersist(t *testing.T) {
	w, dbPath := newWriter(t)
	signals := sharedSpanSignals("sess-shared", "span-abc")

	if _, err := w.WriteBatch(context.Background(), otel.HarnessClaude, nil, signals); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}

	read, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()

	var got int
	if err := read.QueryRow(`SELECT COUNT(*) FROM otel_signals WHERE span_id = ?`, "span-abc").Scan(&got); err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != len(signals) {
		t.Errorf("rows for span-abc = %d, want %d — signals sharing a span_id are being dropped", got, len(signals))
	}

	// Every native name must survive, not just the count. A count-only
	// assertion would pass if the writer stored the same row repeatedly.
	rows, err := read.Query(`SELECT native FROM otel_signals WHERE span_id = ? ORDER BY native`, "span-abc")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var native string
		if err := rows.Scan(&native); err != nil {
			t.Fatal(err)
		}
		seen[native] = true
	}
	for _, s := range signals {
		if !seen[s.NativeName] {
			t.Errorf("signal %q (span %s) is missing from otel_signals", s.NativeName, s.SpanID)
		}
	}
}

// TestSignalsSharingOneSpanAreLostUnderTheOldUniqueIndex proves the test above
// is not vacuous: it recreates the exact index the migration removes and shows
// the same fixture silently loses all but one row, with no error returned from
// the write path.
//
// If this test ever fails, the unique index has stopped causing loss and the
// regression test above no longer proves anything — check whether INSERT OR
// IGNORE is still in use on otel_signals before trusting it.
func TestSignalsSharingOneSpanAreLostUnderTheOldUniqueIndex(t *testing.T) {
	w, dbPath := newWriter(t)

	admin, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(
		`CREATE UNIQUE INDEX idx_otel_span_id_unique ON otel_signals(span_id) WHERE span_id IS NOT NULL`,
	); err != nil {
		admin.Close()
		t.Fatalf("recreate pre-fix index: %v", err)
	}
	admin.Close()

	signals := sharedSpanSignals("sess-shared", "span-old")
	if _, err := w.WriteBatch(context.Background(), otel.HarnessClaude, nil, signals); err != nil {
		t.Fatalf("WriteBatch returned an error; the pre-fix behaviour was a SILENT drop: %v", err)
	}

	read, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()

	var got int
	if err := read.QueryRow(`SELECT COUNT(*) FROM otel_signals WHERE span_id = ?`, "span-old").Scan(&got); err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != 1 {
		t.Errorf("rows under the pre-fix unique index = %d, want 1 — the index no longer collapses "+
			"signals sharing a span, so TestSignalsSharingOneSpanAllPersist proves nothing", got)
	}
	if got == len(signals) {
		t.Error("no loss occurred under the old index: the regression test above is vacuous")
	}
}

// TestSpanIDIndexIsNotUnique pins the schema itself, so a future edit that
// reintroduces uniqueness fails here with a name rather than as a mysterious
// row shortfall months later.
func TestSpanIDIndexIsNotUnique(t *testing.T) {
	_, dbPath := newWriter(t)

	read, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()

	rows, err := read.Query(`PRAGMA index_list(otel_signals)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var seq int
		var name string
		var unique int
		var origin string
		var partial int
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatal(err)
		}
		if unique == 0 || origin == "pk" {
			continue
		}
		cols, err := read.Query(`PRAGMA index_info(` + name + `)`)
		if err != nil {
			t.Fatal(err)
		}
		for cols.Next() {
			var seqno, cid int
			var col sql.NullString
			if err := cols.Scan(&seqno, &cid, &col); err != nil {
				cols.Close()
				t.Fatal(err)
			}
			if col.String == "span_id" {
				cols.Close()
				t.Fatalf("index %q makes otel_signals.span_id unique. span_id identifies a span, "+
					"not a signal — combined with INSERT OR IGNORE this silently discards every "+
					"log and metric correlated to a span after the first (bug-0fc17d53).", name)
			}
		}
		cols.Close()
	}
}
