"""
Pydantic models for HtmlGraph nodes, edges, and steps.

These models provide:
- Schema validation for graph data
- HTML serialization/deserialization
- Lightweight context generation for AI agents
"""

from datetime import datetime, timezone
from typing import Any, Literal

from pydantic import BaseModel, Field


def utc_now() -> datetime:
    """Return current time as UTC-aware datetime."""
    return datetime.now(timezone.utc)


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


from htmlgraph.models.base import Edge, Node


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
                subdir = _work_item_subdir(self.feature_id)
                links_section += f'\n                <li><a href="../{subdir}/{self.feature_id}.html">Feature: {self.feature_id}</a></li>'
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
