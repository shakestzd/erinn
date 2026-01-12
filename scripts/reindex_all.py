#!/usr/bin/env python3
import sqlite3
from datetime import datetime
from html.parser import HTMLParser
from pathlib import Path
from typing import Any


class HtmlMetadataParser(HTMLParser):
    def __init__(self):
        super().__init__()
        self.metadata: dict[str, Any] = {}
        self.title: str | None = None
        self.in_title = False
        self.current_node_id: str | None = None

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str]]):
        attrs_dict = dict(attrs)
        if tag in ("article", "section"):
            self.current_node_id = attrs_dict.get("id")
            for key, value in attrs_dict.items():
                if key.startswith("data-"):
                    attr_name = key[5:].replace("-", "_")
                    self.metadata[attr_name] = value
        if tag == "title" or (tag == "h1" and not self.title):
            self.in_title = True

    def handle_endtag(self, tag: str):
        if tag in ("title", "h1"):
            self.in_title = False

    def handle_data(self, data: str):
        if self.in_title and data.strip():
            if not self.title:
                self.title = data.strip()


def parse_html(html_file: Path) -> dict[str, Any] | None:
    try:
        with open(html_file, encoding="utf-8") as f:
            html_content = f.read()
        parser = HtmlMetadataParser()
        parser.feed(html_content)
        data = parser.metadata
        data["id"] = data.get("id") or html_file.stem
        data["title"] = parser.title or data.get("title") or "Untitled"
        return data
    except Exception as e:
        print(f"Error parsing {html_file}: {e}")
        return None


def normalize_status(status: str | None) -> str:
    if not status:
        return "todo"
    status = status.lower().replace("-", "_")
    if status in ("todo", "in_progress", "blocked", "done", "cancelled"):
        return status
    if status == "active":
        return "in_progress"
    if status == "completed":
        return "done"
    return "todo"


def normalize_type(node_type: str | None, default: str) -> str:
    if not node_type:
        return default
    node_type = node_type.lower()
    if node_type in ("feature", "bug", "spike", "chore", "epic", "task"):
        return node_type
    if node_type == "phase":
        return "epic"
    return "feature"


def reindex():
    graph_dir = Path(".htmlgraph")
    db_path = graph_dir / "index.sqlite"

    print(f"Reindexing HTML files into {db_path}...")

    conn = sqlite3.connect(db_path)
    conn.row_factory = sqlite3.Row
    cursor = conn.cursor()

    print("Clearing features and tracks tables...")
    cursor.execute("DELETE FROM features")
    cursor.execute("DELETE FROM tracks")

    type_map = {
        "features": "feature",
        "bugs": "bug",
        "spikes": "spike",
        "chores": "chore",
        "epics": "epic",
    }

    feature_count = 0
    for dir_name, node_type in type_map.items():
        dir_path = graph_dir / dir_name
        if not dir_path.exists():
            continue

        print(f"Scanning {dir_name}...")
        for html_file in dir_path.glob("*.html"):
            data = parse_html(html_file)
            if not data:
                continue

            data_type = normalize_type(data.get("type"), node_type)
            status = normalize_status(data.get("status"))

            try:
                cursor.execute(
                    """
                    INSERT INTO features (id, type, title, status, priority, assigned_to, track_id, description, created_at, updated_at)
                    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
                    (
                        data["id"],
                        data_type,
                        data["title"],
                        status,
                        data.get("priority", "medium"),
                        data.get("agent_assigned") or data.get("assigned"),
                        data.get("track_id"),
                        data.get("description"),
                        data.get("created", datetime.now().isoformat()),
                        data.get("updated", datetime.now().isoformat()),
                    ),
                )
                feature_count += 1
            except sqlite3.Error as e:
                print(f"Failed to insert {data['id']}: {e}")

    print(f"Inserted {feature_count} items into features table.")

    tracks_dir = graph_dir / "tracks"
    track_count = 0
    if tracks_dir.exists():
        print("Scanning tracks...")
        for html_file in tracks_dir.glob("*.html"):
            data = parse_html(html_file)
            if not data:
                continue
            try:
                cursor.execute(
                    """
                    INSERT INTO tracks (track_id, title, description, priority, status, created_at, updated_at)
                    VALUES (?, ?, ?, ?, ?, ?, ?)
                """,
                    (
                        data["id"],
                        data["title"],
                        data.get("description"),
                        data.get("priority", "medium"),
                        normalize_status(data.get("status")),
                        data.get("created", datetime.now().isoformat()),
                        data.get("updated", datetime.now().isoformat()),
                    ),
                )
                track_count += 1
            except sqlite3.Error as e:
                print(f"Failed to insert track {data['id']}: {e}")

    print(f"Inserted {track_count} tracks.")

    sessions_dir = graph_dir / "sessions"
    session_update_count = 0
    if sessions_dir.exists():
        print("Scanning sessions...")
        for html_file in sessions_dir.glob("*.html"):
            data = parse_html(html_file)
            if not data:
                continue

            try:
                cursor.execute(
                    """
                    INSERT INTO sessions (session_id, agent_assigned, status, created_at, completed_at)
                    VALUES (?, ?, ?, ?, ?)
                    ON CONFLICT(session_id) DO UPDATE SET
                        agent_assigned=excluded.agent_assigned,
                        status=excluded.status,
                        created_at=excluded.created_at,
                        completed_at=excluded.completed_at
                """,
                    (
                        data["id"],
                        data.get("agent", "unknown"),
                        data.get("status", "active"),
                        data.get("started_at")
                        or data.get("created")
                        or datetime.now().isoformat(),
                        data.get("ended_at") or data.get("completed_at"),
                    ),
                )
                session_update_count += 1
            except sqlite3.Error as e:
                print(f"Failed to upsert session {data['id']}: {e}")

    print(f"Updated {session_update_count} sessions.")

    conn.commit()
    conn.close()
    print("Reindexing complete!")


if __name__ == "__main__":
    reindex()
