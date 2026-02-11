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
"""

import json
import logging
import sqlite3
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any

logger = logging.getLogger(__name__)


class HtmlGraphDB:
    """
    SQLite database manager for HtmlGraph observability backend.

    Provides schema creation, migrations, and query helpers for storing
    and retrieving agent events, features, sessions, and collaborations.
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
            ("agent_id", "TEXT"),
            ("source", "TEXT DEFAULT 'hook'"),
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
            ("model", "TEXT"),
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

    def _migrate_tool_traces_table(self, cursor: sqlite3.Cursor) -> None:
        """Migrate tool_traces table to add parent_event_id for attribution."""
        cursor.execute(
            "SELECT name FROM sqlite_master WHERE type='table' AND name='tool_traces'"
        )
        if not cursor.fetchone():
            return

        cursor.execute("PRAGMA table_info(tool_traces)")
        columns = {row[1] for row in cursor.fetchall()}

        migrations = [
            ("parent_event_id", "TEXT"),
        ]

        for col_name, col_type in migrations:
            if col_name not in columns:
                try:
                    cursor.execute(
                        f"ALTER TABLE tool_traces ADD COLUMN {col_name} {col_type}"
                    )
                    logger.info(f"Added column tool_traces.{col_name}")
                except sqlite3.OperationalError as e:
                    logger.debug(f"Could not add {col_name}: {e}")

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
        self._migrate_tool_traces_table(cursor)

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
                source TEXT DEFAULT 'hook',
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
        # Events are inserted here for WebSocket broadcasting, then auto-cleaned after broadcast
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
                parent_event_id TEXT,
                created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
                FOREIGN KEY (session_id) REFERENCES sessions(session_id),
                FOREIGN KEY (parent_tool_use_id) REFERENCES tool_traces(tool_use_id)
            )
        """)

        # 10. HANDOFF_TRACKING TABLE - Phase 2 Feature 3: Track handoff effectiveness
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

        # 11. COST_EVENTS TABLE - Phase 3.1: Real-time cost monitoring & alerts
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

        # 12. AGENT_PRESENCE TABLE - Phase 1: Cross-Agent Presence Tracking
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

        # 13. OFFLINE_EVENTS TABLE - Phase 4: Offline-First Merge
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

        # 14. CONFLICT_LOG TABLE - Phase 4: Conflict tracking and resolution
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

        # 15. SYNC_OPERATIONS TABLE - Phase 5: Git sync tracking
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

        # 9. Create indexes for performance
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
            # agent_events indexes - optimized for common query patterns
            # Pattern: WHERE session_id ORDER BY timestamp DESC (activity feed)
            "CREATE INDEX IF NOT EXISTS idx_agent_events_session_ts_desc ON agent_events(session_id, timestamp DESC)",
            # Pattern: WHERE agent_id ORDER BY timestamp DESC (agent timeline)
            "CREATE INDEX IF NOT EXISTS idx_agent_events_agent_ts_desc ON agent_events(agent_id, timestamp DESC)",
            # Pattern: GROUP BY agent_id (agent statistics)
            "CREATE INDEX IF NOT EXISTS idx_agent_events_agent ON agent_events(agent_id)",
            # Pattern: WHERE event_type = 'error' (error tracking)
            "CREATE INDEX IF NOT EXISTS idx_agent_events_type ON agent_events(event_type)",
            # Pattern: WHERE parent_event_id (hierarchical queries)
            "CREATE INDEX IF NOT EXISTS idx_agent_events_parent_event ON agent_events(parent_event_id)",
            # Pattern: WHERE event_type = 'task_delegation' (task delegation queries)
            "CREATE INDEX IF NOT EXISTS idx_agent_events_task_delegation ON agent_events(event_type, subagent_type, timestamp DESC)",
            # Pattern: Tool usage summary GROUP BY tool_name WHERE session_id
            "CREATE INDEX IF NOT EXISTS idx_agent_events_session_tool ON agent_events(session_id, tool_name)",
            # Pattern: Timestamp range queries
            "CREATE INDEX IF NOT EXISTS idx_agent_events_timestamp ON agent_events(timestamp DESC)",
            # Pattern: WHERE claude_task_id (task attribution queries)
            "CREATE INDEX IF NOT EXISTS idx_agent_events_claude_task_id ON agent_events(claude_task_id)",
            # Pattern: WHERE agent_id (agent-specific queries)
            "CREATE INDEX IF NOT EXISTS idx_agent_events_agent_id ON agent_events(agent_id)",
            # features indexes - optimized for kanban/filtering
            # Pattern: WHERE status ORDER BY priority DESC (feature list views)
            "CREATE INDEX IF NOT EXISTS idx_features_status_priority ON features(status, priority DESC, created_at DESC)",
            # Pattern: WHERE track_id ORDER BY priority (track features)
            "CREATE INDEX IF NOT EXISTS idx_features_track_priority ON features(track_id, priority DESC, created_at DESC)",
            # Pattern: WHERE assigned_to (agent workload)
            "CREATE INDEX IF NOT EXISTS idx_features_assigned ON features(assigned_to)",
            # Pattern: WHERE parent_feature_id (feature tree)
            "CREATE INDEX IF NOT EXISTS idx_features_parent ON features(parent_feature_id)",
            # Pattern: WHERE type (filtering by type)
            "CREATE INDEX IF NOT EXISTS idx_features_type ON features(type)",
            # Pattern: Created timestamp range queries
            "CREATE INDEX IF NOT EXISTS idx_features_created ON features(created_at DESC)",
            # sessions indexes - optimized for session analysis
            # Pattern: WHERE agent_assigned ORDER BY created_at DESC
            "CREATE INDEX IF NOT EXISTS idx_sessions_agent_created ON sessions(agent_assigned, created_at DESC)",
            # Pattern: WHERE status (active sessions query)
            "CREATE INDEX IF NOT EXISTS idx_sessions_status_created ON sessions(status, created_at DESC)",
            # Pattern: WHERE parent_session_id (subagent queries)
            "CREATE INDEX IF NOT EXISTS idx_sessions_parent ON sessions(parent_session_id, created_at DESC)",
            # Pattern: Timestamp ordering for metrics
            "CREATE INDEX IF NOT EXISTS idx_sessions_created ON sessions(created_at DESC)",
            # tracks indexes - optimized for track queries
            # Pattern: WHERE status GROUP BY track_id
            "CREATE INDEX IF NOT EXISTS idx_tracks_status_created ON tracks(status, created_at DESC)",
            # Pattern: Ordering by priority
            "CREATE INDEX IF NOT EXISTS idx_tracks_priority ON tracks(priority DESC)",
            # collaboration indexes - optimized for handoff queries
            # Pattern: WHERE session_id, WHERE from_agent, WHERE to_agent
            "CREATE INDEX IF NOT EXISTS idx_collaboration_session ON agent_collaboration(session_id, timestamp DESC)",
            "CREATE INDEX IF NOT EXISTS idx_collaboration_from_agent ON agent_collaboration(from_agent)",
            "CREATE INDEX IF NOT EXISTS idx_collaboration_to_agent ON agent_collaboration(to_agent)",
            # Pattern: GROUP BY from_agent, to_agent
            "CREATE INDEX IF NOT EXISTS idx_collaboration_agents ON agent_collaboration(from_agent, to_agent)",
            "CREATE INDEX IF NOT EXISTS idx_collaboration_feature ON agent_collaboration(feature_id)",
            "CREATE INDEX IF NOT EXISTS idx_collaboration_handoff_type ON agent_collaboration(handoff_type, timestamp DESC)",
            # graph_edges indexes - optimized for graph traversal
            "CREATE INDEX IF NOT EXISTS idx_edges_from ON graph_edges(from_node_id)",
            "CREATE INDEX IF NOT EXISTS idx_edges_to ON graph_edges(to_node_id)",
            "CREATE INDEX IF NOT EXISTS idx_edges_type ON graph_edges(relationship_type)",
            # tool_traces indexes - optimized for tool performance analysis
            "CREATE INDEX IF NOT EXISTS idx_tool_traces_trace_id ON tool_traces(trace_id, start_time DESC)",
            "CREATE INDEX IF NOT EXISTS idx_tool_traces_session ON tool_traces(session_id, start_time DESC)",
            "CREATE INDEX IF NOT EXISTS idx_tool_traces_tool_name ON tool_traces(tool_name, status)",
            "CREATE INDEX IF NOT EXISTS idx_tool_traces_status ON tool_traces(status, start_time DESC)",
            "CREATE INDEX IF NOT EXISTS idx_tool_traces_start_time ON tool_traces(start_time DESC)",
            # Pattern: WHERE parent_event_id (tool trace attribution)
            "CREATE INDEX IF NOT EXISTS idx_tool_traces_parent_event ON tool_traces(parent_event_id)",
            # live_events indexes - optimized for real-time WebSocket streaming
            "CREATE INDEX IF NOT EXISTS idx_live_events_pending ON live_events(broadcast_at) WHERE broadcast_at IS NULL",
            "CREATE INDEX IF NOT EXISTS idx_live_events_created ON live_events(created_at DESC)",
            # handoff_tracking indexes - optimized for handoff effectiveness queries
            "CREATE INDEX IF NOT EXISTS idx_handoff_from_session ON handoff_tracking(from_session_id, created_at DESC)",
            "CREATE INDEX IF NOT EXISTS idx_handoff_to_session ON handoff_tracking(to_session_id, resumed_at DESC)",
            "CREATE INDEX IF NOT EXISTS idx_handoff_rating ON handoff_tracking(user_rating, created_at DESC)",
            # cost_events indexes - optimized for real-time cost monitoring & alerts
            # Pattern: WHERE session_id ORDER BY timestamp DESC (cost timeline)
            "CREATE INDEX IF NOT EXISTS idx_cost_events_session_ts ON cost_events(session_id, timestamp DESC)",
            # Pattern: WHERE alert_type (alert filtering)
            "CREATE INDEX IF NOT EXISTS idx_cost_events_alert_type ON cost_events(alert_type, timestamp DESC)",
            # Pattern: WHERE model GROUP BY (cost breakdown)
            "CREATE INDEX IF NOT EXISTS idx_cost_events_model ON cost_events(model, session_id)",
            # Pattern: WHERE tool_name GROUP BY (tool cost analysis)
            "CREATE INDEX IF NOT EXISTS idx_cost_events_tool ON cost_events(tool_name, session_id)",
            # Pattern: WHERE severity (alert severity filtering)
            "CREATE INDEX IF NOT EXISTS idx_cost_events_severity ON cost_events(severity, timestamp DESC)",
            # Pattern: Timestamp range queries for predictions
            "CREATE INDEX IF NOT EXISTS idx_cost_events_timestamp ON cost_events(timestamp DESC)",
            # agent_presence indexes - optimized for presence queries
            # Pattern: WHERE status (filter by status)
            "CREATE INDEX IF NOT EXISTS idx_agent_presence_status ON agent_presence(status, last_activity DESC)",
            # Pattern: WHERE current_feature_id (agents working on feature)
            "CREATE INDEX IF NOT EXISTS idx_agent_presence_feature ON agent_presence(current_feature_id, last_activity DESC)",
            # Pattern: ORDER BY last_activity (recent activity)
            "CREATE INDEX IF NOT EXISTS idx_agent_presence_activity ON agent_presence(last_activity DESC)",
            # offline_events indexes - optimized for sync and conflict detection
            # Pattern: WHERE status = 'local_only' (unsynced events)
            "CREATE INDEX IF NOT EXISTS idx_offline_events_status ON offline_events(status, timestamp DESC)",
            # Pattern: WHERE resource_id (detect conflicts)
            "CREATE INDEX IF NOT EXISTS idx_offline_events_resource ON offline_events(resource_id, resource_type)",
            # Pattern: WHERE agent_id (agent's offline work)
            "CREATE INDEX IF NOT EXISTS idx_offline_events_agent ON offline_events(agent_id, timestamp DESC)",
            # conflict_log indexes - optimized for conflict management
            # Pattern: WHERE status = 'pending_review' (unresolved conflicts)
            "CREATE INDEX IF NOT EXISTS idx_conflict_log_status ON conflict_log(status, created_at DESC)",
            # Pattern: WHERE resource_id (conflicts for resource)
            "CREATE INDEX IF NOT EXISTS idx_conflict_log_resource ON conflict_log(resource_id, created_at DESC)",
            # Pattern: WHERE local_event_id (lookup by event)
            "CREATE INDEX IF NOT EXISTS idx_conflict_log_local_event ON conflict_log(local_event_id)",
            # sync_operations indexes - optimized for sync history queries
            # Pattern: WHERE status ORDER BY timestamp DESC (recent sync status)
            "CREATE INDEX IF NOT EXISTS idx_sync_operations_status ON sync_operations(status, timestamp DESC)",
            # Pattern: WHERE operation ORDER BY timestamp DESC (push/pull history)
            "CREATE INDEX IF NOT EXISTS idx_sync_operations_operation ON sync_operations(operation, timestamp DESC)",
            # Pattern: ORDER BY timestamp DESC (recent activity)
            "CREATE INDEX IF NOT EXISTS idx_sync_operations_timestamp ON sync_operations(timestamp DESC)",
        ]

        for index_sql in indexes:
            try:
                cursor.execute(index_sql)
            except sqlite3.OperationalError as e:
                logger.warning(f"Index creation warning: {e}")

    def insert_event(
        self,
        event_id: str,
        agent_id: str,
        event_type: str,
        session_id: str,
        tool_name: str | None = None,
        input_summary: str | None = None,
        tool_input: dict[str, Any] | None = None,
        output_summary: str | None = None,
        context: dict[str, Any] | None = None,
        parent_agent_id: str | None = None,
        parent_event_id: str | None = None,
        cost_tokens: int = 0,
        execution_duration_seconds: float = 0.0,
        subagent_type: str | None = None,
        model: str | None = None,
        feature_id: str | None = None,
        claude_task_id: str | None = None,
        source: str = "hook",
    ) -> bool:
        """
        Insert an agent event into the database.

        Gracefully handles FOREIGN KEY constraint failures by retrying without
        the parent_event_id reference. This allows events to be recorded even if
        the parent event doesn't exist yet (useful for cross-process or distributed
        event tracking).

        Args:
            event_id: Unique event identifier
            agent_id: Agent that generated this event
            event_type: Type of event (tool_call, tool_result, error, etc.)
            session_id: Session this event belongs to
            tool_name: Tool that was called (optional)
            input_summary: Summary of tool input (optional)
            tool_input: Raw tool input as JSON (optional)
            output_summary: Summary of tool output (optional)
            context: Additional metadata as JSON (optional)
            parent_agent_id: Parent agent if delegated (optional)
            parent_event_id: Parent event if nested (optional)
            cost_tokens: Token usage estimate (optional)
            execution_duration_seconds: Execution time in seconds (optional)
            subagent_type: Subagent type for Task delegations (optional)
            model: Claude model name (e.g., claude-haiku, claude-opus, claude-sonnet) (optional)
            claude_task_id: Claude Code's internal task ID for tool attribution (optional)
            source: Source of the event (hook, sdk, api) (default: 'hook')

        Returns:
            True if insert successful, False otherwise
        """
        if not self.connection:
            self.connect()

        try:
            cursor = self.connection.cursor()  # type: ignore[union-attr]
            # Temporarily disable foreign key constraints to allow inserting
            # events even if parent_event_id or session_id don't exist yet
            # (useful for cross-process event tracking where sessions are created asynchronously)
            cursor.execute("PRAGMA foreign_keys=OFF")
            cursor.execute(
                """
                INSERT INTO agent_events
                (event_id, agent_id, event_type, session_id, feature_id, tool_name,
                 input_summary, tool_input, output_summary, context, parent_agent_id,
                 parent_event_id, cost_tokens, execution_duration_seconds, subagent_type, model, claude_task_id, source)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
                (
                    event_id,
                    agent_id,
                    event_type,
                    session_id,
                    feature_id,
                    tool_name,
                    input_summary,
                    json.dumps(tool_input) if tool_input else None,
                    output_summary,
                    json.dumps(context) if context else None,
                    parent_agent_id,
                    parent_event_id,
                    cost_tokens,
                    execution_duration_seconds,
                    subagent_type,
                    model,
                    claude_task_id,
                    source,
                ),
            )
            # Re-enable foreign key constraints
            cursor.execute("PRAGMA foreign_keys=ON")

            # Update session metadata counters
            cursor.execute(
                """
                UPDATE sessions
                SET total_events = total_events + 1,
                    total_tokens_used = total_tokens_used + ?
                WHERE session_id = ?
            """,
                (cost_tokens, session_id),
            )

            self.connection.commit()  # type: ignore[union-attr]
            return True
        except sqlite3.IntegrityError as e:
            # Other integrity errors (unique constraint, etc.)
            logger.error(f"Error inserting event: {e}")
            return False
        except sqlite3.Error as e:
            logger.error(f"Error inserting event: {e}")
            return False

    def insert_feature(
        self,
        feature_id: str,
        feature_type: str,
        title: str,
        status: str = "todo",
        priority: str = "medium",
        assigned_to: str | None = None,
        track_id: str | None = None,
        description: str | None = None,
        steps_total: int = 0,
        tags: list | None = None,
    ) -> bool:
        """
        Insert a feature/bug/spike work item.

        Args:
            feature_id: Unique feature identifier
            feature_type: Type (feature, bug, spike, chore, epic)
            title: Feature title
            status: Current status (todo, in_progress, done, etc.)
            priority: Priority level (low, medium, high, critical)
            assigned_to: Assigned agent (optional)
            track_id: Parent track ID (optional)
            description: Feature description (optional)
            steps_total: Total implementation steps
            tags: Tags for categorization (optional)

        Returns:
            True if insert successful, False otherwise
        """
        if not self.connection:
            self.connect()

        try:
            cursor = self.connection.cursor()  # type: ignore[union-attr]
            cursor.execute(
                """
                INSERT INTO features
                (id, type, title, status, priority, assigned_to, track_id,
                 description, steps_total, tags)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
                (
                    feature_id,
                    feature_type,
                    title,
                    status,
                    priority,
                    assigned_to,
                    track_id,
                    description,
                    steps_total,
                    json.dumps(tags) if tags else None,
                ),
            )
            self.connection.commit()  # type: ignore[union-attr]
            return True
        except sqlite3.Error as e:
            logger.error(f"Error inserting feature: {e}")
            return False

    def insert_session(
        self,
        session_id: str,
        agent_assigned: str,
        parent_session_id: str | None = None,
        parent_event_id: str | None = None,
        is_subagent: bool = False,
        transcript_id: str | None = None,
        transcript_path: str | None = None,
        model: str | None = None,
    ) -> bool:
        """
        Insert a new session record.

        Gracefully handles FOREIGN KEY constraint failures by retrying without
        the parent_event_id or parent_session_id reference. This allows sessions
        to be created even if the parent doesn't exist yet.

        Args:
            session_id: Unique session identifier
            agent_assigned: Primary agent for this session
            parent_session_id: Parent session if subagent (optional)
            parent_event_id: Event that spawned this session (optional)
            is_subagent: Whether this is a subagent session
            transcript_id: ID of Claude transcript (optional)
            transcript_path: Path to transcript file (optional)
            model: Claude model name for this session (optional)

        Returns:
            True if insert successful, False otherwise
        """
        if not self.connection:
            self.connect()

        try:
            cursor = self.connection.cursor()  # type: ignore[union-attr]
            cursor.execute(
                """
                INSERT OR IGNORE INTO sessions
                (session_id, agent_assigned, parent_session_id, parent_event_id,
                 is_subagent, transcript_id, transcript_path, model)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?)
            """,
                (
                    session_id,
                    agent_assigned,
                    parent_session_id,
                    parent_event_id,
                    is_subagent,
                    transcript_id,
                    transcript_path,
                    model,
                ),
            )
            self.connection.commit()  # type: ignore[union-attr]
            return True
        except sqlite3.IntegrityError as e:
            # FOREIGN KEY constraint failed - parent doesn't exist
            if "FOREIGN KEY constraint failed" in str(e) and (
                parent_event_id or parent_session_id
            ):
                logger.warning(
                    "Parent session/event not found, creating session without parent link"
                )
                # Retry without parent references to enable graceful degradation
                try:
                    cursor = self.connection.cursor()  # type: ignore[union-attr]
                    cursor.execute(
                        """
                        INSERT OR IGNORE INTO sessions
                        (session_id, agent_assigned, parent_session_id, parent_event_id,
                         is_subagent, transcript_id, transcript_path, model)
                        VALUES (?, ?, ?, ?, ?, ?, ?, ?)
                    """,
                        (
                            session_id,
                            agent_assigned,
                            None,  # Drop parent_session_id
                            None,  # Drop parent_event_id
                            is_subagent,
                            transcript_id,
                            transcript_path,
                            model,
                        ),
                    )
                    self.connection.commit()  # type: ignore[union-attr]
                    return True
                except sqlite3.Error as retry_error:
                    logger.error(f"Error inserting session after retry: {retry_error}")
                    return False
            else:
                logger.error(f"Error inserting session: {e}")
                return False
        except sqlite3.Error as e:
            logger.error(f"Error inserting session: {e}")
            return False

    def update_feature_status(
        self,
        feature_id: str,
        status: str,
        steps_completed: int | None = None,
    ) -> bool:
        """
        Update feature status and completion progress.

        Args:
            feature_id: Feature to update
            status: New status (todo, in_progress, done, etc.)
            steps_completed: Number of steps completed (optional)

        Returns:
            True if update successful, False otherwise
        """
        if not self.connection:
            self.connect()

        try:
            cursor = self.connection.cursor()  # type: ignore[union-attr]
            if steps_completed is not None:
                cursor.execute(
                    """
                    UPDATE features
                    SET status = ?, steps_completed = ?, updated_at = CURRENT_TIMESTAMP
                    WHERE id = ?
                """,
                    (status, steps_completed, feature_id),
                )
            else:
                cursor.execute(
                    """
                    UPDATE features
                    SET status = ?, updated_at = CURRENT_TIMESTAMP
                    WHERE id = ?
                """,
                    (status, feature_id),
                )

            # Auto-set completed_at if status is done
            if status == "done":
                cursor.execute(
                    """
                    UPDATE features
                    SET completed_at = CURRENT_TIMESTAMP
                    WHERE id = ?
                """,
                    (feature_id,),
                )

            self.connection.commit()  # type: ignore[union-attr]
            return True
        except sqlite3.Error as e:
            logger.error(f"Error updating feature: {e}")
            return False

    def get_session_events(self, session_id: str) -> list[dict[str, Any]]:
        """
        Get all events for a session.

        Args:
            session_id: Session to query

        Returns:
            List of event dictionaries ordered by timestamp DESC (newest first)
        """
        if not self.connection:
            self.connect()

        try:
            cursor = self.connection.cursor()  # type: ignore[union-attr]
            cursor.execute(
                """
                SELECT * FROM agent_events
                WHERE session_id = ?
                ORDER BY timestamp DESC
            """,
                (session_id,),
            )

            rows = cursor.fetchall()
            return [dict(row) for row in rows]
        except sqlite3.Error as e:
            logger.error(f"Error querying events: {e}")
            return []

    def get_feature_by_id(self, feature_id: str) -> dict[str, Any] | None:
        """
        Get a feature by ID.

        Args:
            feature_id: Feature ID to retrieve

        Returns:
            Feature dictionary or None if not found
        """
        if not self.connection:
            self.connect()

        try:
            cursor = self.connection.cursor()  # type: ignore[union-attr]
            cursor.execute(
                """
                SELECT * FROM features WHERE id = ?
            """,
                (feature_id,),
            )

            row = cursor.fetchone()
            return dict(row) if row else None
        except sqlite3.Error as e:
            logger.error(f"Error fetching feature: {e}")
            return None

    def get_features_by_status(self, status: str) -> list[dict[str, Any]]:
        """
        Get all features with a specific status.

        Args:
            status: Status to filter by

        Returns:
            List of feature dictionaries
        """
        if not self.connection:
            self.connect()

        try:
            cursor = self.connection.cursor()  # type: ignore[union-attr]
            cursor.execute(
                """
                SELECT * FROM features
                WHERE status = ?
                ORDER BY priority DESC, created_at DESC
            """,
                (status,),
            )

            rows = cursor.fetchall()
            return [dict(row) for row in rows]
        except sqlite3.Error as e:
            logger.error(f"Error querying features: {e}")
            return []

    def _ensure_session_exists(
        self, session_id: str, agent_id: str | None = None
    ) -> bool:
        """
        Ensure a session record exists in the database.

        Creates a placeholder session if it doesn't exist. Useful for
        handling foreign key constraints when recording delegations
        before the session is explicitly created.

        Args:
            session_id: Session ID to ensure exists
            agent_id: Agent assigned to session (optional, defaults to 'system')

        Returns:
            True if session exists or was created, False on error
        """
        if not self.connection:
            self.connect()

        try:
            cursor = self.connection.cursor()  # type: ignore[union-attr]

            # Check if session already exists
            cursor.execute("SELECT 1 FROM sessions WHERE session_id = ?", (session_id,))
            if cursor.fetchone():
                return True

            # Session doesn't exist, create placeholder
            cursor.execute(
                """
                INSERT INTO sessions
                (session_id, agent_assigned, status)
                VALUES (?, ?, 'active')
            """,
                (session_id, agent_id or "system"),
            )
            self.connection.commit()  # type: ignore[union-attr]
            return True

        except sqlite3.Error as e:
            # Session might exist but check failed, continue anyway
            logger.debug(f"Session creation warning: {e}")
            return False

    def record_collaboration(
        self,
        handoff_id: str,
        from_agent: str,
        to_agent: str,
        session_id: str,
        feature_id: str | None = None,
        handoff_type: str = "delegation",
        reason: str | None = None,
        context: dict[str, Any] | None = None,
    ) -> bool:
        """
        Record an agent handoff or collaboration event.

        Args:
            handoff_id: Unique handoff identifier
            from_agent: Agent handing off work
            to_agent: Agent receiving work
            session_id: Session this handoff occurs in
            feature_id: Feature being handed off (optional)
            handoff_type: Type of handoff (delegation, parallel, sequential, fallback)
            reason: Reason for handoff (optional)
            context: Additional context (optional)

        Returns:
            True if record successful, False otherwise
        """
        if not self.connection:
            self.connect()

        try:
            cursor = self.connection.cursor()  # type: ignore[union-attr]
            cursor.execute(
                """
                INSERT INTO agent_collaboration
                (handoff_id, from_agent, to_agent, session_id, feature_id,
                 handoff_type, reason, context)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?)
            """,
                (
                    handoff_id,
                    from_agent,
                    to_agent,
                    session_id,
                    feature_id,
                    handoff_type,
                    reason,
                    json.dumps(context) if context else None,
                ),
            )
            self.connection.commit()  # type: ignore[union-attr]
            return True
        except sqlite3.Error as e:
            logger.error(f"Error recording collaboration: {e}")
            return False

    def record_delegation_event(
        self,
        from_agent: str,
        to_agent: str,
        task_description: str,
        session_id: str | None = None,
        feature_id: str | None = None,
        context: dict[str, Any] | None = None,
    ) -> str | None:
        """
        Record a delegation event from one agent to another.

        This is a convenience method that wraps record_collaboration
        with sensible defaults for Task() delegation tracking.

        Handles foreign key constraints by creating placeholder session
        if it doesn't exist.

        Args:
            from_agent: Agent delegating work
            to_agent: Agent receiving work
            task_description: Description of the delegated task
            session_id: Session this delegation occurs in (optional, auto-creates if missing)
            feature_id: Feature being delegated (optional)
            context: Additional metadata (optional)

        Returns:
            Handoff ID if successful, None otherwise
        """
        import uuid

        if not self.connection:
            self.connect()

        # Auto-create session if not provided or doesn't exist
        if not session_id:
            session_id = f"session-{uuid.uuid4().hex[:8]}"

        # Ensure session exists (create placeholder if needed)
        self._ensure_session_exists(session_id, from_agent)

        handoff_id = f"hand-{uuid.uuid4().hex[:8]}"

        # Prepare context with task description
        delegation_context = context or {}
        delegation_context["task_description"] = task_description

        success = self.record_collaboration(
            handoff_id=handoff_id,
            from_agent=from_agent,
            to_agent=to_agent,
            session_id=session_id,
            feature_id=feature_id,
            handoff_type="delegation",
            reason=task_description,
            context=delegation_context,
        )

        return handoff_id if success else None

    def get_delegations(
        self,
        session_id: str | None = None,
        from_agent: str | None = None,
        to_agent: str | None = None,
        limit: int = 100,
    ) -> list[dict[str, Any]]:
        """
        Query delegation events from agent_collaboration table.

        Args:
            session_id: Filter by session (optional)
            from_agent: Filter by source agent (optional)
            to_agent: Filter by target agent (optional)
            limit: Maximum number of results

        Returns:
            List of delegation events as dictionaries
        """
        if not self.connection:
            self.connect()

        try:
            cursor = self.connection.cursor()  # type: ignore[union-attr]

            # Build WHERE clause
            where_clauses = ["handoff_type = 'delegation'"]
            params: list[str | int] = []

            if session_id:
                where_clauses.append("session_id = ?")
                params.append(session_id)
            if from_agent:
                where_clauses.append("from_agent = ?")
                params.append(from_agent)
            if to_agent:
                where_clauses.append("to_agent = ?")
                params.append(to_agent)

            where_sql = " AND ".join(where_clauses)

            # Query agent_collaboration table for delegations
            cursor.execute(
                f"""
                SELECT
                    handoff_id,
                    from_agent,
                    to_agent,
                    session_id,
                    feature_id,
                    handoff_type,
                    reason,
                    context,
                    timestamp
                FROM agent_collaboration
                WHERE {where_sql}
                ORDER BY timestamp DESC
                LIMIT ?
            """,
                params + [limit],
            )

            rows = cursor.fetchall()

            # Convert to dictionaries
            delegations = []
            for row in rows:
                row_dict = dict(row)
                delegations.append(row_dict)

            return delegations
        except sqlite3.Error as e:
            logger.error(f"Error querying delegations: {e}")
            return []

    def insert_collaboration(
        self,
        handoff_id: str,
        from_agent: str,
        to_agent: str,
        session_id: str,
        handoff_type: str = "delegation",
        reason: str | None = None,
        context: dict[str, Any] | None = None,
        status: str = "pending",
    ) -> bool:
        """
        Record an agent collaboration/delegation event.

        Args:
            handoff_id: Unique handoff identifier
            from_agent: Agent initiating the handoff
            to_agent: Target agent receiving the task
            session_id: Session this handoff belongs to
            handoff_type: Type of handoff (delegation, parallel, sequential, fallback)
            reason: Reason for the handoff (optional)
            context: Additional metadata as JSON (optional)
            status: Status of the handoff (pending, accepted, rejected, completed, failed)

        Returns:
            True if insert successful, False otherwise
        """
        if not self.connection:
            self.connect()

        try:
            cursor = self.connection.cursor()  # type: ignore[union-attr]
            cursor.execute(
                """
                INSERT INTO agent_collaboration
                (handoff_id, from_agent, to_agent, session_id, handoff_type,
                 reason, context, status)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?)
            """,
                (
                    handoff_id,
                    from_agent,
                    to_agent,
                    session_id,
                    handoff_type,
                    reason,
                    json.dumps(context) if context else None,
                    status,
                ),
            )
            self.connection.commit()  # type: ignore[union-attr]
            return True
        except sqlite3.Error as e:
            logger.error(f"Error inserting collaboration record: {e}")
            return False

    def insert_tool_trace(
        self,
        tool_use_id: str,
        trace_id: str,
        session_id: str,
        tool_name: str,
        tool_input: dict[str, Any] | None = None,
        start_time: str | None = None,
        parent_tool_use_id: str | None = None,
        parent_event_id: str | None = None,
    ) -> bool:
        """
        Insert a tool trace start event.

        Args:
            tool_use_id: Unique tool use identifier (UUID)
            trace_id: Parent trace ID for correlation
            session_id: Session this tool use belongs to
            tool_name: Name of the tool being executed
            tool_input: Tool input parameters as dict (optional)
            start_time: Start time ISO8601 UTC (optional, defaults to now)
            parent_tool_use_id: Parent tool use ID if nested (optional)
            parent_event_id: Parent event ID for attribution (optional)

        Returns:
            True if insert successful, False otherwise
        """
        if not self.connection:
            self.connect()

        try:
            cursor = self.connection.cursor()  # type: ignore[union-attr]

            if start_time is None:
                start_time = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M:%S")

            cursor.execute(
                """
                INSERT INTO tool_traces
                (tool_use_id, trace_id, session_id, tool_name, tool_input,
                 start_time, status, parent_tool_use_id, parent_event_id)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
                (
                    tool_use_id,
                    trace_id,
                    session_id,
                    tool_name,
                    json.dumps(tool_input) if tool_input else None,
                    start_time,
                    "started",
                    parent_tool_use_id,
                    parent_event_id,
                ),
            )
            self.connection.commit()  # type: ignore[union-attr]
            return True
        except sqlite3.Error as e:
            logger.error(f"Error inserting tool trace: {e}")
            return False

    def update_tool_trace(
        self,
        tool_use_id: str,
        tool_output: dict[str, Any] | None = None,
        end_time: str | None = None,
        duration_ms: int | None = None,
        status: str = "completed",
        error_message: str | None = None,
    ) -> bool:
        """
        Update tool trace with completion data.

        Args:
            tool_use_id: Tool use ID to update
            tool_output: Tool output result (optional)
            end_time: End time ISO8601 UTC (optional, defaults to now)
            duration_ms: Execution duration in milliseconds (optional)
            status: Final status (completed, failed, timeout, cancelled)
            error_message: Error message if failed (optional)

        Returns:
            True if update successful, False otherwise
        """
        if not self.connection:
            self.connect()

        try:
            cursor = self.connection.cursor()  # type: ignore[union-attr]

            if end_time is None:
                end_time = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M:%S")

            cursor.execute(
                """
                UPDATE tool_traces
                SET tool_output = ?, end_time = ?, duration_ms = ?,
                    status = ?, error_message = ?
                WHERE tool_use_id = ?
            """,
                (
                    json.dumps(tool_output) if tool_output else None,
                    end_time,
                    duration_ms,
                    status,
                    error_message,
                    tool_use_id,
                ),
            )
            self.connection.commit()  # type: ignore[union-attr]
            return True
        except sqlite3.Error as e:
            logger.error(f"Error updating tool trace: {e}")
            return False

    def get_tool_trace(self, tool_use_id: str) -> dict[str, Any] | None:
        """
        Get a tool trace by tool_use_id.

        Args:
            tool_use_id: Tool use ID to retrieve

        Returns:
            Tool trace dictionary or None if not found
        """
        if not self.connection:
            self.connect()

        try:
            cursor = self.connection.cursor()  # type: ignore[union-attr]
            cursor.execute(
                """
                SELECT * FROM tool_traces
                WHERE tool_use_id = ?
            """,
                (tool_use_id,),
            )

            row = cursor.fetchone()
            return dict(row) if row else None
        except sqlite3.Error as e:
            logger.error(f"Error fetching tool trace: {e}")
            return None

    def get_session_tool_traces(
        self, session_id: str, limit: int = 1000
    ) -> list[dict[str, Any]]:
        """
        Get all tool traces for a session ordered by start time DESC.

        Args:
            session_id: Session to query
            limit: Maximum number of results

        Returns:
            List of tool trace dictionaries
        """
        if not self.connection:
            self.connect()

        try:
            cursor = self.connection.cursor()  # type: ignore[union-attr]
            cursor.execute(
                """
                SELECT * FROM tool_traces
                WHERE session_id = ?
                ORDER BY start_time DESC
                LIMIT ?
            """,
                (session_id, limit),
            )

            rows = cursor.fetchall()
            return [dict(row) for row in rows]
        except sqlite3.Error as e:
            logger.error(f"Error querying tool traces: {e}")
            return []

    def update_session_activity(self, session_id: str, user_query: str) -> None:
        """
        Update session with latest user query activity.

        Args:
            session_id: Session ID to update
            user_query: The user query text (will be truncated to 200 chars)
        """
        if not self.connection:
            self.connect()

        try:
            cursor = self.connection.cursor()  # type: ignore[union-attr]
            cursor.execute(
                """
                UPDATE sessions
                SET last_user_query_at = ?, last_user_query = ?
                WHERE session_id = ?
            """,
                (
                    datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M:%S"),
                    user_query[:200] if user_query else None,
                    session_id,
                ),
            )
            self.connection.commit()  # type: ignore[union-attr]
        except sqlite3.Error as e:
            logger.error(f"Error updating session activity: {e}")

    def get_concurrent_sessions(
        self, current_session_id: str, minutes: int = 30
    ) -> list[dict[str, Any]]:
        """
        Get other sessions active in the last N minutes.

        Args:
            current_session_id: Current session ID to exclude from results
            minutes: Time window in minutes (default: 30)

        Returns:
            List of concurrent session dictionaries
        """
        if not self.connection:
            self.connect()

        try:
            cursor = self.connection.cursor()  # type: ignore[union-attr]
            cutoff = (datetime.now(timezone.utc) - timedelta(minutes=minutes)).strftime(
                "%Y-%m-%d %H:%M:%S"
            )
            cursor.execute(
                """
                SELECT session_id, agent_assigned, created_at, last_user_query_at,
                       last_user_query, status
                FROM sessions
                WHERE session_id != ?
                  AND status = 'active'
                  AND (last_user_query_at > ? OR created_at > ?)
                ORDER BY last_user_query_at DESC
            """,
                (current_session_id, cutoff, cutoff),
            )

            rows = cursor.fetchall()
            return [dict(row) for row in rows]
        except sqlite3.Error as e:
            logger.error(f"Error querying concurrent sessions: {e}")
            return []

    def insert_live_event(
        self,
        event_type: str,
        event_data: dict[str, Any],
        parent_event_id: str | None = None,
        session_id: str | None = None,
        spawner_type: str | None = None,
    ) -> int | None:
        """
        Insert a live event for real-time WebSocket streaming.

        These events are temporary and should be cleaned up after broadcast.

        Args:
            event_type: Type of live event (spawner_start, spawner_phase, spawner_complete, etc.)
            event_data: Event payload as dictionary (will be JSON serialized)
            parent_event_id: Parent event ID for hierarchical linking (optional)
            session_id: Session this event belongs to (optional)
            spawner_type: Spawner type (gemini, codex, copilot) if applicable (optional)

        Returns:
            Live event ID if successful, None otherwise
        """
        if not self.connection:
            self.connect()

        try:
            cursor = self.connection.cursor()  # type: ignore[union-attr]
            cursor.execute(
                """
                INSERT INTO live_events
                (event_type, event_data, parent_event_id, session_id, spawner_type)
                VALUES (?, ?, ?, ?, ?)
            """,
                (
                    event_type,
                    json.dumps(event_data),
                    parent_event_id,
                    session_id,
                    spawner_type,
                ),
            )
            self.connection.commit()  # type: ignore[union-attr]
            return cursor.lastrowid
        except sqlite3.Error as e:
            logger.error(f"Error inserting live event: {e}")
            return None

    def get_pending_live_events(self, limit: int = 100) -> list[dict[str, Any]]:
        """
        Get live events that haven't been broadcast yet.

        Args:
            limit: Maximum number of events to return

        Returns:
            List of pending live event dictionaries
        """
        if not self.connection:
            self.connect()

        try:
            cursor = self.connection.cursor()  # type: ignore[union-attr]
            cursor.execute(
                """
                SELECT id, event_type, event_data, parent_event_id, session_id,
                       spawner_type, created_at
                FROM live_events
                WHERE broadcast_at IS NULL
                ORDER BY created_at ASC
                LIMIT ?
            """,
                (limit,),
            )

            rows = cursor.fetchall()
            events = []
            for row in rows:
                event = dict(row)
                # Parse JSON event_data
                if event.get("event_data"):
                    try:
                        event["event_data"] = json.loads(event["event_data"])
                    except json.JSONDecodeError:
                        pass
                events.append(event)
            return events
        except sqlite3.Error as e:
            logger.error(f"Error fetching pending live events: {e}")
            return []

    def mark_live_events_broadcast(self, event_ids: list[int]) -> bool:
        """
        Mark live events as broadcast (sets broadcast_at timestamp).

        Args:
            event_ids: List of live event IDs to mark as broadcast

        Returns:
            True if successful, False otherwise
        """
        if not self.connection or not event_ids:
            return False

        try:
            cursor = self.connection.cursor()  # type: ignore[union-attr]
            placeholders = ",".join("?" for _ in event_ids)
            cursor.execute(
                f"""
                UPDATE live_events
                SET broadcast_at = CURRENT_TIMESTAMP
                WHERE id IN ({placeholders})
            """,
                event_ids,
            )
            self.connection.commit()  # type: ignore[union-attr]
            return True
        except sqlite3.Error as e:
            logger.error(f"Error marking live events as broadcast: {e}")
            return False

    def cleanup_old_live_events(self, max_age_minutes: int = 5) -> int:
        """
        Delete live events that have been broadcast and are older than max_age_minutes.

        Args:
            max_age_minutes: Maximum age in minutes for broadcast events

        Returns:
            Number of deleted events
        """
        if not self.connection:
            self.connect()

        try:
            cursor = self.connection.cursor()  # type: ignore[union-attr]
            cutoff = (
                datetime.now(timezone.utc) - timedelta(minutes=max_age_minutes)
            ).strftime("%Y-%m-%d %H:%M:%S")
            cursor.execute(
                """
                DELETE FROM live_events
                WHERE broadcast_at IS NOT NULL
                  AND created_at < ?
            """,
                (cutoff,),
            )
            deleted_count = cursor.rowcount
            self.connection.commit()  # type: ignore[union-attr]
            return deleted_count
        except sqlite3.Error as e:
            logger.error(f"Error cleaning up old live events: {e}")
            return 0

    def get_events_for_task(self, claude_task_id: str) -> list[dict[str, Any]]:
        """
        Get all events (and their descendants) for a Claude Code task.

        This enables answering "show me all the work (tool calls) that happened
        when this Task() was delegated".

        Args:
            claude_task_id: Claude Code's internal task ID

        Returns:
            List of event dictionaries, ordered by timestamp
        """
        if not self.connection:
            self.connect()

        try:
            cursor = self.connection.cursor()  # type: ignore[union-attr]
            cursor.execute(
                """
                WITH task_events AS (
                    SELECT event_id FROM agent_events
                    WHERE claude_task_id = ?
                )
                SELECT ae.* FROM agent_events ae
                WHERE ae.claude_task_id = ?
                   OR ae.parent_event_id IN (
                       SELECT event_id FROM task_events
                   )
                ORDER BY ae.created_at
            """,
                (claude_task_id, claude_task_id),
            )

            rows = cursor.fetchall()
            return [dict(row) for row in rows]
        except sqlite3.Error as e:
            logger.error(f"Error querying events for task: {e}")
            return []

    def get_subagent_work(self, session_id: str) -> dict[str, list[dict[str, Any]]]:
        """
        Get all work grouped by which subagent did it.

        This enables answering "which subagent did what work in this session?"

        Args:
            session_id: Session ID to analyze

        Returns:
            Dictionary mapping subagent_type to list of events they executed.
            Example: {
                'researcher': [
                    {'tool_name': 'Read', 'input_summary': '...', ...},
                    {'tool_name': 'Grep', 'input_summary': '...', ...}
                ],
                'general-purpose': [
                    {'tool_name': 'Bash', 'input_summary': '...', ...}
                ]
            }
        """
        if not self.connection:
            self.connect()

        try:
            cursor = self.connection.cursor()  # type: ignore[union-attr]
            cursor.execute(
                """
                SELECT
                    ae.subagent_type,
                    ae.tool_name,
                    ae.event_id,
                    ae.input_summary,
                    ae.output_summary,
                    ae.created_at,
                    ae.claude_task_id
                FROM agent_events ae
                WHERE ae.session_id = ?
                  AND ae.subagent_type IS NOT NULL
                  AND ae.event_type = 'tool_call'
                ORDER BY ae.subagent_type, ae.created_at
            """,
                (session_id,),
            )

            # Group by subagent_type
            result: dict[str, list[dict[str, Any]]] = {}
            for row in cursor.fetchall():
                row_dict = dict(row)
                subagent = row_dict.pop("subagent_type")
                if subagent not in result:
                    result[subagent] = []
                result[subagent].append(row_dict)

            return result
        except sqlite3.Error as e:
            logger.error(f"Error querying subagent work: {e}")
            return {}

    def insert_sync_operation(
        self,
        sync_id: str,
        operation: str,
        status: str,
        timestamp: str,
        files_changed: int = 0,
        conflicts: list[str] | None = None,
        message: str | None = None,
        hostname: str | None = None,
    ) -> bool:
        """
        Record a sync operation in the database.

        Args:
            sync_id: Unique sync operation ID
            operation: Operation type (push, pull)
            status: Sync status (idle, pushing, pulling, success, error, conflict)
            timestamp: Operation timestamp
            files_changed: Number of files changed
            conflicts: List of conflicted files (optional)
            message: Status message (optional)
            hostname: Hostname that performed the sync (optional)

        Returns:
            True if insert successful, False otherwise
        """
        if not self.connection:
            self.connect()

        try:
            cursor = self.connection.cursor()  # type: ignore[union-attr]
            cursor.execute(
                """
                INSERT INTO sync_operations
                (sync_id, operation, status, timestamp, files_changed, conflicts,
                 message, hostname)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?)
            """,
                (
                    sync_id,
                    operation,
                    status,
                    timestamp,
                    files_changed,
                    json.dumps(conflicts) if conflicts else None,
                    message,
                    hostname,
                ),
            )
            self.connection.commit()  # type: ignore[union-attr]
            return True
        except sqlite3.Error as e:
            logger.error(f"Error inserting sync operation: {e}")
            return False

    def get_sync_operations(
        self, limit: int = 100, operation: str | None = None
    ) -> list[dict[str, Any]]:
        """
        Get recent sync operations.

        Args:
            limit: Maximum number of results
            operation: Filter by operation type (optional)

        Returns:
            List of sync operation dictionaries
        """
        if not self.connection:
            self.connect()

        try:
            cursor = self.connection.cursor()  # type: ignore[union-attr]

            if operation:
                cursor.execute(
                    """
                    SELECT * FROM sync_operations
                    WHERE operation = ?
                    ORDER BY timestamp DESC
                    LIMIT ?
                """,
                    (operation, limit),
                )
            else:
                cursor.execute(
                    """
                    SELECT * FROM sync_operations
                    ORDER BY timestamp DESC
                    LIMIT ?
                """,
                    (limit,),
                )

            rows = cursor.fetchall()
            results = []
            for row in rows:
                row_dict = dict(row)
                # Parse JSON conflicts
                if row_dict.get("conflicts"):
                    try:
                        row_dict["conflicts"] = json.loads(row_dict["conflicts"])
                    except json.JSONDecodeError:
                        pass
                results.append(row_dict)
            return results
        except sqlite3.Error as e:
            logger.error(f"Error querying sync operations: {e}")
            return []

    def close(self) -> None:
        """Clean up database connection."""
        self.disconnect()
