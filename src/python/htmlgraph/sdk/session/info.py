"""
Session info mixin for SDK - session start info and active work tracking.

Provides optimized methods for session context gathering.
"""

from __future__ import annotations

import os
import subprocess
from typing import TYPE_CHECKING, Any

if TYPE_CHECKING:
    from pathlib import Path

    from htmlgraph.session_manager import SessionManager
    from htmlgraph.types import ActiveWorkItem, SessionStartInfo


class SessionInfoMixin:
    """
    Mixin providing session info and active work methods to SDK.

    Provides optimized methods for gathering session context in single calls.
    Requires SDK instance with _directory, _agent_id, session_manager attributes.
    """

    _directory: Path
    _agent_id: str | None
    session_manager: SessionManager

    def get_session_start_info(
        self,
        include_git_log: bool = True,
        git_log_count: int = 5,
        analytics_top_n: int = 3,
        analytics_max_agents: int = 3,
    ) -> SessionStartInfo:
        """
        Get comprehensive session start information in a single call.

        Consolidates all information needed for session start into one method,
        reducing context usage from 6+ tool calls to 1.

        Args:
            include_git_log: Include recent git commits (default: True)
            git_log_count: Number of recent commits to include (default: 5)
            analytics_top_n: Number of bottlenecks/recommendations (default: 3)
            analytics_max_agents: Max agents for parallel work analysis (default: 3)

        Returns:
            Dict with comprehensive session start context:
                - status: Project status (nodes, collections, WIP)
                - active_work: Current active work item (if any)
                - features: List of features with status
                - sessions: Recent sessions
                - git_log: Recent commits (if include_git_log=True)
                - analytics: Strategic insights (bottlenecks, recommendations, parallel)

        Note:
            Returns empty dict {} if session context unavailable.
            Always check for expected keys before accessing.

        Example:
            >>> sdk = SDK(agent="claude")
            >>> info = sdk.get_session_start_info()
            >>> logger.info(f"Project: {info['status']['total_nodes']} nodes")
            >>> logger.info(f"WIP: {info['status']['in_progress_count']}")
            >>> if info.get('active_work'):
            ...     logger.info(f"Active: {info['active_work']['title']}")
            >>> for bn in info['analytics']['bottlenecks']:
            ...     logger.info(f"Bottleneck: {bn['title']}")
        """
        result: dict[str, Any] = {}

        # 1. Project status
        result["status"] = self.get_status()  # type: ignore[attr-defined]

        # 2. Active work item (validation status) - always include, even if None
        result["active_work"] = self.get_active_work_item()

        # 3. Features list (simplified)
        features_list: list[dict[str, object]] = []
        for feature in self.features.all():  # type: ignore[attr-defined]
            features_list.append(
                {
                    "id": feature.id,
                    "title": feature.title,
                    "status": feature.status,
                    "priority": feature.priority,
                    "steps_total": len(feature.steps),
                    "steps_completed": sum(1 for s in feature.steps if s.completed),
                }
            )
        result["features"] = features_list

        # 4. Sessions list (recent 20)
        sessions_list: list[dict[str, Any]] = []
        for session in self.sessions.all()[:20]:  # type: ignore[attr-defined]
            sessions_list.append(
                {
                    "id": session.id,
                    "status": session.status,
                    "agent": session.properties.get("agent", "unknown"),
                    "event_count": session.properties.get("event_count", 0),
                    "started": session.created.isoformat()
                    if hasattr(session, "created")
                    else None,
                }
            )
        result["sessions"] = sessions_list

        # 5. Git log (if requested)
        if include_git_log:
            try:
                git_result = subprocess.run(
                    ["git", "log", "--oneline", f"-{git_log_count}"],
                    capture_output=True,
                    text=True,
                    check=True,
                    cwd=self._directory.parent,
                )
                git_lines: list[str] = git_result.stdout.strip().split("\n")
                result["git_log"] = git_lines
            except (subprocess.CalledProcessError, FileNotFoundError):
                empty_list: list[str] = []
                result["git_log"] = empty_list

        # 6. Strategic analytics
        result["analytics"] = {
            "bottlenecks": self.find_bottlenecks(top_n=analytics_top_n),  # type: ignore[attr-defined]
            "recommendations": self.recommend_next_work(agent_count=analytics_top_n),  # type: ignore[attr-defined]
            "parallel": self.get_parallel_work(max_agents=analytics_max_agents),  # type: ignore[attr-defined]
        }

        return result  # type: ignore[return-value]

    def get_active_work_item(
        self,
        agent: str | None = None,
        filter_by_agent: bool = False,
        work_types: list[str] | None = None,
    ) -> ActiveWorkItem | None:
        """
        Get the currently active work item (in-progress status).

        This is used by the PreToolUse validation hook to check if code changes
        have an active work item for attribution.

        Args:
            agent: Agent ID for filtering (optional)
            filter_by_agent: If True, filter by agent. If False (default), return any active work item
            work_types: Work item types to check (defaults to all: features, bugs, spikes, chores, epics)

        Returns:
            Dict with work item details or None if no active work item found:
                - id: Work item ID
                - title: Work item title
                - type: Work item type (feature, bug, spike, chore, epic)
                - status: Should be "in-progress"
                - agent: Assigned agent
                - steps_total: Total steps
                - steps_completed: Completed steps
                - auto_generated: (spikes only) True if auto-generated spike
                - spike_subtype: (spikes only) "session-init" or "transition"

        Example:
            >>> sdk = SDK(agent="claude")
            >>> # Get any active work item
            >>> active = sdk.get_active_work_item()
            >>> if active:
            ...     logger.info(f"Working on: {active['title']}")
            ...
            >>> # Get only this agent's active work item
            >>> active = sdk.get_active_work_item(filter_by_agent=True)
        """
        # Default to all work item types
        if work_types is None:
            work_types = ["features", "bugs", "spikes", "chores", "epics"]

        # Session-scoped fast path: query the sessions table directly
        # This avoids scanning all HTML files when we know the active session
        if hasattr(self, "_db") and self._db and hasattr(self, "session_manager"):
            active_session = self.session_manager.get_active_session()  # type: ignore[attr-defined]
            if active_session:
                try:
                    active_id = self._db.get_active_work_item_for_session(
                        active_session.id
                    )
                    if active_id:
                        for work_type in work_types:
                            collection = getattr(self, work_type, None)
                            if collection is None:
                                continue
                            try:
                                node = collection.get(active_id)
                                if node:
                                    active_item_dict: dict[str, Any] = {
                                        "id": active_id,
                                        "title": getattr(node, "title", active_id),
                                        "type": getattr(
                                            node, "type", work_type.rstrip("s")
                                        ),
                                        "status": getattr(
                                            node, "status", "in-progress"
                                        ),
                                        "agent": getattr(node, "agent_assigned", None),
                                        "steps_total": len(node.steps)
                                        if hasattr(node, "steps")
                                        else 0,
                                        "steps_completed": sum(
                                            1 for s in node.steps if s.completed
                                        )
                                        if hasattr(node, "steps")
                                        else 0,
                                    }
                                    if getattr(node, "type", None) == "spike":
                                        active_item_dict["auto_generated"] = getattr(
                                            node, "auto_generated", False
                                        )
                                        active_item_dict["spike_subtype"] = getattr(
                                            node, "spike_subtype", None
                                        )
                                    return active_item_dict  # type: ignore[return-value]
                            except Exception:
                                continue
                        # ID set in session row but item not found in any collection
                        return {  # type: ignore[return-value]
                            "id": active_id,
                            "title": active_id,
                            "type": "unknown",
                            "status": "in-progress",
                        }
                except Exception:
                    pass  # Fall through to global scan

        # Search across all work item types
        # Separate real work items from auto-generated spikes
        real_work_items: list[dict[str, Any]] = []
        auto_spikes: list[dict[str, Any]] = []

        for work_type in work_types:
            collection = getattr(self, work_type, None)
            if collection is None:
                continue

            # Query for in-progress items
            in_progress = collection.where(status="in-progress")

            for item in in_progress:
                # Filter by agent if requested
                if filter_by_agent:
                    agent_id = agent or self._agent_id
                    if agent_id and hasattr(item, "agent_assigned"):
                        if item.agent_assigned != agent_id:
                            continue

                item_dict: dict[str, Any] = {
                    "id": item.id,
                    "title": item.title,
                    "type": item.type,
                    "status": item.status,
                    "agent": getattr(item, "agent_assigned", None),
                    "steps_total": len(item.steps) if hasattr(item, "steps") else 0,
                    "steps_completed": sum(1 for s in item.steps if s.completed)
                    if hasattr(item, "steps")
                    else 0,
                }

                # Add spike-specific fields for auto-spike detection
                if item.type == "spike":
                    item_dict["auto_generated"] = getattr(item, "auto_generated", False)
                    item_dict["spike_subtype"] = getattr(item, "spike_subtype", None)

                    # Separate auto-spikes from real work
                    # Auto-spikes are temporary tracking items (session-init, transition, conversation-init)
                    is_auto_spike = item_dict["auto_generated"] and item_dict[
                        "spike_subtype"
                    ] in ("session-init", "transition", "conversation-init")

                    if is_auto_spike:
                        auto_spikes.append(item_dict)
                    else:
                        # Real user-created spike
                        real_work_items.append(item_dict)
                else:
                    # Features, bugs, chores, epics are always real work
                    real_work_items.append(item_dict)

        # Prioritize real work items over auto-spikes
        # Auto-spikes should only show if there's NO other active work item
        if real_work_items:
            return real_work_items[0]  # type: ignore[return-value]

        if auto_spikes:
            return auto_spikes[0]  # type: ignore[return-value]

        return None

    def track_activity(
        self,
        tool: str,
        summary: str,
        file_paths: list[str] | None = None,
        success: bool = True,
        feature_id: str | None = None,
        session_id: str | None = None,
        parent_activity_id: str | None = None,
        payload: dict[str, Any] | None = None,
    ) -> Any:
        """
        Track an activity in the current or specified session.

        Args:
            tool: Tool name (Edit, Bash, Read, etc.)
            summary: Human-readable summary of the activity
            file_paths: Files involved in this activity
            success: Whether the tool call succeeded
            feature_id: Explicit feature ID (skips attribution if provided)
            session_id: Session ID (defaults to parent session if available, then active session)
            parent_activity_id: ID of parent activity (e.g., Skill/Task invocation)
            payload: Optional rich payload data

        Returns:
            Created ActivityEntry with attribution

        Example:
            >>> sdk = SDK(agent="claude")
            >>> entry = sdk.track_activity(
            ...     tool="CustomTool",
            ...     summary="Performed custom analysis",
            ...     file_paths=["src/main.py"],
            ...     success=True
            ... )
            >>> logger.info(f"Tracked: [{entry.tool}] {entry.summary}")
        """
        # Determine target session: explicit parameter > parent_session > active > none
        if not session_id:
            # Priority 1: Parent session (explicitly provided or from env var)
            if hasattr(self, "_parent_session") and self._parent_session:  # type: ignore[attr-defined]
                session_id = self._parent_session  # type: ignore[attr-defined]
            else:
                # Priority 2: Active session for this agent
                active = self.session_manager.get_active_session(agent=self._agent_id)
                if active:
                    session_id = active.id
                else:
                    raise ValueError(
                        "No active session. Start one with sdk.start_session()"
                    )

        # Get parent activity ID from environment if not provided
        if not parent_activity_id:
            parent_activity_id = os.getenv("HTMLGRAPH_PARENT_ACTIVITY")

        return self.session_manager.track_activity(
            session_id=session_id,
            tool=tool,
            summary=summary,
            file_paths=file_paths,
            success=success,
            feature_id=feature_id,
            parent_activity_id=parent_activity_id,
            payload=payload,
        )
