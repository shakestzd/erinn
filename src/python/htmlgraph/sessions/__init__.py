"""
Session management and continuity.

Provides session lifecycle, handoff, and resumption features.
"""

from htmlgraph.sessions.attribution import ActivityAttribution
from htmlgraph.sessions.features import FeatureManager
from htmlgraph.sessions.handoff import (
    ContextRecommender,
    HandoffBuilder,
    HandoffMetrics,
    HandoffTracker,
    SessionResume,
    SessionResumeInfo,
)
from htmlgraph.sessions.lifecycle import SessionLifecycle
from htmlgraph.sessions.transcripts import TranscriptManager

__all__ = [
    # Handoff and continuity
    "HandoffBuilder",
    "SessionResume",
    "SessionResumeInfo",
    "HandoffTracker",
    "HandoffMetrics",
    "ContextRecommender",
    # New modular components
    "SessionLifecycle",
    "ActivityAttribution",
    "FeatureManager",
    "TranscriptManager",
]
