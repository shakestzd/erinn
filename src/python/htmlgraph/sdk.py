"""
HtmlGraph SDK - AI-Friendly Interface

Provides a fluent, ergonomic API for AI agents with:
- Auto-discovery of .htmlgraph directory
- Method chaining for all operations
- Context managers for auto-save
- Batch operations
- Minimal boilerplate

Example:
    from htmlgraph import SDK

    # Auto-discovers .htmlgraph directory
    sdk = SDK(agent="claude")

    # Fluent feature creation
    feature = sdk.features.create(
        title="User Authentication",
        track="auth"
    ).add_steps([
        "Create login endpoint",
        "Add JWT middleware",
        "Write tests"
    ]).set_priority("high").save()

    # Work on a feature
    with sdk.features.get("feature-001") as feature:
        feature.start()
        feature.complete_step(0)
        # Auto-saves on exit

    # Query
    todos = sdk.features.where(status="todo", priority="high")

    # Batch operations
    sdk.features.mark_done(["feat-001", "feat-002", "feat-003"])
"""

from __future__ import annotations
from contextlib import contextmanager
from datetime import datetime
from pathlib import Path
from typing import Any, Iterator, Literal
from dataclasses import dataclass

from htmlgraph.models import Node, Step, Edge
from htmlgraph.graph import HtmlGraph
from htmlgraph.agents import AgentInterface
from htmlgraph.track_builder import TrackCollection
from htmlgraph.ids import generate_id
from htmlgraph.analytics import Analytics


@dataclass
class FeatureBuilder:
    """Fluent builder for creating features."""

    _sdk: 'SDK'
    _data: dict[str, Any]

    def __init__(self, sdk: 'SDK', title: str, **kwargs):
        self._sdk = sdk
        self._data = {
            "title": title,
            "type": "feature",
            "status": "todo",
            "priority": "medium",
            "steps": [],
            "edges": {},
            "properties": {},
            **kwargs
        }

    def set_priority(self, priority: Literal["low", "medium", "high", "critical"]) -> FeatureBuilder:
        """Set feature priority."""
        self._data["priority"] = priority
        return self

    def set_status(self, status: str) -> FeatureBuilder:
        """Set feature status."""
        self._data["status"] = status
        return self

    def add_step(self, description: str) -> FeatureBuilder:
        """Add a single step."""
        self._data["steps"].append(Step(description=description))
        return self

    def add_steps(self, descriptions: list[str]) -> FeatureBuilder:
        """Add multiple steps."""
        for desc in descriptions:
            self._data["steps"].append(Step(description=desc))
        return self

    def set_track(self, track_id: str) -> FeatureBuilder:
        """Link to a track."""
        self._data["track_id"] = track_id
        return self

    def set_description(self, description: str) -> FeatureBuilder:
        """Set feature description."""
        self._data["content"] = f"<p>{description}</p>"
        return self

    def blocks(self, feature_id: str) -> FeatureBuilder:
        """Add blocking relationship."""
        if "blocks" not in self._data["edges"]:
            self._data["edges"]["blocks"] = []
        self._data["edges"]["blocks"].append(
            Edge(target_id=feature_id, relationship="blocks")
        )
        return self

    def blocked_by(self, feature_id: str) -> FeatureBuilder:
        """Add blocked-by relationship."""
        if "blocked_by" not in self._data["edges"]:
            self._data["edges"]["blocked_by"] = []
        self._data["edges"]["blocked_by"].append(
            Edge(target_id=feature_id, relationship="blocked_by")
        )
        return self

    def complete_and_handoff(
        self,
        reason: str,
        notes: str | None = None,
        next_agent: str | None = None,
    ) -> FeatureBuilder:
        """
        Mark feature as complete and create a handoff for the next agent.

        Sets handoff metadata and releases the feature for another agent to claim.

        Args:
            reason: Reason for handoff
            notes: Detailed handoff context/decisions
            next_agent: Next agent to claim (optional)

        Returns:
            Self for method chaining

        Example:
            feature = sdk.features.create("Review PR").complete_and_handoff(
                reason="awaiting code review",
                notes="All tests passing, ready for review"
            ).save()
        """
        self._data["handoff_required"] = True
        self._data["handoff_reason"] = reason
        self._data["handoff_notes"] = notes
        self._data["handoff_timestamp"] = datetime.now()
        return self

    def save(self) -> Node:
        """Save the feature and return the Node."""
        # Generate collision-resistant ID if not provided
        if "id" not in self._data:
            self._data["id"] = generate_id(
                node_type=self._data.get("type", "feature"),
                title=self._data.get("title", ""),
            )

        node = Node(**self._data)
        self._sdk._graph.add(node)
        return node


