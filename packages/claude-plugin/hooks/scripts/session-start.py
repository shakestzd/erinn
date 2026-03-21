#!/usr/bin/env -S uv run
# /// script
# requires-python = ">=3.10"
# dependencies = [
#   "htmlgraph",
# ]
# ///
"""
HtmlGraph Session Start Hook (Thin Wrapper)

Records session start and provides feature context to Claude.
All business logic lives in the SDK (htmlgraph.session_context).

Architecture:
- SessionContextBuilder (SDK) = All context computation
- SessionManager (SDK) = Session lifecycle
- This hook = Thin wrapper orchestrating SDK calls
"""

import json
import logging
import os
import sys
import tempfile
import time
from datetime import datetime
from pathlib import Path

# Bootstrap Python path and setup
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from bootstrap import resolve_project_dir

# Setup logging
logging.basicConfig(
    level=logging.INFO,
    format="%(levelname)s: %(message)s",
    stream=sys.stderr,
)
logger = logging.getLogger(__name__)

if os.environ.get("HTMLGRAPH_DISABLE_TRACKING") == "1":
    print(json.dumps({}))
    sys.exit(0)


try:
    from htmlgraph import generate_id
    from htmlgraph.session_context import (
        GitHooksInstaller,
        SessionContextBuilder,
    )
    from htmlgraph.session_manager import SessionManager
except Exception as e:
    print(
        f"Warning: HtmlGraph not available ({e}). Install with: uv pip install htmlgraph",
        file=sys.stderr,
    )
    print(json.dumps({}))
    sys.exit(0)


def _is_htmlgraph_launched(graph_dir: str) -> bool:
    """Check if Claude was launched via htmlgraph CLI.

    Primary signal: HTMLGRAPH_LAUNCHED env var set by ClaudeLauncher before
    calling subprocess.run(["claude", ...]).  env vars are inherited by child
    processes, so this is reliable even though the launcher process may have
    already exited (subprocess.run waits; exec would replace it, but we use
    subprocess.run so the var is in the env block passed to the child).

    Fallback: .htmlgraph/.launch-mode marker file, checked by timestamp only
    (within 5 minutes).  The old PID liveness check is removed because
    subprocess.run() keeps the launcher alive while Claude runs, but exec()
    (if ever used) would kill the PID immediately.

    Args:
        graph_dir: Path to the .htmlgraph/ directory

    Returns:
        True if Claude was launched via htmlgraph CLI; False otherwise
    """
    # Primary: env var set by launcher before subprocess.run(["claude", ...])
    if os.environ.get("HTMLGRAPH_LAUNCHED") == "1":
        return True

    # Fallback: marker file with recent timestamp (within 5 minutes)
    marker_path = os.path.join(graph_dir, ".launch-mode")
    if not os.path.exists(marker_path):
        return False
    try:
        with open(marker_path) as f:
            marker = json.load(f)
        ts = marker.get("timestamp", 0)
        if time.time() - ts < 300:
            return True
    except Exception:
        pass
    return False


def claim_traceparent() -> dict | None:
    """Claim the most recent unclaimed traceparent from the queue.

    Written by the parent agent's PreToolUse hook when it calls ``Task()`` or
    ``Agent()``.  This subagent's session-start hook claims it so we know
    which parent session and task delegation spawned us.

    Entries older than 30 seconds are ignored (subagent should have started
    by then).  Files older than 5 minutes are cleaned up.

    Returns:
        Dict with ``trace_id`` and ``parent_span_id`` keys, or ``None`` if
        no unclaimed entry is available.
    """
    try:
        queue_dir = Path(tempfile.gettempdir()) / "htmlgraph-traceparent"
        if not queue_dir.exists():
            return None

        now = time.time()
        candidates: list[tuple[Path, dict]] = []

        for f in sorted(queue_dir.glob("tp-*.json")):
            try:
                data = json.loads(f.read_text())
                age = now - data.get("timestamp", 0)
                if not data.get("claimed") and age < 30:
                    candidates.append((f, data))
                elif age > 300:
                    # Clean up entries older than 5 minutes
                    f.unlink(missing_ok=True)
            except Exception:
                continue

        if not candidates:
            return None

        # Claim the most recent unclaimed entry (last in sorted order)
        queue_file, entry = candidates[-1]
        entry["claimed"] = True
        queue_file.write_text(json.dumps(entry))
        logger.debug(
            f"Claimed traceparent: {queue_file.name} "
            f"(trace_id={entry.get('trace_id')}, "
            f"parent_span_id={entry.get('parent_span_id')})"
        )
        return entry
    except Exception as e:
        logger.debug(f"Could not claim traceparent: {e}")
        return None


