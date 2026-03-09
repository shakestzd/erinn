"""
Pydantic models for HtmlGraph nodes, edges, and steps.

These models provide:
- Schema validation for graph data
- HTML serialization/deserialization
- Lightweight context generation for AI agents
"""

from datetime import datetime
from pathlib import Path
from typing import Any, Literal

from pydantic import BaseModel, Field

from htmlgraph.models.base import Node, utc_now
from htmlgraph.models.context import ActivityEntry, ContextSnapshot, ErrorEntry


def _work_item_subdir(item_id: str) -> str:
    """Return the subdirectory name for a work item based on its ID prefix."""
    if item_id.startswith("feat-") or item_id.startswith("feature-"):
        return "features"
    elif item_id.startswith("bug-"):
        return "bugs"
    elif item_id.startswith("spk-") or item_id.startswith("spike-"):
        return "spikes"
    elif item_id.startswith("trk-") or item_id.startswith("track-"):
        return "tracks"
    elif item_id.startswith("insi-") or item_id.startswith("insight-"):
        return "insights"
    elif item_id.startswith("chore-"):
        return "chores"
    elif item_id.startswith("epic-"):
        return "epics"
    else:
        return "features"


class Session(BaseModel):
    """
    An agent work session containing an activity log.

    Sessions track agent work over time with:
    - Status tracking (active, ended, stale)
    - High-frequency activity log (inline events)
    - Links to features worked on
    - Session continuity (continued_from)
    """

    id: str
    title: str = ""
    agent: str = "claude-code"
    status: Literal["active", "ended", "stale"] = "active"
    is_subagent: bool = False

    started_at: datetime = Field(default_factory=datetime.now)
    ended_at: datetime | None = None
    last_activity: datetime = Field(default_factory=datetime.now)

    start_commit: str | None = None  # Git commit hash at session start
    end_commit: str | None = None  # Git commit hash at session end
    event_count: int = 0

    # Relationships
    worked_on: list[str] = Field(default_factory=list)  # Feature IDs
    continued_from: str | None = None  # Previous session ID

    # Parent session context (for nested Task() calls)
    parent_session: str | None = None  # Parent session ID
    parent_activity: str | None = None  # Parent activity ID
    nesting_depth: int = 0  # Depth of nesting (0 = top-level)

    # Handoff context (Phase 2 Feature 3: Cross-Session Continuity)
    handoff_notes: str | None = None
    recommended_next: str | None = None
    blockers: list[str] = Field(default_factory=list)
    recommended_context: list[str] = Field(
        default_factory=list
    )  # File paths to keep context for

    # High-frequency activity log
    activity_log: list[ActivityEntry] = Field(default_factory=list)

    # Work type categorization (Phase 1: Work Type Classification)
    primary_work_type: str | None = None  # WorkType enum value
    work_breakdown: dict[str, int] | None = None  # {work_type: event_count}

    # Conversation tracking (for conversation-level auto-spikes)
    last_conversation_id: str | None = None  # Last external conversation ID

    # Context tracking (Phase N: Context Analytics)
    context_snapshots: list[ContextSnapshot] = Field(default_factory=list)
    peak_context_tokens: int = 0  # High water mark for context usage
    total_tokens_generated: int = 0  # Cumulative output tokens
    total_cost_usd: float = 0.0  # Cumulative cost for session
    context_by_feature: dict[str, int] = Field(
        default_factory=dict
    )  # {feature_id: tokens}

    # Claude Code transcript integration
    transcript_id: str | None = None  # Claude Code session UUID (from JSONL)
    transcript_path: str | None = None  # Path to source JSONL file
    transcript_synced_at: datetime | None = None  # Last sync timestamp
    transcript_git_branch: str | None = None  # Git branch from transcript

    # Pattern detection (inline storage to avoid file bloat)
    detected_patterns: list[dict[str, Any]] = Field(default_factory=list)
    """
    Patterns detected during this session.

    Format:
    {
        "sequence": ["Bash", "Read", "Edit"],
        "pattern_type": "neutral",  # or "optimal", "anti_pattern"
        "detection_count": 3,
        "first_detected": "2026-01-02T10:00:00",
        "last_detected": "2026-01-02T10:30:00"
    }
    """

    # Error handling (Phase 1B)
    error_log: list[ErrorEntry] = Field(default_factory=list)
    """Error records for this session with full tracebacks for debugging."""

    @classmethod
    def from_node(cls, node: Node) -> "Session":
        """
        Create a Session from a generic Node.

        Args:
            node: Node instance to convert

        Returns:
            Session instance populated from Node data
        """
        # Most fields are in properties
        data = {
            "id": node.id,
            "title": node.title,
            "agent": node.properties.get("agent") or "claude-code",
            "status": node.status
            if node.status in ("active", "ended", "stale")
            else "active",
            "event_count": int(node.properties.get("event_count") or 0),
        }

        # Handle datetimes
        if node.properties.get("started_at"):
            data["started_at"] = node.properties["started_at"]
        if node.properties.get("ended_at"):
            data["ended_at"] = node.properties["ended_at"]

        return cls.from_dict(data)

    def to_node(self) -> Node:
        """
        Convert Session to a generic Node.

        Returns:
            Node instance populated from Session data
        """
        # Map Session fields to Node properties
        node = Node(
            id=self.id,
            title=self.title or f"Session {self.id}",
            type="session",
            status=self.status,
            classes=["session", self.status],
            created=self.started_at,
            updated=self.last_activity,
        )

        # Store parent session context in properties
        if self.parent_session:
            node.properties["parent_session"] = self.parent_session
        if self.parent_activity:
            node.properties["parent_activity"] = self.parent_activity
        if self.nesting_depth > 0:
            node.properties["nesting_depth"] = self.nesting_depth

        # Set specific properties
        node.properties["agent"] = self.agent
        node.properties["started_at"] = self.started_at
        node.properties["event_count"] = self.event_count
        if self.ended_at:
            node.properties["ended_at"] = self.ended_at

        return node

    def add_activity(self, entry: ActivityEntry) -> None:
        """Add an activity entry to the log."""
        self.activity_log.append(entry)
        self.event_count += 1
        self.last_activity = utc_now()

        # Track features worked on
        if entry.feature_id and entry.feature_id not in self.worked_on:
            self.worked_on.append(entry.feature_id)

    def add_error(
        self,
        error_type: str,
        message: str,
        traceback: str | None = None,
        tool: str | None = None,
        context: str | None = None,
    ) -> None:
        """
        Add an error entry to the error log.

        Args:
            error_type: Exception class name (ValueError, FileNotFoundError, etc.)
            message: Error message
            traceback: Full traceback for debugging
            tool: Tool that caused the error (Edit, Bash, etc.)
            context: Additional context information
        """
        error = ErrorEntry(
            error_type=error_type,
            message=message,
            traceback=traceback,
            tool=tool,
            context=context,
            session_id=self.id,
        )
        self.error_log.append(error)

    def end(self) -> None:
        """Mark session as ended."""
        self.status = "ended"
        self.ended_at = utc_now()

    def record_context(
        self, snapshot: ContextSnapshot, sample_interval: int = 10
    ) -> None:
        """
        Record a context snapshot for analytics.

        Args:
            snapshot: ContextSnapshot to record
            sample_interval: Only store every Nth snapshot to avoid bloat

        Updates:
            - peak_context_tokens if current exceeds previous peak
            - total_tokens_generated from cumulative output
            - total_cost_usd from snapshot
            - context_by_feature if feature_id is set
            - context_snapshots (sampled)
        """
        # Update peak context
        current_tokens = snapshot.current_tokens
        if current_tokens > self.peak_context_tokens:
            self.peak_context_tokens = current_tokens

        # Update totals
        self.total_tokens_generated = snapshot.total_output_tokens
        self.total_cost_usd = snapshot.cost_usd

        # Track context by feature
        if snapshot.feature_id:
            prev = self.context_by_feature.get(snapshot.feature_id, 0)
            # Use delta from last snapshot with same feature
            self.context_by_feature[snapshot.feature_id] = max(prev, current_tokens)

        # Sample snapshots to avoid bloat (every Nth or on significant events)
        should_sample = (
            len(self.context_snapshots) == 0
            or len(self.context_snapshots) % sample_interval == 0
            or snapshot.trigger in ("session_start", "session_end", "feature_switch")
            or current_tokens > self.peak_context_tokens * 0.9  # Near peak
        )

        if should_sample:
            self.context_snapshots.append(snapshot)

    def context_stats(self) -> dict:
        """
        Get context usage statistics for this session.

        Returns:
            Dictionary with context usage metrics
        """
        if not self.context_snapshots:
            return {
                "peak_tokens": self.peak_context_tokens,
                "total_output": self.total_tokens_generated,
                "total_cost": self.total_cost_usd,
                "by_feature": self.context_by_feature,
                "snapshots": 0,
            }

        # Calculate averages and trends
        tokens_over_time = [s.current_tokens for s in self.context_snapshots]
        avg_tokens = (
            sum(tokens_over_time) / len(tokens_over_time) if tokens_over_time else 0
        )

        return {
            "peak_tokens": self.peak_context_tokens,
            "avg_tokens": int(avg_tokens),
            "total_output": self.total_tokens_generated,
            "total_cost": self.total_cost_usd,
            "by_feature": self.context_by_feature,
            "snapshots": len(self.context_snapshots),
            "peak_percent": (self.peak_context_tokens / 200000) * 100
            if self.context_snapshots
            else 0,
        }

    def get_events(
        self,
        limit: int | None = 100,
        offset: int = 0,
        events_dir: str = ".htmlgraph/events",
    ) -> list[dict]:
        """
        Get events for this session from JSONL event log.

        Args:
            limit: Maximum number of events to return (None = all)
            offset: Number of events to skip from start
            events_dir: Path to events directory

        Returns:
            List of event dictionaries, oldest first

        Example:
            >>> session = sdk.sessions.get("session-123")
            >>> recent_events = session.get_events(limit=10)
            >>> for evt in recent_events:
            ...     print(f"{evt['event_id']}: {evt['tool']}")
        """
        from htmlgraph.event_log import JsonlEventLog

        event_log = JsonlEventLog(events_dir)
        return event_log.get_session_events(self.id, limit=limit, offset=offset)

    def query_events(
        self,
        tool: str | None = None,
        feature_id: str | None = None,
        since: Any = None,
        limit: int | None = 100,
        events_dir: str = ".htmlgraph/events",
    ) -> list[dict]:
        """
        Query events for this session with filters.

        Args:
            tool: Filter by tool name (e.g., 'Bash', 'Edit')
            feature_id: Filter by attributed feature ID
            since: Only events after this timestamp
            limit: Maximum number of events (newest first)
            events_dir: Path to events directory

        Returns:
            List of matching event dictionaries, newest first

        Example:
            >>> session = sdk.sessions.get("session-123")
            >>> bash_events = session.query_events(tool='Bash', limit=20)
            >>> feature_events = session.query_events(feature_id='feat-123')
        """
        from htmlgraph.event_log import JsonlEventLog

        event_log = JsonlEventLog(events_dir)
        return event_log.query_events(
            session_id=self.id,
            tool=tool,
            feature_id=feature_id,
            since=since,
            limit=limit,
        )

    def event_stats(self, events_dir: str = ".htmlgraph/events") -> dict:
        """
        Get event statistics for this session.

        Returns:
            Dictionary with event counts by tool and feature

        Example:
            >>> session = sdk.sessions.get("session-123")
            >>> stats = session.event_stats()
            >>> print(f"Bash commands: {stats['by_tool']['Bash']}")
            >>> print(f"Total features: {len(stats['by_feature'])}")
        """
        events = self.get_events(limit=None, events_dir=events_dir)

        by_tool: dict[str, int] = {}
        by_feature: dict[str, int] = {}

        for evt in events:
            # Count by tool
            tool = evt.get("tool", "Unknown")
            by_tool[tool] = by_tool.get(tool, 0) + 1

            # Count by feature
            feature = evt.get("feature_id")
            if feature:
                by_feature[feature] = by_feature.get(feature, 0) + 1

        return {
            "total_events": len(events),
            "by_tool": by_tool,
            "by_feature": by_feature,
            "tools_used": len(by_tool),
            "features_worked": len(by_feature),
        }

    def calculate_work_breakdown(
        self, events_dir: str = ".htmlgraph/events"
    ) -> dict[str, int]:
        """
        Calculate distribution of work types from events.

        Returns:
            Dictionary mapping work type to event count

        Example:
            >>> session = sdk.sessions.get("session-123")
            >>> breakdown = session.calculate_work_breakdown()
            >>> print(breakdown)
            {"feature-implementation": 120, "spike-investigation": 45, "maintenance": 30}
        """
        events = self.get_events(limit=None, events_dir=events_dir)
        breakdown: dict[str, int] = {}

        for evt in events:
            work_type = evt.get("work_type")
            if work_type:
                breakdown[work_type] = breakdown.get(work_type, 0) + 1

        return breakdown

    def calculate_primary_work_type(
        self, events_dir: str = ".htmlgraph/events"
    ) -> str | None:
        """
        Determine primary work type based on event distribution.

        Returns work type with most events, or None if no work types recorded.

        Example:
            >>> session = sdk.sessions.get("session-123")
            >>> primary = session.calculate_primary_work_type()
            >>> print(primary)
            "feature-implementation"
        """
        breakdown = self.calculate_work_breakdown(events_dir=events_dir)
        if not breakdown:
            return None

        # Return work type with most events
        return max(breakdown, key=breakdown.get)  # type: ignore

    def cleanup_missing_references(self, graph_dir: str | Path) -> dict[str, Any]:
        """
        Remove references to deleted/missing work items from worked_on list.

        This fixes session data integrity issues where worked_on contains IDs
        that no longer exist (deleted spikes, removed features, etc.).

        Args:
            graph_dir: Path to .htmlgraph directory

        Returns:
            Dict with cleanup statistics: {
                "removed": [...],  # List of removed IDs
                "kept": [...],     # List of valid IDs that were kept
                "removed_count": int,
                "kept_count": int
            }
        """
        graph_path = Path(graph_dir)
        removed = []
        kept = []

        # Check each work item in worked_on
        for item_id in self.worked_on:
            subdir = _work_item_subdir(item_id)
            file_path = graph_path / subdir / f"{item_id}.html"

            # Check if file exists
            if file_path.exists():
                kept.append(item_id)
            else:
                removed.append(item_id)

        # Update worked_on with only valid references
        self.worked_on = kept

        return {
            "removed": removed,
            "kept": kept,
            "removed_count": len(removed),
            "kept_count": len(kept),
        }

    def to_html(self, stylesheet_path: str = "../styles.css") -> str:
        """Convert session to HTML document with inline activity log."""
        # Build edges HTML for worked_on features
        edges_html = ""
        if self.worked_on or self.continued_from:
            edge_sections = []

            if self.worked_on:
                feature_links = "\n                    ".join(
                    f'<li><a href="../{_work_item_subdir(fid)}/{fid}.html" data-relationship="worked-on">{fid}</a></li>'
                    for fid in self.worked_on
                )
                edge_sections.append(f"""
            <section data-edge-type="worked-on">
                <h3>Worked On:</h3>
                <ul>
                    {feature_links}
                </ul>
            </section>""")

            if self.continued_from:
                edge_sections.append(f'''
            <section data-edge-type="continued-from">
                <h3>Continued From:</h3>
                <ul>
                    <li><a href="{self.continued_from}.html" data-relationship="continued-from">{self.continued_from}</a></li>
                </ul>
            </section>''')

            edges_html = f"""
        <nav data-graph-edges>{"".join(edge_sections)}
        </nav>"""

        # Build handoff HTML
        handoff_html = ""
        if (
            self.handoff_notes
            or self.recommended_next
            or self.blockers
            or self.recommended_context
        ):
            handoff_section = """
        <section data-handoff>
            <h3>Handoff Context</h3>"""

            if self.handoff_notes:
                handoff_section += f"\n            <p data-handoff-notes><strong>Notes:</strong> {self.handoff_notes}</p>"

            if self.recommended_next:
                handoff_section += f"\n            <p data-recommended-next><strong>Recommended Next:</strong> {self.recommended_next}</p>"

            if self.blockers:
                blockers_items = "\n                ".join(
                    f"<li>{blocker}</li>" for blocker in self.blockers
                )
                handoff_section += f"""
            <div data-blockers>
                <strong>Blockers:</strong>
                <ul>
                    {blockers_items}
                </ul>
            </div>"""

            if self.recommended_context:
                context_items = "\n                ".join(
                    f"<li>{file_path}</li>" for file_path in self.recommended_context
                )
                handoff_section += f"""
            <div data-recommended-context>
                <strong>Recommended Context:</strong>
                <ul>
                    {context_items}
                </ul>
            </div>"""

            handoff_section += "\n        </section>"
            handoff_html = handoff_section

        # Build activity log HTML
        activity_html = ""
        if self.activity_log:
            # Show most recent first (reversed)
            # NOTE: Previously limited to last 100 entries, but this caused data loss
            # for pattern detection and analytics. Now stores all entries.
            log_items = "\n                ".join(
                entry.to_html()
                for entry in reversed(self.activity_log)  # All entries
            )
            activity_html = f"""
        <section data-activity-log>
            <h3>Activity Log ({self.event_count} events)</h3>
            <ol reversed>
                {log_items}
            </ol>
        </section>"""

        # Build attributes
        subagent_attr = f' data-is-subagent="{str(self.is_subagent).lower()}"'
        commit_attr = (
            f' data-start-commit="{self.start_commit}"' if self.start_commit else ""
        )
        ended_attr = (
            f' data-ended-at="{self.ended_at.isoformat()}"' if self.ended_at else ""
        )
        primary_work_type_attr = (
            f' data-primary-work-type="{self.primary_work_type}"'
            if self.primary_work_type
            else ""
        )
        # Parent session attributes
        parent_session_attrs = ""
        if self.parent_session:
            parent_session_attrs += f' data-parent-session="{self.parent_session}"'
        if self.parent_activity:
            parent_session_attrs += f' data-parent-activity="{self.parent_activity}"'
        if self.nesting_depth > 0:
            parent_session_attrs += f' data-nesting-depth="{self.nesting_depth}"'

        # Serialize work_breakdown as JSON if present
        import json

        work_breakdown_attr = ""
        if self.work_breakdown:
            work_breakdown_json = json.dumps(self.work_breakdown)
            work_breakdown_attr = f" data-work-breakdown='{work_breakdown_json}'"

        # Context tracking attributes
        context_attrs = ""
        if self.peak_context_tokens > 0:
            context_attrs += f' data-peak-context="{self.peak_context_tokens}"'
        if self.total_tokens_generated > 0:
            context_attrs += f' data-total-output="{self.total_tokens_generated}"'
        if self.total_cost_usd > 0:
            context_attrs += f' data-total-cost="{self.total_cost_usd:.4f}"'
        if self.context_by_feature:
            context_by_feature_json = json.dumps(self.context_by_feature)
            context_attrs += f" data-context-by-feature='{context_by_feature_json}'"

        # Transcript integration attributes
        transcript_attrs = ""
        if self.transcript_id:
            transcript_attrs += f' data-transcript-id="{self.transcript_id}"'
        if self.transcript_path:
            transcript_attrs += f' data-transcript-path="{self.transcript_path}"'
        if self.transcript_synced_at:
            transcript_attrs += (
                f' data-transcript-synced="{self.transcript_synced_at.isoformat()}"'
            )
        if self.transcript_git_branch:
            transcript_attrs += (
                f' data-transcript-branch="{self.transcript_git_branch}"'
            )

        # Build context summary section
        context_html = ""
        if self.peak_context_tokens > 0 or self.context_snapshots:
            context_html = f"""
        <section data-context-tracking>
            <h3>Context Usage</h3>
            <dl>
                <dt>Peak Context</dt>
                <dd>{self.peak_context_tokens:,} tokens ({self.peak_context_tokens * 100 // 200000}%)</dd>
                <dt>Total Output</dt>
                <dd>{self.total_tokens_generated:,} tokens</dd>
                <dt>Total Cost</dt>
                <dd>${self.total_cost_usd:.4f}</dd>
                <dt>Snapshots</dt>
                <dd>{len(self.context_snapshots)}</dd>
            </dl>
        </section>"""

        # Build detected patterns section
        patterns_html = ""
        if self.detected_patterns:
            patterns_html = f"""
        <section data-detected-patterns>
            <h3>Detected Patterns ({len(self.detected_patterns)})</h3>
            <table class="patterns-table">
                <thead>
                    <tr>
                        <th>Sequence</th>
                        <th>Type</th>
                        <th>Count</th>
                        <th>First/Last Detected</th>
                    </tr>
                </thead>
                <tbody>"""

            for pattern in self.detected_patterns:
                seq_str = " → ".join(pattern.get("sequence", []))
                pattern_type = pattern.get("pattern_type", "neutral")
                count = pattern.get("detection_count", 0)
                first = pattern.get("first_detected", "")
                last = pattern.get("last_detected", "")

                patterns_html += f"""
                    <tr data-pattern-type="{pattern_type}">
                        <td class="sequence">{seq_str}</td>
                        <td><span class="badge pattern-{pattern_type}">{pattern_type}</span></td>
                        <td>{count}</td>
                        <td>{first} / {last}</td>
                    </tr>"""

            patterns_html += """
                </tbody>
            </table>
        </section>"""

        # Build error log section
        error_html = ""
        if self.error_log:
            error_items = "\n                ".join(
                error.to_html() for error in self.error_log
            )
            error_html = f"""
        <section data-error-log>
            <h3>Errors ({len(self.error_log)})</h3>
            <div class="error-log">
                {error_items}
            </div>
            <style>
                .error-item {{ margin: 10px 0; padding: 10px; border-left: 3px solid #ff6b6b; }}
                .error-type {{ font-weight: bold; color: #ff6b6b; }}
                .traceback {{ background: #f5f5f5; padding: 10px; overflow-x: auto; font-size: 0.9em; margin-top: 5px; }}
            </style>
        </section>"""

        title = self.title or f"Session {self.id}"

        return f'''<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <meta name="htmlgraph-version" content="1.0">
    <title>{title}</title>
    <link rel="stylesheet" href="{stylesheet_path}">
</head>
<body>
    <article id="{self.id}"
             data-type="session"
             data-status="{self.status}"
             data-agent="{self.agent}"
             data-started-at="{self.started_at.isoformat()}"
             data-last-activity="{self.last_activity.isoformat()}"
             data-event-count="{self.event_count}"{subagent_attr}{commit_attr}{ended_attr}{primary_work_type_attr}{work_breakdown_attr}{context_attrs}{transcript_attrs}{parent_session_attrs}>

        <header>
            <h1>{title}</h1>
            <div class="metadata">
                <span class="badge status-{self.status}">{self.status.title()}</span>
                <span class="badge">{self.agent}</span>
                <span class="badge">{self.event_count} events</span>
            </div>
        </header>
{edges_html}{handoff_html}{context_html}{error_html}{patterns_html}{activity_html}
    </article>
</body>
</html>
'''

    def to_context(self) -> str:
        """Generate lightweight context for AI agents."""
        lines = [f"# Session: {self.id}"]
        lines.append(f"Status: {self.status} | Agent: {self.agent}")
        lines.append(f"Started: {self.started_at.strftime('%Y-%m-%d %H:%M')}")
        lines.append(f"Events: {self.event_count}")

        if self.worked_on:
            lines.append(f"Worked on: {', '.join(self.worked_on)}")

        if self.handoff_notes or self.recommended_next or self.blockers:
            lines.append("\nHandoff:")
            if self.handoff_notes:
                lines.append(f"  Notes: {self.handoff_notes}")
            if self.recommended_next:
                lines.append(f"  Recommended next: {self.recommended_next}")
            if self.blockers:
                lines.append(f"  Blockers: {', '.join(self.blockers)}")

        # Last 5 activities
        if self.activity_log:
            lines.append("\nRecent activity:")
            for entry in self.activity_log[-5:]:
                lines.append(f"  {entry.to_context()}")

        return "\n".join(lines)

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "Session":
        """Create a Session from a dictionary."""
        if "activity_log" in data:
            data["activity_log"] = [
                ActivityEntry(**e) if isinstance(e, dict) else e
                for e in data["activity_log"]
            ]
        if "context_snapshots" in data:
            data["context_snapshots"] = [
                ContextSnapshot.from_dict(s) if isinstance(s, dict) else s
                for s in data["context_snapshots"]
            ]
        return cls(**data)
