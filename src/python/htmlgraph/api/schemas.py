"""
Pydantic response models for HtmlGraph API.

These models define the structure of API responses for:
- Events (agent activity)
- Features (tracking items)
- Sessions (agent sessions)
"""

from pydantic import BaseModel, ConfigDict


class EventModel(BaseModel):
    """Event data model for API ingress/responses."""

    model_config = ConfigDict(strict=True)

    event_id: str
    agent_id: str
    event_type: str
    timestamp: str
    tool_name: str | None = None
    input_summary: str | None = None
    tool_input: dict | None = None
    output_summary: str | None = None
    session_id: str
    feature_id: str | None = None
    parent_event_id: str | None = None
    status: str
    model: str | None = None


class FeatureModel(BaseModel):
    """Feature data model for API ingress/responses."""

    model_config = ConfigDict(strict=True)

    id: str
    type: str
    title: str
    description: str | None = None
    status: str
    priority: str
    assigned_to: str | None = None
    created_at: str
    updated_at: str
    completed_at: str | None = None


class SessionModel(BaseModel):
    """Session data model for API ingress/responses."""

    model_config = ConfigDict(strict=True)

    session_id: str
    agent: str | None = None
    status: str
    started_at: str
    ended_at: str | None = None
    event_count: int = 0
    duration_seconds: float | None = None
