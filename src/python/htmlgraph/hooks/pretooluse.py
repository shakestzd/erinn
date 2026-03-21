"""
Unified PreToolUse Hook - Parallel Orchestrator + Validator + Event Tracing

This module provides a unified PreToolUse hook that runs orchestrator
enforcement, work validation checks, and event tracing in parallel using asyncio.

Architecture:
- Runs orchestrator check, validator check, and event tracing simultaneously
- Combines results into Claude Code standard format
- Returns blocking response only if both checks agree
- Provides combined guidance from both systems
- Generates tool_use_id and initiates event tracing for correlation

Performance:
- ~40-50% faster than sequential subprocess execution
- Single Python process (no subprocess overhead)
- Parallel execution via asyncio.gather()

Event Tracing:
- Generates UUID v4 for tool_use_id
- Captures tool name, input, start time (ISO8601 UTC), session_id
- Inserts start event into tool_traces table for PostToolUse correlation
- Non-blocking - errors gracefully degrade to allow tool execution
"""

import asyncio
import json
import logging
import os
import sys
import tempfile
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from htmlgraph.db.schema import HtmlGraphDB
from htmlgraph.hooks.orchestrator import enforce_orchestrator_mode
from htmlgraph.hooks.task_enforcer import enforce_task_saving
from htmlgraph.hooks.validator import (
    load_tool_history as validator_load_history,
)
from htmlgraph.hooks.validator import (
    load_validation_config,
    validate_tool_call,
)

logger = logging.getLogger(__name__)

# NEVER_BLOCK_TOOLS: Tools that should NEVER be blocked by enforcement
# These are essential for coordination, orchestration, and exploration
NEVER_BLOCK_TOOLS = {
    "Task",
    "Agent",
    "TaskCreate",
    "TaskUpdate",
    "TaskList",
    "TaskGet",
    "AskUserQuestion",
    "TodoWrite",
    "TodoRead",
    "Skill",
    "Read",
    "Grep",
    "Glob",
    "WebSearch",
    "WebFetch",
}


def generate_tool_use_id() -> str:
    """
    Generate UUID v4 for tool_use_id.

    Used for trace correlation between PreToolUse and PostToolUse hooks.

    Returns:
        UUID v4 string (36 chars)
    """
    return str(uuid.uuid4())


def write_traceparent_queue(session_id: str, task_event_id: str) -> None:
    """Write traceparent to queue file for next subagent to claim.

    Claude Code spawns subagents as separate OS processes.  At the moment
    the PreToolUse hook fires for a ``Task``/``Agent`` call the subagent's
    ``agentId`` has not been assigned yet, so we cannot write a keyed temp
    file.  Instead, we write an *unclaimed* entry to a shared queue directory.
    The subagent's session-start hook claims the most recent unclaimed entry
    and stores ``parent_session_id`` / ``parent_event_id`` in its session row.

    Files older than 5 minutes are cleaned up to avoid accumulation.

    Args:
        session_id: Current (parent) session ID — becomes ``trace_id`` for
                    the W3C ``traceparent`` header.
        task_event_id: The ``event_id`` of the ``task_delegation`` event
                       just created — becomes ``parent_span_id``.
    """
    try:
        queue_dir = Path(tempfile.gettempdir()) / "htmlgraph-traceparent"
        queue_dir.mkdir(exist_ok=True)

        # Clean up stale files (> 5 minutes old) before writing new entry
        now = time.time()
        for stale in queue_dir.glob("tp-*.json"):
            try:
                if now - stale.stat().st_mtime > 300:
                    stale.unlink(missing_ok=True)
            except Exception:
                pass

        entry = {
            "trace_id": session_id,
            "parent_span_id": task_event_id,
            "timestamp": now,
            "claimed": False,
        }
        # Millisecond-precision timestamp in name avoids collisions when
        # multiple parallel Task() spawns happen in the same second.
        ts = int(now * 1000)
        queue_file = queue_dir / f"tp-{ts}.json"
        queue_file.write_text(json.dumps(entry))
        logger.debug(
            f"Wrote traceparent queue entry: {queue_file.name} "
            f"(trace_id={session_id}, parent_span_id={task_event_id})"
        )
    except Exception as e:
        # Non-blocking — traceparent propagation failure must not block tools
        logger.debug(f"Could not write traceparent queue entry: {e}")


