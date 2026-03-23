#!/usr/bin/env -S uv run
# /// script
# requires-python = ">=3.10"
# dependencies = [
#   "htmlgraph>=0.34.15",
# ]
# ///
"""
PreToolUse: Attribution Check — Enforce work item attribution on Agent delegation.

Fires before Agent tool calls (the strongest signal that real work is about to
happen). Delegating to a subagent without an active work item means all of the
subagent's tool calls will be orphaned in the dashboard.

Detection strategy (ordered by speed):
1. SQLite query on `features` table for any in-progress row (< 5ms).
2. If DB is unavailable, fall back to SDK `get_active_work_item()` which
   scans HTML files (< 50ms but heavier).

Decision is always "allow" — this hook warns, never blocks.
"""

import json
import os
import sqlite3
import sys

try:
    from htmlgraph.hooks.version_check import check_hook_version

    check_hook_version("0.34.15")
except Exception:
    pass


def _resolve_project_dir() -> str:
    """Resolve the project directory from env or cwd."""
    env_dir = os.environ.get("CLAUDE_PROJECT_DIR")
    if env_dir:
        return env_dir
    return os.getcwd()


def _db_path(project_dir: str) -> str:
    """Return the path to the htmlgraph SQLite database."""
    return os.path.join(project_dir, ".htmlgraph", "htmlgraph.db")


def _check_active_work_item_db(db_file: str) -> dict | None:
    """Query SQLite for any in-progress work item. Returns dict or None.

    Uses a single fast query against the features table (which stores all
    work item types: features, bugs, spikes, chores, epics).
    Filters out auto-generated spikes (session-init, conversation-init)
    since those are transient tracking items, not real work.
    """
    try:
        conn = sqlite3.connect(db_file, timeout=2)
        conn.row_factory = sqlite3.Row
        try:
            row = conn.execute(
                """
                SELECT id, title, type FROM features
                WHERE status = 'in-progress'
                ORDER BY updated_at DESC
                LIMIT 5
                """
            ).fetchall()

            if not row:
                return None

            # Filter out auto-generated spikes (session-init, conversation-init)
            for r in row:
                item_id = r["id"]
                item_type = r["type"]
                # Auto-generated spikes have IDs starting with 'spk-' and are
                # typically session-init items. Real spikes created by users
                # also start with 'spk-' but have meaningful titles.
                # The most reliable signal: check the HTML file for auto_generated flag.
                # But for speed, we accept any in-progress item that is a feature or bug.
                if item_type in ("feature", "bug", "chore", "epic"):
                    return {"id": item_id, "title": r["title"], "type": item_type}

            # If only spikes remain, accept the first one — better than no attribution
            first = row[0]
            return {"id": first["id"], "title": first["title"], "type": first["type"]}
        finally:
            conn.close()
    except (sqlite3.Error, OSError):
        return None


def _check_active_work_item_sdk() -> dict | None:
    """Fallback: use SDK to scan HTML files for active work item."""
    try:
        from htmlgraph import SDK

        sdk = SDK()
        active = sdk.get_active_work_item()
        if active:
            return {
                "id": active.get("id", ""),
                "title": active.get("title", ""),
                "type": active.get("type", ""),
            }
    except Exception:
        pass
    return None


# --- Warning messages ---

_NO_WORK_ITEM_WARNING = (
    "STOP: AGENT DELEGATION WITHOUT ACTIVE WORK ITEM\n\n"
    "You are about to delegate to a subagent, but no work item is active. "
    "All subagent tool calls will be attributed to NULL -- orphaned in the dashboard.\n\n"
    "REQUIRED before delegating:\n"
    "  1. Find or create the right work item:\n"
    "     sdk.bugs.create('title').save() -> sdk.bugs.start('id')\n"
    "     sdk.features.create('title').set_track('track_id').save() -> sdk.features.start('id')\n"
    "  2. Start it: sdk.bugs.start('id') or sdk.features.start('id')\n"
    "  3. THEN delegate with Task/Agent\n\n"
    "This is mandatory -- delegation without attribution defeats the purpose of work tracking."
)

_ACTIVE_ITEM_CONFIRMATION = "Working on: {item_id} ({title})"


def main() -> None:
    # Parse hook input from stdin
    try:
        hook_input = json.load(sys.stdin)
    except (json.JSONDecodeError, ValueError):
        print(json.dumps({"decision": "allow"}))
        return

    tool_name = hook_input.get("tool_name", "")

    # Only enforce on Agent tool calls — the strongest delegation signal.
    # Task is an alias/variant; check both.
    if tool_name not in ("Agent", "Task"):
        print(json.dumps({"decision": "allow"}))
        return

    # Fast path: check SQLite for any in-progress work item
    project_dir = _resolve_project_dir()
    db_file = _db_path(project_dir)

    active_item = None
    if os.path.exists(db_file):
        active_item = _check_active_work_item_db(db_file)

    # Fallback: use SDK if DB query returned nothing or DB doesn't exist
    if active_item is None:
        active_item = _check_active_work_item_sdk()

    if active_item is None:
        # No active work item — inject warning
        print(json.dumps({"decision": "allow", "message": _NO_WORK_ITEM_WARNING}))
    else:
        # Active work item found — brief confirmation
        confirmation = _ACTIVE_ITEM_CONFIRMATION.format(
            item_id=active_item["id"], title=active_item["title"]
        )
        print(json.dumps({"decision": "allow", "message": confirmation}))


if __name__ == "__main__":
    main()
