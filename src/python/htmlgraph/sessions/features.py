"""
Feature management and attribution scoring.

Provides:
- FeatureWorkflow: create, start, complete, activate features
- Attribution scoring: match activities to features using smart scoring
  (file pattern matching, keyword extraction, system overhead detection)

FeatureWorkflow uses a back-reference to SessionManager for cross-cutting operations.
Scoring functions are standalone and used by ActivityTracker for attribution.
"""

from __future__ import annotations

import fnmatch
import logging
import re
from datetime import datetime
from typing import TYPE_CHECKING, Any

from htmlgraph.converter import dict_to_node
from htmlgraph.graph import HtmlGraph
from htmlgraph.ids import generate_id
from htmlgraph.models import Node

if TYPE_CHECKING:
    from htmlgraph.session_manager import SessionManager

logger = logging.getLogger(__name__)


# =========================================================================
# Attribution Scoring Constants
# =========================================================================

# Attribution scoring weights
WEIGHT_FILE_PATTERN = 0.4
WEIGHT_KEYWORD = 0.3
WEIGHT_TYPE_PRIORITY = 0.2
WEIGHT_IS_PRIMARY = 0.1

# Type priorities (higher = more likely to be active work)
TYPE_PRIORITY: dict[str, float] = {
    "bug": 1.0,
    "hotfix": 1.0,
    "feature": 0.8,
    "spike": 0.6,
    "chore": 0.4,
    "epic": 0.2,
}

# System skills that are overhead, not feature work
SYSTEM_SKILLS = {"htmlgraph-tracker", "htmlgraph:htmlgraph-tracker"}

# Infrastructure file patterns to exclude from drift scoring
INFRASTRUCTURE_PATTERNS = [
    ".htmlgraph/",
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
    ".github/",
    ".gitlab-ci.yml",
    ".travis.yml",
    "circle.yml",
    ".pre-commit-config.yaml",
    "dist/",
    "build/",
    ".eggs/",
    "*.egg-info/",
    "__pycache__/",
    "*.pyc",
    "*.pyo",
    "*.pyd",
    ".vscode/",
    ".idea/",
    "*.swp",
    "*.swo",
    "*~",
    ".DS_Store",
    "Thumbs.db",
    ".pytest_cache/",
    ".coverage",
    "htmlcov/",
    ".tox/",
    ".env",
    ".env.local",
    ".env.*.local",
    "README.md",
    "CONTRIBUTING.md",
    "LICENSE",
    "CHANGELOG.md",
    "docs/",
    ".contextune/",
    ".parallel/",
    "node_modules/",
    ".venv/",
    "venv/",
]


# =========================================================================
# Attribution Scoring Functions
# =========================================================================


def extract_keywords(text: str) -> set[str]:
    """Extract keywords from text."""
    words = re.findall(r"\b[a-zA-Z]{3,}\b", text.lower())
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


def score_keyword_overlap(text: str, keywords: set[str]) -> float:
    """Score keyword overlap between text and keywords."""
    if not keywords:
        return 0.0
    text_words = extract_keywords(text)
    overlap = text_words & keywords
    return len(overlap) / len(keywords) if keywords else 0.0


def score_file_patterns(file_paths: list[str], patterns: list[str]) -> float:
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


