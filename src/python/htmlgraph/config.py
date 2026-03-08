"""
HtmlGraph Configuration Management.

This module provides centralized configuration management using Pydantic Settings,
allowing configuration from environment variables, .env files, and CLI arguments.

IMPORTANT: Database path functions (get_database_path, get_analytics_cache_path)
are lightweight and have NO dependencies. They can be imported anywhere.
"""

import os
from pathlib import Path
from typing import Any

# =============================================================================
# LIGHTWEIGHT DATABASE PATH FUNCTIONS (NO DEPENDENCIES)
# These MUST come before any heavy imports so spawners can use them
# =============================================================================

# Database filenames (SINGLE SOURCE OF TRUTH)
DATABASE_FILENAME = "htmlgraph.db"  # Unified event database
ANALYTICS_CACHE_FILENAME = "index.sqlite"  # Analytics cache (rebuildable)


def get_database_path(project_root: Path | str | None = None) -> Path:
    """
    Get the unified database path for event tracking.

    This is the SINGLE source of truth for database location.
    All hooks, agents, and spawners MUST use this function.

    Args:
        project_root: Optional project root path. If None, uses HTMLGRAPH_PROJECT_ROOT
                     env var or current working directory.

    Returns:
        Path to htmlgraph.db (the unified event database)
    """
    if project_root is None:
        project_root = Path(os.environ.get("HTMLGRAPH_PROJECT_ROOT", os.getcwd()))
    else:
        project_root = Path(project_root)

    return project_root / ".htmlgraph" / DATABASE_FILENAME


def get_analytics_cache_path(project_root: Path | str | None = None) -> Path:
    """
    Get the analytics cache database path.

    This is for read-only analytics queries (rebuildable from events).
    NOT for event tracking - use get_database_path() for that.

    Args:
        project_root: Optional project root path. If None, uses HTMLGRAPH_PROJECT_ROOT
                     env var or current working directory.

    Returns:
        Path to index.sqlite (analytics cache, gitignored)
    """
    if project_root is None:
        project_root = Path(os.environ.get("HTMLGRAPH_PROJECT_ROOT", os.getcwd()))
    else:
        project_root = Path(project_root)

    return project_root / ".htmlgraph" / ANALYTICS_CACHE_FILENAME


# =============================================================================
# PYDANTIC CONFIGURATION (Heavy imports below - spawners don't need this)
# =============================================================================

from pydantic_settings import BaseSettings


class HtmlGraphConfig(BaseSettings):
    """Global HtmlGraph configuration using Pydantic Settings.

    Configuration can be provided via:
    1. Environment variables (prefix: HTMLGRAPH_)
    2. .env file
    3. Direct instantiation with parameters
    4. CLI argument overrides
    """

    # Core paths
    graph_dir: Path = Path.home() / ".htmlgraph"

    # Database paths (SINGLE SOURCE OF TRUTH)
    # All hooks, agents, and spawners MUST use these via get_database_path()
    database_filename: str = "htmlgraph.db"  # Unified event database
    analytics_cache_filename: str = "index.sqlite"  # Analytics cache (rebuildable)

    # Feature tracking
    features_dir: Path | None = None
    sessions_dir: Path | None = None
    spikes_dir: Path | None = None
    tracks_dir: Path | None = None
    archives_dir: Path | None = None

    # CLI behavior
    debug: bool = False
    verbose: bool = False
    auto_sync: bool = True
    color_output: bool = True

    # Session management
    max_sessions: int = 100
    session_retention_days: int = 30
    auto_archive_sessions: bool = True

    # Performance
    max_query_results: int = 1000
    cache_enabled: bool = True
    cache_ttl_seconds: int = 3600

    # Logging
    log_level: str = "INFO"
    log_file: Path | None = None

    model_config = {
        "env_prefix": "HTMLGRAPH_",
        "env_file": ".env",
        "case_sensitive": False,
        "extra": "ignore",
    }

    def __init__(self, **data: Any) -> None:
        """Initialize config and compute derived paths."""
        super().__init__(**data)
        # Compute derived paths if not explicitly set
        if self.features_dir is None:
            self.features_dir = self.graph_dir / "features"
        if self.sessions_dir is None:
            self.sessions_dir = self.graph_dir / "sessions"
        if self.spikes_dir is None:
            self.spikes_dir = self.graph_dir / "spikes"
        if self.tracks_dir is None:
            self.tracks_dir = self.graph_dir / "tracks"
        if self.archives_dir is None:
            self.archives_dir = self.graph_dir / "archives"

    def ensure_directories(self) -> None:
        """Create all configured directories if they don't exist."""
        for directory in [
            self.graph_dir,
            self.features_dir,
            self.sessions_dir,
            self.spikes_dir,
            self.tracks_dir,
            self.archives_dir,
        ]:
            if directory:
                directory.mkdir(parents=True, exist_ok=True)

    def get_config_dict(self) -> dict[str, Any]:
        """Get configuration as dictionary."""
        return {
            "graph_dir": str(self.graph_dir),
            "features_dir": str(self.features_dir),
            "sessions_dir": str(self.sessions_dir),
            "spikes_dir": str(self.spikes_dir),
            "tracks_dir": str(self.tracks_dir),
            "archives_dir": str(self.archives_dir),
            "debug": self.debug,
            "verbose": self.verbose,
            "auto_sync": self.auto_sync,
            "color_output": self.color_output,
            "max_sessions": self.max_sessions,
            "session_retention_days": self.session_retention_days,
            "auto_archive_sessions": self.auto_archive_sessions,
            "max_query_results": self.max_query_results,
            "cache_enabled": self.cache_enabled,
            "cache_ttl_seconds": self.cache_ttl_seconds,
            "log_level": self.log_level,
            "log_file": str(self.log_file) if self.log_file else None,
        }


# Global configuration instance
config: HtmlGraphConfig = HtmlGraphConfig()
