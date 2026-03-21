"""
SubagentStop Hook - Update parent events when subagents complete.

This module handles the SubagentStop hook event, which fires when a subagent
(spawned via Task()) completes. It updates the parent event with completion
status and counts child spikes created during the subagent's execution.

Architecture:
- Reads HTMLGRAPH_PARENT_EVENT from environment (set by PreToolUse hook)
- Queries database for spikes created since parent event start
- Updates parent event: status="completed", child_spike_count=N
- Handles graceful degradation if parent event not found

Parent-Child Event Nesting:
- Parent: evt-abc (Task delegation) created by PreToolUse
- Child events: spikes created by subagent during task execution
- Result: Full trace of delegation work visible in dashboard
"""

import json
import logging
import os
import sqlite3
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any
from uuid import uuid4

logger = logging.getLogger(__name__)


def get_parent_event_id() -> str | None:
    """
    Get the parent event ID from environment.

    Set by PreToolUse hook when Task() is detected.

    Returns:
        Parent event ID (evt-XXXXX) or None if not found
    """
    return os.environ.get("HTMLGRAPH_PARENT_EVENT")


def get_session_id() -> str | None:
    """
    Get the current session ID from environment.

    Set by SessionStart hook.

    Returns:
        Session ID or None if not found
    """
    return os.environ.get("HTMLGRAPH_SESSION_ID")


def count_child_spikes(
    db_path: str, parent_event_id: str, parent_start_time: str
) -> int:
    """
    Count spikes created after the parent event started.

    Queries the features table for spikes with created_at > parent start time.
    Uses a narrow time window (5 minutes) to avoid counting unrelated spikes
    from other sessions.

    Args:
        db_path: Path to SQLite database
        parent_event_id: Parent event ID
        parent_start_time: ISO8601 timestamp when parent event started

    Returns:
        Count of child spikes (0 if none found)
    """
    try:
        conn = sqlite3.connect(db_path)
        cursor = conn.cursor()

        # Validate parent start time format (ISO8601)
        try:
            datetime.fromisoformat(parent_start_time)
        except (ValueError, TypeError):
            # If parsing fails, return 0 (couldn't validate time window)
            logger.warning(f"Could not parse parent start time: {parent_start_time}")
            return 0

        # Query spikes created within 5 minutes after parent event
        # This avoids counting unrelated spikes from other sessions
        query = """
            SELECT COUNT(*) FROM features
            WHERE type = 'spike'
            AND created_at >= ?
            AND created_at <= datetime(?, '+5 minutes')
        """

        cursor.execute(query, (parent_start_time, parent_start_time))
        result = cursor.fetchone()
        count = result[0] if result else 0

        conn.close()
        logger.debug(f"Found {count} child spikes for parent event {parent_event_id}")
        return count

    except Exception as e:
        logger.warning(f"Error counting child spikes: {e}")
        return 0


def update_parent_event(
    db_path: str,
    parent_event_id: str,
    child_spike_count: int,
    completion_time: str | None = None,
    last_assistant_message: str | None = None,
) -> bool:
    """
    Update parent event with completion status and child spike count.

    Updates agent_events table:
    - status: "started" → "completed"
    - child_spike_count: Count of spikes created by subagent
    - output_summary: JSON with completion info (includes last_assistant_message when present)

    Args:
        db_path: Path to SQLite database
        parent_event_id: Parent event ID to update
        child_spike_count: Number of child spikes created
        completion_time: ISO8601 timestamp (optional, defaults to now)
        last_assistant_message: Final assistant message from Stop/SubagentStop hook input

    Returns:
        True if update successful, False otherwise
    """
    try:
        if completion_time is None:
            completion_time = datetime.now(timezone.utc).isoformat()

        conn = sqlite3.connect(db_path)
        cursor = conn.cursor()

        # Build output summary including last_assistant_message when present
        summary_data: dict[str, Any] = {
            "status": "completed",
            "child_spike_count": child_spike_count,
            "completion_time": completion_time,
        }
        if last_assistant_message:
            summary_data["last_assistant_message"] = last_assistant_message[:2000]

        output_summary = json.dumps(summary_data)

        # Update parent event
        query = """
            UPDATE agent_events
            SET status = ?, child_spike_count = ?, output_summary = ?, updated_at = CURRENT_TIMESTAMP
            WHERE event_id = ?
        """

        cursor.execute(
            query,
            ("completed", child_spike_count, output_summary, parent_event_id),
        )

        if cursor.rowcount == 0:
            logger.warning(f"Parent event not found: {parent_event_id}")
            conn.close()
            return False

        conn.commit()
        conn.close()

        logger.info(
            f"Updated parent event {parent_event_id}: "
            f"status=completed, child_spike_count={child_spike_count}"
        )
        return True

    except Exception as e:
        logger.warning(f"Error updating parent event: {e}")
        return False


