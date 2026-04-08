package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Open opens (or creates) an HtmlGraph SQLite database at the given path,
// applies performance PRAGMAs, and ensures the schema exists.
func Open(dbPath string) (*sql.DB, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating db directory: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	if err := ApplyPragmas(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("applying pragmas: %w", err)
	}

	if err := CreateAllTables(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating tables: %w", err)
	}

	if err := CreateAllIndexes(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating indexes: %w", err)
	}

	// Idempotent migrations for columns added after initial schema.
	db.Exec(`ALTER TABLE sessions ADD COLUMN title TEXT`)
	db.Exec(`ALTER TABLE sessions ADD COLUMN active_feature_id TEXT`)
	db.Exec(`ALTER TABLE sessions ADD COLUMN updated_at DATETIME`)
	db.Exec(`ALTER TABLE agent_events ADD COLUMN subagent_type TEXT`)
	db.Exec(`ALTER TABLE sessions ADD COLUMN git_remote_url TEXT`)
	db.Exec(`ALTER TABLE sessions ADD COLUMN project_dir TEXT`)
	db.Exec(`ALTER TABLE tool_calls ADD COLUMN feature_id TEXT`)
	db.Exec(`ALTER TABLE messages ADD COLUMN agent_id TEXT`)

	// Per-agent work attribution: each subagent records its identity on claims.
	db.Exec(`ALTER TABLE claims ADD COLUMN claimed_by_agent_id TEXT DEFAULT ""`)

	// Drop deprecated tables replaced by claims system.
	db.Exec(`DROP TABLE IF EXISTS agent_collaboration`)
	db.Exec(`DROP TABLE IF EXISTS agent_presence`)

	// Trigger: auto-increment sessions.total_events on each agent_event insert.
	db.Exec(`CREATE TRIGGER IF NOT EXISTS trg_increment_total_events
		AFTER INSERT ON agent_events
		FOR EACH ROW
		BEGIN
			UPDATE sessions
			SET total_events = total_events + 1
			WHERE session_id = NEW.session_id;
		END`)

	// Backfill total_events for existing sessions that were created before the trigger.
	db.Exec(`UPDATE sessions SET total_events = (
		SELECT COUNT(*) FROM agent_events WHERE agent_events.session_id = sessions.session_id
	) WHERE total_events = 0 AND EXISTS (
		SELECT 1 FROM agent_events WHERE agent_events.session_id = sessions.session_id
	)`)

	// FTS5 semantic index — non-fatal if FTS5 unavailable.
	_ = CreateSemanticIndex(db)

	return db, nil
}

