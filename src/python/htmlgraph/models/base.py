"""
Pydantic models for HtmlGraph nodes, edges, and steps.

These models provide:
- Schema validation for graph data
- HTML serialization/deserialization
- Lightweight context generation for AI agents
"""

from datetime import datetime, timezone
from enum import Enum
from typing import Any, Literal

from pydantic import BaseModel, Field, model_validator


def utc_now() -> datetime:
    """Return current time as UTC-aware datetime."""
    return datetime.now(timezone.utc)


class RelationshipType(str, Enum):
    """
    Typed relationships between graph nodes.

    Used for declaring dependencies, provenance, and associations
    between features, bugs, spikes, and other work items.
    """

    BLOCKS = "blocks"
    BLOCKED_BY = "blocked_by"
    RELATES_TO = "relates_to"
    IMPLEMENTS = "implements"
    CAUSED_BY = "caused_by"
    SPAWNED_FROM = "spawned_from"
    IMPLEMENTED_IN = "implemented-in"


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
    step_id: str | None = None
    depends_on: list[str] = []

    def to_html(self) -> str:
        """Convert step to HTML list item."""
        status = "✅" if self.completed else "⏳"
        agent_attr = f' data-agent="{self.agent}"' if self.agent else ""
        completed_attr = f' data-completed="{str(self.completed).lower()}"'
        step_id_attr = f' data-step-id="{self.step_id}"' if self.step_id else ""
        return f"<li{completed_attr}{agent_attr}{step_id_attr}>{status} {self.description}</li>"

    def to_context(self) -> str:
        """Lightweight context for AI agents."""
        status = "[x]" if self.completed else "[ ]"
        step_id_prefix = f"[{self.step_id}] " if self.step_id else ""
        return f"{step_id_prefix}{status} {self.description}"

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

    model_config = {"populate_by_name": True}

    id: str = Field(..., alias="node_id")
    title: str = ""

    @model_validator(mode="before")
    @classmethod
    def _compat_attributes(cls, data: Any) -> Any:
        """Translate legacy ``attributes`` kwarg into ``properties`` + top-level fields."""
        if not isinstance(data, dict):
            return data
        attrs = data.pop("attributes", None)
        if attrs and isinstance(attrs, dict):
            # Promote known top-level fields
            if "title" in attrs and "title" not in data:
                data["title"] = attrs.pop("title")
            if "status" in attrs and "status" not in data:
                data["status"] = attrs.pop("status")
            # Merge remaining into properties
            props = data.get("properties", {})
            props.update(attrs)
            data["properties"] = props
        return data

    type: str = "node"
    status: Literal[
        "todo", "in-progress", "blocked", "done", "active", "ended", "stale"
    ] = "todo"
    priority: Literal["low", "medium", "high", "critical"] = "medium"
    classes: list[str] = Field(default_factory=list)
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

    @property
    def node_id(self) -> str:
        """Backward-compatible alias for ``id``."""
        return self.id

    @property
    def attributes(self) -> dict[str, Any]:
        """Backward-compatible proxy that returns ``properties``."""
        return self.properties

    @attributes.setter
    def attributes(self, value: dict[str, Any]) -> None:
        """Backward-compatible setter that writes to ``properties``."""
        self.properties = value

    def model_post_init(self, __context: Any) -> None:
        """Lightweight validation for required fields."""
        if not self.id or not str(self.id).strip():
            raise ValueError("Node.id must be non-empty")

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

    def relates_to(self, other_id: str, title: str | None = None) -> None:
        """Add a 'relates_to' edge to another node."""
        self.add_edge(
            Edge(
                target_id=other_id,
                relationship=RelationshipType.RELATES_TO,
                title=title,
            )
        )

    def spawned_from(self, other_id: str, title: str | None = None) -> None:
        """Add a 'spawned_from' edge indicating provenance."""
        self.add_edge(
            Edge(
                target_id=other_id,
                relationship=RelationshipType.SPAWNED_FROM,
                title=title,
            )
        )

    def caused_by(self, other_id: str, title: str | None = None) -> None:
        """Add a 'caused_by' edge indicating causation."""
        self.add_edge(
            Edge(
                target_id=other_id,
                relationship=RelationshipType.CAUSED_BY,
                title=title,
            )
        )

    def implements(self, other_id: str, title: str | None = None) -> None:
        """Add an 'implements' edge linking implementation to spec/requirement."""
        self.add_edge(
            Edge(
                target_id=other_id,
                relationship=RelationshipType.IMPLEMENTS,
                title=title,
            )
        )

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

        # Build class attribute
        class_attr = ""
        if self.classes:
            class_attr = f' class="{" ".join(self.classes)}"'

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
    <article id="{self.id}"{class_attr}
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
