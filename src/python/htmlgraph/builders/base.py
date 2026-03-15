from __future__ import annotations

"""
Base builder class for fluent node creation.

Provides common builder patterns shared across all node types.
"""


from datetime import datetime
from typing import TYPE_CHECKING, Any, Generic, TypeVar

if TYPE_CHECKING:
    from htmlgraph.models import Node
    from htmlgraph.sdk import SDK

from htmlgraph.ids import generate_id
from htmlgraph.models import Edge, Step

# Generic type for the builder subclass
BuilderT = TypeVar("BuilderT", bound="BaseBuilder")

# For type hints in helper methods
from typing_extensions import Self


class BaseBuilder(Generic[BuilderT]):
    """
    Base builder for creating nodes with fluent interface.

    Provides common methods shared across all node types:
    - Priority and status management
    - Step management
    - Relationship management (blocks, blocked_by)
    - Description/content
    - Save functionality

    Subclasses should:
    1. Set `node_type` class attribute
    2. Override `__init__` to set node-specific defaults
    3. Add node-specific builder methods
    """

    node_type: str = "node"  # Override in subclasses

    def __init__(self, sdk: SDK, title: str, **kwargs: Any):
        """
        Initialize builder.

        Args:
            sdk: Parent SDK instance
            title: Node title
            **kwargs: Additional node data
        """
        self._sdk = sdk
        self._data: dict[str, Any] = {
            "title": title,
            "type": self.node_type,
            "status": "todo",
            "priority": "medium",
            "steps": [],
            "edges": {},
            "properties": {},
            **kwargs,
        }

    # Helper methods for common patterns
    def _add_edge(
        self, edge_type: str, target_id: str, relationship: str | None = None
    ) -> Self:
        """Add an edge to the node being built."""
        if edge_type not in self._data["edges"]:
            self._data["edges"][edge_type] = []
        self._data["edges"][edge_type].append(
            Edge(target_id=target_id, relationship=relationship or edge_type)
        )
        return self

    def _set_date(self, field_name: str, date_value: Any) -> Self:
        """Set a date field in properties, converting to ISO format if needed."""
        iso_date = (
            date_value.isoformat() if hasattr(date_value, "isoformat") else date_value
        )
        self._data["properties"][field_name] = iso_date
        return self

    def _append_to_list(self, field_name: str, value: Any) -> Self:
        """Append a value to a list field in properties, creating list if needed."""
        if field_name not in self._data["properties"]:
            self._data["properties"][field_name] = []
        self._data["properties"][field_name].append(value)
        return self

    def set_priority(self, priority: str) -> BuilderT:
        """Set node priority (low, medium, high, critical)."""
        self._data["priority"] = priority
        return self  # type: ignore

    def set_status(self, status: str) -> BuilderT:
        """Set node status (todo, in-progress, blocked, done, etc.)."""
        self._data["status"] = status
        return self  # type: ignore

    def add_step(self, description: str, step_id: str | None = None) -> BuilderT:
        """Add a single implementation step."""
        self._data["steps"].append(Step(description=description, step_id=step_id))
        return self  # type: ignore

    def add_steps(self, descriptions: list[str]) -> BuilderT:
        """Add multiple implementation steps."""
        for desc in descriptions:
            self._data["steps"].append(Step(description=desc))
        return self  # type: ignore

    def set_description(self, description: str) -> BuilderT:
        """Set node description/content."""
        self._data["content"] = f"<p>{description}</p>"
        return self  # type: ignore

    def blocks(self, node_id: str) -> BuilderT:
        """Add blocking relationship (this node blocks another)."""
        return self._add_edge("blocks", node_id)  # type: ignore

    def blocked_by(self, node_id: str) -> BuilderT:
        """Add blocked-by relationship (this node is blocked by another)."""
        return self._add_edge("blocked_by", node_id)  # type: ignore

    def relates_to(self, other_id: str, rel_type: str | None = None) -> BuilderT:
        """Add a typed relationship edge to another node.

        Args:
            other_id: Target node ID
            rel_type: Relationship type string (e.g., 'depends_on', 'related_to').
                     Defaults to 'relates_to' if not specified.

        Returns:
            Self for method chaining
        """
        if rel_type is None:
            rel_type = "relates_to"
        return self._add_edge(rel_type, other_id)  # type: ignore

    def spawned_from(self, node_id: str) -> BuilderT:
        """Add spawned-from relationship (provenance)."""
        return self._add_edge("spawned_from", node_id)  # type: ignore

    def caused_by(self, node_id: str) -> BuilderT:
        """Add caused-by relationship (causation)."""
        return self._add_edge("caused_by", node_id)  # type: ignore

    def implements(self, node_id: str) -> BuilderT:
        """Add implements relationship (links implementation to spec)."""
        return self._add_edge("implements", node_id)  # type: ignore

    def set_track(self, track_id: str) -> BuilderT:
        """Link to a track."""
        self._data["track_id"] = track_id
        return self  # type: ignore

    def complete_and_handoff(
        self,
        reason: str,
        notes: str | None = None,
        next_agent: str | None = None,
    ) -> BuilderT:
        """
        Mark as complete and create handoff for next agent.

        Args:
            reason: Reason for handoff
            notes: Detailed handoff context/decisions
            next_agent: Next agent to claim (optional)

        Returns:
            Self for method chaining
        """
        self._data["handoff_required"] = True
        self._data["handoff_reason"] = reason
        self._data["handoff_notes"] = notes
        self._data["handoff_timestamp"] = datetime.now()
        return self  # type: ignore

    def save(self) -> Node:
        """
        Save the node and return the Node instance.

        Generates ID if not provided, creates Node instance,
        and adds to the correct collection's graph.

        Returns:
            Created Node instance

        Raises:
            ValueError: If node type requires track_id but none is set
        """
        # Generate collision-resistant ID if not provided
        if "id" not in self._data:
            self._data["id"] = generate_id(
                node_type=self._data.get("type", self.node_type),
                title=self._data.get("title", ""),
            )

        # Auto-assign step_ids to any steps that don't have one yet
        node_id = self._data["id"]
        for i, step in enumerate(self._data.get("steps", [])):
            if isinstance(step, Step) and not step.step_id:
                step.step_id = f"step-{node_id}-{i}"

        # Validate track_id requirement for features
        node_type = self._data.get("type", self.node_type)
        if node_type == "feature" and not self._data.get("track_id"):
            # Get available tracks for helpful error message
            try:
                tracks = self._sdk.tracks.all()
                track_options = "\n".join(
                    [f"  - {track.id}: {track.title}" for track in tracks[:10]]
                )
                if len(tracks) > 10:
                    track_options += f"\n  ... and {len(tracks) - 10} more tracks"

                error_msg = (
                    f"Feature '{self._data.get('title', 'Unknown')}' requires a track linkage.\n\n"
                    f"Use: .set_track('track_id') to link to a track before saving.\n\n"
                    f"Available tracks:\n{track_options or '  (no tracks found)'}\n\n"
                    f"Create a track first: sdk.tracks.create('Track Title')"
                )
            except Exception:
                # Fallback error message if we can't fetch tracks
                error_msg = (
                    f"Feature '{self._data.get('title', 'Unknown')}' requires a track linkage.\n"
                    f"Use: .set_track('track_id') to link to a track before saving."
                )

            raise ValueError(error_msg)

        # Import Node here to avoid circular imports
        from htmlgraph.models import Node

        node = Node(**self._data)

        # Save to the collection's shared graph (not a new instance)
        # This ensures the node is visible via collection.get() immediately
        collection_name = self._data.get("type", self.node_type) + "s"
        collection = getattr(self._sdk, collection_name, None)

        if collection is not None:
            # Use the collection's shared graph
            graph = collection._ensure_graph()
            graph.add(node)
        else:
            # Fallback: create new graph (for collections not yet on SDK)
            from htmlgraph.graph import HtmlGraph

            graph_path = self._sdk._directory / collection_name
            graph = HtmlGraph(graph_path, auto_load=False)
            graph.add(node)

        # Log creation event to SQLite for dashboard observability
        try:
            action_type = self._data.get("type", self.node_type)
            self._sdk._log_event(
                event_type="tool_call",
                tool_name="SDK.create",
                input_summary=f"Create {action_type}: {self._data.get('title', 'Untitled')}",
                output_summary=f"Created {collection_name}/{node.id}",
                context={
                    "collection": collection_name,
                    "node_id": node.id,
                    "node_type": action_type,
                    "title": node.title,
                    "status": self._data.get("status", "todo"),
                    "priority": self._data.get("priority", "medium"),
                },
                cost_tokens=50,
            )
        except Exception as e:
            # Never break save because of logging
            import logging

            logging.debug(f"Event logging failed: {e}")

        # Also log via SessionManager for backward compatibility
        if hasattr(self._sdk, "session_manager") and self._sdk.agent:
            try:
                self._sdk.session_manager._maybe_log_work_item_action(
                    agent=self._sdk.agent,
                    tool="FeatureCreate",
                    summary=f"Created: {collection_name}/{node.id}",
                    feature_id=node.id,
                    payload={
                        "collection": collection_name,
                        "action": "create",
                        "title": node.title,
                    },
                )
            except Exception:
                # Never break save because of logging
                pass

        return node
