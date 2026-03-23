"""
HtmlGraph SDK - Modular Architecture

This package provides a fluent, ergonomic API for AI agents with:
- Auto-discovery of .htmlgraph directory
- Method chaining for all operations
- Context managers for auto-save
- Batch operations
- Minimal boilerplate

The SDK is composed from specialized mixins:
- AnalyticsRegistry: Analytics properties (analytics, dep_analytics, context, etc.)
- SessionManagerMixin: Session lifecycle (start_session, end_session)
- SessionHandoffMixin: Handoff operations (set_session_handoff, end_session_with_handoff)
- SessionContinuityMixin: Continuity (continue_from_last)
- SessionInfoMixin: Session info (get_session_start_info, get_active_work_item)
- PlanningMixin: Planning methods (find_bottlenecks, recommend_next_work, etc.)
- OrchestrationMixin: Subagent spawning (spawn_explorer, spawn_coder, orchestrate)
- OperationsMixin: Server, hooks, events (start_server, install_hooks, etc.)
- CoreMixin: Database, refs, utilities (db, query, ref, reload, etc.)
- TaskAttributionMixin: Task attribution (get_task_attribution, get_subagent_work)
- HelpMixin: Help system (help, __dir__)

Public API exports maintain backward compatibility.
All existing imports continue to work:
    from htmlgraph import SDK  # Still works
    from htmlgraph.sdk import SDK  # Also works
"""

from __future__ import annotations

import os
from pathlib import Path
from typing import Any

from htmlgraph.agent_detection import detect_agent_name
from htmlgraph.agents import AgentInterface
from htmlgraph.collections import (
    BugCollection,
    FeatureCollection,
    SpikeCollection,
)
from htmlgraph.collections.session import SessionCollection
from htmlgraph.db.schema import HtmlGraphDB
from htmlgraph.graph import HtmlGraph
from htmlgraph.graph.networkx_manager import GraphManager
from htmlgraph.sdk.analytics import AnalyticsRegistry
from htmlgraph.sdk.base import BaseSDK
from htmlgraph.sdk.constants import SDKSettings
from htmlgraph.sdk.discovery import (
    auto_discover_agent,
    discover_htmlgraph_dir,
    find_project_root,
)
from htmlgraph.sdk.help import HelpMixin
from htmlgraph.sdk.mixins import CoreMixin, TaskAttributionMixin
from htmlgraph.sdk.operations import OperationsMixin
from htmlgraph.sdk.orchestration import OrchestrationMixin
from htmlgraph.sdk.planning import PlanningMixin
from htmlgraph.sdk.session import (
    SessionContinuityMixin,
    SessionHandoffMixin,
    SessionInfoMixin,
    SessionManagerMixin,
)
from htmlgraph.session_manager import SessionManager
from htmlgraph.session_warning import check_and_show_warning
from htmlgraph.system_prompts import SystemPromptManager
from htmlgraph.track_builder import TrackCollection

# Agent name aliases — map common variations to canonical names
AGENT_ALIASES: dict[str, str] = {
    # Model-based names
    "opus": "claude-opus",
    "sonnet": "claude-sonnet",
    "haiku": "claude-haiku",
    "claude": "claude-code",
    "claude-code": "claude-code",
    # Platform names
    "gemini": "gemini-cli",
    "codex": "openai-codex",
    "copilot": "github-copilot",
    # Dashboard/internal
    "phoenix": "phoenix-dashboard",
    "phoenix-dashboard": "phoenix-dashboard",
    "dashboard": "phoenix-dashboard",
}


def normalize_agent_name(agent: str) -> str:
    """Normalize agent name to canonical form.

    Accepts model names (opus, sonnet, haiku), platform names (gemini, codex),
    or full names (claude-code, phoenix-dashboard). Returns canonical name.
    Unknown names are passed through unchanged for backward compatibility.
    """
    return AGENT_ALIASES.get(agent.lower(), agent)


