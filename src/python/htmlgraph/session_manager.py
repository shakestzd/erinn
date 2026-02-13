"""SessionManager - facade for session lifecycle, attribution, and drift detection."""

import fnmatch
import logging
import re
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any

from htmlgraph.agent_detection import detect_agent_name
from htmlgraph.converter import SessionConverter
from htmlgraph.event_log import EventRecord, JsonlEventLog
from htmlgraph.exceptions import SessionNotFoundError
from htmlgraph.graph import HtmlGraph
from htmlgraph.ids import generate_id
from htmlgraph.models import ActivityEntry, Node, Session

logger = logging.getLogger(__name__)
from htmlgraph.services import ClaimingService
from htmlgraph.sessions.drift import DriftDetector
from htmlgraph.sessions.errors import ErrorManager
from htmlgraph.sessions.feature_workflow import FeatureWorkflow
from htmlgraph.sessions.linking import LinkingOps
from htmlgraph.sessions.spikes import SpikeManager
from htmlgraph.sessions.transcript_ops import TranscriptOps
from htmlgraph.spike_index import ActiveAutoSpikeIndex


class SessionManager:
    """Manages agent sessions with smart attribution and drift detection."""

    # Attribution scoring weights
    WEIGHT_FILE_PATTERN = 0.4
    WEIGHT_KEYWORD = 0.3
    WEIGHT_TYPE_PRIORITY = 0.2
    WEIGHT_IS_PRIMARY = 0.1

    TYPE_PRIORITY = {
        "bug": 1.0, "hotfix": 1.0, "feature": 0.8,
        "spike": 0.6, "chore": 0.4, "epic": 0.2,
    }

    DEFAULT_WIP_LIMIT = 3
    DEFAULT_SESSION_DEDUPE_WINDOW_SECONDS = 120
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
        self.graph_dir = Path(graph_dir)
        self.wip_limit = wip_limit
        self.session_dedupe_window_seconds = session_dedupe_window_seconds

        self.sessions_dir = self.graph_dir / "sessions"
        self.features_dir = self.graph_dir / "features"
        self.bugs_dir = self.graph_dir / "bugs"
        self.sessions_dir.mkdir(parents=True, exist_ok=True)
        self.features_dir.mkdir(parents=True, exist_ok=True)
        self.bugs_dir.mkdir(parents=True, exist_ok=True)

        self.session_converter = SessionConverter(self.sessions_dir)

        self.features_graph = (
            features_graph if features_graph is not None
            else HtmlGraph(self.features_dir, auto_load=False)
        )
        self.bugs_graph = (
            bugs_graph if bugs_graph is not None
            else HtmlGraph(self.bugs_dir, auto_load=False)
        )

        self.claiming_service = ClaimingService(
            features_graph=self.features_graph,
            bugs_graph=self.bugs_graph,
            session_manager=self,
        )

        self._active_session: Session | None = None
        self._active_sessions_cache: list[Session] | None = None
        self._sessions_cache_dirty: bool = True
        self._active_features_cache: list[Node] | None = None
        self._features_cache_dirty: bool = True

        self._spike_index = ActiveAutoSpikeIndex(self.graph_dir)
        self._active_auto_spikes: set[str] = self._spike_index.get_all()

        self.events_dir = self.graph_dir / "events"
        self.event_log = JsonlEventLog(self.events_dir)

        # Delegate modules
        self._spikes = SpikeManager(
            graph_dir=self.graph_dir, session_converter=self.session_converter,
            spike_index=self._spike_index, active_auto_spikes=self._active_auto_spikes,
        )
        self._errors = ErrorManager(session_converter=self.session_converter)
        self._drift = DriftDetector()
        self._transcript_ops = TranscriptOps(
            session_converter=self.session_converter, event_log=self.event_log,
        )
        self._linking = LinkingOps(
            session_converter=self.session_converter,
            features_graph=self.features_graph, bugs_graph=self.bugs_graph,
        )
        self._feature_workflow = FeatureWorkflow(self)

    # -- Session Lifecycle --

    def _list_active_sessions(self) -> list[Session]:
        if self._sessions_cache_dirty or self._active_sessions_cache is None:
            self._active_sessions_cache = [
                s for s in self.session_converter.load_all() if s.status == "active"
            ]
            self._sessions_cache_dirty = False
        return self._active_sessions_cache

    def _choose_canonical_active_session(self, sessions: list[Session]) -> Session | None:
        if not sessions:
            return None
        sessions.sort(key=lambda s: (s.event_count, s.last_activity.timestamp()), reverse=True)
        return sessions[0]

    def _mark_session_stale(self, session: Session) -> None:
        if session.status != "active":
            return
        now = datetime.now(timezone.utc)
        session.status = "stale"
        session.ended_at = now
        session.last_activity = now
        self.session_converter.save(session)
        self._sessions_cache_dirty = True

    def normalize_active_sessions(self) -> dict[str, int]:
        active_sessions = self._list_active_sessions()
        kept, staled = 0, 0
        by_agent: dict[str, list[Session]] = {}
        for s in active_sessions:
            if not s.is_subagent:
                by_agent.setdefault(s.agent, []).append(s)
        for _agent, sessions in by_agent.items():
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
        self, session_id: str | None = None, agent: str | None = None,
        is_subagent: bool = False, continued_from: str | None = None,
        start_commit: str | None = None, title: str | None = None,
        parent_session_id: str | None = None,
    ) -> Session:
        if agent is None:
            agent = detect_agent_name()
        now = datetime.now()
        if session_id is None:
            session_id = generate_id(node_type="session", title=title or agent)
        desired_commit = start_commit or self._get_current_commit()

        # Idempotency
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

        # Dedupe
        if not is_subagent:
            active_sessions = [
                s for s in self._list_active_sessions()
                if (not s.is_subagent) and s.agent == agent
            ]
            canonical = self._choose_canonical_active_session(active_sessions)
            if canonical and canonical.start_commit == desired_commit:
                self._active_session = canonical
                canonical.last_activity = now
                self.session_converter.save(canonical)
                self._sessions_cache_dirty = True
                return canonical
            for s in active_sessions:
                self._mark_session_stale(s)

        session = Session(
            id=session_id, agent=agent, is_subagent=is_subagent,
            continued_from=continued_from, start_commit=desired_commit,
            status="active", started_at=now, last_activity=now,
            title=title or "", parent_session=parent_session_id,
        )
        session.add_activity(ActivityEntry(tool="SessionStart", summary="Session started", timestamp=now))

        import os
        os.environ["HTMLGRAPH_PARENT_SESSION"] = session.id

        self.session_converter.save(session)
        self._sessions_cache_dirty = True
        self._active_session = session

        self._spikes.complete_transition_spikes_on_conversation_start(session.agent)
        self._spikes.create_session_init_spike(session)
        return session

    def get_session(self, session_id: str) -> Session | None:
        if self._active_session and self._active_session.id == session_id:
            return self._active_session
        return self.session_converter.load(session_id)

    def get_last_ended_session(self, agent: str | None = None) -> Session | None:
        sessions = [s for s in self.session_converter.load_all() if s.status == "ended"]
        if agent:
            sessions = [s for s in sessions if s.agent == agent]
        if not sessions:
            return None
        sessions.sort(key=lambda s: s.ended_at or s.last_activity or s.started_at, reverse=True)
        return sessions[0]

    def get_active_session(self, agent: str | None = None) -> Session | None:
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

    def get_active_session_for_agent(self, agent: str) -> Session | None:
        if not agent:
            return self.get_active_session()
        if (self._active_session and self._active_session.status == "active"
                and self._active_session.agent == agent):
            return self._active_session
        sessions = [s for s in self._list_active_sessions() if s.agent == agent]
        canonical = self._choose_canonical_active_session(sessions)
        if canonical:
            self._active_session = canonical
        return canonical

    def dedupe_orphan_sessions(
        self, max_events: int = 1, move_dir_name: str = "_orphans",
        dry_run: bool = False, stale_extra_active: bool = True,
    ) -> dict[str, int]:
        moved, scanned, missing = 0, 0, 0
        dest_dir = self.sessions_dir / move_dir_name
        if not dry_run:
            dest_dir.mkdir(parents=True, exist_ok=True)
        for session in self.session_converter.load_all():
            scanned += 1
            if session.event_count > max_events or len(session.activity_log) > max_events:
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
        return {"scanned": scanned, "moved": moved, "missing": missing,
                "kept_active": normalized.get("kept", 0), "staled_active": normalized.get("staled", 0)}

    def end_session(
        self, session_id: str, handoff_notes: str | None = None,
        recommended_next: str | None = None, blockers: list[str] | None = None,
        end_commit: str | None = None,
    ) -> Session | None:
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
            session.end_commit = self._get_current_commit()
        session.end()
        session.add_activity(ActivityEntry(tool="SessionEnd", summary="Session ended", timestamp=datetime.now(timezone.utc)))
        self.release_session_features(session_id)
        self.session_converter.save(session)
        self._sessions_cache_dirty = True
        if self._active_session and self._active_session.id == session_id:
            self._active_session = None
        return session

    def set_session_handoff(
        self, session_id: str, handoff_notes: str | None = None,
        recommended_next: str | None = None, blockers: list[str] | None = None,
    ) -> Session | None:
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
            session.add_activity(ActivityEntry(tool="SessionHandoff", summary="Session handoff updated", timestamp=datetime.now()))
            self.session_converter.save(session)
        return session

    def continue_from_last(self, agent: str | None = None, auto_create_session: bool = True) -> tuple[Session | None, Any]:
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
            new_session = self.start_session(
                session_id=generate_id("sess"), agent=agent or last_session.agent,
                title=f"Continuing from {last_session.id}",
            )
            new_session.continued_from = last_session.id
            self.session_converter.save(new_session)
        return new_session, resume_info

    def end_session_with_handoff(
        self, session_id: str, summary: str | None = None,
        next_focus: str | None = None, blockers: list[str] | None = None,
        keep_context: list[str] | None = None, auto_recommend_context: bool = True,
    ) -> Session | None:
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
            builder.auto_recommend_context(ContextRecommender(), max_files=10)
        handoff_data = builder.build()
        session.handoff_notes = handoff_data["handoff_notes"]
        session.recommended_next = handoff_data["recommended_next"]
        session.blockers = handoff_data["blockers"]
        session.recommended_context = handoff_data["recommended_context"]
        self.session_converter.save(session)
        self.end_session(session_id)
        return session

    def release_session_features(self, session_id: str) -> list[str]:
        return self.claiming_service.release_session_features(session_id)

    # =========================================================================
    # Error Handling (delegated)
    # =========================================================================

    def log_error(self, session_id: str, error: Exception, traceback_str: str, context: dict[str, Any] | None = None) -> None:
        self._errors.log_error(session_id=session_id, error=error, traceback_str=traceback_str, context=context, cached_session=self._active_session)

    def get_session_errors(self, session_id: str) -> list[dict[str, Any]]:
        return self._errors.get_session_errors(session_id, cached_session=self._active_session)

    def search_errors(self, session_id: str, error_type: str | None = None, pattern: str | None = None) -> list[dict[str, Any]]:
        return self._errors.search_errors(session_id, error_type=error_type, pattern=pattern, cached_session=self._active_session)

    def get_error_summary(self, session_id: str) -> dict[str, Any]:
        return self._errors.get_error_summary(session_id, cached_session=self._active_session)

    # =========================================================================
    # Activity Tracking
    # =========================================================================

    def track_activity(
        self, session_id: str, tool: str, summary: str,
        file_paths: list[str] | None = None, success: bool = True,
        feature_id: str | None = None, parent_activity_id: str | None = None,
        payload: dict[str, Any] | None = None,
    ) -> ActivityEntry:
        session = self.get_session(session_id)
        if not session:
            raise SessionNotFoundError(session_id)

        active_features = self.get_active_features()
        attributed_feature = feature_id
        drift_score = None
        attribution_reason = None

        if parent_activity_id:
            if not attributed_feature and active_features:
                primary = next((f for f in active_features if f.properties.get("is_primary")), None)
                attributed_feature = (primary or active_features[0]).id if active_features else None
            attribution_reason = "child_activity"
        elif self._is_system_overhead(tool, summary, file_paths or []):
            if not attributed_feature and active_features:
                primary = next((f for f in active_features if f.properties.get("is_primary")), None)
                attributed_feature = (primary or active_features[0]).id if active_features else None
            attribution_reason = "system_overhead"
        elif not attributed_feature and active_features:
            attribution = self.attribute_activity(
                tool=tool, summary=summary, file_paths=file_paths or [],
                active_features=active_features, agent=session.agent,
            )
            attributed_feature = attribution["feature_id"]
            drift_score = attribution["drift_score"]
            attribution_reason = attribution["reason"]

        event_id = generate_id(node_type="event", title=f"{tool}:{summary[:50]}")
        entry = ActivityEntry(
            id=event_id, timestamp=datetime.now(), tool=tool, summary=summary,
            success=success, feature_id=attributed_feature, drift_score=drift_score,
            parent_activity_id=parent_activity_id,
            payload={**(payload or {}), "file_paths": file_paths,
                     "attribution_reason": attribution_reason, "session_id": session_id}
            if file_paths or attribution_reason or session_id else payload,
        )

        self._append_to_event_log(entry, session, session_id, file_paths, payload)
        self._update_sqlite_index(entry, session, session_id, file_paths, payload)

        session.add_activity(entry)
        if attributed_feature:
            self._linking.add_session_link_to_feature(attributed_feature, session_id, self.get_session)
            self._check_completion(attributed_feature, tool, success)
        self.session_converter.save(session)
        self._active_session = session
        return entry

    def _append_to_event_log(
        self, entry: ActivityEntry, session: Session, session_id: str,
        file_paths: list[str] | None, payload: dict[str, Any] | None,
    ) -> None:
        try:
            from htmlgraph.work_type_utils import infer_work_type_from_id
            work_type = infer_work_type_from_id(entry.feature_id)
            self.event_log.append(EventRecord(
                event_id=entry.id or "", timestamp=entry.timestamp,
                session_id=session_id, agent=session.agent, tool=entry.tool,
                summary=entry.summary, success=entry.success,
                feature_id=entry.feature_id, drift_score=entry.drift_score,
                start_commit=session.start_commit, continued_from=session.continued_from,
                work_type=work_type, session_status=session.status,
                file_paths=file_paths, parent_session_id=session.parent_session,
                payload=entry.payload if isinstance(entry.payload, dict) else payload,
            ))
        except Exception as e:
            logger.warning(f"Failed to append to event log: {e}")

    def _update_sqlite_index(
        self, entry: ActivityEntry, session: Session, session_id: str,
        file_paths: list[str] | None, payload: dict[str, Any] | None,
    ) -> None:
        try:
            index_path = self.graph_dir / "index.sqlite"
            if not index_path.exists():
                return
            from htmlgraph.analytics_index import AnalyticsIndex
            idx = AnalyticsIndex(index_path)
            idx.ensure_schema()
            idx.upsert_session({
                "session_id": session_id, "agent": session.agent,
                "start_commit": session.start_commit, "continued_from": session.continued_from,
                "status": session.status, "started_at": session.started_at.isoformat(),
                "ended_at": session.ended_at.isoformat() if session.ended_at else None,
            })
            idx.upsert_event({
                "event_id": entry.id, "timestamp": entry.timestamp.isoformat(),
                "session_id": session_id, "tool": entry.tool, "summary": entry.summary,
                "success": entry.success, "feature_id": entry.feature_id,
                "drift_score": entry.drift_score, "file_paths": file_paths or [],
                "payload": entry.payload if isinstance(entry.payload, dict) else payload,
            })
        except Exception as e:
            logger.warning(f"Failed to update SQLite index: {e}")

    def track_user_query(self, session_id: str, prompt: str, feature_id: str | None = None) -> ActivityEntry:
        preview = prompt[:100] + "..." if len(prompt) > 100 else prompt
        return self.track_activity(
            session_id=session_id, tool="UserQuery", summary=f'"{preview.replace(chr(10), " ")}"',
            feature_id=feature_id, payload={"prompt": prompt, "prompt_length": len(prompt)},
        )

    # =========================================================================
    # Smart Attribution
    # =========================================================================

    def attribute_activity(
        self, tool: str, summary: str, file_paths: list[str],
        active_features: list[Node], agent: str | None = None,
    ) -> dict[str, Any]:
        active_spike = self._spikes.get_active_auto_spike(active_features)
        if active_spike:
            return {"feature_id": active_spike.id, "score": 1.0, "drift_score": 0.0,
                    "reason": f"auto_spike_{active_spike.spike_subtype}"}
        if not active_features:
            return {"feature_id": None, "score": 0, "drift_score": None, "reason": "no_active_features"}

        scores = []
        for feature in active_features:
            score, reasons = self._score_feature_match(feature, tool, summary, file_paths, agent=agent)
            if score >= 0:
                scores.append((feature, score, reasons))
        if not scores:
            return {"feature_id": None, "score": 0, "drift_score": None, "reason": "no_matching_features_authorized"}

        scores.sort(key=lambda x: x[1], reverse=True)
        best_feature, best_score, best_reasons = scores[0]
        return {"feature_id": best_feature.id, "score": best_score,
                "drift_score": 1.0 - min(best_score, 1.0),
                "reason": ", ".join(best_reasons) if best_reasons else "default_match"}

    def _score_feature_match(self, feature: Node, _tool: str, summary: str,
                             file_paths: list[str], agent: str | None = None) -> tuple[float, list[str]]:
        score, reasons = 0.0, []
        if feature.agent_assigned:
            if agent and feature.agent_assigned != agent:
                return -1.0, ["claimed_by_other"]
            if agent and feature.agent_assigned == agent:
                score += 2.0
                reasons.append("assigned_to_agent")
        file_patterns = feature.properties.get("file_patterns", [])
        if file_patterns and file_paths:
            ps = self._score_file_patterns(file_paths, file_patterns)
            if ps > 0:
                score += ps * self.WEIGHT_FILE_PATTERN
                reasons.append("file_pattern")
        keywords = self._extract_keywords(feature.title + " " + feature.content)
        ks = self._score_keyword_overlap(summary + " " + " ".join(file_paths), keywords)
        if ks > 0:
            score += ks * self.WEIGHT_KEYWORD
            reasons.append("keyword")
        score += self.TYPE_PRIORITY.get(feature.type, 0.5) * self.WEIGHT_TYPE_PRIORITY
        if feature.properties.get("is_primary"):
            score += self.WEIGHT_IS_PRIMARY
            reasons.append("primary")
        if feature.status == "in-progress":
            score += 0.1
            reasons.append("in_progress")
        return score, reasons

    def _score_file_patterns(self, file_paths: list[str], patterns: list[str]) -> float:
        if not file_paths or not patterns:
            return 0.0
        matches = sum(1 for p in file_paths if any(fnmatch.fnmatch(p, pat) for pat in patterns))
        return matches / len(file_paths)

    def _extract_keywords(self, text: str) -> set[str]:
        words = re.findall(r"\b[a-zA-Z]{3,}\b", text.lower())
        return set(words) - {"the", "and", "for", "with", "this", "that", "from", "are", "was", "were"}

    def _score_keyword_overlap(self, text: str, keywords: set[str]) -> float:
        if not keywords:
            return 0.0
        return len(self._extract_keywords(text) & keywords) / len(keywords)

    def _is_system_overhead(self, tool: str, summary: str, file_paths: list[str]) -> bool:
        return self._drift.is_system_overhead(tool, summary, file_paths)

    # =========================================================================
    # Drift Detection (delegated)
    # =========================================================================

    def detect_drift(self, session_id: str, feature_id: str) -> dict[str, Any]:
        session = self.get_session(session_id)
        if not session:
            return {"is_drifting": False, "drift_score": 0, "reasons": []}
        return self._drift.detect_drift(session, feature_id)

    # =========================================================================
    # Feature Management
    # =========================================================================

    def _ensure_session_for_agent(self, agent: str) -> Session:
        active = self.get_active_session_for_agent(agent)
        return active or self.start_session(session_id=None, agent=agent, title=f"Auto session ({agent})")

    def _maybe_log_work_item_action(self, *, agent: str | None, tool: str, summary: str,
                                     feature_id: str | None, success: bool = True,
                                     payload: dict[str, Any] | None = None) -> None:
        if not agent:
            return
        try:
            session = self._ensure_session_for_agent(agent)
            self.track_activity(session_id=session.id, tool=tool, summary=summary,
                                file_paths=None, success=success, feature_id=feature_id, payload=payload)
        except Exception as e:
            logger.warning(f"Failed to log work item action ({tool}): {e}")

    def get_active_features(self) -> list[Node]:
        if self._features_cache_dirty or self._active_features_cache is None:
            self._active_features_cache = [
                n for n in list(self.features_graph) + list(self.bugs_graph)
                if n.status == "in-progress"
            ]
            self._features_cache_dirty = False
        return self._active_features_cache

    def create_feature(self, title: str, collection: str = "features", description: str = "",
                       priority: str = "medium", steps: list[str] | None = None, agent: str | None = None) -> Node:
        return self._feature_workflow.create_feature(title, collection, description, priority, steps, agent)

    def get_primary_feature(self) -> Node | None:
        for f in self.get_active_features():
            if f.properties.get("is_primary"):
                return f
        active = self.get_active_features()
        return active[0] if active else None

    def start_feature(self, feature_id: str, collection: str = "features", *,
                      agent: str | None = None, log_activity: bool = True) -> Node | None:
        return self._feature_workflow.start_feature(feature_id, collection, agent=agent, log_activity=log_activity)

    def complete_feature(self, feature_id: str, collection: str = "features", *,
                         agent: str | None = None, log_activity: bool = True,
                         transcript_id: str | None = None) -> Node | None:
        return self._feature_workflow.complete_feature(feature_id, collection, agent=agent,
                                                        log_activity=log_activity, transcript_id=transcript_id)

    def set_primary_feature(self, feature_id: str, collection: str = "features", *,
                            agent: str | None = None, log_activity: bool = True) -> Node | None:
        return self._feature_workflow.set_primary_feature(feature_id, collection, agent=agent, log_activity=log_activity)

    def activate_feature(self, feature_id: str, collection: str = "features", *,
                         agent: str | None = None, log_activity: bool = True) -> Node | None:
        return self._feature_workflow.activate_feature(feature_id, collection, agent=agent, log_activity=log_activity)

    def _check_completion(self, feature_id: str, tool: str, success: bool) -> bool:
        return self._feature_workflow.check_completion(feature_id, tool, success)

    # =========================================================================
    # Status & Reporting
    # =========================================================================

    def get_status(self) -> dict[str, Any]:
        all_features = list(self.features_graph) + list(self.bugs_graph)
        by_status: dict[str, int] = {"todo": 0, "in-progress": 0, "blocked": 0, "done": 0}
        for n in all_features:
            by_status[n.status] = by_status.get(n.status, 0) + 1
        active = self.get_active_features()
        primary = self.get_primary_feature()
        active_session = self.get_active_session()
        return {"total_features": len(all_features), "by_status": by_status,
                "wip_count": len(active), "wip_limit": self.wip_limit,
                "wip_remaining": self.wip_limit - len(active),
                "primary_feature": primary.id if primary else None,
                "active_features": [f.id for f in active],
                "active_session": active_session.id if active_session else None}

    # =========================================================================
    # Claiming Mechanism
    # =========================================================================

    def claim_feature(self, feature_id: str, collection: str = "features", *, agent: str) -> Node | None:
        return self.claiming_service.claim_feature(feature_id=feature_id, collection=collection, agent=agent)

    def release_feature(self, feature_id: str, collection: str = "features", *, agent: str) -> Node | None:
        return self.claiming_service.release_feature(feature_id=feature_id, collection=collection, agent=agent)

    def auto_release_features(self, agent: str) -> list[str]:
        return self.claiming_service.auto_release_features(agent)

    def create_handoff(self, feature_id: str, reason: str, notes: str | None = None,
                       collection: str = "features", *, agent: str, next_agent: str | None = None) -> Node | None:
        graph = self._get_graph(collection)
        node = graph.get(feature_id)
        if not node:
            return None
        if node.agent_assigned and node.agent_assigned != agent:
            raise ValueError(f"Feature '{feature_id}' is claimed by {node.agent_assigned}, not {agent}")
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
        self._maybe_log_work_item_action(agent=agent, tool="FeatureHandoff",
            summary=f"Handed off: {collection}/{feature_id} (reason: {reason})", feature_id=feature_id,
            payload={"collection": collection, "action": "handoff", "reason": reason, "notes": notes, "next_agent": next_agent})
        return node

    # =========================================================================
    # Helpers
    # =========================================================================

    def _get_graph(self, collection: str) -> HtmlGraph:
        return self.bugs_graph if collection == "bugs" else self.features_graph

    def _get_graph_for_node(self, node: Node) -> HtmlGraph:
        return self.bugs_graph if node.type == "bug" else self.features_graph

    def _get_current_commit(self) -> str | None:
        try:
            import subprocess
            result = subprocess.run(["git", "rev-parse", "--short", "HEAD"],
                                    capture_output=True, text=True, cwd=self.graph_dir.parent)
            if result.returncode == 0:
                return result.stdout.strip()
        except Exception as e:
            logger.warning(f"Failed to get current git commit: {e}")
        return None

    # =========================================================================
    # Transcript Integration (delegated)
    # =========================================================================

    def link_transcript(self, session_id: str, transcript_id: str,
                        transcript_path: str | None = None, git_branch: str | None = None) -> Session | None:
        session = self.get_session(session_id)
        if not session:
            return None
        return self._transcript_ops.link_transcript(session, transcript_id, transcript_path, git_branch)

    def find_session_by_transcript(self, transcript_id: str) -> Session | None:
        return self._transcript_ops.find_session_by_transcript(transcript_id)

    def import_transcript_events(self, session_id: str, transcript_session: Any,
                                 overwrite: bool = False) -> dict[str, int | str]:
        session = self.get_session(session_id)
        if not session:
            return {"error": "session_not_found", "imported": 0}
        return self._transcript_ops.import_transcript_events(session, session_id, transcript_session, overwrite)

    def auto_link_transcript_by_branch(self, git_branch: str, agent: str | None = None) -> list[tuple[str, str]]:
        return self._transcript_ops.auto_link_by_branch(git_branch, self.graph_dir, agent)

    def get_transcript_stats(self, session_id: str) -> dict[str, Any] | None:
        session = self.get_session(session_id)
        if not session:
            return None
        return self._transcript_ops.get_transcript_stats(session)

    # =========================================================================
    # Session Context Builder
    # =========================================================================

    def get_version_status(self) -> dict[str, Any]:
        from htmlgraph.session_context import VersionChecker
        return VersionChecker.get_version_status()

    def initialize_git_hooks(self, project_dir: str | Path) -> bool:
        from htmlgraph.session_context import GitHooksInstaller
        return GitHooksInstaller.install(project_dir)

    def get_start_context(self, session_id: str, project_dir: str | Path | None = None,
                          compute_async: bool = True) -> str:
        from htmlgraph.session_context import SessionContextBuilder
        if project_dir is None:
            project_dir = self.graph_dir.parent
        return SessionContextBuilder(self.graph_dir, project_dir).build(
            session_id=session_id, compute_async=compute_async)

    def detect_feature_conflicts(self) -> list[dict[str, Any]]:
        from htmlgraph.session_context import SessionContextBuilder
        return SessionContextBuilder(self.graph_dir, self.graph_dir.parent).detect_feature_conflicts()
