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
                               'teammate_idle', 'task_completed')
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
            active_feature_id TEXT,
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

    # 9. AGENT_PRESENCE TABLE - Cross-Agent Presence Tracking
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

    # 11. FTS5 VIRTUAL TABLES - Full-text search (sessions_fts, events_fts)
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
        "CREATE INDEX IF NOT EXISTS idx_agent_events_step_id ON agent_events(step_id)",
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
        # live_events indexes
        "CREATE INDEX IF NOT EXISTS idx_live_events_pending ON live_events(broadcast_at) WHERE broadcast_at IS NULL",
        "CREATE INDEX IF NOT EXISTS idx_live_events_created ON live_events(created_at DESC)",
        # agent_presence indexes
        "CREATE INDEX IF NOT EXISTS idx_agent_presence_status ON agent_presence(status, last_activity DESC)",
        "CREATE INDEX IF NOT EXISTS idx_agent_presence_feature ON agent_presence(current_feature_id, last_activity DESC)",
        "CREATE INDEX IF NOT EXISTS idx_agent_presence_activity ON agent_presence(last_activity DESC)",
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
        ("step_id", "TEXT"),
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
        ("active_feature_id", "TEXT"),
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


def migrate_features(cursor: sqlite3.Cursor) -> None:
    """
    Migrate features table to add missing columns.

    Adds columns that may be missing from older database versions.

    Args:
        cursor: SQLite cursor for executing queries
    """
    cursor.execute(
        "SELECT name FROM sqlite_master WHERE type='table' AND name='features'"
    )
    if not cursor.fetchone():
        return  # Table doesn't exist yet, will be created fresh

    cursor.execute("PRAGMA table_info(features)")
    columns = {row[1] for row in cursor.fetchall()}

    migrations = [
        ("assignee", "TEXT DEFAULT NULL"),
    ]

    for col_name, col_type in migrations:
        if col_name not in columns:
            try:
                cursor.execute(f"ALTER TABLE features ADD COLUMN {col_name} {col_type}")
                logger.info(f"Added column features.{col_name}")
            except sqlite3.OperationalError as e:
                logger.debug(f"Could not add {col_name}: {e}")


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
