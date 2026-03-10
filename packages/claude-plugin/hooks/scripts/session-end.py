#!/usr/bin/env -S uv run
# /// script
# requires-python = ">=3.10"
# dependencies = [
#   "htmlgraph",
# ]
# ///
"""
HtmlGraph Session End Hook

Records session end and generates summary.
Uses htmlgraph Python API directly for all storage operations.
"""

import json
import os
import sys

# Bootstrap Python path and setup
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from bootstrap import bootstrap_pythonpath, is_tracking_disabled, resolve_project_dir

if is_tracking_disabled():
    print(json.dumps({}))
    sys.exit(0)

project_dir_for_import = resolve_project_dir()
bootstrap_pythonpath(project_dir_for_import)

from pathlib import Path

try:
    from htmlgraph.session_manager import SessionManager
except Exception as e:
    print(
        f"Warning: HtmlGraph not available ({e}). Install with: pip install htmlgraph",
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


def main() -> None:
    try:
        hook_input = json.load(sys.stdin)
    except json.JSONDecodeError:
        hook_input = {}

    cwd = hook_input.get("cwd")
    project_dir = resolve_project_dir(cwd if cwd else None)
    graph_dir = Path(project_dir) / ".htmlgraph"

    # Session lifecycle management
    # Note: Transcript import happens on work item completion or git commit,
    # not on session end (sessions can end frequently during context switches)
    try:
        manager = SessionManager(graph_dir)
        active = manager.get_active_session()

        # Capture current git commit for end_commit tracking
        end_commit = _get_head_commit(project_dir)

        # Optional handoff context capture (non-interactive)
        handoff_notes = hook_input.get("handoff_notes") or os.environ.get(
            "HTMLGRAPH_HANDOFF_NOTES"
        )
        recommended_next = hook_input.get("recommended_next") or os.environ.get(
            "HTMLGRAPH_HANDOFF_RECOMMEND"
        )
        blockers_raw = hook_input.get("blockers") or os.environ.get(
            "HTMLGRAPH_HANDOFF_BLOCKERS"
        )
        blockers = None
        if isinstance(blockers_raw, str):
            blockers = [b.strip() for b in blockers_raw.split(",") if b.strip()]
        elif isinstance(blockers_raw, list):
            blockers = [str(b).strip() for b in blockers_raw if str(b).strip()]

        # Update session with end_commit and handoff notes
        if active:
            try:
                # Set end_commit if available
                if end_commit and not active.end_commit:
                    active.end_commit = end_commit
                    manager.session_converter.save(active)

                # Set handoff notes if provided
                if handoff_notes or recommended_next or blockers:
                    manager.set_session_handoff(
                        session_id=active.id,
                        handoff_notes=handoff_notes,
                        recommended_next=recommended_next,
                        blockers=blockers,
                    )
            except Exception:
                pass
        elif sys.stderr.isatty():
            print(
                "HtmlGraph: add handoff notes with 'uv run htmlgraph session handoff --notes ...'",
                file=sys.stderr,
            )
    except Exception as e:
        print(f"Warning: Could not end session: {e}", file=sys.stderr)

    # Output empty response (session end doesn't add context)
    print(json.dumps({"continue": True}))


if __name__ == "__main__":
    main()
