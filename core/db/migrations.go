package db

import (
	"database/sql"
	"fmt"
	"strings"
)

// currentSchemaVersion is the highest migration step version. Bump by 1 when
// adding a new migration step to the migrations slice below. PRAGMA
// user_version on a fully-migrated database is equal to currentSchemaVersion.
//
// Versioning replaces the unconditional DDL re-execution that previously ran on
// every Open. The fast warm-open path (when user_version == currentSchemaVersion)
// executes ZERO CREATE / ALTER / DROP / trigger / normalisation statements —
// avoiding the write-lock acquisition that caused SQLITE_BUSY in short-lived
// hook processes.
const currentSchemaVersion = 17

// copySwapStepName is the name of the agent_events copy-and-swap migration
// step. Exposed via CopySwapStepName() so tests can assert it runs at most
// once per database.
const copySwapStepName = "004_agent_events_copy_swap"

// migrationStep represents one ordered, idempotent schema migration. apply
// MUST be safe to call on a database whose live schema is already at or beyond
// the step's intended state — every step is guarded by user_version, but
// idempotent apply functions are belt-and-suspenders insurance against partial
// rollbacks.
type migrationStep struct {
	version int    // user_version after this step applies
	name    string // stable identifier (recorded via the migration observer)
	apply   func(*sql.DB) error
}

// migrations is the ordered registry of every schema migration. Steps are
// applied in slice order. The runner skips steps whose version is <= the
// database's current user_version.
//
// Adding a new step:
//  1. Append a migrationStep with version = currentSchemaVersion + 1.
//  2. Bump currentSchemaVersion to match.
//  3. Make the apply function idempotent — it may run on legacy DBs whose
//     live schema already reflects part of the change.
var migrations = []migrationStep{
	{
		version: 1,
		name:    "001_initial_schema",
		apply:   stepCreateBaseTables,
	},
	{
		version: 2,
		name:    "002_create_indexes",
		apply:   stepCreateIndexes,
	},
	{
		version: 3,
		name:    "003_post_initial_columns_and_tables",
		apply:   stepPostInitialColumnsAndTables,
	},
	{
		version: 4,
		name:    copySwapStepName,
		apply:   stepAgentEventsCopySwap,
	},
	{
		version: 5,
		name:    "005_normalize_plan_feedback",
		apply:   stepNormalizePlanFeedback,
	},
	{
		version: 6,
		name:    "006_gate_records",
		apply:   stepGateRecords,
	},
	{
		version: 7,
		name:    "007_session_family_id",
		apply:   stepSessionFamilyID,
	},
	{
		version: 8,
		name:    "008_session_files",
		apply:   stepSessionFiles,
	},
	{
		version: 9,
		name:    "009_feature_files_path_seen_index",
		apply:   stepFeatureFilesPathSeenIndex,
	},
	{
		version: 10,
		name:    "010_repair_trigger_increment_total_events",
		apply:   stepRepairTriggerIncrementTotalEvents,
	},
	{
		version: 11,
		name:    "011_backfill_total_events",
		apply:   stepBackfillTotalEvents,
	},
	{
		version: 12,
		name:    "012_gate_records_profile_signature",
		apply:   stepGateRecordsProfileSignature,
	},
	{
		version: 13,
		name:    "013_arch_cards",
		apply:   stepArchCards,
	},
	{
		version: 14,
		name:    "014_session_exec_context",
		apply:   stepSessionExecContext,
	},
	{
		version: 15,
		name:    "015_session_handoff_fields",
		apply:   stepSessionHandoffFields,
	},
	{
		version: 16,
		name:    "016_plan_feedback_annotation_state",
		apply:   stepPlanFeedbackAnnotationState,
	},
	{
		version: 17,
		name:    "017_recaps_table",
		apply:   stepRecapsTable,
	},
}

// CurrentSchemaVersion returns the highest migration step version. Exposed for
// tests; production code should not branch on it.
func CurrentSchemaVersion() int { return currentSchemaVersion }

// MigrationStepNames returns the ordered list of step names exposed by the
// migration runner. Tests use this to assert exact migrations applied.
func MigrationStepNames() []string {
	out := make([]string, len(migrations))
	for i, m := range migrations {
		out[i] = m.name
	}
	return out
}

