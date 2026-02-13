"""
HtmlGraph SQLite Schema - Phase 1 Backend Storage

This module defines the comprehensive SQLite schema for HtmlGraph agent observability,
replacing HTML file storage with structured relational database.

Key design principles:
- Normalize data while preserving flexibility via JSON columns
- Index frequently queried fields for performance
- Track audit trails (created_at, updated_at)
- Support graph relationships via edge tracking
- Enable full observability of agent activities

Tables:
- agent_events: All agent tool calls, results, errors, delegations
- features: Feature/bug/spike/chore/epic work items
- sessions: Agent session tracking with metrics
- tracks: Multi-feature initiatives
- agent_collaboration: Handoffs and parallel work
- graph_edges: General relationship tracking
- event_log_archive: Historical event log for querying

CRUD operations are provided by domain-specific mixin classes:
- SessionOps: Session insert/update/query
- EventOps: Event insert/query
- FeatureOps: Feature insert/update/query
- CollaborationOps: Collaboration/delegation insert/query
- TraceOps: Tool trace and live event insert/update/query
- SyncOps: Sync operation insert/query
"""

import logging
import sqlite3
from pathlib import Path

from htmlgraph.db.collaboration_ops import CollaborationOps
from htmlgraph.db.event_ops import EventOps
from htmlgraph.db.feature_ops import FeatureOps
from htmlgraph.db.session_ops import SessionOps
from htmlgraph.db.sync_ops import SyncOps
from htmlgraph.db.trace_ops import TraceOps

logger = logging.getLogger(__name__)


