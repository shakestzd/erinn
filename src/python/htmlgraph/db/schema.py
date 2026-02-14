"""
HtmlGraph SQLite Schema - Database Manager

HtmlGraphDB is the single entry point for all database operations.

Core CRUD (session, event, feature) is defined directly on the class.
Auxiliary operations (traces, collaboration, sync) are inherited from
ExtensionOps in extensions.py.

DDL (tables, indexes, migrations) is delegated to ddl.py.
"""

from __future__ import annotations

import json
import logging
import sqlite3
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any

from htmlgraph.db.ddl import (
    create_all_indexes,
    create_all_tables,
    migrate_agent_events,
    migrate_sessions,
    run_data_migrations,
)
from htmlgraph.db.extensions import ExtensionOps

logger = logging.getLogger(__name__)


class HtmlGraphDB(ExtensionOps):
    """
    SQLite database manager for HtmlGraph observability backend.

    Provides schema creation, migrations, and query helpers for storing
    and retrieving agent events, features, sessions, and collaborations.

    Core CRUD methods (defined here):
    - Session: insert_session, _ensure_session_exists, update_session_activity,
               get_concurrent_sessions
    - Event: insert_event, get_session_events, get_events_for_task,
             get_subagent_work
    - Feature: insert_feature, update_feature_status, get_feature_by_id,
               get_features_by_status

    Auxiliary methods (inherited from ExtensionOps):
    - Traces: insert_tool_trace, update_tool_trace, get_tool_trace,
              get_session_tool_traces
    - Live events: insert_live_event, get_pending_live_events,
                   mark_live_events_broadcast, cleanup_old_live_events
    - Collaboration: record_collaboration, record_delegation_event,
                     get_delegations, insert_collaboration
    - Sync: insert_sync_operation, get_sync_operations
    """

    def __init__(self, db_path: str | None = None):
        """
        Initialize HtmlGraph database.

        Args:
            db_path: Path to SQLite database file. If None, uses default location.
        """
        if db_path is None:
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
        self.connection.execute("PRAGMA foreign_keys = ON")
        return self.connection

    def disconnect(self) -> None:
        """Close database connection."""
        if self.connection:
            self.connection.close()
            self.connection = None

    def create_tables(self) -> None:
        """
        Create all required tables in SQLite database.

        Runs migrations for existing tables, then creates tables,
        and finally creates indexes for performance optimization.
        """
        if not self.connection:
            self.connect()

        cursor = self.connection.cursor()  # type: ignore[union-attr]

        # Run migrations for existing tables before creating new ones
        migrate_agent_events(cursor)
        migrate_sessions(cursor)

        # Run data migrations to normalize existing data
        run_data_migrations(cursor)

        # Create all tables (IF NOT EXISTS, safe to re-run)
        create_all_tables(cursor)

        # Create indexes for performance
        create_all_indexes(cursor)

        if self.connection:
            self.connection.commit()
        logger.info(f"SQLite schema created at {self.db_path}")

    def close(self) -> None:
        """Clean up database connection."""
        self.disconnect()

    # ------------------------------------------------------------------
    # Session CRUD
    # ------------------------------------------------------------------

    def insert_session(
        self,
        session_id: str,
        agent_assigned: str,
        parent_session_id: str | None = None,
        parent_event_id: str | None = None,
        is_subagent: bool = False,
        transcript_id: str | None = None,
        transcript_path: str | None = None,
    ) -> bool:
        """
        Insert a new session record.

        Gracefully handles FOREIGN KEY constraint failures by retrying without
        the parent_event_id or parent_session_id reference.

        Args:
            session_id: Unique session identifier
            agent_assigned: Primary agent for this session
            parent_session_id: Parent session if subagent (optional)
            parent_event_id: Event that spawned this session (optional)
            is_subagent: Whether this is a subagent session
            transcript_id: ID of Claude transcript (optional)
            transcript_path: Path to transcript file (optional)

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
                 is_subagent, transcript_id, transcript_path)
                VALUES (?, ?, ?, ?, ?, ?, ?)
            """,
                (
                    session_id,
                    agent_assigned,
                    parent_session_id,
                    parent_event_id,
                    is_subagent,
                    transcript_id,
                    transcript_path,
                ),
            )
            self.connection.commit()  # type: ignore[union-attr]
            return True
        except sqlite3.IntegrityError as e:
            if "FOREIGN KEY constraint failed" in str(e) and (
                parent_event_id or parent_session_id
            ):
                logger.warning(
                    "Parent session/event not found, creating session without parent link"
                )
                try:
                    cursor = self.connection.cursor()  # type: ignore[union-attr]
                    cursor.execute(
                        """
                        INSERT OR IGNORE INTO sessions
                        (session_id, agent_assigned, parent_session_id, parent_event_id,
                         is_subagent, transcript_id, transcript_path)
                        VALUES (?, ?, ?, ?, ?, ?, ?)
                    """,
                        (
                            session_id,
                            agent_assigned,
                            None,
                            None,
                            is_subagent,
                            transcript_id,
                            transcript_path,
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

            cursor.execute("SELECT 1 FROM sessions WHERE session_id = ?", (session_id,))
            if cursor.fetchone():
                return True

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
            logger.debug(f"Session creation warning: {e}")
            return False

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
                    datetime.now(timezone.utc).isoformat(),
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
            cutoff = (
                datetime.now(timezone.utc) - timedelta(minutes=minutes)
            ).isoformat()
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

    # ------------------------------------------------------------------
    # Event CRUD
    # ------------------------------------------------------------------

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
    ) -> bool:
        """
        Insert an agent event into the database.

        Temporarily disables foreign key constraints to allow inserting events
        even if the parent event doesn't exist yet (useful for cross-process
        or distributed event tracking).

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
            model: Claude model name (optional)
            feature_id: Feature ID this event relates to (optional)
            claude_task_id: Claude Code's internal task ID (optional)

        Returns:
            True if insert successful, False otherwise
        """
        if not self.connection:
            self.connect()

        try:
            cursor = self.connection.cursor()  # type: ignore[union-attr]
            cursor.execute("PRAGMA foreign_keys=OFF")
            cursor.execute(
                """
                INSERT INTO agent_events
                (event_id, agent_id, event_type, session_id, feature_id, tool_name,
                 input_summary, tool_input, output_summary, context, parent_agent_id,
                 parent_event_id, cost_tokens, execution_duration_seconds, subagent_type, model, claude_task_id)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
                ),
            )
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
            logger.error(f"Error inserting event: {e}")
            return False
        except sqlite3.Error as e:
            logger.error(f"Error inserting event: {e}")
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
                ORDER BY datetime(REPLACE(SUBSTR(timestamp, 1, 19), 'T', ' ')) DESC
            """,
                (session_id,),
            )

            rows = cursor.fetchall()
            return [dict(row) for row in rows]
        except sqlite3.Error as e:
            logger.error(f"Error querying events: {e}")
            return []

    def get_events_for_task(self, claude_task_id: str) -> list[dict[str, Any]]:
        """
        Get all events (and their descendants) for a Claude Code task.

        Enables answering "show me all the work (tool calls) that happened
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

        Enables answering "which subagent did what work in this session?"

        Args:
            session_id: Session ID to analyze

        Returns:
            Dictionary mapping subagent_type to list of events they executed.
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

    # ------------------------------------------------------------------
    # Feature CRUD
    # ------------------------------------------------------------------

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
