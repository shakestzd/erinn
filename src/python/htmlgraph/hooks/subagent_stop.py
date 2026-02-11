"""
SubagentStop Hook - Update parent events when subagents complete.

This module handles the SubagentStop hook event, which fires when a subagent
(spawned via Task()) completes. It updates the parent event with completion
status and counts child spikes created during the subagent's execution.

Architecture:
- Reads HTMLGRAPH_PARENT_EVENT from environment (set by PreToolUse hook)
- Reads agent_id from hook_input for exact parent event lookup
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


def _event_exists_near_timestamp(
    cursor: sqlite3.Cursor,
    session_id: str,
    tool_name: str,
    timestamp_str: str,
    window_seconds: int = 2,
) -> bool:
    """
    Check if an event with similar timestamp exists (for deduplication).

    Args:
        cursor: SQLite cursor
        session_id: Session ID to check
        tool_name: Tool name to match
        timestamp_str: ISO8601 timestamp to compare
        window_seconds: Time window in seconds (default: 2)

    Returns:
        True if matching event exists, False otherwise
    """
    try:
        cursor.execute(
            """
            SELECT 1 FROM agent_events
            WHERE session_id = ?
              AND tool_name = ?
              AND ABS(JULIANDAY(timestamp) - JULIANDAY(?)) * 86400 < ?
            LIMIT 1
            """,
            (session_id, tool_name, timestamp_str, window_seconds),
        )
        return cursor.fetchone() is not None
    except Exception as e:
        logger.warning(f"Error checking event existence: {e}")
        return False


def backfill_from_transcript(
    db_path: str,
    transcript_path: str,
    parent_event_id: str,
    session_id: str,
    model: str | None = None,
) -> int:
    """
    Parse subagent JSONL transcript and backfill missing tool call events.

    Claude Code hooks don't always fire for all subagent tool calls.
    This function reads the transcript file and inserts any events
    that weren't captured by hooks.

    Args:
        db_path: Path to HtmlGraph database
        transcript_path: Path to JSONL transcript file
        parent_event_id: Parent task_delegation event ID
        session_id: Session ID for the events
        model: Model name for attribution

    Returns:
        Count of events backfilled from transcript
    """
    try:
        from htmlgraph.transcript import TranscriptEntry

        backfill_count = 0

        # Read and parse JSONL transcript
        transcript_file = Path(transcript_path)
        if not transcript_file.exists():
            logger.warning(f"Transcript file not found: {transcript_path}")
            return 0

        conn = sqlite3.connect(db_path)
        cursor = conn.cursor()

        with transcript_file.open("r", encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue

                try:
                    data = json.loads(line)
                    entry = TranscriptEntry.from_jsonl_line(data)

                    # Only process tool_use entries
                    if entry.entry_type != "tool_use" or not entry.tool_name:
                        continue

                    # Check if event already exists (deduplication)
                    timestamp_str = entry.timestamp.isoformat()
                    if _event_exists_near_timestamp(
                        cursor, session_id, entry.tool_name, timestamp_str
                    ):
                        continue

                    # Generate event ID for backfilled event
                    import uuid

                    event_id = f"evt-{uuid.uuid4().hex[:8]}"

                    # Prepare input summary from tool_input
                    input_summary = None
                    if entry.tool_input:
                        try:
                            # Create a brief summary of tool input
                            input_summary = json.dumps(entry.tool_input)[:200]
                        except Exception:
                            input_summary = str(entry.tool_input)[:200]

                    # Insert backfilled event with source='transcript'
                    cursor.execute(
                        """
                        INSERT INTO agent_events
                        (event_id, agent_id, event_type, session_id, tool_name,
                         input_summary, tool_input, parent_event_id, timestamp,
                         model, source, created_at)
                        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                        """,
                        (
                            event_id,
                            "subagent",  # Generic agent ID for backfilled events
                            "tool_call",
                            session_id,
                            entry.tool_name,
                            input_summary,
                            json.dumps(entry.tool_input) if entry.tool_input else None,
                            parent_event_id,
                            timestamp_str,
                            model,
                            "transcript",  # Mark as backfilled from transcript
                            datetime.now(timezone.utc).isoformat(),
                        ),
                    )

                    backfill_count += 1
                    logger.debug(
                        f"Backfilled event for {entry.tool_name} at {timestamp_str}"
                    )

                except json.JSONDecodeError:
                    continue
                except Exception as e:
                    logger.debug(f"Error processing transcript line: {e}")
                    continue

        conn.commit()
        conn.close()

        if backfill_count > 0:
            logger.info(
                f"Backfilled {backfill_count} events from transcript for session {session_id}"
            )

        return backfill_count

    except Exception as e:
        logger.warning(f"Error backfilling from transcript: {e}")
        return 0


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


def get_parent_event_from_db(db_path: str, agent_id: str | None = None) -> str | None:
    """
    Query database for the most recent task_delegation event.

    Used when HTMLGRAPH_PARENT_EVENT environment variable is not available
    (due to inter-process communication limitations).

    Args:
        db_path: Path to SQLite database
        agent_id: Optional agent ID for exact lookup

    Returns:
        Parent event ID (evt-XXXXX) or None if not found
    """
    try:
        from htmlgraph.hooks.pretooluse import resolve_parent_task_delegation

        conn = sqlite3.connect(db_path)
        cursor = conn.cursor()

        # If agent_id provided, do exact query first
        if agent_id:
            cursor.execute(
                """SELECT event_id FROM agent_events
                   WHERE event_type = 'task_delegation'
                     AND agent_id = ?""",
                (agent_id,),
            )
            result = cursor.fetchone()
            if result:
                conn.close()
                parent_event_id: str = result[0]
                logger.debug(
                    f"Found parent task_delegation from agent_id: {parent_event_id}"
                )
                return parent_event_id

        # Otherwise fall back to resolve_parent_task_delegation
        resolved = resolve_parent_task_delegation(cursor)
        conn.close()

        if resolved:
            logger.debug(f"Found parent task_delegation from database: {resolved}")
            return resolved

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

    # Extract agent_id from hook_input for exact lookup
    agent_id = hook_input.get("agent_id")

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

    # If parent event ID not in environment, query database
    if not parent_event_id:
        logger.debug("Parent event ID not in environment, querying database...")
        try:
            parent_event_id = get_parent_event_from_db(db_path, agent_id=agent_id)
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

    # Update parent event with completion info
    completion_time = datetime.now(timezone.utc).isoformat()
    success = update_parent_event(
        db_path,
        parent_event_id,
        child_spike_count,
        completion_time,
    )

    if success:
        # Backfill missing tool calls from transcript
        transcript_path = hook_input.get("agent_transcript_path")
        if transcript_path and Path(transcript_path).exists():
            try:
                # Get session_id and model from parent event
                session_id = None
                model = hook_input.get("model")

                try:
                    conn = sqlite3.connect(db_path)
                    cursor = conn.cursor()
                    cursor.execute(
                        "SELECT session_id, model FROM agent_events WHERE event_id = ?",
                        (parent_event_id,),
                    )
                    result = cursor.fetchone()
                    if result:
                        session_id = result[0]
                        if not model and result[1]:
                            model = result[1]
                    conn.close()
                except Exception as e:
                    logger.debug(
                        f"Could not fetch session/model from parent event: {e}"
                    )

                # Fall back to environment session_id if not found in database
                if not session_id:
                    session_id = get_session_id()

                if session_id:
                    backfill_count = backfill_from_transcript(
                        db_path,
                        transcript_path,
                        parent_event_id,
                        session_id,
                        model=model,
                    )
                    if backfill_count > 0:
                        logger.info(
                            f"Backfilled {backfill_count} events from transcript"
                        )
                else:
                    logger.warning(
                        "Could not determine session_id for transcript backfill"
                    )
            except Exception as e:
                logger.warning(f"Transcript backfill failed: {e}")

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