// MigrationStepVersions returns the ordered list of step versions. Tests use
// this to assert strictly-increasing version ordering.
func MigrationStepVersions() []int {
	out := make([]int, len(migrations))
	for i, m := range migrations {
		out[i] = m.version
	}
	return out
}

// CopySwapStepName returns the name of the agent_events copy-and-swap
// migration step so tests can assert it runs at most once per DB.
func CopySwapStepName() string { return copySwapStepName }

// readUserVersion returns the database's current PRAGMA user_version. A fresh
// database reports 0. Errors propagate to the caller (e.g. for fail-fast Open).
func readUserVersion(db *sql.DB) (int, error) {
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("read user_version: %w", err)
	}
	return v, nil
}

// writeUserVersion sets PRAGMA user_version. SQLite does not support parameter
// binding for PRAGMA values, so the literal is rendered into the statement.
func writeUserVersion(db *sql.DB, v int) error {
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", v)); err != nil {
		return fmt.Errorf("write user_version=%d: %w", v, err)
	}
	return nil
}

// runMigrations applies every migration step whose version is greater than the
// database's current user_version. Steps are applied in slice order and the
// user_version PRAGMA is bumped after each successful step so a mid-chain
// failure leaves the database at the last completed step (forward progress is
// preserved across process restarts).
//
// Each step is responsible for its own transactional discipline. Schema-altering
// steps that combine multiple DDL operations wrap them in BEGIN/COMMIT
// internally; trivial single-statement steps may run outside a transaction
// because SQLite's autocommit already provides atomicity for one DDL.
func runMigrations(db *sql.DB) error {
	current, err := readUserVersion(db)
	if err != nil {
		return err
	}
	if current >= currentSchemaVersion {
		return nil
	}

	for _, step := range migrations {
		if step.version <= current {
			continue
		}
		notifyMigration(step.name)
		if err := step.apply(db); err != nil {
			return fmt.Errorf("migration %s (v%d): %w", step.name, step.version, err)
		}
		if err := writeUserVersion(db, step.version); err != nil {
			return fmt.Errorf("after %s: %w", step.name, err)
		}
	}
	return nil
}

// ---- migration step implementations -----------------------------------------

// stepCreateBaseTables creates every wipnote table (CreateAllTables) and the
// OTel ingestion tables (CreateOtelTables). All statements are idempotent
// (CREATE TABLE IF NOT EXISTS), so the step is safe to run against a legacy DB
// whose tables already exist.
func stepCreateBaseTables(db *sql.DB) error {
	if err := CreateAllTables(db); err != nil {
		return fmt.Errorf("create base tables: %w", err)
	}
	if err := CreateOtelTables(db); err != nil {
		return fmt.Errorf("create otel tables: %w", err)
	}
	return nil
}

// stepCreateIndexes installs all performance indexes. Both index sets use
// CREATE INDEX IF NOT EXISTS, so the step is idempotent.
func stepCreateIndexes(db *sql.DB) error {
	if err := CreateAllIndexes(db); err != nil {
		return fmt.Errorf("create base indexes: %w", err)
	}
	if err := CreateOtelIndexes(db); err != nil {
		return fmt.Errorf("create otel indexes: %w", err)
	}
	return nil
}

