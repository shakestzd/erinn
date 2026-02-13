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
import uuid
from datetime import datetime, timezone
from typing import Any

from htmlgraph.db.schema import HtmlGraphDB
from htmlgraph.hooks.constants import NEVER_BLOCK_TOOLS, SUBAGENT_SUFFIXES
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


def resolve_parent_task_delegation(
    cursor: Any,
    parent_session_id: str,
    model_hint: str | None = None,
) -> str | None:
    """
    Resolve which parent task_delegation event a subagent should be attributed to.

    When multiple Task() delegations run in parallel in the same session,
    this function determines which one a child event belongs to using:
    1. Model matching (if model_hint provided and models differ)
    2. Child count balancing (pick the task with fewest children)
    3. FIFO tiebreak (earliest timestamp wins)

    Args:
        cursor: SQLite cursor with access to agent_events table
        parent_session_id: The parent session ID to search in
        model_hint: Optional model name to match against task_delegation model

    Returns:
        event_id of the best matching task_delegation, or None if none found
    """
    try:
        # Find all active task_delegation events in this session
        cursor.execute(
            """SELECT event_id, model, timestamp FROM agent_events
               WHERE session_id = ?
                 AND event_type = 'task_delegation'
                 AND status = 'started'
               ORDER BY timestamp ASC""",
            (parent_session_id,),
        )
        rows = cursor.fetchall()

        if not rows:
            return None

        # If only one, return it
        if len(rows) == 1:
            return rows[0][0]  # event_id

        # Extract candidates as list of dicts for easier handling
        candidates = [
            {"event_id": row[0], "model": row[1], "timestamp": row[2]}
            for row in rows
        ]

        # Step 1: Filter by model_hint if provided and models differ
        if model_hint:
            model_matched = [c for c in candidates if c["model"] == model_hint]
            if model_matched:
                candidates = model_matched

        # Step 2: Count children for each candidate
        for candidate in candidates:
            cursor.execute(
                """SELECT COUNT(*) FROM agent_events
                   WHERE parent_event_id = ?""",
                (candidate["event_id"],),
            )
            candidate["child_count"] = cursor.fetchone()[0]

        # Step 3: Sort by child_count ASC (fewest first), then timestamp ASC (FIFO)
        candidates.sort(key=lambda c: (c["child_count"], c["timestamp"]))

        return candidates[0]["event_id"]

    except Exception:
        return None


def generate_tool_use_id() -> str:
    """
    Generate UUID v4 for tool_use_id.

    Used for trace correlation between PreToolUse and PostToolUse hooks.

    Returns:
        UUID v4 string (36 chars)
    """
    return str(uuid.uuid4())


# Import get_current_session_id from db_helpers


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

        # Extract parent session ID (remove subagent suffix if present)
        # Example: "abc123-general-purpose" -> "abc123"
        parent_session_id = session_id  # Default: same session (it IS the parent)
        for suffix in SUBAGENT_SUFFIXES:
            if session_id.endswith(suffix):
                parent_session_id = session_id[: -len(suffix)]
                break

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
        cursor.execute(
            """
            INSERT INTO agent_events
            (event_id, agent_id, event_type, timestamp, tool_name,
             input_summary, session_id, status, subagent_type, parent_event_id, model)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        """,
            (
                parent_event_id,
                "claude-code",  # Main orchestrator agent
                "task_delegation",
                start_time,
                "Task",
                input_summary,
                parent_session_id,  # Use parent session, not subagent session
                "started",
                subagent_type or "general-purpose",
                parent_event_id_for_insertion,  # Link to parent Task or UserQuery
                model,  # Model from tool_input (e.g., "claude-haiku")
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
        tool_use_id = generate_tool_use_id()
        trace_id = os.environ.get("HTMLGRAPH_TRACE_ID", tool_use_id)
        start_time = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M:%S")

        # Sanitize input before storing
        sanitized_input = sanitize_tool_input(tool_input)

        # Connect to database (use project's .htmlgraph/htmlgraph.db, not home directory)
        from htmlgraph.hooks.db_helpers import get_db_from_config

        db = get_db_from_config()
        if not db:
            logger.warning("Could not initialize database")
            return None

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

        # Get UserQuery event ID as fallback (for top-level tool calls)
        user_query_event_id = None
        try:
            from htmlgraph.hooks.event_tracker import get_parent_user_query

            user_query_event_id = get_parent_user_query(db, session_id)
        except Exception:
            pass

        # Check if this is a Task() call for parent event creation
        task_parent_event_id = None
        if tool_name == "Task":
            task_parent_event_id = create_task_parent_event(
                db, tool_input, session_id, start_time
            )

        # Detect if we're in a subagent session and find parent task_delegation
        subagent_parent_event_id = None
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
        if tool_name == "Task" and task_parent_event_id:
            parent_event_id = user_query_event_id  # Task links to UserQuery
        elif subagent_parent_event_id:
            parent_event_id = subagent_parent_event_id  # Subagent links to Task
        elif env_parent_event:
            parent_event_id = env_parent_event  # Explicit parent from env
        else:
            parent_event_id = user_query_event_id  # Fall back to UserQuery

        # Export parent event for PostToolUse to use
        if parent_event_id:
            os.environ["HTMLGRAPH_PARENT_EVENT_FOR_POST"] = parent_event_id

        # For Task() calls, reuse the task_delegation event (no duplicate)
        if tool_name == "Task" and task_parent_event_id:
            event_id = task_parent_event_id
        else:
            # CRITICAL FIX: Create preliminary event for ALL tools (not just Task)
            # This allows PostToolUse to UPDATE the event instead of creating duplicates
            event_id = f"evt-{generate_tool_use_id()[:8]}"

            # Insert preliminary event with status='started'
            # PostToolUse will UPDATE this event with output_summary and status='completed'
            try:
                from htmlgraph.hooks.event_tracker import format_tool_summary

                input_summary = format_tool_summary(tool_name, sanitized_input, None)

                cursor.execute(
                    """
                    INSERT INTO agent_events
                    (event_id, agent_id, event_type, timestamp, tool_name,
                     input_summary, session_id, status, parent_event_id, tool_input, claude_task_id)
                    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                    """,
                    (
                        event_id,
                        os.environ.get("HTMLGRAPH_AGENT", "claude-code"),
                        "tool_call",
                        start_time,
                        tool_name,
                        input_summary,
                        session_id,
                        "started",  # PostToolUse will update to 'completed'
                        parent_event_id,
                        json.dumps(sanitized_input),
                        None,  # claude_task_id will be set by PostToolUse if available
                    ),
                )
                logger.debug(
                    f"Created preliminary event {event_id} with status='started' "
                    f"(tool={tool_name}, session={session_id})"
                )
            except Exception as e:
                logger.warning(f"Could not create preliminary event: {e}")

        # Export event_id for PostToolUse to UPDATE (instead of INSERT)
        os.environ["HTMLGRAPH_PRETOOL_EVENT_ID"] = event_id

        # For Task delegation, export task_parent_event_id for subagent context
        if tool_name == "Task" and task_parent_event_id:
            os.environ["HTMLGRAPH_PARENT_EVENT"] = task_parent_event_id

        # Insert into tool_traces for correlation (if table exists)
        try:
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
                    json.dumps(sanitized_input),
                    start_time,
                    "started",
                    None,  # Will be set by SubagentStop hook
                ),
            )
        except Exception as e:
            logger.debug(f"Could not insert into tool_traces: {e}")

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
