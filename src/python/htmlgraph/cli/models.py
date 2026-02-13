from __future__ import annotations

"""HtmlGraph CLI - Pydantic models for command filters and configuration.

This module provides type-safe models for validating command inputs:
- Filter models for list commands (features, sessions, tracks)
- Configuration models for infrastructure commands (init, serve)
- Validation helpers for converting argparse to Pydantic
"""


from datetime import datetime
from typing import Any, Literal, TypeVar

from pydantic import BaseModel, Field, ValidationError, field_validator

T = TypeVar("T", bound=BaseModel)

# ============================================================================
# Filter Models
# ============================================================================


class FeatureFilter(BaseModel):
    """Filter options for feature listing.

    Attributes:
        status: Filter by status (todo, in_progress, completed, blocked, all)
        priority: Filter by priority (high, medium, low, critical, all)
        agent: Filter by agent name
        collection: Collection name to query (default: features)
        quiet: Suppress empty output
    """

    status: Literal["todo", "in_progress", "completed", "blocked", "all"] | None = None
    priority: Literal["high", "medium", "low", "critical", "all"] | None = None
    agent: str | None = None
    collection: str = Field(default="features")
    quiet: bool = Field(default=False)

    @field_validator("status")
    @classmethod
    def validate_status(cls, v: str | None) -> str | None:
        """Validate status value."""
        if v and v not in ["todo", "in_progress", "completed", "blocked", "all"]:
            raise ValueError(
                f"Invalid status: {v}. "
                f"Valid values: todo, in_progress, completed, blocked, all"
            )
        return v

    @field_validator("priority")
    @classmethod
    def validate_priority(cls, v: str | None) -> str | None:
        """Validate priority value."""
        if v and v not in ["high", "medium", "low", "critical", "all"]:
            raise ValueError(
                f"Invalid priority: {v}. Valid values: high, medium, low, critical, all"
            )
        return v


class FeatureCreateConfig(BaseModel):
    """Configuration for creating a new feature.

    Attributes:
        title: Feature title
        description: Feature description
        priority: Feature priority (low, medium, high, critical)
        steps: Number of steps
        collection: Collection name
        track: Track ID to link feature to
        agent: Agent name
    """

    title: str = Field(..., min_length=1, description="Feature title")
    description: str | None = Field(None, description="Feature description")
    priority: Literal["low", "medium", "high", "critical"] = Field(default="medium")
    steps: int | None = Field(None, ge=1, description="Number of steps")
    collection: str = Field(default="features")
    track: str | None = Field(None, description="Track ID to link feature to")
    agent: str = Field(default="claude-code")

    @field_validator("title")
    @classmethod
    def validate_title(cls, v: str) -> str:
        """Validate title is not empty."""
        if not v or not v.strip():
            raise ValueError("Feature title cannot be empty")
        return v.strip()


class SessionFilter(BaseModel):
    """Filter options for session listing.

    Attributes:
        status: Filter by status (active, ended, all)
        agent: Filter by agent name
        since: Only show sessions since this date
    """

    status: Literal["active", "ended", "all"] | None = None
    agent: str | None = None
    since: datetime | None = None

    @field_validator("status")
    @classmethod
    def validate_status(cls, v: str | None) -> str | None:
        """Validate status value."""
        if v and v not in ["active", "ended", "all"]:
            raise ValueError(f"Invalid status: {v}. Valid values: active, ended, all")
        return v


class SessionStartConfig(BaseModel):
    """Configuration for starting a new session.

    Attributes:
        session_id: Session ID (auto-generated if not provided)
        agent: Agent name
        title: Session title
    """

    session_id: str | None = Field(None, description="Session ID")
    agent: str = Field(default="claude-code")
    title: str | None = Field(None, description="Session title")


class SessionEndConfig(BaseModel):
    """Configuration for ending a session.

    Attributes:
        session_id: Session ID to end
        notes: Handoff notes for the next session
        recommend: Recommended next steps
        blockers: List of blockers to record
    """

    session_id: str = Field(..., min_length=1, description="Session ID to end")
    notes: str | None = Field(None, description="Handoff notes")
    recommend: str | None = Field(None, description="Recommended next steps")
    blockers: list[str] = Field(default_factory=list, description="Blockers to record")

    @field_validator("session_id")
    @classmethod
    def validate_session_id(cls, v: str) -> str:
        """Validate session ID is not empty."""
        if not v or not v.strip():
            raise ValueError("Session ID cannot be empty")
        return v.strip()


