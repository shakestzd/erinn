"""
Bidirectional converters between HTML files and Pydantic models.

Provides:
- html_to_node: Parse HTML file into Node model
- node_to_html: Write Node model to HTML file
- Preserves all semantic information
- Handles edge cases (missing fields, malformed HTML)
"""

import logging
from pathlib import Path
from typing import Any, cast

from htmlgraph.models import (
    ActivityEntry,
    Chore,
    Edge,
    Node,
    Pattern,
    Session,
    Spike,
    Step,
)
from htmlgraph.parser import HtmlParser

logger = logging.getLogger(__name__)


def html_to_node(filepath: Path | str) -> Node:
    """
    Parse HTML file into a Node model (or subclass).

    Args:
        filepath: Path to HTML file

    Returns:
        Node instance (or Spike/Chore subclass) populated from HTML

    Raises:
        FileNotFoundError: If file doesn't exist
        ValueError: If HTML is malformed or missing required data
    """
    filepath = Path(filepath)
    if not filepath.exists():
        raise FileNotFoundError(f"HTML file not found: {filepath}")

    parser = HtmlParser.from_file(filepath)
    data = parser.parse_full_node()

    # Validate required fields
    if not data.get("id"):
        raise ValueError(f"HTML file missing node ID: {filepath}")

    # Convert edge dicts to Edge models
    edges: dict[str, list[Edge]] = {}
    for rel_type, edge_list in data.get("edges", {}).items():
        edges[rel_type] = [
            Edge(
                target_id=e["target_id"],
                relationship=e.get("relationship", rel_type),
                title=e.get("title"),
                since=e.get("since"),
                properties=e.get("properties", {}),
            )
            for e in edge_list
        ]
    data["edges"] = edges

    # Convert step dicts to Step models
    steps = [
        Step(
            description=s["description"],
            completed=s.get("completed", False),
            agent=s.get("agent"),
        )
        for s in data.get("steps", [])
    ]
    data["steps"] = steps

    # Map node type to model class
    node_type = data.get("type", "node")
    model_classes: dict[str, type[Node]] = {
        "spike": Spike,
        "chore": Chore,
        "pattern": Pattern,
        "node": Node,
    }

    model_class = model_classes.get(node_type, Node)
    return cast(Node, model_class(**data))


def node_to_html(
    node: Node,
    filepath: Path | str,
    stylesheet_path: str = "../styles.css",
    create_dirs: bool = True,
) -> Path:
    """
    Write a Node model to an HTML file.

    Args:
        node: Node instance to serialize
        filepath: Destination file path
        stylesheet_path: Relative path to CSS stylesheet
        create_dirs: Create parent directories if needed

    Returns:
        Path to written file
    """
    filepath = Path(filepath)

    if create_dirs:
        filepath.parent.mkdir(parents=True, exist_ok=True)

    html_content = node.to_html(stylesheet_path=stylesheet_path)
    filepath.write_text(html_content, encoding="utf-8")

    return filepath


def update_node_html(
    filepath: Path | str,
    updates: dict[str, Any],
    stylesheet_path: str = "../styles.css",
) -> Node:
    """
    Update specific fields in an existing HTML node file.

    Args:
        filepath: Path to existing HTML file
        updates: Dict of fields to update
        stylesheet_path: Relative path to CSS stylesheet

    Returns:
        Updated Node instance

    Example:
        update_node_html("task.html", {"status": "done"})
    """
    # Load existing node
    node = html_to_node(filepath)

    # Apply updates
    for key, value in updates.items():
        if hasattr(node, key):
            setattr(node, key, value)

    # Write back
    node_to_html(node, filepath, stylesheet_path=stylesheet_path)

    return node


def merge_nodes(base: Node, overlay: Node) -> Node:
    """
    Merge two nodes, with overlay values taking precedence.

    Useful for updating nodes while preserving unspecified fields.

    Args:
        base: Base node with default values
        overlay: Node with values to apply over base

    Returns:
        New Node instance with merged values
    """
    base_dict = base.model_dump()
    overlay_dict = overlay.model_dump(exclude_unset=True)

    # Deep merge for nested structures
    for key, value in overlay_dict.items():
        if key == "edges" and value:
            # Merge edge dictionaries
            base_edges = base_dict.get("edges", {})
            for rel_type, edge_list in value.items():
                if rel_type in base_edges:
                    # Replace edges of same type
                    base_edges[rel_type] = edge_list
                else:
                    base_edges[rel_type] = edge_list
            base_dict["edges"] = base_edges
        elif key == "steps" and value:
            # Replace steps entirely
            base_dict["steps"] = value
        elif key == "properties" and value:
            # Merge properties
            base_dict.setdefault("properties", {}).update(value)
        else:
            base_dict[key] = value

    return Node.from_dict(base_dict)


