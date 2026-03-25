#!/usr/bin/env python3
"""One-time migration: normalize UserQuery input_summary fields that contain
raw <task-notification> XML or /private/tmp/... file paths.

Also normalizes any session_ids that contain file paths (belt-and-suspenders).

Run from project root:
    uv run python scripts/normalize-session-ids.py
"""

import re
import sqlite3
from pathlib import Path

DB_PATH = Path(".htmlgraph/htmlgraph.db")

# Matches a UUID anywhere in a string
UUID_RE = re.compile(
    r"([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})",
    re.IGNORECASE,
)


def sanitize_summary(text: str, max_len: int = 200) -> str:
    """Strip noise from a stored input_summary."""
    if not text:
        return text

    t = text.strip()

    # Strip complete XML blocks
    t = re.sub(
        r"<task-notification>[\s\S]*?</task-notification>", "", t, flags=re.IGNORECASE
    )
    t = re.sub(
        r"<system-reminder>[\s\S]*?</system-reminder>", "", t, flags=re.IGNORECASE
    )
    t = re.sub(r"<[a-zA-Z_-]+>[\s\S]*?</[a-zA-Z_-]+>", "", t, flags=re.IGNORECASE)

    # Strip unclosed/truncated <task-notification> blocks
    t = re.sub(r"<task-notification>[\s\S]*", "", t, flags=re.IGNORECASE)

    # Strip /private/tmp/... and /tmp/... file paths
    t = re.sub(r"/private/tmp/\S*", "", t)
    t = re.sub(r"/tmp/\S*", "", t)

    # Strip remaining orphaned XML tags
    t = re.sub(r"</?[a-zA-Z_-]+>", "", t)

    t = t.strip()
    # If sanitization produced nothing, store empty string (don't fall back to raw noise)
    return t[:max_len]


def normalize_session_ids(conn: sqlite3.Connection) -> int:
    """Normalize path-format session_ids to UUID-only in both tables."""
    fixed = 0
    cursor = conn.cursor()

    for table in ("agent_events", "sessions"):
        cursor.execute(
            f"SELECT DISTINCT session_id FROM {table} WHERE session_id LIKE '%/%'"
        )
        path_ids = cursor.fetchall()
        if path_ids:
            print(f"Found {len(path_ids)} path-format session_ids in {table}")
        for (path_id,) in path_ids:
            match = UUID_RE.search(path_id)
            if match:
                uuid = match.group(1)
                cursor.execute(
                    f"UPDATE {table} SET session_id = ? WHERE session_id = ?",
                    (uuid, path_id),
                )
                print(f"  {table}: {path_id[:70]}... → {uuid}")
                fixed += cursor.rowcount

    return fixed


def normalize_user_query_summaries(conn: sqlite3.Connection) -> int:
    """Clean up UserQuery input_summary fields containing XML/path noise."""
    cursor = conn.cursor()

    cursor.execute("""
        SELECT event_id, input_summary
        FROM agent_events
        WHERE tool_name = 'UserQuery'
          AND (
            input_summary LIKE '%task-notification%'
            OR input_summary LIKE '/private/tmp%'
            OR input_summary LIKE '/tmp/%'
          )
    """)
    rows = cursor.fetchall()
    print(f"\nFound {len(rows)} UserQuery events with noisy input_summary")

    fixed = 0
    for event_id, raw_summary in rows:
        clean = sanitize_summary(raw_summary)
        if clean != raw_summary:
            cursor.execute(
                "UPDATE agent_events SET input_summary = ? WHERE event_id = ?",
                (clean, event_id),
            )
            preview_before = (raw_summary or "")[:60].replace("\n", " ")
            preview_after = (clean or "")[:60].replace("\n", " ")
            print(f"  {event_id}: [{preview_before}...] → [{preview_after}]")
            fixed += 1

    return fixed


def main() -> None:
    if not DB_PATH.exists():
        print(f"Database not found at {DB_PATH}. Run from project root.")
        return

    conn = sqlite3.connect(DB_PATH)
    try:
        # 1. Normalize path-format session_ids
        sid_fixed = normalize_session_ids(conn)

        # 2. Clean up noisy UserQuery input_summary fields
        summary_fixed = normalize_user_query_summaries(conn)

        conn.commit()
        print(
            f"\nDone! Fixed {sid_fixed} session_id rows, {summary_fixed} input_summary rows."
        )

    finally:
        conn.close()


if __name__ == "__main__":
    main()
