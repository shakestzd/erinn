package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// Open opens (or creates) an wipnote SQLite database at the given path,
// applies performance PRAGMAs, and runs any pending schema migrations.
//
// Migrations are version-gated by PRAGMA user_version: a database whose
// user_version already equals currentSchemaVersion takes the warm-open fast
// path and executes ZERO CREATE / ALTER / DROP / trigger / normalisation
// statements. This eliminates the write-lock contention that DDL re-execution
// caused in short-lived hook and CLI processes.
//
// Brand-new databases (user_version = 0) run every registered migration in
// order, then land at currentSchemaVersion. Legacy databases at any
// intermediate version run only the missing migrations and advance to current.
// The full migration registry lives in migrations.go.
func Open(dbPath string) (*sql.DB, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating db directory: %w", err)
	}

	// Embed busy_timeout in the DSN so it is applied on the first connection
	// open, before any schema queries. This prevents SQLITE_BUSY errors during
	// startup races when the OTel receiver writer or a hook binary holds the
	// write lock. ApplyPragmas below re-applies the full set for completeness
	// (idempotent).
	// Skip DSN-level pragma for in-memory DBs; the driver will reject it.
	dsn := dbPath
	isInMemory := strings.Contains(dbPath, ":memory:")
	if !isInMemory {
		dsn = dsn + "?_pragma=busy_timeout(5000)&_txlock=immediate"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	if err := ApplyPragmas(db, BuildPragmas(dbPath)); err != nil {
		db.Close()
		return nil, fmt.Errorf("applying pragmas: %w", err)
	}

	// bug-7dbaf552: runMigrations performs the user_version write (and, for a
	// brand-new / legacy DB, DDL). On a contended DELETE-journal database the
	// SHARED→RESERVED upgrade can transiently SQLITE_BUSY even with
	// busy_timeout set. Wrapping the whole migration step in RetryOnBusy makes
	// EVERY dbpkg.Open caller (lineage/query/session/workitem/...) resilient
	// without each having to hand-roll its own loop. runMigrations is
	// version-gated and idempotent, so re-running it is safe. Same-package
	// call: RetryOnBusy + DefaultBusyBackoff live in busy_retry.go.
	if err := RetryOnBusy(DefaultBusyBackoff, func() error {
		return runMigrations(db)
	}); err != nil {
		db.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	return db, nil
}

// CreateAllTables creates every wipnote table if it does not already exist.
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
				               'teammate_idle','task_created','task_completed','quality_gate',
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
			CHECK (NOT (event_type = 'tool_call' AND agent_id = 'human' AND (tool_name IS NULL OR tool_name != 'UserQuery'))),
			FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE CASCADE ON UPDATE CASCADE,
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
			session_family_id TEXT,
			cost_budget REAL,
			cost_threshold_breached INTEGER DEFAULT 0,
			predicted_cost REAL DEFAULT 0.0,
			model TEXT,
			active_feature_id TEXT,
			updated_at DATETIME,
			exec_worktree_path TEXT,
			branch TEXT,
			harness TEXT,
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
		// feature_id is NOT NULL DEFAULT '' (never NULL) because it is part of
		// the primary key: two NULLs are never equal under a UNIQUE index, so a
		// nullable PK column would silently defeat INSERT OR IGNORE
		// de-duplication for unattributed commits. See migration 018 (bug-3bf05d49)
		// for why feature_id had to join the key: a single commit can
		// legitimately name multiple work items (e.g. "Refs: feat-a, feat-b"),
		// and the old two-column key silently dropped every ID after the first.
		`CREATE TABLE IF NOT EXISTS git_commits (
			commit_hash TEXT NOT NULL,
			session_id TEXT NOT NULL,
			feature_id TEXT NOT NULL DEFAULT '',
			tool_event_id TEXT,
			message TEXT,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (commit_hash, session_id, feature_id)
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

		// 12b. session_files — file paths touched by a session WITHOUT an active
		// claim/feature (claimless visibility, feat-793844bd slice-4). Distinct
		// from feature_files because feature_files.feature_id is NOT NULL with
		// UNIQUE(feature_id,file_path) and cannot hold empty-feature_id rows.
		// Keyed on (session_id, file_path) so a session's claimless touches are
		// upserted idempotently with a single statement and zero hot-path write
		// amplification.
		`CREATE TABLE IF NOT EXISTS session_files (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			file_path TEXT NOT NULL,
			operation TEXT NOT NULL DEFAULT 'unknown',
			first_seen DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_seen DATETIME DEFAULT CURRENT_TIMESTAMP,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(session_id, file_path)
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

		// 15. plan_feedback — structured feedback from CRISPI plan review.
		// anchor / consumed / resolved / resolution_target (migration 016)
		// carry block-level annotation state: anchor pins a note to a specific
		// plan block, and consumed vs resolved are two independent axes routed
		// to agent|human via resolution_target. Non-annotation rows leave them
		// at their zero defaults.
		`CREATE TABLE IF NOT EXISTS plan_feedback (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			plan_id TEXT NOT NULL,
			section TEXT NOT NULL,
			action TEXT NOT NULL,
			value TEXT,
			question_id TEXT NOT NULL DEFAULT '',
			anchor TEXT NOT NULL DEFAULT '',
			consumed INTEGER NOT NULL DEFAULT 0,
			resolved INTEGER NOT NULL DEFAULT 0,
			resolution_target TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(plan_id, section, action, question_id)
		)`,

		// 16. arch_cards — read index for architectural memory cards (.wipnote/arch/*.md)
		// Canonical store is the .md files; this table is derived and never authoritative.
		`CREATE TABLE IF NOT EXISTS arch_cards (
			slug           TEXT PRIMARY KEY,
			kind           TEXT NOT NULL,
			paths_json     TEXT NOT NULL DEFAULT '[]',
			verified_at    TEXT NOT NULL DEFAULT '',
			links_json     TEXT NOT NULL DEFAULT '[]',
			created_by     TEXT NOT NULL DEFAULT '',
			superseded_by  TEXT NOT NULL DEFAULT '',
			retired        INTEGER NOT NULL DEFAULT 0,
			body           TEXT NOT NULL DEFAULT '',
			created_at     DATETIME,
			updated_at     DATETIME,
			indexed_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,

		// 16b. claim_episodes — read index for the claim ledger
		// (.wipnote/claims/*.html). Canonical store is the HTML; this table is
		// derived and never authoritative, and a cache wipe rebuilds it whole.
		//
		// Unlike claims/active_work_items — which are single-mutable-slot CURRENT
		// state — a row here is a closed or open INTERVAL, so a signal emitted at
		// time T by agent A has something to join against.
		//
		// started_at/ended_at are TEXT in claimledger.TimeFormat: RFC3339 UTC with
		// fixed nine-digit fractions, so TEXT comparison IS chronological ordering.
		// ended_at is '' (never NULL) while the episode is open, which keeps the
		// interval predicate a plain string comparison with no COALESCE.
		`CREATE TABLE IF NOT EXISTS claim_episodes (
			episode_id      TEXT PRIMARY KEY,
			work_item_id    TEXT NOT NULL,
			session_id      TEXT NOT NULL,
			root_session_id TEXT NOT NULL DEFAULT '',
			agent_id        TEXT NOT NULL,
			started_at      TEXT NOT NULL,
			ended_at        TEXT NOT NULL DEFAULT '',
			outcome         TEXT NOT NULL DEFAULT '',
			source_file     TEXT NOT NULL DEFAULT '',
			indexed_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,

		// 17. gate_records — DERIVED quality-gate runs, projected from the
		// canonical ledger at .wipnote/gate-ledger.html (feat-0e5ca43e). record_id
		// is that ledger row's id and the key the projection is idempotent on; ""
		// marks a legacy row written before the ledger existed.
		`CREATE TABLE IF NOT EXISTS gate_records (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			record_id TEXT NOT NULL DEFAULT '',
			session_id TEXT NOT NULL,
			work_item_id TEXT,
			harness TEXT,
			project_type TEXT NOT NULL,
			gate_command TEXT NOT NULL,
			status TEXT NOT NULL CHECK(status IN ('pass','fail')),
			checked_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			signature TEXT NOT NULL,
			allowlist_hits_json TEXT NOT NULL DEFAULT '[]',
			allowlist_hit_count INTEGER NOT NULL DEFAULT 0,
			source TEXT NOT NULL DEFAULT 'check',
			output_summary TEXT,
			profile_signature TEXT NOT NULL DEFAULT '',
			guards_run TEXT NOT NULL DEFAULT '[]'
		)`,

		// 18. recaps — read index for committed recap artifacts (.wipnote/recaps/*.html).
		// Canonical store is the HTML files; this table is derived and never authoritative.
		`CREATE TABLE IF NOT EXISTS recaps (
			id            TEXT PRIMARY KEY,
			kind          TEXT NOT NULL DEFAULT '',
			input         TEXT NOT NULL DEFAULT '',
			git_range     TEXT NOT NULL DEFAULT '',
			grounded      INTEGER NOT NULL DEFAULT 0,
			title         TEXT NOT NULL DEFAULT '',
			outcome       TEXT NOT NULL DEFAULT '',
			work_item_id  TEXT NOT NULL DEFAULT '',
			created_at    DATETIME,
			updated_at    DATETIME,
			indexed_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
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
		"CREATE INDEX IF NOT EXISTS idx_sessions_family ON sessions(session_family_id)",
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
		// Composite (file_path, last_seen) — supports the live file-overlap
		// query (Tier 1): an equality+range probe on (file_path, recent
		// last_seen) without range-scanning every row for the path.
		"CREATE INDEX IF NOT EXISTS idx_feature_files_path_seen ON feature_files(file_path, last_seen)",
		// session_files
		"CREATE INDEX IF NOT EXISTS idx_session_files_session ON session_files(session_id)",
		"CREATE INDEX IF NOT EXISTS idx_session_files_path ON session_files(file_path)",
		// arch_cards
		"CREATE INDEX IF NOT EXISTS idx_arch_cards_kind ON arch_cards(kind)",
		"CREATE INDEX IF NOT EXISTS idx_arch_cards_retired ON arch_cards(retired)",
		// claim_episodes — the point query is (agent, timestamp) → work item, so
		// the covering index leads with agent_id and orders by started_at.
		"CREATE INDEX IF NOT EXISTS idx_claim_episodes_agent_start ON claim_episodes(agent_id, started_at)",
		"CREATE INDEX IF NOT EXISTS idx_claim_episodes_session_agent ON claim_episodes(session_id, agent_id, started_at)",
		"CREATE INDEX IF NOT EXISTS idx_claim_episodes_work_item ON claim_episodes(work_item_id, started_at)",
		// plan_feedback
		"CREATE INDEX IF NOT EXISTS idx_plan_feedback_plan_id ON plan_feedback(plan_id)",
		"CREATE INDEX IF NOT EXISTS idx_plan_feedback_section ON plan_feedback(plan_id, section)",
		// gate_records
		"CREATE INDEX IF NOT EXISTS idx_gate_records_session_checked ON gate_records(session_id, checked_at DESC)",
		"CREATE INDEX IF NOT EXISTS idx_gate_records_work_item_checked ON gate_records(work_item_id, checked_at DESC)",
		// PARTIAL unique index: it makes the ledger projection idempotent (INSERT
		// OR IGNORE on a record already projected) while leaving legacy rows, which
		// all share record_id '', outside the constraint entirely.
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_gate_records_record_id ON gate_records(record_id) WHERE record_id != ''",
		// recaps
		"CREATE INDEX IF NOT EXISTS idx_recaps_kind ON recaps(kind)",
		"CREATE INDEX IF NOT EXISTS idx_recaps_work_item ON recaps(work_item_id)",
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

// agentEventsCheckConstraintDDL is the target DDL for the agent_events table
// including the attribution CHECK constraint. It must match CreateAllTables exactly.
const agentEventsCheckConstraintDDL = `CREATE TABLE agent_events (
			event_id TEXT PRIMARY KEY,
			agent_id TEXT NOT NULL,
			event_type TEXT NOT NULL CHECK(
				event_type IN ('tool_call','tool_result','error','delegation',
				               'completion','start','end','check_point','task_delegation',
				               'teammate_idle','task_created','task_completed','quality_gate',
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
			CHECK (NOT (event_type = 'tool_call' AND agent_id = 'human' AND (tool_name IS NULL OR tool_name != 'UserQuery'))),
			FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE CASCADE ON UPDATE CASCADE,
			FOREIGN KEY (feature_id) REFERENCES features(id) ON DELETE SET NULL ON UPDATE CASCADE
		)`

// migrateAgentEventsAddCheckConstraint adds the attribution CHECK constraint to
// the existing agent_events table via copy-and-swap, and also drops the
// self-referential FK on parent_event_id (which caused silent insert failures
// when the parent row didn't exist yet — see bug-89990f33). It is idempotent:
// it only runs when the live DDL requires changes.
//
// The migration uses dynamic column introspection (PRAGMA table_info) to detect
// and preserve columns added by later migrations (reason, teammate_name, team_name,
// prompt_id, etc.), ensuring no data loss during the copy-and-swap.
func migrateAgentEventsAddCheckConstraint(db *sql.DB) error {
	// Check if the live table DDL requires changes.
	var currentSQL string
	err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='agent_events'`,
	).Scan(&currentSQL)
	if err != nil {
		// Table doesn't exist yet — CreateAllTables will create it correctly.
		return nil
	}

	needsCheck := !strings.Contains(currentSQL, "tool_name != 'UserQuery'")
	// The parent_event_id self-referential FK causes silent insert drops when the
	// parent row hasn't been written yet (timing race). Drop it.
	hasParentEventFK := strings.Contains(currentSQL, "REFERENCES agent_events(event_id)")
	if !needsCheck && !hasParentEventFK {
		// Both the check constraint is present and the FK is already gone — nothing to do.
		return nil
	}

	// Disable foreign keys for the duration of the swap.
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable foreign_keys: %w", err)
	}
	defer db.Exec(`PRAGMA foreign_keys = ON`) //nolint:errcheck

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// 1. Introspect the live agent_events table to preserve ALL columns.
	rows, err := tx.Query(`PRAGMA table_info(agent_events)`)
	if err != nil {
		return fmt.Errorf("pragma table_info: %w", err)
	}
	defer rows.Close()

	type columnInfo struct {
		name     string
		typeName string
		notnull  int
		dfltVal  *string
		pk       int
	}
	var columns []columnInfo
	var colNames []string // for SELECT clause
	for rows.Next() {
		var cid int
		var name, typeName string
		var notnull int
		var dfltVal *string
		var pk int
		if err := rows.Scan(&cid, &name, &typeName, &notnull, &dfltVal, &pk); err != nil {
			return fmt.Errorf("scan column info: %w", err)
		}
		columns = append(columns, columnInfo{
			name:     name,
			typeName: typeName,
			notnull:  notnull,
			dfltVal:  dfltVal,
			pk:       pk,
		})
		colNames = append(colNames, name)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate table_info: %w", err)
	}

	// 2. Build the new DDL dynamically from the introspected columns.
	// Preserve the CHECK constraint, session_id FK, and feature_id FK.
	// OMIT the parent_event_id FK (self-referential) — this is the whole point.
	ddlParts := []string{"CREATE TABLE agent_events_new ("}
	for i, col := range columns {
		ddlParts = append(ddlParts, col.name+" "+col.typeName)
		if col.notnull == 1 {
			ddlParts = append(ddlParts, " NOT NULL")
		}
		if col.dfltVal != nil {
			ddlParts = append(ddlParts, " DEFAULT "+*col.dfltVal)
		}
		if col.pk == 1 {
			ddlParts = append(ddlParts, " PRIMARY KEY")
		}
		if i < len(columns)-1 {
			ddlParts = append(ddlParts, ",")
		}
	}
	// Add the CHECK constraint and foreign keys (except parent_event_id FK).
	ddlParts = append(ddlParts, `,
	CHECK (NOT (event_type = 'tool_call' AND agent_id = 'human' AND (tool_name IS NULL OR tool_name != 'UserQuery'))),
	FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE CASCADE ON UPDATE CASCADE,
	FOREIGN KEY (feature_id) REFERENCES features(id) ON DELETE SET NULL ON UPDATE CASCADE`)
	ddlParts = append(ddlParts, ")")
	newTableDDL := strings.Join(ddlParts, "")

	if _, err := tx.Exec(newTableDDL); err != nil {
		return fmt.Errorf("create agent_events_new: %w", err)
	}

	// 3. Copy all rows. Build the INSERT...SELECT with the dynamically derived column list.
	// This preserves all columns (including those added by later migrations).
	selectClause := strings.Join(colNames, ",")
	copySQL := fmt.Sprintf(`
		INSERT OR IGNORE INTO agent_events_new
		SELECT %s FROM agent_events`, selectClause)
	if _, err := tx.Exec(copySQL); err != nil {
		return fmt.Errorf("copy rows to agent_events_new: %w", err)
	}

	// 4. Drop the old table and rename the new one.
	if _, err := tx.Exec(`DROP TABLE agent_events`); err != nil {
		return fmt.Errorf("drop agent_events: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE agent_events_new RENAME TO agent_events`); err != nil {
		return fmt.Errorf("rename agent_events_new: %w", err)
	}

	return tx.Commit()
}
