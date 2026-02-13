from __future__ import annotations

"""
HtmlGraph Session Handler Module

Centralizes session lifecycle and tracking logic for hooks.
Provides unified functions for session initialization, tracking, and cleanup.

This module extracts common patterns from session-start.py and session-end.py
hooks to provide reusable session management operations.

Public API:
    init_or_get_session(context: HookContext) -> Session | None
        Get or create session from SessionManager

    handle_session_start(context: HookContext, session: Session | None) -> dict
        Initialize HtmlGraph tracking and build feature context

    handle_session_end(context: HookContext) -> dict
        Close session gracefully and record final metrics

    record_user_query_event(context: HookContext, prompt: str) -> str | None
        Create UserQuery event in database

    check_version_status() -> dict | None
        Check if HtmlGraph has updates available
"""


import json
import logging
import os
import subprocess
from datetime import datetime, timezone
from pathlib import Path
from typing import TYPE_CHECKING, Any

if TYPE_CHECKING:
    from htmlgraph.hooks.context import HookContext

logger = logging.getLogger(__name__)


def init_or_get_session(context: HookContext) -> Any | None:
    """
    Get or create session from SessionManager.

    Attempts to get an active session for the current agent.
    If none exists, creates a new one with automatic initialization.

    Args:
        context: HookContext with project and graph directory information

    Returns:
        Session object if successful, None if SessionManager unavailable or error occurs

    Note:
        - Handles graceful degradation if SessionManager cannot be imported
        - Caches session in context for reuse
        - Logs session ID for debugging
    """
    try:
        manager = context.session_manager
        agent = context.agent_id

        # Try to get existing session for this agent
        active = manager.get_active_session_for_agent(agent=agent)
        if not active:
            # Create new session with commit info
            try:
                head_commit = _get_head_commit(context.project_dir)
            except Exception:
                head_commit = None

            active = manager.start_session(
                session_id=None,
                agent=agent,
                start_commit=head_commit,
                title=f"Session {datetime.now().strftime('%Y-%m-%d %H:%M')}",
            )

        context.log("info", f"Session initialized: {active.id if active else 'None'}")
        return active

    except ImportError as e:
        context.log("error", f"SessionManager not available: {e}")
        return None
    except Exception as e:
        context.log("error", f"Failed to initialize session: {e}")
        return None