class TrackFilter(BaseModel):
    """Filter options for track listing.

    Attributes:
        status: Filter by status (todo, in_progress, completed, all)
        priority: Filter by priority (high, medium, low, all)
        has_spec: Filter for tracks with specs
        has_plan: Filter for tracks with plans
    """

    status: Literal["todo", "in_progress", "completed", "all"] | None = None
    priority: Literal["high", "medium", "low", "all"] | None = None
    has_spec: bool | None = None
    has_plan: bool | None = None

    @field_validator("status")
    @classmethod
    def validate_status(cls, v: str | None) -> str | None:
        """Validate status value."""
        if v and v not in ["todo", "in_progress", "completed", "all"]:
            raise ValueError(
                f"Invalid status: {v}. Valid values: todo, in_progress, completed, all"
            )
        return v

    @field_validator("priority")
    @classmethod
    def validate_priority(cls, v: str | None) -> str | None:
        """Validate priority value."""
        if v and v not in ["high", "medium", "low", "all"]:
            raise ValueError(
                f"Invalid priority: {v}. Valid values: high, medium, low, all"
            )
        return v


class TrackNewConfig(BaseModel):
    """Configuration for creating a new track.

    Attributes:
        title: Track title
        description: Track description
        priority: Track priority (low, medium, high)
    """

    title: str = Field(..., min_length=1, description="Track title")
    description: str | None = Field(None, description="Track description")
    priority: Literal["low", "medium", "high"] = Field(default="medium")

    @field_validator("title")
    @classmethod
    def validate_title(cls, v: str) -> str:
        """Validate title is not empty."""
        if not v or not v.strip():
            raise ValueError("Track title cannot be empty")
        return v.strip()


# ============================================================================
# Configuration Models
# ============================================================================


class InitConfig(BaseModel):
    """Configuration for htmlgraph init command.

    Attributes:
        dir: Directory to initialize (default: .)
        install_hooks: Install Git hooks for event logging
        interactive: Interactive setup wizard
        no_index: Do not create the analytics cache (index.sqlite)
        no_update_gitignore: Do not update/create .gitignore for cache files
        no_events_keep: Do not create .htmlgraph/events/.gitkeep
    """

    dir: str = Field(default=".")
    install_hooks: bool = Field(default=False)
    interactive: bool = Field(default=False)
    no_index: bool = Field(default=False)
    no_update_gitignore: bool = Field(default=False)
    no_events_keep: bool = Field(default=False)


class ServeConfig(BaseModel):
    """Configuration for htmlgraph serve command.

    Attributes:
        port: Port to bind to (must be between 1024-65535)
        host: Host to bind to (default: 0.0.0.0)
        graph_dir: Graph directory path
        static_dir: Static files directory
        no_watch: Disable file watching (auto-reload disabled)
        auto_port: Automatically find available port if default is occupied
    """

    port: int = Field(default=8080, ge=1024, le=65535)
    host: str = Field(default="0.0.0.0")
    graph_dir: str = Field(default=".htmlgraph")
    static_dir: str = Field(default=".")
    no_watch: bool = Field(default=False)
    auto_port: bool = Field(default=False)

    @field_validator("port")
    @classmethod
    def validate_port(cls, v: int) -> int:
        """Validate port is in valid range."""
        if not 1024 <= v <= 65535:
            raise ValueError(f"Port must be between 1024 and 65535, got {v}")
        return v

    @field_validator("host")
    @classmethod
    def validate_host(cls, v: str) -> str:
        """Validate host is not empty."""
        if not v or not v.strip():
            raise ValueError("Host cannot be empty")
        return v.strip()


class ServeApiConfig(BaseModel):
    """Configuration for htmlgraph serve-api command.

    Attributes:
        port: Port to bind to (must be between 1024-65535)
        host: Host to bind to (default: 127.0.0.1)
        db: Path to SQLite database file
        auto_port: Automatically find available port if default is occupied
        reload: Enable auto-reload on file changes (development mode)
    """

    port: int = Field(default=8000, ge=1024, le=65535)
    host: str = Field(default="127.0.0.1")
    db: str | None = None
    auto_port: bool = Field(default=False)
    reload: bool = Field(default=False)

    @field_validator("port")
    @classmethod
    def validate_port(cls, v: int) -> int:
        """Validate port is in valid range."""
        if not 1024 <= v <= 65535:
            raise ValueError(f"Port must be between 1024 and 65535, got {v}")
        return v

    @field_validator("host")
    @classmethod
    def validate_host(cls, v: str) -> str:
        """Validate host is not empty."""
        if not v or not v.strip():
            raise ValueError("Host cannot be empty")
        return v.strip()


# ============================================================================
# Result Models
# ============================================================================


