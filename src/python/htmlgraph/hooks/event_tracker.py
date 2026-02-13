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

import os
import re
import subprocess
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, cast  # noqa: F401

from htmlgraph.db.schema import HtmlGraphDB
from htmlgraph.hooks.constants import SUBAGENT_SUFFIXES
from htmlgraph.hooks.db_helpers import get_parent_user_query, resolve_project_path
from htmlgraph.hooks.drift import (
    add_to_drift_queue,
    build_classification_prompt,
    clear_drift_queue_activities,
    load_drift_config,
    save_drift_queue,
    should_trigger_classification,
)
from htmlgraph.hooks.event_recording import (
    extract_file_paths,
    format_tool_summary,
    record_delegation_to_sqlite,
    record_event_to_sqlite,
)

# Backward compatibility: Re-export functions that other modules import from event_tracker
from htmlgraph.hooks.model_detection import (
    detect_agent_from_environment,
    detect_model_from_hook_input,
    get_model_from_status_cache,  # noqa: F401
)
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


def _resolve_subagent_context(
    db: HtmlGraphDB | None, hook_session_id: str | None
) -> tuple[str | None, str | None, str | None]:
    """
    Resolve subagent context (type, parent_session, task_event_id).

    Returns:
        Tuple of (subagent_type, parent_session_id, task_event_id_from_db)
    """
    subagent_type = None
    parent_session_id = None
    task_event_id_from_db = None

    # Method 1: Check if current session is already a subagent
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
                agent_assigned = row[1] or ""
                if agent_assigned and agent_assigned.endswith("-spawner"):
                    subagent_type = agent_assigned[:-8]
                else:
                    subagent_type = "general-purpose"

                # Find the task_delegation event
                try:
                    if parent_session_id:
                        cursor.execute(
                            """
                            SELECT event_id
                            FROM agent_events
                            WHERE event_type = 'task_delegation'
                              AND subagent_type = ?
                              AND status = 'started'
                              AND session_id = ?
                            ORDER BY datetime(REPLACE(SUBSTR(timestamp, 1, 19), 'T', ' ')) DESC
                            LIMIT 1
                            """,
                            (subagent_type, parent_session_id),
                        )
                        task_row = cursor.fetchone()
                        if task_row:
                            task_event_id_from_db = task_row[0]

                    if not task_event_id_from_db:
                        cursor.execute(
                            """
                            SELECT event_id
                            FROM agent_events
                            WHERE event_type = 'task_delegation'
                              AND subagent_type = ?
                              AND status = 'started'
                            ORDER BY datetime(REPLACE(SUBSTR(timestamp, 1, 19), 'T', ' ')) DESC
                            LIMIT 1
                            """,
                            (subagent_type,),
                        )
                        task_row = cursor.fetchone()
                        if task_row:
                            task_event_id_from_db = task_row[0]
                except Exception as e:
                    logger.warning(f"DEBUG: Error finding task_delegation: {e}")

                logger.debug(
                    f"DEBUG subagent persistence: Found current session as subagent: "
                    f"type={subagent_type}, parent={parent_session_id}, task={task_event_id_from_db}"
                )
        except Exception as e:
            logger.warning(f"DEBUG: Error checking sessions table: {e}")

    # Method 2: Environment variables
    if not subagent_type:
        subagent_type = os.environ.get("HTMLGRAPH_SUBAGENT_TYPE")
        parent_session_id = os.environ.get("HTMLGRAPH_PARENT_SESSION")

    # Method 3: Database detection of active task_delegation
    if not subagent_type and db and db.connection:
        try:
            cursor = db.connection.cursor()
            cursor.execute(
                """
                SELECT event_id, subagent_type, session_id
                FROM agent_events
                WHERE event_type = 'task_delegation'
                  AND status = 'started'
                  AND tool_name = 'Task'
                ORDER BY datetime(REPLACE(SUBSTR(timestamp, 1, 19), 'T', ' ')) DESC
                LIMIT 1
                """
            )
            row = cursor.fetchone()
            if row:
                task_event_id, detected_subagent_type, _ = row
                subagent_type = detected_subagent_type or "general-purpose"
                parent_session_id = hook_session_id
                task_event_id_from_db = task_event_id
                logger.debug(
                    f"DEBUG subagent detection: Detected active task_delegation "
                    f"type={subagent_type}, parent={parent_session_id}, event={task_event_id}"
                )
        except Exception as e:
            logger.warning(f"DEBUG: Error detecting subagent from database: {e}")

    return subagent_type, parent_session_id, task_event_id_from_db