def handle_session_start(context: HookContext, session: Any | None) -> dict[str, Any]:
    """
    Initialize HtmlGraph tracking for the session.

    Performs session startup operations:
    - Initializes database entry if needed
    - Loads active features and spikes from project
    - Builds feature context string
    - Records session start event
    - Creates conversation-init spike if new conversation
    - Injects concurrent session and recent work context

    Args:
        context: HookContext with project and graph directory information
        session: Session object from SessionManager (optional)

    Returns:
        dict with:
            {
                "continue": True,
                "hookSpecificOutput": {
                    "sessionFeatureContext": str with feature context,
                    "sessionContext": optional concurrent/recent work context,
                    "versionInfo": optional version check result
                }
            }
    """
    output: dict[str, Any] = {
        "continue": True,
        "hookSpecificOutput": {
            "sessionFeatureContext": "",
            "sessionContext": "",
            "versionInfo": None,
        },
    }

    if not session:
        return output

    # Ensure session exists in database
    try:
        db = context.database
        cursor = db.connection.cursor()
        cursor.execute(
            "SELECT COUNT(*) FROM sessions WHERE session_id = ?",
            (session.id,),
        )
        session_exists = cursor.fetchone()[0] > 0

        if not session_exists:
            cursor.execute(
                """
                INSERT INTO sessions (session_id, agent_assigned, created_at, status, parent_session_id)
                VALUES (?, ?, ?, 'active', ?)
                """,
                (
                    session.id,
                    context.agent_id,
                    datetime.now(timezone.utc).isoformat(),
                    session.parent_session,
                ),
            )
            db.connection.commit()
            context.log("info", f"Created database session: {session.id}")
    except ImportError:
        context.log("debug", "Database not available, skipping session entry")
    except Exception as e:
        context.log("warning", f"Could not create database session: {e}")

    # Track session start activity
    try:
        external_session_id = context.hook_input.get("session_id", "unknown")
        context.session_manager.track_activity(
            session_id=session.id,
            tool="SessionStart",
            summary=f"Session started: {external_session_id}",
            payload={
                "agent": context.agent_id,
                "external_session_id": external_session_id,
            },
        )
    except Exception as e:
        context.log("warning", f"Could not track session start activity: {e}")

    # Load features and build context
    try:
        features = _load_features(context.graph_dir)
        active_features = [f for f in features if f.get("status") == "in-progress"]

        if active_features:
            feature_list = "\n".join(
                [f"- **{f['id']}**: {f['title']}" for f in active_features[:3]]
            )
            context_str = f"""## Active Features

{feature_list}

Activity will be attributed to these features based on file patterns and keywords.

**To view all work and progress:** `htmlgraph snapshot --summary`"""
            output["hookSpecificOutput"]["sessionFeatureContext"] = context_str
            context.log("info", f"Loaded {len(active_features)} active features")

    except Exception as e:
        context.log("warning", f"Could not load features: {e}")

    # Check version status
    try:
        version_info = check_version_status()
        if version_info and version_info.get("is_outdated"):
            output["hookSpecificOutput"]["versionInfo"] = version_info
            context.log(
                "info",
                f"Update available: {version_info.get('installed')} → {version_info.get('latest')}",
            )
    except Exception as e:
        context.log("debug", f"Could not check version: {e}")

    # Build concurrent session context
    try:
        from htmlgraph.hooks.concurrent_sessions import (
            format_concurrent_sessions_markdown,
            format_recent_work_markdown,
            get_concurrent_sessions,
            get_recent_completed_sessions,
        )

        db = context.database

        # Get concurrent sessions (other active windows)
        concurrent = get_concurrent_sessions(db, context.session_id, minutes=30)
        concurrent_md = format_concurrent_sessions_markdown(concurrent)

        # Get recent completed work
        recent = get_recent_completed_sessions(db, hours=24, limit=5)
        recent_md = format_recent_work_markdown(recent)

        # Build session context
        session_context = ""
        if concurrent_md:
            session_context += concurrent_md + "\n"
        if recent_md:
            session_context += recent_md + "\n"

        if session_context:
            output["hookSpecificOutput"]["sessionContext"] = session_context.strip()
            context.log(
                "info",
                f"Injected context: {len(concurrent)} concurrent, {len(recent)} recent",
            )

    except ImportError:
        context.log("debug", "Concurrent session module not available")
    except Exception as e:
        context.log("warning", f"Failed to get concurrent session context: {e}")

    # Update session with user's current query (if available from hook input)
    try:
        user_query = context.hook_input.get("prompt", "")
        if user_query and session:
            context.session_manager.track_activity(
                session_id=session.id,
                tool="UserQuery",
                summary=user_query[:100],
                payload={"query_length": len(user_query)},
            )
    except Exception as e:
        context.log("warning", f"Failed to update session activity: {e}")

    return output