class InitResult(BaseModel):
    """Result from htmlgraph init command.

    Attributes:
        success: Whether initialization succeeded
        graph_dir: Path to initialized .htmlgraph directory
        directories_created: List of directories created
        files_created: List of files created
        hooks_installed: Whether Git hooks were installed
        warnings: List of warning messages
        errors: List of error messages
    """

    success: bool = Field(default=True)
    graph_dir: str = Field(...)
    directories_created: list[str] = Field(default_factory=list)
    files_created: list[str] = Field(default_factory=list)
    hooks_installed: bool = Field(default=False)
    warnings: list[str] = Field(default_factory=list)
    errors: list[str] = Field(default_factory=list)

    @property
    def summary(self) -> str:
        """Human-readable summary of initialization."""
        lines = [f"Initialized {self.graph_dir}"]
        if self.directories_created:
            lines.append(f"  • Created {len(self.directories_created)} directories")
        if self.files_created:
            lines.append(f"  • Created {len(self.files_created)} files")
        if self.hooks_installed:
            lines.append("  • Installed Git hooks")
        if self.warnings:
            lines.append(f"  • {len(self.warnings)} warnings")
        if self.errors:
            lines.append(f"  • {len(self.errors)} errors")
        return "\n".join(lines)


class ValidationResult(BaseModel):
    """Result from directory validation.

    Attributes:
        valid: Whether directory is valid for initialization
        exists: Whether directory already exists
        is_initialized: Whether directory is already initialized
        has_git: Whether directory is in a Git repository
        errors: List of validation errors
    """

    valid: bool = Field(default=True)
    exists: bool = Field(default=False)
    is_initialized: bool = Field(default=False)
    has_git: bool = Field(default=False)
    errors: list[str] = Field(default_factory=list)


# ============================================================================
# Display Models
# ============================================================================


class SessionDisplay(BaseModel):
    """Validated session data for CLI display."""

    id: str = Field(..., description="Session identifier")
    status: str = Field(default="active", description="Session status")
    agent: str = Field(default="unknown", description="Agent name")
    event_count: int = Field(default=0, ge=0, description="Number of events")
    started_at: datetime = Field(..., description="Session start time")
    ended_at: datetime | None = Field(None, description="Session end time")
    title: str | None = Field(None, description="Session title")

    @classmethod
    def from_node(cls, node: object) -> SessionDisplay:
        """
        Create SessionDisplay from graph node.

        Args:
            node: Session node from graph

        Returns:
            SessionDisplay instance
        """
        return cls(
            id=getattr(node, "id"),
            status=getattr(node, "status", "active"),
            agent=getattr(node, "agent", "unknown"),
            event_count=getattr(node, "event_count", 0),
            started_at=getattr(node, "started_at"),
            ended_at=getattr(node, "ended_at", None),
            title=getattr(node, "title", None),
        )

    @property
    def started_str(self) -> str:
        """Formatted start time for display."""
        return self.started_at.strftime("%Y-%m-%d %H:%M")

    @property
    def ended_str(self) -> str | None:
        """Formatted end time for display."""
        if self.ended_at:
            return self.ended_at.strftime("%Y-%m-%d %H:%M")
        return None

    def sort_key(self) -> datetime:
        """
        Return sort key for session ordering.

        Returns timezone-naive datetime for consistent sorting.
        """
        ts = self.started_at
        if ts.tzinfo is None:
            return ts
        return ts.replace(tzinfo=None)


class FeatureDisplay(BaseModel):
    """Validated feature data for CLI display."""

    id: str = Field(..., description="Feature identifier")
    title: str = Field(default="Untitled", description="Feature title")
    status: str = Field(default="unknown", description="Feature status")
    priority: Literal["low", "medium", "high", "critical"] = Field(
        default="medium", description="Feature priority"
    )
    updated: datetime = Field(..., description="Last update time")
    created: datetime | None = Field(None, description="Creation time")
    track_id: str | None = Field(None, description="Associated track ID")
    agent_assigned: str | None = Field(None, description="Assigned agent")

    @classmethod
    def from_node(cls, node: object) -> FeatureDisplay:
        """
        Create FeatureDisplay from graph node.

        Args:
            node: Feature node from graph

        Returns:
            FeatureDisplay instance
        """
        return cls(
            id=getattr(node, "id"),
            title=getattr(node, "title", "Untitled"),
            status=getattr(node, "status", "unknown"),
            priority=getattr(node, "priority", "medium"),
            updated=getattr(node, "updated"),
            created=getattr(node, "created", None),
            track_id=getattr(node, "track_id", None),
            agent_assigned=getattr(node, "agent_assigned", None),
        )

    @property
    def updated_str(self) -> str:
        """Formatted update time for display."""
        return self.updated.strftime("%Y-%m-%d %H:%M")

    @property
    def created_str(self) -> str | None:
        """Formatted creation time for display."""
        if self.created:
            return self.created.strftime("%Y-%m-%d %H:%M")
        return None

    def sort_key(self) -> tuple[int, datetime]:
        """
        Return sort key for feature ordering (priority, then updated time).

        Returns:
            Tuple of (priority_rank, timezone_naive_updated_time)
        """
        priority_order = {"critical": 0, "high": 1, "medium": 2, "low": 3}
        priority_rank = priority_order.get(self.priority, 99)

        updated_ts = self.updated
        if updated_ts.tzinfo is None:
            updated_naive = updated_ts
        else:
            updated_naive = updated_ts.replace(tzinfo=None)

        return (priority_rank, updated_naive)