def _get_head_commit(project_dir: str) -> str | None:
    """Get current HEAD commit hash (short form)."""
    import subprocess

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


def _manage_conversation_spike(
    manager: SessionManager,
    active: object,
    external_session_id: str,
    graph_dir: Path,
) -> None:
    """
    Handle conversation-level auto-spike management.

    Each new conversation gets a new auto-spike; previous auto-spikes are closed.
    """
    try:
        last_conversation_id = getattr(active, "last_conversation_id", None)
        is_new_conversation = last_conversation_id != external_session_id

        # Record external session breadcrumb
        try:
            manager.track_activity(
                session_id=active.id,  # type: ignore[union-attr]
                tool="ClaudeSessionStart",
                summary=f"Claude session started: {external_session_id}",
                payload={
                    "claude_session_id": external_session_id,
                    "is_new_conversation": is_new_conversation,
                },
            )
        except Exception:
            pass

        if not is_new_conversation:
            return

        # Close open auto-spikes from previous conversation
        from htmlgraph.converter import NodeConverter  # type: ignore[import]

        spike_converter = NodeConverter(graph_dir / "spikes")
        all_spikes = spike_converter.load_all()

        for spike in all_spikes:
            if (
                spike.type == "spike"
                and spike.auto_generated
                and spike.spike_subtype
                in ("session-init", "transition", "conversation-init")
                and spike.status == "in-progress"
            ):
                spike.status = "done"
                spike.updated = datetime.now()
                spike_converter.save(spike)

        # Create new conversation-init spike
        spike_id = (
            f"spk-{external_session_id[:8]}"
            if external_session_id != "unknown"
            else generate_id("spike", "conversation")
        )

        from htmlgraph.models import Node  # type: ignore[import]

        conversation_spike = Node(
            id=spike_id,
            title=f"Conversation {datetime.now().strftime('%H:%M')}",
            type="spike",
            status="in-progress",
            priority="low",
            spike_subtype="conversation-init",
            auto_generated=True,
            session_id=active.id,  # type: ignore[union-attr]
            model_name=active.agent,  # type: ignore[union-attr]
            content=(
                "Auto-generated spike for conversation startup.\n\n"
                "Captures:\n- Context review\n- Planning\n- Exploration\n\n"
                "Auto-completes when feature is started or next conversation begins."
            ),
        )
        spike_converter.save(conversation_spike)

        # Update session metadata
        active.last_conversation_id = external_session_id  # type: ignore[union-attr]
        if conversation_spike.id not in active.worked_on:  # type: ignore[union-attr]
            active.worked_on.append(conversation_spike.id)  # type: ignore[union-attr]
        manager.session_converter.save(active)  # type: ignore[arg-type]

    except Exception as e:
        print(
            f"Warning: Could not manage conversation spike: {e}",
            file=sys.stderr,
        )


def _setup_env_vars(
    active: object,
    external_session_id: str,
    env_file: str | None,
    project_dir: str | None = None,
) -> None:
    """
    Set environment variables for parent session context propagation.

    CRITICAL: Use external_session_id (from Claude Code) for cross-hook consistency.
    This ensures UserPromptSubmit and PreToolUse hooks use the same session_id.

    Also exports CLAUDE_PROJECT_DIR so subagents spawned via Task() inherit the
    correct project root and the SDK discovers the right .htmlgraph/ directory.
    """
    session_id = (
        external_session_id  # Use Claude Code's session ID, not HtmlGraph's internal ID
    )
    if not session_id or session_id == "unknown":
        return

    os.environ["HTMLGRAPH_SESSION_ID"] = session_id
    os.environ["HTMLGRAPH_PARENT_SESSION"] = session_id
    os.environ["HTMLGRAPH_PARENT_AGENT"] = "claude-code"
    os.environ["HTMLGRAPH_NESTING_DEPTH"] = "0"
    if project_dir:
        os.environ["CLAUDE_PROJECT_DIR"] = project_dir

    if env_file:
        try:
            with open(env_file, "a") as f:
                f.write(f"export HTMLGRAPH_SESSION_ID={session_id}\n")
                f.write(f"export HTMLGRAPH_PARENT_SESSION={session_id}\n")
                f.write("export HTMLGRAPH_PARENT_AGENT=claude-code\n")
                f.write("export HTMLGRAPH_NESTING_DEPTH=0\n")
                if project_dir:
                    f.write(f"export CLAUDE_PROJECT_DIR={project_dir}\n")
            logger.info(f"Environment variables written to {env_file}")
        except Exception as e:
            logger.warning(f"Could not write to CLAUDE_ENV_FILE: {e}")
    else:
        logger.warning("CLAUDE_ENV_FILE not set.")