def get_current_session_id() -> str | None:
    """
    Query current session_id from environment or session files.

    Reads from:
    1. Environment variable HTMLGRAPH_SESSION_ID (set by SessionStart hook)
    2. Latest session HTML file (fallback if env var not set)
    3. Session registry file (fallback if HTML file not found)

    Returns:
        Session ID string or None if not found
    """
    # First try environment variable
    session_id = os.environ.get("HTMLGRAPH_SESSION_ID")
    if session_id:
        logger.debug(f"Session ID from environment: {session_id}")
        return session_id

    # Fallback: Read from latest session HTML file
    try:
        import re
        from pathlib import Path

        graph_dir = Path.cwd() / ".htmlgraph"
        sessions_dir = graph_dir / "sessions"

        logger.debug(f"Looking for session files in: {sessions_dir}")

        if sessions_dir.exists():
            # Get the most recent session HTML file
            session_files = sorted(
                sessions_dir.glob("sess-*.html"),
                key=lambda p: p.stat().st_mtime,
                reverse=True,
            )
            logger.debug(f"Found {len(session_files)} session files")

            for session_file in session_files:
                try:
                    # Extract session_id from filename (sess-XXXXX.html)
                    match = re.search(r"sess-([a-f0-9]+)", session_file.name)
                    if match:
                        session_id = f"sess-{match.group(1)}"
                        logger.debug(f"Found session ID from file: {session_id}")
                        return session_id
                except Exception as e:
                    logger.debug(f"Error reading session file {session_file}: {e}")
                    continue
            logger.debug("No valid session files found")
        else:
            logger.debug(f"Sessions directory not found: {sessions_dir}")
    except Exception as e:
        logger.debug(f"Could not read from session files: {e}")

    # Fallback: Read from session registry
    try:
        import json
        from pathlib import Path

        graph_dir = Path.cwd() / ".htmlgraph"
        registry_dir = graph_dir / "sessions" / "registry" / "active"

        if registry_dir.exists():
            # Get the most recent session file
            session_files = sorted(
                registry_dir.glob("*.json"),
                key=lambda p: p.stat().st_mtime,
                reverse=True,
            )

            for session_file in session_files:
                try:
                    with open(session_file) as f:
                        data = json.load(f)
                        if data.get("status") == "active":
                            session_id = data.get("session_id")
                            if isinstance(session_id, str):
                                return session_id
                except Exception:
                    continue
    except Exception as e:
        logger.debug(f"Could not read from session registry: {e}")

    return None


def sanitize_tool_input(tool_input: dict[str, Any]) -> dict[str, Any]:
    """
    Sanitize tool input to remove sensitive data before storage.

    Removes or truncates:
    - Passwords and tokens (any field with 'password', 'token', 'secret', 'key')
    - Large binary data
    - Deeply nested structures

    Args:
        tool_input: Raw tool input to sanitize

    Returns:
        Sanitized copy of tool_input
    """
    try:
        sanitized = {}
        sensitive_keys = {"password", "token", "secret", "key", "auth", "api_key"}

        for key, value in tool_input.items():
            # Remove sensitive fields
            if any(sens in key.lower() for sens in sensitive_keys):
                sanitized[key] = "[REDACTED]"
            # Truncate very large values
            elif isinstance(value, str) and len(value) > 10000:
                sanitized[key] = f"{value[:10000]}... [TRUNCATED]"
            # Keep other values
            else:
                sanitized[key] = value

        return sanitized
    except Exception as e:
        logger.warning(f"Error sanitizing tool input: {e}")
        return tool_input


def extract_subagent_type(tool_input: dict[str, Any]) -> str | None:
    """
    Extract subagent_type from Task() tool input.

    Looks for patterns like:
    - "subagent_type": "gemini-spawner"
    - Task with specific naming patterns

    Args:
        tool_input: Task() tool input parameters

    Returns:
        Subagent type string or None if not found
    """
    try:
        # Check for explicit subagent_type parameter
        if "subagent_type" in tool_input:
            return str(tool_input.get("subagent_type"))

        # Check in prompt for agent references
        prompt = str(tool_input.get("prompt", "")).lower()
        if "gemini" in prompt:
            return "gemini-spawner"
        if "codex" in prompt:
            return "codex-spawner"
        if "researcher" in prompt:
            return "researcher"
        if "debugger" in prompt:
            return "debugger"

        return None
    except Exception:
        return None


