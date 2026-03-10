"""
HtmlGraph Database DDL - Data Definition Language

All DDL operations consolidated into one module:
- CREATE TABLE statements (15 tables)
- CREATE INDEX statements (performance optimization)
- ALTER TABLE migrations (schema evolution)

Called by HtmlGraphDB.create_tables() in schema.py.
"""

import logging
import sqlite3

from htmlgraph.search import create_fts_tables

logger = logging.getLogger(__name__)


# ---------------------------------------------------------------------------
# CREATE TABLE
# ---------------------------------------------------------------------------


def create_all_tables(cursor: sqlite3.Cursor) -> None:
    """
    Create all HtmlGraph database tables.

    Args:
        cursor: SQLite cursor for executing queries
    """
    # 1. AGENT_EVENTS TABLE - Core event tracking
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS agent_events (
            event_id TEXT PRIMARY KEY,
            agent_id TEXT NOT NULL,
            event_type TEXT NOT NULL CHECK(
                event_type IN ('tool_call', 'tool_result', 'error', 'delegation',
                               'completion', 'start', 'end', 'check_point', 'task_delegation',
                               'teammate_idle', 'task_completed', 'user_input')
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
            created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
            updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
            FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE CASCADE ON UPDATE CASCADE,
            FOREIGN KEY (parent_event_id) REFERENCES agent_events(event_id) ON DELETE SET NULL ON UPDATE CASCADE,
            FOREIGN KEY (feature_id) REFERENCES features(id) ON DELETE SET NULL ON UPDATE CASCADE
        )
    """)

    # 2. FEATURES TABLE - Work items (features, bugs, spikes, chores, epics)
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS features (
            id TEXT PRIMARY KEY,
            type TEXT NOT NULL CHECK(
                type IN ('feature', 'bug', 'spike', 'chore', 'epic', 'task')
            ),
            title TEXT NOT NULL,
            description TEXT,
            status TEXT NOT NULL DEFAULT 'todo' CHECK(
                status IN ('todo', 'in-progress', 'blocked', 'done', 'active', 'ended', 'stale')
            ),
            priority TEXT DEFAULT 'medium' CHECK(
                priority IN ('low', 'medium', 'high', 'critical')
            ),
            assigned_to TEXT,
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
        )
    """)

    # 3. SESSIONS TABLE - Agent sessions with metrics
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS sessions (
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
                status IN ('active', 'completed', 'paused', 'failed')
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
            FOREIGN KEY (parent_session_id) REFERENCES sessions(session_id) ON DELETE SET NULL ON UPDATE CASCADE,
            FOREIGN KEY (parent_event_id) REFERENCES agent_events(event_id) ON DELETE SET NULL ON UPDATE CASCADE,
            FOREIGN KEY (continued_from) REFERENCES sessions(session_id) ON DELETE SET NULL ON UPDATE CASCADE
        )
    """)

    # 4. TRACKS TABLE - Multi-feature initiatives
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS tracks (
            id TEXT PRIMARY KEY,
            type TEXT DEFAULT 'track',
            title TEXT NOT NULL,
            description TEXT,
            priority TEXT DEFAULT 'medium' CHECK(
                priority IN ('low', 'medium', 'high', 'critical')
            ),
            status TEXT NOT NULL DEFAULT 'todo' CHECK(
                status IN ('todo', 'in-progress', 'blocked', 'done', 'active', 'ended', 'stale')
            ),
            created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
            updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
            completed_at DATETIME,
            features JSON,
            metadata JSON
        )
    """)

    # 5. AGENT_COLLABORATION TABLE - Handoffs and parallel work
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS agent_collaboration (
            handoff_id TEXT PRIMARY KEY,
            from_agent TEXT NOT NULL,
            to_agent TEXT NOT NULL,
            timestamp DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
            feature_id TEXT,
            session_id TEXT,
            handoff_type TEXT CHECK(
                handoff_type IN ('delegation', 'parallel', 'sequential', 'fallback')
            ),
            status TEXT DEFAULT 'pending' CHECK(
                status IN ('pending', 'accepted', 'rejected', 'completed', 'failed')
            ),
            reason TEXT,
            context JSON,
            result JSON,
            FOREIGN KEY (feature_id) REFERENCES features(id),
            FOREIGN KEY (session_id) REFERENCES sessions(session_id)
        )
    """)

    # 6. GRAPH_EDGES TABLE - Flexible relationship tracking
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS graph_edges (
            edge_id TEXT PRIMARY KEY,
            from_node_id TEXT NOT NULL,
            from_node_type TEXT NOT NULL,
            to_node_id TEXT NOT NULL,
            to_node_type TEXT NOT NULL,
            relationship_type TEXT NOT NULL,
            weight REAL DEFAULT 1.0,
            created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
            metadata JSON
        )
    """)

    # 7. EVENT_LOG_ARCHIVE TABLE - Historical event queries
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS event_log_archive (
            archive_id TEXT PRIMARY KEY,
            session_id TEXT NOT NULL,
            agent_id TEXT NOT NULL,
            event_date DATE NOT NULL,
            event_count INTEGER DEFAULT 0,
            total_tokens INTEGER DEFAULT 0,
            summary TEXT,
            archived_at DATETIME DEFAULT CURRENT_TIMESTAMP,
            FOREIGN KEY (session_id) REFERENCES sessions(session_id)
        )
    """)

    # 8. LIVE_EVENTS TABLE - Real-time event streaming buffer
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS live_events (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            event_type TEXT NOT NULL,
            event_data TEXT NOT NULL,
            parent_event_id TEXT,
            session_id TEXT,
            spawner_type TEXT,
            created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
            broadcast_at TIMESTAMP
        )
    """)

    # 9. TOOL_TRACES TABLE - Detailed tool execution tracing
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS tool_traces (
            tool_use_id TEXT PRIMARY KEY,
            trace_id TEXT NOT NULL,
            session_id TEXT NOT NULL,
            tool_name TEXT NOT NULL,
            tool_input JSON,
            tool_output JSON,
            start_time TIMESTAMP NOT NULL,
            end_time TIMESTAMP,
            duration_ms INTEGER,
            status TEXT NOT NULL DEFAULT 'started' CHECK(
                status IN ('started', 'completed', 'failed', 'timeout', 'cancelled')
            ),
            error_message TEXT,
            parent_tool_use_id TEXT,
            created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
            FOREIGN KEY (session_id) REFERENCES sessions(session_id),
            FOREIGN KEY (parent_tool_use_id) REFERENCES tool_traces(tool_use_id)
        )
    """)

    # 10. HANDOFF_TRACKING TABLE - Track handoff effectiveness
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS handoff_tracking (
            handoff_id TEXT PRIMARY KEY,
            from_session_id TEXT NOT NULL,
            to_session_id TEXT,
            items_in_context INTEGER DEFAULT 0,
            items_accessed INTEGER DEFAULT 0,
            time_to_resume_seconds INTEGER DEFAULT 0,
            user_rating INTEGER CHECK(user_rating BETWEEN 1 AND 5),
            created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
            resumed_at DATETIME,
            FOREIGN KEY (from_session_id) REFERENCES sessions(session_id) ON DELETE CASCADE,
            FOREIGN KEY (to_session_id) REFERENCES sessions(session_id) ON DELETE SET NULL
        )
    """)

    # 11. COST_EVENTS TABLE - Real-time cost monitoring & alerts
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS cost_events (
            event_id TEXT PRIMARY KEY,
            session_id TEXT NOT NULL,
            timestamp DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

            -- Token tracking
            tool_name TEXT,
            model TEXT,
            input_tokens INTEGER DEFAULT 0,
            output_tokens INTEGER DEFAULT 0,
            total_tokens INTEGER DEFAULT 0,
            cost_usd REAL DEFAULT 0.0,

            -- Agent tracking
            agent_id TEXT,
            subagent_type TEXT,

            -- Alert tracking
            alert_type TEXT,
            message TEXT,
            current_cost_usd REAL,
            budget_usd REAL,
            predicted_cost_usd REAL,
            severity TEXT,
            acknowledged BOOLEAN DEFAULT 0,

            created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
            FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE CASCADE
        )
    """)

    # 12. AGENT_PRESENCE TABLE - Cross-Agent Presence Tracking
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS agent_presence (
            agent_id TEXT PRIMARY KEY,
            status TEXT NOT NULL DEFAULT 'offline' CHECK(
                status IN ('active', 'idle', 'offline')
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
        )
    """)

    # 13. OFFLINE_EVENTS TABLE - Offline-First Merge
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS offline_events (
            event_id TEXT PRIMARY KEY,
            agent_id TEXT NOT NULL,
            resource_id TEXT NOT NULL,
            resource_type TEXT NOT NULL,
            operation TEXT NOT NULL CHECK(
                operation IN ('create', 'update', 'delete')
            ),
            timestamp TEXT NOT NULL,
            payload TEXT NOT NULL,
            status TEXT DEFAULT 'local_only' CHECK(
                status IN ('local_only', 'synced', 'conflict', 'resolved')
            ),
            created_at TEXT DEFAULT CURRENT_TIMESTAMP
        )
    """)

    # 14. CONFLICT_LOG TABLE - Conflict tracking and resolution
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS conflict_log (
            conflict_id TEXT PRIMARY KEY,
            local_event_id TEXT NOT NULL,
            remote_event_id TEXT,
            resource_id TEXT NOT NULL,
            conflict_type TEXT NOT NULL,
            local_timestamp TEXT NOT NULL,
            remote_timestamp TEXT NOT NULL,
            resolution_strategy TEXT NOT NULL,
            resolution TEXT,
            status TEXT DEFAULT 'pending_review' CHECK(
                status IN ('pending_review', 'resolved')
            ),
            created_at TEXT DEFAULT CURRENT_TIMESTAMP,
            FOREIGN KEY (local_event_id) REFERENCES offline_events(event_id) ON DELETE CASCADE
        )
    """)

    # 15. SYNC_OPERATIONS TABLE - Git sync tracking
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS sync_operations (
            sync_id TEXT PRIMARY KEY,
            operation TEXT NOT NULL CHECK(operation IN ('push', 'pull')),
            status TEXT NOT NULL CHECK(
                status IN ('idle', 'pushing', 'pulling', 'success', 'error', 'conflict')
            ),
            timestamp DATETIME NOT NULL,
            files_changed INTEGER DEFAULT 0,
            conflicts TEXT,
            message TEXT,
            hostname TEXT,
            created_at DATETIME DEFAULT CURRENT_TIMESTAMP
        )
    """)

    # 17. OPLOG TABLE - Canonical local-first sync transport
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS oplog (
            seq INTEGER PRIMARY KEY AUTOINCREMENT,
            entry_id TEXT NOT NULL UNIQUE,
            idempotency_key TEXT NOT NULL UNIQUE,
            entity_type TEXT NOT NULL,
            entity_id TEXT NOT NULL,
            op TEXT NOT NULL CHECK(
                op IN ('create', 'update', 'delete', 'upsert', 'patch')
            ),
            payload TEXT NOT NULL,
            actor TEXT NOT NULL,
            ts TEXT NOT NULL,
            field_mask TEXT,
            session_id TEXT,
            created_at TEXT DEFAULT CURRENT_TIMESTAMP
        )
    """)

    # 18. SYNC_CURSORS TABLE - Per-consumer cursor tracking
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS sync_cursors (
            consumer_id TEXT PRIMARY KEY,
            last_seen_seq INTEGER NOT NULL DEFAULT 0,
            last_acked_seq INTEGER NOT NULL DEFAULT 0,
            updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
            CHECK(last_seen_seq >= 0),
            CHECK(last_acked_seq >= 0),
            CHECK(last_acked_seq <= last_seen_seq)
        )
    """)

    # 19. SYNC_CONFLICTS TABLE - Deterministic conflict records (LWW policy)
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS sync_conflicts (
            conflict_id TEXT PRIMARY KEY,
            local_entry_id TEXT NOT NULL,
            remote_entry_id TEXT NOT NULL,
            entity_type TEXT NOT NULL,
            entity_id TEXT NOT NULL,
            field_set TEXT NOT NULL,
            policy TEXT NOT NULL,
            resolution TEXT NOT NULL,
            status TEXT NOT NULL DEFAULT 'resolved' CHECK(
                status IN ('pending_review', 'resolved')
            ),
            created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
            FOREIGN KEY (local_entry_id) REFERENCES oplog(entry_id) ON DELETE CASCADE,
            FOREIGN KEY (remote_entry_id) REFERENCES oplog(entry_id) ON DELETE CASCADE
        )
    """)

    # 20. FTS5 VIRTUAL TABLES - Full-text search (sessions_fts, events_fts)
    create_fts_tables(cursor)


