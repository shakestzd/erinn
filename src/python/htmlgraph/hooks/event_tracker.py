"""
HtmlGraph Event Tracker Module

Reusable event tracking logic for hook integrations.
Provides session management, drift detection, activity logging, and SQLite persistence.

Public API:
    track_event(hook_type: str, tool_input: dict[str, Any]) -> dict
        Main entry point for tracking hook events (PostToolUse, Stop, UserPromptSubmit)

Events are recorded to both:
    - HTML files via SessionManager (existing)
    - SQLite database via HtmlGraphDB (new - for dashboard queries)

Parent-child event linking:
    - Database is the single source of truth for parent-child linking
    - UserQuery events are stored in agent_events table with tool_name='UserQuery'
    - get_parent_user_query() queries database for most recent UserQuery in session
"""

import json
import os
import re
import subprocess
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any, cast  # noqa: F401

from htmlgraph.db.schema import HtmlGraphDB
from htmlgraph.ids import generate_id
from htmlgraph.session_manager import SessionManager

# Drift classification queue (stored in session directory)
DRIFT_QUEUE_FILE = "drift-queue.json"


def get_model_from_status_cache(session_id: str | None = None) -> str | None:
    """
    Read current model from SQLite model_cache table.

    The status line script writes model info to the model_cache table.
    This allows hooks to know which Claude model is currently running,
    even though hooks don't receive model info directly from Claude Code.

    Args:
        session_id: Unused, kept for backward compatibility.

    Returns:
        Model display name (e.g., "Opus 4.5", "Sonnet", "Haiku") or None if not found.
    """
    import sqlite3

    try:
        # Try project database first
        db_path = Path.cwd() / ".htmlgraph" / "htmlgraph.db"
        if not db_path.exists():
            return None

        conn = sqlite3.connect(str(db_path), timeout=1.0)
        cursor = conn.cursor()

        # Check if model_cache table exists and has data
        cursor.execute("SELECT model FROM model_cache WHERE id = 1 LIMIT 1")
        row = cursor.fetchone()
        conn.close()

        if row and row[0] and row[0] != "Claude":
            return str(row[0])
        return str(row[0]) if row else None

    except Exception:
        # Table doesn't exist or read error - silently fail
        pass

    return None


def load_drift_config() -> dict[str, Any]:
    """Load drift configuration from plugin config or project .claude directory."""
    config_paths = [
        Path(__file__).parent.parent.parent.parent.parent
        / ".claude"
        / "config"
        / "drift-config.json",
        Path(os.environ.get("CLAUDE_PROJECT_DIR", ""))
        / ".claude"
        / "config"
        / "drift-config.json",
        Path(os.environ.get("CLAUDE_PLUGIN_ROOT", "")) / "config" / "drift-config.json",
    ]

    for config_path in config_paths:
        if config_path.exists():
            try:
                with open(config_path) as f:
                    return cast(dict[Any, Any], json.load(f))
            except Exception:
                pass

    # Default config
    return {
        "drift_detection": {
            "enabled": True,
            "warning_threshold": 0.7,
            "auto_classify_threshold": 0.85,
            "min_activities_before_classify": 3,
            "cooldown_minutes": 10,
        },
        "classification": {"enabled": True, "use_haiku_agent": True},
        "queue": {
            "max_pending_classifications": 5,
            "max_age_hours": 48,
            "process_on_stop": True,
            "process_on_threshold": True,
        },
    }


def get_parent_user_query(db: HtmlGraphDB, session_id: str) -> str | None:
    """
    Get the most recent UserQuery event_id for this session from database.

    This is the primary method for parent-child event linking.
    Database is the single source of truth - no file-based state.

    Args:
        db: HtmlGraphDB instance
        session_id: Session ID to query

    Returns:
        event_id of the most recent UserQuery event, or None if not found
    """
    try:
        if db.connection is None:
            return None
        cursor = db.connection.cursor()
        cursor.execute(
            """
            SELECT event_id FROM agent_events
            WHERE session_id = ? AND tool_name = 'UserQuery'
            ORDER BY timestamp DESC
            LIMIT 1
            """,
            (session_id,),
        )
        row = cursor.fetchone()
        if row:
            return str(row[0])
        return None
    except Exception as e:
        print(
            f"Debug: Database query for UserQuery failed: {e}",
            file=sys.stderr,
        )
        return None


def load_drift_queue(graph_dir: Path, max_age_hours: int = 48) -> dict[str, Any]:
    """
    Load the drift queue from file and clean up stale entries.

    Args:
        graph_dir: Path to .htmlgraph directory
        max_age_hours: Maximum age in hours before activities are removed (default: 48)

    Returns:
        Drift queue dict with only recent activities
    """
    queue_path = graph_dir / DRIFT_QUEUE_FILE
    if queue_path.exists():
        try:
            with open(queue_path) as f:
                queue = json.load(f)

            # Filter out stale activities
            cutoff_time = datetime.now() - timedelta(hours=max_age_hours)
            original_count = len(queue.get("activities", []))

            fresh_activities = []
            for activity in queue.get("activities", []):
                try:
                    activity_time = datetime.fromisoformat(
                        activity.get("timestamp", "")
                    )
                    if activity_time >= cutoff_time:
                        fresh_activities.append(activity)
                except (ValueError, TypeError):
                    # Keep activities with invalid timestamps to avoid data loss
                    fresh_activities.append(activity)

            # Update queue if we removed stale entries
            if len(fresh_activities) < original_count:
                queue["activities"] = fresh_activities
                save_drift_queue(graph_dir, queue)
                removed = original_count - len(fresh_activities)
                print(
                    f"Cleaned {removed} stale drift queue entries (older than {max_age_hours}h)",
                    file=sys.stderr,
                )

            return cast(dict[Any, Any], queue)
        except Exception:
            pass
    return {"activities": [], "last_classification": None}


def save_drift_queue(graph_dir: Path, queue: dict[str, Any]) -> None:
    """Save the drift queue to file."""
    queue_path = graph_dir / DRIFT_QUEUE_FILE
    try:
        with open(queue_path, "w") as f:
            json.dump(queue, f, indent=2, default=str)
    except Exception as e:
        print(f"Warning: Could not save drift queue: {e}", file=sys.stderr)