def create_task_parent_event(
    db: HtmlGraphDB,
    tool_input: dict[str, Any],
    session_id: str,
    start_time: str,
) -> str | None:
    """
    Create a parent event for Task() delegations.

    Inserts into agent_events with:
    - event_type: 'task_delegation'
    - subagent_type: Extracted from tool input
    - status: 'started'
    - parent_event_id: UserQuery event ID (links back to conversation root)

    This event will be linked to child events created by the subagent
    and updated when SubagentStop fires.

    Args:
        db: Database connection
        tool_input: Task() tool input parameters
        session_id: Current session ID (may be subagent session with suffix)
        start_time: ISO8601 UTC timestamp

    Returns:
        Parent event_id if successful, None otherwise
    """
    try:
        if not db.connection:
            db.connect()

        parent_event_id = f"evt-{str(uuid.uuid4())[:8]}"

        # Task parameters are nested inside "input" or "tool_input" key from hook_input
        task_params = (
            tool_input.get("input", {})
            or tool_input.get("tool_input", {})
            or tool_input
        )

        subagent_type = extract_subagent_type(task_params)
        prompt = str(task_params.get("prompt", ""))[:200]

        # Extract model from task parameters (e.g., "haiku" -> "claude-haiku")
        model = None
        if isinstance(task_params, dict) and "model" in task_params:
            model_value = task_params.get("model")
            if model_value and isinstance(model_value, str):
                model = model_value.strip().lower()
                if model and not model.startswith("claude-"):
                    model = f"claude-{model}"

        # Extract parent session ID using native agent_id/agent_type fields.
        # When agent_id is present in hook_input, Claude Code tells us we ARE in
        # a subagent context — session_id is the parent session, no stripping needed.
        # When agent_id is absent (orchestrator context), session_id IS the parent.
        parent_session_id = session_id  # Default: this session is the parent

        # Load UserQuery event ID for parent-child linking from database
        # Use parent_session_id to ensure we find UserQuery in the main session
        user_query_event_id = None
        try:
            from htmlgraph.hooks.event_tracker import get_parent_user_query

            user_query_event_id = get_parent_user_query(db, parent_session_id)
            if user_query_event_id:
                logger.debug(
                    f"Found UserQuery parent for Task: {user_query_event_id} in session {parent_session_id}"
                )
            else:
                logger.warning(
                    f"No UserQuery found for Task in session {parent_session_id}. "
                    "Task will be orphaned in activity feed."
                )
        except Exception as e:
            logger.warning(f"Error looking up UserQuery parent: {e}")

        # Check if we're in a nested delegation context
        # If HTMLGRAPH_PARENT_EVENT is set, we're already inside a subagent
        # and should link to that Task delegation, not UserQuery
        env_parent = os.environ.get("HTMLGRAPH_PARENT_EVENT")
        parent_event_id_for_insertion: str | None = None
        if env_parent:
            # Nested Task() - parent is the enclosing Task delegation
            parent_event_id_for_insertion = env_parent
        else:
            # Top-level Task() - parent is the UserQuery (None if not found)
            parent_event_id_for_insertion = user_query_event_id

        # Build input summary - human-readable, not raw JSON
        description = str(task_params.get("description", ""))[:100]
        if description:
            input_summary = f"({subagent_type or 'general-purpose'}): {description}"
        else:
            # Fallback to prompt snippet if no description
            input_summary = f"({subagent_type or 'general-purpose'}): {prompt[:100]}"

        cursor = db.connection.cursor()  # type: ignore[union-attr]

        # Insert parent event in the PARENT session (not subagent session)
        # This ensures task_delegation events are in the same session as UserQuery
        # Use native agent_id from hook input if present, else fall back to "claude-code"
        orchestrator_agent_id = str(tool_input.get("agent_id") or "claude-code")

        cursor.execute(
            """
            INSERT INTO agent_events
            (event_id, agent_id, event_type, timestamp, tool_name,
             input_summary, session_id, status, subagent_type, parent_event_id, model,
             tool_input)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        """,
            (
                parent_event_id,
                orchestrator_agent_id,  # Native agent_id from hook input
                "task_delegation",
                start_time,
                "Task",
                input_summary,
                parent_session_id,  # Use parent session, not subagent session
                "started",
                subagent_type or "general-purpose",
                parent_event_id_for_insertion,  # Link to parent Task or UserQuery
                model,  # Model from tool_input (e.g., "claude-haiku")
                json.dumps(
                    task_params
                ),  # Store full task input for background detection
            ),
        )

        db.connection.commit()  # type: ignore[union-attr]

        # Export to environment for subagent reference
        os.environ["HTMLGRAPH_PARENT_EVENT"] = parent_event_id
        os.environ["HTMLGRAPH_PARENT_QUERY_EVENT"] = user_query_event_id or ""
        os.environ["HTMLGRAPH_SUBAGENT_TYPE"] = subagent_type or "general-purpose"

        logger.debug(
            f"Created parent event for Task delegation: "
            f"event_id={parent_event_id}, subagent_type={subagent_type}, "
            f"parent_query_event={user_query_event_id}"
        )

        return parent_event_id

    except Exception as e:
        logger.warning(f"Error creating parent event: {e}")
        return None


