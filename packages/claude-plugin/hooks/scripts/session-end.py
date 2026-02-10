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


def _print_session_summary(
    manager: "SessionManager", session_id: str, project_dir: str
) -> None:
    """Print human-readable session summary to stdout."""
    from datetime import datetime

    db_path = Path(project_dir) / ".htmlgraph" / "htmlgraph.db"
    if not db_path.exists():
        return

    import sqlite3

    conn = sqlite3.connect(str(db_path))
    cursor = conn.cursor()

    try:
        # Get session duration
        cursor.execute(
            "SELECT created_at FROM sessions WHERE session_id = ?", (session_id,)
        )
        result = cursor.fetchone()
        duration_str = "Unknown"
        if result:
            try:
                created_at = datetime.fromisoformat(result[0])
                duration = datetime.now() - created_at
                minutes = int(duration.total_seconds() / 60)
                if minutes < 60:
                    duration_str = f"{minutes} minutes"
                else:
                    hours = minutes // 60
                    mins = minutes % 60
                    duration_str = f"{hours}h {mins}m"
            except Exception:
                pass

        # Get features worked on
        cursor.execute(
            """
            SELECT DISTINCT ae.feature_id, f.title
            FROM agent_events ae
            LEFT JOIN features f ON ae.feature_id = f.feature_id
            WHERE ae.session_id = ? AND ae.feature_id IS NOT NULL
        """,
            (session_id,),
        )
        features = cursor.fetchall()
        feature_strs = []
        for feat_id, title in features:
            if title:
                feature_strs.append(f"{title} ({feat_id})")
            else:
                feature_strs.append(feat_id)

        # Get event counts by tool
        cursor.execute(
            """
            SELECT tool_name, COUNT(*) as count
            FROM agent_events
            WHERE session_id = ?
            GROUP BY tool_name
            ORDER BY count DESC
            LIMIT 10
        """,
            (session_id,),
        )
        tool_counts = cursor.fetchall()

        # Get total events
        cursor.execute(
            "SELECT COUNT(*) FROM agent_events WHERE session_id = ?", (session_id,)
        )
        total_events = cursor.fetchone()[0]

        # Print summary
        print("\n--- Session Summary ---", file=sys.stderr)
        print(f"Duration: {duration_str}", file=sys.stderr)

        if feature_strs:
            features_line = ", ".join(feature_strs)
            print(f"Features: {features_line}", file=sys.stderr)

        if tool_counts:
            tool_parts = [f"{name}: {count}" for name, count in tool_counts[:5]]
            tools_line = ", ".join(tool_parts)
            if len(tool_counts) > 5:
                tools_line += "..."
            print(f"Events: {total_events} total ({tools_line})", file=sys.stderr)
        elif total_events > 0:
            print(f"Events: {total_events} total", file=sys.stderr)

        print("", file=sys.stderr)  # Blank line

    finally:
        conn.close()


def main() -> None:
    try:
        hook_input = json.load(sys.stdin)
    except json.JSONDecodeError:
        hook_input = {}

    external_session_id = hook_input.get("session_id") or os.environ.get(
        "CLAUDE_SESSION_ID"
    )
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

        # Link transcript to session (but don't import events yet)
        if active and external_session_id:
            try:
                from htmlgraph.transcript import TranscriptReader

                reader = TranscriptReader()
                transcript = reader.read_session(external_session_id)
                if transcript:
                    # Just link, don't import - import happens on commit/completion
                    manager.link_transcript(
                        session_id=active.id,
                        transcript_id=external_session_id,
                        transcript_path=str(transcript.path),
                        git_branch=transcript.git_branch,
                    )
            except Exception:
                pass

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

        # End session properly with handoff notes
        if active:
            try:
                # Call end_session which handles status update, HTML save, and handoff
                manager.end_session(
                    session_id=active.id,
                    handoff_notes=handoff_notes,
                    recommended_next=recommended_next,
                    blockers=blockers,
                    end_commit=end_commit,
                )

                # Print session summary
                try:
                    _print_session_summary(manager, active.id, project_dir)
                except Exception:
                    pass  # Don't let summary errors break session-end
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