class TrackDisplay(BaseModel):
    """Validated track data for CLI display."""

    id: str = Field(..., description="Track identifier")
    title: str = Field(default="Untitled", description="Track title")
    status: str = Field(default="planning", description="Track status")
    priority: Literal["low", "medium", "high"] = Field(
        default="medium", description="Track priority"
    )
    has_spec: bool = Field(default=False, description="Has specification")
    has_plan: bool = Field(default=False, description="Has plan")
    format_type: str = Field(
        default="consolidated", description="File format (consolidated or directory)"
    )
    feature_count: int = Field(default=0, ge=0, description="Number of features")

    @classmethod
    def from_track_id(
        cls,
        track_id: str,
        has_spec: bool = False,
        has_plan: bool = False,
        format_type: str = "consolidated",
    ) -> TrackDisplay:
        """
        Create TrackDisplay from track ID and metadata.

        Args:
            track_id: Track identifier
            has_spec: Whether track has specification
            has_plan: Whether track has plan
            format_type: File format type

        Returns:
            TrackDisplay instance
        """
        return cls(
            id=track_id,
            title=track_id,  # Default to ID if title not available
            has_spec=has_spec,
            has_plan=has_plan,
            format_type=format_type,
        )

    @property
    def components_str(self) -> str:
        """Formatted components list for display."""
        components = []
        if self.has_spec:
            components.append("spec")
        if self.has_plan:
            components.append("plan")
        return ", ".join(components) if components else "empty"

    def sort_key(self) -> tuple[int, str]:
        """
        Return sort key for track ordering (priority, then ID).

        Returns:
            Tuple of (priority_rank, track_id)
        """
        priority_order = {"high": 0, "medium": 1, "low": 2}
        priority_rank = priority_order.get(self.priority, 99)
        return (priority_rank, self.id)


class BootstrapConfig(BaseModel):
    """Configuration for htmlgraph bootstrap command.

    Attributes:
        project_path: Directory to bootstrap (default: .)
        no_plugins: Skip plugin installation
    """

    project_path: str = Field(default=".")
    no_plugins: bool = Field(default=False)


# ============================================================================
# Validation Helpers
# ============================================================================


def validate_args(model: type[T], args: Any) -> T:
    """Convert argparse Namespace to validated Pydantic model.

    Args:
        model: Pydantic model class to validate against
        args: argparse.Namespace or dict of arguments

    Returns:
        Validated model instance

    Raises:
        ValidationError: If validation fails

    Example:
        >>> args = parser.parse_args(['--port', '8080'])
        >>> config = validate_args(ServeConfig, args)
        >>> config.port
        8080
    """
    if hasattr(args, "__dict__"):
        # Convert Namespace to dict
        args_dict = vars(args)
    else:
        args_dict = args

    # Filter out None values and command routing fields
    filtered = {
        k: v
        for k, v in args_dict.items()
        if v is not None
        and k
        not in [
            "command",
            "func",
            "feature_command",
            "session_command",
            "track_command",
            "cigs_command",
        ]
    }

    return model(**filtered)


def format_validation_error(error: ValidationError) -> str:
    """Format Pydantic ValidationError for user-friendly CLI output.

    Args:
        error: Pydantic ValidationError

    Returns:
        Formatted error message with field details

    Example:
        >>> try:
        ...     ServeConfig(port=99999)
        ... except ValidationError as e:
        ...     print(format_validation_error(e))
        Validation error:
          • port: Port must be between 1024 and 65535, got 99999
    """
    lines = ["Validation error:"]
    for err in error.errors():
        field = ".".join(str(x) for x in err["loc"])
        msg = err["msg"]
        lines.append(f"  • {field}: {msg}")
    return "\n".join(lines)