class SDK(
    AnalyticsRegistry,
    SessionManagerMixin,
    SessionHandoffMixin,
    SessionContinuityMixin,
    SessionInfoMixin,
    PlanningMixin,
    OrchestrationMixin,
    OperationsMixin,
    CoreMixin,
    TaskAttributionMixin,
    HelpMixin,
):
    """
    Main SDK interface for AI agents.

    Auto-discovers .htmlgraph directory and provides fluent API for all collections.

    Available Collections:
        - features: Feature work items with builder support
        - bugs: Bug reports
        - spikes: Investigation and research spikes
        - sessions: Agent sessions
        - tracks: Work tracks

    This SDK class is a thin composition layer that inherits from specialized mixins:
    - AnalyticsRegistry: analytics, dep_analytics, context, pattern_learning properties
    - SessionManagerMixin: start_session, end_session, _ensure_session_exists
    - SessionHandoffMixin: set_session_handoff, end_session_with_handoff
    - SessionContinuityMixin: continue_from_last
    - SessionInfoMixin: get_session_start_info, get_active_work_item, track_activity
    - PlanningMixin: find_bottlenecks, recommend_next_work, get_parallel_work, etc.
    - OrchestrationMixin: orchestrator, spawn_explorer, spawn_coder, orchestrate
    - OperationsMixin: start_server, install_hooks, analyze_session, etc.
    - CoreMixin: db, query, ref, reload, summary, my_work, next_task, etc.
    - TaskAttributionMixin: get_task_attribution, get_subagent_work
    - HelpMixin: help, __dir__

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

        # Strategic analytics
        bottlenecks = sdk.find_bottlenecks(top_n=5)
        recommendations = sdk.recommend_next_work(agent_count=3)
    """

    def __init__(
        self,
        directory: Path | str | None = None,
        agent: str | None = None,
        parent_session: str | None = None,
        db_path: str | None = None,
    ):
        """
        Initialize SDK.

        Args:
            directory: Path to .htmlgraph directory (auto-discovered if not provided)
            agent: REQUIRED - Agent identifier for operations.
                Used to attribute work items (features, spikes, bugs, etc) to the agent.
                Examples: agent='explorer', agent='coder', agent='tester'
                Critical for: Work attribution, result retrieval, orchestrator tracking
                Falls back to: CLAUDE_AGENT_NAME env var, then detect_agent_name()
                Raises ValueError if not provided and cannot be detected
            parent_session: Parent session ID to log activities to (for nested contexts)
            db_path: Path to SQLite database file (optional, defaults to ~/.htmlgraph/htmlgraph.db)
        """
        if directory is None:
            directory = self._discover_htmlgraph()
        else:
            directory = Path(directory)
            # Auto-discover .htmlgraph if given a project root
            if directory.name != ".htmlgraph" and (directory / ".htmlgraph").exists():
                directory = directory / ".htmlgraph"

        if agent is None:
            # Try environment variable fallback
            agent = os.getenv("CLAUDE_AGENT_NAME")

        if agent is None:
            # Try automatic detection
            detected = detect_agent_name()
            if detected and detected != "cli":
                # Only accept detected if it's not the default fallback
                agent = detected
            else:
                # No valid agent found - fail fast with helpful error message
                raise ValueError(
                    "Agent identifier is required for work attribution. "
                    "Pass agent='name' to SDK() initialization. "
                    "Examples: SDK(agent='explorer'), SDK(agent='coder'), SDK(agent='tester')\n"
                    "Alternatively, set CLAUDE_AGENT_NAME environment variable.\n"
                    "Critical for: Work attribution, result retrieval, orchestrator tracking"
                )

        self._directory = Path(directory)
        self._agent_id = normalize_agent_name(agent)
        self._parent_session = parent_session or os.getenv("HTMLGRAPH_PARENT_SESSION")

        # Initialize SQLite database (Phase 2)
        # Resolve DB path relative to the discovered .htmlgraph directory so that
        # subagents launched from a different cwd still use the project-local DB,
        # not ~/.htmlgraph/htmlgraph.db.
        resolved_db_path = db_path or str(self._directory / "htmlgraph.db")
        self._db = HtmlGraphDB(resolved_db_path)
        self._db.connect()
        self._db.create_tables()

        # Initialize underlying HtmlGraphs first (for backward compatibility and sharing)
        # These are shared with SessionManager to avoid double-loading features
        self._graph = HtmlGraph(self._directory / "features")
        self._bugs_graph = HtmlGraph(self._directory / "bugs")

        # Initialize SessionManager with shared graph instances to avoid double-loading
        self.session_manager = SessionManager(
            self._directory,
            features_graph=self._graph,
            bugs_graph=self._bugs_graph,
            db=self._db,
        )

        # NetworkX graph manager (lazy-initialized)
        self._nx_graph_manager: GraphManager | None = None

        # Agent interface (for backward compatibility)
        self._agent_interface = AgentInterface(
            self._directory / "features", agent_id=agent
        )

        # Collection interfaces - core work item types
        self.features = FeatureCollection(self)
        self.bugs = BugCollection(self)
        self.spikes = SpikeCollection(self)

        # Non-work collections
        self.sessions: SessionCollection = SessionCollection(self)
        self.tracks: TrackCollection = TrackCollection(
            self
        )  # Use specialized collection with builder support

        # Initialize RefManager and set on all collections
        from htmlgraph.refs import RefManager

        self.refs = RefManager(self._directory)

        # Set ref manager on all work item collections
        self.features.set_ref_manager(self.refs)
        self.bugs.set_ref_manager(self.refs)
        self.spikes.set_ref_manager(self.refs)
        self.tracks.set_ref_manager(self.refs)

        # Analytics engine (centralized analytics management with lazy loading)
        from htmlgraph.sdk.analytics.helpers import create_analytics_engine

        self._analytics_engine = create_analytics_engine(
            sdk=self, graph=self._graph, directory=self._directory
        )

        # Lazy-loaded orchestrator for subagent management
        self._orchestrator: Any = None

        # System prompt manager (lazy-loaded)
        self._system_prompts: SystemPromptManager | None = None

        # Session warning system (workaround for Claude Code hook bug #10373)
        # Shows orchestrator instructions on first SDK usage per session
        self._session_warning = check_and_show_warning(
            self._directory,
            agent=self._agent_id,
            session_id=None,  # Will be set by session manager if available
        )

    @staticmethod
    def _discover_htmlgraph() -> Path:
        """
        Auto-discover .htmlgraph directory.

        Searches current directory and parents.
        """
        return discover_htmlgraph_dir()

    @property
    def agent(self) -> str:
        """The normalized agent name."""
        return self._agent_id

    @property
    def graph(self) -> GraphManager:
        """NetworkX graph manager for dependency analysis, critical path, and cycle detection."""
        if self._nx_graph_manager is None:
            self._nx_graph_manager = GraphManager(
                graph_dir=str(self._directory),
                db_path=str(self._directory / "htmlgraph.db"),
            )
        return self._nx_graph_manager

    @property
    def system_prompts(self) -> SystemPromptManager:
        """
        Access system prompt management.

        Provides methods to:
        - Get active prompt (project override OR plugin default)
        - Create/delete project-level overrides
        - Validate token counts
        - Get prompt statistics

        Lazy-loaded on first access.

        Returns:
            SystemPromptManager instance

        Example:
            >>> sdk = SDK(agent="claude")

            # Get active prompt
            >>> prompt = sdk.system_prompts.get_active()

            # Create project override
            >>> sdk.system_prompts.create("## Custom prompt\\n...")

            # Validate token count
            >>> result = sdk.system_prompts.validate()
            >>> print(result['message'])

            # Get statistics
            >>> stats = sdk.system_prompts.get_stats()
            >>> print(f"Source: {stats['source']}")
        """
        if self._system_prompts is None:
            self._system_prompts = SystemPromptManager(self._directory)
        return self._system_prompts

    def dismiss_session_warning(self) -> bool:
        """
        Dismiss the session warning after reading it.

        IMPORTANT: Call this as your FIRST action after seeing the orchestrator
        warning. This confirms you've read the instructions.

        Returns:
            True if warning was dismissed, False if already dismissed

        Example:
            sdk = SDK(agent="claude")
            # Warning shown automatically...

            # First action: dismiss to confirm you read it
            sdk.dismiss_session_warning()

            # Now proceed with orchestration
            sdk.spawn_coder(feature_id="feat-123", ...)
        """
        if self._session_warning:
            return self._session_warning.dismiss(
                agent=self._agent_id,
                session_id=None,
            )
        return False

    def get_warning_status(self) -> dict[str, Any]:
        """
        Get current session warning status.

        Returns:
            Dict with dismissed status, timestamp, and show count
        """
        if self._session_warning:
            return self._session_warning.get_status()
        return {"dismissed": True, "show_count": 0}

    def query(self) -> SemanticQueryBuilder:  # noqa: F821
        """
        Create a semantic query builder for expressive work item queries.

        Returns a fluent builder that supports chained filters, sorting,
        and pagination across features, bugs, and spikes.

        Example:
            >>> results = (sdk.query()
            ...     .features()
            ...     .where_status("in-progress")
            ...     .where_priority("high")
            ...     .with_tag("backend")
            ...     .sort_by("updated", "desc")
            ...     .limit(10)
            ...     .execute())
        """
        from htmlgraph.sdk.query_builder import SemanticQueryBuilder

        return SemanticQueryBuilder(self)


__all__ = [
    # Core SDK class
    "SDK",
    "BaseSDK",
    # Agent normalization
    "AGENT_ALIASES",
    "normalize_agent_name",
    # Discovery utilities
    "find_project_root",
    "discover_htmlgraph_dir",
    "auto_discover_agent",
    # Constants and configuration
    "SDKSettings",
]