// stepPostInitialColumnsAndTables collects every column / table / trigger that
// was added after the initial schema landed. Each operation is independently
// idempotent (ALTER TABLE ADD COLUMN swallows "duplicate column"; CREATE TABLE
// IF NOT EXISTS, CREATE INDEX IF NOT EXISTS, CREATE TRIGGER IF NOT EXISTS, and
// DROP TABLE IF EXISTS are all no-ops on second run).
//
// This step does NOT include the agent_events copy-and-swap (step 4) or the
// post-swap columns (teammate_name / team_name / prompt_id, which must run
// AFTER the swap to avoid being lost during the column copy). Those live in
// step 4.
func stepPostInitialColumnsAndTables(db *sql.DB) error {
	// Idempotent column additions on existing tables.
	addCols := []string{
		`ALTER TABLE sessions ADD COLUMN title TEXT`,
		`ALTER TABLE sessions ADD COLUMN active_feature_id TEXT`,
		`ALTER TABLE sessions ADD COLUMN updated_at DATETIME`,
		`ALTER TABLE agent_events ADD COLUMN subagent_type TEXT`,
		`ALTER TABLE agent_events ADD COLUMN reason TEXT`,
		`ALTER TABLE sessions ADD COLUMN git_remote_url TEXT`,
		`ALTER TABLE sessions ADD COLUMN project_dir TEXT`,
		`ALTER TABLE tool_calls ADD COLUMN feature_id TEXT`,
		`ALTER TABLE messages ADD COLUMN agent_id TEXT`,
		`ALTER TABLE claims ADD COLUMN claimed_by_agent_id TEXT DEFAULT ""`,
	}
	for _, stmt := range addCols {
		if _, err := db.Exec(stmt); err != nil {
			if !isDuplicateColumnError(err) {
				return fmt.Errorf("add column (%s): %w", stmt, err)
			}
		}
	}

	// active_work_items: per-agent claim attribution.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS active_work_items (
		session_id    TEXT NOT NULL,
		agent_id      TEXT NOT NULL,
		work_item_id  TEXT NOT NULL,
		claimed_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (session_id, agent_id)
	)`); err != nil {
		return fmt.Errorf("create active_work_items: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_active_work_items_work_item
		ON active_work_items(work_item_id)`); err != nil {
		return fmt.Errorf("create idx_active_work_items_work_item: %w", err)
	}

	// Drop deprecated tables replaced by the claims system.
	if _, err := db.Exec(`DROP TABLE IF EXISTS agent_collaboration`); err != nil {
		return fmt.Errorf("drop agent_collaboration: %w", err)
	}
	if _, err := db.Exec(`DROP TABLE IF EXISTS agent_presence`); err != nil {
		return fmt.Errorf("drop agent_presence: %w", err)
	}

	// Trigger: auto-increment sessions.total_events on each agent_event insert.
	if err := createTriggerIncrementTotalEvents(db); err != nil {
		return err
	}

	// Backfill total_events for sessions that pre-date the trigger.
	if _, err := db.Exec(`UPDATE sessions SET total_events = (
		SELECT COUNT(*) FROM agent_events WHERE agent_events.session_id = sessions.session_id
	) WHERE total_events = 0 AND EXISTS (
		SELECT 1 FROM agent_events WHERE agent_events.session_id = sessions.session_id
	)`); err != nil {
		return fmt.Errorf("backfill total_events: %w", err)
	}
	return nil
}

