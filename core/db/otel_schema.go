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

	// Idempotent migration: feature_id column added after initial schema
	// so existing DBs pick it up on the next `wipnote serve`. Duplicate
	// column errors are expected on re-runs and are silently swallowed,
	// matching the convention used elsewhere in internal/db/schema.go.
	if _, err := db.Exec(`ALTER TABLE otel_signals ADD COLUMN feature_id TEXT`); err != nil {
		// Ignore "duplicate column" errors — the column is already there.
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_otel_feature_ts ON otel_signals(feature_id, ts_micros) WHERE feature_id IS NOT NULL`); err != nil {
		// Index creation is non-critical; continue.
	}

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
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_pending_subagent_session ON pending_subagent_starts(session_id)`); err != nil {
		// Index creation is non-critical; continue.
	}

	// Unique index on span_id (where not null) allows the writer to detect
	// placeholder rows keyed on span_id and upgrade them when the real Agent
	// span arrives. Added as an idempotent migration so existing DBs pick it up.
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_otel_span_id_unique ON otel_signals(span_id) WHERE span_id IS NOT NULL`); err != nil {
		// Non-fatal: may fail on existing DBs with duplicate span_ids from before
		// this migration. The placeholder feature degrades gracefully without it.
		_ = err
	}

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
	_, err := db.Exec(`
		INSERT OR REPLACE INTO pending_subagent_starts
			(agent_id, agent_type, session_id, cwd, parent_agent_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		p.AgentID, p.AgentType, p.SessionID,
		nullableStr(p.CWD), nullableStr(p.ParentAgentID), p.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert pending_subagent_starts: %w", err)
	}
	return nil
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
	}
	for _, idx := range indexes {
		if _, err := db.Exec(idx); err != nil {
			_ = err // non-fatal, matches CreateAllIndexes convention
		}
	}
	return nil
}