def clear_drift_queue_activities(graph_dir: Path) -> None:
    """
    Clear activities from the drift queue after successful classification.

    This removes stale entries that have been processed, preventing indefinite accumulation.
    """
    queue_path = graph_dir / DRIFT_QUEUE_FILE
    try:
        # Load existing queue to preserve last_classification timestamp
        queue = {"activities": [], "last_classification": datetime.now().isoformat()}
        if queue_path.exists():
            with open(queue_path) as f:
                existing = json.load(f)
                # Preserve the classification timestamp if it exists
                if existing.get("last_classification"):
                    queue["last_classification"] = existing["last_classification"]

        # Save cleared queue
        with open(queue_path, "w") as f:
            json.dump(queue, f, indent=2)
    except Exception as e:
        print(f"Warning: Could not clear drift queue: {e}", file=sys.stderr)


def add_to_drift_queue(
    graph_dir: Path, activity: dict[str, Any], config: dict[str, Any]
) -> dict[str, Any]:
    """Add a high-drift activity to the queue."""
    max_age_hours = config.get("queue", {}).get("max_age_hours", 48)
    queue = load_drift_queue(graph_dir, max_age_hours=max_age_hours)
    max_pending = config.get("queue", {}).get("max_pending_classifications", 5)

    queue["activities"].append(
        {
            "timestamp": datetime.now(timezone.utc).isoformat(),
            "tool": activity.get("tool"),
            "summary": activity.get("summary"),
            "file_paths": activity.get("file_paths", []),
            "drift_score": activity.get("drift_score"),
            "feature_id": activity.get("feature_id"),
        }
    )

    # Keep only recent activities
    queue["activities"] = queue["activities"][-max_pending:]
    save_drift_queue(graph_dir, queue)
    return queue


def should_trigger_classification(
    queue: dict[str, Any], config: dict[str, Any]
) -> bool:
    """Check if we should trigger auto-classification."""
    drift_config = config.get("drift_detection", {})

    if not config.get("classification", {}).get("enabled", True):
        return False

    min_activities = drift_config.get("min_activities_before_classify", 3)
    cooldown_minutes = drift_config.get("cooldown_minutes", 10)

    # Check minimum activities threshold
    if len(queue.get("activities", [])) < min_activities:
        return False

    # Check cooldown
    last_classification = queue.get("last_classification")
    if last_classification:
        try:
            last_time = datetime.fromisoformat(last_classification)
            if datetime.now() - last_time < timedelta(minutes=cooldown_minutes):
                return False
        except Exception:
            pass

    return True


def build_classification_prompt(queue: dict[str, Any], feature_id: str) -> str:
    """Build the prompt for the classification agent."""
    activities = queue.get("activities", [])

    activity_lines = []
    for act in activities:
        line = f"- {act.get('tool', 'unknown')}: {act.get('summary', 'no summary')}"
        if act.get("file_paths"):
            line += f" (files: {', '.join(act['file_paths'][:2])})"
        line += f" [drift: {act.get('drift_score', 0):.2f}]"
        activity_lines.append(line)

    return f"""Classify these high-drift activities into a work item.

Current feature context: {feature_id}

Recent activities with high drift:
{chr(10).join(activity_lines)}

Based on the activity patterns:
1. Determine the work item type (bug, feature, spike, chore, or hotfix)
2. Create an appropriate title and description
3. Create the work item HTML file in .htmlgraph/

Use the classification rules:
- bug: fixing errors, incorrect behavior
- feature: new functionality, additions
- spike: research, exploration, investigation
- chore: maintenance, refactoring, cleanup
- hotfix: urgent production issues

Create the work item now using Write tool."""


def resolve_project_path(cwd: str | None = None) -> str:
    """Resolve project path (git root or cwd)."""
    start_dir = cwd or os.getcwd()
    try:
        result = subprocess.run(
            ["git", "rev-parse", "--show-toplevel"],
            capture_output=True,
            text=True,
            cwd=start_dir,
            timeout=5,
        )
        if result.returncode == 0:
            return result.stdout.strip()
    except Exception:
        pass
    return start_dir


def detect_model_from_hook_input(hook_input: dict[str, Any]) -> str | None:
    """
    Detect the Claude model from hook input data.

    Checks in order of priority:
    1. Task() model parameter (if tool_name == 'Task')
    2. HTMLGRAPH_MODEL environment variable (set by hooks)
    3. ANTHROPIC_MODEL or CLAUDE_MODEL environment variables
    4. Status line cache (for orchestrator tool calls)

    Args:
        hook_input: Hook input dict containing tool_name and tool_input

    Returns:
        Model name (e.g., 'claude-opus', 'claude-sonnet', 'claude-haiku') or None
    """
    # Get tool info
    tool_name_value: Any = hook_input.get("tool_name", "") or hook_input.get("name", "")
    tool_name = tool_name_value if isinstance(tool_name_value, str) else ""
    tool_input_value: Any = hook_input.get("tool_input", {}) or hook_input.get(
        "input", {}
    )
    tool_input = tool_input_value if isinstance(tool_input_value, dict) else {}

    # 1. Check for Task() model parameter first
    if tool_name == "Task" and "model" in tool_input:
        model_value: Any = tool_input.get("model")
        if model_value and isinstance(model_value, str):
            model = model_value.strip().lower()
            if model:
                if not model.startswith("claude-"):
                    model = f"claude-{model}"
                return cast(str, model)

    # 2. Check environment variables (set by PreToolUse hook)
    for env_var in ["HTMLGRAPH_MODEL", "ANTHROPIC_MODEL", "CLAUDE_MODEL"]:
        value = os.environ.get(env_var)
        if value and isinstance(value, str):
            model = value.strip()
            if model:
                return model

    # 3. Fallback to status line cache for orchestrator's own tool calls
    # This gives regular tool calls (Bash, Read, etc.) the model of the orchestrator
    session_id = hook_input.get("session_id") or hook_input.get("sessionId")
    if session_id:
        model_from_cache = get_model_from_status_cache(session_id)
        if model_from_cache:
            return model_from_cache

    return None