// CreateAllTables creates every HtmlGraph table if it does not already exist.
// Mirrors create_all_tables() from Python ddl.py.
func CreateAllTables(db *sql.DB) error {
	stmts := []string{
		// 1. agent_events
		`CREATE TABLE IF NOT EXISTS agent_events (
			event_id TEXT PRIMARY KEY,
			agent_id TEXT NOT NULL,
			event_type TEXT NOT NULL CHECK(
				event_type IN ('tool_call','tool_result','error','delegation',
				               'completion','start','end','check_point','task_delegation',
				               'teammate_idle','task_completed',
				               'claim.proposed','claim.claimed','claim.heartbeat','claim.blocked',
				               'claim.completed','claim.abandoned','claim.expired','claim.handoff')
			),
			timestamp DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			tool_name TEXT,
			input_summary TEXT,
			tool_input JSON,
			output_summary TEXT,
			context JSON,
			session_id TEXT NOT NULL,
			feature_id TEXT,
			parent_agent_id TEXT,
			parent_event_id TEXT,
			subagent_type TEXT,
			child_spike_count INTEGER DEFAULT 0,
			cost_tokens INTEGER DEFAULT 0,
			execution_duration_seconds REAL DEFAULT 0.0,
			status TEXT DEFAULT 'recorded',
			model TEXT,
			claude_task_id TEXT,
			source TEXT DEFAULT 'hook',
			step_id TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE CASCADE ON UPDATE CASCADE,
			FOREIGN KEY (parent_event_id) REFERENCES agent_events(event_id) ON DELETE SET NULL ON UPDATE CASCADE,
			FOREIGN KEY (feature_id) REFERENCES features(id) ON DELETE SET NULL ON UPDATE CASCADE
		)`,

		// 2. features
		`CREATE TABLE IF NOT EXISTS features (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL CHECK(
				type IN ('feature','bug','spike','chore','epic','task','plan','spec')
			),
			title TEXT NOT NULL,
			description TEXT,
			status TEXT NOT NULL DEFAULT 'todo' CHECK(
				status IN ('todo','in-progress','blocked','done','active','ended','stale')
			),
			priority TEXT DEFAULT 'medium' CHECK(
				priority IN ('low','medium','high','critical')
			),
			assigned_to TEXT,
			assignee TEXT DEFAULT NULL,
			track_id TEXT,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			completed_at DATETIME,
			steps_total INTEGER DEFAULT 0,
			steps_completed INTEGER DEFAULT 0,
			parent_feature_id TEXT,
			tags JSON,
			metadata JSON,
			FOREIGN KEY (track_id) REFERENCES tracks(id),
			FOREIGN KEY (parent_feature_id) REFERENCES features(id)
		)`,

		// 3. sessions
		`CREATE TABLE IF NOT EXISTS sessions (
			session_id TEXT PRIMARY KEY,
			agent_assigned TEXT NOT NULL,
			parent_session_id TEXT,
			parent_event_id TEXT,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			completed_at DATETIME,
			total_events INTEGER DEFAULT 0,
			total_tokens_used INTEGER DEFAULT 0,
			context_drift REAL DEFAULT 0.0,
			status TEXT NOT NULL DEFAULT 'active' CHECK(
				status IN ('active','completed','paused','failed')
			),
			transcript_id TEXT,
			transcript_path TEXT,
			transcript_synced DATETIME,
			start_commit TEXT,
			end_commit TEXT,
			is_subagent BOOLEAN DEFAULT FALSE,
			features_worked_on JSON,
			metadata JSON,
			last_user_query_at DATETIME,
			last_user_query TEXT,
			handoff_notes TEXT,
			recommended_next TEXT,
			blockers JSON,
			recommended_context JSON,
			continued_from TEXT,
			cost_budget REAL,
			cost_threshold_breached INTEGER DEFAULT 0,
			predicted_cost REAL DEFAULT 0.0,
			model TEXT,
			active_feature_id TEXT,
			updated_at DATETIME,
			FOREIGN KEY (parent_session_id) REFERENCES sessions(session_id) ON DELETE SET NULL ON UPDATE CASCADE,
			FOREIGN KEY (parent_event_id) REFERENCES agent_events(event_id) ON DELETE SET NULL ON UPDATE CASCADE,
			FOREIGN KEY (continued_from) REFERENCES sessions(session_id) ON DELETE SET NULL ON UPDATE CASCADE
		)`,

		// 4. tracks
		`CREATE TABLE IF NOT EXISTS tracks (
			id TEXT PRIMARY KEY,
			type TEXT DEFAULT 'track',
			title TEXT NOT NULL,
			description TEXT,
			priority TEXT DEFAULT 'medium' CHECK(
				priority IN ('low','medium','high','critical')
			),
			status TEXT NOT NULL DEFAULT 'todo' CHECK(
				status IN ('todo','in-progress','blocked','done','active','ended','stale')
			),
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			completed_at DATETIME,
			features JSON,
			metadata JSON
		)`,

		// 5. claims
		`CREATE TABLE IF NOT EXISTS claims (
			claim_id TEXT PRIMARY KEY,
			work_item_id TEXT NOT NULL,
			track_id TEXT,
			owner_session_id TEXT NOT NULL,
			owner_agent TEXT NOT NULL DEFAULT 'claude-code',
			status TEXT NOT NULL DEFAULT 'proposed' CHECK(
				status IN ('proposed','claimed','in_progress','blocked',
				           'handoff_pending','completed','abandoned','expired','rejected')
			),
			intended_output TEXT,
			write_scope JSON,
			leased_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			lease_expires_at DATETIME NOT NULL,
			last_heartbeat_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			dependencies JSON,
			progress_notes TEXT,
			blocker_reason TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (work_item_id) REFERENCES features(id) ON DELETE CASCADE,
			FOREIGN KEY (owner_session_id) REFERENCES sessions(session_id) ON DELETE CASCADE
		)`,

		// 6. graph_edges
		`CREATE TABLE IF NOT EXISTS graph_edges (
			edge_id TEXT PRIMARY KEY,
			from_node_id TEXT NOT NULL,
			from_node_type TEXT NOT NULL,
			to_node_id TEXT NOT NULL,
			to_node_type TEXT NOT NULL,
			relationship_type TEXT NOT NULL,
			weight REAL DEFAULT 1.0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			metadata JSON
		)`,

		// 7. git_commits
		`CREATE TABLE IF NOT EXISTS git_commits (
			commit_hash TEXT NOT NULL,
			session_id TEXT NOT NULL,
			feature_id TEXT,
			tool_event_id TEXT,
			message TEXT,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (commit_hash, session_id)
		)`,

		// 8. live_events
		`CREATE TABLE IF NOT EXISTS live_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			event_type TEXT NOT NULL,
			event_data TEXT NOT NULL,
			parent_event_id TEXT,
			session_id TEXT,
			spawner_type TEXT,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			broadcast_at TIMESTAMP
		)`,

		// 9. agent_lineage_trace
		`CREATE TABLE IF NOT EXISTS agent_lineage_trace (
			trace_id TEXT PRIMARY KEY,
			root_session_id TEXT NOT NULL,
			session_id TEXT,
			agent_name TEXT,
			depth INTEGER DEFAULT 0,
			path TEXT,
			feature_id TEXT,
			started_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			completed_at DATETIME,
			status TEXT DEFAULT 'active'
		)`,

		// 10. messages (transcript data from Claude Code JSONL)
		`CREATE TABLE IF NOT EXISTS messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			ordinal INTEGER NOT NULL,
			role TEXT NOT NULL CHECK(role IN ('user','assistant')),
			content TEXT NOT NULL DEFAULT '',
			timestamp DATETIME,
			has_thinking INTEGER DEFAULT 0,
			has_tool_use INTEGER DEFAULT 0,
			content_length INTEGER DEFAULT 0,
			model TEXT,
			input_tokens INTEGER DEFAULT 0,
			output_tokens INTEGER DEFAULT 0,
			cache_read_tokens INTEGER DEFAULT 0,
			stop_reason TEXT,
			uuid TEXT,
			parent_uuid TEXT,
			UNIQUE(session_id, ordinal)
		)`,

		// 11. tool_calls (extracted from assistant messages)
		`CREATE TABLE IF NOT EXISTS tool_calls (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			message_id INTEGER REFERENCES messages(id) ON DELETE CASCADE,
			session_id TEXT NOT NULL,
			tool_name TEXT NOT NULL,
			category TEXT NOT NULL DEFAULT 'Other',
			tool_use_id TEXT,
			input_json TEXT,
			result_content_length INTEGER DEFAULT 0,
			subagent_session_id TEXT,
			feature_id TEXT
		)`,

		// 12. feature_files — file paths touched by each feature
		`CREATE TABLE IF NOT EXISTS feature_files (
			id TEXT PRIMARY KEY,
			feature_id TEXT NOT NULL,
			file_path TEXT NOT NULL,
			operation TEXT NOT NULL DEFAULT 'unknown',
			session_id TEXT,
			first_seen DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_seen DATETIME DEFAULT CURRENT_TIMESTAMP,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(feature_id, file_path)
		)`,

		// 13. agent_presence
		`CREATE TABLE IF NOT EXISTS agent_presence (
			agent_id TEXT PRIMARY KEY,
			status TEXT NOT NULL DEFAULT 'offline' CHECK(
				status IN ('active','idle','offline')
			),
			current_feature_id TEXT,
			last_tool_name TEXT,
			last_activity DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			total_tools_executed INTEGER DEFAULT 0,
			total_cost_tokens INTEGER DEFAULT 0,
			session_id TEXT,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (current_feature_id) REFERENCES features(id) ON DELETE SET NULL,
			FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE SET NULL
		)`,

		// 14. metadata — key-value store for operational state (e.g. last_indexed_commit)
		`CREATE TABLE IF NOT EXISTS metadata (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at TEXT DEFAULT (datetime('now'))
		)`,

		// 15. plan_feedback — structured feedback from CRISPI plan review
		`CREATE TABLE IF NOT EXISTS plan_feedback (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			plan_id TEXT NOT NULL,
			section TEXT NOT NULL,
			action TEXT NOT NULL,
			value TEXT,
			question_id TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(plan_id, section, action, question_id)
		)`,
	}

	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("exec DDL: %w\nSQL: %.120s", err, stmt)
		}
	}
	return nil
}

