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


def _setup_env_vars(active: object, env_file: str | None, hook_input: dict) -> None:
    """Set environment variables for parent session context propagation."""
    session_id = getattr(active, "id", None)
    if not session_id:
        return

    os.environ["HTMLGRAPH_PARENT_SESSION"] = session_id
    os.environ["HTMLGRAPH_PARENT_AGENT"] = "claude-code"
    os.environ["HTMLGRAPH_NESTING_DEPTH"] = "0"

    # Extract model and Claude session ID from hook_input
    model = (
        hook_input.get("model")
        or os.environ.get("CLAUDE_MODEL")
        or os.environ.get("ANTHROPIC_MODEL")
    )
    claude_session_id = hook_input.get("session_id") or hook_input.get("sessionId")

    if env_file:
        try:
            with open(env_file, "a") as f:
                f.write(f"export HTMLGRAPH_SESSION_ID={session_id}\n")
                f.write(f"export HTMLGRAPH_PARENT_SESSION={session_id}\n")
                f.write("export HTMLGRAPH_PARENT_AGENT=claude-code\n")
                f.write("export HTMLGRAPH_NESTING_DEPTH=0\n")

                # Write model if available
                if model:
                    f.write(f"export HTMLGRAPH_MODEL={model}\n")

                # Write Claude session ID for correlation
                if claude_session_id:
                    f.write(f"export CLAUDE_SESSION_ID={claude_session_id}\n")

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

    # Install pre-commit hooks (silent, non-blocking)
    try:
        GitHooksInstaller.install(project_dir)
    except Exception:
        pass

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
        _setup_env_vars(active, os.environ.get("CLAUDE_ENV_FILE"), hook_input)

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