def create_start_event(
    tool_name: str, tool_input: dict[str, Any], session_id: str
) -> str | None:
    """
    Capture and store tool execution start event.

    Inserts into tool_traces table with:
    - tool_use_id: UUID v4 for correlation
    - trace_id: Parent trace ID (from context)
    - session_id: Current session
    - tool_name: Tool being executed
    - tool_input: Sanitized input parameters
    - start_time: ISO8601 UTC timestamp
    - status: 'started'

    For Task() calls, also creates a parent event for event nesting.

    Args:
        tool_name: Name of tool being executed
        tool_input: Tool input parameters (will be sanitized)
        session_id: Current session ID

    Returns:
        tool_use_id on success, None on error
    """
    tool_use_id = None
    try:
        # Use native tool_use_id from Claude Code if present (e.g. toolu_01ABC123),
        # otherwise generate a UUID for correlation.
        tool_use_id = tool_input.get("tool_use_id") or generate_tool_use_id()
        start_time = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M:%S")

        # Connect to database (use project's .htmlgraph/htmlgraph.db, not home directory)
        from htmlgraph.config import get_database_path

        db_path = str(get_database_path())
        db = HtmlGraphDB(db_path)

        # Ensure session exists (create placeholder if needed)
        if not db._ensure_session_exists(session_id, "system"):
            logger.warning(f"Could not ensure session {session_id} exists in database")

        # Insert start event into tool_traces
        if not db.connection:
            db.connect()

        cursor = db.connection.cursor()  # type: ignore[union-attr]

        # Determine parent event ID with proper hierarchy:
        # 1. FIRST check HTMLGRAPH_PARENT_EVENT env var (set by Task delegation for subagents)
        # 2. For Task() tool, create a new task_delegation event
        # 3. Fall back to UserQuery only if no parent context available
        #
        # This ensures tool events executed within Task() subagents are properly
        # nested under the Task delegation event, not flattened to UserQuery.
        env_parent_event = os.environ.get("HTMLGRAPH_PARENT_EVENT")

        # Transcript-based parent resolution (parallel-safe, restart-safe).
        #
        # Read the Claude Code conversation transcript to find the authoritative
        # session_id and user turn uuid for the current tool_use_id.  This avoids
        # relying on env var staleness checks or timestamp comparisons, both of
        # which break under parallel sessions or resumed sessions.
        #
        # tool_input may contain the native tool_use_id as provided by Claude Code
        # (e.g. "toolu_01XYZ").  If present, we use it to locate the exact
        # assistant message in the transcript and walk up to the user turn.
        _native_tool_use_id = tool_input.get("tool_use_id") or tool_input.get(
            "toolUseId"
        )
        _transcript_session_id: str | None = None
        _transcript_user_uuid: str | None = None
        if _native_tool_use_id:
            try:
                from htmlgraph.hooks.transcript import (
                    find_parent_user_query as _find_parent,
                )

                _transcript_session_id, _transcript_user_uuid = _find_parent(
                    _native_tool_use_id, os.getcwd()
                )
                if _transcript_session_id and _transcript_session_id != session_id:
                    logger.debug(
                        f"Transcript resolved session_id={_transcript_session_id} "
                        f"(was {session_id}); updating session_id"
                    )
                    session_id = _transcript_session_id
            except Exception as _te:
                logger.debug(f"Transcript lookup skipped: {_te}")

        # Get UserQuery event ID as fallback (for top-level tool calls).
        # If the transcript gave us a user_turn_uuid, try to match it against a
        # UserQuery event in the database first (most precise).  Fall back to
        # the most recent UserQuery in the session.
        user_query_event_id = None
        try:
            from htmlgraph.hooks.event_tracker import get_parent_user_query

            if _transcript_user_uuid:
                # Prefer the UserQuery whose content references the transcript uuid.
                # The user_prompt_submit hook stores the uuid as transcript_id in
                # agent_events metadata; try that column first.
                try:
                    cursor.execute(
                        """
                        SELECT event_id FROM agent_events
                        WHERE session_id = ?
                          AND tool_name = 'UserQuery'
                          AND (metadata LIKE ? OR event_id = ?)
                        ORDER BY datetime(REPLACE(SUBSTR(timestamp, 1, 19), 'T', ' ')) DESC
                        LIMIT 1
                        """,
                        (
                            session_id,
                            f"%{_transcript_user_uuid}%",
                            _transcript_user_uuid,
                        ),
                    )
                    row = cursor.fetchone()
                    if row:
                        user_query_event_id = str(row[0])
                        logger.debug(
                            f"Matched UserQuery={user_query_event_id} via "
                            f"transcript_user_uuid={_transcript_user_uuid}"
                        )
                except Exception as _dbe:
                    logger.debug(f"Transcript uuid DB lookup failed: {_dbe}")

            if not user_query_event_id:
                user_query_event_id = get_parent_user_query(db, session_id)
        except Exception:
            pass

        # Check if this is a Task()/Agent() call for parent event creation
        task_parent_event_id = None
        if tool_name in ("Task", "Agent"):
            task_parent_event_id = create_task_parent_event(
                db, tool_input, session_id, start_time
            )
            # Write W3C traceparent queue entry so the spawned subagent's
            # session-start hook can claim it and record parent linkage.
            if task_parent_event_id:
                write_traceparent_queue(session_id, task_parent_event_id)

        # Detect if we're in a subagent session and find parent task_delegation.
        #
        # Strategy:
        # 1. Native agent_id from hook_input (Claude Code provides this when the hook
        #    fires inside a subagent). SubagentStart already mapped this agent_id to
        #    a task_delegation row, so we can do an exact lookup.
        # 2. If agent_id is absent: check if session_id exists in the sessions table.
        #    If it does NOT exist, this is a subagent with a brand-new session_id that
        #    Claude Code assigned. Find the most recent task_delegation with status='started'.
        subagent_parent_event_id = None
        session_known = True  # Default: assume known (orchestrator). Updated below if session lookup succeeds.
        native_agent_id = tool_input.get("agent_id")
        if native_agent_id:
            # Use the agent_id already mapped to a task_delegation by SubagentStart hook
            try:
                cursor.execute(
                    """SELECT event_id FROM agent_events
                       WHERE agent_id = ?
                         AND event_type = 'task_delegation'
                       ORDER BY datetime(REPLACE(SUBSTR(timestamp, 1, 19), 'T', ' ')) DESC LIMIT 1""",
                    (native_agent_id,),
                )
                row = cursor.fetchone()
                if row:
                    subagent_parent_event_id = row[0]
            except Exception:
                pass
        else:
            # Primary: check if this session_id exists in the sessions table.
            # If it does NOT exist, we are in a subagent with a brand-new session_id
            # that Claude Code assigned. Find the most recently started task_delegation.
            try:
                cursor.execute(
                    "SELECT 1 FROM sessions WHERE session_id = ? LIMIT 1",
                    (session_id,),
                )
                session_known = cursor.fetchone() is not None
            except Exception:
                session_known = True  # Assume known on error to avoid false positives

            if not session_known:
                # This session is unknown → we are a subagent. Find the task that spawned us.
                try:
                    cursor.execute(
                        """
                        SELECT event_id
                        FROM agent_events
                        WHERE event_type = 'task_delegation'
                          AND status = 'started'
                        ORDER BY datetime(REPLACE(SUBSTR(timestamp, 1, 19), 'T', ' ')) DESC
                        LIMIT 1
                        """,
                    )
                    row = cursor.fetchone()
                    if row:
                        subagent_parent_event_id = row[0]
                        logger.debug(
                            f"Subagent detected via unknown session_id={session_id}: "
                            f"parent task_delegation={subagent_parent_event_id}"
                        )
                except Exception as e:
                    logger.debug(
                        f"Could not find task_delegation for unknown session: {e}"
                    )
            else:
                # Session is known (main agent). Try suffix-stripping as secondary check.
                known_suffixes = ["-general-purpose", "-Explore", "-Bash", "-Plan"]
                for suffix in known_suffixes:
                    if session_id.endswith(suffix):
                        parent_session_id = session_id[: -len(suffix)]
                        try:
                            cursor.execute(
                                """SELECT event_id FROM agent_events
                                   WHERE session_id = ?
                                     AND event_type = 'task_delegation'
                                   ORDER BY datetime(REPLACE(SUBSTR(timestamp, 1, 19), 'T', ' ')) DESC LIMIT 1""",
                                (parent_session_id,),
                            )
                            row = cursor.fetchone()
                            if row:
                                subagent_parent_event_id = row[0]
                        except Exception:
                            pass
                        break

        # Determine parent for this event (priority order)
        #
        # MCP tool calls (tool_name contains "__", e.g. mcp__plugin_htmlgraph_chrome-devtools__navigate_page)
        # are always invoked directly by the orchestrator, never from inside a subagent.
        # HTMLGRAPH_PARENT_EVENT is set when a Task() delegation is created and persists in the
        # process environment.  If we let MCP tools inherit it they get attributed to the last
        # Task event rather than the current UserQuery, which is wrong.
        # Solution: MCP tools skip the env_parent_event branch entirely.
        #
        # RESTART BUG FIX: HTMLGRAPH_PARENT_EVENT also persists across Claude Code restarts.
        # After a restart, the env var still holds the Task event from the previous session.
        # Before trusting env_parent_event, validate it belongs to the CURRENT session by
        # querying the database.  If the event belongs to a different session, discard it.
        is_mcp_tool = "__" in tool_name

        # Validate env_parent_event belongs to the current session AND is not stale.
        #
        # Staleness detection strategy: compare the task_delegation's timestamp
        # against the most recent UserQuery's timestamp.  A valid in-flight
        # Task() delegation was created AFTER the current UserQuery (same turn),
        # so its timestamp will be greater.  A stale task from a prior turn will
        # have a timestamp <= the current UserQuery.
        #
        # This correctly handles nested tasks (task B under task A) because BOTH
        # tasks were created after the current UserQuery, regardless of their
        # parent_event_id chain.
        #
        # NOTE: env vars don't propagate between hook processes, so this branch
        # is effectively dead code in the real system.  The real staleness fix
        # is in PostToolUse (event_tracker.py).  This is kept as defence-in-depth.
        if env_parent_event and not is_mcp_tool:
            try:
                cursor.execute(
                    """
                    SELECT ae.session_id, ae.timestamp
                    FROM agent_events ae
                    WHERE ae.event_id = ?
                    LIMIT 1
                    """,
                    (env_parent_event,),
                )
                row = cursor.fetchone()
                # Session validation: the event must either belong to our session
                # OR we are a subagent (unknown session) and the event belongs to
                # the parent session.  In subagent context the task_delegation is
                # stored under the parent session, not the subagent session.
                event_session_ok = row is not None and (
                    row[0] == session_id or not session_known
                )
                if not event_session_ok:
                    # Event doesn't exist or belongs to a different session
                    logger.debug(
                        f"Discarding stale HTMLGRAPH_PARENT_EVENT={env_parent_event}: "
                        f"belongs to session {row[0] if row else 'unknown'}, "
                        f"current session={session_id}"
                    )
                    env_parent_event = None
                    os.environ.pop("HTMLGRAPH_PARENT_EVENT", None)
                else:
                    # Timestamp-based staleness: task must have been created AFTER
                    # the current UserQuery.
                    task_ts = row[1] or ""
                    uq_ts = None
                    if user_query_event_id:
                        try:
                            cursor.execute(
                                "SELECT timestamp FROM agent_events WHERE event_id = ? LIMIT 1",
                                (user_query_event_id,),
                            )
                            uq_row = cursor.fetchone()
                            if uq_row:
                                uq_ts = uq_row[0]
                        except Exception:
                            pass
                    task_ts_norm = task_ts.replace("T", " ")[:19]
                    uq_ts_norm = (uq_ts or "").replace("T", " ")[:19]
                    if uq_ts and task_ts_norm <= uq_ts_norm:
                        logger.debug(
                            f"Discarding stale HTMLGRAPH_PARENT_EVENT={env_parent_event}: "
                            f"ts={task_ts} <= current UserQuery ts={uq_ts} "
                            f"(task_delegation is from a prior turn)"
                        )
                        env_parent_event = None
                        os.environ.pop("HTMLGRAPH_PARENT_EVENT", None)
            except Exception as e:
                logger.debug(f"Could not validate env_parent_event: {e}")

        if tool_name in ("Task", "Agent") and task_parent_event_id:
            parent_event_id = user_query_event_id  # Task links to UserQuery
        elif subagent_parent_event_id:
            parent_event_id = subagent_parent_event_id  # Subagent links to Task
        elif env_parent_event and not is_mcp_tool and not session_known:
            parent_event_id = env_parent_event  # Explicit parent from env (non-MCP only; staleness validated above)
        else:
            parent_event_id = user_query_event_id  # Fall back to UserQuery

        # Export parent event for PostToolUse to use
        if parent_event_id:
            os.environ["HTMLGRAPH_PARENT_EVENT_FOR_POST"] = parent_event_id

        # For Task()/Agent() calls, reuse the task_delegation event (no duplicate)
        if tool_name in ("Task", "Agent") and task_parent_event_id:
            event_id = task_parent_event_id
        else:
            event_id = f"evt-{generate_tool_use_id()[:8]}"
            # Skip preliminary event insertion for non-Task tools.
            # PostToolUse handler creates the full event with output data.
            # Only Task() needs PreToolUse event creation (for task_delegation hierarchy).

        # For Task/Agent delegation, export task_parent_event_id for subagent context
        if tool_name in ("Task", "Agent") and task_parent_event_id:
            os.environ["HTMLGRAPH_PARENT_EVENT"] = task_parent_event_id

        db.connection.commit()  # type: ignore[union-attr]
        db.disconnect()

        logger.debug(
            f"Created start event: event_id={event_id}, tool_use_id={tool_use_id}, "
            f"tool={tool_name}, session={session_id}, parent_event={parent_event_id}"
        )
        return tool_use_id  # Return tool_use_id for PostToolUse correlation

    except Exception as e:
        logger.warning(f"Error creating start event: {e}")
        # Graceful degradation - return None but don't block tool
        return None