// CreateAllIndexes creates performance indexes matching Python ddl.py.
func CreateAllIndexes(db *sql.DB) error {
	indexes := []string{
		// agent_events
		"CREATE INDEX IF NOT EXISTS idx_agent_events_session_ts_desc ON agent_events(session_id, timestamp DESC)",
		"CREATE INDEX IF NOT EXISTS idx_agent_events_agent_ts_desc ON agent_events(agent_id, timestamp DESC)",
		"CREATE INDEX IF NOT EXISTS idx_agent_events_agent ON agent_events(agent_id)",
		"CREATE INDEX IF NOT EXISTS idx_agent_events_type ON agent_events(event_type)",
		"CREATE INDEX IF NOT EXISTS idx_agent_events_parent_event ON agent_events(parent_event_id)",
		"CREATE INDEX IF NOT EXISTS idx_agent_events_task_delegation ON agent_events(event_type, subagent_type, timestamp DESC)",
		"CREATE INDEX IF NOT EXISTS idx_agent_events_session_tool ON agent_events(session_id, tool_name)",
		"CREATE INDEX IF NOT EXISTS idx_agent_events_timestamp ON agent_events(timestamp DESC)",
		"CREATE INDEX IF NOT EXISTS idx_agent_events_claude_task_id ON agent_events(claude_task_id)",
		"CREATE INDEX IF NOT EXISTS idx_agent_events_step_id ON agent_events(step_id)",
		// features
		"CREATE INDEX IF NOT EXISTS idx_features_status_priority ON features(status, priority DESC, created_at DESC)",
		"CREATE INDEX IF NOT EXISTS idx_features_track_priority ON features(track_id, priority DESC, created_at DESC)",
		"CREATE INDEX IF NOT EXISTS idx_features_assigned ON features(assigned_to)",
		"CREATE INDEX IF NOT EXISTS idx_features_parent ON features(parent_feature_id)",
		"CREATE INDEX IF NOT EXISTS idx_features_type ON features(type)",
		"CREATE INDEX IF NOT EXISTS idx_features_created ON features(created_at DESC)",
		// sessions
		"CREATE INDEX IF NOT EXISTS idx_sessions_agent_created ON sessions(agent_assigned, created_at DESC)",
		"CREATE INDEX IF NOT EXISTS idx_sessions_status_created ON sessions(status, created_at DESC)",
		"CREATE INDEX IF NOT EXISTS idx_sessions_parent ON sessions(parent_session_id, created_at DESC)",
		"CREATE INDEX IF NOT EXISTS idx_sessions_created ON sessions(created_at DESC)",
		// tracks
		"CREATE INDEX IF NOT EXISTS idx_tracks_status_created ON tracks(status, created_at DESC)",
		"CREATE INDEX IF NOT EXISTS idx_tracks_priority ON tracks(priority DESC)",
		// claims
		"CREATE INDEX IF NOT EXISTS idx_claims_work_item ON claims(work_item_id)",
		"CREATE INDEX IF NOT EXISTS idx_claims_session ON claims(owner_session_id)",
		"CREATE INDEX IF NOT EXISTS idx_claims_status ON claims(status)",
		"CREATE INDEX IF NOT EXISTS idx_claims_lease_expiry ON claims(lease_expires_at)",
		// graph_edges
		"CREATE INDEX IF NOT EXISTS idx_edges_from ON graph_edges(from_node_id)",
		"CREATE INDEX IF NOT EXISTS idx_edges_to ON graph_edges(to_node_id)",
		"CREATE INDEX IF NOT EXISTS idx_edges_type ON graph_edges(relationship_type)",
		// git_commits
		"CREATE INDEX IF NOT EXISTS idx_git_commits_feature ON git_commits(feature_id)",
		// live_events
		"CREATE INDEX IF NOT EXISTS idx_live_events_pending ON live_events(broadcast_at) WHERE broadcast_at IS NULL",
		"CREATE INDEX IF NOT EXISTS idx_live_events_created ON live_events(created_at DESC)",
		// agent_lineage_trace
		"CREATE INDEX IF NOT EXISTS idx_lineage_root ON agent_lineage_trace(root_session_id)",
		"CREATE INDEX IF NOT EXISTS idx_lineage_session ON agent_lineage_trace(session_id)",
		// messages
		"CREATE INDEX IF NOT EXISTS idx_messages_session_ord ON messages(session_id, ordinal)",
		"CREATE INDEX IF NOT EXISTS idx_messages_session_role ON messages(session_id, role)",
		"CREATE INDEX IF NOT EXISTS idx_messages_timestamp ON messages(timestamp DESC)",
		// tool_calls
		"CREATE INDEX IF NOT EXISTS idx_tool_calls_session ON tool_calls(session_id)",
		"CREATE INDEX IF NOT EXISTS idx_tool_calls_message ON tool_calls(message_id)",
		"CREATE INDEX IF NOT EXISTS idx_tool_calls_name ON tool_calls(tool_name)",
		"CREATE INDEX IF NOT EXISTS idx_tool_calls_category ON tool_calls(category)",
		"CREATE INDEX IF NOT EXISTS idx_tool_calls_feature ON tool_calls(feature_id)",
		// feature_files
		"CREATE INDEX IF NOT EXISTS idx_feature_files_feature ON feature_files(feature_id)",
		"CREATE INDEX IF NOT EXISTS idx_feature_files_path ON feature_files(file_path)",
		// plan_feedback
		"CREATE INDEX IF NOT EXISTS idx_plan_feedback_plan_id ON plan_feedback(plan_id)",
		"CREATE INDEX IF NOT EXISTS idx_plan_feedback_section ON plan_feedback(plan_id, section)",
	}

	for _, idx := range indexes {
		if _, err := db.Exec(idx); err != nil {
			// Non-fatal: silently continue (matches Python behaviour).
			// Do NOT log to stderr — Claude Code treats any hook stderr as an error.
			_ = err
		}
	}
	return nil
}