func stepGateRecords(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS gate_records (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
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
		output_summary TEXT
	)`); err != nil {
		return fmt.Errorf("create gate_records: %w", err)
	}
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_gate_records_session_checked ON gate_records(session_id, checked_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_gate_records_work_item_checked ON gate_records(work_item_id, checked_at DESC)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("create gate_records index: %w", err)
		}
	}
	return nil
}

// stepAgentEventsCopySwap runs the agent_events CHECK-constraint + self-FK-drop
// migration and the three post-swap columns (teammate_name, team_name,
// prompt_id) that must be added AFTER the swap so they aren't dropped during
// the column copy. The copy-swap helper checks the live DDL and short-circuits
// when no changes are required, so this step is idempotent on already-migrated
// DBs.
//
// agent_events indexes are reinstalled after the swap because the DROP TABLE
// inside migrateAgentEventsAddCheckConstraint discards the table's indexes.
// CreateAllIndexes uses CREATE INDEX IF NOT EXISTS for all index sets, so it
// is safe to re-run for every table (the only ones that actually missed indexes
// are the agent_events set).
func stepAgentEventsCopySwap(db *sql.DB) error {
	if err := migrateAgentEventsAddCheckConstraint(db); err != nil {
		return fmt.Errorf("agent_events copy-swap: %w", err)
	}

	postSwapCols := []string{
		`ALTER TABLE agent_events ADD COLUMN teammate_name TEXT`,
		`ALTER TABLE agent_events ADD COLUMN team_name TEXT`,
		// OTel correlation: stable per-turn identifier bridged from OTel signals.
		`ALTER TABLE agent_events ADD COLUMN prompt_id TEXT`,
	}
	for _, stmt := range postSwapCols {
		if _, err := db.Exec(stmt); err != nil {
			if !isDuplicateColumnError(err) {
				return fmt.Errorf("add column after copy-swap (%s): %w", stmt, err)
			}
		}
	}

	// Reinstall indexes lost during the DROP TABLE inside the swap.
	// CreateAllIndexes is fully idempotent (CREATE INDEX IF NOT EXISTS).
	if err := CreateAllIndexes(db); err != nil {
		return fmt.Errorf("reinstall indexes after copy-swap: %w", err)
	}

	// Recreate trg_increment_total_events: the DROP TABLE inside the swap
	// silently destroyed the trigger that was attached to agent_events.
	// Without recreation sessions.total_events stops incrementing after migration.
	if err := createTriggerIncrementTotalEvents(db); err != nil {
		return fmt.Errorf("recreate trigger after copy-swap: %w", err)
	}
	return nil
}

// stepNormalizePlanFeedback rewrites legacy plan_feedback value strings
// ('approved' / 'rejected' / 'changes_requested') to the canonical boolean
// strings ('true' / 'false'). Idempotent: once migrated no rows match the
// WHERE clauses.
func stepNormalizePlanFeedback(db *sql.DB) error {
	return NormalizePlanFeedbackValues(db)
}

// isDuplicateColumnError reports whether err is the "duplicate column" error
// that SQLite returns when ALTER TABLE ADD COLUMN names an already-present
// column. The migration runner uses this to keep the apply function quiet on
// idempotent re-runs.
func isDuplicateColumnError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "duplicate column name")
}

// triggerIncrementTotalEventsSQL is the single source of truth for the
// trg_increment_total_events trigger DDL. Both the initial creation path
// (stepPostInitialColumnsAndTables) and the post-copy-swap recreation path
// (stepAgentEventsCopySwap) use this constant — never copy-paste the SQL.
const triggerIncrementTotalEventsSQL = `CREATE TRIGGER IF NOT EXISTS trg_increment_total_events
	AFTER INSERT ON agent_events
	FOR EACH ROW
	BEGIN
		UPDATE sessions
		SET total_events = total_events + 1
		WHERE session_id = NEW.session_id;
	END`

// createTriggerIncrementTotalEvents (re)creates the trg_increment_total_events
// trigger. Safe to call after a copy-swap that dropped the trigger — the
// IF NOT EXISTS clause makes it idempotent on already-migrated DBs.
func createTriggerIncrementTotalEvents(db *sql.DB) error {
	if _, err := db.Exec(triggerIncrementTotalEventsSQL); err != nil {
		return fmt.Errorf("create trg_increment_total_events: %w", err)
	}
	return nil
}

// stepSessionFamilyID adds the session_family_id column to sessions and creates
// an index for efficient family-based lookups. Also backfills existing rows so
// that any session without a family_id uses its own session_id as the family
// (each pre-existing session is its own family of one). Idempotent: the ALTER
// TABLE ADD COLUMN is swallowed by isDuplicateColumnError on re-run.
func stepSessionFamilyID(db *sql.DB) error {
	if _, err := db.Exec("ALTER TABLE sessions ADD COLUMN session_family_id TEXT"); err != nil {
		if !isDuplicateColumnError(err) {
			return fmt.Errorf("add session_family_id: %w", err)
		}
	}
	// Backfill: existing sessions without a family get their own session_id as
	// the family_id so they remain queryable by family without NULL handling.
	if _, err := db.Exec("UPDATE sessions SET session_family_id = session_id WHERE session_family_id IS NULL OR session_family_id = ''"); err != nil {
		return fmt.Errorf("backfill session_family_id: %w", err)
	}
	// Index for efficient family-based grouping queries.
	if _, err := db.Exec("CREATE INDEX IF NOT EXISTS idx_sessions_family ON sessions(session_family_id)"); err != nil {
		return fmt.Errorf("create idx_sessions_family: %w", err)
	}
	return nil
}

// stepSessionFiles creates the session_files table (claimless file-touch
// visibility, feat-793844bd slice-4) and its lookup indexes. Idempotent:
// CREATE TABLE/INDEX IF NOT EXISTS are no-ops on a DB that already has them
// (e.g. a fresh DB where stepCreateBaseTables already created the table from
// CreateAllTables). Legacy DBs at user_version<8 that pre-date the table get
// it created here.
func stepSessionFiles(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS session_files (
		id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL,
		file_path TEXT NOT NULL,
		operation TEXT NOT NULL DEFAULT 'unknown',
		first_seen DATETIME DEFAULT CURRENT_TIMESTAMP,
		last_seen DATETIME DEFAULT CURRENT_TIMESTAMP,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(session_id, file_path)
	)`); err != nil {
		return fmt.Errorf("create session_files: %w", err)
	}
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_session_files_session ON session_files(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_session_files_path ON session_files(file_path)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("create session_files index: %w", err)
		}
	}
	return nil
}