def resolve_parent_task_delegation(
    cursor: Any,
    parent_session_id: str,
    model_hint: str | None = None,
) -> str | None:
    """
    Resolve the best active task_delegation event to attribute child events to.

    Selection algorithm:
    1. Fetch all started task_delegations for parent_session_id
    2. If model_hint provided, narrow to rows whose model matches
    3. Among candidates, pick the one with fewest existing children
    4. Tiebreak by earliest timestamp (FIFO)
    5. Return None if no active delegations exist

    Args:
        cursor: SQLite cursor (may be from any connection, including in-memory)
        parent_session_id: Session ID of the parent (orchestrator) session
        model_hint: Optional model string to prefer (e.g. "claude-haiku")

    Returns:
        event_id of the best matching task_delegation, or None
    """
    try:
        cursor.execute(
            """
            SELECT event_id, model
            FROM agent_events
            WHERE session_id = ?
              AND event_type = 'task_delegation'
              AND status = 'started'
            ORDER BY datetime(REPLACE(SUBSTR(timestamp, 1, 19), 'T', ' ')) ASC
            """,
            (parent_session_id,),
        )
        candidates = cursor.fetchall()
    except Exception as e:
        logger.debug(f"resolve_parent_task_delegation query failed: {e}")
        return None

    if not candidates:
        return None

    # Narrow by model_hint when provided
    if model_hint:
        model_hint_lower = model_hint.lower()
        filtered = [
            row for row in candidates if row[1] and row[1].lower() == model_hint_lower
        ]
        if filtered:
            candidates = filtered

    # Count existing children for each candidate
    def child_count(event_id: str) -> int:
        try:
            cursor.execute(
                "SELECT COUNT(*) FROM agent_events WHERE parent_event_id = ?",
                (event_id,),
            )
            row = cursor.fetchone()
            return row[0] if row else 0
        except Exception:
            return 0

    # Pick candidate with fewest children; FIFO tiebreak (list already sorted ASC)
    best_id: str | None = None
    best_count: int = -1
    for row in candidates:
        event_id = row[0]
        count = child_count(event_id)
        if best_id is None or count < best_count:
            best_id = event_id
            best_count = count

    return best_id