def detect_agent_from_environment() -> tuple[str, str | None]:
    """
    Detect the agent/model name from environment variables and status cache.

    Checks multiple sources in order of priority:
    1. HTMLGRAPH_AGENT - Explicit agent name set by user
    2. HTMLGRAPH_SUBAGENT_TYPE - For subagent sessions
    3. HTMLGRAPH_PARENT_AGENT - Parent agent context
    4. HTMLGRAPH_MODEL - Model name (e.g., claude-haiku, claude-opus)
    5. CLAUDE_MODEL - Model name if exposed by Claude Code
    6. ANTHROPIC_MODEL - Alternative model env var
    7. Status line cache (model only) - ~/.cache/claude-code/status-{session_id}.json

    Falls back to 'claude-code' if no environment variable is set.

    Returns:
        Tuple of (agent_id, model_name). Model name may be None if not detected.
    """
    # Check for explicit agent name first
    agent_id = None
    env_vars_agent = [
        "HTMLGRAPH_AGENT",
        "HTMLGRAPH_SUBAGENT_TYPE",
        "HTMLGRAPH_PARENT_AGENT",
    ]

    for var in env_vars_agent:
        value = os.environ.get(var)
        if value and value.strip():
            agent_id = value.strip()
            break

    # Check for model name separately
    model_name = None
    env_vars_model = [
        "HTMLGRAPH_MODEL",
        "CLAUDE_MODEL",
        "ANTHROPIC_MODEL",
    ]

    for var in env_vars_model:
        value = os.environ.get(var)
        if value and value.strip():
            model_name = value.strip()
            break

    # Fallback: Try to read model from status line cache
    if not model_name:
        model_name = get_model_from_status_cache()

    # Default fallback for agent_id
    if not agent_id:
        agent_id = "claude-code"

    return agent_id, model_name


def detect_subagent_context_from_database(
    db: HtmlGraphDB,
    current_session_id: str,
    parent_event_id_hint: str | None = None,
    current_tool_name: str | None = None,
) -> tuple[str | None, str | None, str | None]:
    """
    Detect if we're in a subagent context by checking for active task_delegation events.

    This is the DATABASE-BASED approach to subagent detection, which is necessary because
    environment variables set by PreToolUse hooks in the parent process do NOT propagate
    to subagent processes spawned by Claude Code's Task() tool.

    IMPORTANT CONTEXT:
    - Claude Code passes the SAME session_id to both parent and subagent hooks
    - Environment variables set in hooks don't persist (each hook is a new subprocess)
    - The only way to detect subagent context is through database state

    DETECTION STRATEGY:
    - If there's an active task_delegation (status='started') within the time window,
      AND the current tool is NOT the Task tool itself (to avoid self-detection),
      then we're likely in a subagent context.
    - The Task tool check is critical: when PostToolUse fires for the Task tool itself,
      we should NOT consider ourselves in a subagent context - we're the orchestrator
      that just finished delegating.

    Strategy (in order of precedence):
    1. If parent_event_id_hint is provided, look up that specific event directly
    2. Otherwise, query for task_delegation events with status='started' (fallback)
    3. If found within the last 5 minutes AND current tool is not Task, we're in subagent context
    4. Return the subagent_type and parent session info

    Args:
        db: HtmlGraphDB instance
        current_session_id: The session_id from hook_input (Claude Code's session ID)
        parent_event_id_hint: Optional event_id from environment variable for direct lookup.
                              This is the preferred method when available, as it correctly
                              handles parallel Task() calls.
        current_tool_name: The tool being executed. If "Task", we skip subagent detection
                           to avoid the orchestrator thinking it's a subagent.

    Returns:
        Tuple of (subagent_type, parent_session_id, parent_event_id)
        All None if not in subagent context
    """
    # Skip detection if the current tool is Task - we're the orchestrator, not a subagent
    if current_tool_name == "Task":
        print(
            "DEBUG detect_subagent_context_from_database: "
            "Skipping detection for Task tool (we're the orchestrator)",
            file=sys.stderr,
        )
        return None, None, None
    try:
        if db.connection is None:
            return None, None, None

        cursor = db.connection.cursor()

        # Priority 1: Direct lookup using parent_event_id_hint (handles parallel tasks correctly)
        if parent_event_id_hint:
            cursor.execute(
                """
                SELECT event_id, session_id, subagent_type, timestamp
                FROM agent_events
                WHERE event_id = ?
                AND event_type = 'task_delegation'
                AND status = 'started'
                """,
                (parent_event_id_hint,),
            )
            row = cursor.fetchone()

            if row:
                parent_event_id = row[0]
                parent_session_id = row[1]
                subagent_type = row[2] or "general-purpose"

                print(
                    f"Debug: Detected subagent context via hint: "
                    f"type={subagent_type}, parent_session={parent_session_id}, "
                    f"parent_event={parent_event_id}",
                    file=sys.stderr,
                )

                return subagent_type, parent_session_id, parent_event_id

            # Hint provided but event not found or not in 'started' status
            # This can happen if the event was already completed
            print(
                f"Debug: Parent event hint '{parent_event_id_hint}' not found or not active",
                file=sys.stderr,
            )

        # Priority 2: Fallback to most recent active task_delegation
        # WARNING: This can pick the wrong parent when multiple Task() calls run in parallel!
        # This is kept as a fallback for cases where environment variables don't propagate.
        #
        # IMPORTANT: We previously had `AND session_id != ?` to exclude the current session,
        # but this was WRONG. Claude Code passes the SAME session_id to subagent hooks as
        # to the parent session. So we need to find task_delegation events from the SAME
        # session that are in 'started' status (not yet completed).
        #
        # The key insight: when a subagent runs, the task_delegation event from the parent
        # is ALREADY in the database with status='started'. We just need to find it.
        print(
            f"DEBUG detect_subagent_context_from_database: "
            f"Querying for task_delegation events in session {current_session_id}",
            file=sys.stderr,
        )

        # First, let's see what task_delegation events exist at all (debug)
        cursor.execute(
            """
            SELECT event_id, session_id, subagent_type, status, timestamp
            FROM agent_events
            WHERE event_type = 'task_delegation'
            ORDER BY timestamp DESC
            LIMIT 5
            """
        )
        debug_rows = cursor.fetchall()
        print(
            f"DEBUG detect_subagent_context_from_database: "
            f"Recent task_delegation events: {debug_rows}",
            file=sys.stderr,
        )

        # Query for active task_delegation in the SAME session (or any session within time window)
        # The subagent may have the same session_id as parent, so we look for ANY active
        # task_delegation within the time window. The most recent one is likely our parent.
        cursor.execute(
            """
            SELECT event_id, session_id, subagent_type, timestamp
            FROM agent_events
            WHERE event_type = 'task_delegation'
            AND status = 'started'
            AND timestamp >= datetime('now', '-5 minutes')
            ORDER BY timestamp DESC
            LIMIT 1
            """
        )
        row = cursor.fetchone()
        print(
            f"DEBUG detect_subagent_context_from_database: "
            f"Fallback query result: {row}",
            file=sys.stderr,
        )

        if row:
            parent_event_id = row[0]
            parent_session_id = row[1]
            subagent_type = row[2] or "general-purpose"

            print(
                f"Debug: Detected subagent context from database (fallback): "
                f"type={subagent_type}, parent_session={parent_session_id}, "
                f"parent_event={parent_event_id}",
                file=sys.stderr,
            )

            return subagent_type, parent_session_id, parent_event_id

        return None, None, None

    except Exception as e:
        print(
            f"Debug: Error detecting subagent context from database: {e}",
            file=sys.stderr,
        )
        return None, None, None