def node_to_dict(node: Node) -> dict[str, Any]:
    """
    Convert Node to a plain dictionary (JSON-serializable).

    Useful for API responses or JSON export.
    """
    data = node.model_dump()

    # Convert datetime objects to ISO strings
    for key in ["created", "updated"]:
        if key in data and data[key]:
            data[key] = data[key].isoformat()

    # Convert edges
    for rel_type, edges in data.get("edges", {}).items():
        for edge in edges:
            if edge.get("since"):
                edge["since"] = edge["since"].isoformat()

    # Convert steps
    for step in data.get("steps", []):
        if step.get("timestamp"):
            step["timestamp"] = step["timestamp"].isoformat()

    result: dict[str, Any] = data
    return result


def dict_to_node(data: dict[str, Any]) -> Node:
    """
    Create Node from a plain dictionary.

    Handles datetime string parsing.
    """
    from datetime import datetime

    # Parse datetime strings
    for key in ["created", "updated"]:
        if key in data and isinstance(data[key], str):
            data[key] = datetime.fromisoformat(data[key].replace("Z", "+00:00"))

    # Parse edge datetimes
    for edges in data.get("edges", {}).values():
        for edge in edges:
            if isinstance(edge.get("since"), str):
                edge["since"] = datetime.fromisoformat(
                    edge["since"].replace("Z", "+00:00")
                )

    # Parse step datetimes
    for step in data.get("steps", []):
        if isinstance(step.get("timestamp"), str):
            step["timestamp"] = datetime.fromisoformat(
                step["timestamp"].replace("Z", "+00:00")
            )

    return Node.from_dict(data)


class NodeConverter:
    """
    Converter class for batch operations on multiple nodes.

    Example:
        converter = NodeConverter("features/")
        nodes = converter.load_all()
        converter.save_all(nodes)
    """

    def __init__(self, directory: Path | str, stylesheet_path: str = "../styles.css"):
        """
        Initialize converter for a directory.

        Args:
            directory: Directory containing HTML node files
            stylesheet_path: Default stylesheet path for new files
        """
        self.directory = Path(directory)
        self.stylesheet_path = stylesheet_path

    def load(self, node_id: str) -> Node | None:
        """Load a single node by ID."""
        filepath = self.directory / f"{node_id}.html"
        if filepath.exists():
            return html_to_node(filepath)
        return None

    def load_all(self, pattern: str | list[str] = "*.html") -> list[Node]:
        """
        Load all nodes matching pattern(s).

        Args:
            pattern: Glob pattern(s) to match. Can be a single pattern or list of patterns.
                     Examples: "*.html", ["*.html", "*/index.html"]
        """
        nodes = []
        patterns = [pattern] if isinstance(pattern, str) else pattern

        for pat in patterns:
            for filepath in self.directory.glob(pat):
                if filepath.is_file():  # Skip directories
                    try:
                        nodes.append(html_to_node(filepath))
                    except (ValueError, KeyError):
                        continue  # Skip malformed files
        return nodes

    def save(self, node: Node) -> Path:
        """Save a single node."""
        filepath = self.directory / f"{node.id}.html"
        return node_to_html(node, filepath, self.stylesheet_path)

    def save_all(self, nodes: list[Node]) -> list[Path]:
        """Save multiple nodes."""
        return [self.save(node) for node in nodes]

    def exists(self, node_id: str) -> bool:
        """Check if a node file exists."""
        return (self.directory / f"{node_id}.html").exists()

    def delete(self, node_id: str) -> bool:
        """Delete a node file."""
        filepath = self.directory / f"{node_id}.html"
        if filepath.exists():
            filepath.unlink()
            return True
        return False


# =============================================================================
# Session Converters
# =============================================================================


def session_to_dict(session: Session) -> dict[str, Any]:
    """
    Convert Session to a plain dictionary (JSON-serializable).

    Useful for API responses or JSON export.
    """
    data = session.model_dump()

    # Convert datetime objects to ISO strings
    for key in ["started_at", "ended_at", "last_activity"]:
        if key in data and data[key]:
            data[key] = data[key].isoformat()

    # Convert activity log timestamps
    for entry in data.get("activity_log", []):
        if entry.get("timestamp"):
            entry["timestamp"] = entry["timestamp"].isoformat()

    result: dict[str, Any] = data
    return result


