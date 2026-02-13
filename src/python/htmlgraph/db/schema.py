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

from htmlgraph.db.indexes import create_all_indexes
from htmlgraph.db.migrations import (
    migrate_agent_events,
    migrate_sessions,
    run_data_migrations,
)
from htmlgraph.db.tables import create_all_tables

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
        migrate_agent_events(cursor)
        migrate_sessions(cursor)

        # Run data migrations to normalize existing data
        run_data_migrations(cursor)

        # Create all tables using extracted module
        create_all_tables(cursor)

        # Create all indexes using extracted module
        create_all_indexes(cursor)

        if self.connection:
            self.connection.commit()
        logger.info(f"SQLite schema created at {self.db_path}")

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
                         is_subagent, transcript_id, transcript_path)
                        VALUES (?, ?, ?, ?, ?, ?, ?)
                    """,
                        (
                            session_id,
                            agent_assigned,
                            None,  # Drop parent_session_id
                            None,  # Drop parent_event_id
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
                ORDER BY datetime(REPLACE(SUBSTR(timestamp, 1, 19), 'T', ' ')) DESC
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
                ORDER BY datetime(REPLACE(SUBSTR(timestamp, 1, 19), 'T', ' ')) DESC
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

        Returns:
            True if insert successful, False otherwise
        """
        if not self.connection:
            self.connect()

        try:
            cursor = self.connection.cursor()  # type: ignore[union-attr]

            if start_time is None:
                start_time = datetime.now(timezone.utc).isoformat()

            cursor.execute(
                """
                INSERT INTO tool_traces
                (tool_use_id, trace_id, session_id, tool_name, tool_input,
                 start_time, status, parent_tool_use_id)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?)
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
                end_time = datetime.now(timezone.utc).isoformat()

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
            ).isoformat()
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
                    ORDER BY datetime(REPLACE(SUBSTR(timestamp, 1, 19), 'T', ' ')) DESC
                    LIMIT ?
                """,
                    (operation, limit),
                )
            else:
                cursor.execute(
                    """
                    SELECT * FROM sync_operations
                    ORDER BY datetime(REPLACE(SUBSTR(timestamp, 1, 19), 'T', ' ')) DESC
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
