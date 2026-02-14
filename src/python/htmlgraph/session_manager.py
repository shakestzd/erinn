"""
SessionManager - Smart session and activity tracking for AI agents.

Thin facade that delegates to extracted modules:
- sessions/session_lifecycle.py  - Session start/end/get/normalize/dedupe
- sessions/activity_tracking.py  - Activity recording, event logging, SQLite index
- sessions/scoring.py            - Attribution scoring, system overhead detection
- sessions/drift_detection.py    - Drift detection
- sessions/feature_workflow.py   - Feature CRUD & lifecycle
- sessions/linking.py            - Bidirectional graph links
- sessions/transcript_ops.py     - Transcript import/linking
- sessions/spikes.py             - Auto-generated spike management
"""

import logging
import subprocess
from datetime import timedelta
from pathlib import Path
from typing import Any

logger = logging.getLogger(__name__)

from htmlgraph.converter import SessionConverter
from htmlgraph.event_log import JsonlEventLog
from htmlgraph.exceptions import SessionNotFoundError
from htmlgraph.graph import HtmlGraph
from htmlgraph.models import ActivityEntry, Node, Session
from htmlgraph.services import ClaimingService
from htmlgraph.sessions.activity_tracking import ActivityTracker
from htmlgraph.sessions.drift_detection import detect_drift as _detect_drift
from htmlgraph.sessions.feature_workflow import FeatureWorkflow
from htmlgraph.sessions.linking import LinkingOps
from htmlgraph.sessions.scoring import (
    attribute_activity as _attribute_activity,
    is_system_overhead as _is_system_overhead,
    score_feature_match as _score_feature_match,
)
from htmlgraph.sessions.session_lifecycle import SessionLifecycleOps
from htmlgraph.sessions.spikes import SpikeManager
from htmlgraph.sessions.transcript_ops import TranscriptOps
from htmlgraph.spike_index import ActiveAutoSpikeIndex


