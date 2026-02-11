import logging
import sys

logger = logging.getLogger(__name__)

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
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any, cast  # noqa: F401

from htmlgraph.db.schema import HtmlGraphDB
from htmlgraph.ids import generate_id
from htmlgraph.session_manager import SessionManager

# Global presence manager instance (initialized on first use)
_presence_manager = None


def get_presence_manager() -> Any:
    """Get or create global PresenceManager instance."""
    global _presence_manager
    if _presence_manager is None:
        try:
            from htmlgraph.api.presence import PresenceManager
            from htmlgraph.config import get_database_path

            _presence_manager = PresenceManager(db_path=str(get_database_path()))
        except Exception as e:
            logger.warning(f"Could not initialize PresenceManager: {e}")
            _presence_manager = None
    return _presence_manager


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
        logger.warning(f"Debug: Database query for UserQuery failed: {e}")
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
                logger.warning(
                    f"Cleaned {removed} stale drift queue entries (older than {max_age_hours}h)"
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
        logger.warning(f"Warning: Could not save drift queue: {e}")


def clear_drift_queue_activities(graph_dir: Path) -> None:
    """
    Clear activities from the drift queue after successful classification.

    This removes stale entries that have been processed, preventing indefinite accumulation.
    """
    queue_path = graph_dir / DRIFT_QUEUE_FILE
    try:
        # Load existing queue to preserve last_classification timestamp
        queue = {
            "activities": [],
            "last_classification": datetime.now().strftime("%Y-%m-%d %H:%M:%S"),
        }
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
        logger.warning(f"Warning: Could not clear drift queue: {e}")


def add_to_drift_queue(
    graph_dir: Path, activity: dict[str, Any], config: dict[str, Any]
) -> dict[str, Any]:
    """Add a high-drift activity to the queue."""
    max_age_hours = config.get("queue", {}).get("max_age_hours", 48)
    queue = load_drift_queue(graph_dir, max_age_hours=max_age_hours)
    max_pending = config.get("queue", {}).get("max_pending_classifications", 5)

    queue["activities"].append(
        {
            "timestamp": datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M:%S"),
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

    return None


def get_model_from_parent_event(db_path: str | None = None) -> str | None:
    """
    Look up the model from the parent Task delegation event in the database.

    This is used when a child event (Read, Bash, Grep, etc.) is running in a subagent
    and needs to inherit the model from the parent Task that delegated to it.

    Args:
        db_path: Optional database path. If not provided, uses default path.

    Returns:
        Model name from parent event if found, None otherwise.
    """
    parent_event_id = os.environ.get("HTMLGRAPH_PARENT_EVENT")
    if not parent_event_id:
        return None

    try:
        from htmlgraph.config import get_database_path
        from htmlgraph.db.schema import HtmlGraphDB

        path = db_path or str(get_database_path())
        db = HtmlGraphDB(path)
        if db.connection is None:
            return None
        cursor = db.connection.cursor()
        cursor.execute(
            "SELECT model FROM agent_events WHERE event_id = ? LIMIT 1",
            (parent_event_id,),
        )
        row = cursor.fetchone()
        if row and row[0]:
            return str(row[0])
    except Exception:
        pass
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
    7. Parent event model (from database) - If HTMLGRAPH_PARENT_EVENT is set
    8. Status line cache (model only) - ~/.cache/claude-code/status-{session_id}.json

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

    # NEW: Check parent event model from database (before status cache fallback)
    if not model_name:
        model_name = get_model_from_parent_event()

    # Fallback: Try to read model from status line cache
    if not model_name:
        model_name = get_model_from_status_cache()

    # Default fallback for agent_id
    if not agent_id:
        agent_id = "claude-code"

    return agent_id, model_name


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
    """
    Format a human-readable summary of the tool call.

    Returns only the description part (without tool name prefix) since tool_name
    is stored as a separate field in the database. Frontend can format as needed.
    """
    if tool_name == "Read":
        path = str(tool_input.get("file_path", "unknown"))
        return path

    elif tool_name == "Write":
        path = str(tool_input.get("file_path", "unknown"))
        return path

    elif tool_name == "Edit":
        path = str(tool_input.get("file_path", "unknown"))
        old = str(tool_input.get("old_string", ""))[:30]
        return f"{path} ({old}...)"

    elif tool_name == "Bash":
        cmd = str(tool_input.get("command", ""))[:60]
        desc = str(tool_input.get("description", ""))
        if desc:
            return desc
        return cmd

    elif tool_name == "Glob":
        pattern = str(tool_input.get("pattern", ""))
        return pattern

    elif tool_name == "Grep":
        pattern = str(tool_input.get("pattern", ""))
        return pattern

    elif tool_name == "Task":
        desc = str(tool_input.get("description", ""))[:50]
        agent = str(tool_input.get("subagent_type", ""))
        return f"({agent}): {desc}"

    elif tool_name == "TodoWrite":
        todos = tool_input.get("todos", [])
        return f"{len(todos)} items"

    elif tool_name == "WebSearch":
        query = str(tool_input.get("query", ""))[:40]
        return query

    elif tool_name == "WebFetch":
        url = str(tool_input.get("url", ""))[:40]
        return url

    elif tool_name == "UserQuery":
        # Extract the actual prompt text from the tool_input
        prompt = str(tool_input.get("prompt", ""))
        preview = prompt[:100].replace("\n", " ")
        if len(prompt) > 100:
            preview += "..."
        return preview

    else:
        return str(tool_input)[:50]


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
    claude_task_id: str | None = None,
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
        claude_task_id: Claude Code's internal task ID for tool attribution (optional)

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

        # If we have a parent event, inherit its model (child events inherit from parent Task)
        if parent_event_id and db and db.connection:
            try:
                cursor = db.connection.cursor()
                cursor.execute(
                    "SELECT model FROM agent_events WHERE event_id = ? LIMIT 1",
                    (parent_event_id,),
                )
                row = cursor.fetchone()
                if row and row[0]:
                    model = row[0]  # Inherit parent's model
            except Exception:
                pass

        # Build context metadata
        context = {
            "file_paths": file_paths or [],
            "tool_input_keys": list(tool_input.keys()),
            "is_error": is_error,
        }

        # Extract task_id from Tool response if not provided
        if (
            not claude_task_id
            and tool_name == "Task"
            and isinstance(tool_response, dict)
        ):
            claude_task_id = tool_response.get("task_id")

        # Insert event to SQLite
        success = db.insert_event(
            event_id=event_id,
            agent_id=agent_id or "claude-code",
            event_type="tool_call",
            session_id=session_id,
            tool_name=tool_name,
            input_summary=input_summary,
            tool_input=tool_input,  # CRITICAL: Pass tool_input for dashboard display
            output_summary=output_summary,
            context=context,
            parent_event_id=parent_event_id,
            cost_tokens=0,
            subagent_type=subagent_type,
            model=model,
            feature_id=feature_id,
            claude_task_id=claude_task_id,
        )

        if success:
            # Also insert into live_events for real-time WebSocket dashboard
            try:
                event_data = {
                    "tool": tool_name,
                    "summary": input_summary,
                    "success": not is_error,
                    "feature_id": feature_id,
                    "file_paths": file_paths,
                    "timestamp": datetime.now(timezone.utc).strftime(
                        "%Y-%m-%d %H:%M:%S"
                    ),
                }

                db.insert_live_event(
                    event_type="tool_call",
                    event_data=event_data,
                    parent_event_id=parent_event_id,
                    session_id=session_id,
                    spawner_type=None,
                )
            except Exception as e:
                # Don't fail the hook if live event insertion fails
                logger.debug(f"Could not insert live event: {e}")

            return event_id
        return None

    except Exception as e:
        logger.warning(f"Warning: Could not record event to SQLite: {e}")
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
        logger.warning(f"Warning: Could not record delegation to SQLite: {e}")
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
    cwd = hook_input.get("cwd")
    project_dir = resolve_project_path(cwd if cwd else None)
    graph_dir = Path(project_dir) / ".htmlgraph"

    # Load drift configuration
    drift_config = load_drift_config()

    # Initialize SessionManager and SQLite DB
    try:
        manager = SessionManager(graph_dir)
    except Exception as e:
        logger.warning(f"Warning: Could not initialize SessionManager: {e}")
        return {"continue": True}

    # Initialize SQLite database for event recording
    db = None
    try:
        from htmlgraph.config import get_database_path
        from htmlgraph.db.schema import HtmlGraphDB

        db = HtmlGraphDB(str(get_database_path()))
    except Exception as e:
        logger.warning(f"Warning: Could not initialize SQLite database: {e}")
        # Continue without SQLite (graceful degradation)

    # Detect agent and model from environment
    detected_agent, detected_model = detect_agent_from_environment()

    # Also try to detect model from hook input (more specific than environment)
    model_from_input = detect_model_from_hook_input(hook_input)
    if model_from_input:
        detected_model = model_from_input

    active_session = None

    # Check if we're in a subagent context using multiple methods:
    #
    # PRECEDENCE ORDER:
    # 1. Sessions table - if THIS session is already marked as subagent, use stored parent info
    #    (fixes persistence issue for subsequent tool calls in same subagent)
    # 2. Environment variables - set by spawner router for first tool call
    # 3. Fallback to normal orchestrator context
    #
    # Method 1: Check if current session is already a subagent (CRITICAL for persistence!)
    # This fixes the issue where subsequent tool calls in the same subagent session
    # lose the parent_event_id linkage.
    subagent_type = None
    parent_session_id = None
    task_event_id_from_db = None  # Will be set by Method 1 if found
    hook_session_id = hook_input.get("session_id") or hook_input.get("sessionId")

    if db and db.connection and hook_session_id:
        try:
            cursor = db.connection.cursor()
            cursor.execute(
                """
                SELECT parent_session_id, agent_assigned
                FROM sessions
                WHERE session_id = ? AND is_subagent = 1
                LIMIT 1
                """,
                (hook_session_id,),
            )
            row = cursor.fetchone()
            if row:
                parent_session_id = row[0]
                # Extract subagent_type from agent_assigned (e.g., "general-purpose-spawner" -> "general-purpose")
                agent_assigned = row[1] or ""
                if agent_assigned and agent_assigned.endswith("-spawner"):
                    subagent_type = agent_assigned[:-8]  # Remove "-spawner" suffix
                else:
                    subagent_type = "general-purpose"  # Default if format unexpected

                # CRITICAL FIX: When Method 1 succeeds, also find the task_delegation event!
                # This ensures parent_activity_id will use the task event, not fall back to UserQuery
                try:
                    # First try to find task in parent_session_id (if not NULL)
                    if parent_session_id:
                        cursor.execute(
                            """
                            SELECT event_id
                            FROM agent_events
                            WHERE event_type = 'task_delegation'
                              AND subagent_type = ?
                              AND status = 'started'
                              AND session_id = ?
                            ORDER BY timestamp DESC
                            LIMIT 1
                            """,
                            (subagent_type, parent_session_id),
                        )
                        task_row = cursor.fetchone()
                        if task_row:
                            task_event_id_from_db = task_row[0]

                    # If not found (parent_session_id is NULL), fallback to finding most recent task
                    # This handles Claude Code's session reuse where parent_session_id can be NULL
                    if not task_event_id_from_db:
                        cursor.execute(
                            """
                            SELECT event_id
                            FROM agent_events
                            WHERE event_type = 'task_delegation'
                              AND subagent_type = ?
                              AND status = 'started'
                            ORDER BY timestamp DESC
                            LIMIT 1
                            """,
                            (subagent_type,),
                        )
                        task_row = cursor.fetchone()
                        if task_row:
                            task_event_id_from_db = task_row[0]
                            logger.warning(
                                f"DEBUG Method 1 fallback: Found task_delegation={task_event_id_from_db} for {subagent_type}"
                            )
                        else:
                            logger.warning(
                                f"DEBUG Method 1: No task_delegation found for subagent_type={subagent_type}"
                            )
                    else:
                        logger.warning(
                            f"DEBUG Method 1: Found task_delegation={task_event_id_from_db} for subagent {subagent_type}"
                        )
                except Exception as e:
                    logger.warning(
                        f"DEBUG: Error finding task_delegation for Method 1: {e}"
                    )

                logger.debug(
                    f"DEBUG subagent persistence: Found current session as subagent in sessions table: "
                    f"type={subagent_type}, parent_session={parent_session_id}, task_event={task_event_id_from_db}",
                )
        except Exception as e:
            logger.warning(f"DEBUG: Error checking sessions table for subagent: {e}")

    # Method 2: Environment variables (for first tool call before session table is populated)
    if not subagent_type:
        subagent_type = os.environ.get("HTMLGRAPH_SUBAGENT_TYPE")
        parent_session_id = os.environ.get("HTMLGRAPH_PARENT_SESSION")

    # Method 3: Database detection of active task_delegation events
    # CRITICAL: When Task() subprocess is launched, environment variables don't propagate
    # So we must query the database for active task_delegation events to detect subagent context
    # NOTE: Claude Code passes the SAME session_id to parent and subagent, so we CAN'T use
    # session_id to distinguish them. Instead, look for the most recent task_delegation event
    # and if found with status='started', we ARE the subagent.
    #
    # CRITICAL FIX: The actual PARENT session is hook_session_id (what Claude Code passes),
    # NOT the session_id from the task_delegation event (which is the same as current).
    # NOTE: DO NOT reinitialize task_event_id_from_db here - it may have been set by Method 1!
    if not subagent_type and db and db.connection:
        try:
            cursor = db.connection.cursor()
            # Find the most recent active task_delegation event
            cursor.execute(
                """
                SELECT event_id, subagent_type, session_id
                FROM agent_events
                WHERE event_type = 'task_delegation'
                  AND status = 'started'
                  AND tool_name = 'Task'
                ORDER BY timestamp DESC
                LIMIT 1
                """,
            )
            row = cursor.fetchone()
            if row:
                task_event_id, detected_subagent_type, parent_sess = row
                # If we found an active task_delegation, we're running as a subagent
                # (Claude Code uses the same session_id for both parent and subagent)
                subagent_type = detected_subagent_type or "general-purpose"
                # IMPORTANT: Use the hook_session_id as parent, not parent_sess!
                # The parent_sess from task_delegation is the same as current session
                # (Claude Code reuses session_id). The actual parent is hook_session_id.
                parent_session_id = hook_session_id
                task_event_id_from_db = (
                    task_event_id  # Store for later use as parent_event_id
                )
                logger.debug(
                    f"DEBUG subagent detection (database): Detected active task_delegation "
                    f"type={subagent_type}, parent_session={parent_session_id}, "
                    f"parent_event={task_event_id}"
                )
        except Exception as e:
            logger.warning(f"DEBUG: Error detecting subagent from database: {e}")

    if subagent_type and parent_session_id:
        # We're in a subagent - create or get subagent session
        # Use deterministic session ID based on parent + subagent type
        subagent_session_id = f"{parent_session_id}-{subagent_type}"

        # Check if subagent session already exists
        existing = manager.session_converter.load(subagent_session_id)
        if existing:
            active_session = existing
            logger.warning(
                f"Debug: Using existing subagent session: {subagent_session_id}"
            )
        else:
            # Create new subagent session with parent link
            try:
                active_session = manager.start_session(
                    session_id=subagent_session_id,
                    agent=f"{subagent_type}-spawner",
                    is_subagent=True,
                    parent_session_id=parent_session_id,
                    title=f"{subagent_type.capitalize()} Subagent",
                )
                logger.debug(
                    f"Debug: Created subagent session: {subagent_session_id} "
                    f"(parent: {parent_session_id})"
                )
            except Exception as e:
                logger.warning(f"Warning: Could not create subagent session: {e}")
                return {"continue": True}

        # Override detected agent for subagent context
        detected_agent = f"{subagent_type}-spawner"
    else:
        # Normal orchestrator/parent context
        # CRITICAL: Use session_id from hook_input (Claude Code provides this)
        # Only fall back to manager.get_active_session() if not in hook_input
        # hook_session_id already defined at line 730

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

            is_subagent_raw = safe_getattr(active_session, "is_subagent", False)
            is_subagent = (
                bool(is_subagent_raw) if isinstance(is_subagent_raw, bool) else False
            )

            transcript_id = safe_getattr(active_session, "transcript_id", None)
            transcript_path = safe_getattr(active_session, "transcript_path", None)
            # Ensure strings or None, not mock objects
            if transcript_id is not None and not isinstance(transcript_id, str):
                transcript_id = None
            if transcript_path is not None and not isinstance(transcript_path, str):
                transcript_path = None

            db.insert_session(
                session_id=active_session_id,
                agent_assigned=safe_getattr(active_session, "agent", None)
                or detected_agent,
                is_subagent=is_subagent,
                transcript_id=transcript_id,
                transcript_path=transcript_path,
            )
        except Exception as e:
            # Session may already exist, that's OK - continue
            logger.warning(
                f"Debug: Could not insert session to SQLite (may already exist): {e}"
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

            # Update presence - mark as offline
            presence_mgr = get_presence_manager()
            if presence_mgr:
                presence_mgr.mark_offline(detected_agent)
        except Exception as e:
            logger.warning(f"Warning: Could not track stop: {e}")
        return {"continue": True}

    elif hook_type == "UserPromptSubmit":
        # User submitted a query
        prompt = hook_input.get("prompt", "")

        print(
            f"[DEBUG UserPromptSubmit] REACHED HANDLER. active_session_id={active_session_id}, "
            f"subagent_type={subagent_type}, parent_session_id={parent_session_id}, "
            f"prompt_preview={prompt[:50]}...",
            file=sys.stderr,
        )

        # CRITICAL FIX: Filter out task notifications from Claude Code's background task system
        # Task notifications are NOT user conversation turns - they're system messages about
        # completed background tasks. These should not appear in the activity feed as UserQuery events.
        if prompt.strip().startswith("<task-notification>"):
            logger.debug("Skipping task notification (not a user query)")
            print(
                "[DEBUG UserPromptSubmit] SKIPPED: Task notification", file=sys.stderr
            )
            return {"continue": True}

        preview = prompt[:100].replace("\n", " ")
        if len(prompt) > 100:
            preview += "..."

        # CRITICAL FIX: UserQuery events MUST be in the parent session, not subagent session
        # When in subagent context, active_session_id is the subagent session (e.g., "abc123-general-purpose")
        # But UserQuery should be in parent session (e.g., "abc123") for proper event hierarchy
        # Solution: Use parent_session_id if we're in subagent context, otherwise use active_session_id
        userquery_session_id = active_session_id
        if subagent_type and parent_session_id:
            # We're in a subagent - record UserQuery to PARENT session
            userquery_session_id = parent_session_id
            logger.debug(
                f"UserPromptSubmit in subagent context: Recording to parent session {parent_session_id} "
                f"instead of subagent session {active_session_id}"
            )
        else:
            # DEFENSIVE FALLBACK: Strip known subagent suffixes if Methods 1-3 failed to detect
            # This handles edge cases where subagent detection fails but session_id has subagent suffix
            known_suffixes = [
                "-general-purpose",
                "-Explore",
                "-Bash",
                "-Plan",
                "-researcher",
                "-debugger",
                "-test-runner",
            ]
            for suffix in known_suffixes:
                if active_session_id.endswith(suffix):
                    userquery_session_id = active_session_id[: -len(suffix)]
                    print(
                        f"[DEBUG UserPromptSubmit] DEFENSIVE FALLBACK: Stripped suffix '{suffix}' from session_id. "
                        f"Original: {active_session_id}, Parent: {userquery_session_id}",
                        file=sys.stderr,
                    )
                    break

        print(
            f"[DEBUG UserPromptSubmit] FINAL DECISION: Recording UserQuery to session_id={userquery_session_id}",
            file=sys.stderr,
        )

        try:
            result = manager.track_activity(
                session_id=userquery_session_id,
                tool="UserQuery",
                summary=f'"{preview}"',
            )

            # Record to SQLite if available
            # UserQuery event is stored in database - no file-based state needed
            # Subsequent tool calls query database for parent via get_parent_user_query()
            if db:
                event_id = record_event_to_sqlite(
                    db=db,
                    session_id=userquery_session_id,  # Use parent session, not subagent
                    tool_name="UserQuery",
                    tool_input={"prompt": prompt},
                    tool_response={"content": "Query received"},
                    is_error=False,
                    agent_id=detected_agent,
                    model=detected_model,
                    feature_id=result.feature_id if result else None,
                )

                # Update presence
                presence_mgr = get_presence_manager()
                if presence_mgr and event_id:
                    presence_mgr.update_presence(
                        agent_id=detected_agent,
                        event={
                            "tool_name": "UserQuery",
                            "session_id": userquery_session_id,  # Use parent session
                            "feature_id": result.feature_id if result else None,
                            "event_id": event_id,
                        },
                    )

        except Exception as e:
            logger.warning(f"Warning: Could not track query: {e}")
        return {"continue": True}

    elif hook_type == "TaskCompleted":
        # Task delegation completed - update task_delegation event status
        try:
            if db and db.connection:
                cursor = db.connection.cursor()

                # Find the most recent task_delegation event with status='started'
                cursor.execute(
                    """
                    SELECT event_id, subagent_type
                    FROM agent_events
                    WHERE session_id = ?
                      AND event_type = 'task_delegation'
                      AND status = 'started'
                    ORDER BY timestamp DESC
                    LIMIT 1
                    """,
                    (active_session_id,),
                )
                row = cursor.fetchone()

                if row:
                    task_event_id, subagent_type_val = row

                    # Extract result summary from hook_input if available
                    result_summary = hook_input.get("result", "Task completed")
                    if isinstance(result_summary, dict):
                        result_summary = str(
                            result_summary.get("summary", "Task completed")
                        )

                    # Update the task_delegation event to status='completed'
                    cursor.execute(
                        """
                        UPDATE agent_events
                        SET status = 'completed',
                            output_summary = ?,
                            updated_at = CURRENT_TIMESTAMP
                        WHERE event_id = ?
                        """,
                        (result_summary[:200], task_event_id),
                    )

                    # Create a new task_completed event linked to the task_delegation
                    completed_event_id = generate_id("event")
                    cursor.execute(
                        """
                        INSERT INTO agent_events
                        (event_id, agent_id, event_type, session_id, tool_name,
                         input_summary, output_summary, parent_event_id, subagent_type,
                         status, model)
                        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                        """,
                        (
                            completed_event_id,
                            detected_agent,
                            "task_completed",
                            active_session_id,
                            "TaskCompleted",
                            f"Task {subagent_type_val} completed",
                            result_summary[:200],
                            task_event_id,  # Link to parent task_delegation event
                            subagent_type_val,
                            "completed",
                            detected_model,
                        ),
                    )

                    db.connection.commit()
                    logger.debug(
                        f"TaskCompleted: Updated task_delegation={task_event_id} to completed, "
                        f"created task_completed event={completed_event_id}"
                    )
                else:
                    logger.warning(
                        "TaskCompleted: No active task_delegation event found to update"
                    )

        except Exception as e:
            logger.warning(f"Warning: Could not handle TaskCompleted: {e}")
        return {"continue": True}

    elif hook_type == "TeammateIdle":
        # Teammate (subagent) became idle - track event for observability
        try:
            # Extract agent_id from hook input if available
            idle_agent_id = hook_input.get("agent_id") or detected_agent

            # Track the idle event
            result = manager.track_activity(
                session_id=active_session_id,
                tool="TeammateIdle",
                summary=f"Agent {idle_agent_id} became idle",
            )

            # Record to SQLite if available
            if db:
                record_event_to_sqlite(
                    db=db,
                    session_id=active_session_id,
                    tool_name="TeammateIdle",
                    tool_input={"agent_id": idle_agent_id},
                    tool_response={"content": "Agent became idle"},
                    is_error=False,
                    agent_id=idle_agent_id,
                    model=detected_model,
                    feature_id=result.feature_id if result else None,
                )

            # Update presence - mark as idle (not fully offline, just idle)
            presence_mgr = get_presence_manager()
            if presence_mgr:
                # For now, we'll mark as offline - can enhance PresenceManager later
                # to support "idle" state distinct from "offline"
                presence_mgr.mark_offline(idle_agent_id)

            logger.debug(f"TeammateIdle: Tracked idle event for agent {idle_agent_id}")

        except Exception as e:
            logger.warning(f"Warning: Could not track TeammateIdle: {e}")
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

        # Determine parent activity context using database-only lookup
        parent_activity_id = None

        # Check environment variable FIRST for cross-process parent linking
        # HTMLGRAPH_PARENT_EVENT_FOR_POST is set by PreToolUse for same-process parent
        # HTMLGRAPH_PARENT_EVENT is set for cross-process (Task delegation)
        # HTMLGRAPH_PARENT_QUERY_EVENT is legacy fallback
        env_parent = (
            os.environ.get("HTMLGRAPH_PARENT_EVENT_FOR_POST")
            or os.environ.get("HTMLGRAPH_PARENT_EVENT")
            or os.environ.get("HTMLGRAPH_PARENT_QUERY_EVENT")
        )
        if env_parent:
            parent_activity_id = env_parent
        # If we detected a Task delegation event via database detection (Method 3),
        # use that as the parent for all tool calls within the subagent
        elif task_event_id_from_db:
            parent_activity_id = task_event_id_from_db
        # CRITICAL FIX: Check for active task_delegation EVEN IF task_event_id_from_db not set
        # This handles Claude Code's session reuse where parent_session_id is NULL
        # When tool calls come from a subagent, they should be under the task_delegation parent,
        # NOT under UserQuery. So we MUST check for active tasks BEFORE falling back to UserQuery.
        # IMPORTANT: This must work EVEN IF db is None, so try to get it from htmlgraph_db
        else:
            # Ensure we have a db connection (may not have been passed in for parent session)
            db_to_use = db
            if not db_to_use:
                try:
                    from htmlgraph.config import get_database_path
                    from htmlgraph.db.schema import HtmlGraphDB

                    db_to_use = HtmlGraphDB(str(get_database_path()))
                except Exception:
                    db_to_use = None

            # Try to find an active task_delegation event
            if db_to_use:
                try:
                    cursor = db_to_use.connection.cursor()  # type: ignore[union-attr]
                    # First try with the current session_id
                    cursor.execute(
                        """
                        SELECT event_id, subagent_type
                        FROM agent_events
                        WHERE event_type = 'task_delegation'
                          AND status = 'started'
                          AND session_id = ?
                        ORDER BY timestamp DESC
                        LIMIT 1
                        """,
                        (active_session_id,),
                    )
                    task_row = cursor.fetchone()

                    # If not found, try stripping subagent suffixes to find the parent session
                    if not task_row:
                        known_suffixes = [
                            "-general-purpose",
                            "-Explore",
                            "-Bash",
                            "-Plan",
                            "-researcher",
                            "-debugger",
                            "-test-runner",
                        ]
                        stripped_session_id = active_session_id
                        for suffix in known_suffixes:
                            if active_session_id.endswith(suffix):
                                stripped_session_id = active_session_id[: -len(suffix)]
                                break

                        if stripped_session_id != active_session_id:
                            cursor.execute(
                                """
                                SELECT event_id, subagent_type
                                FROM agent_events
                                WHERE event_type = 'task_delegation'
                                  AND status = 'started'
                                  AND session_id = ?
                                ORDER BY timestamp DESC
                                LIMIT 1
                                """,
                                (stripped_session_id,),
                            )
                            task_row = cursor.fetchone()

                    # Last resort: find ANY active task_delegation regardless of session
                    if not task_row:
                        cursor.execute(
                            """
                            SELECT event_id, subagent_type
                            FROM agent_events
                            WHERE event_type = 'task_delegation'
                              AND status = 'started'
                            ORDER BY timestamp DESC
                            LIMIT 1
                            """,
                        )
                        task_row = cursor.fetchone()

                    if task_row:
                        parent_activity_id = task_row[0]
                        # Propagate subagent_type from the resolved task_delegation
                        # so child events inherit the correct badge/spawner metadata
                        if not subagent_type and task_row[1]:
                            subagent_type = task_row[1]
                        logger.warning(
                            f"DEBUG: Found active task_delegation={parent_activity_id} "
                            f"subagent_type={subagent_type} in parent_activity_id fallback"
                        )
                except Exception as e:
                    logger.warning(
                        f"DEBUG: Error finding task_delegation in parent_activity_id: {e}"
                    )

                # Only if no active task found, fall back to UserQuery
                if not parent_activity_id:
                    parent_activity_id = get_parent_user_query(
                        db_to_use, active_session_id
                    )

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
                # Determine subagent_type for this event:
                # - Task delegations: extract from tool_input
                # - Child events in subagent context: inherit from parent task_delegation
                event_subagent_type = None
                if tool_name == "Task":
                    event_subagent_type = tool_input_data.get(
                        "subagent_type", "general-purpose"
                    )
                elif subagent_type:
                    # Propagate subagent_type to child events so dashboard
                    # shows correct badge (e.g., "GENERAL-PURPOSE-SPAWNER")
                    event_subagent_type = subagent_type

                event_id = record_event_to_sqlite(
                    db=db,
                    session_id=active_session_id,
                    tool_name=tool_name,
                    tool_input=tool_input_data,
                    tool_response=tool_response,
                    is_error=is_error,
                    file_paths=file_paths if file_paths else None,
                    parent_event_id=parent_activity_id,  # Link to parent event
                    agent_id=detected_agent,
                    subagent_type=event_subagent_type,
                    model=detected_model,
                    feature_id=result.feature_id if result else None,
                )

                # Update presence
                presence_mgr = get_presence_manager()
                if presence_mgr and event_id:
                    presence_mgr.update_presence(
                        agent_id=detected_agent,
                        event={
                            "tool_name": tool_name,
                            "session_id": active_session_id,
                            "feature_id": result.feature_id if result else None,
                            "cost_tokens": 0,  # TODO: Extract from tool_response
                            "event_id": event_id,
                        },
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
                        ).strftime("%Y-%m-%d %H:%M:%S")
                        save_drift_queue(graph_dir, queue)
                    else:
                        nudge = f"Drift detected ({drift_score:.2f}): Activity queued for classification ({len(queue['activities'])}/{drift_settings.get('min_activities_before_classify', 3)} needed)."

                elif drift_score > warning_threshold:
                    # Moderate drift - just warn
                    nudge = f"Drift detected ({drift_score:.2f}): Activity may not align with {feature_id}. Consider refocusing or updating the feature."

        except Exception as e:
            logger.warning(f"Warning: Could not track activity: {e}")

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