# ---------------------------------------------------------------------------
# CREATE INDEX
# ---------------------------------------------------------------------------


def create_all_indexes(cursor: sqlite3.Cursor) -> None:
    """
    Create all performance indexes on HtmlGraph database tables.

    OPTIMIZATION STRATEGY:
    - Composite indexes for most common query patterns (session+timestamp, agent+timestamp)
    - Single-column indexes for individual filters and sorts
    - DESC indexes for reverse-order queries (e.g., activity feed, timelines)
    - Covering indexes where beneficial to reduce table lookups

    Args:
        cursor: SQLite cursor for executing queries
    """
    indexes = [
        # agent_events indexes
        "CREATE INDEX IF NOT EXISTS idx_agent_events_session_ts_desc ON agent_events(session_id, timestamp DESC)",
        "CREATE INDEX IF NOT EXISTS idx_agent_events_agent_ts_desc ON agent_events(agent_id, timestamp DESC)",
        "CREATE INDEX IF NOT EXISTS idx_agent_events_agent ON agent_events(agent_id)",
        "CREATE INDEX IF NOT EXISTS idx_agent_events_type ON agent_events(event_type)",
        "CREATE INDEX IF NOT EXISTS idx_agent_events_parent_event ON agent_events(parent_event_id)",
        "CREATE INDEX IF NOT EXISTS idx_agent_events_task_delegation ON agent_events(event_type, subagent_type, timestamp DESC)",
        "CREATE INDEX IF NOT EXISTS idx_agent_events_session_tool ON agent_events(session_id, tool_name)",
        "CREATE INDEX IF NOT EXISTS idx_agent_events_timestamp ON agent_events(timestamp DESC)",
        "CREATE INDEX IF NOT EXISTS idx_agent_events_claude_task_id ON agent_events(claude_task_id)",
        # features indexes
        "CREATE INDEX IF NOT EXISTS idx_features_status_priority ON features(status, priority DESC, created_at DESC)",
        "CREATE INDEX IF NOT EXISTS idx_features_track_priority ON features(track_id, priority DESC, created_at DESC)",
        "CREATE INDEX IF NOT EXISTS idx_features_assigned ON features(assigned_to)",
        "CREATE INDEX IF NOT EXISTS idx_features_parent ON features(parent_feature_id)",
        "CREATE INDEX IF NOT EXISTS idx_features_type ON features(type)",
        "CREATE INDEX IF NOT EXISTS idx_features_created ON features(created_at DESC)",
        # sessions indexes
        "CREATE INDEX IF NOT EXISTS idx_sessions_agent_created ON sessions(agent_assigned, created_at DESC)",
        "CREATE INDEX IF NOT EXISTS idx_sessions_status_created ON sessions(status, created_at DESC)",
        "CREATE INDEX IF NOT EXISTS idx_sessions_parent ON sessions(parent_session_id, created_at DESC)",
        "CREATE INDEX IF NOT EXISTS idx_sessions_created ON sessions(created_at DESC)",
        # tracks indexes
        "CREATE INDEX IF NOT EXISTS idx_tracks_status_created ON tracks(status, created_at DESC)",
        "CREATE INDEX IF NOT EXISTS idx_tracks_priority ON tracks(priority DESC)",
        # collaboration indexes
        "CREATE INDEX IF NOT EXISTS idx_collaboration_session ON agent_collaboration(session_id, timestamp DESC)",
        "CREATE INDEX IF NOT EXISTS idx_collaboration_from_agent ON agent_collaboration(from_agent)",
        "CREATE INDEX IF NOT EXISTS idx_collaboration_to_agent ON agent_collaboration(to_agent)",
        "CREATE INDEX IF NOT EXISTS idx_collaboration_agents ON agent_collaboration(from_agent, to_agent)",
        "CREATE INDEX IF NOT EXISTS idx_collaboration_feature ON agent_collaboration(feature_id)",
        "CREATE INDEX IF NOT EXISTS idx_collaboration_handoff_type ON agent_collaboration(handoff_type, timestamp DESC)",
        # graph_edges indexes
        "CREATE INDEX IF NOT EXISTS idx_edges_from ON graph_edges(from_node_id)",
        "CREATE INDEX IF NOT EXISTS idx_edges_to ON graph_edges(to_node_id)",
        "CREATE INDEX IF NOT EXISTS idx_edges_type ON graph_edges(relationship_type)",
        # tool_traces indexes
        "CREATE INDEX IF NOT EXISTS idx_tool_traces_trace_id ON tool_traces(trace_id, start_time DESC)",
        "CREATE INDEX IF NOT EXISTS idx_tool_traces_session ON tool_traces(session_id, start_time DESC)",
        "CREATE INDEX IF NOT EXISTS idx_tool_traces_tool_name ON tool_traces(tool_name, status)",
        "CREATE INDEX IF NOT EXISTS idx_tool_traces_status ON tool_traces(status, start_time DESC)",
        "CREATE INDEX IF NOT EXISTS idx_tool_traces_start_time ON tool_traces(start_time DESC)",
        # live_events indexes
        "CREATE INDEX IF NOT EXISTS idx_live_events_pending ON live_events(broadcast_at) WHERE broadcast_at IS NULL",
        "CREATE INDEX IF NOT EXISTS idx_live_events_created ON live_events(created_at DESC)",
        # handoff_tracking indexes
        "CREATE INDEX IF NOT EXISTS idx_handoff_from_session ON handoff_tracking(from_session_id, created_at DESC)",
        "CREATE INDEX IF NOT EXISTS idx_handoff_to_session ON handoff_tracking(to_session_id, resumed_at DESC)",
        "CREATE INDEX IF NOT EXISTS idx_handoff_rating ON handoff_tracking(user_rating, created_at DESC)",
        # cost_events indexes
        "CREATE INDEX IF NOT EXISTS idx_cost_events_session_ts ON cost_events(session_id, timestamp DESC)",
        "CREATE INDEX IF NOT EXISTS idx_cost_events_alert_type ON cost_events(alert_type, timestamp DESC)",
        "CREATE INDEX IF NOT EXISTS idx_cost_events_model ON cost_events(model, session_id)",
        "CREATE INDEX IF NOT EXISTS idx_cost_events_tool ON cost_events(tool_name, session_id)",
        "CREATE INDEX IF NOT EXISTS idx_cost_events_severity ON cost_events(severity, timestamp DESC)",
        "CREATE INDEX IF NOT EXISTS idx_cost_events_timestamp ON cost_events(timestamp DESC)",
        # agent_presence indexes
        "CREATE INDEX IF NOT EXISTS idx_agent_presence_status ON agent_presence(status, last_activity DESC)",
        "CREATE INDEX IF NOT EXISTS idx_agent_presence_feature ON agent_presence(current_feature_id, last_activity DESC)",
        "CREATE INDEX IF NOT EXISTS idx_agent_presence_activity ON agent_presence(last_activity DESC)",
        # offline_events indexes
        "CREATE INDEX IF NOT EXISTS idx_offline_events_status ON offline_events(status, timestamp DESC)",
        "CREATE INDEX IF NOT EXISTS idx_offline_events_resource ON offline_events(resource_id, resource_type)",
        "CREATE INDEX IF NOT EXISTS idx_offline_events_agent ON offline_events(agent_id, timestamp DESC)",
        # conflict_log indexes
        "CREATE INDEX IF NOT EXISTS idx_conflict_log_status ON conflict_log(status, created_at DESC)",
        "CREATE INDEX IF NOT EXISTS idx_conflict_log_resource ON conflict_log(resource_id, created_at DESC)",
        "CREATE INDEX IF NOT EXISTS idx_conflict_log_local_event ON conflict_log(local_event_id)",
        # sync_operations indexes
        "CREATE INDEX IF NOT EXISTS idx_sync_operations_status ON sync_operations(status, timestamp DESC)",
        "CREATE INDEX IF NOT EXISTS idx_sync_operations_operation ON sync_operations(operation, timestamp DESC)",
        "CREATE INDEX IF NOT EXISTS idx_sync_operations_timestamp ON sync_operations(timestamp DESC)",
        # oplog indexes
        "CREATE INDEX IF NOT EXISTS idx_oplog_seq ON oplog(seq DESC)",
        "CREATE INDEX IF NOT EXISTS idx_oplog_entity ON oplog(entity_type, entity_id, seq DESC)",
        "CREATE INDEX IF NOT EXISTS idx_oplog_actor_ts ON oplog(actor, ts DESC)",
        "CREATE INDEX IF NOT EXISTS idx_oplog_session_seq ON oplog(session_id, seq DESC)",
        # sync_cursors indexes
        "CREATE INDEX IF NOT EXISTS idx_sync_cursors_updated ON sync_cursors(updated_at DESC)",
        # sync_conflicts indexes
        "CREATE INDEX IF NOT EXISTS idx_sync_conflicts_status ON sync_conflicts(status, created_at DESC)",
        "CREATE INDEX IF NOT EXISTS idx_sync_conflicts_entity ON sync_conflicts(entity_type, entity_id, created_at DESC)",
    ]

    for index_sql in indexes:
        try:
            cursor.execute(index_sql)
        except sqlite3.OperationalError as e:
            logger.warning(f"Index creation warning: {e}")


