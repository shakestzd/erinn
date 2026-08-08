package db_test

import (
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
)

func TestOtelSchemaCreated(t *testing.T) {
	dbPath := t.TempDir() + "/otel.db"
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	wantTables := []string{"otel_signals", "otel_resource_attrs", "otel_session_rollup"}
	for _, tbl := range wantTables {
		var got string
		err := database.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", tbl).Scan(&got)
		if err != nil {
			t.Errorf("table %s not created: %v", tbl, err)
		}
	}

	wantIndexes := []string{
		"idx_otel_session_ts",
		"idx_otel_prompt",
		"idx_otel_canonical_ts",
		"idx_otel_trace",
		"idx_otel_parent_span",
		"idx_otel_tool",
		"idx_otel_harness",
		"idx_otel_agent_ts",
	}
	for _, idx := range wantIndexes {
		var got string
		err := database.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='index' AND name=?", idx).Scan(&got)
		if err != nil {
			t.Errorf("index %s not created: %v", idx, err)
		}
	}
}

// TestOtelSignalsAgentIDColumn verifies the agent_id column (feat-be696acc)
// exists on a fresh DB, is nullable, and that re-running the migration
// against an already-migrated DB is a no-op rather than an error — the same
// idempotency guarantee the feature_id column migration already relies on.
func TestOtelSignalsAgentIDColumn(t *testing.T) {
	dbPath := t.TempDir() + "/otel.db"
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	if _, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned) VALUES (?, ?)`,
		"sess-agent-col", "claude-code"); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	// agent_id must accept NULL (pre-migration rows, and rows the resolver
	// can't attribute to any agent).
	if _, err := database.Exec(`
		INSERT INTO otel_signals (
			signal_id, harness, session_id, kind, canonical, native, ts_micros, attrs_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"sig-agent-col-null", "claude_code", "sess-agent-col", "span", "tool_result", "claude_code.tool",
		time.Now().UnixMicro(), "{}",
	); err != nil {
		t.Fatalf("insert with NULL agent_id: %v", err)
	}

	if _, err := database.Exec(`
		INSERT INTO otel_signals (
			signal_id, harness, session_id, kind, canonical, native, ts_micros, attrs_json, agent_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"sig-agent-col-set", "claude_code", "sess-agent-col", "span", "tool_result", "claude_code.tool",
		time.Now().UnixMicro(), "{}", "otel-success@session-abc",
	); err != nil {
		t.Fatalf("insert with agent_id set: %v", err)
	}

	var got string
	if err := database.QueryRow(
		`SELECT agent_id FROM otel_signals WHERE signal_id = ?`, "sig-agent-col-set",
	).Scan(&got); err != nil {
		t.Fatalf("select agent_id: %v", err)
	}
	if got != "otel-success@session-abc" {
		t.Errorf("agent_id = %q, want %q", got, "otel-success@session-abc")
	}

	// Re-running the migration (as happens on every `wipnote serve` startup
	// against an existing DB) must not error on the duplicate ALTER TABLE.
	if err := db.CreateOtelTables(database); err != nil {
		t.Fatalf("re-run CreateOtelTables: %v", err)
	}
}

func TestOtelSignalsInsertAndIdempotency(t *testing.T) {
	dbPath := t.TempDir() + "/otel.db"
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	// Need a session row (foreign key).
	_, err = database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned) VALUES (?, ?)`,
		"sess-1", "claude-code")
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}

	ts := time.Now().UnixMicro()
	insert := func(signalID string) error {
		_, err := database.Exec(`
			INSERT OR IGNORE INTO otel_signals (
				signal_id, harness, session_id, prompt_id,
				kind, canonical, native, ts_micros,
				model, tokens_in, tokens_out, cost_usd, cost_source,
				attrs_json
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			signalID, "claude_code", "sess-1", "prompt-1",
			"log", "api_request", "claude_code.api_request", ts,
			"claude-haiku-4-5", 10, 577, 0.00804885, "vendor",
			`{"request_id":"req_011"}`)
		return err
	}

	if err := insert("sig-A"); err != nil {
		t.Fatalf("insert A: %v", err)
	}

	// Idempotency: second insert with same signal_id must not error and
	// must not produce duplicate rows. OTLP retries exercise this path.
	if err := insert("sig-A"); err != nil {
		t.Fatalf("reinsert A: %v", err)
	}

	var count int
	if err := database.QueryRow("SELECT COUNT(*) FROM otel_signals").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("duplicate signal_id produced %d rows, want 1", count)
	}
}

func TestOtelSignalsCheckConstraints(t *testing.T) {
	dbPath := t.TempDir() + "/otel.db"
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()
	database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned) VALUES (?, ?)`,
		"sess-1", "claude-code")

	// kind must be one of metric|log|span.
	_, err = database.Exec(`
		INSERT INTO otel_signals (signal_id, harness, session_id, kind, canonical, native, ts_micros, attrs_json)
		VALUES ('x', 'claude_code', 'sess-1', 'bogus', 'api_request', 'claude_code.api_request', 0, '{}')`)
	if err == nil {
		t.Error("expected CHECK failure for invalid kind, got nil")
	}

	// cost_source must be one of vendor|derived|unknown (or NULL).
	_, err = database.Exec(`
		INSERT INTO otel_signals (signal_id, harness, session_id, kind, canonical, native, ts_micros, cost_source, attrs_json)
		VALUES ('y', 'claude_code', 'sess-1', 'log', 'api_request', 'claude_code.api_request', 0, 'invented', '{}')`)
	if err == nil {
		t.Error("expected CHECK failure for invalid cost_source, got nil")
	}
}