def extract_file_paths(tool_input: dict[str, Any], tool_name: str) -> list[str]:
    """Extract file paths from tool input based on tool type."""
    paths = []

    # Common path fields
    for field in ["file_path", "path", "filepath"]:
        if field in tool_input:
            paths.append(tool_input[field])

    # Glob/Grep patterns
    if "pattern" in tool_input and tool_name in ["Glob", "Grep"]:
        pattern = tool_input.get("pattern", "")
        if "." in pattern:
            paths.append(f"pattern:{pattern}")

    # Bash commands - extract paths heuristically
    if tool_name == "Bash" and "command" in tool_input:
        cmd = tool_input["command"]
        file_matches = re.findall(r"[\w./\-_]+\.[a-zA-Z]{1,5}", cmd)
        paths.extend(file_matches[:3])

    return paths


def format_tool_summary(
    tool_name: str, tool_input: dict[str, Any], tool_result: dict | None = None
) -> str:
    """Format a human-readable summary of the tool call."""
    if tool_name == "Read":
        path = tool_input.get("file_path", "unknown")
        return f"Read: {path}"

    elif tool_name == "Write":
        path = tool_input.get("file_path", "unknown")
        return f"Write: {path}"

    elif tool_name == "Edit":
        path = tool_input.get("file_path", "unknown")
        old = tool_input.get("old_string", "")[:30]
        return f"Edit: {path} ({old}...)"

    elif tool_name == "Bash":
        cmd = tool_input.get("command", "")[:60]
        desc = tool_input.get("description", "")
        if desc:
            return f"Bash: {desc}"
        return f"Bash: {cmd}"

    elif tool_name == "Glob":
        pattern = tool_input.get("pattern", "")
        return f"Glob: {pattern}"

    elif tool_name == "Grep":
        pattern = tool_input.get("pattern", "")
        return f"Grep: {pattern}"

    elif tool_name == "Task":
        desc = tool_input.get("description", "")[:50]
        agent = tool_input.get("subagent_type", "")
        return f"Task ({agent}): {desc}"

    elif tool_name == "TodoWrite":
        todos = tool_input.get("todos", [])
        return f"TodoWrite: {len(todos)} items"

    elif tool_name == "WebSearch":
        query = tool_input.get("query", "")[:40]
        return f"WebSearch: {query}"

    elif tool_name == "WebFetch":
        url = tool_input.get("url", "")[:40]
        return f"WebFetch: {url}"

    elif tool_name == "UserQuery":
        # Extract the actual prompt text from the tool_input
        prompt = str(tool_input.get("prompt", ""))
        preview = prompt[:100].replace("\n", " ")
        if len(prompt) > 100:
            preview += "..."
        return preview

    else:
        return f"{tool_name}: {str(tool_input)[:50]}"


def record_event_to_sqlite(
    db: HtmlGraphDB,
    session_id: str,
    tool_name: str,
    tool_input: dict[str, Any],
    tool_response: dict[str, Any],
    is_error: bool,
    file_paths: list[str] | None = None,
    parent_event_id: str | None = None,
    agent_id: str | None = None,
    subagent_type: str | None = None,
    model: str | None = None,
    feature_id: str | None = None,
) -> str | None:
    """
    Record a tool call event to SQLite database for dashboard queries.

    Args:
        db: HtmlGraphDB instance
        session_id: Session ID from HtmlGraph
        tool_name: Name of the tool called
        tool_input: Tool input parameters
        tool_response: Tool response/result
        is_error: Whether the tool call resulted in an error
        file_paths: File paths affected by the tool
        parent_event_id: Parent event ID if this is a child event
        agent_id: Agent identifier (optional)
        subagent_type: Subagent type for Task delegations (optional)
        model: Claude model name (e.g., claude-haiku, claude-opus) (optional)
        feature_id: Feature ID for attribution (optional)

    Returns:
        event_id if successful, None otherwise
    """
    try:
        event_id = generate_id("event")
        input_summary = format_tool_summary(tool_name, tool_input, tool_response)

        # Build output summary from tool response
        output_summary = ""
        if isinstance(tool_response, dict):  # type: ignore[arg-type]
            if is_error:
                output_summary = tool_response.get("error", "error")[:200]
            else:
                # Extract summary from response
                content = tool_response.get("content", tool_response.get("output", ""))
                if isinstance(content, str):
                    output_summary = content[:200]
                elif isinstance(content, list):
                    output_summary = f"{len(content)} items"
                else:
                    output_summary = "success"

        # Build context metadata
        context = {
            "file_paths": file_paths or [],
            "tool_input_keys": list(tool_input.keys()),
            "is_error": is_error,
        }

        # Insert event to SQLite
        success = db.insert_event(
            event_id=event_id,
            agent_id=agent_id or "claude-code",
            event_type="tool_call",
            session_id=session_id,
            tool_name=tool_name,
            input_summary=input_summary,
            output_summary=output_summary,
            context=context,
            parent_event_id=parent_event_id,
            cost_tokens=0,
            subagent_type=subagent_type,
            model=model,
            feature_id=feature_id,
        )

        if success:
            return event_id
        return None

    except Exception as e:
        print(f"Warning: Could not record event to SQLite: {e}", file=sys.stderr)
        return None