class Collection:
    """Generic collection interface for any node type."""

    def __init__(self, sdk: 'SDK', collection_name: str, node_type: str):
        """
        Initialize a collection.

        Args:
            sdk: Parent SDK instance
            collection_name: Name of the collection (e.g., "features", "bugs")
            node_type: Node type to filter by (e.g., "feature", "bug")
        """
        self._sdk = sdk
        self._collection_name = collection_name
        self._node_type = node_type
        self._graph = None  # Lazy-loaded

    def _ensure_graph(self):
        """Lazy-load the graph for this collection."""
        if self._graph is None:
            from htmlgraph.graph import HtmlGraph
            collection_path = self._sdk._directory / self._collection_name
            self._graph = HtmlGraph(collection_path, auto_load=True)
        return self._graph

    def get(self, node_id: str) -> Node | None:
        """Get a node by ID."""
        return self._ensure_graph().get(node_id)

    @contextmanager
    def edit(self, node_id: str) -> Iterator[Node]:
        """
        Context manager for editing a node.

        Auto-saves on exit.

        Example:
            with sdk.bugs.edit("bug-001") as bug:
                bug.status = "in-progress"
        """
        graph = self._ensure_graph()
        node = graph.get(node_id)
        if not node:
            raise ValueError(f"{self._node_type.capitalize()} {node_id} not found")

        yield node

        # Auto-save on exit
        graph.update(node)

    def where(
        self,
        status: str | None = None,
        priority: str | None = None,
        track: str | None = None,
        assigned_to: str | None = None,
        **extra_filters
    ) -> list[Node]:
        """
        Query nodes with filters.

        Example:
            high_bugs = sdk.bugs.where(status="todo", priority="high")
        """
        def matches(node: Node) -> bool:
            if node.type != self._node_type:
                return False
            if status and getattr(node, 'status', None) != status:
                return False
            if priority and getattr(node, 'priority', None) != priority:
                return False
            if track and getattr(node, "track_id", None) != track:
                return False
            if assigned_to and getattr(node, 'agent_assigned', None) != assigned_to:
                return False

            # Check extra filters
            for key, value in extra_filters.items():
                if getattr(node, key, None) != value:
                    return False

            return True

        return self._ensure_graph().filter(matches)

    def all(self) -> list[Node]:
        """Get all nodes of this type."""
        return [n for n in self._ensure_graph() if n.type == self._node_type]

    def delete(self, node_id: str) -> bool:
        """
        Delete a node.

        Args:
            node_id: Node ID to delete

        Returns:
            True if deleted, False if not found

        Example:
            sdk.features.delete("feature-001")
        """
        graph = self._ensure_graph()
        return graph.delete(node_id)

    def batch_delete(self, node_ids: list[str]) -> int:
        """
        Delete multiple nodes in batch.

        Args:
            node_ids: List of node IDs to delete

        Returns:
            Number of nodes successfully deleted

        Example:
            count = sdk.features.batch_delete(["feat-001", "feat-002", "feat-003"])
            print(f"Deleted {count} features")
        """
        graph = self._ensure_graph()
        return graph.batch_delete(node_ids)

    def update(self, node: Node) -> Node:
        """
        Update a node.

        Args:
            node: Node to update

        Returns:
            Updated node
        """
        node.updated = datetime.now()
        self._ensure_graph().update(node)
        return node

    def batch_update(
        self,
        node_ids: list[str],
        updates: dict[str, Any]
    ) -> int:
        """
        Vectorized batch update operation.

        Args:
            node_ids: List of node IDs to update
            updates: Dictionary of attribute: value pairs to update

        Returns:
            Number of nodes successfully updated

        Example:
            sdk.bugs.batch_update(
                ["bug-1", "bug-2"],
                {"status": "done", "agent_assigned": "claude"}
            )
        """
        graph = self._ensure_graph()
        now = datetime.now()
        count = 0

        # Vectorized retrieval
        nodes = [graph.get(nid) for nid in node_ids]

        # Batch update
        for node in nodes:
            if node:
                # Apply all updates
                for attr, value in updates.items():
                    setattr(node, attr, value)
                node.updated = now
                graph.update(node)
                count += 1

        return count

    def mark_done(self, node_ids: list[str]) -> int:
        """
        Batch mark nodes as done.

        Returns:
            Number of nodes updated
        """
        return self.batch_update(node_ids, {"status": "done"})

    def assign(self, node_ids: list[str], agent: str) -> int:
        """
        Batch assign nodes to an agent.

        Returns:
            Number of nodes assigned
        """
        updates = {
            "agent_assigned": agent,
            "status": "in-progress"
        }
        return self.batch_update(node_ids, updates)

    def claim(self, node_id: str, agent: str | None = None) -> Node:
        """
        Claim a node for an agent.

        Args:
            node_id: Node ID to claim
            agent: Agent ID (defaults to SDK agent)

        Returns:
            The claimed Node
        """
        agent = agent or self._sdk.agent
        if not agent:
            raise ValueError("Agent ID required for claiming")

        graph = self._ensure_graph()
        node = graph.get(node_id)
        if not node:
            raise ValueError(f"Node {node_id} not found")

        if node.agent_assigned and node.agent_assigned != agent:
            raise ValueError(f"Node {node_id} is already claimed by {node.agent_assigned}")

        node.agent_assigned = agent
        node.claimed_at = datetime.now()
        node.status = "in-progress"
        node.updated = datetime.now()
        graph.update(node)
        return node

    def release(self, node_id: str) -> Node:
        """
        Release a claimed node.

        Args:
            node_id: Node ID to release

        Returns:
            The released Node
        """
        graph = self._ensure_graph()
        node = graph.get(node_id)
        if not node:
            raise ValueError(f"Node {node_id} not found")

        node.agent_assigned = None
        node.claimed_at = None
        node.claimed_by_session = None
        node.status = "todo"
        node.updated = datetime.now()
        graph.update(node)
        return node