# ---------------------------------------------------------------------------
# MIGRATIONS
# ---------------------------------------------------------------------------


def migrate_agent_events(cursor: sqlite3.Cursor) -> None:
    """
    Migrate agent_events table to add missing columns.

    Adds columns that may be missing from older database versions.

    Args:
        cursor: SQLite cursor for executing queries
    """
    cursor.execute(
        "SELECT name FROM sqlite_master WHERE type='table' AND name='agent_events'"
    )
    if not cursor.fetchone():
        return  # Table doesn't exist yet, will be created fresh

    cursor.execute("PRAGMA table_info(agent_events)")
    columns = {row[1] for row in cursor.fetchall()}

    migrations = [
        ("feature_id", "TEXT"),
        ("subagent_type", "TEXT"),
        ("child_spike_count", "INTEGER DEFAULT 0"),
        ("cost_tokens", "INTEGER DEFAULT 0"),
        ("execution_duration_seconds", "REAL DEFAULT 0.0"),
        ("status", "TEXT DEFAULT 'recorded'"),
        ("created_at", "DATETIME DEFAULT CURRENT_TIMESTAMP"),
        ("updated_at", "DATETIME DEFAULT CURRENT_TIMESTAMP"),
        ("model", "TEXT"),
        ("claude_task_id", "TEXT"),
        ("tool_input", "JSON"),
        ("source", "TEXT"),
    ]

    for col_name, col_type in migrations:
        if col_name not in columns:
            try:
                cursor.execute(
                    f"ALTER TABLE agent_events ADD COLUMN {col_name} {col_type}"
                )
                logger.info(f"Added column agent_events.{col_name}")
            except sqlite3.OperationalError as e:
                logger.debug(f"Could not add {col_name}: {e}")


