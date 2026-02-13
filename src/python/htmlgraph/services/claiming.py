"""
ClaimingService - Handles feature claiming and release logic.

Extracted from SessionManager to reduce complexity and improve maintainability.
"""

from datetime import datetime
from typing import TYPE_CHECKING

from htmlgraph.graph import HtmlGraph
from htmlgraph.models import Node

if TYPE_CHECKING:
    from htmlgraph.session_manager import SessionManager


class ClaimingService:
    """
    Service for managing feature claims and releases.

    This service handles:
    - Claiming features for agents
    - Releasing feature claims
    - Auto-releasing all claims for an agent
    - Releasing all claims for a session
    """

    def __init__(
        self,
        features_graph: HtmlGraph,
        bugs_graph: HtmlGraph,
        session_manager: "SessionManager",
    ):
        """
        Initialize ClaimingService.

        Args:
            features_graph: Graph for features collection
            bugs_graph: Graph for bugs collection
            session_manager: Reference to SessionManager for session operations
        """
        self.features_graph = features_graph
        self.bugs_graph = bugs_graph
        self.session_manager = session_manager

    def _get_graph(self, collection: str) -> HtmlGraph:
        """Get graph for a collection."""
        if collection == "bugs":
            return self.bugs_graph
        return self.features_graph

    def is_claimed(self, feature_id: str, collection: str = "features") -> bool:
        """
        Check if a feature is currently claimed by any agent.

        Args:
            feature_id: Feature to check
            collection: Collection name

        Returns:
            True if the feature is claimed
        """
        graph = self._get_graph(collection)
        node = graph.get(feature_id)
        if not node:
            return False
        return bool(node.agent_assigned)

    def release_claim(self, feature_id: str, agent: str | None = None, collection: str = "features") -> bool:
        """
        Release a feature claim (unconditional or agent-specific).

        Args:
            feature_id: Feature to release
            agent: If provided, only release if claimed by this agent
            collection: Collection name

        Returns:
            True if claim was released
        """
        graph = self._get_graph(collection)
        node = graph.get(feature_id)
        if not node:
            return False

        if not node.agent_assigned:
            return False

        if agent and node.agent_assigned != agent:
            return False

        node.agent_assigned = None
        node.claimed_at = None
        node.claimed_by_session = None
        node.updated = datetime.now()
        graph.update(node)

        self._invalidate_features_cache()
        return True

    def get_claimed_by_agent(self, agent: str) -> list[str]:
        """
        Get all feature IDs claimed by a specific agent.

        Args:
            agent: Agent name

        Returns:
            List of feature IDs claimed by the agent
        """
        claimed = []
        for collection in ["features", "bugs"]:
            graph = self._get_graph(collection)
            for node in graph:
                if node.agent_assigned == agent:
                    claimed.append(node.id)
        return claimed

    def _invalidate_features_cache(self) -> None:
        """Invalidate the active features cache in SessionManager."""
        self.session_manager._features_cache_dirty = True

    def claim_feature(
        self,
        feature_id: str,
        collection: str = "features",
        *,
        agent: str,
    ) -> Node | None:
        """
        Claim a feature for an agent.

        Args:
            feature_id: Feature to claim
            collection: Collection name
            agent: Agent name claiming the feature

        Returns:
            Updated Node or None
        """
        graph = self._get_graph(collection)
        node = graph.get(feature_id)
        if not node:
            return None

        # Check if already claimed by someone else
        if node.agent_assigned and node.agent_assigned != agent:
            # Check if session that claimed it is still active
            if node.claimed_by_session:
                session = self.session_manager.get_session(node.claimed_by_session)
                if session and session.status == "active":
                    raise ValueError(
                        f"Feature '{feature_id}' is already claimed by {node.agent_assigned} "
                        f"(session {node.claimed_by_session})"
                    )

        session = self.session_manager._ensure_session_for_agent(agent)

        node.agent_assigned = agent
        node.claimed_at = datetime.now()
        node.claimed_by_session = session.id
        node.updated = datetime.now()
        graph.update(node)

        self.session_manager._maybe_log_work_item_action(
            agent=agent,
            tool="FeatureClaim",
            summary=f"Claimed: {collection}/{feature_id}",
            feature_id=feature_id,
            payload={"collection": collection, "action": "claim"},
        )

        return node

    def release_feature(
        self,
        feature_id: str,
        collection: str = "features",
        *,
        agent: str,
    ) -> Node | None:
        """
        Release a feature claim.

        Args:
            feature_id: Feature to release
            collection: Collection name
            agent: Agent name releasing the feature

        Returns:
            Updated Node or None
        """
        graph = self._get_graph(collection)
        node = graph.get(feature_id)
        if not node:
            return None

        if node.agent_assigned and node.agent_assigned != agent:
            raise ValueError(
                f"Feature '{feature_id}' is claimed by {node.agent_assigned}, not {agent}"
            )

        node.agent_assigned = None
        node.claimed_at = None
        node.claimed_by_session = None
        node.updated = datetime.now()
        graph.update(node)

        # Invalidate active features cache
        self._invalidate_features_cache()

        self.session_manager._maybe_log_work_item_action(
            agent=agent,
            tool="FeatureRelease",
            summary=f"Released: {collection}/{feature_id}",
            feature_id=feature_id,
            payload={"collection": collection, "action": "release"},
        )

        return node

    def auto_release_features(self, agent: str) -> list[str]:
        """
        Release all features claimed by an agent.

        Args:
            agent: Agent name

        Returns:
            List of released feature IDs
        """
        released = []
        for collection in ["features", "bugs"]:
            graph = self._get_graph(collection)
            for node in graph:
                if node.agent_assigned == agent:
                    node.agent_assigned = None
                    node.claimed_at = None
                    node.claimed_by_session = None
                    node.updated = datetime.now()
                    graph.update(node)
                    released.append(node.id)
        return released

    def release_session_features(self, session_id: str) -> list[str]:
        """
        Release all features claimed by a specific session.

        Args:
            session_id: Session ID

        Returns:
            List of released feature IDs
        """
        released = []
        for collection in ["features", "bugs"]:
            graph = self._get_graph(collection)
            for node in graph:
                if node.claimed_by_session == session_id:
                    node.agent_assigned = None
                    node.claimed_at = None
                    node.claimed_by_session = None
                    node.updated = datetime.now()
                    graph.update(node)
                    released.append(node.id)
        return released