def handle_session_end(context: HookContext) -> dict[str, Any]:
    """
    Close session gracefully and record final metrics.

    Performs session end operations:
    - Captures handoff notes if provided
    - Links transcript if available
    - Records session end event
    - Cleans up temporary state files

    Args:
        context: HookContext with project and graph directory information

    Returns:
        dict with:
            {
                "continue": True,
                "status": "success" | "partial" | "error"
            }
    """
    output: dict[str, Any] = {
        "continue": True,
        "status": "success",
    }

    try:
        session = context.session_manager.get_active_session()
        if not session:
            context.log("debug", "No active session to close")
        else:
            # Capture handoff context if provided
            handoff_notes = context.hook_input.get("handoff_notes") or os.environ.get(
                "HTMLGRAPH_HANDOFF_NOTES"
            )
            recommended_next = context.hook_input.get(
                "recommended_next"
            ) or os.environ.get("HTMLGRAPH_HANDOFF_RECOMMEND")
            blockers_raw = context.hook_input.get("blockers") or os.environ.get(
                "HTMLGRAPH_HANDOFF_BLOCKERS"
            )

            blockers = None
            if isinstance(blockers_raw, str):
                blockers = [b.strip() for b in blockers_raw.split(",") if b.strip()]
            elif isinstance(blockers_raw, list):
                blockers = [str(b).strip() for b in blockers_raw if str(b).strip()]

            if handoff_notes or recommended_next or blockers:
                try:
                    context.session_manager.set_session_handoff(
                        session_id=session.id,
                        handoff_notes=handoff_notes,
                        recommended_next=recommended_next,
                        blockers=blockers,
                    )
                    context.log("info", "Session handoff recorded")
                except Exception as e:
                    context.log("warning", f"Could not set handoff: {e}")
                    output["status"] = "partial"

            # Link transcript if external session ID provided
            external_session_id = context.hook_input.get(
                "session_id"
            ) or os.environ.get("CLAUDE_SESSION_ID")
            if external_session_id:
                try:
                    from htmlgraph.transcript import TranscriptReader

                    reader = TranscriptReader()
                    transcript = reader.read_session(external_session_id)
                    if transcript:
                        context.session_manager.link_transcript(
                            session_id=session.id,
                            transcript_id=external_session_id,
                            transcript_path=str(transcript.path),
                            git_branch=transcript.git_branch
                            if hasattr(transcript, "git_branch")
                            else None,
                        )
                        context.log("info", "Transcript linked to session")
                except ImportError:
                    context.log("debug", "Transcript reader not available")
                except Exception as e:
                    context.log("warning", f"Could not link transcript: {e}")
                    output["status"] = "partial"

            # Record session end activity
            try:
                context.session_manager.track_activity(
                    session_id=session.id,
                    tool="SessionEnd",
                    summary="Session ended",
                )
            except Exception as e:
                context.log("warning", f"Could not track session end: {e}")
                output["status"] = "partial"

            context.log("info", f"Session closed: {session.id}")

    except ImportError:
        context.log("error", "SessionManager not available")
        output["status"] = "error"
    except Exception as e:
        context.log("error", f"Failed to close session: {e}")
        output["status"] = "error"

    # Always cleanup temp files
    try:
        _cleanup_temp_files(context.graph_dir)
    except Exception as e:
        context.log("warning", f"Could not cleanup temp files: {e}")

    return output


def record_user_query_event(context: HookContext, prompt: str) -> str | None:
    """
    Create UserQuery event in database.

    Records a user query prompt as an event in the database for later
    reference by tool calls in the same conversation turn.

    Args:
        context: HookContext with project and graph directory information
        prompt: The user query prompt text

    Returns:
        event_id if successful, None otherwise

    Note:
        - Event ID is stored for parent-child linking of subsequent tool calls
        - Events expire after 10 minutes (conversation turn boundary)
        - Safe to call even if database unavailable (graceful degradation)
    """
    try:
        from htmlgraph.ids import generate_id

        db = context.database
        event_id = generate_id("event")

        # Preview for logging
        preview = prompt[:100].replace("\n", " ")
        if len(prompt) > 100:
            preview += "..."

        # Insert UserQuery event
        success = db.insert_event(
            event_id=event_id,
            agent_id=context.agent_id,
            event_type="user_query",
            session_id=context.session_id,
            tool_name="UserQuery",
            input_summary=preview,
            output_summary="Query recorded",
            context={"full_prompt_length": len(prompt)},
        )

        if success:
            context.log("info", f"Recorded UserQuery event: {event_id}")
            return event_id
        else:
            context.log("warning", "Failed to insert UserQuery event")
            return None

    except ImportError:
        context.log("debug", "Database not available for UserQuery event")
        return None
    except Exception as e:
        context.log("error", f"Failed to record UserQuery event: {e}")
        return None


def check_version_status() -> dict | None:
    """
    Check if HtmlGraph has updates available.

    Compares installed version with latest version on PyPI.
    Attempts multiple methods to get version information:
    1. import htmlgraph and check __version__
    2. pip show htmlgraph
    3. PyPI JSON API (requires network)

    Returns:
        dict with version info if outdated:
            {
                "installed": "0.9.0",
                "latest": "0.9.1",
                "is_outdated": True
            }
        None if versions match or cannot be determined

    Note:
        - Never blocks on network errors (5 second timeout)
        - Gracefully degrades if methods unavailable
        - Safe for use in hooks (catches all exceptions)
    """
    try:
        installed_version = _get_installed_version()
        latest_version = _get_latest_pypi_version()

        if not (installed_version and latest_version):
            return None

        if installed_version == latest_version:
            return None

        # Compare versions
        is_outdated = _compare_versions(installed_version, latest_version)
        if is_outdated:
            return {
                "installed": installed_version,
                "latest": latest_version,
                "is_outdated": True,
            }

        return None

    except Exception:
        return None


# ============================================================================
# Private Helper Functions
# ============================================================================