def record_delegation_to_sqlite(
    db: HtmlGraphDB,
    session_id: str,
    from_agent: str,
    to_agent: str,
    task_description: str,
    task_input: dict[str, Any],
) -> str | None:
    """
    Record a Task() delegation to agent_collaboration table.

    Args:
        db: HtmlGraphDB instance
        session_id: Session ID from HtmlGraph
        from_agent: Agent delegating the task (usually 'orchestrator' or 'claude-code')
        to_agent: Target subagent type (e.g., 'general-purpose', 'researcher')
        task_description: Task description/prompt
        task_input: Full task input parameters

    Returns:
        handoff_id if successful, None otherwise
    """
    try:
        handoff_id = generate_id("handoff")

        # Build context with task input
        context = {
            "task_input_keys": list(task_input.keys()),
            "model": task_input.get("model"),
            "temperature": task_input.get("temperature"),
        }

        # Insert delegation record
        success = db.insert_collaboration(
            handoff_id=handoff_id,
            from_agent=from_agent,
            to_agent=to_agent,
            session_id=session_id,
            handoff_type="delegation",
            reason=task_description[:200],
            context=context,
        )

        if success:
            return handoff_id
        return None

    except Exception as e:
        print(f"Warning: Could not record delegation to SQLite: {e}", file=sys.stderr)
        return None