// stepFeatureFilesPathSeenIndex adds the composite (file_path, last_seen)
// index on feature_files (live file-overlap detection, Tier 1 / feat-b5fa9392).
// Only idx_feature_files_path on (file_path) existed before; the overlap query
// filters by file_path AND a recent last_seen window, so without the trailing
// last_seen column the planner range-scans every row for the path. Idempotent:
// CREATE INDEX IF NOT EXISTS is a no-op on a DB that already has it (e.g. a
// fresh DB where stepCreateIndexes created it from CreateAllIndexes).
func stepFeatureFilesPathSeenIndex(db *sql.DB) error {
	if _, err := db.Exec(
		`CREATE INDEX IF NOT EXISTS idx_feature_files_path_seen ON feature_files(file_path, last_seen)`,
	); err != nil {
		return fmt.Errorf("create idx_feature_files_path_seen: %w", err)
	}
	return nil
}

// stepRepairTriggerIncrementTotalEvents ensures trg_increment_total_events
// exists on every database, regardless of starting version.
//
// The bug-045124a6 fix recreated this trigger inside stepAgentEventsCopySwap
// (step 4), which only ran on DBs that had not yet completed step 4. Any DB
// already at user_version >= 4 that lost the trigger (e.g. due to manual DROP,
// or a buggy old migration) remained broken. This new step runs unconditionally
// on all DBs still below version 10 — including those already at version 9 —
// and (re)creates the trigger using CREATE TRIGGER IF NOT EXISTS, which is a
// no-op on DBs that still have it and a repair on DBs that lost it.
func stepRepairTriggerIncrementTotalEvents(db *sql.DB) error {
	return createTriggerIncrementTotalEvents(db)
}

// stepGateRecordsProfileSignature adds the guard-profile provenance columns to
// gate_records: profile_signature (the canonical guardprofile.Signature of the
// approved profile that supplied the guards, empty when autodetection was used)
// and guards_run (a JSON array of the guard names that ran). Both are NEW
// columns distinct from the existing record-integrity `signature` MAC column.
// Idempotent: ALTER TABLE ADD COLUMN is swallowed by isDuplicateColumnError on
// re-run, and a fresh DB created by CreateAllTables already has both columns.
func stepGateRecordsProfileSignature(db *sql.DB) error {
	for _, stmt := range []string{
		`ALTER TABLE gate_records ADD COLUMN profile_signature TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE gate_records ADD COLUMN guards_run TEXT NOT NULL DEFAULT '[]'`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			if !isDuplicateColumnError(err) {
				return fmt.Errorf("add gate_records column (%s): %w", stmt, err)
			}
		}
	}
	return nil
}

// stepArchCards creates the arch_cards read-index table and its indexes.
// This is a derived read index — the canonical store is .wipnote/arch/*.md.
// Idempotent: CREATE TABLE/INDEX IF NOT EXISTS are no-ops on a DB that already
// has the table (e.g. a fresh DB where stepCreateBaseTables ran first).
func stepArchCards(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS arch_cards (
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
	)`); err != nil {
		return fmt.Errorf("create arch_cards: %w", err)
	}
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_arch_cards_kind ON arch_cards(kind)`,
		`CREATE INDEX IF NOT EXISTS idx_arch_cards_retired ON arch_cards(retired)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("create arch_cards index: %w", err)
		}
	}
	return nil
}

// stepRecapsTable creates the recaps read-index table and its indexes. Recaps
// are committed HTML artifacts under .wipnote/recaps/; this table is a derived
// read index (the HTML stays canonical). Recaps carry a distinct shape from
// work items — grounding scope plus a source range/session — so they get a
// dedicated table rather than extending the features index.
// Idempotent: CREATE TABLE/INDEX IF NOT EXISTS are no-ops on a DB that already
// has the table (e.g. a fresh DB where stepCreateBaseTables ran first).
func stepRecapsTable(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS recaps (
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
	)`); err != nil {
		return fmt.Errorf("create recaps: %w", err)
	}
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_recaps_kind ON recaps(kind)`,
		`CREATE INDEX IF NOT EXISTS idx_recaps_work_item ON recaps(work_item_id)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("create recaps index: %w", err)
		}
	}
	return nil
}

