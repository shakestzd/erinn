package db

import (
	"database/sql"
	"fmt"
	"time"
)

// CreateOtelTables creates the OpenTelemetry ingestion tables. It is
// called from Open after CreateAllTables so the otel_signals foreign
// key to sessions(session_id) resolves. All statements are idempotent.
//
// Schema overview:
//
//	otel_signals         — one row per OTLP metric point, log record, or span
//	otel_resource_attrs  — per-session resource attribute snapshot (service.version, terminal.type, ...)
//	otel_session_rollup  — materialized totals written on SessionEnd
//
// Design notes:
//   - signal_id is the idempotency key. Receivers compute it as a hash
//     of (resource, scope, name, timestamp, sorted attributes) so OTLP
//     retries don't double-count. INSERT OR IGNORE on conflict.
//   - session_id is normalized across harnesses (Claude session.id, Codex
//     conversation_id, Gemini session.id).
//   - prompt_id is Claude's native prompt.id, or a synthesized ID for
//     Codex (codex:{conversation_id}:{turn_counter}) and any future
//     harness without a native per-turn correlator.
//   - tokens_* columns cover every dimension any harness emits; unused
//     dimensions are NULL, not 0, so aggregate queries can distinguish
//     "zero reported" from "not applicable".
//   - cost_source records how cost_usd was derived: "vendor" when the
//     harness reported it natively (Claude), "derived" when we computed
//     it from tokens × pricing (Codex, Gemini), or "unknown" when we
//     lacked pricing data for the model.
func CreateOtelTables(db *sql.DB) error {
	stmts := []string{
		// otel_signals: one row per OTLP metric/log/span signal.
		`CREATE TABLE IF NOT EXISTS otel_signals (
			signal_id             TEXT PRIMARY KEY,
			harness               TEXT NOT NULL,
			session_id            TEXT NOT NULL,
			prompt_id             TEXT,
			trace_id              TEXT,
			span_id               TEXT,
			parent_span           TEXT,
			kind                  TEXT NOT NULL CHECK(kind IN ('metric','log','span')),
			canonical             TEXT NOT NULL,
			native                TEXT NOT NULL,
			ts_micros             INTEGER NOT NULL,
			tool_name             TEXT,
			tool_use_id           TEXT,
			model                 TEXT,
			decision              TEXT,
			decision_source       TEXT,
			tokens_in             INTEGER,
			tokens_out            INTEGER,
			tokens_cache_read     INTEGER,
			tokens_cache_creation INTEGER,
			tokens_thought        INTEGER,
			tokens_tool           INTEGER,
			tokens_reasoning      INTEGER,
			cost_usd              REAL,
			cost_source           TEXT CHECK(cost_source IS NULL OR cost_source IN ('vendor','derived','unknown')),
			duration_ms           INTEGER,
			success               INTEGER,
			error_msg             TEXT,
			attempt               INTEGER,
			status_code           INTEGER,
			attrs_json            TEXT NOT NULL,
			created_at            INTEGER NOT NULL DEFAULT (strftime('%s','now') * 1000000),
			FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE CASCADE ON UPDATE CASCADE
		)`,

		// otel_resource_attrs: one row per (session_id, key).
		// Resource attributes repeat on every OTLP batch; we snapshot them
		// once per session so queries can filter by terminal.type, host.arch,
		// service.version, etc. without scanning otel_signals.
		`CREATE TABLE IF NOT EXISTS otel_resource_attrs (
			session_id TEXT NOT NULL,
			harness    TEXT NOT NULL,
			key        TEXT NOT NULL,
			value      TEXT NOT NULL,
			observed_at INTEGER NOT NULL DEFAULT (strftime('%s','now') * 1000000),
			PRIMARY KEY (session_id, key),
			FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE CASCADE ON UPDATE CASCADE
		)`,

		// otel_session_rollup: aggregated totals, materialized on SessionEnd.
		// The dashboard reads this table for cheap per-session cost/token
		// summaries instead of scanning otel_signals. Rebuilt idempotently
		// from otel_signals, so destroying and recomputing is always safe.
		`CREATE TABLE IF NOT EXISTS otel_session_rollup (
			session_id                   TEXT PRIMARY KEY,
			harness                      TEXT NOT NULL,
			total_cost_usd               REAL,
			total_tokens_in              INTEGER,
			total_tokens_out             INTEGER,
			total_tokens_cache_read      INTEGER,
			total_tokens_cache_creation  INTEGER,
			total_tokens_thought         INTEGER,
			total_tokens_tool            INTEGER,
			total_tokens_reasoning       INTEGER,
			total_turns                  INTEGER,
			total_tool_calls             INTEGER,
			total_api_calls              INTEGER,
			total_api_errors             INTEGER,
			max_attempt                  INTEGER,
			materialized_at              INTEGER NOT NULL,
			FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE CASCADE ON UPDATE CASCADE
		)`,
	}

	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("exec OTel DDL: %w\nSQL: %.160s", err, stmt)
		}
	}

	// feature_id and agent_id are added by migrations.go's versioned step
	// 019_otel_signals_attribution_columns (bug-286ce8f7), not here. They
	// used to be idempotent ALTERs bolted directly onto this function, which
	// is WRONG for any column added after a database's first bootstrap:
	// CreateOtelTables is only reachable from migration step version 1, and
	// step 1 runs exactly once per database (the first time user_version
	// advances from 0), never again once a database reaches
	// currentSchemaVersion. A column added here after that point silently
	// never applies to any already-migrated database, regardless of which
	// binary or entry point (serve, hooks, reindex) opens it. See
	// stepOtelSignalsAttributionColumns in migrations.go for the fix and the
	// full incident writeup. agent_id (feat-be696acc) is populated at write
	// time by the OTLP writers (observe/otel/receiver and
	// observe/otel/sink/sqlite): a signal's own native "agent_id" attribute
	// (Claude Code's per-span attribution — present on claude_code.llm_request
	// and claude_code.tool spans) when present, else its immediate parent
	// span's resolved agent_id (one hop only), else NULL for root. This is a
	// forward-only migration: existing rows are NOT backfilled, so agent_id
	// is NULL on every signal written before this column existed.

	// pending_subagent_starts: staging table written by the SubagentStart hook.
	// The OTLP receiver reads this to synthesize a placeholder otel_signals row
	// as soon as the first subagent span arrives, eliminating the "flash" where
	// orphan tool-call spans render without a parent Agent row.
	//
	// agent_id is the unique subagent identity (WIPNOTE_AGENT_ID written into
	// the subagent env by writeSubagentEnvVars and echoed as a resource attribute
	// wipnote.agent_id on every subagent OTel span).
	//
	// agent_span_id is the span_id of the otel_signals placeholder row created
	// for this subagent. Populated by the OTLP receiver's placeholder-creation
	// path so later re-attribution queries can map agent_id → agent span_id without
	// scanning otel_signals.
	//
	// consumed_at is set when the placeholder is first matched to an incoming span.
	// Rows older than 24 h are purged by PurgeStalePendingSubagentStarts on startup
	// and periodically.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS pending_subagent_starts (
		agent_id          TEXT PRIMARY KEY,
		agent_type        TEXT NOT NULL,
		session_id        TEXT NOT NULL,
		cwd               TEXT,
		parent_agent_id   TEXT,
		created_at        INTEGER NOT NULL,
		consumed_at       INTEGER,
		agent_span_id     TEXT
	)`); err != nil {
		return fmt.Errorf("exec pending_subagent_starts DDL: %w", err)
	}
	// Idempotent migration: agent_span_id column added after initial schema.
	if _, err := db.Exec(`ALTER TABLE pending_subagent_starts ADD COLUMN agent_span_id TEXT`); err != nil {
		// Ignore "duplicate column" errors — the column is already there.
	}
	// Index creation lives in CreateOtelIndexes, which is what the versioned
	// step 002_create_indexes actually applies. Creating an index here instead
	// reaches only databases that run the create-all path (step 1, once, on a
	// v0 database) — the same defect shape TestCreateAllTables_..._MigrationCoverage
	// guards for tables. bug-0fc17d53 landed exactly that way.

	return nil
}

// PendingSubagentStart holds data written by the SubagentStart hook so the
// OTLP receiver can synthesize a placeholder otel_signals row before the
// real Agent span arrives.
type PendingSubagentStart struct {
	AgentID       string
	AgentType     string
	SessionID     string
	CWD           string
	ParentAgentID string
	CreatedAt     int64 // microseconds since epoch
}

// UpsertPendingSubagentStart inserts or replaces a pending_subagent_starts row.
// INSERT OR REPLACE tolerates re-delivery of SubagentStart events.
func UpsertPendingSubagentStart(db *sql.DB, p *PendingSubagentStart) error {
	query, args := UpsertPendingSubagentStartStmt(p)
	if _, err := db.Exec(query, args...); err != nil {
		return fmt.Errorf("upsert pending_subagent_starts: %w", err)
	}
	return nil
}

// UpsertPendingSubagentStartStmt builds the parameterized INSERT OR REPLACE that
// UpsertPendingSubagentStart Execs, WITHOUT executing it, so the hot-path
// subagent-start hook can route the exact same statement through the daemon's
// enqueue-only seam instead of blocking a direct writable handle under a held
// external write lock (bug-c9ec25a4). The returned (sql, args) is byte-for-byte
// equivalent to what UpsertPendingSubagentStart binds today.
//
// JSON-TRANSPORT SAFETY: cwd / parent_agent_id are normalized through
// nullableStr — which returns nil or a plain string — and created_at is an int64.
// Every arg is therefore a transport-safe primitive (string / number / nil) the
// daemon can JSON-encode and re-bind identically. No sql.NullString crosses the
// wire.
func UpsertPendingSubagentStartStmt(p *PendingSubagentStart) (string, []any) {
	query := `
		INSERT OR REPLACE INTO pending_subagent_starts
			(agent_id, agent_type, session_id, cwd, parent_agent_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`
	args := []any{
		p.AgentID, p.AgentType, p.SessionID,
		nullableStr(p.CWD), nullableStr(p.ParentAgentID), p.CreatedAt,
	}
	return query, args
}

// GetPendingSubagentStart fetches the row for agentID, or returns nil if not found.
func GetPendingSubagentStart(db *sql.DB, agentID string) (*PendingSubagentStart, error) {
	var p PendingSubagentStart
	var cwd, parentAgentID sql.NullString
	err := db.QueryRow(`
		SELECT agent_id, agent_type, session_id, cwd, parent_agent_id, created_at
		FROM pending_subagent_starts
		WHERE agent_id = ?`, agentID).Scan(
		&p.AgentID, &p.AgentType, &p.SessionID, &cwd, &parentAgentID, &p.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get pending_subagent_starts: %w", err)
	}
	p.CWD = cwd.String
	p.ParentAgentID = parentAgentID.String
	return &p, nil
}

// MarkPendingSubagentConsumed sets consumed_at to now for the given agentID.
// Used for observability; not required for correctness.
func MarkPendingSubagentConsumed(db *sql.DB, agentID string, consumedAt int64) {
	db.Exec(`UPDATE pending_subagent_starts SET consumed_at = ? WHERE agent_id = ?`,
		consumedAt, agentID)
}

// SetPendingSubagentAgentSpanID records the span_id of the placeholder
// otel_signals row for the given agentID. Called by the OTLP receiver's
// placeholder-creation path so subsequent re-attribution queries can map
// agent_id → agent span_id in O(1) without scanning otel_signals.
// Best-effort: errors are silently ignored by the caller.
func SetPendingSubagentAgentSpanID(db *sql.DB, agentID, agentSpanID string) error {
	_, err := db.Exec(
		`UPDATE pending_subagent_starts SET agent_span_id = ? WHERE agent_id = ?`,
		agentSpanID, agentID,
	)
	if err != nil {
		return fmt.Errorf("set pending_subagent_starts.agent_span_id: %w", err)
	}
	return nil
}

// GetPendingSubagentAgentSpanID returns the agent_span_id for a given agentID,
// or empty string if not found or not yet set. Used for re-attribution.
func GetPendingSubagentAgentSpanID(db *sql.DB, agentID string) string {
	var v sql.NullString
	db.QueryRow(`SELECT agent_span_id FROM pending_subagent_starts WHERE agent_id = ?`, agentID).Scan(&v)
	return v.String
}

// PurgeStalePendingSubagentStarts deletes rows older than 24 h.
// Subagents never run longer than that, so stale rows are safe to remove.
func PurgeStalePendingSubagentStarts(db *sql.DB) {
	cutoff := (int64(0) + time.Now().Add(-24*time.Hour).UnixMicro())
	db.Exec(`DELETE FROM pending_subagent_starts WHERE created_at < ?`, cutoff)
}

// nullableStr returns nil for empty strings (maps to SQL NULL).
func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// CreateOtelIndexes creates performance indexes for the OTel tables.
// Mirrors the CreateAllIndexes pattern — non-fatal on individual failures
// so a partially-migrated DB can still serve traffic.
func CreateOtelIndexes(db *sql.DB) error {
	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_otel_session_ts   ON otel_signals(session_id, ts_micros)",
		"CREATE INDEX IF NOT EXISTS idx_otel_prompt       ON otel_signals(prompt_id)",
		"CREATE INDEX IF NOT EXISTS idx_otel_canonical_ts ON otel_signals(canonical, ts_micros DESC)",
		"CREATE INDEX IF NOT EXISTS idx_otel_trace        ON otel_signals(trace_id)",
		"CREATE INDEX IF NOT EXISTS idx_otel_parent_span  ON otel_signals(parent_span)",
		"CREATE INDEX IF NOT EXISTS idx_otel_tool         ON otel_signals(session_id, tool_name, ts_micros)",
		"CREATE INDEX IF NOT EXISTS idx_otel_harness      ON otel_signals(harness, ts_micros DESC)",
		"CREATE INDEX IF NOT EXISTS idx_otel_model_ts     ON otel_signals(model, ts_micros) WHERE model IS NOT NULL",

		// span_id is indexed but NOT unique, and must never become unique
		// again (bug-0fc17d53). A span_id identifies a span, not a signal:
		// in OTel every log record and metric correlated to a span carries
		// that span's id, and Claude Code hangs many of them off one
		// interaction span. A UNIQUE index here combined with the writer's
		// INSERT OR IGNORE silently discarded every signal after the first
		// for each span — 86,758 rows, 56% of the corpus it was measured on,
		// with no error and no log line. The index exists only so the
		// placeholder-upgrade lookups (WHERE span_id = ?) stay cheap; none of
		// them need at-most-one-row, and each carries its own discriminator.
		"CREATE INDEX IF NOT EXISTS idx_otel_span_id      ON otel_signals(span_id) WHERE span_id IS NOT NULL",

		// Serves pruneMetricSignals, which runs once per metric signal and was
		// the dominant ingest cost before this index existed (bug-129bf18d).
		// Both its DELETE and its keep-the-newest-N subquery filter on
		// (session_id, kind); with only session_id indexed they degraded into a
		// walk of every row in the session, making ingest quadratic in session
		// size. The trailing DESC columns match the subquery's ORDER BY exactly,
		// which turns the plan into a covering-index search with no temp B-tree:
		// 33.8ms -> 1.7ms per call, and 5.5x faster end-to-end replay on a real
		// shard. Keep the column order and the DESC qualifiers — dropping them
		// costs half the gain (measured: 3.4ms per call without them).
		"CREATE INDEX IF NOT EXISTS idx_otel_session_kind_ts ON otel_signals(session_id, kind, ts_micros DESC, created_at DESC, signal_id DESC)",

		"CREATE INDEX IF NOT EXISTS idx_pending_subagent_session ON pending_subagent_starts(session_id)",
	}
	for _, idx := range indexes {
		if _, err := db.Exec(idx); err != nil {
			_ = err // non-fatal, matches CreateAllIndexes convention
		}
	}
	return nil
}
