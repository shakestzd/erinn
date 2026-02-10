"""
Pydantic models for HtmlGraph nodes, edges, and steps.

These models provide:
- Schema validation for graph data
- HTML serialization/deserialization
- Lightweight context generation for AI agents
"""

from datetime import datetime, timezone
from enum import Enum
from pathlib import Path
from typing import Any, Literal

from pydantic import BaseModel, Field


def utc_now() -> datetime:
    """Return current time as UTC-aware datetime."""
    return datetime.now(timezone.utc)


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
        return f"<li{completed_attr}{agent_attr}>{status} {self.description}</li>"

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
        href = (
            f"{base_path}{self.target_id}.html"
            if not self.target_id.endswith(".html")
            else f"{base_path}{self.target_id}"
        )
        attrs = [f'href="{href}"', f'data-relationship="{self.relationship}"']

        if self.since:
            attrs.append(f'data-since="{self.since.isoformat()}"')

        for key, value in self.properties.items():
            attrs.append(f'data-{key}="{value}"')

        title = self.title or self.target_id
        return f"<a {' '.join(attrs)}>{title}</a>"

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
    status: Literal[
        "todo", "in-progress", "blocked", "done", "active", "ended", "stale"
    ] = "todo"
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
    spec_requirements: list[str] = Field(
        default_factory=list
    )  # Which spec requirements this satisfies

    # Handoff context fields for agent-to-agent transitions
    handoff_required: bool = False  # Whether this node needs to be handed off
    previous_agent: str | None = None  # Agent who previously worked on this
    handoff_reason: str | None = (
        None  # Reason for handoff (e.g., blocked, requires different expertise)
    )
    handoff_notes: str | None = None  # Detailed handoff context/decisions
    handoff_timestamp: datetime | None = None  # When the handoff was created

    # Capability-based routing (Phase 3: Agent Routing & Capabilities)
    required_capabilities: list[str] = Field(
        default_factory=list
    )  # Capabilities needed for this task
    capability_tags: list[str] = Field(
        default_factory=list
    )  # Flexible tags for advanced matching

    # Context tracking (aggregated from sessions)
    # These are updated when sessions report context usage for this feature
    context_tokens_used: int = 0  # Total context tokens attributed to this feature
    context_peak_tokens: int = 0  # Highest context usage in any session
    context_cost_usd: float = 0.0  # Total cost attributed to this feature
    context_sessions: list[str] = Field(
        default_factory=list
    )  # Session IDs that reported context

    # Auto-spike metadata (for transition spike generation)
    spike_subtype: (
        Literal[
            "session-init",
            "transition",
            "conversation-init",
            "planning",
            "investigation",
        ]
        | None
    ) = None
    auto_generated: bool = False  # True if auto-created by SessionManager
    session_id: str | None = None  # Session that created/owns this spike
    from_feature_id: str | None = (
        None  # For transition spikes: feature we transitioned from
    )
    to_feature_id: str | None = (
        None  # For transition spikes: feature we transitioned to
    )
    model_name: str | None = (
        None  # Model that worked on this (e.g., "claude-sonnet-4-5")
    )

    def model_post_init(self, __context: Any) -> None:
        """Lightweight validation for required fields."""
        if not self.id or not str(self.id).strip():
            raise ValueError("Node.id must be non-empty")
        if not self.title or not str(self.title).strip():
            raise ValueError("Node.title must be non-empty")

        # Validate auto-spike metadata
        if self.spike_subtype and self.type != "spike":
            raise ValueError(
                f"spike_subtype can only be set on spike nodes, got type='{self.type}'"
            )
        if self.auto_generated and not self.session_id:
            raise ValueError("auto_generated spikes must have session_id set")
        if self.spike_subtype == "transition" and not self.from_feature_id:
            raise ValueError("transition spikes must have from_feature_id set")

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
        self.updated = utc_now()

    def complete_step(self, index: int, agent: str | None = None) -> bool:
        """Mark a step as completed."""
        if 0 <= index < len(self.steps):
            self.steps[index].completed = True
            self.steps[index].agent = agent
            self.steps[index].timestamp = utc_now()
            self.updated = utc_now()
            return True
        return False

    def record_context_usage(
        self,
        session_id: str,
        tokens_used: int,
        peak_tokens: int = 0,
        cost_usd: float = 0.0,
    ) -> None:
        """
        Record context usage from a session working on this feature.

        Args:
            session_id: Session that used context
            tokens_used: Total tokens attributed to this feature
            peak_tokens: Peak context usage during this work
            cost_usd: Cost attributed to this feature
        """
        # Track session if not already recorded
        if session_id not in self.context_sessions:
            self.context_sessions.append(session_id)

        # Update aggregates
        self.context_tokens_used += tokens_used
        self.context_peak_tokens = max(self.context_peak_tokens, peak_tokens)
        self.context_cost_usd += cost_usd
        self.updated = utc_now()

    def context_stats(self) -> dict:
        """
        Get context usage statistics for this feature.

        Returns:
            Dictionary with context usage metrics
        """
        return {
            "tokens_used": self.context_tokens_used,
            "peak_tokens": self.context_peak_tokens,
            "cost_usd": self.context_cost_usd,
            "sessions": len(self.context_sessions),
            "session_ids": self.context_sessions,
        }

    def to_dict(self) -> dict:
        """
        Convert Node to dictionary format.

        This is a convenience alias for Pydantic's model_dump() method,
        providing a more discoverable API for serialization.

        Returns:
            dict: Dictionary representation of the Node with all fields

        Example:
            >>> feature = sdk.features.create("My Feature").save()
            >>> data = feature.to_dict()
            >>> print(data['title'])
            'My Feature'
        """
        return self.model_dump()

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
                edges_html = f"""
        <nav data-graph-edges>{"".join(edge_sections)}
        </nav>"""

        # Build steps HTML
        steps_html = ""
        if self.steps:
            step_items = "\n                ".join(
                step.to_html() for step in self.steps
            )
            steps_html = f"""
        <section data-steps>
            <h3>Implementation Steps</h3>
            <ol>
                {step_items}
            </ol>
        </section>"""

        # Build properties HTML
        props_html = ""
        if self.properties:
            prop_items = []
            for key, value in self.properties.items():
                unit = ""
                if isinstance(value, dict) and "value" in value:
                    unit = (
                        f' data-unit="{value.get("unit", "")}"'
                        if value.get("unit")
                        else ""
                    )
                    display = f"{value['value']} {value.get('unit', '')}".strip()
                    val = value["value"]
                else:
                    display = str(value)
                    val = value
                prop_items.append(
                    f"<dt>{key.replace('_', ' ').title()}</dt>\n"
                    f'                <dd data-key="{key}" data-value="{val}"{unit}>{display}</dd>'
                )
            props_html = f"""
        <section data-properties>
            <h3>Properties</h3>
            <dl>
                {chr(10).join(prop_items)}
            </dl>
        </section>"""

        # Build handoff HTML
        handoff_html = ""
        if self.handoff_required or self.previous_agent:
            handoff_attrs = []
            if self.previous_agent:
                handoff_attrs.append(f'data-previous-agent="{self.previous_agent}"')
            if self.handoff_reason:
                handoff_attrs.append(f'data-reason="{self.handoff_reason}"')
            if self.handoff_timestamp:
                handoff_attrs.append(
                    f'data-timestamp="{self.handoff_timestamp.isoformat()}"'
                )

            attrs_str = " ".join(handoff_attrs)
            handoff_section = f"""
        <section data-handoff{f" {attrs_str}" if attrs_str else ""}>
            <h3>Handoff Context</h3>"""

            if self.previous_agent:
                handoff_section += (
                    f"\n            <p><strong>From:</strong> {self.previous_agent}</p>"
                )

            if self.handoff_reason:
                handoff_section += f"\n            <p><strong>Reason:</strong> {self.handoff_reason}</p>"

            if self.handoff_notes:
                handoff_section += (
                    f"\n            <p><strong>Notes:</strong> {self.handoff_notes}</p>"
                )

            handoff_section += "\n        </section>"
            handoff_html = handoff_section

        # Build content HTML
        content_html = ""
        if self.content:
            content_html = f"""
        <section data-content>
            <h3>Description</h3>
            {self.content}
        </section>"""

        # Build required capabilities HTML
        capabilities_html = ""
        if self.required_capabilities or self.capability_tags:
            cap_items = []
            if self.required_capabilities:
                for cap in self.required_capabilities:
                    cap_items.append(f'<li data-capability="{cap}">{cap}</li>')
            if self.capability_tags:
                for tag in self.capability_tags:
                    cap_items.append(f'<li data-tag="{tag}" class="tag">{tag}</li>')
            if cap_items:
                capabilities_html = f"""
        <section data-required-capabilities>
            <h3>Required Capabilities</h3>
            <ul>
                {chr(10).join(cap_items)}
            </ul>
        </section>"""

        # Agent attribute
        agent_attr = (
            f' data-agent-assigned="{self.agent_assigned}"'
            if self.agent_assigned
            else ""
        )
        if self.claimed_at:
            agent_attr += f' data-claimed-at="{self.claimed_at.isoformat()}"'
        if self.claimed_by_session:
            agent_attr += f' data-claimed-by-session="{self.claimed_by_session}"'

        # Track ID attribute
        track_attr = f' data-track-id="{self.track_id}"' if self.track_id else ""

        # Context tracking attributes
        context_attr = ""
        if self.context_tokens_used > 0:
            context_attr += f' data-context-tokens="{self.context_tokens_used}"'
        if self.context_peak_tokens > 0:
            context_attr += f' data-context-peak="{self.context_peak_tokens}"'
        if self.context_cost_usd > 0:
            context_attr += f' data-context-cost="{self.context_cost_usd:.4f}"'

        # Auto-spike metadata attributes
        auto_spike_attr = ""
        if self.spike_subtype:
            auto_spike_attr += f' data-spike-subtype="{self.spike_subtype}"'
        if self.auto_generated:
            auto_spike_attr += (
                f' data-auto-generated="{str(self.auto_generated).lower()}"'
            )
        if self.session_id:
            auto_spike_attr += f' data-session-id="{self.session_id}"'
        if self.from_feature_id:
            auto_spike_attr += f' data-from-feature-id="{self.from_feature_id}"'
        if self.to_feature_id:
            auto_spike_attr += f' data-to-feature-id="{self.to_feature_id}"'
        if self.model_name:
            auto_spike_attr += f' data-model-name="{self.model_name}"'

        # Build context usage section
        context_html = ""
        if self.context_tokens_used > 0 or self.context_sessions:
            context_html = f"""
        <section data-context-tracking>
            <h3>Context Usage</h3>
            <dl>
                <dt>Total Tokens</dt>
                <dd>{self.context_tokens_used:,}</dd>
                <dt>Peak Tokens</dt>
                <dd>{self.context_peak_tokens:,}</dd>
                <dt>Total Cost</dt>
                <dd>${self.context_cost_usd:.4f}</dd>
                <dt>Sessions</dt>
                <dd>{len(self.context_sessions)}</dd>
            </dl>
        </section>"""

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
             data-updated="{self.updated.isoformat()}"{agent_attr}{track_attr}{context_attr}{auto_spike_attr}>

        <header>
            <h1>{self.title}</h1>
            <div class="metadata">
                <span class="badge status-{self.status}">{self.status.replace("-", " ").title()}</span>
                <span class="badge priority-{self.priority}">{self.priority.title()} Priority</span>
            </div>
        </header>
{edges_html}{handoff_html}{props_html}{capabilities_html}{context_html}{steps_html}{content_html}
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
            lines.append(
                f"Progress: {completed}/{len(self.steps)} steps ({self.completion_percentage}%)"
            )

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
                    Edge(**e) if isinstance(e, dict) else e for e in edge_list
                ]
            data["edges"] = edges

        # Convert step dicts to Step objects
        if "steps" in data:
            data["steps"] = [
                Step(**s) if isinstance(s, dict) else s for s in data["steps"]
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

    def to_html(self, stylesheet_path: str = "../styles.css") -> str:
        """
        Convert spike to HTML document with spike-specific fields.

        Overrides Node.to_html() to include findings and decision sections.
        """
        # Build findings section
        findings_html = ""
        if self.findings:
            findings_html = f"""
        <section data-findings>
            <h3>Findings</h3>
            <div class="findings-content">
                {self.findings}
            </div>
        </section>"""

        # Build decision section
        decision_html = ""
        if self.decision:
            decision_html = f"""
        <section data-decision>
            <h3>Decision</h3>
            <p>{self.decision}</p>
        </section>"""

        # Build spike metadata section
        spike_meta_html = f"""
        <section data-spike-metadata>
            <h3>Spike Metadata</h3>
            <dl>
                <dt>Type</dt>
                <dd>{self.spike_type.value.title()}</dd>"""

        if self.timebox_hours:
            spike_meta_html += f"""
                <dt>Timebox</dt>
                <dd>{self.timebox_hours} hours</dd>"""

        spike_meta_html += """
            </dl>
        </section>"""

        # Get base HTML from Node and insert spike-specific sections
        # We need to call Node's to_html() but inject our sections
        # Strategy: Get base HTML, then insert our sections before closing article tag

        # Call parent's to_html to get base structure
        base_html = super().to_html(stylesheet_path)

        # Insert spike sections before </article>
        spike_sections = f"{spike_meta_html}{findings_html}{decision_html}"
        html_with_findings = base_html.replace(
            "</article>", f"{spike_sections}\n    </article>"
        )

        # Add spike-specific attributes to article tag
        spike_attrs = f' data-spike-type="{self.spike_type.value}"'
        if self.timebox_hours:
            spike_attrs += f' data-timebox-hours="{self.timebox_hours}"'

        # Insert spike attributes into article tag
        html_with_attrs = html_with_findings.replace(
            f'data-updated="{self.updated.isoformat()}"',
            f'data-updated="{self.updated.isoformat()}"{spike_attrs}',
        )

        return html_with_attrs


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


class ContextSnapshot(BaseModel):
    """
    A snapshot of context window usage at a point in time.

    Used to track how context is consumed across sessions, features,
    and activities. Enables analytics for context efficiency.

    The snapshot captures data from Claude Code's status line JSON input.
    """

    timestamp: datetime = Field(default_factory=datetime.now)

    # Token usage in current context window
    input_tokens: int = 0
    output_tokens: int = 0
    cache_creation_tokens: int = 0
    cache_read_tokens: int = 0

    # Context window capacity
    context_window_size: int = 200000

    # Cumulative totals for the session
    total_input_tokens: int = 0
    total_output_tokens: int = 0

    # Cost tracking
    cost_usd: float = 0.0

    # Optional context for what triggered this snapshot
    trigger: str | None = None  # "activity", "feature_switch", "session_start", etc.
    feature_id: str | None = None  # Feature being worked on at this moment

    @property
    def current_tokens(self) -> int:
        """Total tokens in current context window."""
        return self.input_tokens + self.cache_creation_tokens + self.cache_read_tokens

    @property
    def usage_percent(self) -> float:
        """Context window usage as a percentage."""
        if self.context_window_size == 0:
            return 0.0
        return (self.current_tokens / self.context_window_size) * 100

    @classmethod
    def from_claude_input(
        cls, data: dict, trigger: str | None = None, feature_id: str | None = None
    ) -> "ContextSnapshot":
        """
        Create a ContextSnapshot from Claude Code status line JSON input.

        Args:
            data: JSON input from Claude Code (contains context_window, cost, etc.)
            trigger: What triggered this snapshot
            feature_id: Current feature being worked on

        Returns:
            ContextSnapshot instance
        """
        context = data.get("context_window", {})
        usage = context.get("current_usage") or {}
        cost = data.get("cost", {})

        return cls(
            input_tokens=usage.get("input_tokens", 0),
            output_tokens=usage.get("output_tokens", 0),
            cache_creation_tokens=usage.get("cache_creation_input_tokens", 0),
            cache_read_tokens=usage.get("cache_read_input_tokens", 0),
            context_window_size=context.get("context_window_size", 200000),
            total_input_tokens=context.get("total_input_tokens", 0),
            total_output_tokens=context.get("total_output_tokens", 0),
            cost_usd=cost.get("total_cost_usd", 0.0),
            trigger=trigger,
            feature_id=feature_id,
        )

    def to_dict(self) -> dict:
        """Convert to dictionary for serialization."""
        return {
            "ts": self.timestamp.isoformat(),
            "in": self.input_tokens,
            "out": self.output_tokens,
            "cache_create": self.cache_creation_tokens,
            "cache_read": self.cache_read_tokens,
            "window": self.context_window_size,
            "total_in": self.total_input_tokens,
            "total_out": self.total_output_tokens,
            "cost": self.cost_usd,
            "trigger": self.trigger,
            "feature": self.feature_id,
        }

    @classmethod
    def from_dict(cls, data: dict) -> "ContextSnapshot":
        """Create from dictionary."""
        return cls(
            timestamp=datetime.fromisoformat(data["ts"]) if "ts" in data else utc_now(),
            input_tokens=data.get("in", 0),
            output_tokens=data.get("out", 0),
            cache_creation_tokens=data.get("cache_create", 0),
            cache_read_tokens=data.get("cache_read", 0),
            context_window_size=data.get("window", 200000),
            total_input_tokens=data.get("total_in", 0),
            total_output_tokens=data.get("total_out", 0),
            cost_usd=data.get("cost", 0.0),
            trigger=data.get("trigger"),
            feature_id=data.get("feature"),
        )


class ErrorEntry(BaseModel):
    """
    An error record for session error tracking and debugging.

    Stored inline within Session nodes for error analysis and debugging.
    """

    timestamp: datetime = Field(default_factory=datetime.now)
    error_type: str  # Exception class name (ValueError, FileNotFoundError, etc.)
    message: str  # Error message
    traceback: str | None = None  # Full traceback for debugging
    tool: str | None = None  # Tool that caused the error (Edit, Bash, etc.)
    context: str | None = None  # Additional context information
    session_id: str | None = None  # Session ID for cross-referencing
    locals_dump: str | None = None  # JSON-serialized local variables at error point
    stack_frames: list[dict[str, Any]] | None = (
        None  # Structured stack frame information
    )
    command_args: dict[str, Any] | None = None  # Command arguments being executed
    display_level: str = "minimal"  # Display level: minimal, verbose, or debug

    def to_html(self) -> str:
        """Convert error to HTML details element."""
        attrs = [
            f'data-ts="{self.timestamp.isoformat()}"',
            f'data-error-type="{self.error_type}"',
        ]
        if self.tool:
            attrs.append(f'data-tool="{self.tool}"')

        summary = f"<span class='error-type'>{self.error_type}</span>: {self.message}"
        details = ""
        if self.traceback:
            details = f"<pre class='traceback'>{self.traceback}</pre>"

        return f"<details class='error-item' {' '.join(attrs)}><summary>{summary}</summary>{details}</details>"

    def to_context(self) -> str:
        """Lightweight context for AI agents."""
        return f"[{self.timestamp.strftime('%H:%M:%S')}] ERROR {self.error_type}: {self.message}"


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
    parent_activity_id: str | None = (
        None  # Link to parent activity (e.g., Skill invocation)
    )
    payload: dict[str, Any] | None = (
        None  # Optional rich payload for significant events
    )

    # Context tracking (optional, captured when available)
    context_tokens: int | None = None  # Tokens in context when this activity occurred

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
        if self.context_tokens is not None:
            attrs.append(f'data-context-tokens="{self.context_tokens}"')

        return f"<li {' '.join(attrs)}>{self.summary}</li>"

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
    status: Literal["active", "completed", "paused", "failed", "stale"] = "active"
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
        self.status = "completed"
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
            # Determine work item type from ID prefix
            if item_id.startswith("feat-") or item_id.startswith("feature-"):
                file_path = graph_path / "features" / f"{item_id}.html"
            elif item_id.startswith("bug-"):
                file_path = graph_path / "bugs" / f"{item_id}.html"
            elif item_id.startswith("spk-") or item_id.startswith("spike-"):
                file_path = graph_path / "spikes" / f"{item_id}.html"
            elif item_id.startswith("chore-"):
                file_path = graph_path / "chores" / f"{item_id}.html"
            elif item_id.startswith("epic-"):
                file_path = graph_path / "epics" / f"{item_id}.html"
            else:
                # Unknown type, keep it
                kept.append(item_id)
                continue

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
                    f'<li><a href="../features/{fid}.html" data-relationship="worked-on">{fid}</a></li>'
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


class Pattern(Node):
    """Learned workflow pattern for agent optimization.

    Stores detected tool sequences that are either optimal patterns
    to encourage or anti-patterns to avoid.
    """

    pattern_type: Literal["optimal", "anti-pattern", "neutral"] = "neutral"
    sequence: list[str] = Field(default_factory=list)  # ["Bash", "Edit", "Read"]

    # Detection metrics
    detection_count: int = 0
    success_rate: float = 0.0  # 0.0-1.0
    avg_duration_seconds: float = 0.0

    # Sessions where detected
    detected_in_sessions: list[str] = Field(default_factory=list)

    # Recommendation
    recommendation: str | None = None

    # Trend
    first_detected: datetime | None = None
    last_detected: datetime | None = None
    detection_trend: Literal["increasing", "stable", "decreasing"] = "stable"

    def __init__(self, **data: Any):
        # Ensure type is always "pattern"
        data["type"] = "pattern"
        super().__init__(**data)

    def to_html(self, stylesheet_path: str = "../styles.css") -> str:
        """Convert pattern to HTML document with pattern-specific fields."""
        # Build pattern sequence HTML
        sequence_html = ""
        if self.sequence:
            sequence_items = " → ".join(self.sequence)
            sequence_html = f"""
        <section data-pattern-sequence>
            <h3>Tool Sequence</h3>
            <p class="sequence">{sequence_items}</p>
        </section>"""

        # Build pattern metrics HTML
        metrics_html = f"""
        <section data-pattern-metrics>
            <h3>Pattern Metrics</h3>
            <dl>
                <dt>Detection Count</dt>
                <dd>{self.detection_count}</dd>
                <dt>Success Rate</dt>
                <dd>{self.success_rate:.1%}</dd>
                <dt>Avg Duration</dt>
                <dd>{self.avg_duration_seconds:.1f}s</dd>
            </dl>
        </section>"""

        # Build detected sessions HTML
        detected_sessions_html = ""
        if self.detected_in_sessions:
            session_links = "\n                    ".join(
                f'<li><a href="../sessions/{sid}.html">{sid}</a></li>'
                for sid in self.detected_in_sessions
            )
            detected_sessions_html = f"""
        <section data-detected-sessions>
            <h3>Detected In Sessions</h3>
            <ul>
                {session_links}
            </ul>
        </section>"""

        # Build recommendation HTML
        recommendation_html = ""
        if self.recommendation:
            recommendation_html = f"""
        <section data-recommendation>
            <h3>Recommendation</h3>
            <p>{self.recommendation}</p>
        </section>"""

        # Build trend HTML
        trend_html = ""
        if self.first_detected or self.last_detected:
            trend_html = """
        <section data-trend>
            <h3>Trend Analysis</h3>
            <dl>"""
            if self.first_detected:
                trend_html += f"""
                <dt>First Detected</dt>
                <dd>{self.first_detected.strftime("%Y-%m-%d %H:%M")}</dd>"""
            if self.last_detected:
                trend_html += f"""
                <dt>Last Detected</dt>
                <dd>{self.last_detected.strftime("%Y-%m-%d %H:%M")}</dd>"""
            trend_html += f"""
                <dt>Detection Trend</dt>
                <dd class="trend-{self.detection_trend}">{self.detection_trend.title()}</dd>
            </dl>
        </section>"""

        # Build pattern-specific attributes
        pattern_attrs = f' data-pattern-type="{self.pattern_type}"'
        pattern_attrs += f' data-detection-count="{self.detection_count}"'
        pattern_attrs += f' data-success-rate="{self.success_rate:.2f}"'
        pattern_attrs += f' data-detection-trend="{self.detection_trend}"'
        if self.sequence:
            import json

            sequence_json = json.dumps(self.sequence)
            pattern_attrs += f" data-sequence='{sequence_json}'"

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
             data-updated="{self.updated.isoformat()}"{pattern_attrs}>

        <header>
            <h1>{self.title}</h1>
            <div class="metadata">
                <span class="badge status-{self.status}">{self.status.replace("-", " ").title()}</span>
                <span class="badge pattern-{self.pattern_type}">{self.pattern_type.title()}</span>
            </div>
        </header>
{sequence_html}{metrics_html}{detected_sessions_html}{recommendation_html}{trend_html}
    </article>
</body>
</html>
'''


class SessionInsight(Node):
    """Session analysis and health metrics.

    Stores efficiency scores, detected issues, and recommendations
    for a specific session.
    """

    session_id: str = ""
    insight_type: Literal["health", "recommendation", "anomaly"] = "health"

    # Health metrics
    efficiency_score: float = 0.0  # 0.0-1.0
    retry_rate: float = 0.0
    context_rebuild_count: int = 0
    tool_diversity: float = 0.0
    error_recovery_rate: float = 0.0
    overall_health_score: float = 0.0

    # Detections
    issues_detected: list[str] = Field(default_factory=list)
    patterns_matched: list[str] = Field(default_factory=list)  # Pattern IDs
    anti_patterns_matched: list[str] = Field(default_factory=list)

    # Recommendations
    recommendations: list[str] = Field(default_factory=list)

    # Metadata
    analyzed_at: datetime | None = None

    def __init__(self, **data: Any):
        # Ensure type is always "session-insight"
        data["type"] = "session-insight"
        super().__init__(**data)

    def to_html(self, stylesheet_path: str = "../styles.css") -> str:
        """Convert session insight to HTML document with insight-specific fields."""
        # Build health metrics HTML
        metrics_html = f"""
        <section data-health-metrics>
            <h3>Health Metrics</h3>
            <dl>
                <dt>Efficiency Score</dt>
                <dd>{self.efficiency_score:.2f}</dd>
                <dt>Retry Rate</dt>
                <dd>{self.retry_rate:.1%}</dd>
                <dt>Context Rebuild Count</dt>
                <dd>{self.context_rebuild_count}</dd>
                <dt>Tool Diversity</dt>
                <dd>{self.tool_diversity:.2f}</dd>
                <dt>Error Recovery Rate</dt>
                <dd>{self.error_recovery_rate:.1%}</dd>
                <dt>Overall Health Score</dt>
                <dd class="health-score">{self.overall_health_score:.2f}</dd>
            </dl>
        </section>"""

        # Build issues detected HTML
        issues_html = ""
        if self.issues_detected:
            issues_items = "\n                ".join(
                f"<li>{issue}</li>" for issue in self.issues_detected
            )
            issues_html = f"""
        <section data-issues-detected>
            <h3>Issues Detected</h3>
            <ul>
                {issues_items}
            </ul>
        </section>"""

        # Build patterns matched HTML
        patterns_html = ""
        if self.patterns_matched or self.anti_patterns_matched:
            patterns_section = """
        <section data-patterns-matched>
            <h3>Patterns Matched</h3>"""

            if self.patterns_matched:
                pattern_links = "\n                    ".join(
                    f'<li><a href="../patterns/{pid}.html" data-pattern-type="optimal">{pid}</a></li>'
                    for pid in self.patterns_matched
                )
                patterns_section += f"""
            <div data-optimal-patterns>
                <h4>Optimal Patterns:</h4>
                <ul>
                    {pattern_links}
                </ul>
            </div>"""

            if self.anti_patterns_matched:
                anti_pattern_links = "\n                    ".join(
                    f'<li><a href="../patterns/{pid}.html" data-pattern-type="anti-pattern">{pid}</a></li>'
                    for pid in self.anti_patterns_matched
                )
                patterns_section += f"""
            <div data-anti-patterns>
                <h4>Anti-Patterns:</h4>
                <ul>
                    {anti_pattern_links}
                </ul>
            </div>"""

            patterns_section += """
        </section>"""
            patterns_html = patterns_section

        # Build recommendations HTML
        recommendations_html = ""
        if self.recommendations:
            rec_items = "\n                ".join(
                f"<li>{rec}</li>" for rec in self.recommendations
            )
            recommendations_html = f"""
        <section data-recommendations>
            <h3>Recommendations</h3>
            <ul>
                {rec_items}
            </ul>
        </section>"""

        # Build session link HTML
        session_link_html = ""
        if self.session_id:
            session_link_html = f"""
        <section data-session-link>
            <h3>Related Session</h3>
            <p><a href="../sessions/{self.session_id}.html">{self.session_id}</a></p>
        </section>"""

        # Build insight-specific attributes
        import json

        insight_attrs = (
            f' data-session-id="{self.session_id}"' if self.session_id else ""
        )
        insight_attrs += f' data-insight-type="{self.insight_type}"'
        insight_attrs += f' data-efficiency-score="{self.efficiency_score:.2f}"'
        insight_attrs += f' data-retry-rate="{self.retry_rate:.2f}"'
        insight_attrs += f' data-overall-health="{self.overall_health_score:.2f}"'

        if self.analyzed_at:
            insight_attrs += f' data-analyzed-at="{self.analyzed_at.isoformat()}"'

        if self.issues_detected:
            issues_json = json.dumps(self.issues_detected)
            insight_attrs += f" data-issues='{issues_json}'"

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
             data-updated="{self.updated.isoformat()}"{insight_attrs}>

        <header>
            <h1>{self.title}</h1>
            <div class="metadata">
                <span class="badge status-{self.status}">{self.status.replace("-", " ").title()}</span>
                <span class="badge insight-{self.insight_type}">{self.insight_type.title()}</span>
                <span class="badge health-score">Health: {self.overall_health_score:.2f}</span>
            </div>
        </header>
{session_link_html}{metrics_html}{issues_html}{patterns_html}{recommendations_html}
    </article>
</body>
</html>
'''


class AggregatedMetric(Node):
    """Time-aggregated metrics across sessions.

    Stores weekly/monthly aggregated metrics for trend analysis.
    """

    metric_type: Literal["efficiency", "context_usage", "tool_distribution"] = (
        "efficiency"
    )
    scope: Literal["session", "feature", "track", "agent"] = "session"
    scope_id: str | None = None

    # Time window
    period: Literal["daily", "weekly", "monthly"] = "weekly"
    period_start: datetime | None = None
    period_end: datetime | None = None

    # Metrics
    metric_values: dict[str, float] = Field(default_factory=dict)
    percentiles: dict[str, float] = Field(
        default_factory=dict
    )  # {"p50": 0.8, "p90": 0.9}

    # Trend
    trend_direction: Literal["improving", "stable", "declining"] = "stable"
    trend_strength: float = 0.0  # 0.0-1.0
    vs_previous_period_pct: float = 0.0

    # Data source
    sessions_in_period: list[str] = Field(default_factory=list)
    data_points_count: int = 0

    def __init__(self, **data: Any):
        # Ensure type is always "aggregated-metric"
        data["type"] = "aggregated-metric"
        super().__init__(**data)

    def to_html(self, stylesheet_path: str = "../styles.css") -> str:
        """Convert aggregated metric to HTML document with metric-specific fields."""
        # Build metric overview HTML
        overview_html = f"""
        <section data-metric-overview>
            <h3>Metric Overview</h3>
            <dl>
                <dt>Metric Type</dt>
                <dd>{self.metric_type.replace("_", " ").title()}</dd>
                <dt>Scope</dt>
                <dd>{self.scope.title()}</dd>"""

        if self.scope_id:
            overview_html += f"""
                <dt>Scope ID</dt>
                <dd>{self.scope_id}</dd>"""

        overview_html += f"""
                <dt>Period</dt>
                <dd>{self.period.title()}</dd>"""

        if self.period_start:
            overview_html += f"""
                <dt>Period Start</dt>
                <dd>{self.period_start.strftime("%Y-%m-%d %H:%M")}</dd>"""

        if self.period_end:
            overview_html += f"""
                <dt>Period End</dt>
                <dd>{self.period_end.strftime("%Y-%m-%d %H:%M")}</dd>"""

        overview_html += """
            </dl>
        </section>"""

        # Build metric values HTML
        values_html = ""
        if self.metric_values:
            value_items = "\n                ".join(
                f"<dt>{k.replace('_', ' ').title()}</dt>\n                <dd>{v:.4f}</dd>"
                for k, v in self.metric_values.items()
            )
            values_html = f"""
        <section data-metric-values>
            <h3>Metric Values</h3>
            <dl>
                {value_items}
            </dl>
        </section>"""

        # Build percentiles HTML
        percentiles_html = ""
        if self.percentiles:
            percentile_items = "\n                ".join(
                f"<dt>{k}</dt>\n                <dd>{v:.4f}</dd>"
                for k, v in self.percentiles.items()
            )
            percentiles_html = f"""
        <section data-percentiles>
            <h3>Percentiles</h3>
            <dl>
                {percentile_items}
            </dl>
        </section>"""

        # Build trend HTML
        trend_html = f"""
        <section data-trend>
            <h3>Trend Analysis</h3>
            <dl>
                <dt>Direction</dt>
                <dd class="trend-{self.trend_direction}">{self.trend_direction.title()}</dd>
                <dt>Strength</dt>
                <dd>{self.trend_strength:.1%}</dd>
                <dt>vs Previous Period</dt>
                <dd class="{"positive" if self.vs_previous_period_pct > 0 else "negative"}">{self.vs_previous_period_pct:+.1f}%</dd>
            </dl>
        </section>"""

        # Build sessions HTML
        sessions_html = ""
        if self.sessions_in_period:
            session_links = "\n                    ".join(
                f'<li><a href="../sessions/{sid}.html">{sid}</a></li>'
                for sid in self.sessions_in_period[:20]  # Limit to first 20
            )
            more_sessions = ""
            if len(self.sessions_in_period) > 20:
                more_sessions = f"\n                    <li>... and {len(self.sessions_in_period) - 20} more</li>"

            sessions_html = f"""
        <section data-sessions>
            <h3>Sessions in Period ({len(self.sessions_in_period)})</h3>
            <ul>
                {session_links}{more_sessions}
            </ul>
        </section>"""

        # Build data source HTML
        data_source_html = f"""
        <section data-data-source>
            <h3>Data Source</h3>
            <dl>
                <dt>Data Points</dt>
                <dd>{self.data_points_count}</dd>
                <dt>Sessions Analyzed</dt>
                <dd>{len(self.sessions_in_period)}</dd>
            </dl>
        </section>"""

        # Build metric-specific attributes
        import json

        metric_attrs = f' data-metric-type="{self.metric_type}"'
        metric_attrs += f' data-scope="{self.scope}"'
        if self.scope_id:
            metric_attrs += f' data-scope-id="{self.scope_id}"'
        metric_attrs += f' data-period="{self.period}"'
        metric_attrs += f' data-trend-direction="{self.trend_direction}"'
        metric_attrs += f' data-trend-strength="{self.trend_strength:.2f}"'
        metric_attrs += f' data-data-points="{self.data_points_count}"'

        if self.period_start:
            metric_attrs += f' data-period-start="{self.period_start.isoformat()}"'
        if self.period_end:
            metric_attrs += f' data-period-end="{self.period_end.isoformat()}"'

        if self.metric_values:
            values_json = json.dumps(self.metric_values)
            metric_attrs += f" data-values='{values_json}'"

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
             data-updated="{self.updated.isoformat()}"{metric_attrs}>

        <header>
            <h1>{self.title}</h1>
            <div class="metadata">
                <span class="badge status-{self.status}">{self.status.replace("-", " ").title()}</span>
                <span class="badge metric-{self.metric_type}">{self.metric_type.replace("_", " ").title()}</span>
                <span class="badge trend-{self.trend_direction}">{self.trend_direction.title()}</span>
            </div>
        </header>
{overview_html}{values_html}{percentiles_html}{trend_html}{data_source_html}{sessions_html}
    </article>
</body>
</html>
'''


class Todo(BaseModel):
    """
    A persistent todo item for AI agent task tracking.

    Unlike ephemeral in-context todos (TodoWrite), this model:
    - Persists to `.htmlgraph/todos/` as HTML files
    - Links to sessions and features
    - Enables learning from task patterns across sessions
    - Provides full audit trail of agent work decomposition

    Matches TodoWrite format with content and activeForm fields.
    """

    id: str
    content: str  # The imperative form (e.g., "Run tests")
    active_form: str  # The present continuous form (e.g., "Running tests")
    status: Literal["pending", "in_progress", "completed"] = "pending"

    # Timestamps
    created: datetime = Field(default_factory=datetime.now)
    updated: datetime = Field(default_factory=datetime.now)
    started_at: datetime | None = None
    completed_at: datetime | None = None

    # Context linking
    session_id: str | None = None  # Session where this todo was created
    feature_id: str | None = None  # Feature this todo belongs to
    parent_todo_id: str | None = None  # For nested/sub-todos

    # Agent tracking
    agent: str | None = None  # Agent that created this todo
    completed_by: str | None = None  # Agent that completed it

    # Metadata
    priority: int = 0  # Order within a list (0 = first)
    duration_seconds: float | None = None  # How long it took to complete

    def start(self) -> "Todo":
        """Mark todo as in progress."""
        self.status = "in_progress"
        self.started_at = utc_now()
        self.updated = utc_now()
        return self

    def complete(self, agent: str | None = None) -> "Todo":
        """Mark todo as completed."""
        self.status = "completed"
        self.completed_at = utc_now()
        self.completed_by = agent
        self.updated = utc_now()

        # Calculate duration if started
        if self.started_at:
            self.duration_seconds = (
                self.completed_at - self.started_at
            ).total_seconds()

        return self

    def to_html(self, stylesheet_path: str = "../styles.css") -> str:
        """Convert todo to HTML document."""
        # Status emoji
        status_emoji = {
            "pending": "⏳",
            "in_progress": "🔄",
            "completed": "✅",
        }.get(self.status, "⏳")

        # Build attributes
        # Escape quotes in content for HTML attributes
        escaped_content = self.content.replace('"', "&quot;")
        escaped_active_form = self.active_form.replace('"', "&quot;")

        attrs = [
            f'data-status="{self.status}"',
            f'data-priority="{self.priority}"',
            f'data-created="{self.created.isoformat()}"',
            f'data-updated="{self.updated.isoformat()}"',
            f'data-todo-content="{escaped_content}"',
            f'data-todo-active-form="{escaped_active_form}"',
        ]

        if self.session_id:
            attrs.append(f'data-session-id="{self.session_id}"')
        if self.feature_id:
            attrs.append(f'data-feature-id="{self.feature_id}"')
        if self.parent_todo_id:
            attrs.append(f'data-parent-todo-id="{self.parent_todo_id}"')
        if self.agent:
            attrs.append(f'data-agent="{self.agent}"')
        if self.started_at:
            attrs.append(f'data-started-at="{self.started_at.isoformat()}"')
        if self.completed_at:
            attrs.append(f'data-completed-at="{self.completed_at.isoformat()}"')
        if self.completed_by:
            attrs.append(f'data-completed-by="{self.completed_by}"')
        if self.duration_seconds is not None:
            attrs.append(f'data-duration="{self.duration_seconds:.1f}"')

        attrs_str = " ".join(attrs)

        # Build links section
        links_html = ""
        if self.session_id or self.feature_id or self.parent_todo_id:
            links_section = """
        <section data-links>
            <h3>Related</h3>
            <ul>"""
            if self.session_id:
                links_section += f'\n                <li><a href="../sessions/{self.session_id}.html">Session: {self.session_id}</a></li>'
            if self.feature_id:
                links_section += f'\n                <li><a href="../features/{self.feature_id}.html">Feature: {self.feature_id}</a></li>'
            if self.parent_todo_id:
                links_section += f'\n                <li><a href="{self.parent_todo_id}.html">Parent: {self.parent_todo_id}</a></li>'
            links_section += """
            </ul>
        </section>"""
            links_html = links_section

        return f'''<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <meta name="htmlgraph-version" content="1.0">
    <title>{status_emoji} {self.content}</title>
    <link rel="stylesheet" href="{stylesheet_path}">
</head>
<body>
    <article id="{self.id}"
             data-type="todo"
             {attrs_str}>

        <header>
            <h1>{status_emoji} {self.content}</h1>
            <div class="metadata">
                <span class="badge status-{self.status}">{self.status.replace("_", " ").title()}</span>
            </div>
        </header>

        <section data-content>
            <h3>Task</h3>
            <p><strong>Content:</strong> {self.content}</p>
            <p><strong>Active Form:</strong> {self.active_form}</p>
        </section>
{links_html}
    </article>
</body>
</html>
'''

    def to_context(self) -> str:
        """Lightweight context for AI agents."""
        status_marker = {
            "pending": "[ ]",
            "in_progress": "[~]",
            "completed": "[x]",
        }.get(self.status, "[ ]")

        return f"{status_marker} {self.content}"

    def to_todowrite_format(self) -> dict[str, str]:
        """Convert to TodoWrite format for compatibility."""
        return {
            "content": self.content,
            "status": self.status,
            "activeForm": self.active_form,
        }

    @classmethod
    def from_todowrite(
        cls,
        todo_dict: dict[str, str],
        todo_id: str,
        session_id: str | None = None,
        feature_id: str | None = None,
        agent: str | None = None,
        priority: int = 0,
    ) -> "Todo":
        """
        Create a Todo from TodoWrite format.

        Args:
            todo_dict: Dict with 'content', 'status', 'activeForm' keys
            todo_id: Unique ID for this todo
            session_id: Current session ID
            feature_id: Feature this todo belongs to
            agent: Agent creating this todo
            priority: Order in the list

        Returns:
            Todo instance
        """
        status = todo_dict.get("status", "pending")
        if status not in ("pending", "in_progress", "completed"):
            status = "pending"

        return cls(
            id=todo_id,
            content=todo_dict.get("content", ""),
            active_form=todo_dict.get("activeForm", todo_dict.get("content", "")),
            status=status,  # type: ignore
            session_id=session_id,
            feature_id=feature_id,
            agent=agent,
            priority=priority,
        )