def get_parent_event_start_time(db_path: str, parent_event_id: str) -> str | None:
    """
    Get the start time of the parent event.

    Used to set the time window for counting child spikes.

    Args:
        db_path: Path to SQLite database
        parent_event_id: Parent event ID

    Returns:
        ISO8601 timestamp or None if not found
    """
    try:
        conn = sqlite3.connect(db_path)
        cursor = conn.cursor()

        query = "SELECT timestamp FROM agent_events WHERE event_id = ?"
        cursor.execute(query, (parent_event_id,))
        result = cursor.fetchone()

        conn.close()
        return result[0] if result else None

    except Exception as e:
        logger.warning(f"Error getting parent event start time: {e}")
        return None


def get_parent_event_from_db(
    db_path: str,
    agent_id: str | None = None,
    session_id: str | None = None,
) -> str | None:
    """
    Query database for the task_delegation event that spawned this subagent.

    Prefers exact lookup by agent_id (native Claude Code field) when available,
    falling back to most-recent heuristic only when agent_id is absent.

    Used when HTMLGRAPH_PARENT_EVENT environment variable is not available
    (due to inter-process communication limitations).

    SESSION_ID CORRECTNESS NOTE (roborev-259, Finding 1):
    Claude Code passes the PARENT/orchestrator session_id to SubagentStop hooks,
    NOT a separate subagent session_id. This is confirmed by Claude Code behavior:
    "All subagents share the same session_id" (see subagent-stop.py plugin docs).
    Since task_delegation rows are written by PreToolUse in the parent session,
    they carry the same session_id. Therefore the session_id filter correctly
    scopes to the current orchestrator session, avoiding cross-session pollution.

    Args:
        db_path: Path to SQLite database
        agent_id: Native agent_id from hook input (e.g. subagent UUID). Used for
                  exact task_delegation lookup to avoid mis-attribution when
                  multiple subagents run in parallel.
        session_id: Current session ID used to scope queries and avoid matching
                    stale task_delegation rows from previous sessions.

    Returns:
        Parent event ID (evt-XXXXX) or None if not found
    """
    try:
        conn = sqlite3.connect(db_path)
        cursor = conn.cursor()

        if agent_id:
            # Exact lookup: find the task_delegation whose agent_id matches.
            # PreToolUse stores agent_id on task_delegation events so we can
            # correlate SubagentStop back to the right parent even in parallel.
            # Scope to current session when available to avoid cross-session pollution.
            if session_id:
                cursor.execute(
                    """
                    SELECT event_id FROM agent_events
                    WHERE event_type = 'task_delegation'
                      AND agent_id = ?
                      AND status = 'started'
                      AND session_id = ?
                    ORDER BY timestamp DESC
                    LIMIT 1
                    """,
                    (agent_id, session_id),
                )
            else:
                cursor.execute(
                    """
                    SELECT event_id FROM agent_events
                    WHERE event_type = 'task_delegation'
                      AND agent_id = ?
                      AND status = 'started'
                    ORDER BY timestamp DESC
                    LIMIT 1
                    """,
                    (agent_id,),
                )
        else:
            # Fallback heuristic: most recent started task_delegation.
            # Scope to current session when available to avoid cross-session pollution.
            # Only correct for single-subagent scenarios.
            if session_id:
                cursor.execute(
                    """
                    SELECT event_id FROM agent_events
                    WHERE event_type = 'task_delegation'
                      AND status = 'started'
                      AND session_id = ?
                    ORDER BY timestamp DESC
                    LIMIT 1
                    """,
                    (session_id,),
                )
            else:
                cursor.execute(
                    """
                    SELECT event_id FROM agent_events
                    WHERE event_type = 'task_delegation'
                      AND status = 'started'
                    ORDER BY timestamp DESC
                    LIMIT 1
                    """
                )

        result = cursor.fetchone()
        conn.close()

        if result:
            parent_event_id: str = result[0]
            logger.debug(
                f"Found parent task_delegation from database: {parent_event_id}"
                + (f" (agent_id={agent_id})" if agent_id else " (heuristic)")
                + (f" (session_id={session_id})" if session_id else "")
            )
            return parent_event_id

        logger.debug("No active task_delegation found in database")
        return None

    except Exception as e:
        logger.warning(f"Error querying for parent event: {e}")
        return None