def main() -> None:
    try:
        hook_input = json.load(sys.stdin)
    except json.JSONDecodeError:
        hook_input = {}

    # Resolve paths
    external_session_id = hook_input.get("session_id") or os.environ.get(
        "CLAUDE_SESSION_ID", "unknown"
    )
    cwd = hook_input.get("cwd")
    project_dir = resolve_project_dir(cwd if cwd else None)
    graph_dir = Path(project_dir) / ".htmlgraph"

    # Claim W3C traceparent from queue (written by parent's PreToolUse hook).
    # If found, export parent linkage env vars so all subsequent hooks in
    # this subagent session can attribute events to the correct parent.
    traceparent = claim_traceparent()
    if traceparent:
        parent_trace_id = traceparent.get("trace_id", "")
        parent_span_id = traceparent.get("parent_span_id", "")
        if parent_trace_id:
            os.environ["HTMLGRAPH_PARENT_SESSION"] = parent_trace_id
        if parent_span_id:
            os.environ["HTMLGRAPH_PARENT_EVENT"] = parent_span_id
        logger.info(
            f"Claimed traceparent: parent_session={parent_trace_id}, "
            f"parent_event={parent_span_id}"
        )

    # Install pre-commit hooks (silent, non-blocking)
    try:
        GitHooksInstaller.install(project_dir)
    except Exception:
        pass

    # Detect whether Claude was launched via htmlgraph CLI (read once, used twice below)
    launched = _is_htmlgraph_launched(str(graph_dir))

    # Ensure a single stable HtmlGraph session exists for this agent
    active = None
    try:
        manager = SessionManager(graph_dir)
        active = manager.get_active_session_for_agent(agent="claude-code")
        if not active:
            active = manager.start_session(
                session_id=None,
                agent="claude-code",
                start_commit=_get_head_commit(project_dir),
                title=f"Session {datetime.now().strftime('%Y-%m-%d %H:%M')}",
            )

        # Set environment variables for parent session context
        env_file = os.environ.get("CLAUDE_ENV_FILE")
        _setup_env_vars(active, external_session_id, env_file, project_dir)

        # Propagate htmlgraph launch mode to downstream hooks via CLAUDE_ENV_FILE.
        # The launcher already sets HTMLGRAPH_LAUNCHED=1 in the process env before
        # calling subprocess.run(["claude", ...]), so this write is a belt-and-
        # suspenders backup for hooks that source env vars from the file rather
        # than inheriting them from the process environment.
        if launched and env_file:
            try:
                with open(env_file, "a") as f:
                    f.write("export HTMLGRAPH_LAUNCHED=1\n")
            except Exception as e:
                logger.warning(f"Could not write HTMLGRAPH_LAUNCHED to env file: {e}")

        # Manage conversation-level auto-spikes
        _manage_conversation_spike(manager, active, external_session_id, graph_dir)

    except Exception as e:
        print(f"Warning: Could not start session: {e}", file=sys.stderr)

    # Build complete session context via SDK
    session_id = (
        getattr(active, "id", external_session_id) if active else external_session_id
    )
    builder = SessionContextBuilder(graph_dir, project_dir)
    context = builder.build(
        session_id=session_id,
        compute_async=True,
        launched_by_htmlgraph=launched,
    )

    # Build status summary for terminal
    try:
        features, stats = builder.get_feature_summary()
        status_summary = builder.build_status_summary(features, stats)
        print(f"\n{status_summary}\n", file=sys.stderr)
    except Exception:
        pass

    # Output response
    print(
        json.dumps(
            {
                "continue": True,
                "hookSpecificOutput": {
                    "hookEventName": "SessionStart",
                    "additionalContext": context,
                },
            }
        )
    )


if __name__ == "__main__":
    main()