def migrate_sessions(cursor: sqlite3.Cursor) -> None:
    """
    Migrate sessions table from old schema to new schema.

    Old schema had columns: session_id, agent, start_commit, continued_from,
                           status, started_at, ended_at
    New schema expects: session_id, agent_assigned, parent_session_id,
                       parent_event_id, created_at, etc.

    Args:
        cursor: SQLite cursor for executing queries
    """
    cursor.execute(
        "SELECT name FROM sqlite_master WHERE type='table' AND name='sessions'"
    )
    if not cursor.fetchone():
        return  # Table doesn't exist yet, will be created fresh

    cursor.execute("PRAGMA table_info(sessions)")
    columns = {row[1] for row in cursor.fetchall()}

    # Migration: rename 'agent' to 'agent_assigned' if needed
    if "agent" in columns and "agent_assigned" not in columns:
        try:
            cursor.execute("ALTER TABLE sessions RENAME COLUMN agent TO agent_assigned")
            logger.info("Migrated sessions.agent -> sessions.agent_assigned")
        except sqlite3.OperationalError as e:
            logger.debug(f"Could not rename column: {e}")

    migrations = [
        ("parent_session_id", "TEXT"),
        ("parent_event_id", "TEXT"),
        ("created_at", "DATETIME"),
        ("is_subagent", "INTEGER DEFAULT 0"),
        ("total_events", "INTEGER DEFAULT 0"),
        ("total_tokens_used", "INTEGER DEFAULT 0"),
        ("context_drift", "REAL DEFAULT 0.0"),
        ("transcript_id", "TEXT"),
        ("transcript_path", "TEXT"),
        ("transcript_synced", "INTEGER DEFAULT 0"),
        ("end_commit", "TEXT"),
        ("features_worked_on", "TEXT"),
        ("metadata", "TEXT"),
        ("completed_at", "DATETIME"),
        ("last_user_query_at", "DATETIME"),
        ("last_user_query", "TEXT"),
        ("handoff_notes", "TEXT"),
        ("recommended_next", "TEXT"),
        ("blockers", "TEXT"),
        ("recommended_context", "TEXT"),
        ("continued_from", "TEXT"),
        ("cost_budget", "REAL"),
        ("cost_threshold_breached", "INTEGER DEFAULT 0"),
        ("predicted_cost", "REAL DEFAULT 0.0"),
        ("model", "TEXT"),
    ]

    # Refresh columns after potential rename
    cursor.execute("PRAGMA table_info(sessions)")
    columns = {row[1] for row in cursor.fetchall()}

    for col_name, col_type in migrations:
        if col_name not in columns:
            try:
                cursor.execute(f"ALTER TABLE sessions ADD COLUMN {col_name} {col_type}")
                logger.info(f"Added column sessions.{col_name}")
            except sqlite3.OperationalError as e:
                logger.debug(f"Could not add {col_name}: {e}")