class SessionManager:
    """
    Manages agent sessions with smart attribution and drift detection.

    Usage:
        manager = SessionManager(".htmlgraph")

        # Start a session
        session = manager.start_session("session-001", agent="claude-code")

        # Track activity (auto-attributes to best feature)
        manager.track_activity(
            session_id="session-001",
            tool="Edit",
            summary="Edit: src/auth/login.py:45-52",
            file_paths=["src/auth/login.py"]
        )

        # End session
        manager.end_session("session-001")
    """

    # Drift thresholds (kept for backward compat - code that reads these)
    DRIFT_TIME_THRESHOLD = timedelta(minutes=15)
    DRIFT_EVENT_THRESHOLD = 5

    # WIP limit
    DEFAULT_WIP_LIMIT = 3
    DEFAULT_SESSION_DEDUPE_WINDOW_SECONDS = 120

    def __init__(
        self,
        graph_dir: str | Path = ".htmlgraph",
        wip_limit: int = DEFAULT_WIP_LIMIT,
        session_dedupe_window_seconds: int = DEFAULT_SESSION_DEDUPE_WINDOW_SECONDS,
        features_graph: HtmlGraph | None = None,
        bugs_graph: HtmlGraph | None = None,
    ):
        self.graph_dir = Path(graph_dir)
        self.wip_limit = wip_limit
        self.session_dedupe_window_seconds = session_dedupe_window_seconds

        # Initialize directories
        self.sessions_dir = self.graph_dir / "sessions"
        self.features_dir = self.graph_dir / "features"
        self.bugs_dir = self.graph_dir / "bugs"
        self.sessions_dir.mkdir(parents=True, exist_ok=True)
        self.features_dir.mkdir(parents=True, exist_ok=True)
        self.bugs_dir.mkdir(parents=True, exist_ok=True)

        # Session converter
        self.session_converter = SessionConverter(self.sessions_dir)

        # Feature graphs
        self.features_graph = (
            features_graph
            if features_graph is not None
            else HtmlGraph(self.features_dir, auto_load=False)
        )
        self.bugs_graph = (
            bugs_graph
            if bugs_graph is not None
            else HtmlGraph(self.bugs_dir, auto_load=False)
        )

        # Claiming service
        self.claiming_service = ClaimingService(
            features_graph=self.features_graph,
            bugs_graph=self.bugs_graph,
            session_manager=self,
        )

        # Cache for active features
        self._active_features_cache: list[Node] | None = None
        self._features_cache_dirty: bool = True

        # Spike index
        self._spike_index = ActiveAutoSpikeIndex(self.graph_dir)
        self._active_auto_spikes: set[str] = self._spike_index.get_all()

        # Event log
        self.events_dir = self.graph_dir / "events"
        self.event_log = JsonlEventLog(self.events_dir)

        # --- Delegate modules ---
        self._spikes = SpikeManager(
            graph_dir=self.graph_dir,
            session_converter=self.session_converter,
            spike_index=self._spike_index,
            active_auto_spikes=self._active_auto_spikes,
        )
        self._linking = LinkingOps(
            session_converter=self.session_converter,
            features_graph=self.features_graph,
            bugs_graph=self.bugs_graph,
        )
        self._transcript_ops = TranscriptOps(
            session_converter=self.session_converter,
            event_log=self.event_log,
        )
        self._feature_workflow = FeatureWorkflow(manager=self)
        self._lifecycle = SessionLifecycleOps(
            session_converter=self.session_converter,
            sessions_dir=self.sessions_dir,
            graph_dir=self.graph_dir,
            session_dedupe_window_seconds=session_dedupe_window_seconds,
        )
        self._activity_tracker = ActivityTracker(
            graph_dir=self.graph_dir,
            session_converter=self.session_converter,
            event_log=self.event_log,
        )

    # =========================================================================
    # Session Lifecycle - delegated to SessionLifecycleOps
    # =========================================================================

    def _list_active_sessions(self) -> list[Session]:
        return self._lifecycle.list_active_sessions()

    def _choose_canonical_active_session(
        self, sessions: list[Session]
    ) -> Session | None:
        return self._lifecycle.choose_canonical(sessions)

    def _mark_session_stale(self, session: Session) -> None:
        self._lifecycle.mark_stale(session)

    @property
    def _active_session(self) -> Session | None:
        return self._lifecycle.active_session

    @_active_session.setter
    def _active_session(self, value: Session | None) -> None:
        self._lifecycle.active_session = value

    @property
    def _sessions_cache_dirty(self) -> bool:
        return self._lifecycle._sessions_cache_dirty

    @_sessions_cache_dirty.setter
    def _sessions_cache_dirty(self, value: bool) -> None:
        self._lifecycle._sessions_cache_dirty = value

    def normalize_active_sessions(self) -> dict[str, int]:
        return self._lifecycle.normalize_active_sessions()

    def start_session(
        self,
        session_id: str | None = None,
        agent: str | None = None,
        is_subagent: bool = False,
        continued_from: str | None = None,
        start_commit: str | None = None,
        title: str | None = None,
        parent_session_id: str | None = None,
    ) -> Session:
        return self._lifecycle.start_session(
            session_id=session_id,
            agent=agent,
            is_subagent=is_subagent,
            continued_from=continued_from,
            start_commit=start_commit,
            title=title,
            parent_session_id=parent_session_id,
            get_current_commit=self._get_current_commit,
            spikes=self._spikes,
        )

    def get_session(self, session_id: str) -> Session | None:
        return self._lifecycle.get_session(session_id)

    def get_last_ended_session(self, agent: str | None = None) -> Session | None:
        return self._lifecycle.get_last_ended_session(agent)

    def get_active_session(self, agent: str | None = None) -> Session | None:
        return self._lifecycle.get_active_session(agent)

    def get_active_session_for_agent(self, agent: str) -> Session | None:
        return self._lifecycle.get_active_session_for_agent(agent)

    def dedupe_orphan_sessions(
        self,
        max_events: int = 1,
        move_dir_name: str = "_orphans",
        dry_run: bool = False,
        stale_extra_active: bool = True,
    ) -> dict[str, int]:
        return self._lifecycle.dedupe_orphan_sessions(
            max_events=max_events,
            move_dir_name=move_dir_name,
            dry_run=dry_run,
            stale_extra_active=stale_extra_active,
        )

    def end_session(
        self,
        session_id: str,
        handoff_notes: str | None = None,
        recommended_next: str | None = None,
        blockers: list[str] | None = None,
        end_commit: str | None = None,
    ) -> Session | None:
        return self._lifecycle.end_session(
            session_id=session_id,
            handoff_notes=handoff_notes,
            recommended_next=recommended_next,
            blockers=blockers,
            end_commit=end_commit,
            get_current_commit=self._get_current_commit,
            release_session_features=self.release_session_features,
        )

    def set_session_handoff(
        self,
        session_id: str,
        handoff_notes: str | None = None,
        recommended_next: str | None = None,
        blockers: list[str] | None = None,
    ) -> Session | None:
        return self._lifecycle.set_session_handoff(
            session_id=session_id,
            handoff_notes=handoff_notes,
            recommended_next=recommended_next,
            blockers=blockers,
        )

    def continue_from_last(
        self,
        agent: str | None = None,
        auto_create_session: bool = True,
    ) -> tuple[Session | None, Any]:
        return self._lifecycle.continue_from_last(
            agent=agent,
            auto_create_session=auto_create_session,
        )

    def end_session_with_handoff(
        self,
        session_id: str,
        summary: str | None = None,
        next_focus: str | None = None,
        blockers: list[str] | None = None,
        keep_context: list[str] | None = None,
        auto_recommend_context: bool = True,
    ) -> Session | None:
        return self._lifecycle.end_session_with_handoff(
            session_id=session_id,
            summary=summary,
            next_focus=next_focus,
            blockers=blockers,
            keep_context=keep_context,
            auto_recommend_context=auto_recommend_context,
            end_session_fn=self.end_session,
        )

    def release_session_features(self, session_id: str) -> list[str]:
        return self.claiming_service.release_session_features(session_id)

    # =========================================================================
    # Error Logging (inline - small, tightly coupled to session lookup)
    # =========================================================================

    def log_error(
        self,
        session_id: str,
        error: Exception,
        traceback_str: str,
        context: dict[str, Any] | None = None,
    ) -> None:
        """Log error with full traceback to session."""
        from htmlgraph.models import ErrorEntry

        session = self.get_session(session_id)
        if not session:
            return
        error_entry = ErrorEntry(
            timestamp=__import__("datetime").datetime.now(),
            error_type=error.__class__.__name__,
            message=str(error),
            traceback=traceback_str,
        )
        session.error_log.append(error_entry)
        self.session_converter.save(session)

    def get_session_errors(self, session_id: str) -> list[dict[str, Any]]:
        session = self.get_session(session_id)
        if not session:
            return []
        return [error.model_dump() for error in session.error_log]

    def search_errors(
        self,
        session_id: str,
        error_type: str | None = None,
        pattern: str | None = None,
    ) -> list[dict[str, Any]]:
        import re

        session = self.get_session(session_id)
        if not session:
            return []
        errors = [error.model_dump() for error in session.error_log]
        if error_type:
            errors = [e for e in errors if e.get("error_type") == error_type]
        if pattern:
            compiled = re.compile(pattern, re.IGNORECASE)
            errors = [e for e in errors if compiled.search(e.get("message", ""))]
        return errors

    def get_error_summary(self, session_id: str) -> dict[str, Any]:
        session = self.get_session(session_id)
        if not session or not session.error_log:
            return {
                "total_errors": 0,
                "error_types": {},
                "first_error": None,
                "last_error": None,
            }
        errors = session.error_log
        error_types: dict[str, int] = {}
        for error in errors:
            etype = error.error_type
            error_types[etype] = error_types.get(etype, 0) + 1
        return {
            "total_errors": len(errors),
            "error_types": error_types,
            "first_error": errors[0].model_dump() if errors else None,
            "last_error": errors[-1].model_dump() if errors else None,
        }

    # =========================================================================
    # Activity Tracking - delegated to ActivityTracker
    # =========================================================================

    def track_activity(
        self,
        session_id: str,
        tool: str,
        summary: str,
        file_paths: list[str] | None = None,
        success: bool = True,
        feature_id: str | None = None,
        parent_activity_id: str | None = None,
        payload: dict[str, Any] | None = None,
    ) -> ActivityEntry:
        session = self.get_session(session_id)
        if not session:
            raise SessionNotFoundError(session_id)

        entry = self._activity_tracker.track_activity(
            session=session,
            session_id=session_id,
            tool=tool,
            summary=summary,
            file_paths=file_paths,
            success=success,
            feature_id=feature_id,
            parent_activity_id=parent_activity_id,
            payload=payload,
            active_features=self.get_active_features(),
            get_active_auto_spike=self._get_active_auto_spike,
            linking=self._linking,
            get_session=self.get_session,
            check_completion=self._check_completion,
        )
        self._lifecycle.active_session = session
        return entry

    def track_user_query(
        self,
        session_id: str,
        prompt: str,
        feature_id: str | None = None,
    ) -> ActivityEntry:
        preview = prompt[:100] + "..." if len(prompt) > 100 else prompt
        preview = preview.replace("\n", " ")
        return self.track_activity(
            session_id=session_id,
            tool="UserQuery",
            summary=f'"{preview}"',
            feature_id=feature_id,
            payload={"prompt": prompt, "prompt_length": len(prompt)},
        )

    # =========================================================================
    # Smart Attribution - delegated to scoring module
    # =========================================================================

    def _get_active_auto_spike(self, active_features: list[Node]) -> Node | None:
        return self._spikes.get_active_auto_spike(active_features)

    def attribute_activity(
        self,
        tool: str,
        summary: str,
        file_paths: list[str],
        active_features: list[Node],
        agent: str | None = None,
    ) -> dict[str, Any]:
        return _attribute_activity(
            tool=tool,
            summary=summary,
            file_paths=file_paths,
            active_features=active_features,
            agent=agent,
            get_active_auto_spike=self._get_active_auto_spike,
        )

    def _score_feature_match(
        self,
        feature: Node,
        _tool: str,
        summary: str,
        file_paths: list[str],
        agent: str | None = None,
    ) -> tuple[float, list[str]]:
        return _score_feature_match(feature, _tool, summary, file_paths, agent=agent)

    def _is_system_overhead(
        self, tool: str, summary: str, file_paths: list[str]
    ) -> bool:
        return _is_system_overhead(tool, summary, file_paths)

    # =========================================================================
    # Drift Detection - delegated to drift_detection module
    # =========================================================================

    def detect_drift(self, session_id: str, feature_id: str) -> dict[str, Any]:
        session = self.get_session(session_id)
        if not session:
            return {"is_drifting": False, "drift_score": 0, "reasons": []}
        return _detect_drift(session, feature_id)

    # =========================================================================
    # Feature Management - delegated to FeatureWorkflow
    # =========================================================================

    def _ensure_session_for_agent(self, agent: str) -> Session:
        return self._lifecycle.ensure_session_for_agent(agent)

    def _maybe_log_work_item_action(
        self,
        *,
        agent: str | None,
        tool: str,
        summary: str,
        feature_id: str | None,
        success: bool = True,
        payload: dict[str, Any] | None = None,
    ) -> None:
        if not agent:
            return
        try:
            session = self._ensure_session_for_agent(agent)
            self.track_activity(
                session_id=session.id,
                tool=tool,
                summary=summary,
                file_paths=None,
                success=success,
                feature_id=feature_id,
                payload=payload,
            )
        except Exception as e:
            logger.warning(f"Failed to log work item action ({tool}): {e}")

    def get_active_features(self) -> list[Node]:
        if self._features_cache_dirty or self._active_features_cache is None:
            self._active_features_cache = self._compute_active_features()
            self._features_cache_dirty = False
        return self._active_features_cache

    def _compute_active_features(self) -> list[Node]:
        features = []
        for node in self.features_graph:
            if node.status == "in-progress":
                features.append(node)
        for node in self.bugs_graph:
            if node.status == "in-progress":
                features.append(node)
        return features

    def create_feature(
        self, title: str, collection: str = "features", description: str = "",
        priority: str = "medium", steps: list[str] | None = None,
        agent: str | None = None,
    ) -> Node:
        return self._feature_workflow.create_feature(
            title=title, collection=collection, description=description,
            priority=priority, steps=steps, agent=agent,
        )

    def get_primary_feature(self) -> Node | None:
        for feature in self.get_active_features():
            if feature.properties.get("is_primary"):
                return feature
        active = self.get_active_features()
        return active[0] if active else None

    def start_feature(
        self, feature_id: str, collection: str = "features", *,
        agent: str | None = None, log_activity: bool = True,
    ) -> Node | None:
        return self._feature_workflow.start_feature(
            feature_id=feature_id, collection=collection,
            agent=agent, log_activity=log_activity,
        )

    def complete_feature(
        self, feature_id: str, collection: str = "features", *,
        agent: str | None = None, log_activity: bool = True,
        transcript_id: str | None = None,
    ) -> Node | None:
        return self._feature_workflow.complete_feature(
            feature_id=feature_id, collection=collection,
            agent=agent, log_activity=log_activity,
            transcript_id=transcript_id,
        )

    def set_primary_feature(
        self, feature_id: str, collection: str = "features", *,
        agent: str | None = None, log_activity: bool = True,
    ) -> Node | None:
        return self._feature_workflow.set_primary_feature(
            feature_id=feature_id, collection=collection,
            agent=agent, log_activity=log_activity,
        )

    def activate_feature(
        self, feature_id: str, collection: str = "features", *,
        agent: str | None = None, log_activity: bool = True,
    ) -> Node | None:
        return self._feature_workflow.activate_feature(
            feature_id=feature_id, collection=collection,
            agent=agent, log_activity=log_activity,
        )

    # =========================================================================
    # Auto-Completion - delegated to FeatureWorkflow
    # =========================================================================

    def _check_completion(self, feature_id: str, tool: str, success: bool) -> bool:
        return self._feature_workflow.check_completion(feature_id, tool, success)

    # =========================================================================
    # Status & Reporting
    # =========================================================================

    def get_status(self) -> dict[str, Any]:
        all_features = list(self.features_graph) + list(self.bugs_graph)
        by_status: dict[str, int] = {"todo": 0, "in-progress": 0, "blocked": 0, "done": 0}
        for node in all_features:
            by_status[node.status] = by_status.get(node.status, 0) + 1
        active = self.get_active_features()
        primary = self.get_primary_feature()
        active_session = self.get_active_session()
        return {
            "total_features": len(all_features),
            "by_status": by_status,
            "wip_count": len(active),
            "wip_limit": self.wip_limit,
            "wip_remaining": self.wip_limit - len(active),
            "primary_feature": primary.id if primary else None,
            "active_features": [f.id for f in active],
            "active_session": active_session.id if active_session else None,
        }

    # =========================================================================
    # Claiming Mechanism
    # =========================================================================

    def claim_feature(
        self, feature_id: str, collection: str = "features", *, agent: str,
    ) -> Node | None:
        return self.claiming_service.claim_feature(
            feature_id=feature_id, collection=collection, agent=agent,
        )

    def release_feature(
        self, feature_id: str, collection: str = "features", *, agent: str,
    ) -> Node | None:
        return self.claiming_service.release_feature(
            feature_id=feature_id, collection=collection, agent=agent,
        )

    def auto_release_features(self, agent: str) -> list[str]:
        return self.claiming_service.auto_release_features(agent)

    def create_handoff(
        self, feature_id: str, reason: str, notes: str | None = None,
        collection: str = "features", *, agent: str,
        next_agent: str | None = None,
    ) -> Node | None:
        from datetime import datetime

        graph = self._get_graph(collection)
        node = graph.get(feature_id)
        if not node:
            return None
        if node.agent_assigned and node.agent_assigned != agent:
            raise ValueError(
                f"Feature '{feature_id}' is claimed by {node.agent_assigned}, not {agent}"
            )
        node.handoff_required = True
        node.previous_agent = agent
        node.handoff_reason = reason
        node.handoff_notes = notes
        node.handoff_timestamp = datetime.now()
        node.updated = datetime.now()
        node.agent_assigned = None
        node.claimed_at = None
        node.claimed_by_session = None
        graph.update(node)
        self._maybe_log_work_item_action(
            agent=agent, tool="FeatureHandoff",
            summary=f"Handed off: {collection}/{feature_id} (reason: {reason})",
            feature_id=feature_id,
            payload={
                "collection": collection, "action": "handoff",
                "reason": reason, "notes": notes, "next_agent": next_agent,
            },
        )
        return node

    # =========================================================================
    # Helpers
    # =========================================================================

    def _create_transition_spike(self, session: Session, **kwargs: Any) -> Any:
        """Thin wrapper for backward compat (used by tests patching this method)."""
        return self._spikes.create_transition_spike(session, **kwargs)

    def _add_session_link_to_feature(self, feature_id: str, session_id: str) -> None:
        self._linking.add_session_link_to_feature(
            feature_id, session_id, self.get_session
        )

    def _link_transcript_to_feature(
        self, node: Node, transcript_id: str, graph: HtmlGraph,
    ) -> None:
        self._linking.link_transcript_to_feature(node, transcript_id, graph)

    def _get_graph(self, collection: str) -> HtmlGraph:
        if collection == "bugs":
            return self.bugs_graph
        return self.features_graph

    def _get_graph_for_node(self, node: Node) -> HtmlGraph:
        if node.type == "bug":
            return self.bugs_graph
        return self.features_graph

    def _get_current_commit(self) -> str | None:
        try:
            result = subprocess.run(
                ["git", "rev-parse", "--short", "HEAD"],
                capture_output=True, text=True,
                cwd=self.graph_dir.parent,
            )
            if result.returncode == 0:
                return result.stdout.strip()
        except Exception as e:
            logger.warning(f"Failed to get current git commit: {e}")
        return None

    # =========================================================================
    # Transcript Integration - delegated to TranscriptOps
    # =========================================================================

    def link_transcript(
        self, session_id: str, transcript_id: str,
        transcript_path: str | None = None, git_branch: str | None = None,
    ) -> Session | None:
        session = self.get_session(session_id)
        if not session:
            return None
        return self._transcript_ops.link_transcript(
            session=session, transcript_id=transcript_id,
            transcript_path=transcript_path, git_branch=git_branch,
        )

    def find_session_by_transcript(self, transcript_id: str) -> Session | None:
        return self._transcript_ops.find_session_by_transcript(transcript_id)

    def import_transcript_events(
        self, session_id: str, transcript_session: Any, overwrite: bool = False,
    ) -> dict[str, int | str]:
        session = self.get_session(session_id)
        if not session:
            return {"error": "session_not_found", "imported": 0}
        return self._transcript_ops.import_transcript_events(
            session=session, session_id=session_id,
            transcript_session=transcript_session, overwrite=overwrite,
        )

    def auto_link_transcript_by_branch(
        self, git_branch: str, agent: str | None = None,
    ) -> list[tuple[str, str]]:
        return self._transcript_ops.auto_link_by_branch(
            git_branch=git_branch, graph_dir=self.graph_dir, agent=agent,
        )

    def get_transcript_stats(self, session_id: str) -> dict[str, Any] | None:
        session = self.get_session(session_id)
        if not session or not session.transcript_id:
            return None
        return self._transcript_ops.get_transcript_stats(session)

    # =========================================================================
    # Session Context - delegated to SessionContextBuilder
    # =========================================================================

    def get_version_status(self) -> dict[str, Any]:
        from htmlgraph.session_context import VersionChecker
        return VersionChecker.get_version_status()

    def initialize_git_hooks(self, project_dir: str | Path) -> bool:
        from htmlgraph.session_context import GitHooksInstaller
        return GitHooksInstaller.install(project_dir)

    def get_start_context(
        self, session_id: str, project_dir: str | Path | None = None,
        compute_async: bool = True,
    ) -> str:
        from htmlgraph.session_context import SessionContextBuilder
        if project_dir is None:
            project_dir = self.graph_dir.parent
        builder = SessionContextBuilder(self.graph_dir, project_dir)
        return builder.build(session_id=session_id, compute_async=compute_async)

    def detect_feature_conflicts(self) -> list[dict[str, Any]]:
        from htmlgraph.session_context import SessionContextBuilder
        project_dir = self.graph_dir.parent
        builder = SessionContextBuilder(self.graph_dir, project_dir)
        return builder.detect_feature_conflicts()
