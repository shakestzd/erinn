from __future__ import annotations

"""
Event logging for HtmlGraph.

This module provides a Git-friendly append-only JSONL event log.

Design goals:
- Source of truth lives in the filesystem (and therefore Git)
- Append-only writes for high-frequency activity events
- Deterministic serialization for rebuildable analytics indexes
"""


import json
from datetime import datetime
from pathlib import Path
from typing import TYPE_CHECKING, Any

from pydantic import BaseModel, ConfigDict, Field, field_serializer, field_validator, model_validator

if TYPE_CHECKING:
    pass


class EventRecord(BaseModel):
    """
    Event record for HtmlGraph tracking.

    Uses Pydantic for automatic validation and serialization.
    Immutable via ConfigDict(frozen=True).
    """

    model_config = ConfigDict(frozen=True)

    @model_validator(mode="before")
    @classmethod
    def _compat_old_kwargs(cls, data: Any) -> Any:
        """Translate legacy EventRecord kwargs to the current schema.

        Old call sites used:
            EventRecord(event_type="...", session_id=None, agent=None,
                        timestamp=..., data={...})

        This validator maps those to the required fields:
            event_id, tool, summary, success, session_id, agent
        """
        if not isinstance(data, dict):
            return data

        import uuid as _uuid

        # Generate event_id if missing
        if "event_id" not in data or not data.get("event_id"):
            data["event_id"] = f"evt-{str(_uuid.uuid4())[:8]}"

        # Map event_type -> tool  (e.g. "FeatureCreated" -> "FeatureCreated")
        if "tool" not in data and "event_type" in data:
            data["tool"] = data.pop("event_type")

        # Extract summary from data dict, or synthesize from tool name
        if "summary" not in data:
            nested_data = data.get("data", {})
            if isinstance(nested_data, dict) and "summary" in nested_data:
                data["summary"] = nested_data["summary"]
            else:
                data["summary"] = data.get("tool", "unknown") or "unknown"

        # Default success to True if missing
        if "success" not in data:
            data["success"] = True

        # Default session_id to "system" if None or missing
        if not data.get("session_id"):
            data["session_id"] = "system"

        # Default agent to "system" if None or missing
        if not data.get("agent"):
            data["agent"] = "system"

        # Move legacy "data" dict into "payload" if payload not set
        if "data" in data and "payload" not in data:
            data["payload"] = data.pop("data")

        return data

    event_id: str = Field(..., min_length=1, description="Unique event identifier")
    timestamp: datetime = Field(..., description="Event timestamp")
    session_id: str = Field(..., min_length=1, description="Session identifier")
    agent: str = Field(..., description="Agent name (e.g., 'claude', 'gemini')")
    tool: str = Field(..., description="Tool used (e.g., 'Bash', 'Edit', 'Read')")
    summary: str = Field(..., description="Human-readable event summary")
    success: bool = Field(..., description="Whether the operation succeeded")
    feature_id: str | None = Field(None, description="Associated feature ID")
    drift_score: float | None = Field(None, description="Context drift score")
    start_commit: str | None = Field(None, description="Starting git commit hash")
    continued_from: str | None = Field(
        None, description="Previous session ID if continued"
    )
    work_type: str | None = Field(None, description="WorkType enum value")
    session_status: str | None = Field(None, description="Session status")
    file_paths: list[str] | None = Field(None, description="Files involved in event")
    payload: dict[str, Any] | None = Field(None, description="Additional event data")
    parent_session_id: str | None = Field(
        None, description="Parent session ID for subagents"
    )

    # Phase 1: Enhanced Event Data Schema for multi-AI delegation tracking
    delegated_to_ai: str | None = Field(
        None, description="AI delegate: 'gemini', 'codex', 'copilot', 'claude', or None"
    )
    task_id: str | None = Field(
        None, description="Unique task ID for parallel tracking"
    )
    task_status: str | None = Field(
        None,
        description="Task status: 'pending', 'running', 'completed', 'failed', 'timeout'",
    )
    model_selected: str | None = Field(
        None, description="Specific model (e.g., 'gemini-2.0-flash')"
    )
    complexity_level: str | None = Field(
        None, description="Complexity: 'low', 'medium', 'high', 'very-high'"
    )
    budget_mode: str | None = Field(
        None, description="Budget mode: 'free', 'balanced', 'performance'"
    )
    execution_duration_seconds: float | None = Field(
        None, description="Delegation execution time"
    )
    tokens_estimated: int | None = Field(None, description="Estimated token usage")
    tokens_actual: int | None = Field(None, description="Actual token usage")
    cost_usd: float | None = Field(None, description="Calculated cost in USD")
    task_findings: str | None = Field(None, description="Results from delegated task")

    @field_validator("event_id", "session_id")
    @classmethod
    def validate_non_empty_string(cls, v: str) -> str:
        """Ensure event_id and session_id are non-empty."""
        if not v or not v.strip():
            raise ValueError("Field must be a non-empty string")
        return v

    @field_serializer("timestamp")
    def serialize_timestamp(self, timestamp: datetime) -> str:
        """Serialize timestamp to ISO format string."""
        return timestamp.isoformat()

    @field_serializer("file_paths")
    def serialize_file_paths(self, file_paths: list[str] | None) -> list[str]:
        """Ensure file_paths is always a list (never None) in JSON output."""
        return file_paths or []

    def to_json(self) -> dict[str, Any]:
        """Convert EventRecord to JSON-serializable dictionary."""
        return self.model_dump(mode="json")


