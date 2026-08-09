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
const currentSchemaVersion = 23

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
	{
		version: 18,
		name:    "018_git_commits_composite_key",
		apply:   stepGitCommitsCompositeKey,
	},
	{
		version: 19,
		name:    "019_otel_signals_attribution_columns",
		apply:   stepOtelSignalsAttributionColumns,
	},
	{
		version: 20,
		name:    "020_gate_records_record_id",
		apply:   stepGateRecordsRecordID,
	},
	{
		version: 21,
		name:    "021_claim_episodes_table",
		apply:   stepClaimEpisodesTable,
	},
	{
		version: 22,
		name:    "022_otel_span_id_index_not_unique",
		apply:   stepOtelSpanIDIndexNotUnique,
	},
	{
		version: 23,
		name:    "023_otel_session_kind_index",
		apply:   stepOtelSessionKindIndex,
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

// RunMigrations applies any pending schema migrations to an ALREADY-OPEN
// *sql.DB, without going through Open's own connection/pragma setup. It
// exists for callers that must own their own connection-opening logic (their
// own DSN pragmas, pool sizing, or pinned-connection semantics) but still
// need the same version-gated migration guarantee every db.Open caller gets
// for free — see receiver.NewWriter (bug-286ce8f7), which used to open its
// DB handle via a raw sql.Open and never ran migrations at all, silently
// depending on some OTHER writer having already migrated the same file
// first. Safe to call on every open: version-gated and idempotent, exactly
// like the internal runMigrations Open wraps.
func RunMigrations(db *sql.DB) error {
	return RetryOnBusy(DefaultBusyBackoff, func() error {
		return runMigrations(db)
	})
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

// stepGateRecordsRecordID adds gate_records.record_id — the id of the canonical
// .wipnote/gate-ledger.html row this index row projects (feat-0e5ca43e).
//
// The index is now derived: reindex replays the ledger through INSERT OR IGNORE
// keyed on record_id, so a purged cache rebuilds and a warm cache is a no-op.
// The unique index is PARTIAL because existing rows predate the ledger and all
// carry an empty record_id — constraining them would fail the migration on any
// DB with more than one historical gate run. They keep their rows and are given
// canonical ids by the backfill pass, which stamps record_id as it writes each.
//
// Idempotent: ALTER TABLE ADD COLUMN is swallowed by isDuplicateColumnError on
// re-run, and a fresh DB created by CreateAllTables already has the column.
//
// A MISSING gate_records table is not an error. stepCreateBaseTables is version
// 1, so it never re-runs on a database seeded at a later user_version, and such a
// database can legitimately reach this step with the table absent. There is
// nothing to alter and nothing to lose: CreateAllTables already declares
// record_id, so whenever the table is finally created it arrives with the column.
func stepGateRecordsRecordID(db *sql.DB) error {
	var name string
	err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='gate_records'`,
	).Scan(&name)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check gate_records existence: %w", err)
	}

	if _, err := db.Exec(`ALTER TABLE gate_records ADD COLUMN record_id TEXT NOT NULL DEFAULT ''`); err != nil {
		if !isDuplicateColumnError(err) {
			return fmt.Errorf("add gate_records.record_id: %w", err)
		}
	}
	if _, err := db.Exec(
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_gate_records_record_id ON gate_records(record_id) WHERE record_id != ''`,
	); err != nil {
		return fmt.Errorf("create gate_records record_id index: %w", err)
	}
	return nil
}

// stepClaimEpisodesTable creates the claim_episodes read-index table and its
// three lookup indexes (bug-8af46da3).
//
// claim_episodes was declared in CreateAllTables (schema.go) — the function
// stepCreateBaseTables (version 1) calls — but never given a companion
// versioned step. stepCreateBaseTables only runs for a database going from
// user_version 0 to current; any database that had already migrated past
// version 1 BEFORE claim_episodes was added to CreateAllTables never runs
// that function again and therefore never gets the table, no matter which
// binary or entry point (serve, hooks, reindex) opens it afterward — the
// same defect shape as bug-286ce8f7 (see stepOtelSignalsAttributionColumns)
// and the gate_records fix in stepGateRecordsRecordID. Confirmed live: the
// table did not exist on an index already at the (then-)current schema
// version, so every claim-episode reindex pass failed with a missing-table
// error and the claim ledger was never once projected into the index it
// feeds.
//
// Idempotent: CREATE TABLE/INDEX IF NOT EXISTS are no-ops on a DB that
// already has the table (e.g. a fresh DB where stepCreateBaseTables ran
// first — both this step and CreateAllTables declare the same DDL, matching
// the existing gate_records/session_files/arch_cards/recaps pattern where a
// table lives in both the create-all path and its own dedicated step).
func stepClaimEpisodesTable(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS claim_episodes (
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
	)`); err != nil {
		return fmt.Errorf("create claim_episodes: %w", err)
	}
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_claim_episodes_agent_start ON claim_episodes(agent_id, started_at)`,
		`CREATE INDEX IF NOT EXISTS idx_claim_episodes_session_agent ON claim_episodes(session_id, agent_id, started_at)`,
		`CREATE INDEX IF NOT EXISTS idx_claim_episodes_work_item ON claim_episodes(work_item_id, started_at)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("create claim_episodes index: %w", err)
		}
	}
	return nil
}

// stepOtelSpanIDIndexNotUnique replaces the UNIQUE index on
// otel_signals(span_id) with a plain one, and flags the derived OTel index for
// a full re-ingest (bug-0fc17d53).
//
// WHY THE UNIQUE INDEX WAS WRONG: a span_id identifies a span, not a signal.
// Every OTel log record and metric correlated to a span carries that span's
// id, and Claude Code emits many of them against a single interaction span.
// The unique index therefore permitted at most one row per span, and because
// the writer inserts with INSERT OR IGNORE, every later signal sharing a
// span_id was discarded with no error, no log line, and a checkpoint that
// advanced as if the data had been stored. On the corpus this was measured
// against that was 86,758 of 155,855 signals.
//
// WHY THE INDEX DROP ALONE FIXES NOTHING: the NDJSON indexer records a byte
// offset per shard, and those offsets already sit at end-of-file on every
// existing install. Dropping the index stops future loss but leaves the
// historical hole untouched — a fresh database would look perfect while every
// upgraded one stayed 56% empty. So this step also sets the re-ingest marker
// that indexer.EnsureReingest consumes, which clears the per-shard checkpoints
// exactly once so the canonical NDJSON is replayed in full.
//
// Replay is safe and non-destructive: inserts are keyed on signal_id, so rows
// already present are skipped, and rows whose NDJSON has since been swept by
// retention are left alone rather than deleted and not re-derived.
func stepOtelSpanIDIndexNotUnique(db *sql.DB) error {
	// DROP INDEX IF EXISTS is safe on a database without the table.
	if _, err := db.Exec(`DROP INDEX IF EXISTS idx_otel_span_id_unique`); err != nil {
		return fmt.Errorf("drop idx_otel_span_id_unique: %w", err)
	}

	// CREATE INDEX is not safe on a missing table, and a partially-built
	// legacy database can reach this step without one — same guard the
	// gate_records and plan_feedback steps use.
	indexes := map[string]string{
		"otel_signals": `CREATE INDEX IF NOT EXISTS idx_otel_span_id ON otel_signals(span_id) WHERE span_id IS NOT NULL`,
		// Also declared in CreateOtelIndexes. It previously lived in the
		// create-all path, which never reaches a database past version 1, so
		// create it here too rather than assume every install has it.
		"pending_subagent_starts": `CREATE INDEX IF NOT EXISTS idx_pending_subagent_session ON pending_subagent_starts(session_id)`,
	}
	for table, stmt := range indexes {
		exists, err := tableExists(db, table)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("create index on %s: %w", table, err)
		}
	}

	// The marker lives in metadata; a partially-built legacy database may not
	// have that table either. Nothing to recover on such a database anyway.
	hasMetadata, err := tableExists(db, "metadata")
	if err != nil {
		return err
	}
	if hasMetadata {
		if err := SetOtelReingestRequired(db, "022_otel_span_id_index_not_unique"); err != nil {
			return fmt.Errorf("flag otel re-ingest: %w", err)
		}
	}
	return nil
}

// stepOtelSessionKindIndex adds the index that makes pruneMetricSignals
// selective instead of a per-session table walk (bug-129bf18d).
//
// pruneMetricSignals runs once per metric signal. Its DELETE and its
// keep-the-newest-N subquery both filter on (session_id, kind), but only
// session_id was indexed, so each call walked every row belonging to the
// session — including the non-metric rows, which are the large majority.
// Cost per metric signal therefore grew with session size, making ingest
// quadratic. That is what made the bug-0fc17d53 recovery replay expensive.
//
// The DESC columns are not decoration: they match the subquery's ORDER BY, so
// the plan becomes a covering-index search with no temporary B-tree. Measured
// on a real shard, per-call cost fell 33.8ms -> 1.7ms, and replaying 28,000
// real signals fell from 63.6s to 11.6s. Without the DESC tail the same index
// still helps but only reaches 3.4ms per call and 15.2s overall.
//
// Deliberately NOT added here: an index on (session_id, canonical) for
// tryReattributeParent's Strategy B scan. Its query plan improves exactly as
// you would predict, and end-to-end it made replay 19-22% SLOWER, because the
// scan fires for only about 1% of spans on real data (a span's parent is
// rarely an interaction span) while the index is maintained on every insert.
// Do not re-add it on the strength of an EXPLAIN alone.
func stepOtelSessionKindIndex(db *sql.DB) error {
	exists, err := tableExists(db, "otel_signals")
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if _, err := db.Exec(
		`CREATE INDEX IF NOT EXISTS idx_otel_session_kind_ts
		 ON otel_signals(session_id, kind, ts_micros DESC, created_at DESC, signal_id DESC)`,
	); err != nil {
		return fmt.Errorf("create idx_otel_session_kind_ts: %w", err)
	}
	return nil
}

// tableExists reports whether a table of that name is present.
func tableExists(db *sql.DB, table string) (bool, error) {
	var name string
	err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table,
	).Scan(&name)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check %s existence: %w", table, err)
	}
	return true, nil
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

// stepGitCommitsCompositeKey widens git_commits' primary key from
// (commit_hash, session_id) to (commit_hash, session_id, feature_id).
//
// Why: a single commit can legitimately name multiple work items (e.g. a
// trailer block "Refs: feat-a, feat-b", or two IDs in one subject line).
// Every ingestion path (trailer scan, hook capture, bulk backfill) uses
// INSERT OR IGNORE keyed on the primary key. Under the old two-column key,
// the trailer-ingest path writes every row under a single constant
// session_id ("trailer-ingest"), so the second and later feature_id for the
// same commit collided with the first insert and was silently dropped
// (bug-3bf05d49).
//
// feature_id is normalized to the empty string (never NULL) so it can safely sit in a
// composite key: SQLite's PRIMARY KEY does not imply NOT NULL, and two NULLs
// are never equal under a UNIQUE index — a nullable PK column would silently
// defeat INSERT OR IGNORE's de-duplication for unattributed commits.
func stepGitCommitsCompositeKey(db *sql.DB) error {
	var currentSQL string
	err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='git_commits'`,
	).Scan(&currentSQL)
	if err != nil {
		// Table doesn't exist yet -- CreateAllTables will create it correctly.
		return nil
	}
	if strings.Contains(currentSQL, "PRIMARY KEY (commit_hash, session_id, feature_id)") {
		return nil // already migrated
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec(`
		CREATE TABLE git_commits_new (
			commit_hash TEXT NOT NULL,
			session_id TEXT NOT NULL,
			feature_id TEXT NOT NULL DEFAULT '',
			tool_event_id TEXT,
			message TEXT,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (commit_hash, session_id, feature_id)
		)`); err != nil {
		return fmt.Errorf("create git_commits_new: %w", err)
	}

	// The old PK (commit_hash, session_id) guarantees at most one row per pair
	// in the source table, so this copy cannot collide against the new,
	// wider PK -- it only unlocks capacity for future inserts. It does NOT
	// recover feature_ids already dropped by the old collision; re-run
	// `wipnote reindex --full` afterwards to re-derive those from source.
	if _, err := tx.Exec(`
		INSERT INTO git_commits_new
			(commit_hash, session_id, feature_id, tool_event_id, message, timestamp)
		SELECT commit_hash, session_id, COALESCE(feature_id, ''), tool_event_id, message, timestamp
		FROM git_commits`); err != nil {
		return fmt.Errorf("copy git_commits data: %w", err)
	}

	if _, err := tx.Exec(`DROP TABLE git_commits`); err != nil {
		return fmt.Errorf("drop old git_commits: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE git_commits_new RENAME TO git_commits`); err != nil {
		return fmt.Errorf("rename git_commits_new: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit git_commits migration: %w", err)
	}

	// Reinstall idx_git_commits_feature, dropped by DROP TABLE above.
	if err := CreateAllIndexes(db); err != nil {
		return fmt.Errorf("reinstall indexes after git_commits migration: %w", err)
	}
	return nil
}

// stepOtelSignalsAttributionColumns adds the otel_signals.feature_id and
// otel_signals.agent_id columns (and their partial indexes) as a properly
// versioned step, correcting bug-286ce8f7 (filed against team-lead's
// bug-6f0f0b3a): both columns used to be added via an idempotent ALTER
// embedded directly in CreateOtelTables, which core/db is only reachable
// from step version 1 (stepCreateBaseTables). A step at version 1 runs
// exactly once -- the first time a database goes from user_version 0 to
// current -- and never again once a database has reached
// currentSchemaVersion. Any column added inside CreateOtelTables AFTER a
// given database was already fully migrated therefore never applies to
// that database, no matter which binary or which entry point (serve,
// hooks, reindex) opens it afterward. This bit feature_id for roughly four
// months before an incidental full-DB rebuild happened to apply it, and bit
// agent_id (feat-be696acc) immediately, because no such rebuild had
// happened yet -- confirmed live against this repo's own dev database
// (user_version already at 18, feature_id present, agent_id absent).
//
// Both ALTERs are safe to re-run here even on a database that already has
// one or both columns (from an incidental full rebuild, as above): duplicate
// column errors are swallowed the same way every other column-migration step
// in this file does it.
//
// Legacy/partial DBs may not have otel_signals at all yet (a hand-seeded test
// fixture that jumps straight to a mid-chain user_version, skipping step 1).
// The base schema (CreateOtelTables, step 1) creates the table WITH these
// columns for any database that runs the full chain, so when the table is
// absent here there is nothing to ALTER — skip rather than fail, matching
// stepPlanFeedbackAnnotationState and stepGitCommitsCompositeKey.
func stepOtelSignalsAttributionColumns(db *sql.DB) error {
	var name string
	err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='otel_signals'`,
	).Scan(&name)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check otel_signals existence: %w", err)
	}

	addCols := []string{
		`ALTER TABLE otel_signals ADD COLUMN feature_id TEXT`,
		`ALTER TABLE otel_signals ADD COLUMN agent_id TEXT`,
	}
	for _, stmt := range addCols {
		if _, err := db.Exec(stmt); err != nil {
			if !isDuplicateColumnError(err) {
				return fmt.Errorf("add column (%s): %w", stmt, err)
			}
		}
	}
	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_otel_feature_ts ON otel_signals(feature_id, ts_micros) WHERE feature_id IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_otel_agent_ts ON otel_signals(agent_id, ts_micros) WHERE agent_id IS NOT NULL`,
	}
	for _, stmt := range indexes {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("create index (%s): %w", stmt, err)
		}
	}
	return nil
}