def _resolve_session(
    manager: SessionManager,
    hook_session_id: str | None,
    detected_agent: str,
    subagent_type: str | None,
    parent_session_id: str | None,
) -> Any:
    """
    Resolve or create the appropriate session (subagent or parent).

    Returns:
        Session object
    """
    if subagent_type and parent_session_id:
        # Subagent session
        subagent_session_id = f"{parent_session_id}-{subagent_type}"
        existing = manager.session_converter.load(subagent_session_id)
        if existing:
            logger.warning(
                f"Debug: Using existing subagent session: {subagent_session_id}"
            )
            return existing
        else:
            try:
                session = manager.start_session(
                    session_id=subagent_session_id,
                    agent=f"{subagent_type}-spawner",
                    is_subagent=True,
                    parent_session_id=parent_session_id,
                    title=f"{subagent_type.capitalize()} Subagent",
                )
                logger.debug(f"Debug: Created subagent session: {subagent_session_id}")
                return session
            except Exception as e:
                logger.warning(f"Warning: Could not create subagent session: {e}")
                raise

    # Normal orchestrator/parent context
    if hook_session_id:
        existing = manager.session_converter.load(hook_session_id)
        if existing:
            return existing
        else:
            try:
                return manager.start_session(
                    session_id=hook_session_id,
                    agent=detected_agent,
                    title=f"Session {datetime.now().strftime('%Y-%m-%d %H:%M')}",
                )
            except Exception:
                raise
    else:
        # Fallback: No session_id in hook_input
        active_session = manager.get_active_session()
        if not active_session:
            try:
                return manager.start_session(
                    session_id=None,
                    agent=detected_agent,
                    title=f"Session {datetime.now().strftime('%Y-%m-%d %H:%M')}",
                )
            except Exception:
                raise
        return active_session


def _handle_stop(
    manager: SessionManager,
    db: HtmlGraphDB | None,
    active_session_id: str,
    detected_agent: str,
    detected_model: str | None,
) -> dict[str, Any]:
    """Handle Stop hook event."""
    try:
        result = manager.track_activity(
            session_id=active_session_id, tool="Stop", summary="Agent stopped"
        )

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

        presence_mgr = get_presence_manager()
        if presence_mgr:
            presence_mgr.mark_offline(detected_agent)
    except Exception as e:
        logger.warning(f"Warning: Could not track stop: {e}")
    return {"continue": True}