async def run_event_tracing(
    tool_input: dict[str, Any],
) -> dict[str, Any]:
    """
    Run event tracing (async wrapper).

    Generates tool_use_id and creates start event in database.
    Non-blocking - errors don't prevent tool execution.

    Args:
        tool_input: Hook input with tool name and parameters

    Returns:
        Event tracing response: {"hookSpecificOutput": {"tool_use_id": "...", ...}}
    """
    try:
        from htmlgraph.hooks.context import HookContext

        loop = asyncio.get_event_loop()
        tool_name = tool_input.get("name", "") or tool_input.get("tool_name", "")

        # Use HookContext to properly extract session_id (same as UserPromptSubmit)
        context = HookContext.from_input(tool_input)

        try:
            session_id = context.session_id

            # Skip if no session ID
            if not session_id or session_id == "unknown":
                logger.debug("No session ID found, skipping event tracing")
                return {}

            # Run in thread pool since it involves I/O
            tool_use_id = await loop.run_in_executor(
                None,
                create_start_event,
                tool_name,
                tool_input,
                session_id,
            )

            if tool_use_id:
                # Store in environment for PostToolUse correlation
                os.environ["HTMLGRAPH_TOOL_USE_ID"] = tool_use_id

                return {
                    "hookSpecificOutput": {
                        "tool_use_id": tool_use_id,
                        "additionalContext": f"Event tracing started: {tool_use_id}",
                    }
                }

            return {}
        finally:
            # Ensure context resources are properly closed
            context.close()
    except Exception:
        # Graceful degradation - allow on error
        return {}