class HtmlGraphDB(
    SessionOps, EventOps, FeatureOps, CollaborationOps, TraceOps, SyncOps
):
    """
    SQLite database manager for HtmlGraph observability backend.

    Provides schema creation, migrations, and query helpers for storing
    and retrieving agent events, features, sessions, and collaborations.

    CRUD operations are inherited from domain-specific mixins:
    - SessionOps: insert_session, _ensure_session_exists, update_session_activity,
                  get_concurrent_sessions
    - EventOps: insert_event, get_session_events, get_events_for_task, get_subagent_work
    - FeatureOps: insert_feature, update_feature_status, get_feature_by_id,
                  get_features_by_status
    - CollaborationOps: record_collaboration, record_delegation_event, get_delegations,
                        insert_collaboration
    - TraceOps: insert_tool_trace, update_tool_trace, get_tool_trace,
                get_session_tool_traces, insert_live_event, get_pending_live_events,
                mark_live_events_broadcast, cleanup_old_live_events
    - SyncOps: insert_sync_operation, get_sync_operations
    """

    def __init__(self, db_path: str | None = None):
        """
        Initialize HtmlGraph database.

        Args:
            db_path: Path to SQLite database file. If None, uses default location.
        """
        if db_path is None:
            # Default: .htmlgraph/htmlgraph.db in project root
            db_path = str(Path.home() / ".htmlgraph" / "htmlgraph.db")

        self.db_path = Path(db_path)
        self.db_path.parent.mkdir(parents=True, exist_ok=True)
        self.connection: sqlite3.Connection | None = None

        # Auto-initialize schema on first instantiation
        self.connect()
        self.create_tables()

    def connect(self) -> sqlite3.Connection:
        """
        Connect to SQLite database, creating it if needed.

        Returns:
            SQLite connection object
        """
        self.connection = sqlite3.connect(str(self.db_path))
        self.connection.row_factory = sqlite3.Row
        # Enable foreign keys
        self.connection.execute("PRAGMA foreign_keys = ON")
        return self.connection

    def disconnect(self) -> None:
        """Close database connection."""
        if self.connection:
            self.connection.close()
            self.connection = None

    def close(self) -> None:
        """Clean up database connection."""
        self.disconnect()

    # ========== MIGRATION METHODS ==========

    def _migrate_agent_events_table(self, cursor: sqlite3.Cursor) -> None:
        """
        Migrate agent_events table to add missing columns.

        Adds columns that may be missing from older database versions.
        """
        # Check if agent_events table exists
        cursor.execute(
            "SELECT name FROM sqlite_master WHERE type='table' AND name='agent_events'"
        )
        if not cursor.fetchone():
            return  # Table doesn't exist yet, will be created fresh

        # Get current columns
        cursor.execute("PRAGMA table_info(agent_events)")
        columns = {row[1] for row in cursor.fetchall()}

        # Add missing columns with defaults
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
        ]

        for col_name, col_type in migrations:
            if col_name not in columns:
                try:
                    cursor.execute(
                        f"ALTER TABLE agent_events ADD COLUMN {col_name} {col_type}"
                    )
                    logger.info(f"Added column agent_events.{col_name}")
                except sqlite3.OperationalError as e:
                    # Column may already exist
                    logger.debug(f"Could not add {col_name}: {e}")

    def _run_data_migrations(self, cursor: sqlite3.Cursor) -> None:
        """
        Run data migrations to normalize existing data.

        This is idempotent and safe to run multiple times.
        """
        # Check if agent_events table exists
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

            total_normalized_models = (
                normalized_opus + normalized_sonnet + normalized_haiku
            )
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

    def _migrate_sessions_table(self, cursor: sqlite3.Cursor) -> None:
        """
        Migrate sessions table from old schema to new schema.

        Old schema had columns: session_id, agent, start_commit, continued_from,
                               status, started_at, ended_at
        New schema expects: session_id, agent_assigned, parent_session_id,
                           parent_event_id, created_at, etc.
        """
        # Check if sessions table exists with old schema
        cursor.execute(
            "SELECT name FROM sqlite_master WHERE type='table' AND name='sessions'"
        )
        if not cursor.fetchone():
            return  # Table doesn't exist yet, will be created fresh

        # Get current columns
        cursor.execute("PRAGMA table_info(sessions)")
        columns = {row[1] for row in cursor.fetchall()}

        # Migration: rename 'agent' to 'agent_assigned' if needed
        if "agent" in columns and "agent_assigned" not in columns:
            try:
                cursor.execute(
                    "ALTER TABLE sessions RENAME COLUMN agent TO agent_assigned"
                )
                logger.info("Migrated sessions.agent -> sessions.agent_assigned")
            except sqlite3.OperationalError as e:
                logger.debug(f"Could not rename column: {e}")
                # Column may already exist
                pass

        # Add missing columns with defaults
        # Note: SQLite doesn't allow CURRENT_TIMESTAMP in ALTER TABLE, so we use NULL
        migrations = [
            ("parent_session_id", "TEXT"),
            ("parent_event_id", "TEXT"),
            ("created_at", "DATETIME"),  # Can't use DEFAULT CURRENT_TIMESTAMP in ALTER
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
            # Phase 2 Feature 3: Cross-Session Continuity handoff fields
            ("handoff_notes", "TEXT"),
            ("recommended_next", "TEXT"),
            ("blockers", "TEXT"),  # JSON array of blocker strings
            ("recommended_context", "TEXT"),  # JSON array of file paths
            ("continued_from", "TEXT"),  # Previous session ID
            # Phase 3.1: Real-time cost monitoring
            ("cost_budget", "REAL"),  # Budget in USD for this session
            ("cost_threshold_breached", "INTEGER DEFAULT 0"),  # Whether budget exceeded
            ("predicted_cost", "REAL DEFAULT 0.0"),  # Predicted final cost
        ]

        # Refresh columns after potential rename
        cursor.execute("PRAGMA table_info(sessions)")
        columns = {row[1] for row in cursor.fetchall()}

        for col_name, col_type in migrations:
            if col_name not in columns:
                try:
                    cursor.execute(
                        f"ALTER TABLE sessions ADD COLUMN {col_name} {col_type}"
                    )
                    logger.info(f"Added column sessions.{col_name}")
                except sqlite3.OperationalError as e:
                    # Column may already exist
                    logger.debug(f"Could not add {col_name}: {e}")

    # ========== SCHEMA CREATION ==========

    def create_tables(self) -> None:
        """
        Create all required tables in SQLite database.

        Tables created:
        1. agent_events - Core event tracking
        2. features - Work items (features, bugs, spikes, etc.)
        3. sessions - Agent sessions with metrics
        4. tracks - Multi-feature initiatives
        5. agent_collaboration - Handoffs and parallel work
        6. graph_edges - Flexible relationship tracking
        7. event_log_archive - Historical event log
        8. indexes - Performance optimization
        """
        if not self.connection:
            self.connect()

        cursor = self.connection.cursor()  # type: ignore[union-attr]

        # Run migrations for existing tables before creating new ones
        self._migrate_agent_events_table(cursor)
        self._migrate_sessions_table(cursor)

        # Run data migrations to normalize existing data
        self._run_data_migrations(cursor)

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

        # Create indexes for performance
        self._create_indexes(cursor)

        if self.connection:
            self.connection.commit()
        logger.info(f"SQLite schema created at {self.db_path}")

    def _create_indexes(self, cursor: sqlite3.Cursor) -> None:
        """
        Create indexes on frequently queried fields.

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
        ]

        for index_sql in indexes:
            try:
                cursor.execute(index_sql)
            except sqlite3.OperationalError as e:
                logger.warning(f"Index creation warning: {e}")