def _handle_user_prompt_submit(
    hook_input: dict[str, Any],
    manager: SessionManager,
    db: HtmlGraphDB | None,
    active_session_id: str,
    detected_agent: str,
    detected_model: str | None,
    subagent_type: str | None,
    parent_session_id: str | None,
) -> dict[str, Any]:
    """Handle UserPromptSubmit hook event."""
    prompt = hook_input.get("prompt", "")

    print(
        f"[DEBUG UserPromptSubmit] REACHED HANDLER. active_session_id={active_session_id}, "
        f"subagent_type={subagent_type}, parent_session_id={parent_session_id}, "
        f"prompt_preview={prompt[:50]}...",
        file=sys.stderr,
    )

    # Filter out task notifications
    if prompt.strip().startswith("<task-notification>"):
        logger.debug("Skipping task notification (not a user query)")
        print("[DEBUG UserPromptSubmit] SKIPPED: Task notification", file=sys.stderr)
        return {"continue": True}

    preview = prompt[:100].replace("\n", " ")
    if len(prompt) > 100:
        preview += "..."

    # Determine correct session for UserQuery
    userquery_session_id = active_session_id
    if subagent_type and parent_session_id:
        userquery_session_id = parent_session_id
        logger.debug(
            f"UserPromptSubmit in subagent context: Recording to parent session {parent_session_id}"
        )
    else:
        # Defensive fallback: Strip known subagent suffixes
        for suffix in SUBAGENT_SUFFIXES:
            if active_session_id.endswith(suffix):
                userquery_session_id = active_session_id[: -len(suffix)]
                print(
                    f"[DEBUG UserPromptSubmit] DEFENSIVE FALLBACK: Stripped suffix '{suffix}'",
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

        if db:
            event_id = record_event_to_sqlite(
                db=db,
                session_id=userquery_session_id,
                tool_name="UserQuery",
                tool_input={"prompt": prompt},
                tool_response={"content": "Query received"},
                is_error=False,
                agent_id=detected_agent,
                model=detected_model,
                feature_id=result.feature_id if result else None,
            )

            presence_mgr = get_presence_manager()
            if presence_mgr and event_id:
                presence_mgr.update_presence(
                    agent_id=detected_agent,
                    event={
                        "tool_name": "UserQuery",
                        "session_id": userquery_session_id,
                        "feature_id": result.feature_id if result else None,
                        "event_id": event_id,
                    },
                )

    except Exception as e:
        logger.warning(f"Warning: Could not track query: {e}")
    return {"continue": True}


def _handle_post_tool_use(
    hook_input: dict[str, Any],
    manager: SessionManager,
    db: HtmlGraphDB | None,
    active_session_id: str,
    detected_agent: str,
    detected_model: str | None,
    task_event_id_from_db: str | None,
    graph_dir: Path,
    drift_config: dict[str, Any],
) -> dict[str, Any]:
    """Handle PostToolUse hook event."""
    tool_name = hook_input.get("tool_name", "unknown")
    tool_input_data = hook_input.get("tool_input", {})
    tool_response = (
        hook_input.get("tool_response", hook_input.get("tool_result", {})) or {}
    )

    # Skip tracking for some tools
    skip_tools = {"AskUserQuestion"}
    if tool_name in skip_tools:
        return {"continue": True}

    # Extract file paths and format summary
    file_paths = extract_file_paths(tool_input_data, tool_name)
    summary = format_tool_summary(tool_name, tool_input_data, tool_response)

    # Determine success
    if isinstance(tool_response, dict):  # type: ignore[arg-type]
        success_field = tool_response.get("success")
        if isinstance(success_field, bool):
            is_error = not success_field
        else:
            is_error = bool(tool_response.get("is_error", False))

        # Additional check for Bash failures
        if tool_name == "Bash" and not is_error:
            output = str(
                tool_response.get("output", "") or tool_response.get("content", "")
            )
            if re.search(
                r"Exit code [1-9]\d*|exit status [1-9]\d*", output, re.IGNORECASE
            ):
                is_error = True
    else:
        is_error = False

    # Get drift thresholds
    drift_settings = drift_config.get("drift_detection", {})
    warning_threshold = drift_settings.get("warning_threshold") or 0.7
    auto_classify_threshold = drift_settings.get("auto_classify_threshold") or 0.85

    # Determine parent activity context
    parent_activity_id = None
    env_parent = (
        os.environ.get("HTMLGRAPH_PARENT_EVENT_FOR_POST")
        or os.environ.get("HTMLGRAPH_PARENT_EVENT")
        or os.environ.get("HTMLGRAPH_PARENT_QUERY_EVENT")
    )
    if env_parent:
        parent_activity_id = env_parent
    elif task_event_id_from_db:
        parent_activity_id = task_event_id_from_db
    else:
        # Try to find active task_delegation
        db_to_use = db
        if not db_to_use:
            try:
                from htmlgraph.config import get_database_path
                from htmlgraph.db.schema import HtmlGraphDB

                db_to_use = HtmlGraphDB(str(get_database_path()))
            except Exception:
                db_to_use = None

        if db_to_use:
            try:
                cursor = db_to_use.connection.cursor()  # type: ignore[union-attr]
                cursor.execute(
                    """
                    SELECT event_id
                    FROM agent_events
                    WHERE event_type = 'task_delegation'
                      AND status = 'started'
                      AND session_id = ?
                    ORDER BY datetime(REPLACE(SUBSTR(timestamp, 1, 19), 'T', ' ')) DESC
                    LIMIT 1
                    """,
                    (active_session_id,),
                )
                task_row = cursor.fetchone()
                if task_row:
                    parent_activity_id = task_row[0]
                else:
                    # Try with parent session
                    parent_sess = active_session_id
                    for suffix in SUBAGENT_SUFFIXES:
                        if active_session_id.endswith(suffix):
                            parent_sess = active_session_id[: -len(suffix)]
                            break
                    if parent_sess != active_session_id:
                        cursor.execute(
                            """
                            SELECT event_id
                            FROM agent_events
                            WHERE event_type = 'task_delegation'
                              AND status = 'started'
                              AND session_id = ?
                            ORDER BY datetime(REPLACE(SUBSTR(timestamp, 1, 19), 'T', ' ')) DESC
                            LIMIT 1
                            """,
                            (parent_sess,),
                        )
                        task_row = cursor.fetchone()
                        if task_row:
                            parent_activity_id = task_row[0]
            except Exception as e:
                logger.warning(f"DEBUG: Error finding task_delegation: {e}")

            if not parent_activity_id:
                parent_activity_id = get_parent_user_query(db_to_use, active_session_id)

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

        # Record to SQLite
        if db:
            task_subagent_type = None
            if tool_name == "Task":
                task_subagent_type = tool_input_data.get(
                    "subagent_type", "general-purpose"
                )

            event_id = record_event_to_sqlite(
                db=db,
                session_id=active_session_id,
                tool_name=tool_name,
                tool_input=tool_input_data,
                tool_response=tool_response,
                is_error=is_error,
                file_paths=file_paths if file_paths else None,
                parent_event_id=parent_activity_id,
                agent_id=detected_agent,
                subagent_type=task_subagent_type,
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
                        "cost_tokens": 0,
                        "event_id": event_id,
                    },
                )

        # Record Task() delegation
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

        # Handle drift detection
        if result and hasattr(result, "drift_score") and not parent_activity_id:
            drift_score = result.drift_score
            feature_id = getattr(result, "feature_id", "unknown")

            if drift_score is None:
                pass
            elif drift_score >= auto_classify_threshold:
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

                if should_trigger_classification(queue, drift_config):
                    classification_prompt = build_classification_prompt(
                        queue, feature_id
                    )

                    use_headless = drift_config.get("classification", {}).get(
                        "use_headless", True
                    )
                    if use_headless:
                        try:
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
                                    "HTMLGRAPH_DISABLE_TRACKING": "1",
                                },
                            )
                            if proc_result.returncode == 0:
                                nudge = "Drift auto-classification completed. Check .htmlgraph/ for new work item."
                                clear_drift_queue_activities(graph_dir)
                            else:
                                nudge = f"""HIGH DRIFT ({drift_score:.2f}) - Headless classification failed.

{len(queue["activities"])} activities don't align with '{feature_id}'.

Please classify manually: bug, feature, spike, or chore in .htmlgraph/"""
                        except Exception as e:
                            nudge = f"Drift classification error: {e}. Please classify manually."
                    else:
                        nudge = f"""HIGH DRIFT DETECTED ({drift_score:.2f}) - Auto-classification triggered.

{len(queue["activities"])} activities don't align with '{feature_id}'.

ACTION REQUIRED: Spawn a Haiku agent to classify this work or manually create a work item."""

                    queue["last_classification"] = datetime.now(
                        timezone.utc
                    ).isoformat()
                    save_drift_queue(graph_dir, queue)
                else:
                    nudge = f"Drift detected ({drift_score:.2f}): Activity queued for classification ({len(queue['activities'])}/{drift_settings.get('min_activities_before_classify', 3)} needed)."

            elif drift_score > warning_threshold:
                nudge = f"Drift detected ({drift_score:.2f}): Activity may not align with {feature_id}."

    except Exception as e:
        logger.warning(f"Warning: Could not track activity: {e}")

    # Build response
    response: dict[str, Any] = {"continue": True}
    if nudge:
        response["hookSpecificOutput"] = {
            "hookEventName": "PostToolUse",
            "additionalContext": nudge,
        }
    return response


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

    db = None
    try:
        from htmlgraph.config import get_database_path
        from htmlgraph.db.schema import HtmlGraphDB

        db = HtmlGraphDB(str(get_database_path()))
    except Exception as e:
        logger.warning(f"Warning: Could not initialize SQLite database: {e}")

    # Detect agent and model
    detected_agent, detected_model = detect_agent_from_environment()
    model_from_input = detect_model_from_hook_input(hook_input)
    if model_from_input:
        detected_model = model_from_input

    # Resolve subagent context
    hook_session_id = hook_input.get("session_id") or hook_input.get("sessionId")
    subagent_type, parent_session_id, task_event_id_from_db = _resolve_subagent_context(
        db, hook_session_id
    )

    # Override detected agent for subagent context
    if subagent_type and parent_session_id:
        detected_agent = f"{subagent_type}-spawner"

    # Resolve or create session
    try:
        active_session = _resolve_session(
            manager, hook_session_id, detected_agent, subagent_type, parent_session_id
        )
    except Exception:
        return {"continue": True}

    active_session_id = active_session.id

    # Ensure session exists in SQLite
    if db:
        try:

            def safe_getattr(obj: Any, attr: str, default: Any) -> Any:
                try:
                    val = getattr(obj, attr, default)
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
            logger.warning(f"Debug: Could not insert session to SQLite: {e}")

    # Dispatch to appropriate handler
    if hook_type == "Stop":
        return _handle_stop(
            manager, db, active_session_id, detected_agent, detected_model
        )

    elif hook_type == "UserPromptSubmit":
        return _handle_user_prompt_submit(
            hook_input,
            manager,
            db,
            active_session_id,
            detected_agent,
            detected_model,
            subagent_type,
            parent_session_id,
        )

    elif hook_type == "PostToolUse":
        return _handle_post_tool_use(
            hook_input,
            manager,
            db,
            active_session_id,
            detected_agent,
            detected_model,
            task_event_id_from_db,
            graph_dir,
            drift_config,
        )

    # Unknown hook type
    return {"continue": True}