class FeatureCollection(Collection):
    """Collection interface for features with builder support."""

    def __init__(self, sdk: 'SDK'):
        super().__init__(sdk, "features", "feature")
        self._sdk = sdk

    def create(self, title: str, **kwargs) -> FeatureBuilder:
        """
        Create a new feature with fluent interface.

        Args:
            title: Feature title
            **kwargs: Additional feature properties

        Returns:
            FeatureBuilder for method chaining

        Example:
            feature = sdk.features.create("User Auth")
                .set_priority("high")
                .add_steps(["Login", "Logout"])
                .save()
        """
        return FeatureBuilder(self._sdk, title, **kwargs)


class SDK:
    """
    Main SDK interface for AI agents.

    Auto-discovers .htmlgraph directory and provides fluent API for all collections.

    Available Collections:
        - features: Feature work items with builder support
        - bugs: Bug reports
        - chores: Maintenance and chore tasks
        - spikes: Investigation and research spikes
        - epics: Large bodies of work
        - phases: Project phases
        - sessions: Agent sessions
        - tracks: Work tracks
        - agents: Agent information

    Example:
        sdk = SDK(agent="claude")

        # Work with features (has builder support)
        feature = sdk.features.create("User Auth")
            .set_priority("high")
            .add_steps(["Login", "Logout"])
            .save()

        # Work with bugs
        high_bugs = sdk.bugs.where(status="todo", priority="high")
        with sdk.bugs.edit("bug-001") as bug:
            bug.status = "in-progress"

        # Work with any collection
        all_spikes = sdk.spikes.all()
        sdk.chores.mark_done(["chore-001", "chore-002"])
        sdk.epics.assign(["epic-001"], agent="claude")
    """

    def __init__(
        self,
        directory: Path | str | None = None,
        agent: str | None = None
    ):
        """
        Initialize SDK.

        Args:
            directory: Path to .htmlgraph directory (auto-discovered if not provided)
            agent: Agent identifier for operations
        """
        if directory is None:
            directory = self._discover_htmlgraph()

        self._directory = Path(directory)
        self._agent_id = agent

        # Initialize underlying components (for backward compatibility)
        self._graph = HtmlGraph(self._directory / "features")
        self._agent_interface = AgentInterface(
            self._directory / "features",
            agent_id=agent
        )

        # Collection interfaces - all work item types
        self.features = FeatureCollection(self)
        self.bugs = Collection(self, "bugs", "bug")
        self.chores = Collection(self, "chores", "chore")
        self.spikes = Collection(self, "spikes", "spike")
        self.epics = Collection(self, "epics", "epic")
        self.phases = Collection(self, "phases", "phase")

        # Non-work collections
        self.sessions = Collection(self, "sessions", "session")
        self.tracks = TrackCollection(self)  # Use specialized collection with builder support
        self.agents = Collection(self, "agents", "agent")

        # Analytics interface (Phase 2: Work Type Analytics)
        self.analytics = Analytics(self)

        # Dependency analytics interface (Advanced graph analytics)
        from htmlgraph.dependency_analytics import DependencyAnalytics
        self.dep_analytics = DependencyAnalytics(self._graph)

    @staticmethod
    def _discover_htmlgraph() -> Path:
        """
        Auto-discover .htmlgraph directory.

        Searches current directory and parents.
        """
        current = Path.cwd()

        # Check current directory
        if (current / ".htmlgraph").exists():
            return current / ".htmlgraph"

        # Check parent directories
        for parent in current.parents:
            if (parent / ".htmlgraph").exists():
                return parent / ".htmlgraph"

        # Default to current directory
        return current / ".htmlgraph"

    @property
    def agent(self) -> str | None:
        """Get current agent ID."""
        return self._agent_id

    def reload(self) -> None:
        """Reload all data from disk."""
        self._graph.reload()
        self._agent_interface.reload()

    def summary(self, max_items: int = 10) -> str:
        """
        Get project summary.

        Returns:
            Compact overview for AI agent orientation
        """
        return self._agent_interface.get_summary(max_items)

    def my_work(self) -> dict[str, Any]:
        """
        Get current agent's workload.

        Returns:
            Dict with in_progress, completed counts
        """
        if not self._agent_id:
            raise ValueError("No agent ID set")
        return self._agent_interface.get_workload(self._agent_id)

    def next_task(
        self,
        priority: str | None = None,
        auto_claim: bool = True
    ) -> Node | None:
        """
        Get next available task for this agent.

        Args:
            priority: Optional priority filter
            auto_claim: Automatically claim the task

        Returns:
            Next available Node or None
        """
        return self._agent_interface.get_next_task(
            agent_id=self._agent_id,
            priority=priority,
            node_type="feature",
            auto_claim=auto_claim
        )

    # =========================================================================
    # Strategic Planning & Analytics (Agent-Friendly Interface)
    # =========================================================================

    def find_bottlenecks(self, top_n: int = 5) -> list[dict[str, Any]]:
        """
        Identify tasks blocking the most downstream work.

        Args:
            top_n: Maximum number of bottlenecks to return

        Returns:
            List of bottleneck tasks with impact metrics

        Example:
            >>> sdk = SDK(agent="claude")
            >>> bottlenecks = sdk.find_bottlenecks(top_n=3)
            >>> for bn in bottlenecks:
            ...     print(f"{bn['title']} blocks {bn['blocks_count']} tasks")
        """
        return self._agent_interface.find_bottlenecks(top_n=top_n)

    def get_parallel_work(self, max_agents: int = 5) -> dict[str, Any]:
        """
        Find tasks that can be worked on simultaneously.

        Args:
            max_agents: Maximum number of parallel agents to plan for

        Returns:
            Dict with parallelization opportunities

        Example:
            >>> sdk = SDK(agent="claude")
            >>> parallel = sdk.get_parallel_work(max_agents=3)
            >>> print(f"Can work on {parallel['max_parallelism']} tasks at once")
            >>> print(f"Ready now: {parallel['ready_now']}")
        """
        return self._agent_interface.get_parallel_work(max_agents=max_agents)

    def recommend_next_work(self, agent_count: int = 1) -> list[dict[str, Any]]:
        """
        Get smart recommendations for what to work on next.

        Considers priority, dependencies, and transitive impact.

        Args:
            agent_count: Number of agents/tasks to recommend

        Returns:
            List of recommended tasks with reasoning

        Example:
            >>> sdk = SDK(agent="claude")
            >>> recs = sdk.recommend_next_work(agent_count=3)
            >>> for rec in recs:
            ...     print(f"{rec['title']} (score: {rec['score']})")
            ...     print(f"  Reasons: {rec['reasons']}")
        """
        return self._agent_interface.recommend_next_work(agent_count=agent_count)

    def assess_risks(self) -> dict[str, Any]:
        """
        Assess dependency-related risks in the project.

        Identifies single points of failure, circular dependencies,
        and orphaned tasks.

        Returns:
            Dict with risk assessment results

        Example:
            >>> sdk = SDK(agent="claude")
            >>> risks = sdk.assess_risks()
            >>> if risks['high_risk_count'] > 0:
            ...     print(f"Warning: {risks['high_risk_count']} high-risk tasks")
        """
        return self._agent_interface.assess_risks()

    def analyze_impact(self, node_id: str) -> dict[str, Any]:
        """
        Analyze the impact of completing a specific task.

        Args:
            node_id: Task to analyze

        Returns:
            Dict with impact analysis

        Example:
            >>> sdk = SDK(agent="claude")
            >>> impact = sdk.analyze_impact("feature-001")
            >>> print(f"Completing this unlocks {impact['unlocks_count']} tasks")
        """
        return self._agent_interface.analyze_impact(node_id)

    # =========================================================================
    # Planning Workflow Integration
    # =========================================================================

    def start_planning_spike(
        self,
        title: str,
        context: str = "",
        timebox_hours: float = 4.0,
        auto_start: bool = True
    ) -> Node:
        """
        Create a planning spike to research and design before implementation.

        This is for timeboxed investigation before creating a full track.

        Args:
            title: Spike title (e.g., "Plan User Authentication System")
            context: Background information
            timebox_hours: Time limit for spike (default: 4 hours)
            auto_start: Automatically start the spike (default: True)

        Returns:
            Created spike Node

        Example:
            >>> sdk = SDK(agent="claude")
            >>> spike = sdk.start_planning_spike(
            ...     "Plan Real-time Notifications",
            ...     context="Users need live updates. Research options.",
            ...     timebox_hours=3.0
            ... )
        """
        from htmlgraph.models import SpikeType

        spike = self.spikes.create(title) \
            .set_spike_type(SpikeType.ARCHITECTURAL) \
            .set_timebox_hours(timebox_hours) \
            .add_steps([
                "Research existing solutions and patterns",
                "Define requirements and constraints",
                "Design high-level architecture",
                "Identify dependencies and risks",
                "Create implementation plan"
            ]) \
            .save()

        if context:
            with self.spikes.edit(spike.id) as s:
                s.content = f"<p>{context}</p>"

        if auto_start and self._agent_id:
            with self.spikes.edit(spike.id) as s:
                s.status = "in-progress"
                s.agent_assigned = self._agent_id

        return spike

    def create_track_from_plan(
        self,
        title: str,
        description: str,
        spike_id: str | None = None,
        priority: str = "high",
        requirements: list[str | tuple[str, str]] | None = None,
        phases: list[tuple[str, list[str]]] | None = None
    ) -> dict[str, Any]:
        """
        Create a track with spec and plan from planning results.

        Args:
            title: Track title
            description: Track description
            spike_id: Optional spike ID that led to this track
            priority: Track priority (default: "high")
            requirements: List of requirements (strings or (req, priority) tuples)
            phases: List of (phase_name, tasks) tuples for the plan

        Returns:
            Dict with track, spec, and plan details

        Example:
            >>> sdk = SDK(agent="claude")
            >>> track_info = sdk.create_track_from_plan(
            ...     title="User Authentication System",
            ...     description="OAuth 2.0 with JWT tokens",
            ...     requirements=[
            ...         ("OAuth 2.0 integration", "must-have"),
            ...         ("JWT token management", "must-have"),
            ...         "Password reset flow"
            ...     ],
            ...     phases=[
            ...         ("Phase 1: OAuth", ["Setup providers (2h)", "Callback (2h)"]),
            ...         ("Phase 2: JWT", ["Token signing (2h)", "Refresh (1.5h)"])
            ...     ]
            ... )
        """
        from htmlgraph.track_builder import TrackBuilder

        builder = self.tracks.builder() \
            .title(title) \
            .description(description) \
            .priority(priority)

        # Add reference to planning spike if provided
        if spike_id:
            builder._data["properties"]["planning_spike"] = spike_id

        # Add spec if requirements provided
        if requirements:
            # Convert simple strings to (requirement, "must-have") tuples
            req_list = []
            for req in requirements:
                if isinstance(req, str):
                    req_list.append((req, "must-have"))
                else:
                    req_list.append(req)

            builder.with_spec(
                overview=description,
                context=f"Track created from planning spike: {spike_id}" if spike_id else "",
                requirements=req_list,
                acceptance_criteria=[]
            )

        # Add plan if phases provided
        if phases:
            builder.with_plan_phases(phases)

        track = builder.create()

        return {
            "track_id": track.id,
            "title": track.title,
            "has_spec": bool(requirements),
            "has_plan": bool(phases),
            "spike_id": spike_id,
            "priority": priority
        }

    def smart_plan(
        self,
        description: str,
        create_spike: bool = True,
        timebox_hours: float = 4.0
    ) -> dict[str, Any]:
        """
        Smart planning workflow: analyzes project context and creates spike or track.

        This is the main entry point for planning new work. It:
        1. Checks current project state
        2. Provides context from strategic analytics
        3. Creates a planning spike or track as appropriate

        Args:
            description: What you want to plan (e.g., "User authentication system")
            create_spike: Create a spike for research (default: True)
            timebox_hours: If creating spike, time limit (default: 4 hours)

        Returns:
            Dict with planning context and created spike/track info

        Example:
            >>> sdk = SDK(agent="claude")
            >>> plan = sdk.smart_plan(
            ...     "Real-time notifications system",
            ...     create_spike=True
            ... )
            >>> print(f"Created: {plan['spike_id']}")
            >>> print(f"Context: {plan['project_context']}")
        """
        # Get project context from strategic analytics
        bottlenecks = self.find_bottlenecks(top_n=3)
        risks = self.assess_risks()
        parallel = self.get_parallel_work(max_agents=5)

        context = {
            "bottlenecks_count": len(bottlenecks),
            "high_risk_count": risks["high_risk_count"],
            "parallel_capacity": parallel["max_parallelism"],
            "description": description
        }

        if create_spike:
            spike = self.start_planning_spike(
                title=f"Plan: {description}",
                context=f"Project context:\n- {len(bottlenecks)} bottlenecks\n- {risks['high_risk_count']} high-risk items\n- {parallel['max_parallelism']} parallel capacity",
                timebox_hours=timebox_hours
            )

            return {
                "type": "spike",
                "spike_id": spike.id,
                "title": spike.title,
                "status": spike.status,
                "timebox_hours": timebox_hours,
                "project_context": context,
                "next_steps": [
                    "Research and design the solution",
                    "Complete spike steps",
                    "Use SDK.create_track_from_plan() to create track"
                ]
            }
        else:
            # Direct track creation (for when you already know what to do)
            track_info = self.create_track_from_plan(
                title=description,
                description=f"Planned with context: {context}"
            )

            return {
                "type": "track",
                **track_info,
                "project_context": context,
                "next_steps": [
                    "Create features from track plan",
                    "Link features to track",
                    "Start implementation"
                ]
            }