def handle_subagent_stop(hook_input: dict[str, Any]) -> dict[str, Any]:
    """
    Handle SubagentStop hook event.

    When a subagent completes, updates the parent event with:
    1. Completion status
    2. Count of spikes created during subagent execution
    3. Completion timestamp

    This closes the parent-child event trace and enables dashboard visualization
    of the complete delegation hierarchy.

    Args:
        hook_input: Hook input data from Claude Code

    Returns:
        Response: {"continue": True} with optional context
    """
    # Try to get parent event ID from environment (set by PreToolUse hook)
    parent_event_id = get_parent_event_id()

    # If not available in environment, query database
    # (environment variables may not be inherited across subagent process boundary)
    # Get project directory and database path (reuse for both env and db lookup)
    db_path = None
    try:
        from htmlgraph.config import get_database_path

        cwd = hook_input.get("cwd", os.getcwd())
        db_path = str(get_database_path(cwd))

        if not Path(db_path).exists():
            logger.warning(f"Database not found: {db_path}")
            return {"continue": True}

    except Exception as e:
        logger.warning(f"Error resolving database path: {e}")
        return {"continue": True}

    # If parent event ID not in environment, query database.
    # Pass agent_id from hook input for exact lookup (avoids mis-attribution
    # when multiple subagents run in parallel).
    # Pass session_id to scope query to the current session only, preventing
    # stale task_delegation rows from old sessions from being matched.
    if not parent_event_id:
        logger.debug("Parent event ID not in environment, querying database...")
        native_agent_id = hook_input.get("agent_id") or None
        native_session_id = hook_input.get("session_id") or get_session_id() or None
        try:
            parent_event_id = get_parent_event_from_db(
                db_path, agent_id=native_agent_id, session_id=native_session_id
            )
        except Exception as e:
            logger.debug(f"Could not query database for parent event: {e}")

    if not parent_event_id:
        logger.debug(
            "No parent event ID found (env or db), skipping subagent stop tracking"
        )
        return {"continue": True}

    # Get parent event start time
    parent_start_time = get_parent_event_start_time(db_path, parent_event_id)
    if not parent_start_time:
        logger.warning(f"Could not find parent event: {parent_event_id}")
        return {"continue": True}

    # Count child spikes
    child_spike_count = count_child_spikes(db_path, parent_event_id, parent_start_time)

    # Extract last_assistant_message if provided by Claude Code (Stop/SubagentStop hook input)
    last_assistant_message = hook_input.get("last_assistant_message") or None
    if last_assistant_message and not isinstance(last_assistant_message, str):
        last_assistant_message = str(last_assistant_message)

    # Update parent event with completion info
    completion_time = datetime.now(timezone.utc).isoformat()
    success = update_parent_event(
        db_path,
        parent_event_id,
        child_spike_count,
        completion_time,
        last_assistant_message=last_assistant_message,
    )

    if success:
        # Write a task_completed marker event to the database.
        # This is the PRIMARY signal for PostToolUse to know the task is done.
        # NOTE: os.environ.pop("HTMLGRAPH_PARENT_EVENT") is dead code here —
        # each hook runs as a separate subprocess, so env var changes never
        # propagate back to the parent process or other hooks.
        # The task_completed marker row is the reliable inter-hook signal.
        agent_id = hook_input.get("agent_id") or "claude-code"
        session_id = hook_input.get("session_id") or get_session_id() or ""
        try:
            _marker_conn = sqlite3.connect(db_path)
            _marker_cursor = _marker_conn.cursor()

            # Resolve feature_id from the session row so TaskCompleted events
            # carry the same attribution as the task_delegation that spawned them.
            feature_id = None
            try:
                row = _marker_cursor.execute(
                    "SELECT active_feature_id FROM sessions WHERE session_id = ?",
                    (session_id,),
                ).fetchone()
                if row and row[0]:
                    feature_id = row[0]
            except Exception:
                pass

            _marker_cursor.execute(
                """
                INSERT INTO agent_events (
                    event_id, session_id, event_type, tool_name,
                    parent_event_id, agent_id, timestamp, status, feature_id
                ) VALUES (?, ?, 'task_completed', 'TaskCompleted', ?, ?, ?, 'completed', ?)
                """,
                (
                    f"evt-tc-{uuid4().hex[:8]}",
                    session_id,
                    parent_event_id,
                    agent_id,
                    completion_time,
                    feature_id,
                ),
            )
            _marker_conn.commit()
            _marker_conn.close()
            logger.debug(
                f"Wrote task_completed marker for parent_event={parent_event_id}"
            )
        except Exception as _e:
            logger.warning(f"Could not write task_completed marker: {_e}")

        logger.info(
            f"Subagent stop recorded: parent_event={parent_event_id}, "
            f"child_spikes={child_spike_count}"
        )

        return {
            "continue": True,
            "hookSpecificOutput": {
                "hookEventName": "SubagentStop",
                "additionalContext": (
                    f"Task delegation completed: {child_spike_count} spike(s) created"
                ),
            },
        }

    return {"continue": True}


def main() -> None:
    """Hook entry point for script wrapper."""
    # Check if tracking is disabled
    if os.environ.get("HTMLGRAPH_DISABLE_TRACKING") == "1":
        print(json.dumps({"continue": True}))
        sys.exit(0)

    # Read hook input from stdin
    try:
        hook_input = json.load(sys.stdin)
    except json.JSONDecodeError:
        hook_input = {}

    # Handle subagent stop
    result = handle_subagent_stop(hook_input)

    # Output response
    print(json.dumps(result))
    sys.exit(0)


if __name__ == "__main__":
    main()
