"""
Pydantic models for HtmlGraph nodes, edges, and steps.

These models provide:
- Schema validation for graph data
- HTML serialization/deserialization
- Lightweight context generation for AI agents
"""

from datetime import datetime
from typing import Any, Literal
from enum import Enum
from pydantic import BaseModel, Field


class WorkType(str, Enum):
    """
    Classification of work/activity type for events and sessions.

    Used to differentiate exploratory work from implementation work in analytics.
    """
    FEATURE = "feature-implementation"
    SPIKE = "spike-investigation"
    BUG_FIX = "bug-fix"
    MAINTENANCE = "maintenance"
    DOCUMENTATION = "documentation"
    PLANNING = "planning"
    REVIEW = "review"
    ADMIN = "admin"


class SpikeType(str, Enum):
    """
    Categorization of spike investigations based on Agile best practices.

    - TECHNICAL: Investigate technical implementation options
    - ARCHITECTURAL: Research system design and architecture decisions
    - RISK: Identify and assess project risks
    - GENERAL: Uncategorized investigation
    """
    TECHNICAL = "technical"
    ARCHITECTURAL = "architectural"
    RISK = "risk"
    GENERAL = "general"


class MaintenanceType(str, Enum):
    """
    Software maintenance categorization based on IEEE standards.

    - CORRECTIVE: Fix defects and errors
    - ADAPTIVE: Adapt to environment changes (OS, dependencies)
    - PERFECTIVE: Improve performance, usability, maintainability
    - PREVENTIVE: Prevent future problems (refactoring, tech debt)
    """
    CORRECTIVE = "corrective"
    ADAPTIVE = "adaptive"
    PERFECTIVE = "perfective"
    PREVENTIVE = "preventive"


class Step(BaseModel):
    """An implementation step within a node (e.g., task checklist item)."""

    description: str
    completed: bool = False
    agent: str | None = None
    timestamp: datetime | None = None

    def to_html(self) -> str:
        """Convert step to HTML list item."""
        status = "✅" if self.completed else "⏳"
        agent_attr = f' data-agent="{self.agent}"' if self.agent else ""
        completed_attr = f' data-completed="{str(self.completed).lower()}"'
        return f'<li{completed_attr}{agent_attr}>{status} {self.description}</li>'

    def to_context(self) -> str:
        """Lightweight context for AI agents."""
        status = "[x]" if self.completed else "[ ]"
        return f"{status} {self.description}"

    def __getitem__(self, key: str) -> Any:
        """
        Backwards-compatible dict-style access for tests/consumers that treat
        steps as mappings (e.g. step['completed']).
        """
        return getattr(self, key)


class Edge(BaseModel):
    """A graph edge representing a relationship between nodes."""

    target_id: str
    relationship: str = "related"
    title: str | None = None
    since: datetime | None = None
    properties: dict[str, Any] = Field(default_factory=dict)

    def to_html(self, base_path: str = "") -> str:
        """Convert edge to HTML anchor element."""
        href = f"{base_path}{self.target_id}.html" if not self.target_id.endswith('.html') else f"{base_path}{self.target_id}"
        attrs = [f'href="{href}"', f'data-relationship="{self.relationship}"']

        if self.since:
            attrs.append(f'data-since="{self.since.isoformat()}"')

        for key, value in self.properties.items():
            attrs.append(f'data-{key}="{value}"')

        title = self.title or self.target_id
        return f'<a {" ".join(attrs)}>{title}</a>'

    def to_context(self) -> str:
        """Lightweight context for AI agents."""
        return f"→ {self.relationship}: {self.title or self.target_id}"