def dict_to_session(data: dict[str, Any]) -> Session:
    """
    Create Session from a plain dictionary.

    Handles datetime string parsing.
    """
    from datetime import datetime

    # Parse datetime strings
    for key in ["started_at", "ended_at", "last_activity"]:
        if key in data and isinstance(data[key], str):
            data[key] = datetime.fromisoformat(data[key].replace("Z", "+00:00"))

    # Parse activity log timestamps
    for entry in data.get("activity_log", []):
        if isinstance(entry.get("timestamp"), str):
            entry["timestamp"] = datetime.fromisoformat(
                entry["timestamp"].replace("Z", "+00:00")
            )

    return Session.from_dict(data)


def html_to_session(filepath: Path | str) -> Session:
    """
    Parse HTML file into a Session model.

    Args:
        filepath: Path to HTML file

    Returns:
        Session instance populated from HTML

    Raises:
        FileNotFoundError: If file doesn't exist
        ValueError: If HTML is malformed or missing required data
    """
    from datetime import datetime

    filepath = Path(filepath)
    if not filepath.exists():
        raise FileNotFoundError(f"HTML file not found: {filepath}")

    parser = HtmlParser.from_file(filepath)

    # Get article element with session data
    article_results = parser.query("article[data-type='session']")
    article = article_results[0] if article_results else None
    if not article:
        raise ValueError(f"No session article found in: {filepath}")

    # Extract session attributes
    session_id = article.attrs.get("id")
    if not session_id:
        raise ValueError(f"Session missing ID: {filepath}")

    data = {
        "id": session_id,
        "status": article.attrs.get("data-status") or "active",
        "agent": article.attrs.get("data-agent") or "claude-code",
        "is_subagent": article.attrs.get("data-is-subagent") == "true",
        "event_count": int(article.attrs.get("data-event-count") or 0),
    }

    # Parse timestamps
    started_at = article.attrs.get("data-started-at")
    if started_at:
        data["started_at"] = datetime.fromisoformat(started_at.replace("Z", "+00:00"))

    ended_at = article.attrs.get("data-ended-at")
    if ended_at:
        data["ended_at"] = datetime.fromisoformat(ended_at.replace("Z", "+00:00"))

    last_activity = article.attrs.get("data-last-activity")
    if last_activity:
        data["last_activity"] = datetime.fromisoformat(
            last_activity.replace("Z", "+00:00")
        )

    start_commit = article.attrs.get("data-start-commit")
    if start_commit:
        data["start_commit"] = start_commit

    # Parse work type classification fields
    primary_work_type = article.attrs.get("data-primary-work-type")
    if primary_work_type:
        data["primary_work_type"] = primary_work_type

    work_breakdown_json = article.attrs.get("data-work-breakdown")
    if work_breakdown_json:
        import json

        try:
            data["work_breakdown"] = json.loads(work_breakdown_json)
        except (json.JSONDecodeError, ValueError):
            pass  # Skip if invalid JSON

    # Parse transcript integration fields
    transcript_id = article.attrs.get("data-transcript-id")
    if transcript_id:
        data["transcript_id"] = transcript_id

    transcript_path = article.attrs.get("data-transcript-path")
    if transcript_path:
        data["transcript_path"] = transcript_path

    transcript_synced = article.attrs.get("data-transcript-synced")
    if transcript_synced:
        data["transcript_synced_at"] = datetime.fromisoformat(
            transcript_synced.replace("Z", "+00:00")
        )

    transcript_branch = article.attrs.get("data-transcript-branch")
    if transcript_branch:
        data["transcript_git_branch"] = transcript_branch

    # Parse title
    title_el_results = parser.query("h1")
    title_el = title_el_results[0] if title_el_results else None
    if title_el:
        data["title"] = title_el.to_text().strip()

    # Parse worked_on edges
    worked_on = []
    for link in parser.query(
        "nav[data-graph-edges] section[data-edge-type='worked-on'] a"
    ):
        href = link.attrs.get("href") or ""
        # Extract item ID from href - handle any subdirectory (features, bugs, spikes, etc.)
        # href format: ../subdir/item-id.html or legacy ../features/item-id.html
        item_id = href.split("/")[-1].replace(".html", "")
        if item_id:
            worked_on.append(item_id)
    data["worked_on"] = worked_on

    # Parse continued_from edge
    continued_link_results = parser.query(
        "nav[data-graph-edges] section[data-edge-type='continued-from'] a"
    )
    continued_link = continued_link_results[0] if continued_link_results else None
    if continued_link:
        href = continued_link.attrs.get("href") or ""
        data["continued_from"] = href.replace(".html", "")

    # Parse handoff context
    handoff_section_results = parser.query("section[data-handoff]")
    handoff_section = handoff_section_results[0] if handoff_section_results else None
    if handoff_section:
        notes_el_results = parser.query("section[data-handoff] [data-handoff-notes]")
        notes_el = notes_el_results[0] if notes_el_results else None
        if notes_el:
            notes_text = notes_el.to_text().strip()
            if notes_text.lower().startswith("notes:"):
                notes_text = notes_text.split(":", 1)[1].strip()
            data["handoff_notes"] = notes_text

        next_el_results = parser.query("section[data-handoff] [data-recommended-next]")
        next_el = next_el_results[0] if next_el_results else None
        if next_el:
            next_text = next_el.to_text().strip()
            if next_text.lower().startswith("recommended next:"):
                next_text = next_text.split(":", 1)[1].strip()
            data["recommended_next"] = next_text

        blockers = []
        for li in parser.query("section[data-handoff] div[data-blockers] li"):
            blocker_text = li.to_text().strip()
            if blocker_text:
                blockers.append(blocker_text)
        if blockers:
            data["blockers"] = blockers

        # Parse recommended context files
        recommended_context = []
        for li in parser.query(
            "section[data-handoff] div[data-recommended-context] li"
        ):
            file_path = li.to_text().strip()
            if file_path:
                recommended_context.append(file_path)
        if recommended_context:
            data["recommended_context"] = recommended_context

    # Parse activity log
    activity_log = []
    for li in parser.query("section[data-activity-log] ol li"):
        entry_data = {
            "summary": li.to_text().strip(),
            "tool": li.attrs.get("data-tool") or "unknown",
            "success": li.attrs.get("data-success") != "false",
        }

        ts = li.attrs.get("data-ts")
        if ts:
            entry_data["timestamp"] = datetime.fromisoformat(ts.replace("Z", "+00:00"))

        event_id = li.attrs.get("data-event-id")
        if event_id:
            entry_data["id"] = event_id

        feature = li.attrs.get("data-feature")
        if feature:
            entry_data["feature_id"] = feature

        drift = li.attrs.get("data-drift")
        if drift:
            entry_data["drift_score"] = float(drift)

        parent = li.attrs.get("data-parent")
        if parent:
            entry_data["parent_activity_id"] = parent

        activity_log.append(ActivityEntry(**entry_data))

    # Activity log in HTML is reversed (newest first), so reverse back
    data["activity_log"] = list(reversed(activity_log))

    # Parse detected patterns from table (if present)
    detected_patterns = []
    for tr in parser.query("section[data-detected-patterns] table tbody tr"):
        # Extract pattern data from table row
        pattern_type = tr.attrs.get("data-pattern-type", "neutral")

        # Extract sequence from first <td class="sequence">
        seq_tds = tr.query("td.sequence")
        seq_td = seq_tds[0] if seq_tds else None
        sequence_str = seq_td.to_text().strip() if seq_td else ""
        sequence = [s.strip() for s in sequence_str.split("→")] if sequence_str else []

        # Extract count from third <td>
        tds = tr.query("td")
        count_td = tds[2] if len(tds) > 2 else None
        count_str = count_td.to_text().strip() if count_td else "0"
        try:
            count = int(count_str)
        except (ValueError, TypeError):
            count = 0

        # Extract timestamps from fourth <td>
        time_td = tds[3] if len(tds) > 3 else None
        time_str = time_td.to_text().strip() if time_td else ""
        times = time_str.split(" / ")
        first_detected = times[0].strip() if len(times) > 0 else ""
        last_detected = times[1].strip() if len(times) > 1 else ""

        if sequence:  # Only add if we have a valid sequence
            detected_patterns.append(
                {
                    "sequence": sequence,
                    "pattern_type": pattern_type,
                    "detection_count": count,
                    "first_detected": first_detected,
                    "last_detected": last_detected,
                }
            )

    data["detected_patterns"] = detected_patterns

    # Parse error log from error section (if present)
    error_log = []
    for details in parser.query("section[data-error-log] details"):
        error_data = {
            "error_type": details.attrs.get("data-error-type", "Unknown"),
            "message": "",
            "traceback": None,
        }

        ts = details.attrs.get("data-ts")
        if ts:
            error_data["timestamp"] = datetime.fromisoformat(ts.replace("Z", "+00:00"))

        tool = details.attrs.get("data-tool")
        if tool:
            error_data["tool"] = tool

        # Parse summary text (first line of details)
        summary_el_results = details.query("summary")
        summary_el = summary_el_results[0] if summary_el_results else None
        if summary_el:
            summary_text = summary_el.to_text().strip()
            # Extract message from "ErrorType: message" format
            if ": " in summary_text:
                error_data["message"] = summary_text.split(": ", 1)[1]
            else:
                error_data["message"] = summary_text

        # Parse traceback (if present)
        traceback_el_results = details.query("pre.traceback")
        traceback_el = traceback_el_results[0] if traceback_el_results else None
        if traceback_el:
            error_data["traceback"] = traceback_el.to_text().strip()

        if error_data.get("message") or error_data.get("traceback"):
            from htmlgraph.models import ErrorEntry

            error_log.append(ErrorEntry(**error_data))

    data["error_log"] = error_log

    return Session(**data)


