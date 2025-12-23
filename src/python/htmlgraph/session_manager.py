"""
SessionManager - Smart session and activity tracking for AI agents.

Provides:
- Session lifecycle management (start, track, end)
- Smart attribution scoring (match activities to features)
- Drift detection (detect when work diverges from feature)
- Auto-completion checking
- WIP limits enforcement
"""

import os
import re
import fnmatch
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any, Literal

from htmlgraph.models import Node, Session, ActivityEntry
from htmlgraph.graph import HtmlGraph
from htmlgraph.converter import session_to_html, html_to_session, SessionConverter, dict_to_node
from htmlgraph.event_log import JsonlEventLog, EventRecord
from htmlgraph.ids import generate_id


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
    ):
        """
        Initialize SessionManager.

        Args:
            graph_dir: Directory containing HtmlGraph data
            wip_limit: Maximum features in progress simultaneously
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

        # Feature graphs
        self.features_graph = HtmlGraph(self.features_dir, auto_load=True)
        self.bugs_graph = HtmlGraph(self.bugs_dir, auto_load=True)

        # Cache for active session
        self._active_session: Session | None = None

        # Append-only event log (Git-friendly source of truth for activities)
        self.events_dir = self.graph_dir / "events"
        self.event_log = JsonlEventLog(self.events_dir)

    # =========================================================================
    # Session Lifecycle
    # =========================================================================

    def _list_active_sessions(self) -> list[Session]:
        """Return all active sessions found on disk."""
        return [s for s in self.session_converter.load_all() if s.status == "active"]

    def _choose_canonical_active_session(self, sessions: list[Session]) -> Session | None:
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
        now = datetime.now()
        session.status = "stale"
        session.ended_at = now
        session.last_activity = now
        self.session_converter.save(session)

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
        agent: str = "claude-code",
        is_subagent: bool = False,
        continued_from: str | None = None,
        start_commit: str | None = None,
        title: str | None = None,
    ) -> Session:
        """
        Start a new session.

        Args:
            session_id: Unique session identifier (auto-generated if None)
            agent: Agent name (e.g., "claude-code", "haiku")
            is_subagent: True if this is a Task subagent
            continued_from: Previous session ID if continuing
            start_commit: Git commit hash at session start
            title: Optional human-readable title

        Returns:
            New Session instance
        """
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
                s for s in self._list_active_sessions()
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
        )

        # Add session start event
        session.add_activity(ActivityEntry(
            tool="SessionStart",
            summary="Session started",
            timestamp=now,
        ))

        # Save to disk
        self.session_converter.save(session)
        self._active_session = session

        return session

    def get_session(self, session_id: str) -> Session | None:
        """Get a session by ID."""
        if self._active_session and self._active_session.id == session_id:
            return self._active_session
        return self.session_converter.load(session_id)

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

    def end_session(self, session_id: str) -> Session | None:
        """
        End a session.

        Args:
            session_id: Session to end

        Returns:
            Updated Session or None if not found
        """
        session = self.get_session(session_id)
        if not session:
            return None

        session.end()
        session.add_activity(ActivityEntry(
            tool="SessionEnd",
            summary="Session ended",
            timestamp=datetime.now(),
        ))

        # Release all features claimed by this session
        self.release_session_features(session_id)

        self.session_converter.save(session)

        if self._active_session and self._active_session.id == session_id:
            self._active_session = None

        return session

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
            raise ValueError(f"Session not found: {session_id}")

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
                primary = next((f for f in active_features if f.properties.get("is_primary")), None)
                attributed_feature = (primary or active_features[0]).id if active_features else None
            drift_score = None  # No drift for child activities
            attribution_reason = "child_activity"
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
            title=f"{tool}:{summary[:50]}"  # Include tool + summary for content-addressability
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
            } if file_paths or attribution_reason or session_id else payload,
        )

        # Append to JSONL event log (source of truth for analytics)
        try:
            # Auto-infer work type from feature_id (Phase 1: Work Type Classification)
            from htmlgraph.work_type_utils import infer_work_type_from_id
            work_type = infer_work_type_from_id(entry.feature_id)

            self.event_log.append(EventRecord(
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
                payload=entry.payload if isinstance(entry.payload, dict) else payload,
            ))
        except Exception:
            # Never break core tracking because of analytics logging.
            pass

        # Optional: keep SQLite index up to date if it already exists.
        # This keeps the dashboard fast while keeping Git as the source of truth.
        try:
            index_path = self.graph_dir / "index.sqlite"
            if index_path.exists():
                from htmlgraph.analytics_index import AnalyticsIndex

                idx = AnalyticsIndex(index_path)
                idx.ensure_schema()
                idx.upsert_session({
                    "session_id": session_id,
                    "agent": session.agent,
                    "start_commit": session.start_commit,
                    "continued_from": session.continued_from,
                    "status": session.status,
                    "started_at": session.started_at.isoformat(),
                    "ended_at": session.ended_at.isoformat() if session.ended_at else None,
                })
                idx.upsert_event({
                    "event_id": entry.id,
                    "timestamp": entry.timestamp.isoformat(),
                    "session_id": session_id,
                    "tool": entry.tool,
                    "summary": entry.summary,
                    "success": entry.success,
                    "feature_id": entry.feature_id,
                    "drift_score": entry.drift_score,
                    "file_paths": file_paths or [],
                    "payload": entry.payload if isinstance(entry.payload, dict) else payload,
                })
        except Exception:
            pass

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

    def attribute_activity(
        self,
        tool: str,
        summary: str,
        file_paths: list[str],
        active_features: list[Node],
        agent: str | None = None,
    ) -> dict[str, Any]:
        """
        Score and attribute an activity to the best matching feature.

        Args:
            tool: Tool name
            summary: Activity summary
            file_paths: Files involved
            active_features: Features to score against
            agent: Agent performing the activity

        Returns:
            Dict with feature_id, score, drift_score, reason
        """
        if not active_features:
            return {
                "feature_id": None,
                "score": 0,
                "drift_score": None,
                "reason": "no_active_features",
            }

        scores = []
        for feature in active_features:
            score, reasons = self._score_feature_match(
                feature, tool, summary, file_paths, agent=agent
            )
            # Filter out explicitly rejected matches
            if score < 0:
                continue
            scores.append((feature, score, reasons))

        if not scores:
            return {
                "feature_id": None,
                "score": 0,
                "drift_score": None,
                "reason": "no_matching_features_authorized",
            }

        # Sort by score descending
        scores.sort(key=lambda x: x[1], reverse=True)
        best_feature, best_score, best_reasons = scores[0]

        # Calculate drift (how well does this align with the feature?)
        drift_score = 1.0 - min(best_score, 1.0)

        return {
            "feature_id": best_feature.id,
            "score": best_score,
            "drift_score": drift_score,
            "reason": ", ".join(best_reasons) if best_reasons else "default_match",
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
        words = re.findall(r'\b[a-zA-Z]{3,}\b', text.lower())
        # Filter common words
        stop_words = {'the', 'and', 'for', 'with', 'this', 'that', 'from', 'are', 'was', 'were'}
        return set(words) - stop_words

    def _score_keyword_overlap(self, text: str, keywords: set[str]) -> float:
        """Score keyword overlap between text and keywords."""
        if not keywords:
            return 0.0

        text_words = self._extract_keywords(text)
        overlap = text_words & keywords

        return len(overlap) / len(keywords) if keywords else 0.0

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
            a for a in session.activity_log[-20:]
            if a.feature_id == feature_id
        ]

        if not feature_activities:
            return {"is_drifting": False, "drift_score": 0, "reasons": ["no_recent_activity"]}

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
            tool_counts = {}
            for t in recent_tools:
                tool_counts[t] = tool_counts.get(t, 0) + 1
            max_repeat = max(tool_counts.values())
            if max_repeat >= 5:
                drift_indicators += 1
                reasons.append("repetitive_pattern")

        # 3. Check average drift scores
        drift_scores = [a.drift_score for a in feature_activities if a.drift_score is not None]
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
        except Exception:
            # Never break feature ops because of tracking.
            return

    def get_active_features(self) -> list[Node]:
        """Get all features with status 'in-progress'."""
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
            raise ValueError(f"WIP limit ({self.wip_limit}) reached. Complete existing work first.")

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

        # Link feature to active session (bidirectional)
        active_session = self.get_active_session_for_agent(agent) if agent else self.get_active_session()
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
    ) -> Node | None:
        """
        Mark a feature as done.

        Args:
            feature_id: Feature to complete
            collection: Collection name
            agent: Optional agent name for attribution/logging
            log_activity: If true, write an event record (requires agent)

        Returns:
            Updated Node or None
        """
        graph = self._get_graph(collection)
        node = graph.get(feature_id)
        if not node:
            return None

        node.status = "done"
        node.updated = datetime.now()
        node.properties["completed_at"] = datetime.now().isoformat()
        graph.update(node)

        if log_activity and agent:
            self._maybe_log_work_item_action(
                agent=agent,
                tool="FeatureComplete",
                summary=f"Completed: {collection}/{feature_id}",
                feature_id=feature_id,
                payload={"collection": collection, "action": "complete"},
            )

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
        graph = self._get_graph(collection)
        node = graph.get(feature_id)
        if not node:
            return None

        # Check if already claimed by someone else
        if node.agent_assigned and node.agent_assigned != agent:
            # Check if session that claimed it is still active
            if node.claimed_by_session:
                session = self.get_session(node.claimed_by_session)
                if session and session.status == "active":
                    raise ValueError(
                        f"Feature '{feature_id}' is already claimed by {node.agent_assigned} "
                        f"(session {node.claimed_by_session})"
                    )

        session = self._ensure_session_for_agent(agent)

        node.agent_assigned = agent
        node.claimed_at = datetime.now()
        node.claimed_by_session = session.id
        node.updated = datetime.now()
        graph.update(node)

        self._maybe_log_work_item_action(
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
            raise ValueError(f"Feature '{feature_id}' is claimed by {node.agent_assigned}, not {agent}")

        node.agent_assigned = None
        node.claimed_at = None
        node.claimed_by_session = None
        node.updated = datetime.now()
        graph.update(node)

        self._maybe_log_work_item_action(
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
            raise ValueError(f"Feature '{feature_id}' is claimed by {node.agent_assigned}, not {agent}")

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
        feature_node = self.features_graph.get(feature_id) or self.bugs_graph.get(feature_id)
        if not feature_node:
            return

        # Check if feature → session edge already exists
        existing_sessions = feature_node.edges.get("implemented-in", [])
        feature_already_linked = any(edge.target_id == session_id for edge in existing_sessions)

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
        except Exception:
            pass
        return None