func stepSessionExecContext(db *sql.DB) error {
	for _, stmt := range []string{
		`ALTER TABLE sessions ADD COLUMN exec_worktree_path TEXT`,
		`ALTER TABLE sessions ADD COLUMN branch TEXT`,
		`ALTER TABLE sessions ADD COLUMN harness TEXT`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			if !isDuplicateColumnError(err) {
				return fmt.Errorf("add session exec-context column (%s): %w", stmt, err)
			}
		}
	}
	return nil
}

func stepSessionHandoffFields(db *sql.DB) error {
	for _, stmt := range []string{
		`ALTER TABLE sessions ADD COLUMN last_user_query_at DATETIME`,
		`ALTER TABLE sessions ADD COLUMN last_user_query TEXT`,
		`ALTER TABLE sessions ADD COLUMN handoff_notes TEXT`,
		`ALTER TABLE sessions ADD COLUMN recommended_next TEXT`,
		`ALTER TABLE sessions ADD COLUMN blockers JSON`,
		`ALTER TABLE sessions ADD COLUMN recommended_context JSON`,
		`ALTER TABLE sessions ADD COLUMN continued_from TEXT`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			if !isDuplicateColumnError(err) {
				return fmt.Errorf("add session handoff column (%s): %w", stmt, err)
			}
		}
	}
	return nil
}

// stepPlanFeedbackAnnotationState adds the block-anchor + two-axis annotation
// state columns to plan_feedback (slice-8). Annotations reuse the existing
// plan_feedback table (action='annotation') rather than a new table: the FK to
// plan_id and the per-plan SSE/read loop are already wired here. The two axes —
// consumed (an agent has ingested the note) and resolved (the note has been
// addressed) — are independent, and resolution_target routes the note to
// 'agent' or 'human'. All columns are NULL-able / default-zero so existing rows
// and existing writers (approve/comment/answer) are unaffected.
func stepPlanFeedbackAnnotationState(db *sql.DB) error {
	// Legacy/partial DBs may not yet have plan_feedback (e.g. a DB seeded before
	// the table existed). The base schema creates it WITH these columns, so when
	// the table is absent here there is nothing to ALTER — skip rather than fail.
	var name string
	err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='plan_feedback'`,
	).Scan(&name)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check plan_feedback existence: %w", err)
	}
	for _, stmt := range []string{
		`ALTER TABLE plan_feedback ADD COLUMN anchor TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE plan_feedback ADD COLUMN consumed INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE plan_feedback ADD COLUMN resolved INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE plan_feedback ADD COLUMN resolution_target TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			if !isDuplicateColumnError(err) {
				return fmt.Errorf("add plan_feedback annotation column (%s): %w", stmt, err)
			}
		}
	}
	return nil
}

// stepBackfillTotalEvents recomputes sessions.total_events for every session
// from the actual agent_events rows. This repairs DBs that recorded agent_events
// while trg_increment_total_events was absent (e.g. after step 10 recreated the
// trigger but before this step ran) — their total_events counts remained stale.
//
// The backfill exactly mirrors what the trigger does: for each inserted
// agent_events row the trigger increments total_events by 1 where
// agent_events.session_id = sessions.session_id (no additional filter).
// The COUNT(*) correlated subquery reproduces that accumulation in bulk.
//
// Idempotent: recomputing an already-correct count yields the same value.
// Safe on a fresh DB (no sessions → zero rows updated).
func stepBackfillTotalEvents(db *sql.DB) error {
	_, err := db.Exec(`UPDATE sessions
		SET total_events = (
			SELECT COUNT(*)
			FROM agent_events
			WHERE agent_events.session_id = sessions.session_id
		)`)
	if err != nil {
		return fmt.Errorf("backfill total_events: %w", err)
	}
	return nil
}
