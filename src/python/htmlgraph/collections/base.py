from __future__ import annotations

import logging

logger = logging.getLogger(__name__)

"""
Base collection class for managing nodes.

Provides common collection functionality for all node types
with lazy-loading, filtering, and batch operations.
"""


from collections.abc import Callable, Iterator
from contextlib import contextmanager
from datetime import datetime
from typing import TYPE_CHECKING, Any, Generic, Literal, TypeVar, cast

from htmlgraph.exceptions import ClaimConflictError, NodeNotFoundError

if TYPE_CHECKING:
    from htmlgraph.graph import HtmlGraph
    from htmlgraph.models import Node
    from htmlgraph.sdk import SDK

CollectionT = TypeVar("CollectionT", bound="BaseCollection")


class BaseCollection(Generic[CollectionT]):
    """
    Generic collection interface for any node type.

    Provides common functionality for managing collections of nodes:
    - Lazy-loading of graph data
    - Filtering and querying
    - Batch operations (update, delete, assign)
    - Agent claim/release workflow

    Subclasses should override `_collection_name` and `_node_type` class attributes.

    Example:
        >>> class FeatureCollection(BaseCollection['FeatureCollection']):
        ...     _collection_name = "features"
        ...     _node_type = "feature"
        ...
        >>> sdk = SDK(agent="claude")
        >>> features = sdk.features.where(status="todo", priority="high")
    """

    _collection_name: str = "nodes"  # Override in subclasses
    _node_type: str = "node"  # Override in subclasses
    _builder_class: type | None = (
        None  # Override in subclasses to enable builder pattern
    )

    def __init__(
        self,
        sdk: SDK,
        collection_name: str | None = None,
        node_type: str | None = None,
    ):
        """
        Initialize a collection.

        Args:
            sdk: Parent SDK instance
            collection_name: Name of the collection (e.g., "features", "bugs")
                           Defaults to class attribute if not provided
            node_type: Node type to filter by (e.g., "feature", "bug")
                      Defaults to class attribute if not provided
        """
        self._sdk = sdk
        self._collection_name = collection_name or self._collection_name
        self._node_type = node_type or self._node_type
        self._graph: HtmlGraph | None = None  # Lazy-loaded
        self._ref_manager: Any = None  # Set by SDK during initialization

    def _ensure_graph(self) -> HtmlGraph:
        """
        Get or initialize the graph for this collection.

        Uses SDK's shared graph instances where available to avoid creating
        multiple graph objects for the same collection. Creates a new instance
        for unrecognized collections.

        Returns:
            HtmlGraph instance for this collection

        Note:
            This method is lazy - the graph is only loaded on first access.
        """
        if self._graph is None:
            # Use SDK's shared graph instances to avoid multiple graph objects
            if self._collection_name == "features" and hasattr(self._sdk, "_graph"):
                self._graph = self._sdk._graph
            elif self._collection_name == "bugs" and hasattr(self._sdk, "_bugs_graph"):
                self._graph = self._sdk._bugs_graph
            else:
                # For other collections, create a new graph instance
                from htmlgraph.graph import HtmlGraph

                collection_path = self._sdk._directory / self._collection_name
                self._graph = HtmlGraph(collection_path, auto_load=True)

            # Ensure graph is loaded
            if not self._graph._nodes:
                self._graph.reload()

        return self._graph

    def __getattribute__(self, name: str) -> Any:
        """
        Override attribute access to provide helpful error messages.

        When an attribute doesn't exist, provides suggestions for common
        mistakes and similar method names to improve discoverability.

        Args:
            name: Attribute name being accessed

        Returns:
            The requested attribute

        Raises:
            AttributeError: With helpful suggestions if attribute not found
        """
        try:
            return object.__getattribute__(self, name)
        except AttributeError as e:
            # Get available methods
            available = [m for m in dir(self) if not m.startswith("_")]

            # Common mistakes mapping
            common_mistakes = {
                "mark_complete": "mark_done",
                "complete": "Use complete(node_id) for single item or mark_done([ids]) for batch",
                "finish": "mark_done",
                "end": "mark_done",
                "update_status": "edit() context manager or batch_update()",
                "mark_as_done": "mark_done",
                "set_done": "mark_done",
                "complete_all": "mark_done",
            }

            suggestions = []
            if name in common_mistakes:
                suggestions.append(f"Did you mean: {common_mistakes[name]}")

            # Find similar method names
            similar = [
                m
                for m in available
                if name.lower() in m.lower() or m.lower() in name.lower()
            ]
            if similar:
                suggestions.append(f"Similar methods: {', '.join(similar[:5])}")

            # Build helpful error message
            error_msg = f"'{type(self).__name__}' has no attribute '{name}'."
            if suggestions:
                error_msg += "\n\n" + "\n".join(suggestions)
            error_msg += f"\n\nAvailable methods: {', '.join(available[:15])}"
            error_msg += "\n\nTip: Use sdk.help() to see all available operations."

            raise AttributeError(error_msg) from e

    def __dir__(self) -> list[str]:
        """
        Return attributes with most useful ones first.

        Orders attributes to show commonly-used methods first in auto-complete
        and help() output, improving discoverability for new users.

        Returns:
            List of attribute names, ordered by priority then alphabetically
        """
        priority = [
            # Creation and retrieval
            "create",
            "get",
            "all",
            "where",
            "filter",
            # Work management
            "start",
            "complete",
            "claim",
            "release",
            # Editing
            "edit",
            "update",
            # Batch operations
            "mark_done",
            "assign",
            "batch_update",
            # Deletion
            "delete",
            "batch_delete",
        ]
        # Get all attributes
        all_attrs = object.__dir__(self)
        # Separate into priority, regular, and dunder attributes
        regular = [a for a in all_attrs if not a.startswith("_") and a not in priority]
        dunder = [a for a in all_attrs if a.startswith("_")]
        # Return priority items first, then regular, then dunder
        return priority + regular + dunder

    def set_ref_manager(self, ref_manager: Any) -> None:
        """
        Set the ref manager for this collection.

        Called by SDK during initialization to enable short ref support.

        Args:
            ref_manager: RefManager instance from SDK
        """
        self._ref_manager = ref_manager

    def get_ref(self, node_id: str) -> str | None:
        """
        Get short ref for a node in this collection.

        Convenience method to get ref without accessing SDK directly.

        Args:
            node_id: Full node ID like "feat-a1b2c3d4"

        Returns:
            Short ref like "@f1", or None if ref manager not available

        Example:
            >>> feature = sdk.features.get("feat-abc123")
            >>> ref = sdk.features.get_ref(feature.id)
            >>> logger.info("%s", ref)  # "@f1"
        """
        if self._ref_manager:
            result = self._ref_manager.get_ref(node_id)
            return cast(str | None, result)
        return None

    def create(
        self, title: str, priority: str = "medium", status: str = "todo", **kwargs: Any
    ) -> Any:
        """
        Create a new node in this collection.

        If `_builder_class` is set, returns a builder instance for fluent interface.
        Otherwise, creates and saves a simple Node directly.

        Args:
            title: Node title
            priority: Priority level (low, medium, high, critical)
            status: Status (todo, in-progress, blocked, done)
            **kwargs: Additional node properties

        Returns:
            Builder instance if `_builder_class` is set, else created Node instance

        Raises:
            ValueError: If node with same ID already exists (when using simple creation)
            ValidationError: If invalid node properties provided

        Example:
            >>> # With builder (FeatureCollection, BugCollection, etc.)
            >>> feature = sdk.features.create("User Auth") \\
            ...     .set_priority("high") \\
            ...     .save()
            >>>
            >>> # Without builder (simple collections)
            >>> node = sdk.nodes.create("Simple task", priority="medium")
        """
        # If builder class is configured, use it
        if self._builder_class is not None:
            # Pass priority and status to builder via kwargs
            return self._builder_class(
                self._sdk, title, priority=priority, status=status, **kwargs
            )

        # Fallback to simple node creation
        from htmlgraph.ids import generate_id
        from htmlgraph.models import Node

        # Generate ID based on node type
        node_id = generate_id(node_type=self._node_type, title=title)

        # Create node
        node = Node(
            id=node_id,
            title=title,
            type=self._node_type,
            priority=cast(Literal["low", "medium", "high", "critical"], priority),
            status=cast(
                Literal[
                    "todo", "in-progress", "blocked", "done", "active", "ended", "stale"
                ],
                status,
            ),
            **kwargs,
        )

        # Add to graph
        graph = self._ensure_graph()
        graph.add(node)

        return node

    def get(self, node_id: str) -> Node | None:
        """
        Get a node by ID.

        Args:
            node_id: Node ID to retrieve

        Returns:
            Node if found, None otherwise

        Example:
            >>> feature = sdk.features.get("feat-001")
        """
        return cast("Node | None", self._ensure_graph().get(node_id))

    @contextmanager
    def edit(self, node_id: str) -> Iterator[Node]:
        """
        Context manager for editing a node.

        Auto-saves on exit.

        Args:
            node_id: Node ID to edit

        Yields:
            The node to edit

        Raises:
            NodeNotFoundError: If node not found

        Example:
            >>> with sdk.features.edit("feat-001") as feature:
            ...     feature.status = "in-progress"
        """
        graph = self._ensure_graph()
        node = graph.get(node_id)
        if not node:
            raise NodeNotFoundError(self._node_type, node_id)

        yield node

        # Auto-save on exit
        graph.update(node)

    def where(
        self,
        status: str | None = None,
        priority: str | None = None,
        track: str | None = None,
        assigned_to: str | None = None,
        **extra_filters: Any,
    ) -> list[Node]:
        """
        Query nodes with filters.

        Args:
            status: Filter by status (e.g., "todo", "in-progress", "done")
            priority: Filter by priority (e.g., "low", "medium", "high")
            track: Filter by track_id
            assigned_to: Filter by agent_assigned
            **extra_filters: Additional attribute filters

        Returns:
            List of matching nodes

        Example:
            >>> high_priority = sdk.features.where(status="todo", priority="high")
            >>> assigned = sdk.features.where(assigned_to="claude")
        """

        def matches(node: Node) -> bool:
            if node.type != self._node_type:
                return False
            if status and getattr(node, "status", None) != status:
                return False
            if priority and getattr(node, "priority", None) != priority:
                return False
            if track and getattr(node, "track_id", None) != track:
                return False
            if assigned_to and getattr(node, "agent_assigned", None) != assigned_to:
                return False

            # Check extra filters
            for key, value in extra_filters.items():
                if getattr(node, key, None) != value:
                    return False

            return True

        return cast("list[Node]", self._ensure_graph().filter(matches))

    def filter(self, predicate: Callable[[Node], bool]) -> list[Node]:
        """
        Filter nodes using a custom predicate function.

        Args:
            predicate: A callable that takes a Node and returns True if it matches

        Returns:
            List of nodes that match the predicate

        Example:
            >>> # Find features with "High" in title
            >>> high_priority = sdk.features.filter(lambda f: "High" in f.title)
            >>>
            >>> # Find features created in the last week
            >>> from datetime import datetime, timedelta
            >>> recent = sdk.features.filter(
            ...     lambda f: f.created > datetime.now() - timedelta(days=7)
            ... )
            >>>
            >>> # Complex multi-condition filter
            >>> urgent = sdk.features.filter(
            ...     lambda f: f.priority == "high" and f.status == "todo"
            ... )
        """

        def matches(node: Node) -> bool:
            # First filter by type, then apply user predicate
            if node.type != self._node_type:
                return False
            return predicate(node)

        return cast("list[Node]", self._ensure_graph().filter(matches))

    def all(self) -> list[Node]:
        """
        Get all nodes of this type.

        Returns:
            List of all nodes in this collection

        Example:
            >>> all_features = sdk.features.all()
        """
        return [n for n in self._ensure_graph() if n.type == self._node_type]

    def delete(self, node_id: str) -> bool:
        """
        Delete a node.

        Args:
            node_id: Node ID to delete

        Returns:
            True if deleted, False if not found

        Example:
            >>> sdk.features.delete("feat-001")
        """
        graph = self._ensure_graph()
        return cast(bool, graph.delete(node_id))

    def batch_delete(self, node_ids: list[str]) -> int:
        """
        Delete multiple nodes in batch.

        Args:
            node_ids: List of node IDs to delete

        Returns:
            Number of nodes successfully deleted

        Example:
            >>> count = sdk.features.batch_delete(["feat-001", "feat-002", "feat-003"])
            >>> logger.info(f"Deleted {count} features")
        """
        graph = self._ensure_graph()
        return cast(int, graph.batch_delete(node_ids))

    def update(self, node: Node) -> Node:
        """
        Update a node.

        Args:
            node: Node to update

        Returns:
            Updated node

        Raises:
            NodeNotFoundError: If node doesn't exist in the graph

        Example:
            >>> feature.status = "done"
            >>> sdk.features.update(feature)
        """
        node.updated = datetime.now()
        self._ensure_graph().update(node)
        return node

    def batch_update(self, node_ids: list[str], updates: dict[str, Any]) -> int:
        """
        Vectorized batch update operation.

        Args:
            node_ids: List of node IDs to update
            updates: Dictionary of attribute: value pairs to update

        Returns:
            Number of nodes successfully updated

        Example:
            >>> sdk.features.batch_update(
            ...     ["feat-1", "feat-2"],
            ...     {"status": "done", "agent_assigned": "claude"}
            ... )
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

    def mark_done(self, node_ids: list[str]) -> dict[str, Any]:
        """
        Batch mark nodes as done.

        Args:
            node_ids: List of node IDs to mark as done

        Returns:
            Dict with 'success_count', 'failed_ids', and 'warnings'

        Example:
            >>> result = sdk.features.mark_done(["feat-001", "feat-002"])
            >>> logger.info(f"Completed {result['success_count']} of {len(node_ids)}")
            >>> if result['failed_ids']:
            ...     logger.info(f"Failed: {result['failed_ids']}")
        """
        graph = self._ensure_graph()
        results: dict[str, Any] = {"success_count": 0, "failed_ids": [], "warnings": []}

        for node_id in node_ids:
            try:
                node = graph.get(node_id)
                if not node:
                    results["failed_ids"].append(node_id)
                    results["warnings"].append(f"Node {node_id} not found")
                    continue

                node.status = "done"
                node.updated = datetime.now()
                graph.update(node)
                results["success_count"] += 1

                # Log completion event to SQLite
                try:
                    self._sdk._log_event(
                        event_type="tool_call",
                        tool_name="SDK.mark_done",
                        input_summary=f"Mark {self._node_type} done: {node_id}",
                        output_summary=f"Marked {node_id} as done",
                        context={
                            "collection": self._collection_name,
                            "node_id": node_id,
                            "node_type": self._node_type,
                            "title": node.title,
                        },
                        cost_tokens=25,
                    )
                except Exception as e:
                    import logging

                    logging.debug(f"Event logging failed for mark_done: {e}")

            except Exception as e:
                results["failed_ids"].append(node_id)
                results["warnings"].append(f"Failed to mark {node_id}: {str(e)}")

        return results

    def assign(self, node_ids: list[str], agent: str) -> int:
        """
        Batch assign nodes to an agent.

        Args:
            node_ids: List of node IDs to assign
            agent: Agent ID to assign to

        Returns:
            Number of nodes assigned

        Example:
            >>> sdk.features.assign(["feat-001", "feat-002"], "claude")
        """
        updates = {"agent_assigned": agent, "status": "in-progress"}
        return self.batch_update(node_ids, updates)

    def start(self, node_id: str, agent: str | None = None) -> Node | None:
        """
        Start working on a node (feature/bug/etc).

        Delegates to SessionManager if available for smart tracking:
        1. Check WIP limits
        2. Ensure not claimed by others
        3. Auto-claim for agent
        4. Link to active session
        5. Log 'FeatureStart' event

        Falls back to simple status update if SessionManager not available.

        Args:
            node_id: Node ID to start
            agent: Agent ID (defaults to SDK agent)

        Returns:
            Updated Node, or None if not found

        Raises:
            NodeNotFoundError: If node not found

        Example:
            >>> sdk.features.start('feat-abc123')
            >>> sdk.features.start('feat-xyz', agent='claude')
        """
        agent = agent or self._sdk.agent

        # Use SessionManager if available (smart tracking)
        if hasattr(self._sdk, "session_manager"):
            result = cast(
                "Node | None",
                self._sdk.session_manager.start_feature(
                    feature_id=node_id,
                    collection=self._collection_name,
                    agent=agent,
                    log_activity=True,
                ),
            )
            # Write session-scoped active work item (Phase 2: fast lookup).
            # We write to two session ID namespaces so both hook-created rows
            # (keyed by Claude Code UUID via HTMLGRAPH_SESSION_ID) and SDK-created
            # rows (keyed by the internal sess-xxx ID from session_manager) are
            # updated. This handles parallel sessions correctly: each window has
            # its own HTMLGRAPH_SESSION_ID, so the correct row is always updated.
            if hasattr(self._sdk, "_db") and self._sdk._db:
                import json as _json
                import os
                import time as _time

                # Primary: Claude Code session UUID (set by session-start.py hook).
                # This is the key used by hooks (user-prompt-submit, event_tracker).
                claude_session_id = os.environ.get("HTMLGRAPH_SESSION_ID")

                # Fallback 1: read .active-session marker written by session-start.py
                # hook.  Needed when CLAUDE_ENV_FILE is unavailable so the env var
                # was never propagated into this Bash subprocess.
                if not claude_session_id:
                    try:
                        marker_path = self._sdk._directory / ".active-session"
                        if marker_path.exists():
                            marker = _json.loads(marker_path.read_text())
                            marker_session = marker.get("session_id")
                            marker_ts = marker.get("timestamp", 0)
                            # Only use if marker is fresh (< 24 hours old)
                            if marker_session and (_time.time() - marker_ts) < 86400:
                                claude_session_id = marker_session
                    except Exception:
                        pass

                # Fallback 2: most recent active session in SQLite
                if not claude_session_id:
                    try:
                        conn = self._sdk._db.connection
                        if conn:
                            row = conn.execute(
                                "SELECT session_id FROM sessions"
                                " WHERE status = 'active'"
                                " ORDER BY created_at DESC LIMIT 1"
                            ).fetchone()
                            if row:
                                claude_session_id = row[0]
                    except Exception:
                        pass

                if claude_session_id:
                    try:
                        self._sdk._db.set_active_work_item(claude_session_id, node_id)
                    except Exception as e:
                        logger.debug(
                            f"set_active_work_item (claude_session) failed (non-fatal): {e}"
                        )
                # Secondary: internal sess-xxx ID from session_manager (used by tests
                # and SDK-only code paths that don't go through hooks).
                active_session = self._sdk.session_manager.get_active_session()
                if active_session and active_session.id != claude_session_id:
                    try:
                        self._sdk._db.set_active_work_item(active_session.id, node_id)
                    except Exception as e:
                        logger.debug(
                            f"set_active_work_item (active_session) failed (non-fatal): {e}"
                        )

                # Ensure feature exists in SQLite features table (for statusline title lookup)
                try:
                    node = self.get(node_id)
                    if node:
                        conn = self._sdk._db.connection
                        if conn:
                            conn.execute(
                                """INSERT INTO features
                                   (id, type, title, status, priority)
                                   VALUES (?, ?, ?, ?, ?)
                                   ON CONFLICT(id) DO UPDATE SET
                                   title=excluded.title, status=excluded.status""",
                                (
                                    node_id,
                                    node.type,
                                    node.title,
                                    node.status,
                                    node.priority,
                                ),
                            )
                            conn.commit()
                        # Log step count so it's visible in the terminal
                        if node.steps:
                            done = sum(1 for s in node.steps if s.completed)
                            total = len(node.steps)
                            logger.info(
                                f"Feature {node_id}: {done}/{total} steps complete"
                            )
                            remaining = [
                                s.description for s in node.steps if not s.completed
                            ]
                            if remaining:
                                logger.info(f"  Next: {remaining[0][:60]}")
                except Exception as e:
                    logger.debug(f"Feature upsert failed (non-fatal): {e}")
            return result

        # Fallback to simple update (no session/events)
        node = self.get(node_id)
        if not node:
            raise NodeNotFoundError(self._node_type, node_id)

        node.status = "in-progress"
        node.updated = datetime.now()
        self._ensure_graph().update(node)
        return node

    def complete(
        self,
        node_id: str,
        agent: str | None = None,
        transcript_id: str | None = None,
    ) -> Node | None:
        """
        Mark a node as complete.

        Delegates to SessionManager if available for event logging and
        transcript linking:
        1. Auto-complete all pending steps
        2. Update status to 'done'
        3. Log 'FeatureComplete' event
        4. Release claim (optional behavior)
        5. Link transcript if provided (for parallel agent tracking)

        Falls back to simple status update if SessionManager not available.

        Args:
            node_id: Node ID to complete
            agent: Agent ID (defaults to SDK agent)
            transcript_id: Optional transcript ID (agent session) that implemented
                          this feature. Used for parallel agent tracking.

        Returns:
            Updated Node, or None if not found

        Raises:
            NodeNotFoundError: If node not found

        Example:
            >>> sdk.features.complete('feat-abc123')
            >>> sdk.features.complete('feat-xyz', agent='claude', transcript_id='trans-123')
        """
        agent = agent or self._sdk.agent

        # Use SessionManager if available
        if hasattr(self._sdk, "session_manager"):
            result = cast(
                "Node | None",
                self._sdk.session_manager.complete_feature(
                    feature_id=node_id,
                    collection=self._collection_name,
                    agent=agent,
                    log_activity=True,
                    transcript_id=transcript_id,
                ),
            )
            # Clear session-scoped active work item if this was the active one.
            # Mirror the dual-write pattern from start(): clear from both the
            # Claude Code UUID row and the internal sess-xxx row.
            if hasattr(self._sdk, "_db") and self._sdk._db:
                import json as _json
                import os
                import time as _time

                claude_session_id = os.environ.get("HTMLGRAPH_SESSION_ID")

                # Fallback 1: read .active-session marker written by session-start.py
                if not claude_session_id:
                    try:
                        marker_path = self._sdk._directory / ".active-session"
                        if marker_path.exists():
                            marker = _json.loads(marker_path.read_text())
                            marker_session = marker.get("session_id")
                            marker_ts = marker.get("timestamp", 0)
                            if marker_session and (_time.time() - marker_ts) < 86400:
                                claude_session_id = marker_session
                    except Exception:
                        pass

                # Fallback 2: most recent active session in SQLite
                if not claude_session_id:
                    try:
                        conn = self._sdk._db.connection
                        if conn:
                            row = conn.execute(
                                "SELECT session_id FROM sessions"
                                " WHERE status = 'active'"
                                " ORDER BY created_at DESC LIMIT 1"
                            ).fetchone()
                            if row:
                                claude_session_id = row[0]
                    except Exception:
                        pass

                active_session = self._sdk.session_manager.get_active_session()
                session_ids_to_check = []
                if claude_session_id:
                    session_ids_to_check.append(claude_session_id)
                if active_session and active_session.id != claude_session_id:
                    session_ids_to_check.append(active_session.id)
                for sid in session_ids_to_check:
                    try:
                        current_active = self._sdk._db.get_active_work_item_for_session(
                            sid
                        )
                        if current_active == node_id:
                            self._sdk._db.clear_active_work_item(sid)
                    except Exception as e:
                        logger.debug(f"clear_active_work_item failed (non-fatal): {e}")

            # Update features table in SQLite so get_open_work_items() no longer
            # returns this item as 'in-progress'.  Mirror the upsert in start().
            try:
                node = result or self.get(node_id)
                if node:
                    conn = self._sdk._db.connection
                    if conn:
                        conn.execute(
                            """INSERT INTO features
                               (id, type, title, status, priority)
                               VALUES (?, ?, ?, ?, ?)
                               ON CONFLICT(id) DO UPDATE SET
                               title=excluded.title, status=excluded.status""",
                            (
                                node_id,
                                node.type,
                                node.title,
                                node.status,
                                node.priority,
                            ),
                        )
                        conn.commit()
                # Invalidate the prompt_analyzer work-items cache so the next
                # prompt submission does not show this item as 'in-progress'.
                try:
                    from htmlgraph.hooks.prompt_analyzer import (
                        invalidate_work_items_cache,
                    )

                    invalidate_work_items_cache(self._sdk._directory / "htmlgraph.db")
                except Exception:  # noqa: BLE001
                    pass
            except Exception as e:
                logger.debug(f"Feature status sync to SQLite failed (non-fatal): {e}")

            return result

        # Fallback
        node = self.get(node_id)
        if not node:
            raise NodeNotFoundError(self._node_type, node_id)

        # Auto-complete all pending steps
        for step in node.steps:
            if not step.completed:
                step.completed = True
                step.agent = agent
                step.timestamp = datetime.now()

        node.status = "done"
        node.updated = datetime.now()
        self._ensure_graph().update(node)
        return node

    def claim(self, node_id: str, agent: str | None = None) -> Node | None:
        """
        Claim a node for an agent.

        Delegates to SessionManager if available for ownership tracking:
        1. Check ownership rules
        2. Update assignment
        3. Log 'FeatureClaim' event

        Falls back to simple assignment if SessionManager not available.

        Args:
            node_id: Node ID to claim
            agent: Agent ID (defaults to SDK agent)

        Returns:
            The claimed Node, or None if not found

        Raises:
            ValueError: If agent not provided and SDK has no agent
            NodeNotFoundError: If node not found
            ClaimConflictError: If node already claimed by different agent

        Example:
            >>> sdk.features.claim('feat-abc123')
            >>> sdk.features.claim('feat-xyz', agent='claude')
        """
        agent = agent or self._sdk.agent
        if not agent:
            raise ValueError("Agent ID required for claiming")

        # Use SessionManager if available
        if hasattr(self._sdk, "session_manager"):
            return cast(
                "Node | None",
                self._sdk.session_manager.claim_feature(
                    feature_id=node_id, collection=self._collection_name, agent=agent
                ),
            )

        # Fallback logic
        graph = self._ensure_graph()
        node = cast("Node | None", graph.get(node_id))
        if not node:
            raise NodeNotFoundError(self._node_type, node_id)

        if node.agent_assigned and node.agent_assigned != agent:
            raise ClaimConflictError(node_id, node.agent_assigned)

        node.agent_assigned = agent
        node.claimed_at = datetime.now()
        node.status = "in-progress"
        node.updated = datetime.now()
        graph.update(node)
        return node

    def release(self, node_id: str, agent: str | None = None) -> Node | None:
        """
        Release a claimed node.

        Delegates to SessionManager if available for ownership tracking:
        1. Verify ownership
        2. Clear assignment
        3. Log 'FeatureRelease' event

        Falls back to simple assignment clearing if SessionManager not available.

        Args:
            node_id: Node ID to release
            agent: Agent ID (defaults to SDK agent)

        Returns:
            The released Node, or None if not found

        Raises:
            NodeNotFoundError: If node not found

        Example:
            >>> sdk.features.release('feat-abc123')
            >>> sdk.features.release('feat-xyz', agent='claude')
        """
        # SessionManager.release_feature requires an agent to verify ownership
        agent = agent or self._sdk.agent

        # Use SessionManager if available
        if hasattr(self._sdk, "session_manager") and agent:
            return cast(
                "Node | None",
                self._sdk.session_manager.release_feature(
                    feature_id=node_id, collection=self._collection_name, agent=agent
                ),
            )

        # Fallback logic
        graph = self._ensure_graph()
        node = cast("Node | None", graph.get(node_id))
        if not node:
            raise NodeNotFoundError(self._node_type, node_id)

        node.agent_assigned = None
        node.claimed_at = None
        node.claimed_by_session = None
        node.status = "todo"
        node.updated = datetime.now()
        graph.update(node)
        return node

    # ------------------------------------------------------------------
    # Graph Edge Operations (dual-write: HTML + SQLite)
    # ------------------------------------------------------------------

    def add_edge(
        self,
        from_id: str,
        to_id: str,
        relationship: str,
        title: str | None = None,
    ) -> str | None:
        """
        Add a typed edge between two nodes with dual-write.

        Writes the edge to:
        1. The source node's HTML file (via Node.add_edge)
        2. The SQLite graph_edges table (if DB available)

        Args:
            from_id: Source node ID
            to_id: Target node ID
            relationship: Relationship type (blocks, relates_to, etc.)
            title: Optional human-readable title for the edge

        Returns:
            Edge ID from SQLite if DB write succeeded, None otherwise
        """
        from htmlgraph.models import Edge

        # 1. Update HTML: load source node, add edge, save
        graph = self._ensure_graph()
        source_node = graph.get(from_id)
        if source_node:
            edge = Edge(target_id=to_id, relationship=relationship, title=title)
            source_node.add_edge(edge)
            graph.update(source_node)

        # 2. Write to SQLite if DB is available
        edge_id: str | None = None
        try:
            db = self._sdk._db
            if db:
                # Infer node types from IDs
                from_type = self._infer_node_type(from_id)
                to_type = self._infer_node_type(to_id)
                edge_id = db.insert_graph_edge(
                    from_node_id=from_id,
                    from_node_type=from_type,
                    to_node_id=to_id,
                    to_node_type=to_type,
                    relationship_type=relationship,
                )
        except Exception as e:
            logger.debug(f"SQLite edge write failed: {e}")

        return edge_id

    def related_to(self, node_id: str) -> list[Any]:
        """
        Get nodes related to the given node via any relationship.

        Checks both HTML edges (in-memory graph) and SQLite edges.

        Args:
            node_id: Node ID to find related nodes for

        Returns:
            List of related Node objects (deduplicated)
        """
        related_ids: set[str] = set()

        # Check HTML edges on the source node
        graph = self._ensure_graph()
        node = graph.get(node_id)
        if node:
            for edge_list in node.edges.values():
                for edge in edge_list:
                    related_ids.add(edge.target_id)

        # Check SQLite edges (both directions)
        try:
            db = self._sdk._db
            if db:
                for edge_dict in db.get_graph_edges(node_id, direction="both"):
                    if edge_dict["from_node_id"] == node_id:
                        related_ids.add(edge_dict["to_node_id"])
                    else:
                        related_ids.add(edge_dict["from_node_id"])
        except Exception as e:
            logger.debug(f"SQLite edge query failed: {e}")

        # Resolve to Node objects
        result = []
        for rid in related_ids:
            related_node = graph.get(rid)
            if related_node:
                result.append(related_node)
        return result

    def query_blocked_by(self, node_id: str) -> list[Any]:
        """
        Get nodes that are blocking the given node.

        Args:
            node_id: Node ID to find blockers for

        Returns:
            List of blocking Node objects
        """
        return self._query_edges_by_relationship(node_id, "blocked_by")

    def query_blocks(self, node_id: str) -> list[Any]:
        """
        Get nodes that the given node blocks.

        Args:
            node_id: Node ID to find blocked nodes for

        Returns:
            List of blocked Node objects
        """
        return self._query_edges_by_relationship(node_id, "blocks")

    def edges_of(
        self,
        node_id: str,
        relationship: str | None = None,
    ) -> list[dict[str, Any]]:
        """
        Get all edges for a node, optionally filtered by relationship type.

        Merges edges from both HTML and SQLite sources.

        Args:
            node_id: Node ID to query edges for
            relationship: Optional relationship type filter

        Returns:
            List of edge dictionaries with keys:
            source, target_id, relationship, title
        """
        edges: list[dict[str, Any]] = []
        seen: set[tuple[str, str, str]] = set()

        # HTML edges from in-memory node
        graph = self._ensure_graph()
        node = graph.get(node_id)
        if node:
            for rel_type, edge_list in node.edges.items():
                if relationship and rel_type != relationship:
                    continue
                for edge in edge_list:
                    key = (node_id, edge.target_id, edge.relationship)
                    if key not in seen:
                        seen.add(key)
                        edges.append(
                            {
                                "from_id": node_id,
                                "target_id": edge.target_id,
                                "relationship": edge.relationship,
                                "title": edge.title,
                                "source": "html",
                            }
                        )

        # SQLite edges
        try:
            db = self._sdk._db
            if db:
                db_edges = db.get_graph_edges(
                    node_id,
                    direction="both",
                    relationship_type=relationship,
                )
                for e in db_edges:
                    from_id = e["from_node_id"]
                    to_id = e["to_node_id"]
                    rel = e["relationship_type"]
                    key = (from_id, to_id, rel)
                    if key not in seen:
                        seen.add(key)
                        edges.append(
                            {
                                "from_id": from_id,
                                "target_id": to_id,
                                "relationship": rel,
                                "title": None,
                                "source": "sqlite",
                                "edge_id": e["edge_id"],
                            }
                        )
        except Exception as ex:
            logger.debug(f"SQLite edge query failed: {ex}")

        return edges

    def _query_edges_by_relationship(
        self, node_id: str, relationship: str
    ) -> list[Any]:
        """
        Internal helper: get target nodes for a specific relationship.

        Args:
            node_id: Source node ID
            relationship: Relationship type to filter

        Returns:
            List of target Node objects
        """
        target_ids: set[str] = set()

        # HTML edges
        graph = self._ensure_graph()
        node = graph.get(node_id)
        if node:
            for edge in node.edges.get(relationship, []):
                target_ids.add(edge.target_id)

        # SQLite edges (outgoing only for directional relationships)
        try:
            db = self._sdk._db
            if db:
                db_edges = db.get_graph_edges(
                    node_id,
                    direction="outgoing",
                    relationship_type=relationship,
                )
                for e in db_edges:
                    target_ids.add(e["to_node_id"])
        except Exception as e:
            logger.debug(f"SQLite edge query failed: {e}")

        result = []
        for tid in target_ids:
            target_node = graph.get(tid)
            if target_node:
                result.append(target_node)
        return result

    @staticmethod
    def _infer_node_type(node_id: str) -> str:
        """
        Infer node type from ID prefix.

        Args:
            node_id: Node ID like 'feat-abc123' or 'bug-xyz789'

        Returns:
            Node type string (feature, bug, spike, etc.)
        """
        from htmlgraph.ids import PREFIX_TO_TYPE

        prefix = node_id.split("-")[0] if "-" in node_id else node_id
        return PREFIX_TO_TYPE.get(prefix, "feature")

    def atomic_claim(self, node_id: str, agent: str | None = None) -> bool:
        """
        Atomically claim a work item using SQL compare-and-swap.

        Uses a single UPDATE ... WHERE assignee IS NULL OR assignee = ?
        so only one agent wins when multiple agents race to claim the same item.

        Args:
            node_id: Node ID to claim
            agent: Agent ID (defaults to SDK agent or "unknown")

        Returns:
            True if this agent successfully claimed the item,
            False if the item was already claimed by a different agent.

        Example:
            >>> if sdk.features.atomic_claim('feat-abc123'):
            ...     # we own it, start working
            ...     sdk.features.start('feat-abc123')
            ... else:
            ...     # someone else got there first
            ...     pass
        """
        import sqlite3

        if agent is None:
            agent = self._sdk.agent or "unknown"

        # Use SDK's database path from the DB object
        db_path = self._sdk._db.db_path
        conn = sqlite3.connect(str(db_path), timeout=2.0, check_same_thread=False)
        try:
            cursor = conn.execute(
                "UPDATE features SET assignee = ? WHERE id = ? AND (assignee IS NULL OR assignee = ?)",
                (agent, node_id, agent),
            )
            conn.commit()

            if cursor.rowcount == 1:
                self._update_html_assignee(node_id, agent)
                return True
            return False
        finally:
            conn.close()

    def atomic_unclaim(self, node_id: str) -> None:
        """
        Release the atomic claim on a work item.

        Clears the ``assignee`` column in SQLite and removes the
        ``data-assignee`` attribute from the HTML file so both stores
        stay in sync.

        Args:
            node_id: Node ID to unclaim

        Example:
            >>> sdk.features.atomic_unclaim('feat-abc123')
        """
        import sqlite3

        # Use SDK's database path from the DB object
        db_path = self._sdk._db.db_path
        conn = sqlite3.connect(str(db_path), timeout=2.0, check_same_thread=False)
        try:
            conn.execute("UPDATE features SET assignee = NULL WHERE id = ?", (node_id,))
            conn.commit()
            self._update_html_assignee(node_id, None)
        finally:
            conn.close()

    def _update_html_assignee(self, node_id: str, agent: str | None) -> None:
        """
        Sync the ``data-assignee`` HTML attribute to match the SQLite value.

        Searches the standard work-item sub-directories (features, bugs,
        spikes) for an HTML file whose name matches *node_id* and updates
        (or removes) the ``data-assignee`` attribute in place.

        Args:
            node_id: Node ID whose HTML file should be updated
            agent: New assignee string, or None to remove the attribute
        """
        import re
        from pathlib import Path

        graph_dir = Path(self._sdk._directory)
        for subdir in ("features", "bugs", "spikes"):
            html_path = graph_dir / subdir / f"{node_id}.html"
            if html_path.exists():
                content = html_path.read_text()
                if agent:
                    if "data-assignee=" in content:
                        content = re.sub(
                            r'data-assignee="[^"]*"',
                            f'data-assignee="{agent}"',
                            content,
                        )
                    else:
                        content = content.replace(
                            "data-status=",
                            f'data-assignee="{agent}" data-status=',
                            1,
                        )
                else:
                    content = re.sub(r'\s*data-assignee="[^"]*"', "", content)
                html_path.write_text(content)
                break
