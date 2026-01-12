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

logger = logging.getLogger(__name__)


def get_parent_event_id() -> str | None:
    """
    Get the parent event ID from environment.

    Set by PreToolUse hook when Task() is detected.

    NOTE: This relies on environment variables which DON'T persist between
    hook invocations (each hook is a new subprocess). This function is kept
    for backward compatibility but will almost always return None.
    Use get_parent_event_id_from_database() instead.

    Returns:
        Parent event ID (evt-XXXXX) or None if not found
    """
    return os.environ.get("HTMLGRAPH_PARENT_EVENT")


def get_parent_event_id_from_database(db_path: str) -> tuple[str | None, str | None]:
    """
    Get the parent event ID by querying the database for active task_delegation events.

    This is the reliable method for finding parent events since environment variables
    don't persist between hook invocations.

    Args:
        db_path: Path to SQLite database

    Returns:
        Tuple of (parent_event_id, parent_start_time) or (None, None) if not found
    """
    try:
        conn = sqlite3.connect(db_path)
        cursor = conn.cursor()

        # Find the most recent task_delegation event that's still 'started'
        cursor.execute(
            """
            SELECT event_id, timestamp
            FROM agent_events
            WHERE event_type = 'task_delegation'
            AND status = 'started'
            AND timestamp >= datetime('now', '-10 minutes')
            ORDER BY timestamp DESC
            LIMIT 1
            """
        )
        row = cursor.fetchone()
        conn.close()

        if row:
            logger.debug(f"Found active task_delegation from database: {row[0]}")
            return row[0], row[1]

        logger.debug("No active task_delegation found in database")
        return None, None

    except Exception as e:
        logger.warning(f"Error querying database for parent event: {e}")
        return None, None


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
) -> bool:
    """
    Update parent event with completion status and child spike count.

    Updates agent_events table:
    - status: "started" → "completed"
    - child_spike_count: Count of spikes created by subagent
    - output_summary: JSON with completion info

    Args:
        db_path: Path to SQLite database
        parent_event_id: Parent event ID to update
        child_spike_count: Number of child spikes created
        completion_time: ISO8601 timestamp (optional, defaults to now)

    Returns:
        True if update successful, False otherwise
    """
    try:
        if completion_time is None:
            completion_time = datetime.now(timezone.utc).isoformat()

        conn = sqlite3.connect(db_path)
        cursor = conn.cursor()

        # Build output summary
        output_summary = json.dumps(
            {
                "status": "completed",
                "child_spike_count": child_spike_count,
                "completion_time": completion_time,
            }
        )

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
    # Get project directory and database path first (needed for database-based detection)
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

    # Try environment variable first (unlikely to work, but kept for compatibility)
    parent_event_id = get_parent_event_id()
    parent_start_time = None

    # Fall back to database-based detection (the reliable method)
    if not parent_event_id:
        parent_event_id, parent_start_time = get_parent_event_id_from_database(db_path)

    if not parent_event_id:
        logger.debug(
            "No parent event ID found (env or database), skipping subagent stop tracking"
        )
        return {"continue": True}

    logger.info(f"SubagentStop: Found parent event {parent_event_id}")

    # Get parent event start time if not already retrieved from database
    if not parent_start_time:
        parent_start_time = get_parent_event_start_time(db_path, parent_event_id)
        if not parent_start_time:
            logger.warning(f"Could not find parent event start time: {parent_event_id}")
            return {"continue": True}

    # Count child spikes
    child_spike_count = count_child_spikes(db_path, parent_event_id, parent_start_time)

    # Update parent event with completion info
    completion_time = datetime.now(timezone.utc).isoformat()
    success = update_parent_event(
        db_path,
        parent_event_id,
        child_spike_count,
        completion_time,
    )

    if success:
        # Clear parent event from environment
        os.environ.pop("HTMLGRAPH_PARENT_EVENT", None)
        os.environ.pop("HTMLGRAPH_SUBAGENT_TYPE", None)

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
