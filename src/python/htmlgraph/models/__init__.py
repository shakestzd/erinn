"""
Pydantic models for HtmlGraph nodes, edges, and steps.

This package provides schema validation, HTML serialization/deserialization,
and lightweight context generation for AI agents.

All models are re-exported here for backward compatibility with existing imports.
"""

# Base models and utilities
# Analytics models
from htmlgraph.models.analytics import AggregatedMetric, Graph, SessionInsight, Todo
from htmlgraph.models.base import (
    Edge,
    MaintenanceType,
    Node,
    RelationshipType,
    SpikeType,
    Step,
    WorkType,
    utc_now,
)

# Context and session tracking models
from htmlgraph.models.context import ActivityEntry, ContextSnapshot, ErrorEntry
from htmlgraph.models.session import Session

# Work item models
from htmlgraph.models.work_items import Chore, Pattern, Spike

__all__ = [
    # Utilities
    "utc_now",
    # Enums
    "WorkType",
    "SpikeType",
    "MaintenanceType",
    "RelationshipType",
    # Base models
    "Step",
    "Edge",
    "Node",
    "Graph",
    # Work items
    "Spike",
    "Chore",
    "Pattern",
    # Context tracking
    "ContextSnapshot",
    "ErrorEntry",
    "ActivityEntry",
    # Session
    "Session",
    # Analytics
    "SessionInsight",
    "AggregatedMetric",
    "Todo",
]