class JsonlEventLog:
    """
    Append-only JSONL event log stored under `.htmlgraph/events/`.
    """

    def __init__(self, events_dir: Path | str):
        self.events_dir = Path(events_dir)
        self.events_dir.mkdir(parents=True, exist_ok=True)

    def path_for_session(self, session_id: str) -> Path:
        # Keep simple and filesystem-friendly.
        return self.events_dir / f"{session_id}.jsonl"

    def append(self, record: EventRecord) -> Path:
        path = self.path_for_session(record.session_id)
        line = (
            json.dumps(record.model_dump(mode="json"), ensure_ascii=False, default=str)
            + "\n"
        )
        path.parent.mkdir(parents=True, exist_ok=True)

        # Best-effort dedupe: some producers (e.g. git hooks) may retry or be chained.
        # Event IDs are intended to be unique; if we already have this ID in the
        # existing file tail, skip appending.
        try:
            if path.exists():
                with path.open("rb") as f:
                    f.seek(0, 2)
                    size = f.tell()
                    tail_size = min(size, 64 * 1024)
                    if tail_size:
                        f.seek(-tail_size, 2)
                        tail = f.read(tail_size).decode("utf-8", errors="ignore")
                        for raw in tail.splitlines()[-250:]:
                            raw = raw.strip()
                            if not raw:
                                continue
                            try:
                                existing = json.loads(raw)
                            except json.JSONDecodeError:
                                continue
                            if existing.get("event_id") == record.event_id:
                                return path
        except Exception:
            pass

        with path.open("a", encoding="utf-8") as f:
            f.write(line)
        return path

    def iter_events(self) -> Any:
        """
        Yield (path, event_dict) for all events across all JSONL files.
        Skips malformed lines.

        Yields:
            tuple[Path, dict[str, Any]]: Path and event dictionary
        """
        for path in sorted(self.events_dir.glob("*.jsonl")):
            try:
                with path.open("r", encoding="utf-8") as f:
                    for line in f:
                        line = line.strip()
                        if not line:
                            continue
                        try:
                            yield path, json.loads(line)
                        except json.JSONDecodeError:
                            continue
            except OSError:
                continue

    def get_session_events(
        self, session_id: str, limit: int | None = None, offset: int = 0
    ) -> list[dict[str, Any]]:
        """
        Get events for a specific session with pagination.

        Args:
            session_id: Session ID to query
            limit: Maximum number of events to return (None = all)
            offset: Number of events to skip from the start

        Returns:
            List of event dictionaries, oldest first
        """
        path = self.path_for_session(session_id)
        if not path.exists():
            return []

        events = []
        try:
            with path.open("r", encoding="utf-8") as f:
                for line in f:
                    line = line.strip()
                    if not line:
                        continue
                    try:
                        events.append(json.loads(line))
                    except json.JSONDecodeError:
                        continue
        except OSError:
            return []

        # Apply offset and limit
        if offset > 0:
            events = events[offset:]
        if limit is not None:
            events = events[:limit]

        return events

    def query_events(
        self,
        session_id: str | None = None,
        tool: str | None = None,
        feature_id: str | None = None,
        since: Any = None,  # datetime or ISO string
        limit: int | None = None,
    ) -> list[dict[str, Any]]:
        """
        Query events with filters.

        Args:
            session_id: Filter by session ID (None = all sessions)
            tool: Filter by tool name (e.g., 'Bash', 'Edit')
            feature_id: Filter by attributed feature ID
            since: Only events after this timestamp (datetime or ISO string)
            limit: Maximum number of events to return

        Returns:
            List of matching event dictionaries, newest first
        """
        from datetime import datetime

        # Convert since to datetime if needed
        since_dt = None
        if since:
            if isinstance(since, str):
                since_dt = datetime.fromisoformat(since.replace("Z", "+00:00"))
            else:
                since_dt = since

        # Get events from specific session or all sessions
        events: list[dict[str, Any]] = []
        if session_id:
            events = self.get_session_events(session_id, limit=None)
        else:
            events = [evt for _, evt in self.iter_events()]

        # Apply filters
        filtered: list[dict[str, Any]] = []
        for evt in events:
            # Tool filter
            if tool and evt.get("tool") != tool:
                continue

            # Feature filter
            if feature_id and evt.get("feature_id") != feature_id:
                continue

            # Timestamp filter
            if since_dt:
                evt_time_str = evt.get("timestamp")
                if evt_time_str and isinstance(evt_time_str, str):
                    try:
                        evt_time = datetime.fromisoformat(
                            evt_time_str.replace("Z", "+00:00")
                        )
                        if evt_time < since_dt:
                            continue
                    except (ValueError, AttributeError):
                        continue

            filtered.append(evt)

        # Sort newest first and apply limit
        filtered.reverse()
        if limit is not None:
            filtered = filtered[:limit]

        return filtered
