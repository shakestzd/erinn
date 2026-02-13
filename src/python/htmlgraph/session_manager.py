"""
SessionManager - Smart session and activity tracking for AI agents.

Refactored to use modular components for better maintainability.

Provides:
- Session lifecycle management (start, track, end)
- Smart attribution scoring (match activities to features)
- Drift detection (detect when work diverges from feature)
- Auto-completion checking
- WIP limits enforcement
"""

import logging
import re
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any

from htmlgraph.agent_detection import detect_agent_name
from htmlgraph.converter import SessionConverter
from htmlgraph.event_log import JsonlEventLog
from htmlgraph.graph import HtmlGraph
from htmlgraph.ids import generate_id
from htmlgraph.models import ActivityEntry, ErrorEntry, Node, Session
from htmlgraph.services import ClaimingService
from htmlgraph.sessions.attribution import ActivityAttribution
from htmlgraph.sessions.features import FeatureManager
from htmlgraph.sessions.lifecycle import SessionLifecycle
from htmlgraph.sessions.transcripts import TranscriptManager
from htmlgraph.spike_index import ActiveAutoSpikeIndex

logger = logging.getLogger(__name__)


class SessionManager:
    """
    Manages agent sessions with smart attribution and drift detection.

    This class now delegates most functionality to specialized modules:
    - SessionLifecycle: Session start, end, normalization
    - ActivityAttribution: Activity tracking and feature attribution
    - FeatureManager: Feature creation, activation, completion
    - TranscriptManager: Transcript linking and import

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

    # Attribution scoring weights
    WEIGHT_FILE_PATTERN = 0.4
    WEIGHT_KEYWORD = 0.3
    WEIGHT_TYPE_PRIORITY = 0.2
    WEIGHT_IS_PRIMARY = 0.1

    # Type priorities (higher = more likely to be active work)
    TYPE_PRIORITY = {
        "bug": 1.0,
        "hotfix": 1.0,
        "feature": 0.8,
        "spike": 0.6,
        "chore": 0.4,
        "epic": 0.2,
    }

    # WIP limit
    DEFAULT_WIP_LIMIT = 3
    DEFAULT_SESSION_DEDUPE_WINDOW_SECONDS = 120

    # Drift thresholds
    DRIFT_TIME_THRESHOLD = timedelta(minutes=15)
    DRIFT_EVENT_THRESHOLD = 5

    def __init__(
        self,
        graph_dir: str | Path = ".htmlgraph",
        wip_limit: int = DEFAULT_WIP_LIMIT,
        session_dedupe_window_seconds: int = DEFAULT_SESSION_DEDUPE_WINDOW_SECONDS,
        features_graph: HtmlGraph | None = None,
        bugs_graph: HtmlGraph | None = None,
    ):
        """
        Initialize SessionManager.

        Args:
            graph_dir: Directory containing HtmlGraph data
            wip_limit: Maximum features in progress simultaneously
            session_dedupe_window_seconds: Deduplication window for sessions
            features_graph: Optional pre-initialized HtmlGraph for features (avoids double-loading)
            bugs_graph: Optional pre-initialized HtmlGraph for bugs (avoids double-loading)
        """
        self.graph_dir = Path(graph_dir)
        self.wip_limit = wip_limit
        self.session_dedupe_window_seconds = session_dedupe_window_seconds

        # Initialize graphs for each collection
        self.sessions_dir = self.graph_dir / "sessions"
        self.features_dir = self.graph_dir / "features"
        self.bugs_dir = self.graph_dir / "bugs"

        # Ensure directories exist
        self.sessions_dir.mkdir(parents=True, exist_ok=True)
        self.features_dir.mkdir(parents=True, exist_ok=True)
        self.bugs_dir.mkdir(parents=True, exist_ok=True)

        # Session converter
        self.session_converter = SessionConverter(self.sessions_dir)

        # Feature graphs - reuse provided instances to avoid double-loading, or create new with lazy loading
        # Note: Use 'is not None' check because HtmlGraph.__bool__ returns False when empty
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

        # Append-only event log (Git-friendly source of truth for activities)
        self.events_dir = self.graph_dir / "events"
        self.event_log = JsonlEventLog(self.events_dir)

        # Fast index for active auto-generated spikes (avoids scanning all spike files)
        self._spike_index = ActiveAutoSpikeIndex(self.graph_dir)
        self._active_auto_spikes: set[str] = self._spike_index.get_all()

        # Initialize specialized modules
        self._lifecycle = SessionLifecycle(
            htmlgraph_dir=self.graph_dir,
            graph=self.features_graph,  # Sessions are stored in features graph
            event_log=self.event_log,
            active_auto_spike_index=self._spike_index,
            default_agent=detect_agent_name() or "unknown",
            stale_session_hours=24,
        )

        self._attribution = ActivityAttribution(
            htmlgraph_dir=self.graph_dir,
            graph=self.features_graph,
            event_log=self.event_log,
        )

        self._features = FeatureManager(
            htmlgraph_dir=self.graph_dir,
            graph=self.features_graph,
            event_log=self.event_log,
            claiming_service=ClaimingService(
                features_graph=self.features_graph,
                bugs_graph=self.bugs_graph,
                session_manager=self,
            ),
        )

        self._transcripts = TranscriptManager(
            htmlgraph_dir=self.graph_dir,
            graph=self.features_graph,
            event_log=self.event_log,
        )

        # Claiming service (handles feature claims/releases)
        self.claiming_service = self._features.claiming_service

        # Cache for active session
        self._active_session: Session | None = None

        # Cache for active sessions list (invalidated on session lifecycle changes)
        self._active_sessions_cache: list[Session] | None = None
        self._sessions_cache_dirty: bool = True

        # Cache for active features (invalidated on start/complete/release)
        self._active_features_cache: list[Node] | None = None
        self._features_cache_dirty: bool = True

    # =========================================================================
    # Session Lifecycle - Delegate to SessionLifecycle
    # =========================================================================

    def _list_active_sessions(self) -> list[Session]:
        """List all sessions marked as active."""
        return self._lifecycle._list_active_sessions()

    def _choose_canonical_active_session(
        self, sessions: list[Session]
    ) -> Session | None:
        """Choose the canonical active session from a list."""
        return self._lifecycle._choose_canonical_active_session(sessions)

    def _mark_session_stale(self, session: Session) -> None:
        """Mark a session as stale by removing 'active' class."""
        self._lifecycle._mark_session_stale(session)

    def normalize_active_sessions(self) -> dict[str, int]:
        """Ensure only one active session per agent."""
        return self._lifecycle.normalize_active_sessions()

    def start_session(
        self,
        session_id: str | None = None,
        agent: str | None = None,
        title: str | None = None,
        parent_session_id: str | None = None,
        conversation_id: str | None = None,
        metadata: dict[str, Any] | None = None,
        continued_from: str | None = None,
    ) -> Session:
        """Start a new session.

        Args:
            session_id: Unique session identifier
            agent: Agent name (auto-detected if None)
            title: Optional session title
            parent_session_id: Parent session if this is a handoff
            conversation_id: Conversation ID for grouping sessions
            metadata: Additional metadata
            continued_from: Previous session ID if continuing work
        """
        if not session_id:
            session_id = generate_id("session")

        self._sessions_cache_dirty = True
        session = self._lifecycle.start_session(
            session_id=session_id,
            agent=agent,
            title=title,
            parent_session_id=parent_session_id,
            conversation_id=conversation_id,
            metadata=metadata,
        )

        # Set continued_from if provided
        if continued_from:
            session.continued_from = continued_from

        # Always save to session converter so sessions are discoverable
        # by get_last_ended_session(), SessionResume.get_last_session(), etc.
        self.session_converter.save(session)

        # Create session-init spike
        self._create_session_init_spike_for_session(session)

        return session

    def _create_session_init_spike(self, session: Session) -> Node | None:
        """Create initialization spike for session start."""
        return self._lifecycle._create_session_init_spike(session)

    def _create_transition_spike(
        self,
        from_session: Session | None,
        to_session: Session,
        reason: str | None = None,
    ) -> Node | None:
        """Create transition spike for session handoff."""
        return self._lifecycle._create_transition_spike(
            from_session, to_session, reason
        )

    def _complete_transition_spikes_on_conversation_start(
        self, session_id: str
    ) -> list[str]:
        """Complete any active transition spikes when conversation starts."""
        return self._lifecycle._complete_transition_spikes_on_conversation_start(
            session_id
        )

    def _complete_active_auto_spikes(
        self, session_id: str, reason: str = "session_end"
    ) -> list[str]:
        """Complete all active auto-spikes for a session."""
        return self._lifecycle._complete_active_auto_spikes(session_id, reason)

    def get_session(self, session_id: str) -> Session | None:
        """Get session by ID.

        Tries the session converter first (has all fields including handoff data),
        then falls back to the lifecycle graph-based lookup.
        """
        # Try the full-fidelity session converter first (sessions/ directory)
        session = self.session_converter.load(session_id)
        if session:
            return session

        # Fall back to the graph-based lookup
        return self._lifecycle.get_session(session_id)

    def get_last_ended_session(self, agent: str | None = None) -> Session | None:
        """Get the most recently ended session.

        Uses session converter for full-fidelity session data including handoff fields.
        """
        all_sessions = self.session_converter.load_all()
        ended = [s for s in all_sessions if s.status == "ended"]

        if agent:
            ended = [s for s in ended if s.agent == agent]

        if not ended:
            return None

        return max(ended, key=lambda s: s.ended_at or s.started_at)

    def get_active_session(self, agent: str | None = None) -> Session | None:
        """Get the active session."""
        return self._lifecycle.get_active_session(agent)

    def get_active_session_for_agent(self, agent: str) -> Session | None:
        """Get the active session for a specific agent."""
        return self._lifecycle.get_active_session_for_agent(agent)

    def dedupe_orphan_sessions(
        self,
        older_than_hours: int | None = None,
        dry_run: bool = False,
    ) -> dict[str, Any]:
        """Remove duplicate orphan sessions (no events, old)."""
        return self._lifecycle.dedupe_orphan_sessions(older_than_hours, dry_run)

    def end_session(
        self,
        session_id: str,
        status: str = "ended",
        metadata: dict[str, Any] | None = None,
        handoff_notes: str | None = None,
        recommended_next: str | None = None,
        blockers: list[str] | None = None,
    ) -> Session:
        """End a session.

        Args:
            session_id: Session to end
            status: Final status (default: "ended")
            metadata: Additional metadata to merge
            handoff_notes: Optional handoff notes to set before ending
            recommended_next: Optional recommended next steps
            blockers: Optional blockers list
        """
        self._sessions_cache_dirty = True

        # Try to load existing session from converter (has full handoff data)
        existing = self.session_converter.load(session_id)

        session = self._lifecycle.end_session(session_id, status, metadata)

        # Merge data from the converter version (if any) onto the
        # lifecycle-returned session, since lifecycle doesn't store handoff fields,
        # event_count, worked_on, etc.
        if existing:
            if existing.handoff_notes and not session.handoff_notes:
                session.handoff_notes = existing.handoff_notes
            if existing.recommended_next and not session.recommended_next:
                session.recommended_next = existing.recommended_next
            if existing.blockers and not session.blockers:
                session.blockers = existing.blockers
            if existing.recommended_context and not session.recommended_context:
                session.recommended_context = existing.recommended_context
            if existing.worked_on and not session.worked_on:
                session.worked_on = existing.worked_on
            if existing.event_count and not session.event_count:
                session.event_count = existing.event_count
            if existing.continued_from and not session.continued_from:
                session.continued_from = existing.continued_from
            if existing.activity_log and not session.activity_log:
                session.activity_log = existing.activity_log
            if existing.error_log and not session.error_log:
                session.error_log = existing.error_log

        # Apply handoff context from explicit kwargs
        if handoff_notes is not None:
            session.handoff_notes = handoff_notes
        if recommended_next is not None:
            session.recommended_next = recommended_next
        if blockers is not None:
            session.blockers = blockers

        # Always save to session converter so ended sessions are discoverable
        # by get_last_ended_session() and SessionResume.get_last_session()
        self.session_converter.save(session)

        return session

    def _ensure_session_for_agent(self, agent: str) -> Session:
        """Ensure an active session exists for the agent."""
        return self._lifecycle._ensure_session_for_agent(agent)

    # =========================================================================
    # Auto-Spike Management
    # =========================================================================

    def _create_session_init_spike_for_session(self, session: Session) -> Node | None:
        """Create a session-init spike in the spikes/ directory.

        This auto-generated spike tracks the period between session start
        and the first feature being started. It is completed when the first
        feature is started.
        """
        from htmlgraph.converter import NodeConverter

        spikes_dir = self.graph_dir / "spikes"
        spikes_dir.mkdir(parents=True, exist_ok=True)
        converter = NodeConverter(spikes_dir)

        spike_id = f"spike-init-{session.id[:8]}"

        # Idempotent: don't create if already exists
        if converter.exists(spike_id):
            return converter.load(spike_id)

        spike = Node(
            id=spike_id,
            title=f"Session Init: {session.agent}",
            type="spike",
            spike_subtype="session-init",
            auto_generated=True,
            status="in-progress",
            session_id=session.id,
        )

        converter.save(spike)

        # Link spike to session's worked_on
        session.worked_on.append(spike_id)
        self.session_converter.save(session)

        # Track in active auto-spike index
        self._spike_index.add(spike_id, spike_subtype="session-init", session_id=session.id)

        return spike

    def _create_transition_spike_for_feature(
        self, feature_id: str, session: Session
    ) -> Node | None:
        """Create a transition spike when a feature is completed.

        This auto-generated spike tracks the period between completing
        one feature and starting the next. It is completed when the
        next feature is started.
        """
        from htmlgraph.converter import NodeConverter

        spikes_dir = self.graph_dir / "spikes"
        spikes_dir.mkdir(parents=True, exist_ok=True)
        converter = NodeConverter(spikes_dir)

        spike_id = f"spike-transition-{feature_id[:8]}"

        # Idempotent: don't create if already exists
        if converter.exists(spike_id):
            return converter.load(spike_id)

        spike = Node(
            id=spike_id,
            title=f"Transition from {feature_id}",
            type="spike",
            spike_subtype="transition",
            auto_generated=True,
            status="in-progress",
            session_id=session.id,
            from_feature_id=feature_id,
        )

        converter.save(spike)

        # Link spike to session's worked_on
        session_reloaded = self.get_session(session.id)
        if session_reloaded and spike_id not in session_reloaded.worked_on:
            session_reloaded.worked_on.append(spike_id)
            self.session_converter.save(session_reloaded)

        # Track in active auto-spike index
        self._spike_index.add(spike_id, spike_subtype="transition", session_id=session.id)

        return spike

    def _complete_auto_spikes_on_feature_start(self, feature_id: str) -> list[str]:
        """Complete any active auto-spikes when a feature starts.

        Sets the to_feature_id on each completed spike and marks it as done.
        """
        from htmlgraph.converter import NodeConverter

        spikes_dir = self.graph_dir / "spikes"
        if not spikes_dir.exists():
            return []

        converter = NodeConverter(spikes_dir)
        completed = []

        all_spikes = converter.load_all()
        for spike in all_spikes:
            if (
                spike.auto_generated
                and spike.status == "in-progress"
                and spike.spike_subtype in ("session-init", "transition", "conversation-init")
            ):
                spike.status = "done"
                spike.to_feature_id = feature_id
                converter.save(spike)

                # Remove from active index
                self._spike_index.remove(spike.id)
                completed.append(spike.id)

        return completed

    # =========================================================================
    # Session Handoff Methods (kept in SessionManager for now)
    # =========================================================================

    def set_session_handoff(
        self,
        session_id: str,
        handoff_notes: str | None = None,
        recommended_next: str | None = None,
        blockers: list[str] | None = None,
    ) -> Session | None:
        """Set handoff context on a session without ending it."""
        session = self.get_session(session_id)
        if not session:
            return None

        updated = False
        if handoff_notes is not None:
            session.handoff_notes = handoff_notes
            updated = True
        if recommended_next is not None:
            session.recommended_next = recommended_next
            updated = True
        if blockers is not None:
            session.blockers = blockers
            updated = True

        if updated:
            session.add_activity(
                ActivityEntry(
                    tool="SessionHandoff",
                    summary="Session handoff updated",
                    timestamp=datetime.now(),
                )
            )
            self.session_converter.save(session)

        return session

    def continue_from_last(
        self,
        agent: str | None = None,
        auto_create_session: bool = True,
    ) -> tuple[Session | None, Any]:
        """Continue work from the last completed session."""
        from typing import Any

        from htmlgraph.sessions.handoff import SessionResume

        class MinimalSDK:
            def __init__(self, directory: Path) -> None:
                self._directory = directory

        sdk: Any = MinimalSDK(self.graph_dir)
        resume = SessionResume(sdk)

        last_session = resume.get_last_session(agent=agent)
        if not last_session:
            return None, None

        resume_info = resume.build_resume_info(last_session)

        new_session = None
        if auto_create_session:
            session_id = generate_id("sess")
            new_session = self.start_session(
                session_id=session_id,
                agent=agent or last_session.agent,
                title=f"Continuing from {last_session.id}",
            )

            new_session.continued_from = last_session.id
            self.session_converter.save(new_session)

        return new_session, resume_info

    def end_session_with_handoff(
        self,
        session_id: str,
        summary: str | None = None,
        next_focus: str | None = None,
        blockers: list[str] | None = None,
        keep_context: list[str] | None = None,
        auto_recommend_context: bool = True,
    ) -> Session | None:
        """End session with handoff information for next session."""
        from htmlgraph.sessions.handoff import ContextRecommender, HandoffBuilder

        session = self.get_session(session_id)
        if not session:
            return None

        builder = HandoffBuilder(session)

        if summary:
            builder.add_summary(summary)
        if next_focus:
            builder.add_next_focus(next_focus)
        if blockers:
            builder.add_blockers(blockers)
        if keep_context:
            builder.add_context_files(keep_context)

        if auto_recommend_context:
            recommender = ContextRecommender()
            builder.auto_recommend_context(recommender, max_files=10)

        handoff_data = builder.build()

        session.handoff_notes = handoff_data["handoff_notes"]
        session.recommended_next = handoff_data["recommended_next"]
        session.blockers = handoff_data["blockers"]
        session.recommended_context = handoff_data["recommended_context"]

        self.session_converter.save(session)
        ended = self.end_session(session_id)

        # Merge handoff data onto the ended session (end_session may have
        # reloaded from lifecycle which doesn't store handoff fields)
        ended.handoff_notes = handoff_data["handoff_notes"]
        ended.recommended_next = handoff_data["recommended_next"]
        ended.blockers = handoff_data["blockers"]
        ended.recommended_context = handoff_data["recommended_context"]
        self.session_converter.save(ended)

        return ended

    def release_session_features(self, session_id: str) -> list[str]:
        """Release all features claimed by a specific session."""
        return self.claiming_service.release_session_features(session_id)

    # =========================================================================
    # Error Management
    # =========================================================================

    def log_error(
        self,
        session_id: str,
        error: Exception,
        traceback_str: str,
        context: dict[str, Any] | None = None,
    ) -> None:
        """Log error with full traceback to session."""
        session = self.get_session(session_id)
        if not session:
            return

        error_entry = ErrorEntry(
            timestamp=datetime.now(),
            error_type=error.__class__.__name__,
            message=str(error),
            traceback=traceback_str,
        )

        session.error_log.append(error_entry)
        self.session_converter.save(session)

    def get_session_errors(self, session_id: str) -> list[dict[str, Any]]:
        """Retrieve all errors logged for a session."""
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
        """Search errors in a session by type and/or pattern."""
        session = self.get_session(session_id)
        if not session:
            return []

        errors = [error.model_dump() for error in session.error_log]

        if error_type:
            errors = [e for e in errors if e.get("error_type") == error_type]

        if pattern:
            compiled_pattern = re.compile(pattern, re.IGNORECASE)
            errors = [
                e for e in errors if compiled_pattern.search(e.get("message", ""))
            ]

        return errors

    def get_error_summary(self, session_id: str) -> dict[str, Any]:
        """Get summary statistics of errors in a session."""
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
            error_type = error.error_type
            error_types[error_type] = error_types.get(error_type, 0) + 1

        return {
            "total_errors": len(errors),
            "error_types": error_types,
            "first_error": errors[0].model_dump() if errors else None,
            "last_error": errors[-1].model_dump() if errors else None,
        }

    # =========================================================================
    # Activity Tracking - Delegate to ActivityAttribution
    # =========================================================================

    def track_activity(
        self,
        session_id: str,
        tool: str,
        summary: str,
        file_paths: list[str] | None = None,
        success: bool = True,
        feature_id: str | None = None,
        activity_id: str | None = None,
        metadata: dict[str, Any] | None = None,
        parent_activity_id: str | None = None,
        payload: dict[str, Any] | None = None,
        agent: str | None = None,
    ) -> ActivityEntry:
        """Track an activity and attribute it to the best matching feature.

        Args:
            session_id: Session ID
            tool: Tool name
            summary: Activity summary
            file_paths: File paths involved
            success: Whether activity succeeded
            feature_id: Force attribution to specific feature
            activity_id: Specific activity ID
            metadata: Additional metadata
            parent_activity_id: Parent activity ID for nested calls
            payload: Optional rich payload data
            agent: Agent name for event logging
        """
        entry = self._attribution.track_activity(
            session_id=session_id,
            tool=tool,
            summary=summary,
            file_paths=file_paths,
            success=success,
            feature_id=feature_id,
            activity_id=activity_id,
            metadata=metadata,
            agent=agent,
        )
        # Set additional fields that the attribution module doesn't handle
        if parent_activity_id:
            entry.parent_activity_id = parent_activity_id

        # Ensure payload always contains session_id and file_paths for traceability
        merged_payload: dict[str, Any] = {}
        merged_payload["session_id"] = session_id
        if file_paths:
            merged_payload["file_paths"] = file_paths
        if payload:
            merged_payload.update(payload)
        entry.payload = merged_payload

        # Update session's worked_on list and event_count for traceability
        try:
            attributed_feature = entry.feature_id or feature_id
            if attributed_feature or True:  # Always increment event_count
                session = self.session_converter.load(session_id)
                if session:
                    changed = False
                    # Track which features this session worked on
                    if attributed_feature and attributed_feature not in session.worked_on:
                        session.worked_on.append(attributed_feature)
                        changed = True
                    # Increment event count
                    session.event_count = (session.event_count or 0) + 1
                    changed = True
                    if changed:
                        self.session_converter.save(session)
        except Exception:
            pass  # Best-effort; don't fail the activity tracking

        return entry

    def track_user_query(
        self,
        session_id: str,
        query: str,
        feature_id: str | None = None,
    ) -> ActivityEntry:
        """Track a user query as an activity."""
        return self._attribution.track_user_query(session_id, query, feature_id)

    def _get_active_auto_spike(self, active_features: list[Node]) -> Node | None:
        """Find an active auto-generated spike."""
        for feature in active_features:
            if (
                feature.type == "spike"
                and feature.auto_generated
                and feature.spike_subtype
                in ("session-init", "conversation-init", "transition")
                and feature.status == "in-progress"
            ):
                return feature
        return None

    def attribute_activity(
        self,
        tool: str,
        summary: str,
        file_paths: list[str],
        active_features: list[Node],
    ) -> str | None:
        """Attribute activity to best matching feature using scoring."""
        return self._attribution.attribute_activity(
            tool, summary, file_paths, active_features
        )

    def _score_feature_match(
        self,
        tool: str,
        summary: str,
        file_paths: list[str],
        feature: Node,
    ) -> float:
        """Score how well an activity matches a feature."""
        return self._attribution._score_feature_match(
            tool, summary, file_paths, feature
        )

    def _score_file_patterns(
        self,
        file_paths: list[str],
        patterns: list[str],
    ) -> float:
        """Score file path matches against patterns."""
        return self._attribution._score_file_patterns(file_paths, patterns)

    def _extract_keywords(self, text: str) -> set[str]:
        """Extract meaningful keywords from text."""
        return self._attribution._extract_keywords(text)

    def _score_keyword_overlap(self, text: str, keywords: set[str]) -> float:
        """Score keyword overlap between text and keyword set."""
        return self._attribution._score_keyword_overlap(text, keywords)

    def _is_system_overhead(
        self,
        tool: str,
        summary: str,
        file_paths: list[str],
    ) -> bool:
        """Detect if activity is system overhead."""
        return self._attribution._is_system_overhead(tool, summary, file_paths)

    # =========================================================================
    # Drift Detection
    # =========================================================================

    def detect_drift(self, session_id: str, feature_id: str) -> dict[str, Any]:
        """Detect if current work is drifting from a feature."""
        session = self.get_session(session_id)
        if not session:
            return {"is_drifting": False, "drift_score": 0, "reasons": []}

        reasons = []
        drift_indicators = 0

        feature_activities = [
            a for a in session.activity_log[-20:] if a.feature_id == feature_id
        ]

        if not feature_activities:
            return {
                "is_drifting": False,
                "drift_score": 0,
                "reasons": ["no_recent_activity"],
            }

        last_activity = feature_activities[-1]
        time_since = datetime.now() - last_activity.timestamp
        if time_since > self.DRIFT_TIME_THRESHOLD:
            drift_indicators += 1
            reasons.append(f"stalled_{int(time_since.total_seconds() / 60)}min")

        recent_tools = [a.tool for a in feature_activities[-10:]]
        if len(recent_tools) >= 6:
            tool_counts: dict[str, int] = {}
            for t in recent_tools:
                tool_counts[t] = tool_counts.get(t, 0) + 1
            max_repeat = max(tool_counts.values())
            if max_repeat >= 5:
                drift_indicators += 1
                reasons.append("repetitive_pattern")

        drift_scores = [
            a.drift_score for a in feature_activities if a.drift_score is not None
        ]
        if drift_scores:
            avg_drift = sum(drift_scores) / len(drift_scores)
            if avg_drift > 0.6:
                drift_indicators += 1
                reasons.append(f"high_avg_drift_{avg_drift:.2f}")

        failures = sum(1 for a in feature_activities[-10:] if not a.success)
        if failures >= 3:
            drift_indicators += 1
            reasons.append(f"failures_{failures}")

        is_drifting = drift_indicators >= 2
        drift_score = min(drift_indicators / 4, 1.0)

        return {
            "is_drifting": is_drifting,
            "drift_score": drift_score,
            "reasons": reasons,
            "indicators": drift_indicators,
        }

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
        """Log work item action if agent is available."""
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
            return

    # =========================================================================
    # Feature Management - Delegate to FeatureManager
    # =========================================================================

    def get_active_features(self) -> list[Node]:
        """Get all active features."""
        return self._features.get_active_features()

    def _compute_active_features(self) -> list[Node]:
        """Compute active features from graph."""
        return self._features._compute_active_features()

    def create_feature(
        self,
        title: str,
        description: str = "",
        file_patterns: list[str] | None = None,
        metadata: dict[str, Any] | None = None,
        feature_id: str | None = None,
        collection: str = "features",
        priority: str = "medium",
        agent: str | None = None,
        steps: list[str] | None = None,
    ) -> Node:
        """Create a new feature.

        Args:
            title: Feature title
            description: Feature description
            file_patterns: File patterns for attribution
            metadata: Additional metadata
            feature_id: Specific feature ID to use
            collection: Collection to store in (accepted for backward compat, currently unused)
            priority: Priority level (accepted for backward compat, stored in metadata)
            agent: Agent creating the feature (accepted for backward compat, stored in metadata)
            steps: Implementation steps (accepted for backward compat, stored in metadata)
        """
        self._features_cache_dirty = True
        # Merge priority, agent, and steps into metadata for backward compatibility
        merged_metadata = dict(metadata or {})
        if priority and priority != "medium":
            merged_metadata["priority"] = priority
        if agent:
            merged_metadata["agent"] = agent
        if steps:
            merged_metadata["steps"] = steps
        return self._features.create_feature(
            title=title,
            description=description,
            file_patterns=file_patterns,
            metadata=merged_metadata if merged_metadata else metadata,
            feature_id=feature_id,
        )

    def get_primary_feature(self) -> Node | None:
        """Get the primary (most recent active) feature."""
        return self._features.get_primary_feature()

    def start_feature(
        self,
        feature_id: str,
        session_id: str | None = None,
        agent: str | None = None,
    ) -> Node:
        """Start work on a feature."""
        self._features_cache_dirty = True

        # Complete any active auto-spikes (session-init, transition) when feature starts
        self._complete_auto_spikes_on_feature_start(feature_id)

        return self._features.start_feature(feature_id, session_id, agent)

    def complete_feature(
        self,
        feature_id: str,
        session_id: str | None = None,
        agent: str | None = None,
        metadata: dict[str, Any] | None = None,
        log_activity: bool = False,
        transcript_id: str | None = None,
        collection: str = "features",
    ) -> Node:
        """Complete a feature.

        Args:
            feature_id: Feature to complete
            session_id: Current session ID
            agent: Agent name
            metadata: Additional completion metadata
            log_activity: Whether to log a completion activity
            transcript_id: Optional transcript ID to link
            collection: Collection name (accepted for backward compat, currently unused)
        """
        self._features_cache_dirty = True
        # Merge transcript_id into metadata if provided
        merged_metadata = dict(metadata or {})
        if transcript_id:
            merged_metadata["transcript_id"] = transcript_id

        result = self._features.complete_feature(
            feature_id, session_id, agent, merged_metadata if merged_metadata else metadata
        )

        # Link transcript if provided
        if transcript_id:
            self._link_transcript_to_feature(result, transcript_id, self._get_graph_for_node(result))

        # Create transition spike after feature completion
        active_session = self.get_active_session(agent=agent)
        if active_session:
            self._create_transition_spike_for_feature(feature_id, active_session)

        return result

    def set_primary_feature(self, feature_id: str, agent: str | None = None) -> Node:
        """Set a feature as primary.

        Args:
            feature_id: Feature to make primary
            agent: Agent name (accepted for backward compat, currently unused)
        """
        return self._features.set_primary_feature(feature_id)

    def activate_feature(
        self,
        feature_id: str,
        session_id: str | None = None,
        agent: str | None = None,
        collection: str | None = None,
    ) -> Node:
        """Activate a feature.

        Args:
            feature_id: Feature to activate
            session_id: Current session ID
            agent: Agent name
            collection: Collection name (features or bugs). If provided,
                        sets is_primary property on the node.
        """
        self._features_cache_dirty = True
        node = self._features.activate_feature(feature_id, session_id, agent)

        # If called via MCP with collection, set primary flag
        if collection:
            node.properties["is_primary"] = True
            graph = self._get_graph(collection)
            graph.update(node)

            # Log activation event directly to event log (bypass track_activity
            # which would create a secondary ActivityTracked event)
            target_agent = agent or "unknown"
            try:
                from htmlgraph.event_log import EventRecord

                session = self._ensure_session_for_agent(target_agent)
                self.event_log.append(
                    EventRecord(
                        event_id=generate_id("event"),
                        tool="FeatureActivate",
                        summary=f"Activated feature {feature_id}",
                        success=True,
                        session_id=session.id,
                        agent=target_agent,
                        timestamp=datetime.now(),
                        feature_id=feature_id,
                    )
                )
            except Exception:
                pass

        return node

    def _check_completion(self, feature_id: str, tool: str, success: bool) -> bool:
        """Check if feature should be auto-completed."""
        return self._features._check_completion(feature_id, tool, success)

    def claim_feature(
        self,
        feature_id: str,
        collection: str = "features",
        *,
        agent: str,
    ) -> Node | None:
        """Claim a feature for an agent."""
        return self.claiming_service.claim_feature(
            feature_id=feature_id,
            collection=collection,
            agent=agent,
        )

    def release_feature(
        self,
        feature_id: str,
        collection: str = "features",
        *,
        agent: str,
    ) -> Node | None:
        """Release a feature claim."""
        return self.claiming_service.release_feature(
            feature_id=feature_id,
            collection=collection,
            agent=agent,
        )

    def auto_release_features(self, agent: str) -> list[str]:
        """Release all features claimed by an agent."""
        return self.claiming_service.auto_release_features(agent)

    # =========================================================================
    # Status and Information
    # =========================================================================

    def get_status(self) -> dict[str, Any]:
        """Get overall project status."""
        all_features = list(self.features_graph) + list(self.bugs_graph)

        by_status = {"todo": 0, "in-progress": 0, "blocked": 0, "done": 0}
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
    # Handoff Creation
    # =========================================================================

    def create_handoff(
        self,
        feature_id: str,
        reason: str,
        notes: str | None = None,
        collection: str = "features",
        *,
        agent: str,
        next_agent: str | None = None,
    ) -> Node | None:
        """Create a handoff context for a feature."""
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
            agent=agent,
            tool="FeatureHandoff",
            summary=f"Handed off: {collection}/{feature_id} (reason: {reason})",
            feature_id=feature_id,
            payload={
                "collection": collection,
                "action": "handoff",
                "reason": reason,
                "notes": notes,
                "next_agent": next_agent,
            },
        )

        return node

    # =========================================================================
    # Helper Methods
    # =========================================================================

    def _add_session_link_to_feature(self, feature_id: str, session_id: str) -> None:
        """Add bidirectional link between feature and session."""
        from htmlgraph.models import Edge

        feature_node = self.features_graph.get(feature_id) or self.bugs_graph.get(
            feature_id
        )
        if not feature_node:
            return

        existing_sessions = feature_node.edges.get("implemented-in", [])
        feature_already_linked = any(
            edge.target_id == session_id for edge in existing_sessions
        )

        if not feature_already_linked:
            edge = Edge(
                target_id=session_id,
                relationship="implemented-in",
                title=session_id,
                since=datetime.now(),
            )
            feature_node.add_edge(edge)

            graph = self._get_graph_for_node(feature_node)
            graph.update(feature_node)

        session = self.get_session(session_id)
        if not session:
            return

        if feature_id not in session.worked_on:
            session.worked_on.append(feature_id)
            self.session_converter.save(session)

    def _link_transcript_to_feature(
        self,
        node: Node,
        transcript_id: str,
        graph: HtmlGraph,
    ) -> None:
        """Link a Claude Code transcript to a feature."""
        from htmlgraph.models import Edge

        existing_transcripts = node.edges.get("implemented-by", [])
        already_linked = any(
            edge.target_id == transcript_id for edge in existing_transcripts
        )

        if already_linked:
            return

        tool_count = 0
        duration_seconds = 0
        tool_breakdown = {}

        try:
            from htmlgraph.transcript import TranscriptReader

            reader = TranscriptReader()
            transcript = reader.read_session(transcript_id)
            if transcript:
                tool_count = transcript.tool_call_count
                duration_seconds = int(transcript.duration_seconds or 0)
                tool_breakdown = transcript.tool_breakdown
        except Exception as e:
            logger.warning(
                f"Failed to get transcript analytics for {transcript_id}: {e}"
            )

        edge = Edge(
            target_id=transcript_id,
            relationship="implemented-by",
            title=transcript_id,
            since=datetime.now(),
            properties={
                "tool_count": tool_count,
                "duration_seconds": duration_seconds,
                "tool_breakdown": tool_breakdown,
            },
        )
        node.add_edge(edge)

        if tool_count > 0:
            node.properties["transcript_tool_count"] = tool_count
            node.properties["transcript_duration_seconds"] = duration_seconds

    def _get_graph(self, collection: str) -> HtmlGraph:
        """Get graph for a collection."""
        if collection == "bugs":
            return self.bugs_graph
        return self.features_graph

    def _get_graph_for_node(self, node: Node) -> HtmlGraph:
        """Get the graph that contains a node."""
        if node.type == "bug":
            return self.bugs_graph
        return self.features_graph

    def _get_current_commit(self) -> str | None:
        """Get current git commit hash."""
        return self._transcripts._get_current_commit()

    # =========================================================================
    # Transcript Integration - Delegate to TranscriptManager
    # =========================================================================

    def link_transcript(
        self,
        session_id: str,
        transcript_path: str | Path,
        transcript_id: str | None = None,
        git_branch: str | None = None,
    ) -> Session:
        """Link a transcript file to a session.

        Args:
            session_id: Session ID
            transcript_path: Path to transcript file
            transcript_id: Optional transcript ID (accepted for backward compat)
            git_branch: Optional git branch name (accepted for backward compat)
        """
        return self._transcripts.link_transcript(session_id, transcript_path)

    def find_session_by_transcript(
        self,
        transcript_path: str | Path,
    ) -> Session | None:
        """Find session by transcript path."""
        return self._transcripts.find_session_by_transcript(transcript_path)

    def import_transcript_events(
        self,
        transcript_path: str | Path,
        session_id: str,
    ) -> dict[str, Any]:
        """Import events from a transcript file."""
        return self._transcripts.import_transcript_events(transcript_path, session_id)

    def auto_link_transcript_by_branch(
        self,
        session_id: str,
        branch_name: str | None = None,
    ) -> dict[str, Any]:
        """Auto-link transcript by matching git branch."""
        return self._transcripts.auto_link_transcript_by_branch(session_id, branch_name)

    def get_transcript_stats(self, session_id: str) -> dict[str, Any] | None:
        """Get statistics about a session's transcript."""
        return self._transcripts.get_transcript_stats(session_id)

    # =========================================================================
    # Utility Methods
    # =========================================================================

    def get_version_status(self) -> dict[str, Any]:
        """Check installed htmlgraph version against latest on PyPI."""
        from htmlgraph.session_context import VersionChecker

        return VersionChecker.get_version_status()

    def initialize_git_hooks(self, project_dir: str | Path) -> bool:
        """Install pre-commit hooks if not already installed."""
        from htmlgraph.session_context import GitHooksInstaller

        return GitHooksInstaller.install(project_dir)

    def get_start_context(
        self,
        session_id: str,
        project_dir: str | Path | None = None,
        compute_async: bool = True,
    ) -> str:
        """Build complete session start context for AI agents."""
        from htmlgraph.session_context import SessionContextBuilder

        if project_dir is None:
            project_dir = self.graph_dir.parent

        builder = SessionContextBuilder(self.graph_dir, project_dir)
        return builder.build(session_id=session_id, compute_async=compute_async)

    def detect_feature_conflicts(self) -> list[dict[str, Any]]:
        """Detect features being worked on by multiple agents simultaneously."""
        from htmlgraph.session_context import SessionContextBuilder

        project_dir = self.graph_dir.parent
        builder = SessionContextBuilder(self.graph_dir, project_dir)
        return builder.detect_feature_conflicts()