def migrate_event_type_constraint(cursor: sqlite3.Cursor) -> None:
    """
    Migrate agent_events CHECK constraint to include 'user_input'.

    SQLite doesn't support ALTER TABLE to modify CHECK constraints,
    so we use PRAGMA writable_schema to update the SQL in sqlite_master.

    Args:
        cursor: SQLite cursor for executing queries
    """
    cursor.execute(
        "SELECT sql FROM sqlite_master WHERE type='table' AND name='agent_events'"
    )
    row = cursor.fetchone()
    if not row or not row[0]:
        return

    current_sql = row[0]
    if "'user_input'" in current_sql:
        return  # Already migrated

    if "'task_delegation'" in current_sql and "'teammate_idle'" not in current_sql:
        # Old schema: ends with 'task_delegation')
        new_sql = current_sql.replace(
            "'task_delegation')",
            "'task_delegation',\n                               'teammate_idle', 'task_completed', 'user_input')",
        )
    elif "'task_completed'" in current_sql:
        # Has teammate_idle/task_completed but not user_input
        new_sql = current_sql.replace(
            "'task_completed')",
            "'task_completed', 'user_input')",
        )
    else:
        return  # Unknown schema shape, skip

    try:
        cursor.execute("PRAGMA writable_schema = ON")
        cursor.execute(
            "UPDATE sqlite_master SET sql = ? WHERE type='table' AND name='agent_events'",
            (new_sql,),
        )
        cursor.execute("PRAGMA writable_schema = OFF")
        cursor.execute("PRAGMA integrity_check")
        result = cursor.fetchone()
        if result and result[0] == "ok":
            logger.info(
                "Migrated agent_events CHECK constraint to include 'user_input'"
            )
        else:
            logger.warning(f"Integrity check after migration: {result}")
    except sqlite3.Error as e:
        logger.error(f"Failed to migrate event_type constraint: {e}")
        cursor.execute("PRAGMA writable_schema = OFF")