async def run_orchestrator_check(tool_input: dict[str, Any]) -> dict[str, Any]:
    """
    Run orchestrator enforcement check (async wrapper).

    Args:
        tool_input: Hook input with tool name and parameters

    Returns:
        Orchestrator response: {"continue": bool, "hookSpecificOutput": {...}}
    """
    try:
        loop = asyncio.get_event_loop()
        tool_name = tool_input.get("name", "") or tool_input.get("tool_name", "")
        tool_params = tool_input.get("input", {}) or tool_input.get("tool_input", {})

        # Run in thread pool since it's CPU-bound
        return await loop.run_in_executor(
            None,
            enforce_orchestrator_mode,
            tool_name,
            tool_params,
        )
    except Exception:
        # Graceful degradation - allow on error
        return {"continue": True}


async def run_validation_check(tool_input: dict[str, Any]) -> dict[str, Any]:
    """
    Run work validation check (async wrapper).

    Args:
        tool_input: Hook input with tool name and parameters

    Returns:
        Validator response: {"decision": "allow"|"deny", "guidance": "...", ...}
    """
    try:
        loop = asyncio.get_event_loop()

        tool_name = tool_input.get("name", "") or tool_input.get("tool", "")
        tool_params = tool_input.get("input", {}) or tool_input.get("params", {})
        session_id = tool_input.get("session_id", "unknown")

        # Load config and history in thread pool
        config = await loop.run_in_executor(None, load_validation_config)
        history = await loop.run_in_executor(
            None, lambda: validator_load_history(session_id)
        )

        # Run validation
        return await loop.run_in_executor(
            None,
            validate_tool_call,
            tool_name,
            tool_params,
            config,
            history,
        )
    except Exception:
        # Graceful degradation - allow on error
        return {"decision": "allow"}


async def run_task_enforcement(tool_input: dict[str, Any]) -> dict[str, Any]:
    """
    Run task save enforcement check (async wrapper).

    Args:
        tool_input: Hook input with tool name and parameters

    Returns:
        Task enforcer response: {"continue": bool, "hookSpecificOutput": {...}}
    """
    try:
        loop = asyncio.get_event_loop()

        tool_name = tool_input.get("name", "") or tool_input.get("tool_name", "")
        tool_params = tool_input.get("input", {}) or tool_input.get("tool_input", {})

        # Run task enforcement
        return await loop.run_in_executor(
            None,
            enforce_task_saving,
            tool_name,
            tool_params,
        )
    except Exception:
        # Graceful degradation - allow on error
        return {"continue": True}


async def provide_debugging_guidance(tool_input: dict[str, Any]) -> dict[str, Any]:
    """
    Provide debugging guidance based on tool patterns and context.

    Args:
        tool_input: Hook input with tool name and parameters

    Returns:
        Guidance response: {"hookSpecificOutput": {"additionalContext": "..."}}
    """
    try:
        tool_name = tool_input.get("name", "") or tool_input.get("tool_name", "")
        tool_params = tool_input.get("input", {}) or tool_input.get("tool_input", {})

        # High-risk tools that often indicate debugging scenarios
        high_risk_tools = ["Edit", "Write", "Bash", "Read"]
        if tool_name not in high_risk_tools:
            return {}

        guidance = []

        # Check for debugging keywords in tool parameters
        params_text = str(tool_params).lower()
        debug_keywords = ["error", "fix", "broken", "failed", "bug", "issue", "problem"]

        if any(kw in params_text for kw in debug_keywords):
            guidance.append("🔍 Debugging task detected")
            guidance.append("Consider:")
            guidance.append("  - Review DEBUGGING.md for systematic approach")
            guidance.append("  - Use researcher agent for unfamiliar errors")
            guidance.append("  - Use debugger agent for systematic analysis")
            guidance.append("  - Run /doctor or /hooks for diagnostics")

        if guidance:
            return {
                "hookSpecificOutput": {
                    "hookEventName": "PreToolUse",
                    "additionalContext": "\n".join(guidance),
                }
            }

        return {}
    except Exception:
        # Graceful degradation - no guidance on error
        return {}