def score_feature_match(
    feature: Node,
    _tool: str,
    summary: str,
    file_paths: list[str],
    agent: str | None = None,
) -> tuple[float, list[str]]:
    """Score how well an activity matches a feature."""
    score = 0.0
    reasons: list[str] = []

    if feature.agent_assigned:
        if agent and feature.agent_assigned != agent:
            return -1.0, ["claimed_by_other"]
        if agent and feature.agent_assigned == agent:
            score += 2.0
            reasons.append("assigned_to_agent")

    file_patterns_list = feature.properties.get("file_patterns", [])
    if file_patterns_list and file_paths:
        pattern_score = score_file_patterns(file_paths, file_patterns_list)
        if pattern_score > 0:
            score += pattern_score * WEIGHT_FILE_PATTERN
            reasons.append("file_pattern")

    keywords = extract_keywords(feature.title + " " + feature.content)
    activity_text = summary + " " + " ".join(file_paths)
    kw_score = score_keyword_overlap(activity_text, keywords)
    if kw_score > 0:
        score += kw_score * WEIGHT_KEYWORD
        reasons.append("keyword")

    type_score = TYPE_PRIORITY.get(feature.type, 0.5)
    score += type_score * WEIGHT_TYPE_PRIORITY

    if feature.properties.get("is_primary"):
        score += WEIGHT_IS_PRIMARY
        reasons.append("primary")

    if feature.status == "in-progress":
        score += 0.1
        reasons.append("in_progress")

    return score, reasons


def attribute_activity(
    tool: str,
    summary: str,
    file_paths: list[str],
    active_features: list[Node],
    agent: str | None = None,
    get_active_auto_spike: Any = None,
) -> dict[str, Any]:
    """Score and attribute an activity to the best matching feature or auto-spike."""
    # Priority 1: Check for active auto-generated spikes
    if get_active_auto_spike:
        active_spike = get_active_auto_spike(active_features)
        if active_spike:
            return {
                "feature_id": active_spike.id,
                "score": 1.0,
                "drift_score": 0.0,
                "reason": f"auto_spike_{active_spike.spike_subtype}",
            }

    # Priority 2: Regular feature attribution
    if not active_features:
        return {
            "feature_id": None,
            "score": 0,
            "drift_score": None,
            "reason": "no_active_features",
        }

    scores = []
    for feature in active_features:
        sc, reasons = score_feature_match(
            feature, tool, summary, file_paths, agent=agent
        )
        if sc < 0:
            continue
        scores.append((feature, sc, reasons))

    if not scores:
        return {
            "feature_id": None,
            "score": 0,
            "drift_score": None,
            "reason": "no_matching_features_authorized",
        }

    scores.sort(key=lambda x: x[1], reverse=True)
    best_feature, best_score, best_reasons = scores[0]

    drift_score = 1.0 - min(best_score, 1.0)

    return {
        "feature_id": best_feature.id,
        "score": best_score,
        "drift_score": drift_score,
        "reason": ", ".join(best_reasons) if best_reasons else "default_match",
    }


def is_system_overhead(tool: str, summary: str, file_paths: list[str]) -> bool:
    """Determine if an activity is system overhead that shouldn't count as drift."""
    if tool == "Skill":
        for skill_name in SYSTEM_SKILLS:
            if skill_name in summary.lower():
                return True

    if file_paths:
        for path in file_paths:
            path_normalized = path.replace("\\", "/")
            path_lower = path_normalized.lower()

            for pattern in INFRASTRUCTURE_PATTERNS:
                pattern_lower = pattern.lower()

                if pattern_lower.endswith("/"):
                    if "*" in pattern_lower:
                        path_parts = path_lower.split("/")
                        for part in path_parts:
                            if fnmatch.fnmatch(part, pattern_lower.rstrip("/")):
                                return True
                    elif pattern_lower in path_lower or path_lower.startswith(
                        pattern_lower
                    ):
                        return True
                elif "*" in pattern_lower:
                    filename = path_lower.split("/")[-1]
                    if fnmatch.fnmatch(filename, pattern_lower):
                        return True
                else:
                    if (
                        path_lower.endswith(pattern_lower)
                        or f"/{pattern_lower}" in path_lower
                    ):
                        return True

    return False


# =========================================================================
# Feature Workflow
# =========================================================================