def run_data_migrations(cursor: sqlite3.Cursor) -> None:
    """
    Run data migrations to normalize existing data.

    This is idempotent and safe to run multiple times.
    Normalizes:
    - agent_id values to lowercase with hyphens
    - model names to display format (Opus 4.6, Sonnet 4.5, Haiku 4.5)

    Args:
        cursor: SQLite cursor for executing queries
    """
    cursor.execute(
        "SELECT name FROM sqlite_master WHERE type='table' AND name='agent_events'"
    )
    if not cursor.fetchone():
        return  # Table doesn't exist yet

    try:
        # Normalize agent_id to lowercase with hyphens
        cursor.execute("""
            UPDATE agent_events
            SET agent_id = LOWER(REPLACE(agent_id, ' ', '-'))
            WHERE agent_id != LOWER(REPLACE(agent_id, ' ', '-'))
        """)
        normalized_agents = cursor.rowcount
        if normalized_agents > 0:
            logger.info(f"Normalized {normalized_agents} agent_id values")

        # Normalize model names to display format
        cursor.execute("""
            UPDATE agent_events
            SET model = 'Opus 4.6'
            WHERE LOWER(model) IN ('claude-opus-4-6', 'claude-opus', 'opus')
              AND model != 'Opus 4.6'
        """)
        normalized_opus = cursor.rowcount

        cursor.execute("""
            UPDATE agent_events
            SET model = 'Sonnet 4.5'
            WHERE LOWER(model) IN ('claude-sonnet-4-5-20250929', 'claude-sonnet', 'sonnet')
              AND model != 'Sonnet 4.5'
        """)
        normalized_sonnet = cursor.rowcount

        cursor.execute("""
            UPDATE agent_events
            SET model = 'Haiku 4.5'
            WHERE LOWER(model) IN ('claude-haiku-4-5-20251001', 'claude-haiku', 'haiku')
              AND model != 'Haiku 4.5'
        """)
        normalized_haiku = cursor.rowcount

        total_normalized_models = normalized_opus + normalized_sonnet + normalized_haiku
        if total_normalized_models > 0:
            logger.info(f"Normalized {total_normalized_models} model values")

        # Handle partial matches (e.g., "claude-opus-4-6-20250101")
        cursor.execute("""
            UPDATE agent_events
            SET model = 'Opus 4.6'
            WHERE LOWER(model) LIKE '%opus%'
              AND model NOT IN ('Opus 4.6', 'Sonnet 4.5', 'Haiku 4.5')
        """)
        partial_opus = cursor.rowcount

        cursor.execute("""
            UPDATE agent_events
            SET model = 'Sonnet 4.5'
            WHERE LOWER(model) LIKE '%sonnet%'
              AND model NOT IN ('Opus 4.6', 'Sonnet 4.5', 'Haiku 4.5')
        """)
        partial_sonnet = cursor.rowcount

        cursor.execute("""
            UPDATE agent_events
            SET model = 'Haiku 4.5'
            WHERE LOWER(model) LIKE '%haiku%'
              AND model NOT IN ('Opus 4.6', 'Sonnet 4.5', 'Haiku 4.5')
        """)
        partial_haiku = cursor.rowcount

        total_partial = partial_opus + partial_sonnet + partial_haiku
        if total_partial > 0:
            logger.info(f"Normalized {total_partial} partial model matches")

    except sqlite3.Error as e:
        logger.warning(f"Error running data migrations: {e}")