def session_to_html(
    session: Session,
    filepath: Path | str,
    stylesheet_path: str = "../styles.css",
    create_dirs: bool = True,
) -> Path:
    """
    Write a Session model to an HTML file.

    Args:
        session: Session instance to serialize
        filepath: Destination file path
        stylesheet_path: Relative path to CSS stylesheet
        create_dirs: Create parent directories if needed

    Returns:
        Path to written file
    """
    filepath = Path(filepath)

    if create_dirs:
        filepath.parent.mkdir(parents=True, exist_ok=True)

    html_content = session.to_html(stylesheet_path=stylesheet_path)
    filepath.write_text(html_content, encoding="utf-8")

    return filepath


class SessionConverter:
    """
    Converter class for batch operations on sessions.

    Example:
        converter = SessionConverter("sessions/")
        sessions = converter.load_all()
        converter.save_all(sessions)
    """

    def __init__(self, directory: Path | str, stylesheet_path: str = "../styles.css"):
        self.directory = Path(directory)
        self.stylesheet_path = stylesheet_path

    def load(self, session_id: str) -> Session | None:
        """
        Load a single session by ID and cleanup stale work item references.

        This automatically removes references to deleted/missing work items
        from the session's worked_on list to maintain data integrity.
        """
        filepath = self.directory / f"{session_id}.html"
        if not filepath.exists():
            return None

        # Load session from HTML
        session = html_to_session(filepath)

        # Cleanup stale work item references
        # (removes IDs from worked_on that no longer exist in .htmlgraph/)
        graph_dir = self.directory.parent  # .htmlgraph directory
        cleanup_result = session.cleanup_missing_references(graph_dir)

        # Log warning if stale references were removed
        if cleanup_result["removed_count"] > 0:
            logger.warning(
                f"Session {session_id}: Removed {cleanup_result['removed_count']} "
                f"stale work item references: {cleanup_result['removed']}"
            )
            # Save cleaned session back to disk
            self.save(session)

        return session

    def load_all(self, pattern: str = "*.html") -> list[Session]:
        """
        Load all sessions matching pattern and cleanup stale references.

        Each session is loaded through self.load() which automatically cleans up
        stale work item references.
        """
        sessions = []
        for filepath in self.directory.glob(pattern):
            try:
                # Extract session_id from filename
                session_id = filepath.stem  # e.g., "sess-abc123"
                session = self.load(session_id)
                if session:
                    sessions.append(session)
            except (ValueError, KeyError):
                continue
        return sessions

    def save(self, session: Session) -> Path:
        """Save a single session."""
        filepath = self.directory / f"{session.id}.html"
        return session_to_html(session, filepath, self.stylesheet_path)

    def save_all(self, sessions: list[Session]) -> list[Path]:
        """Save multiple sessions."""
        return [self.save(session) for session in sessions]

    def exists(self, session_id: str) -> bool:
        """Check if a session file exists."""
        return (self.directory / f"{session_id}.html").exists()

    def delete(self, session_id: str) -> bool:
        """Delete a session file."""
        filepath = self.directory / f"{session_id}.html"
        if filepath.exists():
            filepath.unlink()
            return True
        return False