def track_event(hook_type: str, hook_input: dict[str, Any]) -> dict[str, Any]:
    """
    Track a hook event and log it to HtmlGraph (both HTML files and SQLite).

    Args:
        hook_type: Type of hook event ("PostToolUse", "Stop", "UserPromptSubmit")
        hook_input: Hook input data from stdin

    Returns:
        Response dict with {"continue": True} and optional hookSpecificOutput
    """
    # Check for debug mode (set HTMLGRAPH_DEBUG=1 to enable verbose logging)
    debug_mode = os.environ.get("HTMLGRAPH_DEBUG") == "1"

    if debug_mode:
        print(
            f"DEBUG track_event: hook_type={hook_type}, "
            f"session_id={hook_input.get('session_id')}, "
            f"tool_name={hook_input.get('tool_name', hook_input.get('name', 'unknown'))}",
            file=sys.stderr,
        )
        print(
            f"DEBUG track_event: ENV HTMLGRAPH_PARENT_EVENT="
            f"{os.environ.get('HTMLGRAPH_PARENT_EVENT')}, "
            f"HTMLGRAPH_SUBAGENT_TYPE={os.environ.get('HTMLGRAPH_SUBAGENT_TYPE')}, "
            f"HTMLGRAPH_PARENT_SESSION={os.environ.get('HTMLGRAPH_PARENT_SESSION')}",
            file=sys.stderr,
        )

    cwd = hook_input.get("cwd")
    project_dir = resolve_project_path(cwd if cwd else None)
    graph_dir = Path(project_dir) / ".htmlgraph"

    # Load drift configuration
    drift_config = load_drift_config()

    # Initialize SessionManager and SQLite DB
    try:
        manager = SessionManager(graph_dir)
    except Exception as e:
        print(f"Warning: Could not initialize SessionManager: {e}", file=sys.stderr)
        return {"continue": True}

    # Initialize SQLite database for event recording
    db = None
    try:
        from htmlgraph.config import get_database_path

        db = HtmlGraphDB(str(get_database_path()))
    except Exception as e:
        print(f"Warning: Could not initialize SQLite database: {e}", file=sys.stderr)
        # Continue without SQLite (graceful degradation)

    # Detect agent and model from environment
    detected_agent, detected_model = detect_agent_from_environment()

    # Also try to detect model from hook input (more specific than environment)
    model_from_input = detect_model_from_hook_input(hook_input)
    if model_from_input:
        detected_model = model_from_input

    active_session = None
    is_subagent_session = False
    parent_event_id_for_session = None

    # Get session_id from hook_input first (Claude Code provides this)
    hook_session_id = hook_input.get("session_id") or hook_input.get("sessionId")

    # Check if we're in a subagent context using multiple methods:
    #
    # PRECEDENCE ORDER (fixes parallel Task() bug):
    # 1. Sessions table - if THIS session is already marked as subagent, use stored parent info
    #    (fixes persistence issue for subsequent tool calls in same subagent)
    # 2. Environment variables - most reliable when available, correctly identifies
    #    the specific parent event even with multiple parallel Task() calls
    # 3. Database with hint - uses env var as hint for direct lookup
    # 4. Database fallback - picks most recent (WARNING: wrong for parallel tasks!)
    #
    # Method 0: Check if current session is already a subagent (CRITICAL for persistence!)
    # This fixes the issue where subsequent tool calls in the same subagent session
    # lose the parent_event_id linkage because task_delegation status may have changed.
    subagent_type = None
    parent_session_id = None
    parent_event_id_for_session = None

    if db and hook_session_id:
        try:
            cursor = db.connection.cursor()
            cursor.execute(
                """
                SELECT parent_session_id, parent_event_id, agent_assigned
                FROM sessions
                WHERE session_id = ? AND is_subagent = 1
                LIMIT 1
                """,
                (hook_session_id,),
            )
            row = cursor.fetchone()
            if row:
                parent_session_id = row[0]
                parent_event_id_for_session = row[1]
                # Extract subagent_type from agent_assigned (e.g., "general-purpose-spawner" -> "general-purpose")
                agent_assigned = row[2] or ""
                if agent_assigned and agent_assigned.endswith("-spawner"):
                    subagent_type = agent_assigned[:-8]  # Remove "-spawner" suffix
                else:
                    subagent_type = "general-purpose"  # Default if format unexpected

                print(
                    f"DEBUG subagent persistence: Found current session as subagent in sessions table: "
                    f"type={subagent_type}, parent_session={parent_session_id}, "
                    f"parent_event={parent_event_id_for_session}",
                    file=sys.stderr,
                )
        except Exception as e:
            print(
                f"DEBUG: Error checking sessions table for subagent: {e}",
                file=sys.stderr,
            )

    # Method 1: Environment variables (works if Task() spawner sets them)
    if not subagent_type:
        env_subagent_type = os.environ.get("HTMLGRAPH_SUBAGENT_TYPE")
        env_parent_session = os.environ.get("HTMLGRAPH_PARENT_SESSION")
        env_parent_event_id = os.environ.get("HTMLGRAPH_PARENT_EVENT")

        # DEBUG: Log environment variable detection
        print(
            f"DEBUG subagent detection (env): "
            f"subagent_type={env_subagent_type}, "
            f"parent_session={env_parent_session}, "
            f"parent_event={env_parent_event_id}",
            file=sys.stderr,
        )

        if env_subagent_type:
            subagent_type = env_subagent_type
            parent_session_id = env_parent_session
            parent_event_id_for_session = env_parent_event_id
            print(
                f"Debug: Using environment variables for subagent detection: "
                f"type={subagent_type}, parent={parent_session_id}",
                file=sys.stderr,
            )

    # Method 2: Database-based detection (CRITICAL for Claude Code Task() tool)
    # Environment variables may not propagate to subagent processes, so we check
    # the database for active task_delegation events.
    # IMPORTANT: Pass env_parent_event_id as hint to handle parallel Task() correctly
    # Get the current tool name for subagent detection
    current_tool_name = hook_input.get("tool_name") or hook_input.get("name")

    if not subagent_type and db and hook_session_id:
        print(
            f"DEBUG db detection: will_check=True, "
            f"current_tool_name={current_tool_name}",
            file=sys.stderr,
        )
        db_subagent_type, db_parent_session_id, db_parent_event_id = (
            detect_subagent_context_from_database(
                db,
                hook_session_id,
                parent_event_id_hint=parent_event_id_for_session
                or os.environ.get("HTMLGRAPH_PARENT_EVENT"),
                current_tool_name=current_tool_name,
            )
        )
        print(
            f"DEBUG db detection result: "
            f"db_subagent_type={db_subagent_type}, "
            f"db_parent_session_id={db_parent_session_id}, "
            f"db_parent_event_id={db_parent_event_id}",
            file=sys.stderr,
        )
        if db_subagent_type:
            subagent_type = db_subagent_type
            parent_session_id = db_parent_session_id
            # Only update parent_event_id if not already set
            if not parent_event_id_for_session:
                parent_event_id_for_session = db_parent_event_id
            print(
                f"Debug: Using database-based subagent detection: "
                f"type={subagent_type}, parent={parent_session_id}, "
                f"parent_event={parent_event_id_for_session}",
                file=sys.stderr,
            )

    # DEBUG: Log subagent context decision
    print(
        f"DEBUG subagent context decision: "
        f"subagent_type={subagent_type}, parent_session_id={parent_session_id}, "
        f"will_create_subagent_session={bool(subagent_type and parent_session_id)}",
        file=sys.stderr,
    )

    if subagent_type and parent_session_id:
        # We're in a subagent context
        is_subagent_session = True
        print(
            "DEBUG: SUBAGENT CONTEXT DETECTED! Creating subagent session...",
            file=sys.stderr,
        )

        # Use Claude's session_id (hook_session_id) as the subagent session ID
        # This ensures events are properly tracked to this session
        subagent_session_id = hook_session_id or f"{parent_session_id}-{subagent_type}"
        print(
            f"DEBUG: subagent_session_id={subagent_session_id}",
            file=sys.stderr,
        )

        # Check if session already exists in our system
        existing = manager.session_converter.load(subagent_session_id)
        if existing:
            active_session = existing
            print(
                f"Debug: Using existing subagent session: {subagent_session_id}",
                file=sys.stderr,
            )
        else:
            # Create new subagent session with parent link
            try:
                print(
                    f"DEBUG: Creating NEW subagent session with is_subagent=True, "
                    f"parent_session_id={parent_session_id}",
                    file=sys.stderr,
                )
                active_session = manager.start_session(
                    session_id=subagent_session_id,
                    agent=f"{subagent_type}-spawner",
                    is_subagent=True,
                    parent_session_id=parent_session_id,
                    title=f"{subagent_type.capitalize()} Subagent",
                )
                print(
                    f"Debug: Created subagent session: {subagent_session_id} "
                    f"(parent: {parent_session_id}, is_subagent=True)",
                    file=sys.stderr,
                )
            except Exception as e:
                print(
                    f"Warning: Could not create subagent session: {e}",
                    file=sys.stderr,
                )
                return {"continue": True}

        # Override detected agent for subagent context
        detected_agent = f"{subagent_type}-spawner"
    else:
        # Normal orchestrator/parent context
        # CRITICAL: Use session_id from hook_input (Claude Code provides this)
        # Only fall back to manager.get_active_session() if not in hook_input
        if hook_session_id:
            # Claude Code provided session_id - use it directly
            # Check if session already exists
            existing = manager.session_converter.load(hook_session_id)
            if existing:
                active_session = existing
            else:
                # Create new session with Claude's session_id
                try:
                    active_session = manager.start_session(
                        session_id=hook_session_id,
                        agent=detected_agent,
                        title=f"Session {datetime.now().strftime('%Y-%m-%d %H:%M')}",
                    )
                except Exception:
                    return {"continue": True}
        else:
            # Fallback: No session_id in hook_input - use global session cache
            active_session = manager.get_active_session()
            if not active_session:
                # No active HtmlGraph session yet; start one
                try:
                    active_session = manager.start_session(
                        session_id=None,
                        agent=detected_agent,
                        title=f"Session {datetime.now().strftime('%Y-%m-%d %H:%M')}",
                    )
                except Exception:
                    return {"continue": True}

    active_session_id = active_session.id

    # Ensure session exists in SQLite database (for foreign key constraints)
    if db:
        try:
            # Get attributes safely - MagicMock objects can cause SQLite binding errors
            # When getattr is called on a MagicMock, it returns another MagicMock, not the default
            def safe_getattr(obj: Any, attr: str, default: Any) -> Any:
                """Get attribute safely, returning default for MagicMock/invalid values."""
                try:
                    val = getattr(obj, attr, default)
                    # Check if it's a mock object (has _mock_name attribute)
                    if hasattr(val, "_mock_name"):
                        return default
                    return val
                except Exception:
                    return default

            # Use is_subagent_session flag from our detection, not just from session object
            is_subagent_raw = safe_getattr(active_session, "is_subagent", False)
            is_subagent_from_obj = (
                bool(is_subagent_raw) if isinstance(is_subagent_raw, bool) else False
            )
            # Prefer our detection (is_subagent_session) over object attribute
            final_is_subagent = is_subagent_session or is_subagent_from_obj

            transcript_id = safe_getattr(active_session, "transcript_id", None)
            transcript_path = safe_getattr(active_session, "transcript_path", None)
            # Ensure strings or None, not mock objects
            if transcript_id is not None and not isinstance(transcript_id, str):
                transcript_id = None
            if transcript_path is not None and not isinstance(transcript_path, str):
                transcript_path = None

            # Get parent_session_id from our detection or from session object
            final_parent_session_id = parent_session_id or safe_getattr(
                active_session, "parent_session_id", None
            )
            if final_parent_session_id is not None and not isinstance(
                final_parent_session_id, str
            ):
                final_parent_session_id = None

            db.insert_session(
                session_id=active_session_id,
                agent_assigned=safe_getattr(active_session, "agent", None)
                or detected_agent,
                parent_session_id=final_parent_session_id,
                parent_event_id=parent_event_id_for_session,
                is_subagent=final_is_subagent,
                transcript_id=transcript_id,
                transcript_path=transcript_path,
            )

            # Log subagent session creation for debugging
            if final_is_subagent:
                print(
                    f"Debug: Inserted subagent session to SQLite: "
                    f"session_id={active_session_id}, is_subagent=True, "
                    f"parent_session={final_parent_session_id}, "
                    f"parent_event={parent_event_id_for_session}",
                    file=sys.stderr,
                )
        except Exception as e:
            # Session may already exist, that's OK - continue
            print(
                f"Debug: Could not insert session to SQLite (may already exist): {e}",
                file=sys.stderr,
            )

    # Handle different hook types
    if hook_type == "Stop":
        # Session is ending - track stop event
        try:
            result = manager.track_activity(
                session_id=active_session_id, tool="Stop", summary="Agent stopped"
            )

            # Record to SQLite if available
            if db:
                record_event_to_sqlite(
                    db=db,
                    session_id=active_session_id,
                    tool_name="Stop",
                    tool_input={},
                    tool_response={"content": "Agent stopped"},
                    is_error=False,
                    agent_id=detected_agent,
                    model=detected_model,
                    feature_id=result.feature_id if result else None,
                )
        except Exception as e:
            print(f"Warning: Could not track stop: {e}", file=sys.stderr)
        return {"continue": True}

    elif hook_type == "UserPromptSubmit":
        # User submitted a query
        prompt = hook_input.get("prompt", "")
        preview = prompt[:100].replace("\n", " ")
        if len(prompt) > 100:
            preview += "..."

        try:
            result = manager.track_activity(
                session_id=active_session_id, tool="UserQuery", summary=f'"{preview}"'
            )

            # Record to SQLite if available
            # UserQuery event is stored in database - no file-based state needed
            # Subsequent tool calls query database for parent via get_parent_user_query()
            if db:
                record_event_to_sqlite(
                    db=db,
                    session_id=active_session_id,
                    tool_name="UserQuery",
                    tool_input={"prompt": prompt},
                    tool_response={"content": "Query received"},
                    is_error=False,
                    agent_id=detected_agent,
                    model=detected_model,
                    feature_id=result.feature_id if result else None,
                )

        except Exception as e:
            print(f"Warning: Could not track query: {e}", file=sys.stderr)
        return {"continue": True}

    elif hook_type == "PostToolUse":
        # Tool was used - track it
        tool_name = hook_input.get("tool_name", "unknown")
        tool_input_data = hook_input.get("tool_input", {})
        tool_response = (
            hook_input.get("tool_response", hook_input.get("tool_result", {})) or {}
        )

        # Skip tracking for some tools
        skip_tools = {"AskUserQuestion"}
        if tool_name in skip_tools:
            return {"continue": True}

        # Extract file paths
        file_paths = extract_file_paths(tool_input_data, tool_name)

        # Format summary
        summary = format_tool_summary(tool_name, tool_input_data, tool_response)

        # Determine success
        if isinstance(tool_response, dict):  # type: ignore[arg-type]
            success_field = tool_response.get("success")
            if isinstance(success_field, bool):
                is_error = not success_field
            else:
                is_error = bool(tool_response.get("is_error", False))

            # Additional check for Bash failures: detect non-zero exit codes
            if tool_name == "Bash" and not is_error:
                output = str(
                    tool_response.get("output", "") or tool_response.get("content", "")
                )
                # Check for exit code patterns (e.g., "Exit code 1", "exit status 1")
                if re.search(
                    r"Exit code [1-9]\d*|exit status [1-9]\d*", output, re.IGNORECASE
                ):
                    is_error = True
        else:
            # For list or other non-dict responses (like Playwright), assume success
            is_error = False

        # Get drift thresholds from config
        drift_settings = drift_config.get("drift_detection", {})
        warning_threshold = drift_settings.get("warning_threshold") or 0.7
        auto_classify_threshold = drift_settings.get("auto_classify_threshold") or 0.85

        # Determine parent activity context using multiple sources
        parent_activity_id = None

        # Priority 1: If we're in a subagent context, use the parent event from
        # the task_delegation that spawned us (detected from database)
        if is_subagent_session and parent_event_id_for_session:
            parent_activity_id = parent_event_id_for_session
            print(
                f"Debug: Using parent_event from subagent detection: {parent_activity_id}",
                file=sys.stderr,
            )
        # Priority 2: Check environment variable for cross-process parent linking
        # This is set by PreToolUse hook when Task() spawns a subagent
        else:
            env_parent = os.environ.get("HTMLGRAPH_PARENT_EVENT") or os.environ.get(
                "HTMLGRAPH_PARENT_QUERY_EVENT"
            )
            if env_parent:
                parent_activity_id = env_parent
            # Priority 3: Query database for most recent UserQuery event as parent
            # Database is the single source of truth for parent-child linking
            elif db:
                parent_activity_id = get_parent_user_query(db, active_session_id)

        # Track the activity
        nudge = None
        try:
            result = manager.track_activity(
                session_id=active_session_id,
                tool=tool_name,
                summary=summary,
                file_paths=file_paths if file_paths else None,
                success=not is_error,
                parent_activity_id=parent_activity_id,
            )

            # Record to SQLite if available
            if db:
                # Extract subagent_type for Task delegations
                task_subagent_type = None
                if tool_name == "Task":
                    task_subagent_type = tool_input_data.get(
                        "subagent_type", "general-purpose"
                    )

                record_event_to_sqlite(
                    db=db,
                    session_id=active_session_id,
                    tool_name=tool_name,
                    tool_input=tool_input_data,
                    tool_response=tool_response,
                    is_error=is_error,
                    file_paths=file_paths if file_paths else None,
                    parent_event_id=parent_activity_id,  # Link to parent event
                    agent_id=detected_agent,
                    subagent_type=task_subagent_type,
                    model=detected_model,
                    feature_id=result.feature_id if result else None,
                )

            # If this was a Task() delegation, also record to agent_collaboration
            if tool_name == "Task" and db:
                subagent = tool_input_data.get("subagent_type", "general-purpose")
                description = tool_input_data.get("description", "")
                record_delegation_to_sqlite(
                    db=db,
                    session_id=active_session_id,
                    from_agent=detected_agent,
                    to_agent=subagent,
                    task_description=description,
                    task_input=tool_input_data,
                )

            # Check for drift and handle accordingly
            # Skip drift detection for child activities (they inherit parent's context)
            if result and hasattr(result, "drift_score") and not parent_activity_id:
                drift_score = result.drift_score
                feature_id = getattr(result, "feature_id", "unknown")

                # Skip drift detection if no score available
                if drift_score is None:
                    pass  # No active features - can't calculate drift
                elif drift_score >= auto_classify_threshold:
                    # High drift - add to classification queue
                    queue = add_to_drift_queue(
                        graph_dir,
                        {
                            "tool": tool_name,
                            "summary": summary,
                            "file_paths": file_paths,
                            "drift_score": drift_score,
                            "feature_id": feature_id,
                        },
                        drift_config,
                    )

                    # Check if we should trigger classification
                    if should_trigger_classification(queue, drift_config):
                        classification_prompt = build_classification_prompt(
                            queue, feature_id
                        )

                        # Try to run headless classification
                        use_headless = drift_config.get("classification", {}).get(
                            "use_headless", True
                        )
                        if use_headless:
                            try:
                                # Run claude in print mode for classification
                                proc_result = subprocess.run(
                                    [
                                        "claude",
                                        "-p",
                                        classification_prompt,
                                        "--model",
                                        "haiku",
                                        "--dangerously-skip-permissions",
                                    ],
                                    capture_output=True,
                                    text=True,
                                    timeout=120,
                                    cwd=str(graph_dir.parent),
                                    env={
                                        **os.environ,
                                        # Prevent hooks from writing new HtmlGraph sessions/events
                                        # when we spawn nested `claude` processes.
                                        "HTMLGRAPH_DISABLE_TRACKING": "1",
                                    },
                                )
                                if proc_result.returncode == 0:
                                    nudge = "Drift auto-classification completed. Check .htmlgraph/ for new work item."
                                    # Clear the queue after successful classification
                                    clear_drift_queue_activities(graph_dir)
                                else:
                                    # Fallback to manual prompt
                                    nudge = f"""HIGH DRIFT ({drift_score:.2f}) - Headless classification failed.

{len(queue["activities"])} activities don't align with '{feature_id}'.

Please classify manually: bug, feature, spike, or chore in .htmlgraph/"""
                            except Exception as e:
                                nudge = f"Drift classification error: {e}. Please classify manually."
                        else:
                            nudge = f"""HIGH DRIFT DETECTED ({drift_score:.2f}) - Auto-classification triggered.

{len(queue["activities"])} activities don't align with '{feature_id}'.

ACTION REQUIRED: Spawn a Haiku agent to classify this work:
```
Task tool with subagent_type="general-purpose", model="haiku", prompt:
{classification_prompt[:500]}...
```

Or manually create a work item in .htmlgraph/ (bug, feature, spike, or chore)."""

                        # Mark classification as triggered
                        queue["last_classification"] = datetime.now(
                            timezone.utc
                        ).isoformat()
                        save_drift_queue(graph_dir, queue)
                    else:
                        nudge = f"Drift detected ({drift_score:.2f}): Activity queued for classification ({len(queue['activities'])}/{drift_settings.get('min_activities_before_classify', 3)} needed)."

                elif drift_score > warning_threshold:
                    # Moderate drift - just warn
                    nudge = f"Drift detected ({drift_score:.2f}): Activity may not align with {feature_id}. Consider refocusing or updating the feature."

        except Exception as e:
            print(f"Warning: Could not track activity: {e}", file=sys.stderr)

        # Build response
        response: dict[str, Any] = {"continue": True}
        if nudge:
            response["hookSpecificOutput"] = {
                "hookEventName": hook_type,
                "additionalContext": nudge,
            }
        return response

    # Unknown hook type
    return {"continue": True}