class Node(BaseModel):
    """
    A graph node representing an HTML file.

    Attributes:
        id: Unique identifier for the node
        title: Human-readable title
        type: Node type (feature, task, note, session, etc.)
        status: Current status (todo, in-progress, blocked, done)
        priority: Priority level (low, medium, high, critical)
        created: Creation timestamp
        updated: Last modification timestamp
        properties: Arbitrary key-value properties
        edges: Relationships to other nodes, keyed by relationship type
        steps: Implementation steps/checklist
        content: Main content/description
        agent_assigned: Agent currently working on this node
    """

    id: str
    title: str
    type: str = "node"
    status: Literal["todo", "in-progress", "blocked", "done", "active", "ended", "stale"] = "todo"
    priority: Literal["low", "medium", "high", "critical"] = "medium"
    created: datetime = Field(default_factory=datetime.now)
    updated: datetime = Field(default_factory=datetime.now)

    properties: dict[str, Any] = Field(default_factory=dict)
    edges: dict[str, list[Edge]] = Field(default_factory=dict)
    steps: list[Step] = Field(default_factory=list)
    content: str = ""
    agent_assigned: str | None = None
    claimed_at: datetime | None = None
    claimed_by_session: str | None = None

    # Vertical integration: Track/Spec/Plan relationships
    track_id: str | None = None  # Which track this feature belongs to
    plan_task_id: str | None = None  # Which plan task this feature implements
    spec_requirements: list[str] = Field(default_factory=list)  # Which spec requirements this satisfies

    # Handoff context fields for agent-to-agent transitions
    handoff_required: bool = False  # Whether this node needs to be handed off
    previous_agent: str | None = None  # Agent who previously worked on this
    handoff_reason: str | None = None  # Reason for handoff (e.g., blocked, requires different expertise)
    handoff_notes: str | None = None  # Detailed handoff context/decisions
    handoff_timestamp: datetime | None = None  # When the handoff was created

    def model_post_init(self, __context: Any) -> None:
        """Lightweight validation for required fields."""
        if not self.id or not str(self.id).strip():
            raise ValueError("Node.id must be non-empty")
        if not self.title or not str(self.title).strip():
            raise ValueError("Node.title must be non-empty")

    @property
    def completion_percentage(self) -> int:
        """Calculate completion percentage from steps."""
        if not self.steps:
            return 100 if self.status == "done" else 0
        completed = sum(1 for s in self.steps if s.completed)
        return int((completed / len(self.steps)) * 100)

    @property
    def next_step(self) -> Step | None:
        """Get the next incomplete step."""
        for step in self.steps:
            if not step.completed:
                return step
        return None

    @property
    def blocking_edges(self) -> list[Edge]:
        """Get edges that are blocking this node."""
        return self.edges.get("blocked_by", []) + self.edges.get("blocks", [])

    def get_edges_by_type(self, relationship: str) -> list[Edge]:
        """Get all edges of a specific relationship type."""
        return self.edges.get(relationship, [])

    def add_edge(self, edge: Edge) -> None:
        """Add an edge to this node."""
        if edge.relationship not in self.edges:
            self.edges[edge.relationship] = []
        self.edges[edge.relationship].append(edge)
        self.updated = datetime.now()

    def complete_step(self, index: int, agent: str | None = None) -> bool:
        """Mark a step as completed."""
        if 0 <= index < len(self.steps):
            self.steps[index].completed = True
            self.steps[index].agent = agent
            self.steps[index].timestamp = datetime.now()
            self.updated = datetime.now()
            return True
        return False

    def to_html(self, stylesheet_path: str = "../styles.css") -> str:
        """
        Convert node to full HTML document.

        Args:
            stylesheet_path: Relative path to CSS stylesheet

        Returns:
            Complete HTML document as string
        """
        # Build edges HTML
        edges_html = ""
        if self.edges:
            edge_sections = []
            for rel_type, edge_list in self.edges.items():
                if edge_list:
                    edge_items = "\n                    ".join(
                        f"<li>{edge.to_html()}</li>" for edge in edge_list
                    )
                    edge_sections.append(f'''
            <section data-edge-type="{rel_type}">
                <h3>{rel_type.replace("_", " ").title()}:</h3>
                <ul>
                    {edge_items}
                </ul>
            </section>''')
            if edge_sections:
                edges_html = f'''
        <nav data-graph-edges>{"".join(edge_sections)}
        </nav>'''

        # Build steps HTML
        steps_html = ""
        if self.steps:
            step_items = "\n                ".join(step.to_html() for step in self.steps)
            steps_html = f'''
        <section data-steps>
            <h3>Implementation Steps</h3>
            <ol>
                {step_items}
            </ol>
        </section>'''

        # Build properties HTML
        props_html = ""
        if self.properties:
            prop_items = []
            for key, value in self.properties.items():
                unit = ""
                if isinstance(value, dict) and "value" in value:
                    unit = f' data-unit="{value.get("unit", "")}"' if value.get("unit") else ""
                    display = f'{value["value"]} {value.get("unit", "")}'.strip()
                    val = value["value"]
                else:
                    display = str(value)
                    val = value
                prop_items.append(
                    f'<dt>{key.replace("_", " ").title()}</dt>\n'
                    f'                <dd data-key="{key}" data-value="{val}"{unit}>{display}</dd>'
                )
            props_html = f'''
        <section data-properties>
            <h3>Properties</h3>
            <dl>
                {chr(10).join(prop_items)}
            </dl>
        </section>'''

        # Build handoff HTML
        handoff_html = ""
        if self.handoff_required or self.previous_agent:
            handoff_attrs = []
            if self.previous_agent:
                handoff_attrs.append(f'data-previous-agent="{self.previous_agent}"')
            if self.handoff_reason:
                handoff_attrs.append(f'data-reason="{self.handoff_reason}"')
            if self.handoff_timestamp:
                handoff_attrs.append(f'data-timestamp="{self.handoff_timestamp.isoformat()}"')

            attrs_str = " ".join(handoff_attrs)
            handoff_section = f'''
        <section data-handoff{f" {attrs_str}" if attrs_str else ""}>
            <h3>Handoff Context</h3>'''

            if self.previous_agent:
                handoff_section += f'\n            <p><strong>From:</strong> {self.previous_agent}</p>'

            if self.handoff_reason:
                handoff_section += f'\n            <p><strong>Reason:</strong> {self.handoff_reason}</p>'

            if self.handoff_notes:
                handoff_section += f'\n            <p><strong>Notes:</strong> {self.handoff_notes}</p>'

            handoff_section += '\n        </section>'
            handoff_html = handoff_section

        # Build content HTML
        content_html = ""
        if self.content:
            content_html = f'''
        <section data-content>
            <h3>Description</h3>
            {self.content}
        </section>'''

        # Agent attribute
        agent_attr = f' data-agent-assigned="{self.agent_assigned}"' if self.agent_assigned else ""
        if self.claimed_at:
            agent_attr += f' data-claimed-at="{self.claimed_at.isoformat()}"'
        if self.claimed_by_session:
            agent_attr += f' data-claimed-by-session="{self.claimed_by_session}"'

        # Track ID attribute
        track_attr = f' data-track-id="{self.track_id}"' if self.track_id else ""

        return f'''<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <meta name="htmlgraph-version" content="1.0">
    <title>{self.title}</title>
    <link rel="stylesheet" href="{stylesheet_path}">
</head>
<body>
    <article id="{self.id}"
             data-type="{self.type}"
             data-status="{self.status}"
             data-priority="{self.priority}"
             data-created="{self.created.isoformat()}"
             data-updated="{self.updated.isoformat()}"{agent_attr}{track_attr}>

        <header>
            <h1>{self.title}</h1>
            <div class="metadata">
                <span class="badge status-{self.status}">{self.status.replace("-", " ").title()}</span>
                <span class="badge priority-{self.priority}">{self.priority.title()} Priority</span>
            </div>
        </header>
{edges_html}{handoff_html}{props_html}{steps_html}{content_html}
    </article>
</body>
</html>
'''

    def to_context(self) -> str:
        """
        Generate lightweight context for AI agents.

        Returns ~50-100 tokens with essential information:
        - Node ID and title
        - Status and priority
        - Progress (if steps exist)
        - Blocking dependencies
        - Next action
        """
        lines = [f"# {self.id}: {self.title}"]
        lines.append(f"Status: {self.status} | Priority: {self.priority}")

        if self.agent_assigned:
            lines.append(f"Assigned: {self.agent_assigned}")

        # Handoff context
        if self.handoff_required or self.previous_agent:
            handoff_info = "🔄 Handoff:"
            if self.previous_agent:
                handoff_info += f" from {self.previous_agent}"
            if self.handoff_reason:
                handoff_info += f" ({self.handoff_reason})"
            lines.append(handoff_info)
            if self.handoff_notes:
                lines.append(f"   Notes: {self.handoff_notes}")

        if self.steps:
            completed = sum(1 for s in self.steps if s.completed)
            lines.append(f"Progress: {completed}/{len(self.steps)} steps ({self.completion_percentage}%)")

        # Blocking dependencies
        blocked_by = self.edges.get("blocked_by", [])
        if blocked_by:
            blockers = ", ".join(e.title or e.target_id for e in blocked_by)
            lines.append(f"⚠️  Blocked by: {blockers}")

        # Next step
        if self.next_step:
            lines.append(f"Next: {self.next_step.description}")

        return "\n".join(lines)

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "Node":
        """Create a Node from a dictionary, handling nested objects."""
        # Convert edge dicts to Edge objects
        if "edges" in data:
            edges = {}
            for rel_type, edge_list in data["edges"].items():
                edges[rel_type] = [
                    Edge(**e) if isinstance(e, dict) else e
                    for e in edge_list
                ]
            data["edges"] = edges

        # Convert step dicts to Step objects
        if "steps" in data:
            data["steps"] = [
                Step(**s) if isinstance(s, dict) else s
                for s in data["steps"]
            ]

        return cls(**data)


