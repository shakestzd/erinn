"""
SessionManager - Smart session and activity tracking for AI agents.

Provides:
- Session lifecycle management (start, track, end)
- Smart attribution scoring (match activities to features)
- Drift detection (detect when work diverges from feature)
- Auto-completion checking
- WIP limits enforcement
"""

import fnmatch
import logging
import re
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any

logger = logging.getLogger(__name__)

from htmlgraph.agent_detection import detect_agent_name
from htmlgraph.converter import (
    SessionConverter,
    dict_to_node,
)
from htmlgraph.db.schema import HtmlGraphDB
from htmlgraph.event_log import EventRecord, JsonlEventLog
from htmlgraph.exceptions import SessionNotFoundError
from htmlgraph.graph import HtmlGraph
from htmlgraph.ids import generate_id
from htmlgraph.models import ActivityEntry, ErrorEntry, Node, Session
from htmlgraph.services import ClaimingService
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

        # Initialize database connection for session status updates
        db_path = str(self.graph_dir / "htmlgraph.db")
        self._db = HtmlGraphDB(db_path)

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

        # Claiming service (handles feature claims/releases)
        self.claiming_service = ClaimingService(
            features_graph=self.features_graph,
            bugs_graph=self.bugs_graph,
            session_manager=self,
        )

        # Cache for active session
        self._active_session: Session | None = None

        # Cache for active sessions list (invalidated on session lifecycle changes)
        self._active_sessions_cache: list[Session] | None = None
        self._sessions_cache_dirty: bool = True

        # Cache for active features (invalidated on start/complete/release)
        self._active_features_cache: list[Node] | None = None
        self._features_cache_dirty: bool = True

        # Fast index for active auto-generated spikes (avoids scanning all spike files)
        self._spike_index = ActiveAutoSpikeIndex(self.graph_dir)
        self._active_auto_spikes: set[str] = self._spike_index.get_all()

        # Append-only event log (Git-friendly source of truth for activities)
        self.events_dir = self.graph_dir / "events"
        self.event_log = JsonlEventLog(self.events_dir)

    # =========================================================================
    # Session Lifecycle
    # =========================================================================

    def _list_active_sessions(self) -> list[Session]:
        """
        Return all active sessions found on disk.

        Uses caching to avoid repeated file I/O. The cache is invalidated
        automatically when sessions are created, ended, or marked as stale.
        """
        if self._sessions_cache_dirty or self._active_sessions_cache is None:
            self._active_sessions_cache = [
                s for s in self.session_converter.load_all() if s.status == "active"
            ]
            self._sessions_cache_dirty = False
        return self._active_sessions_cache

    def _choose_canonical_active_session(
        self, sessions: list[Session]
    ) -> Session | None:
        """Choose a stable 'canonical' session when multiple are active."""
        if not sessions:
            return None
        sessions.sort(
            key=lambda s: (s.event_count, s.last_activity.timestamp()),
            reverse=True,
        )
        return sessions[0]

    def _mark_session_stale(self, session: Session) -> None:
        """Mark a session as stale (kept for history but not considered active)."""
        if session.status != "active":
            return
        now = datetime.now(timezone.utc)
        session.status = "stale"
        session.ended_at = now
        session.last_activity = now
        self.session_converter.save(session)
        self._sessions_cache_dirty = True

    def normalize_active_sessions(self) -> dict[str, int]:
        """
        Ensure a stable active-session set.

        Keeps at most one active, non-subagent session per agent (the canonical one)
        and marks the rest as stale.
        """
        active_sessions = self._list_active_sessions()
        kept = 0
        staled = 0

        by_agent: dict[str, list[Session]] = {}
        for s in active_sessions:
            if s.is_subagent:
                continue
            by_agent.setdefault(s.agent, []).append(s)

        for agent, sessions in by_agent.items():
            canonical = self._choose_canonical_active_session(sessions)
            if not canonical:
                continue
            kept += 1
            for s in sessions:
                if s.id != canonical.id:
                    self._mark_session_stale(s)
                    staled += 1

        return {"kept": kept, "staled": staled}

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
        """
        Start a new session.

        Args:
            session_id: Unique session identifier (auto-generated if None)
            agent: Agent name (auto-detected if None)
            is_subagent: True if this is a Task subagent
            continued_from: Previous session ID if continuing
            start_commit: Git commit hash at session start
            title: Optional human-readable title
            parent_session_id: ID of parent session (for subagents)

        Returns:
            New Session instance
        """
        # Auto-detect agent if not provided
        if agent is None:
            agent = detect_agent_name()

        now = datetime.now()

        # Auto-generate collision-resistant session ID if not provided
        if session_id is None:
            session_id = generate_id(node_type="session", title=title or agent)

        desired_commit = start_commit or self._get_current_commit()

        # Idempotency: if the session already exists, treat this as a no-op start.
        existing = self.session_converter.load(session_id)
        if existing:
            if existing.status != "active":
                existing.status = "active"
            existing.last_activity = now
            if not existing.start_commit:
                existing.start_commit = desired_commit
            if title and not existing.title:
                existing.title = title
            self.session_converter.save(existing)
            self._sessions_cache_dirty = True
            self._active_session = existing
            return existing

        # Dedupe: if a canonical active session already exists for this agent/commit,
        # reuse it instead of creating a new file (prevents session explosion).
        #
        # IMPORTANT: We reuse the session REGARDLESS of time elapsed. A session
        # represents the entire Claude Code process lifecycle, not a time window.
        # The session will only end when the Stop hook is called (process terminates).
        if not is_subagent:
            active_sessions = [
                s
                for s in self._list_active_sessions()
                if (not s.is_subagent) and s.agent == agent
            ]
            canonical = self._choose_canonical_active_session(active_sessions)
            if canonical and canonical.start_commit == desired_commit:
                # Reuse the canonical session regardless of time since last activity.
                # This ensures ONE session per Claude Code process, even if the user
                # pauses for hours between commands.
                self._active_session = canonical
                canonical.last_activity = now  # Update activity timestamp
                self.session_converter.save(canonical)
                self._sessions_cache_dirty = True
                return canonical

            # If we're truly starting a new session (different commit), mark old sessions as stale.
            for s in active_sessions:
                self._mark_session_stale(s)

        session = Session(
            id=session_id,
            agent=agent,
            is_subagent=is_subagent,
            continued_from=continued_from,
            start_commit=desired_commit,
            status="active",
            started_at=now,
            last_activity=now,
            title=title or "",
            parent_session=parent_session_id,
        )

        # Add session start event
        session.add_activity(
            ActivityEntry(
                tool="SessionStart",
                summary="Session started",
                timestamp=now,
            )
        )

        # Set parent session in environment for subsequent subprocesses (e.g. HeadlessSpawner)
        # This ensures that any tools spawned by this session link back to it
        import os

        os.environ["HTMLGRAPH_PARENT_SESSION"] = session.id

        # Save to disk
        self.session_converter.save(session)
        self._sessions_cache_dirty = True
        self._active_session = session

        # Complete any lingering transition spikes from previous conversations
        # This marks the end of the previous conversation's transition period
        self._complete_transition_spikes_on_conversation_start(session.agent)

        # Auto-create session-init spike for transitional activities
        self._create_session_init_spike(session)

        return session

    def _create_session_init_spike(self, session: Session) -> Node | None:
        """
        Auto-create a session-init spike to catch pre-feature activities.

        This spike captures work done before the first feature is started:
        - Session startup, reviewing context
        - Planning what to work on
        - General exploration

        The spike auto-completes when the first feature is started.
        """
        from htmlgraph.converter import NodeConverter

        spike_id = f"spike-init-{session.id[:8]}"

        # Check if spike already exists (idempotency)
        spike_converter = NodeConverter(self.graph_dir / "spikes")
        existing = spike_converter.load(spike_id)
        if existing:
            # Add to index if it's still active
            if existing.status == "in-progress":
                self._active_auto_spikes.add(existing.id)
                self._spike_index.add(existing.id, "session-init", session.id)
            return existing

        # Create session-init spike
        spike = Node(
            id=spike_id,
            title=f"Session Init: {session.agent}",
            type="spike",
            status="in-progress",
            priority="low",
            spike_subtype="session-init",
            auto_generated=True,
            session_id=session.id,
            model_name=session.agent,  # Store agent name as model
            content="Auto-generated spike for session startup activities.\n\nCaptures work before first feature is started:\n- Context review\n- Planning\n- Exploration\n\nAuto-completes when first feature is claimed.",
        )

        # Save spike
        spike_converter.save(spike)

        # Add to active auto-spikes index (both in-memory and persistent)
        self._active_auto_spikes.add(spike.id)
        self._spike_index.add(spike.id, "session-init", session.id)

        # Link session to spike
        if spike.id not in session.worked_on:
            session.worked_on.append(spike.id)
            self.session_converter.save(session)

        return spike

    def _create_transition_spike(
        self, session: Session, from_feature_id: str
    ) -> Node | None:
        """
        Auto-create a transition spike after feature completion.

        This spike captures work done between features:
        - Post-completion cleanup
        - Review and planning
        - Context switching

        The spike auto-completes when the next feature is started.
        """
        from htmlgraph.converter import NodeConverter

        spike_id = generate_id(node_type="spike", title="transition")

        # Create transition spike
        spike = Node(
            id=spike_id,
            title=f"Transition from {from_feature_id[:12]}",
            type="spike",
            status="in-progress",
            priority="low",
            spike_subtype="transition",
            auto_generated=True,
            session_id=session.id,
            from_feature_id=from_feature_id,
            model_name=session.agent,
            content=f"Auto-generated transition spike.\n\nCaptures post-completion activities:\n- Cleanup and review\n- Planning next work\n- Context switching\n\nFrom: {from_feature_id}\nAuto-completes when next feature is started.",
        )

        # Save spike
        spike_converter = NodeConverter(self.graph_dir / "spikes")
        spike_converter.save(spike)

        # Add to active auto-spikes index (both in-memory and persistent)
        self._active_auto_spikes.add(spike.id)
        self._spike_index.add(spike.id, "transition", session.id)

        # Link session to spike
        if spike.id not in session.worked_on:
            session.worked_on.append(spike.id)
            self.session_converter.save(session)

        return spike

    def _complete_transition_spikes_on_conversation_start(
        self, agent: str
    ) -> list[Node]:
        """
        Complete transition spikes from previous conversations when a new conversation starts.

        This implements the state management pattern:
        1. Work item completes → creates transition spike
        2. New conversation starts → completes previous transition spike
        3. New work item starts → completes session-init spike

        Args:
            agent: Agent starting the new conversation

        Returns:
            List of completed transition spikes
        """
        from htmlgraph.converter import NodeConverter

        spike_converter = NodeConverter(self.graph_dir / "spikes")
        completed_spikes = []

        # Complete only TRANSITION spikes (not session-init, which should persist)
        for spike_id in list(self._active_auto_spikes):
            spike = spike_converter.load(spike_id)

            if not spike:
                self._active_auto_spikes.discard(spike_id)
                self._spike_index.remove(spike_id)
                continue

            # Only complete transition spikes on conversation start
            if not (
                spike.type == "spike"
                and getattr(spike, "auto_generated", False)
                and getattr(spike, "spike_subtype", None) == "transition"
                and spike.status == "in-progress"
            ):
                continue

            # Complete the transition spike
            spike.status = "done"
            spike.updated = datetime.now()
            spike.properties["completed_by"] = "conversation-start"

            spike_converter.save(spike)
            completed_spikes.append(spike)
            self._active_auto_spikes.discard(spike_id)
            self._spike_index.remove(spike_id)

            logger.debug(f"Completed transition spike {spike_id} on conversation start")

        return completed_spikes

    def _complete_active_auto_spikes(
        self, agent: str, to_feature_id: str
    ) -> list[Node]:
        """
        Auto-complete any active auto-generated spikes when a feature starts.

        When starting a regular feature, the transitional period is over,
        so we complete session-init and transition spikes.

        Args:
            agent: Agent starting the feature
            to_feature_id: Feature being started

        Returns:
            List of completed spikes
        """
        from htmlgraph.converter import NodeConverter

        spike_converter = NodeConverter(self.graph_dir / "spikes")
        completed_spikes = []

        # Only load spikes we know are active from the index
        # This avoids the expensive load_all() operation
        for spike_id in list(self._active_auto_spikes):
            spike = spike_converter.load(spike_id)

            # Safety check: verify it's actually an active auto-spike
            if not spike:
                # Spike was deleted or doesn't exist - remove from index
                self._active_auto_spikes.discard(spike_id)
                self._spike_index.remove(spike_id)
                continue

            if not (
                spike.type == "spike"
                and spike.auto_generated
                and spike.spike_subtype
                in ("session-init", "transition", "conversation-init")
                and spike.status == "in-progress"
            ):
                # Spike is no longer active - remove from index
                self._active_auto_spikes.discard(spike_id)
                self._spike_index.remove(spike_id)
                continue

            # Complete the spike
            spike.status = "done"
            spike.updated = datetime.now()
            spike.to_feature_id = (
                to_feature_id  # Record what feature we transitioned to
            )

            spike_converter.save(spike)
            completed_spikes.append(spike)

            # Remove from active index (both in-memory and persistent) since it's now completed
            self._active_auto_spikes.discard(spike_id)
            self._spike_index.remove(spike_id)

        # Import transcript when auto-spikes complete (work boundary)
        if completed_spikes:
            session = self.get_active_session(agent=agent)
            if session and session.transcript_id:
                try:
                    from htmlgraph.transcript import TranscriptReader

                    reader = TranscriptReader()
                    transcript = reader.read_session(session.transcript_id)
                    if transcript:
                        self.import_transcript_events(
                            session_id=session.id,
                            transcript_session=transcript,
                            overwrite=True,
                        )
                except Exception as e:
                    logger.warning(
                        f"Failed to import transcript events on auto-spike completion: {e}"
                    )

        return completed_spikes

    def get_session(self, session_id: str) -> Session | None:
        """Get a session by ID."""
        if self._active_session and self._active_session.id == session_id:
            return self._active_session
        return self.session_converter.load(session_id)

    def get_last_ended_session(self, agent: str | None = None) -> Session | None:
        """Get the most recently ended session (optionally filtered by agent)."""
        sessions = [
            s for s in self.session_converter.load_all() if s.status == "completed"
        ]
        if agent:
            sessions = [s for s in sessions if s.agent == agent]
        if not sessions:
            return None

        def sort_key(session: Session) -> datetime:
            if session.ended_at:
                return session.ended_at
            if session.last_activity:
                return session.last_activity
            return session.started_at

        sessions.sort(key=sort_key, reverse=True)
        return sessions[0]

    def get_active_session(self, agent: str | None = None) -> Session | None:
        """
        Get the currently active session (if any).

        Args:
            agent: Optional agent filter
        """
        if self._active_session and self._active_session.status == "active":
            if not agent or self._active_session.agent == agent:
                return self._active_session

        sessions = self._list_active_sessions()
        if agent:
            sessions = [s for s in sessions if s.agent == agent]

        canonical = self._choose_canonical_active_session(sessions)
        if canonical:
            self._active_session = canonical
            return canonical

        return None

    def get_active_session_for_agent(self, agent: str) -> Session | None:
        """
        Get the currently active session for a specific agent.

        This avoids cross-agent pollution (e.g. Codex logging into a Claude session)
        when multiple agents are active in the same repository.
        """
        if not agent:
            return self.get_active_session()

        if (
            self._active_session
            and self._active_session.status == "active"
            and self._active_session.agent == agent
        ):
            return self._active_session

        sessions = [s for s in self._list_active_sessions() if s.agent == agent]
        canonical = self._choose_canonical_active_session(sessions)
        if canonical:
            self._active_session = canonical
            return canonical
        return None

    def dedupe_orphan_sessions(
        self,
        max_events: int = 1,
        move_dir_name: str = "_orphans",
        dry_run: bool = False,
        stale_extra_active: bool = True,
    ) -> dict[str, int]:
        """
        Move low-signal sessions (e.g. SessionStart-only) out of the main sessions dir.

        Rationale:
        - Prevents thousands of tiny session files from polluting `.htmlgraph/sessions/`
        - Keeps Git diffs readable
        - Keeps "active session" selection stable
        """
        moved = 0
        scanned = 0
        missing = 0

        dest_dir = self.sessions_dir / move_dir_name
        if not dry_run:
            dest_dir.mkdir(parents=True, exist_ok=True)

        for session in self.session_converter.load_all():
            scanned += 1

            # Only consider truly tiny sessions.
            if session.event_count > max_events:
                continue
            if len(session.activity_log) > max_events:
                continue
            if session.activity_log and session.activity_log[0].tool != "SessionStart":
                continue

            src = self.sessions_dir / f"{session.id}.html"
            if not src.exists():
                missing += 1
                continue

            if not dry_run and session.status == "active":
                self._mark_session_stale(session)

            if not dry_run:
                src.rename(dest_dir / src.name)

            moved += 1

        normalized = {"kept": 0, "staled": 0}
        if stale_extra_active and not dry_run:
            normalized = self.normalize_active_sessions()

        return {
            "scanned": scanned,
            "moved": moved,
            "missing": missing,
            "kept_active": normalized.get("kept", 0),
            "staled_active": normalized.get("staled", 0),
        }

    def end_session(
        self,
        session_id: str,
        handoff_notes: str | None = None,
        recommended_next: str | None = None,
        blockers: list[str] | None = None,
        end_commit: str | None = None,
    ) -> Session | None:
        """
        End a session.

        Args:
            session_id: Session to end
            handoff_notes: Optional handoff notes for next session
            recommended_next: Optional recommended next steps
            blockers: Optional list of blockers
            end_commit: Optional git commit hash at session end

        Returns:
            Updated Session or None if not found
        """
        session = self.get_session(session_id)
        if not session:
            return None

        if handoff_notes is not None:
            session.handoff_notes = handoff_notes
        if recommended_next is not None:
            session.recommended_next = recommended_next
        if blockers is not None:
            session.blockers = blockers
        if end_commit is not None:
            session.end_commit = end_commit
        elif not session.end_commit:
            # Auto-detect current commit if not provided
            session.end_commit = self._get_current_commit()

        session.end()
        session.add_activity(
            ActivityEntry(
                tool="SessionEnd",
                summary="Session ended",
                timestamp=datetime.now(timezone.utc),
            )
        )

        # Release all features claimed by this session
        self.release_session_features(session_id)

        self.session_converter.save(session)
        self._sessions_cache_dirty = True

        # Update database with session completion status
        if self._db:
            try:
                if self._db.connection:
                    cursor = self._db.connection.cursor()
                    cursor.execute(
                        "UPDATE sessions SET status = ?, completed_at = ? WHERE session_id = ?",
                        (
                            session.status,
                            session.ended_at.isoformat() if session.ended_at else None,
                            session_id,
                        ),
                    )
                    self._db.connection.commit()
            except Exception as e:
                logger.warning(f"Failed to update session status in database: {e}")

        if self._active_session and self._active_session.id == session_id:
            self._active_session = None

        return session

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
    ) -> tuple[Session | None, Any]:  # Returns (new_session, resume_info)
        """
        Continue work from the last completed session.

        Loads context from the previous session including:
        - Handoff notes and next focus
        - Blockers
        - Recommended context files
        - Recent commits
        - Features worked on

        Args:
            agent: Filter by agent (None = current agent)
            auto_create_session: Create new session if True

        Returns:
            Tuple of (new_session, resume_info) or (None, None) if no previous session

        Example:
            >>> manager = SessionManager(".htmlgraph")
            >>> new_session, resume = manager.continue_from_last(agent="claude")
            >>> if resume:
            ...     print(resume.summary)
            ...     print(resume.recommended_files)
        """
        # Import handoff module
        from typing import Any

        from htmlgraph.sessions.handoff import SessionResume

        # Create a minimal SDK-like object with just the directory
        # to avoid circular dependency and database initialization issues
        class MinimalSDK:
            def __init__(self, directory: Path) -> None:
                self._directory = directory

        sdk: Any = MinimalSDK(self.graph_dir)
        resume = SessionResume(sdk)

        # Get last session
        last_session = resume.get_last_session(agent=agent)
        if not last_session:
            return None, None

        # Build resume info
        resume_info = resume.build_resume_info(last_session)

        # Create new session if requested
        new_session = None
        if auto_create_session:
            from htmlgraph.ids import generate_id

            session_id = generate_id("sess")
            new_session = self.start_session(
                session_id=session_id,
                agent=agent or last_session.agent,
                title=f"Continuing from {last_session.id}",
            )

            # Link to previous session
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
        """
        End session with handoff information for next session.

        Args:
            session_id: Session to end
            summary: What was accomplished (handoff notes)
            next_focus: What should be done next
            blockers: List of blockers preventing progress
            keep_context: List of files to keep context for
            auto_recommend_context: Auto-recommend files from git history

        Returns:
            Updated session or None

        Example:
            >>> manager.end_session_with_handoff(
            ...     session_id="sess-123",
            ...     summary="Completed OAuth integration",
            ...     next_focus="Implement JWT token refresh",
            ...     blockers=["Waiting for security review"],
            ...     keep_context=["src/auth/oauth.py"]
            ... )
        """
        from htmlgraph.sessions.handoff import (
            ContextRecommender,
            HandoffBuilder,
        )

        # Get session
        session = self.get_session(session_id)
        if not session:
            return None

        # Build handoff using HandoffBuilder
        builder = HandoffBuilder(session)

        if summary:
            builder.add_summary(summary)

        if next_focus:
            builder.add_next_focus(next_focus)

        if blockers:
            builder.add_blockers(blockers)

        if keep_context:
            builder.add_context_files(keep_context)

        # Auto-recommend context files
        if auto_recommend_context:
            recommender = ContextRecommender()
            builder.auto_recommend_context(recommender, max_files=10)

        handoff_data = builder.build()

        # Update session with handoff data
        session.handoff_notes = handoff_data["handoff_notes"]
        session.recommended_next = handoff_data["recommended_next"]
        session.blockers = handoff_data["blockers"]
        session.recommended_context = handoff_data["recommended_context"]

        # Persist handoff data to database before ending session
        self.session_converter.save(session)

        # End the session
        self.end_session(session_id)

        # Track handoff effectiveness (optional - only if database available)
        # Note: SessionManager doesn't have direct database access,
        # handoff tracking is primarily done through SDK
        # Users should use SDK.end_session_with_handoff() for full tracking

        return session

    def release_session_features(self, session_id: str) -> list[str]:
        """
        Release all features claimed by a specific session.

        Args:
            session_id: Session ID

        Returns:
            List of released feature IDs
        """
        return self.claiming_service.release_session_features(session_id)

    def log_error(
        self,
        session_id: str,
        error: Exception,
        traceback_str: str,
        context: dict[str, Any] | None = None,
    ) -> None:
        """
        Log error with full traceback to session.

        Stores complete error details for later retrieval via debug command.
        Minimizes console output for better token efficiency.

        Args:
            session_id: Session ID to log error to
            error: The exception object
            traceback_str: Full traceback string
            context: Optional context dict (e.g. current file, line number)
        """
        session = self.get_session(session_id)
        if not session:
            return

        error_entry = ErrorEntry(
            timestamp=datetime.now(),
            error_type=error.__class__.__name__,
            message=str(error),
            traceback=traceback_str,
        )

        # Append error record to error_log
        session.error_log.append(error_entry)

        # Save updated session
        self.session_converter.save(session)

    def get_session_errors(self, session_id: str) -> list[dict[str, Any]]:
        """
        Retrieve all errors logged for a session.

        Args:
            session_id: Session ID

        Returns:
            List of error records, or empty list if none
        """
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
        """
        Search errors in a session by type and/or pattern.

        Args:
            session_id: Session ID to search
            error_type: Filter by exception type (e.g., "ValueError")
            pattern: Regex pattern to match in error message

        Returns:
            List of matching error records
        """
        session = self.get_session(session_id)
        if not session:
            return []

        errors = [error.model_dump() for error in session.error_log]

        # Filter by error type
        if error_type:
            errors = [e for e in errors if e.get("error_type") == error_type]

        # Filter by pattern in message
        if pattern:
            compiled_pattern = re.compile(pattern, re.IGNORECASE)
            errors = [
                e for e in errors if compiled_pattern.search(e.get("message", ""))
            ]

        return errors

    def get_error_summary(self, session_id: str) -> dict[str, Any]:
        """
        Get summary statistics of errors in a session.

        Args:
            session_id: Session ID

        Returns:
            Dictionary with error summary statistics
        """
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
    # Activity Tracking
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
        """
        Track an activity and attribute it to a feature.

        Args:
            session_id: Session to add activity to
            tool: Tool name (Edit, Bash, Read, etc.)
            summary: Human-readable summary
            file_paths: Files involved in this activity
            success: Whether the tool call succeeded
            feature_id: Explicit feature ID (skips attribution)
            parent_activity_id: ID of parent activity (e.g., Skill/Task invocation)
            payload: Optional rich payload data

        Returns:
            Created ActivityEntry with attribution
        """
        session = self.get_session(session_id)
        if not session:
            raise SessionNotFoundError(session_id)

        # Get active features for attribution
        active_features = self.get_active_features()

        # Attribute to feature if not explicitly set
        attributed_feature = feature_id
        drift_score = None
        attribution_reason = None

        # Skip drift calculation for child activities (part of Skill/Task invocation)
        # Child activities inherit their parent's context and shouldn't be scored independently
        if parent_activity_id:
            # Inherit feature from parent if not explicitly set
            if not attributed_feature and active_features:
                # Use primary feature or first active feature
                primary = next(
                    (f for f in active_features if f.properties.get("is_primary")), None
                )
                attributed_feature = (
                    (primary or active_features[0]).id if active_features else None
                )
            drift_score = None  # No drift for child activities
            attribution_reason = "child_activity"
        # Skip drift calculation for system overhead activities
        elif self._is_system_overhead(tool, summary, file_paths or []):
            # Attribute to primary or first active feature, but no drift score
            if not attributed_feature and active_features:
                primary = next(
                    (f for f in active_features if f.properties.get("is_primary")), None
                )
                attributed_feature = (
                    (primary or active_features[0]).id if active_features else None
                )
            drift_score = None  # No drift for system overhead
            attribution_reason = "system_overhead"
        elif not attributed_feature and active_features:
            attribution = self.attribute_activity(
                tool=tool,
                summary=summary,
                file_paths=file_paths or [],
                active_features=active_features,
                agent=session.agent,
            )
            attributed_feature = attribution["feature_id"]
            drift_score = attribution["drift_score"]
            attribution_reason = attribution["reason"]

        # Create activity entry with collision-resistant hash-based ID
        # This ensures multi-agent safety - no race conditions even with parallel agents
        event_id = generate_id(
            node_type="event",
            title=f"{tool}:{summary[:50]}",  # Include tool + summary for content-addressability
        )

        entry = ActivityEntry(
            id=event_id,
            timestamp=datetime.now(),
            tool=tool,
            summary=summary,
            success=success,
            feature_id=attributed_feature,
            drift_score=drift_score,
            parent_activity_id=parent_activity_id,
            payload={
                **(payload or {}),
                "file_paths": file_paths,
                "attribution_reason": attribution_reason,
                "session_id": session_id,  # Include session context in payload
            }
            if file_paths or attribution_reason or session_id
            else payload,
        )

        # Append to JSONL event log (source of truth for analytics)
        try:
            # Auto-infer work type from feature_id (Phase 1: Work Type Classification)
            from htmlgraph.work_type_utils import infer_work_type_from_id

            work_type = infer_work_type_from_id(entry.feature_id)

            self.event_log.append(
                EventRecord(
                    event_id=entry.id or "",
                    timestamp=entry.timestamp,
                    session_id=session_id,
                    agent=session.agent,
                    tool=entry.tool,
                    summary=entry.summary,
                    success=entry.success,
                    feature_id=entry.feature_id,
                    drift_score=entry.drift_score,
                    start_commit=session.start_commit,
                    continued_from=session.continued_from,
                    work_type=work_type,
                    session_status=session.status,
                    file_paths=file_paths,
                    parent_session_id=session.parent_session,
                    payload=entry.payload
                    if isinstance(entry.payload, dict)
                    else payload,
                )
            )
        except Exception as e:
            # Never break core tracking because of analytics logging.
            logger.warning(f"Failed to append to event log: {e}")

        # Optional: keep SQLite index up to date if it already exists.
        # This keeps the dashboard fast while keeping Git as the source of truth.
        try:
            index_path = self.graph_dir / "index.sqlite"
            if index_path.exists():
                from htmlgraph.analytics_index import AnalyticsIndex

                idx = AnalyticsIndex(index_path)
                idx.ensure_schema()
                idx.upsert_session(
                    {
                        "session_id": session_id,
                        "agent": session.agent,
                        "start_commit": session.start_commit,
                        "continued_from": session.continued_from,
                        "status": session.status,
                        "started_at": session.started_at.isoformat(),
                        "ended_at": session.ended_at.isoformat()
                        if session.ended_at
                        else None,
                    }
                )
                idx.upsert_event(
                    {
                        "event_id": entry.id,
                        "timestamp": entry.timestamp.isoformat(),
                        "session_id": session_id,
                        "tool": entry.tool,
                        "summary": entry.summary,
                        "success": entry.success,
                        "feature_id": entry.feature_id,
                        "drift_score": entry.drift_score,
                        "file_paths": file_paths or [],
                        "payload": entry.payload
                        if isinstance(entry.payload, dict)
                        else payload,
                    }
                )
        except Exception as e:
            logger.warning(f"Failed to update SQLite index: {e}")

        # Add to session
        session.add_activity(entry)

        # Add bidirectional link: feature -> session
        if attributed_feature:
            self._add_session_link_to_feature(attributed_feature, session_id)
            self._check_completion(attributed_feature, tool, success)

        # Save session
        self.session_converter.save(session)
        self._active_session = session

        return entry

    def track_user_query(
        self,
        session_id: str,
        prompt: str,
        feature_id: str | None = None,
    ) -> ActivityEntry:
        """
        Track a user query/prompt.

        Args:
            session_id: Session ID
            prompt: User's prompt text
            feature_id: Explicit feature attribution

        Returns:
            Created ActivityEntry
        """
        # Truncate long prompts for summary
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
    # Smart Attribution
    # =========================================================================

    def _get_active_auto_spike(self) -> Node | None:
        """
        Find an active auto-generated spike (session-init, conversation-init, or transition).

        Uses the in-memory spike index to avoid scanning the features list
        (spikes are stored separately from features/bugs).

        Returns:
            Active auto-spike Node or None
        """
        from htmlgraph.converter import NodeConverter

        spike_converter = NodeConverter(self.graph_dir / "spikes")

        for spike_id in list(self._active_auto_spikes):
            spike = spike_converter.load(spike_id)
            if not spike:
                self._active_auto_spikes.discard(spike_id)
                self._spike_index.remove(spike_id)
                continue

            if (
                spike.type == "spike"
                and spike.auto_generated
                and spike.spike_subtype
                in ("session-init", "conversation-init", "transition")
                and spike.status == "in-progress"
            ):
                return spike

            # Spike no longer qualifies - remove from index
            self._active_auto_spikes.discard(spike_id)
            self._spike_index.remove(spike_id)

        return None

    def attribute_activity(
        self,
        tool: str,
        summary: str,
        file_paths: list[str],
        active_features: list[Node],
        agent: str | None = None,
    ) -> dict[str, Any]:
        """
        Score and attribute an activity to the best matching feature or auto-spike.

        Attribution priority (highest to lowest):
        1. Real features - scored by the sophisticated matching system
        2. Auto-spikes - fallback only when no real feature scores >= 0.3
        3. No attribution - when nothing matches

        This ensures real features get proper attribution instead of being
        bypassed by auto-generated spikes.

        Args:
            tool: Tool name
            summary: Activity summary
            file_paths: Files involved
            active_features: Features to score against (features and bugs only)
            agent: Agent performing the activity

        Returns:
            Dict with feature_id, score, drift_score, reason
        """
        # Priority 1: Score REAL features first (these are already non-spike
        # since get_active_features() only returns features and bugs)
        if active_features:
            scores = []
            for feature in active_features:
                score, reasons = self._score_feature_match(
                    feature, tool, summary, file_paths, agent=agent
                )
                # Filter out explicitly rejected matches
                if score < 0:
                    continue
                scores.append((feature, score, reasons))

            if scores:
                # Sort by score descending
                scores.sort(key=lambda x: x[1], reverse=True)
                best_feature, best_score, best_reasons = scores[0]

                # Accept if score meets minimum threshold
                if best_score >= 0.3:
                    drift_score = 1.0 - min(best_score, 1.0)

                    # When a real feature wins, complete any active auto-spikes
                    # so they don't compete in future attributions this session
                    if agent:
                        self._complete_active_auto_spikes(
                            agent, to_feature_id=best_feature.id
                        )

                    return {
                        "feature_id": best_feature.id,
                        "score": best_score,
                        "drift_score": drift_score,
                        "reason": ", ".join(best_reasons)
                        if best_reasons
                        else "default_match",
                    }

        # Priority 2: Fall back to auto-spike ONLY if no real feature matched
        active_spike = self._get_active_auto_spike()
        if active_spike:
            return {
                "feature_id": active_spike.id,
                "score": 0.5,  # Lower score - this is a fallback, not a strong match
                "drift_score": 0.0,
                "reason": f"auto_spike_fallback_{active_spike.spike_subtype}",
            }

        # Priority 3: No attribution
        if not active_features:
            return {
                "feature_id": None,
                "score": 0,
                "drift_score": None,
                "reason": "no_active_features",
            }

        return {
            "feature_id": None,
            "score": 0,
            "drift_score": None,
            "reason": "no_matching_features",
        }

    def _score_feature_match(
        self,
        feature: Node,
        _tool: str,
        summary: str,
        file_paths: list[str],
        agent: str | None = None,
    ) -> tuple[float, list[str]]:
        """
        Score how well an activity matches a feature.

        Returns:
            (score, list of reasons)
        """
        score = 0.0
        reasons = []

        # 0. Check Agent Assignment (Critical)
        if feature.agent_assigned:
            if agent and feature.agent_assigned != agent:
                # Explicitly claimed by someone else -> REJECT
                return -1.0, ["claimed_by_other"]
            if agent and feature.agent_assigned == agent:
                # Claimed by me -> Big Bonus (overrides other heuristics)
                score += 2.0
                reasons.append("assigned_to_agent")

        # 1. File pattern matching (40%)
        file_patterns = feature.properties.get("file_patterns", [])
        if file_patterns and file_paths:
            pattern_score = self._score_file_patterns(file_paths, file_patterns)
            if pattern_score > 0:
                score += pattern_score * self.WEIGHT_FILE_PATTERN
                reasons.append("file_pattern")

        # 2. Keyword overlap (30%)
        keywords = self._extract_keywords(feature.title + " " + feature.content)
        activity_text = summary + " " + " ".join(file_paths)
        keyword_score = self._score_keyword_overlap(activity_text, keywords)
        if keyword_score > 0:
            score += keyword_score * self.WEIGHT_KEYWORD
            reasons.append("keyword")

        # 3. Type priority (20%)
        type_score = self.TYPE_PRIORITY.get(feature.type, 0.5)
        score += type_score * self.WEIGHT_TYPE_PRIORITY

        # 4. Primary feature bonus (10%)
        if feature.properties.get("is_primary"):
            score += self.WEIGHT_IS_PRIMARY
            reasons.append("primary")

        # 5. Status bonus (in-progress features get priority)
        if feature.status == "in-progress":
            score += 0.1
            reasons.append("in_progress")

        return score, reasons

    def _score_file_patterns(
        self,
        file_paths: list[str],
        patterns: list[str],
    ) -> float:
        """Score how well file paths match patterns."""
        if not file_paths or not patterns:
            return 0.0

        matches = 0
        for path in file_paths:
            for pattern in patterns:
                if fnmatch.fnmatch(path, pattern):
                    matches += 1
                    break

        return matches / len(file_paths)

    def _extract_keywords(self, text: str) -> set[str]:
        """Extract keywords from text."""
        # Simple keyword extraction - lowercase words > 3 chars
        words = re.findall(r"\b[a-zA-Z]{3,}\b", text.lower())
        # Filter common words
        stop_words = {
            "the",
            "and",
            "for",
            "with",
            "this",
            "that",
            "from",
            "are",
            "was",
            "were",
        }
        return set(words) - stop_words

    def _score_keyword_overlap(self, text: str, keywords: set[str]) -> float:
        """Score keyword overlap between text and keywords."""
        if not keywords:
            return 0.0

        text_words = self._extract_keywords(text)
        overlap = text_words & keywords

        return len(overlap) / len(keywords) if keywords else 0.0

    def _is_system_overhead(
        self, tool: str, summary: str, file_paths: list[str]
    ) -> bool:
        """
        Determine if an activity is system overhead that shouldn't count as drift.

        System overhead includes:
        - Skill invocations for system skills (htmlgraph-tracker, etc.)
        - Read/Write operations on .htmlgraph/ metadata files
        - Infrastructure files (config, docs, build artifacts, IDE files)
        """
        # System skills that are overhead, not feature work
        system_skills = {
            "htmlgraph-tracker",
            "htmlgraph:htmlgraph-tracker",
        }

        # Check if this is a Skill invocation for a system skill
        if tool == "Skill":
            # Extract skill name from summary (format: "Skill: {'skill': 'htmlgraph-tracker'}")
            for skill_name in system_skills:
                if skill_name in summary.lower():
                    return True

        # Infrastructure file patterns to exclude from drift scoring
        infrastructure_patterns = [
            # HtmlGraph metadata
            ".htmlgraph/",
            # Configuration files
            "pyproject.toml",
            "package.json",
            "package-lock.json",
            "setup.py",
            "setup.cfg",
            "requirements.txt",
            "requirements-dev.txt",
            ".gitignore",
            ".gitattributes",
            ".editorconfig",
            "pytest.ini",
            "tox.ini",
            ".coveragerc",
            # CI/CD configs
            ".github/",
            ".gitlab-ci.yml",
            ".travis.yml",
            "circle.yml",
            ".pre-commit-config.yaml",
            # Build and distribution
            "dist/",
            "build/",
            ".eggs/",
            "*.egg-info/",
            "__pycache__/",
            "*.pyc",
            "*.pyo",
            "*.pyd",
            # IDE and editor files
            ".vscode/",
            ".idea/",
            "*.swp",
            "*.swo",
            "*~",
            ".DS_Store",
            "Thumbs.db",
            # Testing artifacts
            ".pytest_cache/",
            ".coverage",
            "htmlcov/",
            ".tox/",
            # Environment and secrets
            ".env",
            ".env.local",
            ".env.*.local",
            # Documentation (consider docs/ as infrastructure)
            "README.md",
            "CONTRIBUTING.md",
            "LICENSE",
            "CHANGELOG.md",
            "docs/",
            # Other common infrastructure
            ".contextune/",
            ".parallel/",
            "node_modules/",
            ".venv/",
            "venv/",
        ]

        # Check if any file paths match infrastructure patterns
        if file_paths:
            for path in file_paths:
                # Normalize path
                path_normalized = path.replace("\\", "/")
                path_lower = path_normalized.lower()

                for pattern in infrastructure_patterns:
                    pattern_lower = pattern.lower()

                    # Directory patterns (end with /)
                    if pattern_lower.endswith("/"):
                        # For wildcard directory patterns like "*.egg-info/"
                        if "*" in pattern_lower:
                            import fnmatch

                            # Check each path segment
                            path_parts = path_lower.split("/")
                            for part in path_parts:
                                if fnmatch.fnmatch(part, pattern_lower.rstrip("/")):
                                    return True
                        # For regular directory patterns like ".htmlgraph/"
                        elif pattern_lower in path_lower or path_lower.startswith(
                            pattern_lower
                        ):
                            return True
                    # Wildcard file patterns (e.g., *.pyc)
                    elif "*" in pattern_lower:
                        import fnmatch

                        # Check the filename (last part of path)
                        filename = path_lower.split("/")[-1]
                        if fnmatch.fnmatch(filename, pattern_lower):
                            return True
                    # Exact filename match
                    else:
                        # Check if path ends with the pattern (handles both absolute and relative)
                        if (
                            path_lower.endswith(pattern_lower)
                            or f"/{pattern_lower}" in path_lower
                        ):
                            return True

        return False

    # =========================================================================
    # Drift Detection
    # =========================================================================

    def detect_drift(self, session_id: str, feature_id: str) -> dict[str, Any]:
        """
        Detect if current work is drifting from a feature.

        Returns:
            Dict with is_drifting, drift_score, reasons
        """
        session = self.get_session(session_id)
        if not session:
            return {"is_drifting": False, "drift_score": 0, "reasons": []}

        reasons = []
        drift_indicators = 0

        # Get recent activities for this feature
        feature_activities = [
            a for a in session.activity_log[-20:] if a.feature_id == feature_id
        ]

        if not feature_activities:
            return {
                "is_drifting": False,
                "drift_score": 0,
                "reasons": ["no_recent_activity"],
            }

        # 1. Check time since last meaningful progress
        last_activity = feature_activities[-1]
        time_since = datetime.now() - last_activity.timestamp
        if time_since > self.DRIFT_TIME_THRESHOLD:
            drift_indicators += 1
            reasons.append(f"stalled_{int(time_since.total_seconds() / 60)}min")

        # 2. Check for repeated tool patterns (loops)
        recent_tools = [a.tool for a in feature_activities[-10:]]
        if len(recent_tools) >= 6:
            # Check for repetitive patterns
            tool_counts: dict[str, int] = {}
            for t in recent_tools:
                tool_counts[t] = tool_counts.get(t, 0) + 1
            max_repeat = max(tool_counts.values())
            if max_repeat >= 5:
                drift_indicators += 1
                reasons.append("repetitive_pattern")

        # 3. Check average drift scores
        drift_scores = [
            a.drift_score for a in feature_activities if a.drift_score is not None
        ]
        if drift_scores:
            avg_drift = sum(drift_scores) / len(drift_scores)
            if avg_drift > 0.6:
                drift_indicators += 1
                reasons.append(f"high_avg_drift_{avg_drift:.2f}")

        # 4. Check for failed tool calls
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

    # =========================================================================
    # Feature Management
    # =========================================================================

    def _ensure_session_for_agent(self, agent: str) -> Session:
        """
        Ensure there is an active session for `agent`, creating one if needed.

        Note: This is intentionally lightweight and relies on start_session()'s
        dedupe logic to prevent session file explosions.
        """
        active = self.get_active_session_for_agent(agent)
        if active:
            return active
        return self.start_session(
            session_id=None,
            agent=agent,
            title=f"Auto session ({agent})",
        )

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
            # Never break feature ops because of tracking.
            logger.warning(f"Failed to log work item action ({tool}): {e}")
            return

    def get_active_features(self) -> list[Node]:
        """
        Get all features with status 'in-progress'.

        Uses a cache to avoid O(n) disk reads on every tool use.
        Cache is invalidated when features are started, completed, or released.
        """
        if self._features_cache_dirty or self._active_features_cache is None:
            self._active_features_cache = self._compute_active_features()
            self._features_cache_dirty = False
        return self._active_features_cache

    def _compute_active_features(self) -> list[Node]:
        """
        Compute active features by iterating all features from disk.

        This is the slow path - only called when cache is dirty.
        """
        features = []

        # From features collection
        for node in self.features_graph:
            if node.status == "in-progress":
                features.append(node)

        # From bugs collection
        for node in self.bugs_graph:
            if node.status == "in-progress":
                features.append(node)

        return features

    def create_feature(
        self,
        title: str,
        collection: str = "features",
        description: str = "",
        priority: str = "medium",
        steps: list[str] | None = None,
        agent: str | None = None,
    ) -> Node:
        """
        Create a new feature/bug/chore.

        Args:
            title: Title of the work item
            collection: Collection name (features, bugs)
            description: Optional description
            priority: Priority (low, medium, high, critical)
            steps: Optional list of implementation steps
            agent: Optional agent name for logging

        Returns:
            Created Node
        """
        # Derive node type from collection name (features -> feature)
        node_type = collection[:-1] if collection.endswith("s") else collection

        # Generate collision-resistant hash-based ID
        node_id = generate_id(node_type=node_type, title=title)

        # Default steps if none provided
        if steps is None:
            if collection == "features":
                steps = [
                    "Design approach",
                    "Implement core functionality",
                    "Add tests",
                    "Update documentation",
                ]
            else:
                steps = []

        node_data = {
            "id": node_id,
            "type": node_type,
            "title": title,
            "status": "todo",
            "priority": priority,
            "created": datetime.now().isoformat(),
            "updated": datetime.now().isoformat(),
            "content": description,
            "steps": [{"description": s, "completed": False} for s in steps],
            "properties": {},
            "edges": {},
        }

        node = dict_to_node(node_data)

        graph = self._get_graph(collection)
        graph.add(node)

        if agent:
            self._maybe_log_work_item_action(
                agent=agent,
                tool="FeatureCreate",
                summary=f"Created: {collection}/{node_id}",
                feature_id=node_id,
                payload={"collection": collection, "action": "create", "title": title},
            )

        return node

    def get_primary_feature(self) -> Node | None:
        """Get the primary active feature."""
        for feature in self.get_active_features():
            if feature.properties.get("is_primary"):
                return feature
        # Fall back to first in-progress feature
        active = self.get_active_features()
        return active[0] if active else None

    def start_feature(
        self,
        feature_id: str,
        collection: str = "features",
        *,
        agent: str | None = None,
        log_activity: bool = True,
    ) -> Node | None:
        """
        Mark a feature as in-progress and link to active session.

        Args:
            feature_id: Feature to start
            collection: Collection name (features, bugs)
            agent: Optional agent name for attribution/logging
            log_activity: If true, write an event record (requires agent)

        Returns:
            Updated Node or None
        """
        graph = self._get_graph(collection)
        node = graph.get(feature_id)
        if not node:
            return None

        # Claim enforcement: prevent starting if claimed by someone else
        if agent and node.agent_assigned and node.agent_assigned != agent:
            if node.claimed_by_session:
                session = self.get_session(node.claimed_by_session)
                if session and session.status == "active":
                    raise ValueError(
                        f"Feature '{feature_id}' is claimed by {node.agent_assigned} "
                        f"(session {node.claimed_by_session})"
                    )

        # Check WIP limit
        active = self.get_active_features()
        if len(active) >= self.wip_limit and node not in active:
            raise ValueError(
                f"WIP limit ({self.wip_limit}) reached. Complete existing work first."
            )

        # Auto-claim if starting and not already claimed
        if agent and not node.agent_assigned:
            self.claim_feature(feature_id, collection=collection, agent=agent)
            # Re-load node after claim
            node = graph.get(feature_id)
            if not node:
                raise ValueError(f"Feature {feature_id} not found after claiming")

        node.status = "in-progress"
        node.updated = datetime.now()
        graph.update(node)

        # Invalidate active features cache
        self._features_cache_dirty = True

        # Auto-complete any active auto-spikes (session-init or transition)
        # When a regular feature starts, transitional period is over
        if agent:
            self._complete_active_auto_spikes(agent, to_feature_id=feature_id)

        # Link feature to active session (bidirectional)
        active_session = (
            self.get_active_session_for_agent(agent)
            if agent
            else self.get_active_session()
        )
        if agent and not active_session:
            active_session = self._ensure_session_for_agent(agent)
        if active_session:
            self._add_session_link_to_feature(feature_id, active_session.id)

        if log_activity and agent:
            self._maybe_log_work_item_action(
                agent=agent,
                tool="FeatureStart",
                summary=f"Started: {collection}/{feature_id}",
                feature_id=feature_id,
                payload={"collection": collection, "action": "start"},
            )

        return node

    def complete_feature(
        self,
        feature_id: str,
        collection: str = "features",
        *,
        agent: str | None = None,
        log_activity: bool = True,
        transcript_id: str | None = None,
    ) -> Node | None:
        """
        Mark a feature as done.

        Args:
            feature_id: Feature to complete
            collection: Collection name
            agent: Optional agent name for attribution/logging
            log_activity: If true, write an event record (requires agent)
            transcript_id: Optional transcript ID (agent session) that implemented this feature.
                          Used to link parallel agent transcripts to features.

        Returns:
            Updated Node or None
        """
        graph = self._get_graph(collection)
        node = graph.get(feature_id)
        if not node:
            # Node might have been created by SDK's collection (different graph instance)
            # Try reloading from disk
            node = graph.reload_node(feature_id)
            if not node:
                return None

        node.status = "done"
        node.updated = datetime.now()
        node.properties["completed_at"] = datetime.now().isoformat()

        # Link transcript if provided (for parallel agent tracking)
        if transcript_id:
            self._link_transcript_to_feature(node, transcript_id, graph)

        graph.update(node)

        # Invalidate active features cache
        self._features_cache_dirty = True

        if log_activity and agent:
            # Include transcript_id in payload for traceability
            payload = {"collection": collection, "action": "complete"}
            if transcript_id:
                payload["transcript_id"] = transcript_id

            self._maybe_log_work_item_action(
                agent=agent,
                tool="FeatureComplete",
                summary=f"Completed: {collection}/{feature_id}",
                feature_id=feature_id,
                payload=payload,
            )

        # Auto-import transcript on work item completion
        session = self.get_active_session(agent=agent)
        if session and session.transcript_id:
            try:
                from htmlgraph.transcript import TranscriptReader

                reader = TranscriptReader()
                transcript = reader.read_session(session.transcript_id)
                if transcript:
                    self.import_transcript_events(
                        session_id=session.id,
                        transcript_session=transcript,
                        overwrite=True,  # Replace hook data with high-fidelity transcript
                    )
            except Exception as e:
                logger.warning(
                    f"Failed to auto-import transcript on feature completion: {e}"
                )

        # Auto-create transition spike for post-completion activities
        # This captures work between features. Completed when next feature starts,
        # or when a new conversation starts (completing previous conversation's spike).
        if session:
            self._create_transition_spike(session, from_feature_id=feature_id)

        # Analyze session for anti-patterns and errors on completion
        # This surfaces feedback to the orchestrator about mistakes made
        if session:
            try:
                from htmlgraph.learning import LearningPersistence
                from htmlgraph.sdk import SDK

                # Create SDK instance for analysis (shares same graph directory)
                sdk = SDK(agent=agent or "unknown", directory=self.graph_dir)
                learning = LearningPersistence(sdk)
                analysis = learning.analyze_for_orchestrator(session.id)
                node.properties["completion_analysis"] = analysis

                # PERSIST learning insights to graph (not just ephemeral properties)
                # This creates queryable SessionInsight and Pattern nodes
                insight_id = learning.persist_session_insight(session.id)
                if insight_id:
                    node.properties["insight_id"] = insight_id
                    logger.debug(f"Persisted learning insight: {insight_id}")

                # Persist patterns detected across sessions
                pattern_ids = learning.persist_patterns()
                if pattern_ids:
                    logger.debug(f"Persisted {len(pattern_ids)} patterns")

                # Log analysis summary if issues detected
                if analysis.get("summary", "").startswith("⚠️"):
                    logger.info(
                        f"Work item {feature_id} completed with issues: {analysis['summary']}"
                    )

                # Update node in graph with analysis
                graph.update(node)
            except Exception as e:
                logger.warning(f"Failed to analyze session on completion: {e}")

        return node

    def set_primary_feature(
        self,
        feature_id: str,
        collection: str = "features",
        *,
        agent: str | None = None,
        log_activity: bool = True,
    ) -> Node | None:
        """Set a feature as the primary focus."""
        # Clear existing primary
        for feature in self.get_active_features():
            if feature.properties.get("is_primary"):
                feature.properties["is_primary"] = False
                self._get_graph_for_node(feature).update(feature)

        # Set new primary
        graph = self._get_graph(collection)
        node = graph.get(feature_id)
        if node:
            node.properties["is_primary"] = True
            graph.update(node)

        if log_activity and agent:
            self._maybe_log_work_item_action(
                agent=agent,
                tool="FeaturePrimary",
                summary=f"Primary: {collection}/{feature_id}",
                feature_id=feature_id,
                payload={"collection": collection, "action": "primary"},
            )

        return node

    def activate_feature(
        self,
        feature_id: str,
        collection: str = "features",
        *,
        agent: str | None = None,
        log_activity: bool = True,
    ) -> Node | None:
        """
        Convenience: ensure feature is in-progress and set as primary in one action.

        This is useful for tool integrations (e.g. MCP) that want a single
        high-signal event instead of multiple low-signal events.
        """
        node = self.start_feature(
            feature_id,
            collection=collection,
            agent=agent,
            log_activity=False,
        )
        if node is None:
            return None
        self.set_primary_feature(
            feature_id,
            collection=collection,
            agent=agent,
            log_activity=False,
        )
        if log_activity and agent:
            self._maybe_log_work_item_action(
                agent=agent,
                tool="FeatureActivate",
                summary=f"Activated: {collection}/{feature_id}",
                feature_id=feature_id,
                payload={"collection": collection, "action": "activate"},
            )
        return node

    # =========================================================================
    # Auto-Completion
    # =========================================================================

    def _check_completion(self, feature_id: str, tool: str, success: bool) -> bool:
        """
        Check if a feature should be auto-completed.

        Returns:
            True if feature was auto-completed
        """
        # Find the feature
        node = self.features_graph.get(feature_id) or self.bugs_graph.get(feature_id)
        if not node:
            return False

        criteria = node.properties.get("completion_criteria", {})
        criteria_type = criteria.get("type", "manual")

        if criteria_type == "manual":
            return False

        if criteria_type == "work_count":
            # Complete after N work tools
            threshold = criteria.get("count", 10)
            work_count = node.properties.get("work_count", 0) + 1
            node.properties["work_count"] = work_count

            if work_count >= threshold:
                self.complete_feature(feature_id)
                return True

        if criteria_type == "test" and tool == "Bash" and success:
            # Check if this was a test command
            # This is simplified - real implementation would check command content
            pass

        if criteria_type == "steps":
            # Complete when all steps are done
            if node.steps and all(s.completed for s in node.steps):
                self.complete_feature(feature_id)
                return True

        return False

    # =========================================================================
    # Status & Reporting
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
    # Claiming Mechanism
    # =========================================================================

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
        """
        Release a feature claim.

        Args:
            feature_id: Feature to release
            collection: Collection name
            agent: Agent name releasing the feature

        Returns:
            Updated Node or None
        """
        return self.claiming_service.release_feature(
            feature_id=feature_id,
            collection=collection,
            agent=agent,
        )

    def auto_release_features(self, agent: str) -> list[str]:
        """
        Release all features claimed by an agent.

        Args:
            agent: Agent name

        Returns:
            List of released feature IDs
        """
        return self.claiming_service.auto_release_features(agent)

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
        """
        Create a handoff context for a feature to transition between agents.

        Sets up handoff metadata and releases the feature for the next agent to claim.

        Args:
            feature_id: Feature to hand off
            reason: Reason for handoff (e.g., "blocked", "requires expertise", "completed")
            notes: Detailed handoff context/decisions
            collection: Collection name
            agent: Current agent releasing the feature
            next_agent: Next agent to claim (optional, for audit trail)

        Returns:
            Updated Node with handoff metadata or None if not found

        Raises:
            ValueError: If agent doesn't own the feature
        """
        graph = self._get_graph(collection)
        node = graph.get(feature_id)
        if not node:
            return None

        # Verify agent owns the feature
        if node.agent_assigned and node.agent_assigned != agent:
            raise ValueError(
                f"Feature '{feature_id}' is claimed by {node.agent_assigned}, not {agent}"
            )

        # Set handoff fields
        node.handoff_required = True
        node.previous_agent = agent
        node.handoff_reason = reason
        node.handoff_notes = notes
        node.handoff_timestamp = datetime.now()
        node.updated = datetime.now()

        # Release the feature for next agent to claim
        node.agent_assigned = None
        node.claimed_at = None
        node.claimed_by_session = None

        # Update the graph
        graph.update(node)

        # Log the handoff action
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
    # Helpers
    # =========================================================================

    def _add_session_link_to_feature(self, feature_id: str, session_id: str) -> None:
        """
        Add a bidirectional link between feature and session.

        This creates:
        1. "implemented-in" edge on the feature pointing to the session
        2. "worked-on" edge on the session pointing to the feature

        Only adds if the links don't already exist.
        """
        from htmlgraph.models import Edge

        # Find the feature in either collection
        feature_node = self.features_graph.get(feature_id) or self.bugs_graph.get(
            feature_id
        )
        if not feature_node:
            return

        # Check if feature → session edge already exists
        existing_sessions = feature_node.edges.get("implemented-in", [])
        feature_already_linked = any(
            edge.target_id == session_id for edge in existing_sessions
        )

        if not feature_already_linked:
            # Add feature → session edge
            edge = Edge(
                target_id=session_id,
                relationship="implemented-in",
                title=session_id,
                since=datetime.now(),
            )
            feature_node.add_edge(edge)

            # Save the updated feature
            graph = self._get_graph_for_node(feature_node)
            graph.update(feature_node)

        # Now add session → feature link (reverse link)
        session = self.get_session(session_id)
        if not session:
            return

        # Check if session → feature link already exists
        if feature_id not in session.worked_on:
            # Add feature to session's worked_on list
            session.worked_on.append(feature_id)

            # Save the updated session
            self.session_converter.save(session)

    def _link_transcript_to_feature(
        self,
        node: Node,
        transcript_id: str,
        graph: HtmlGraph,
    ) -> None:
        """
        Link a Claude Code transcript to a feature.

        Adds an "implemented-by" edge to the feature pointing to the transcript.
        Also aggregates tool analytics from the transcript into feature properties.

        Args:
            node: Feature node to link
            transcript_id: Claude Code transcript/agent session ID
            graph: Graph containing the node
        """
        from htmlgraph.models import Edge

        # Check if edge already exists
        existing_transcripts = node.edges.get("implemented-by", [])
        already_linked = any(
            edge.target_id == transcript_id for edge in existing_transcripts
        )

        if already_linked:
            return

        # Try to get transcript analytics
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

        # Add implemented-by edge with analytics
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

        # Also store aggregated transcript analytics in properties
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
        try:
            import subprocess

            result = subprocess.run(
                ["git", "rev-parse", "--short", "HEAD"],
                capture_output=True,
                text=True,
                cwd=self.graph_dir.parent,
            )
            if result.returncode == 0:
                return result.stdout.strip()
        except Exception as e:
            logger.warning(f"Failed to get current git commit: {e}")
        return None

    # =========================================================================
    # Claude Code Transcript Integration
    # =========================================================================

    def link_transcript(
        self,
        session_id: str,
        transcript_id: str,
        transcript_path: str | None = None,
        git_branch: str | None = None,
    ) -> Session | None:
        """
        Link a Claude Code transcript to an HtmlGraph session.

        Args:
            session_id: HtmlGraph session ID
            transcript_id: Claude Code session UUID (from JSONL filename)
            transcript_path: Path to the JSONL file
            git_branch: Git branch from transcript metadata

        Returns:
            Updated Session or None if not found
        """
        session = self.get_session(session_id)
        if not session:
            return None

        session.transcript_id = transcript_id
        session.transcript_path = transcript_path
        session.transcript_synced_at = datetime.now()
        if git_branch:
            session.transcript_git_branch = git_branch

        self.session_converter.save(session)
        return session

    def find_session_by_transcript(
        self,
        transcript_id: str,
    ) -> Session | None:
        """
        Find an HtmlGraph session linked to a transcript.

        Args:
            transcript_id: Claude Code session UUID

        Returns:
            Session or None if not found
        """
        for session in self.session_converter.load_all():
            if session.transcript_id == transcript_id:
                return session
        return None

    def import_transcript_events(
        self,
        session_id: str,
        transcript_session: Any,  # TranscriptSession from transcript module
        overwrite: bool = False,
    ) -> dict[str, int | str]:
        """
        Import events from a Claude Code transcript into an HtmlGraph session.

        This extracts tool uses and user messages from the transcript
        and adds them to the session's activity log.

        Args:
            session_id: HtmlGraph session ID to import into
            transcript_session: TranscriptSession object from transcript module
            overwrite: If True, clear existing activities before import

        Returns:
            Dict with import statistics
        """
        session = self.get_session(session_id)
        if not session:
            return {"error": "session_not_found", "imported": 0}

        if overwrite:
            session.activity_log = []
            session.event_count = 0

        imported = 0
        skipped = 0

        for entry in transcript_session.entries:
            # Skip non-actionable entries
            if entry.entry_type not in ("user", "tool_use"):
                skipped += 1
                continue

            # Create ActivityEntry from transcript entry
            if entry.entry_type == "user":
                activity = ActivityEntry(
                    id=f"tx-{entry.uuid[:8]}",
                    timestamp=entry.timestamp,
                    tool="UserQuery",
                    summary=entry.to_summary(),
                    success=True,
                    payload={
                        "source": "transcript",
                        "transcript_uuid": entry.uuid,
                        "message_content": entry.message_content,
                    },
                )
            elif entry.entry_type == "tool_use":
                activity = ActivityEntry(
                    id=f"tx-{entry.uuid[:8]}",
                    timestamp=entry.timestamp,
                    tool=entry.tool_name or "Unknown",
                    summary=entry.to_summary(),
                    success=True,  # Assume success unless we have result
                    payload={
                        "source": "transcript",
                        "transcript_uuid": entry.uuid,
                        "tool_input": entry.tool_input,
                        "thinking": entry.thinking,
                    },
                )
            else:
                continue

            session.add_activity(activity)
            imported += 1

            # Also append to JSONL event log
            try:
                from htmlgraph.work_type_utils import infer_work_type_from_id

                work_type = infer_work_type_from_id(activity.feature_id)

                self.event_log.append(
                    EventRecord(
                        event_id=activity.id or "",
                        timestamp=activity.timestamp,
                        session_id=session_id,
                        agent=session.agent,
                        tool=activity.tool,
                        summary=activity.summary,
                        success=activity.success,
                        feature_id=activity.feature_id,
                        drift_score=None,
                        start_commit=session.start_commit,
                        continued_from=session.continued_from,
                        work_type=work_type,
                        session_status=session.status,
                        payload=activity.payload
                        if isinstance(activity.payload, dict)
                        else None,
                    )
                )
            except Exception as e:
                logger.warning(f"Failed to append transcript event to event log: {e}")

        # Update transcript link
        session.transcript_id = transcript_session.session_id
        session.transcript_path = str(transcript_session.path)
        session.transcript_synced_at = datetime.now()
        if transcript_session.git_branch:
            session.transcript_git_branch = transcript_session.git_branch

        self.session_converter.save(session)

        return {
            "imported": imported,
            "skipped": skipped,
            "total_entries": len(transcript_session.entries),
        }

    def auto_link_transcript_by_branch(
        self,
        git_branch: str,
        agent: str | None = None,
    ) -> list[tuple[str, str]]:
        """
        Auto-link HtmlGraph sessions to transcripts based on git branch.

        This finds sessions and transcripts that share the same git branch
        and links them together.

        Args:
            git_branch: Git branch to match
            agent: Optional agent filter

        Returns:
            List of (session_id, transcript_id) tuples that were linked
        """
        from htmlgraph.transcript import TranscriptReader

        linked: list[tuple[str, str]] = []
        reader = TranscriptReader()

        # Find transcripts for this branch
        project_path = self.graph_dir.parent
        transcripts = reader.find_sessions_for_branch(git_branch, project_path)

        if not transcripts:
            return linked

        # Find sessions that might match
        sessions = self.session_converter.load_all()
        if agent:
            sessions = [s for s in sessions if s.agent == agent]

        # Helper to normalize datetimes for comparison
        # (handles timezone-aware vs timezone-naive)
        def normalize_dt(dt: datetime | None) -> datetime | None:
            if dt is None:
                return None
            # If timezone-aware, convert to naive UTC
            if dt.tzinfo is not None:
                return dt.astimezone(timezone.utc).replace(tzinfo=None)
            return dt

        # Match by time overlap and git branch
        for transcript in transcripts:
            if not transcript.started_at:
                continue

            transcript_start = normalize_dt(transcript.started_at)
            transcript_end = normalize_dt(transcript.ended_at)

            for session in sessions:
                # Skip if already linked
                if session.transcript_id:
                    continue

                session_start = normalize_dt(session.started_at)
                session_end = normalize_dt(session.ended_at)

                # Check if session overlaps with transcript time
                if session_start and transcript_end:
                    if session_start > transcript_end:
                        continue  # Session started after transcript ended

                if session_end and transcript_start:
                    if session_end < transcript_start:
                        continue  # Session ended before transcript started

                # Link them
                self.link_transcript(
                    session_id=session.id,
                    transcript_id=transcript.session_id,
                    transcript_path=str(transcript.path),
                    git_branch=git_branch,
                )
                linked.append((session.id, transcript.session_id))
                break  # One transcript per session

        return linked

    def get_transcript_stats(self, session_id: str) -> dict[str, Any] | None:
        """
        Get transcript statistics for a session.

        Args:
            session_id: HtmlGraph session ID

        Returns:
            Dict with transcript stats or None if no transcript linked
        """
        session = self.get_session(session_id)
        if not session or not session.transcript_id:
            return None

        from htmlgraph.transcript import TranscriptReader

        reader = TranscriptReader()
        transcript = reader.read_session(session.transcript_id)

        if not transcript:
            return {
                "transcript_id": session.transcript_id,
                "error": "transcript_not_found",
            }

        return {
            "transcript_id": session.transcript_id,
            "transcript_path": session.transcript_path,
            "synced_at": session.transcript_synced_at.isoformat()
            if session.transcript_synced_at
            else None,
            "git_branch": session.transcript_git_branch,
            "user_messages": transcript.user_message_count,
            "tool_calls": transcript.tool_call_count,
            "tool_breakdown": transcript.tool_breakdown,
            "duration_seconds": transcript.duration_seconds,
            "has_thinking_traces": transcript.has_thinking_traces(),
            "entry_count": len(transcript.entries),
        }

    # =========================================================================
    # Session Context Builder - Delegates to SessionContextBuilder
    # =========================================================================

    def get_version_status(self) -> dict[str, Any]:
        """
        Check installed htmlgraph version against latest on PyPI.

        Returns:
            Dict with installed_version, latest_version, is_outdated
        """
        from htmlgraph.session_context import VersionChecker

        return VersionChecker.get_version_status()

    def initialize_git_hooks(self, project_dir: str | Path) -> bool:
        """
        Install pre-commit hooks if not already installed.

        Args:
            project_dir: Path to the project root

        Returns:
            True if hooks were installed or already exist
        """
        from htmlgraph.session_context import GitHooksInstaller

        return GitHooksInstaller.install(project_dir)

    def get_start_context(
        self,
        session_id: str,
        project_dir: str | Path | None = None,
        compute_async: bool = True,
    ) -> str:
        """
        Build complete session start context for AI agents.

        This is the primary method for generating the full context string
        that gets injected via additionalContext in the SessionStart hook.

        Args:
            session_id: Current session ID
            project_dir: Project root directory (uses graph_dir parent if not provided)
            compute_async: Use parallel async operations for performance

        Returns:
            Complete formatted Markdown context string
        """
        from htmlgraph.session_context import SessionContextBuilder

        if project_dir is None:
            project_dir = self.graph_dir.parent

        builder = SessionContextBuilder(self.graph_dir, project_dir)
        return builder.build(session_id=session_id, compute_async=compute_async)

    def detect_feature_conflicts(self) -> list[dict[str, Any]]:
        """
        Detect features being worked on by multiple agents simultaneously.

        Returns:
            List of conflict dicts with feature_id, title, agents
        """
        from htmlgraph.session_context import SessionContextBuilder

        project_dir = self.graph_dir.parent
        builder = SessionContextBuilder(self.graph_dir, project_dir)
        return builder.detect_feature_conflicts()
