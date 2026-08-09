package db

import (
	"path/filepath"
	"strings"
	"testing"
)

// pruneMetricSignalsSQL mirrors the statement in
// observe/otel/receiver/writer.go's pruneMetricSignals. It is duplicated here
// rather than imported because core/ cannot depend on observe/; the test below
// exists precisely to catch the two drifting apart in a way that matters.
const pruneMetricSignalsSQL = `
	DELETE FROM otel_signals
	WHERE session_id = ?
	  AND kind = 'metric'
	  AND signal_id NOT IN (
		SELECT signal_id
		FROM otel_signals
		WHERE session_id = ? AND kind = 'metric'
		ORDER BY ts_micros DESC, created_at DESC, signal_id DESC
		LIMIT ?
	  )`

// TestPruneMetricSignalsUsesCoveringIndex pins the query plan for the hottest
// statement in the OTel ingest path (bug-129bf18d).
//
// pruneMetricSignals runs once per metric signal. With only session_id
// indexed, each call walked every row in the session — including the
// non-metric majority — so per-signal ingest cost grew with session size and
// total ingest was quadratic. On a real shard that was 33.8ms per call and
// 63.6s to replay 28,000 signals.
//
// idx_otel_session_kind_ts fixes it, but only in a specific shape: the
// (session_id, kind) prefix makes the search selective, and the trailing DESC
// columns match the subquery's ORDER BY so SQLite can satisfy it from the
// index rather than building a temporary B-tree. Dropping the DESC tail keeps
// a correct plan and loses half the benefit (3.4ms per call instead of 1.7ms),
// which is exactly the kind of regression that reads as harmless in review.
//
// So this asserts the plan, not merely the index's existence.
func TestPruneMetricSignalsUsesCoveringIndex(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "otel.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	rows, err := database.Query("EXPLAIN QUERY PLAN "+pruneMetricSignalsSQL, "s", "s", 5000)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()

	var plan []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan plan row: %v", err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(plan) == 0 {
		t.Fatal("empty query plan — the statement did not parse against the live schema")
	}
	joined := strings.Join(plan, " | ")

	if !strings.Contains(joined, "idx_otel_session_kind_ts") {
		t.Errorf("pruneMetricSignals does not use idx_otel_session_kind_ts.\nplan: %s\n\n"+
			"Without it this statement walks every row in the session, once per metric "+
			"signal, making ingest quadratic in session size (bug-129bf18d).", joined)
	}
	if !strings.Contains(joined, "COVERING INDEX") {
		t.Errorf("pruneMetricSignals no longer resolves from a covering index.\nplan: %s\n\n"+
			"The index must carry every column the statement reads, or SQLite falls back "+
			"to fetching rows from the table.", joined)
	}
	if strings.Contains(strings.ToUpper(joined), "TEMP B-TREE") {
		t.Errorf("pruneMetricSignals is building a temporary B-tree.\nplan: %s\n\n"+
			"The index's trailing columns must match the subquery's "+
			"ORDER BY ts_micros DESC, created_at DESC, signal_id DESC, including the "+
			"DESC qualifiers. Without them the plan stays correct but costs roughly "+
			"twice as much per call.", joined)
	}
}