class Spike(Node):
    """
    A Spike node representing timeboxed investigation/research work.

    Extends Node with spike-specific fields:
    - spike_type: Classification (technical/architectural/risk)
    - timebox_hours: Time budget for investigation
    - findings: Summary of what was learned
    - decision: Decision made based on spike results
    """

    spike_type: SpikeType = SpikeType.GENERAL
    timebox_hours: int | None = None
    findings: str | None = None
    decision: str | None = None

    def __init__(self, **data: Any):
        # Ensure type is always "spike"
        data["type"] = "spike"
        super().__init__(**data)


class Chore(Node):
    """
    A Chore node representing maintenance work.

    Extends Node with maintenance-specific fields:
    - maintenance_type: Classification (corrective/adaptive/perfective/preventive)
    - technical_debt_score: Estimated tech debt impact (0-10)
    """

    maintenance_type: MaintenanceType | None = None
    technical_debt_score: int | None = None

    def __init__(self, **data: Any):
        # Ensure type is always "chore"
        data["type"] = "chore"
        super().__init__(**data)


class ActivityEntry(BaseModel):
    """
    A lightweight activity log entry for high-frequency events.

    Stored inline within Session nodes to avoid file explosion.
    """

    id: str | None = None  # Optional event ID for deduplication
    timestamp: datetime = Field(default_factory=datetime.now)
    tool: str  # Edit, Bash, Read, Write, Grep, Glob, Task, UserQuery, etc.
    summary: str  # Human-readable summary (e.g., "Edit: src/auth/login.py:45-52")
    success: bool = True
    feature_id: str | None = None  # Link to feature this activity belongs to
    drift_score: float | None = None  # 0.0-1.0 alignment score
    parent_activity_id: str | None = None  # Link to parent activity (e.g., Skill invocation)
    payload: dict[str, Any] | None = None  # Optional rich payload for significant events

    def to_html(self) -> str:
        """Convert activity to HTML list item."""
        attrs = [
            f'data-ts="{self.timestamp.isoformat()}"',
            f'data-tool="{self.tool}"',
            f'data-success="{str(self.success).lower()}"',
        ]
        if self.id:
            attrs.append(f'data-event-id="{self.id}"')
        if self.feature_id:
            attrs.append(f'data-feature="{self.feature_id}"')
        if self.drift_score is not None:
            attrs.append(f'data-drift="{self.drift_score:.2f}"')
        if self.parent_activity_id:
            attrs.append(f'data-parent="{self.parent_activity_id}"')

        return f'<li {" ".join(attrs)}>{self.summary}</li>'

    def to_context(self) -> str:
        """Lightweight context for AI agents."""
        status = "✓" if self.success else "✗"
        return f"[{self.timestamp.strftime('%H:%M:%S')}] {status} {self.tool}: {self.summary}"


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
    event_count: int = 0

    # Relationships
    worked_on: list[str] = Field(default_factory=list)  # Feature IDs
    continued_from: str | None = None  # Previous session ID

    # High-frequency activity log
    activity_log: list[ActivityEntry] = Field(default_factory=list)

    # Work type categorization (Phase 1: Work Type Classification)
    primary_work_type: str | None = None  # WorkType enum value
    work_breakdown: dict[str, int] | None = None  # {work_type: event_count}

    def add_activity(self, entry: ActivityEntry) -> None:
        """Add an activity entry to the log."""
        self.activity_log.append(entry)
        self.event_count += 1
        self.last_activity = datetime.now()

        # Track features worked on
        if entry.feature_id and entry.feature_id not in self.worked_on:
            self.worked_on.append(entry.feature_id)

    def end(self) -> None:
        """Mark session as ended."""
        self.status = "ended"
        self.ended_at = datetime.now()

    def get_events(
        self,
        limit: int | None = 100,
        offset: int = 0,
        events_dir: str = ".htmlgraph/events"
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
        events_dir: str = ".htmlgraph/events"
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
            limit=limit
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

        by_tool = {}
        by_feature = {}

        for evt in events:
            # Count by tool
            tool = evt.get('tool', 'Unknown')
            by_tool[tool] = by_tool.get(tool, 0) + 1

            # Count by feature
            feature = evt.get('feature_id')
            if feature:
                by_feature[feature] = by_feature.get(feature, 0) + 1

        return {
            'total_events': len(events),
            'by_tool': by_tool,
            'by_feature': by_feature,
            'tools_used': len(by_tool),
            'features_worked': len(by_feature)
        }

    def calculate_work_breakdown(self, events_dir: str = ".htmlgraph/events") -> dict[str, int]:
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

    def calculate_primary_work_type(self, events_dir: str = ".htmlgraph/events") -> str | None:
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

    def to_html(self, stylesheet_path: str = "../styles.css") -> str:
        """Convert session to HTML document with inline activity log."""
        # Build edges HTML for worked_on features
        edges_html = ""
        if self.worked_on or self.continued_from:
            edge_sections = []

            if self.worked_on:
                feature_links = "\n                    ".join(
                    f'<li><a href="../features/{fid}.html" data-relationship="worked-on">{fid}</a></li>'
                    for fid in self.worked_on
                )
                edge_sections.append(f'''
            <section data-edge-type="worked-on">
                <h3>Worked On:</h3>
                <ul>
                    {feature_links}
                </ul>
            </section>''')

            if self.continued_from:
                edge_sections.append(f'''
            <section data-edge-type="continued-from">
                <h3>Continued From:</h3>
                <ul>
                    <li><a href="{self.continued_from}.html" data-relationship="continued-from">{self.continued_from}</a></li>
                </ul>
            </section>''')

            edges_html = f'''
        <nav data-graph-edges>{"".join(edge_sections)}
        </nav>'''

        # Build activity log HTML
        activity_html = ""
        if self.activity_log:
            # Show most recent first (reversed)
            log_items = "\n                ".join(
                entry.to_html() for entry in reversed(self.activity_log[-100:])  # Last 100 entries
            )
            activity_html = f'''
        <section data-activity-log>
            <h3>Activity Log ({self.event_count} events)</h3>
            <ol reversed>
                {log_items}
            </ol>
        </section>'''

        # Build attributes
        subagent_attr = f' data-is-subagent="{str(self.is_subagent).lower()}"'
        commit_attr = f' data-start-commit="{self.start_commit}"' if self.start_commit else ""
        ended_attr = f' data-ended-at="{self.ended_at.isoformat()}"' if self.ended_at else ""
        primary_work_type_attr = f' data-primary-work-type="{self.primary_work_type}"' if self.primary_work_type else ""

        # Serialize work_breakdown as JSON if present
        import json
        work_breakdown_attr = ""
        if self.work_breakdown:
            work_breakdown_json = json.dumps(self.work_breakdown)
            work_breakdown_attr = f' data-work-breakdown=\'{work_breakdown_json}\''

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
             data-event-count="{self.event_count}"{subagent_attr}{commit_attr}{ended_attr}{primary_work_type_attr}{work_breakdown_attr}>

        <header>
            <h1>{title}</h1>
            <div class="metadata">
                <span class="badge status-{self.status}">{self.status.title()}</span>
                <span class="badge">{self.agent}</span>
                <span class="badge">{self.event_count} events</span>
            </div>
        </header>
{edges_html}{activity_html}
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
        return cls(**data)


class Graph(BaseModel):
    """
    A collection of nodes representing the full graph.

    This is primarily used for in-memory operations and serialization.
    For file-based operations, use HtmlGraph class instead.
    """

    nodes: dict[str, Node] = Field(default_factory=dict)

    def add(self, node: Node) -> None:
        """Add a node to the graph."""
        self.nodes[node.id] = node

    def get(self, node_id: str) -> Node | None:
        """Get a node by ID."""
        return self.nodes.get(node_id)

    def remove(self, node_id: str) -> bool:
        """Remove a node from the graph."""
        if node_id in self.nodes:
            del self.nodes[node_id]
            return True
        return False

    def all_edges(self) -> list[tuple[str, Edge]]:
        """Get all edges in the graph as (source_id, edge) tuples."""
        result = []
        for node_id, node in self.nodes.items():
            for edges in node.edges.values():
                for edge in edges:
                    result.append((node_id, edge))
        return result

    def to_context(self) -> str:
        """Generate lightweight context for all nodes."""
        return "\n\n".join(node.to_context() for node in self.nodes.values())