class FeatureWorkflow:
    """Manages feature lifecycle operations (create, start, complete, activate)."""

    def __init__(self, manager: SessionManager):
        self._m = manager

    def create_feature(
        self,
        title: str,
        collection: str = "features",
        description: str = "",
        priority: str = "medium",
        steps: list[str] | None = None,
        agent: str | None = None,
    ) -> Node:
        node_type = collection[:-1] if collection.endswith("s") else collection
        node_id = generate_id(node_type=node_type, title=title)
        if steps is None:
            steps = (
                [
                    "Design approach",
                    "Implement core functionality",
                    "Add tests",
                    "Update documentation",
                ]
                if collection == "features"
                else []
            )
        node = dict_to_node(
            {
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
        )
        self._m._get_graph(collection).add(node)
        if agent:
            self._m._maybe_log_work_item_action(
                agent=agent,
                tool="FeatureCreate",
                summary=f"Created: {collection}/{node_id}",
                feature_id=node_id,
                payload={
                    "collection": collection,
                    "action": "create",
                    "title": title,
                },
            )
        return node

    def start_feature(
        self,
        feature_id: str,
        collection: str = "features",
        *,
        agent: str | None = None,
        log_activity: bool = True,
    ) -> Node | None:
        graph = self._m._get_graph(collection)
        node = graph.get(feature_id)
        if not node:
            return None
        if agent and node.agent_assigned and node.agent_assigned != agent:
            if node.claimed_by_session:
                s = self._m.get_session(node.claimed_by_session)
                if s and s.status == "active":
                    raise ValueError(
                        f"Feature '{feature_id}' is claimed by {node.agent_assigned} "
                        f"(session {node.claimed_by_session})"
                    )
        active = self._m.get_active_features()
        if len(active) >= self._m.wip_limit and node not in active:
            raise ValueError(
                f"WIP limit ({self._m.wip_limit}) reached. "
                f"Complete existing work first."
            )
        if agent and not node.agent_assigned:
            self._m.claim_feature(feature_id, collection=collection, agent=agent)
            node = graph.get(feature_id)
            if not node:
                raise ValueError(f"Feature {feature_id} not found after claiming")
        node.status = "in-progress"
        node.updated = datetime.now()
        graph.update(node)
        self._m._features_cache_dirty = True
        if agent:
            self._m._spikes.complete_active_auto_spikes(
                agent,
                to_feature_id=feature_id,
                get_active_session=self._m.get_active_session,
                import_transcript_events=self._m.import_transcript_events,
            )
        active_session = (
            self._m.get_active_session_for_agent(agent)
            if agent
            else self._m.get_active_session()
        )
        if agent and not active_session:
            active_session = self._m._ensure_session_for_agent(agent)
        if active_session:
            self._m._linking.add_session_link_to_feature(
                feature_id,
                active_session.id,
                self._m.get_session,
            )

        # Backfill Turn 1 attribution: update the most recent unattributed UserQuery
        # event in the current session.  The UserPromptSubmit hook writes the UserQuery
        # before Claude can call sdk.features.start(), so Turn 1's event lands with
        # feature_id IS NULL.  This retroactively stamps it so it shows up attributed.
        #
        # Pass session_id=None when active_session is unknown (e.g. called from a
        # subprocess) so _backfill_turn1_userquery falls back to the DB-wide
        # most-recent unattributed UserQuery instead of silently skipping backfill.
        self._m._backfill_turn1_userquery(
            active_session.id if active_session else None,
            feature_id,
        )

        if log_activity and agent:
            self._m._maybe_log_work_item_action(
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
        graph = self._m._get_graph(collection)
        node = graph.get(feature_id)
        if not node:
            node = graph.reload_node(feature_id)
            if not node:
                return None
        node.status = "done"
        node.updated = datetime.now()
        node.properties["completed_at"] = datetime.now().isoformat()
        if transcript_id:
            self._m._linking.link_transcript_to_feature(node, transcript_id, graph)
        graph.update(node)
        self._m._features_cache_dirty = True
        if log_activity and agent:
            p: dict[str, Any] = {"collection": collection, "action": "complete"}
            if transcript_id:
                p["transcript_id"] = transcript_id
            self._m._maybe_log_work_item_action(
                agent=agent,
                tool="FeatureComplete",
                summary=f"Completed: {collection}/{feature_id}",
                feature_id=feature_id,
                payload=p,
            )
        session = self._m.get_active_session(agent=agent)
        if session and session.transcript_id:
            try:
                from htmlgraph.transcript import TranscriptReader

                t = TranscriptReader().read_session(session.transcript_id)
                if t:
                    self._m.import_transcript_events(
                        session_id=session.id,
                        transcript_session=t,
                        overwrite=True,
                    )
            except Exception as e:
                logger.warning(
                    f"Failed to auto-import transcript on feature completion: {e}"
                )
        if session:
            self._m._spikes.create_transition_spike(session, from_feature_id=feature_id)
        if session:
            self._run_completion_analysis(node, feature_id, session, graph, agent)
        return node

    def _run_completion_analysis(
        self,
        node: Node,
        feature_id: str,
        session: Any,
        graph: HtmlGraph,
        agent: str | None,
    ) -> None:
        try:
            from htmlgraph.learning import LearningPersistence
            from htmlgraph.sdk import SDK

            sdk = SDK(agent=agent or "unknown", directory=self._m.graph_dir)
            learning = LearningPersistence(sdk)
            analysis = learning.analyze_for_orchestrator(session.id)
            node.properties["completion_analysis"] = analysis
            insight_id = learning.persist_session_insight(session.id)
            if insight_id:
                node.properties["insight_id"] = insight_id
            pattern_ids = learning.persist_patterns()
            if pattern_ids:
                logger.debug(f"Persisted {len(pattern_ids)} patterns")
            if analysis.get("summary", "").startswith("\u26a0\ufe0f"):
                logger.info(
                    f"Work item {feature_id} completed with issues: "
                    f"{analysis['summary']}"
                )
            graph.update(node)
        except Exception as e:
            logger.warning(f"Failed to analyze session on completion: {e}")

    def set_primary_feature(
        self,
        feature_id: str,
        collection: str = "features",
        *,
        agent: str | None = None,
        log_activity: bool = True,
    ) -> Node | None:
        for f in self._m.get_active_features():
            if f.properties.get("is_primary"):
                f.properties["is_primary"] = False
                self._m._get_graph_for_node(f).update(f)
        graph = self._m._get_graph(collection)
        node = graph.get(feature_id)
        if node:
            node.properties["is_primary"] = True
            graph.update(node)
        if log_activity and agent:
            self._m._maybe_log_work_item_action(
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
        node = self.start_feature(
            feature_id, collection=collection, agent=agent, log_activity=False
        )
        if node is None:
            return None
        self.set_primary_feature(
            feature_id, collection=collection, agent=agent, log_activity=False
        )
        if log_activity and agent:
            self._m._maybe_log_work_item_action(
                agent=agent,
                tool="FeatureActivate",
                summary=f"Activated: {collection}/{feature_id}",
                feature_id=feature_id,
                payload={"collection": collection, "action": "activate"},
            )
        return node

    def check_completion(self, feature_id: str, tool: str, success: bool) -> bool:
        node = self._m.features_graph.get(feature_id) or self._m.bugs_graph.get(
            feature_id
        )
        if not node:
            return False
        criteria = node.properties.get("completion_criteria", {})
        ct = criteria.get("type", "manual")
        if ct == "manual":
            return False
        if ct == "work_count":
            wc = node.properties.get("work_count", 0) + 1
            node.properties["work_count"] = wc
            if wc >= criteria.get("count", 10):
                self.complete_feature(feature_id)
                return True
        if ct == "test" and tool == "Bash" and success:
            pass
        if ct == "steps" and node.steps and all(s.completed for s in node.steps):
            self.complete_feature(feature_id)
            return True
        return False