async def pretooluse_hook(tool_input: dict[str, Any]) -> dict[str, Any]:
    """
    Unified PreToolUse hook - runs all checks in parallel.

    Args:
        tool_input: Hook input with tool name and parameters

    Returns:
        Claude Code standard format:
        {
            "continue": bool,
            "hookSpecificOutput": {
                "hookEventName": "PreToolUse",
                "updatedInput": {...},  # If task enforcer modified input
                "additionalContext": "Combined guidance",
                "tool_use_id": "..."  # For PostToolUse correlation
            }
        }
    """
    # SAFETY NET: Never block essential tools or MCP tools
    tool_name = tool_input.get("name", "") or tool_input.get("tool_name", "")
    if tool_name in NEVER_BLOCK_TOOLS or "__" in tool_name:  # "__" indicates MCP tools
        # Still run event tracing for hierarchy tracking (especially Task delegation)
        # but skip orchestrator/validator checks
        event_tracing_response = await run_event_tracing(tool_input)

        response: dict[str, Any] = {
            "hookSpecificOutput": {
                "hookEventName": "PreToolUse",
                "permissionDecision": "allow",
            }
        }

        # Add tool_use_id and context from event tracing
        if "hookSpecificOutput" in event_tracing_response:
            tool_use_id = event_tracing_response["hookSpecificOutput"].get(
                "tool_use_id"
            )
            if tool_use_id:
                response["hookSpecificOutput"]["tool_use_id"] = tool_use_id
            ctx = event_tracing_response["hookSpecificOutput"].get("additionalContext")
            if ctx:
                response["hookSpecificOutput"]["additionalContext"] = (
                    f"[EventTrace] {ctx}"
                )

        return response

    # Run all five checks in parallel using asyncio.gather
    (
        event_tracing_response,
        orch_response,
        validate_response,
        task_response,
        debug_guidance,
    ) = await asyncio.gather(
        run_event_tracing(tool_input),
        run_orchestrator_check(tool_input),
        run_validation_check(tool_input),
        run_task_enforcement(tool_input),
        provide_debugging_guidance(tool_input),
    )

    # Integrate responses
    orch_continues = orch_response.get("continue", True)
    validate_allows = validate_response.get("decision", "allow") == "allow"
    task_continues = task_response.get("continue", True)
    should_continue = orch_continues and validate_allows and task_continues

    # Collect guidance from all systems
    guidance_parts = []

    # Event tracing guidance
    if "hookSpecificOutput" in event_tracing_response:
        ctx = event_tracing_response["hookSpecificOutput"].get("additionalContext", "")
        if ctx:
            guidance_parts.append(f"[EventTrace] {ctx}")

    # Orchestrator guidance
    if "hookSpecificOutput" in orch_response:
        ctx = orch_response["hookSpecificOutput"].get("additionalContext", "")
        if ctx:
            guidance_parts.append(f"[Orchestrator] {ctx}")

    # Validator guidance
    if "guidance" in validate_response:
        guidance_parts.append(f"[Validator] {validate_response['guidance']}")

    if "imperative" in validate_response:
        guidance_parts.append(f"[Validator] {validate_response['imperative']}")

    if "suggestion" in validate_response:
        guidance_parts.append(f"[Validator] {validate_response['suggestion']}")

    # Task enforcer guidance
    if "hookSpecificOutput" in task_response:
        ctx = task_response["hookSpecificOutput"].get("additionalContext", "")
        if ctx:
            guidance_parts.append(f"[TaskEnforcer] {ctx}")

    # Debugging guidance
    if "hookSpecificOutput" in debug_guidance:
        ctx = debug_guidance["hookSpecificOutput"].get("additionalContext", "")
        if ctx:
            guidance_parts.append(f"[Debugging] {ctx}")

    # Build unified response in Claude Code format
    response = {
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "permissionDecision": "allow" if should_continue else "deny",
        }
    }

    # Add tool_use_id for PostToolUse correlation if available
    if "hookSpecificOutput" in event_tracing_response:
        tool_use_id = event_tracing_response["hookSpecificOutput"].get("tool_use_id")
        if tool_use_id:
            response["hookSpecificOutput"]["tool_use_id"] = tool_use_id

    # Check if task enforcer provided updatedInput
    updated_input = None
    if "hookSpecificOutput" in task_response:
        updated_input = task_response["hookSpecificOutput"].get("updatedInput")

    if updated_input:
        response["hookSpecificOutput"]["updatedInput"] = updated_input

    if guidance_parts:
        combined_guidance = "\n".join(guidance_parts)
        if should_continue:
            # Allow with context
            response["hookSpecificOutput"]["additionalContext"] = combined_guidance
        else:
            # Deny with reason
            response["hookSpecificOutput"]["permissionDecisionReason"] = (
                combined_guidance
            )

    # FINAL SAFETY NET: Strip any "deny" decisions and convert to guidance
    # This ensures no tool calls are ever blocked, only guided
    if response.get("hookSpecificOutput", {}).get("permissionDecision") == "deny":
        reason = response["hookSpecificOutput"].get("permissionDecisionReason", "")
        response["hookSpecificOutput"]["permissionDecision"] = "allow"
        if reason:
            response["hookSpecificOutput"]["additionalContext"] = f"[Guidance] {reason}"

    return response


def main() -> None:
    """Hook entry point for script wrapper."""
    # Check environment overrides
    if os.environ.get("HTMLGRAPH_DISABLE_TRACKING") == "1":
        print(json.dumps({"continue": True}))
        sys.exit(0)

    if os.environ.get("HTMLGRAPH_ORCHESTRATOR_DISABLED") == "1":
        print(json.dumps({"continue": True}))
        sys.exit(0)

    # Read tool input from stdin
    try:
        tool_input = json.load(sys.stdin)
    except json.JSONDecodeError:
        tool_input = {}

    # Run hook with parallel execution
    result = asyncio.run(pretooluse_hook(tool_input))

    # Output response
    print(json.dumps(result))

    # Exit code based on permission decision
    permission = result.get("hookSpecificOutput", {}).get("permissionDecision", "allow")
    sys.exit(0 if permission == "allow" else 1)


if __name__ == "__main__":
    main()