def _get_head_commit(project_dir: str) -> str | None:
    """Get current HEAD commit hash (short form)."""
    try:
        result = subprocess.run(
            ["git", "rev-parse", "--short", "HEAD"],
            capture_output=True,
            text=True,
            cwd=project_dir,
            timeout=5,
        )
        if result.returncode == 0:
            return result.stdout.strip()
    except Exception:
        pass
    return None


def _load_features(graph_dir: Path) -> list[dict]:
    """Load all features as dicts."""
    try:
        from htmlgraph.converter import node_to_dict  # type: ignore[import]
        from htmlgraph.graph import HtmlGraph

        features_dir = graph_dir / "features"
        if not features_dir.exists():
            return []

        graph = HtmlGraph(features_dir, auto_load=True)
        return [node_to_dict(node) for node in graph.nodes.values()]

    except Exception:
        return []


def _get_installed_version() -> str | None:
    """Get installed htmlgraph version."""
    # Method 1: Import and check __version__
    try:
        import htmlgraph

        return htmlgraph.__version__
    except Exception:
        pass

    # Method 2: pip show
    try:
        result = subprocess.run(
            ["pip", "show", "htmlgraph"],
            capture_output=True,
            text=True,
            timeout=10,
        )
        if result.returncode == 0:
            for line in result.stdout.splitlines():
                if line.startswith("Version:"):
                    return line.split(":", 1)[1].strip()
    except Exception:
        pass

    return None


def _get_latest_pypi_version() -> str | None:
    """Get latest htmlgraph version from PyPI."""
    try:
        import urllib.request

        req = urllib.request.Request(
            "https://pypi.org/pypi/htmlgraph/json",
            headers={
                "Accept": "application/json",
                "User-Agent": "htmlgraph-version-check",
            },
        )
        with urllib.request.urlopen(req, timeout=5) as response:
            data = json.loads(response.read().decode())
            version: str | None = data.get("info", {}).get("version")
            return version
    except Exception:
        return None


def _compare_versions(installed: str, latest: str) -> bool:
    """
    Check if installed version is older than latest.

    Args:
        installed: Installed version string
        latest: Latest version string

    Returns:
        True if installed < latest, False otherwise

    Note:
        - Uses semantic versioning comparison
        - Falls back to string comparison for non-semver versions
    """
    try:
        # Try semantic version comparison
        installed_parts = [int(x) for x in installed.split(".")]
        latest_parts = [int(x) for x in latest.split(".")]
        return installed_parts < latest_parts
    except (ValueError, IndexError):
        # Fallback to string comparison
        return installed != latest


def _cleanup_temp_files(graph_dir: Path) -> None:
    """
    Clean up temporary state files after session end.

    Removes session-scoped temporary files that are no longer needed.
    Safe to call even if files don't exist (idempotent).

    Args:
        graph_dir: Path to .htmlgraph directory
    """
    temp_patterns = [
        "parent-activity.json",
        "user-query-event-*.json",
    ]

    for pattern in temp_patterns:
        if "*" in pattern:
            # Handle glob patterns
            import glob

            for path in glob.glob(str(graph_dir / pattern)):
                try:
                    Path(path).unlink()
                    logger.debug(f"Cleaned up: {path}")
                except Exception as e:
                    logger.debug(f"Could not clean up {path}: {e}")
        else:
            # Handle single file
            file_path: Path = graph_dir / pattern
            try:
                if file_path.exists():
                    file_path.unlink()
                    logger.debug(f"Cleaned up: {file_path}")
            except Exception as e:
                logger.debug(f"Could not clean up {file_path}: {e}")


__all__ = [
    "init_or_get_session",
    "handle_session_start",
    "handle_session_end",
    "record_user_query_event",
    "check_version_status",
]


def main() -> None:
    """Hook entry point for SessionEnd hook."""
    import json
    import os
    import sys

    # Check if tracking is disabled
    if os.environ.get("HTMLGRAPH_DISABLE_TRACKING") == "1":
        print(json.dumps({"continue": True}))
        sys.exit(0)

    try:
        hook_input = json.load(sys.stdin)
    except json.JSONDecodeError:
        hook_input = {}

    # Create context from hook input
    from htmlgraph.hooks.context import HookContext

    context = HookContext.from_input(hook_input)

    # Handle session end
    response = handle_session_end(context)

    # Output JSON response
    print(json.dumps(response))
