"""
Hook Execution Context Manager.

Manages hook execution context including lazy-loading of expensive resources
(database, session manager) to minimize initialization overhead.

This module provides a centralized context object that hooks can use to:
- Access the graph directory and project directory
- Retrieve session information
- Access the database for event recording
- Perform unified logging

Key Design Principles:
- Lazy-loading: Expensive resources (DB, SessionManager) are only loaded on first access
- Resource cleanup: Context properly closes resources when done
- Type safety: Full type hints for all public methods and properties
- Error handling: Graceful degradation if resources fail to initialize
"""

import logging
import os
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

logger = logging.getLogger(__name__)


@dataclass
class HookContext:
    """
    Hook execution context with lazy-loaded resources.

    Attributes:
        project_dir: Absolute path to project root directory
        graph_dir: Path to .htmlgraph directory for tracking data
        session_id: Unique session identifier for this execution
        agent_id: Agent/tool that's executing (e.g., 'claude-code', 'codex')
        hook_input: Raw hook input data from Claude Code
        model_name: Specific Claude model name (e.g., 'claude-haiku', 'claude-opus', 'claude-sonnet')
        _session_manager: Cached SessionManager instance (lazy-loaded)
        _database: Cached HtmlGraphDB instance (lazy-loaded)
    """

    project_dir: str
    graph_dir: Path
    session_id: str
    agent_id: str
    hook_input: dict[str, Any]
    model_name: str | None = field(default=None, repr=False)
    _session_manager: Any | None = field(default=None, repr=False)
    _database: Any | None = field(default=None, repr=False)

    @classmethod
    def from_input(cls, hook_input: dict[str, Any]) -> "HookContext":
        """
        Create HookContext from raw hook input.

        Performs automatic environment resolution:
        - Extracts session_id from hook_input
        - Detects agent_id from environment or hook_input
        - Detects model_name (e.g., claude-haiku, claude-opus, claude-sonnet)
        - Resolves project directory via bootstrap
        - Initializes graph directory

        Args:
            hook_input: Raw hook input dict from Claude Code hook system

        Returns:
            Initialized HookContext instance

        Raises:
            ImportError: If bootstrap module cannot be imported
            OSError: If graph directory cannot be created

        Example:
            ```python
            hook_input = {
                'session_id': 'sess-abc123',
                'type': 'pretooluse',
                'tool_name': 'Edit',
                ...
            }
            context = HookContext.from_input(hook_input)
            logger.info(f"Session: {context.session_id}, Agent: {context.agent_id}, Model: {context.model_name}")
            ```
        """
        # Import bootstrap locally to avoid circular imports
        from htmlgraph.hooks.bootstrap import (
            get_graph_dir,
            resolve_project_dir,
        )

        # Resolve project directory first
        project_dir = resolve_project_dir()
        graph_dir = get_graph_dir(project_dir)

        # Extract session ID with multiple fallbacks
        # Priority order:
        # 1. hook_input["session_id"] (if Claude Code passes it)
        # 2. hook_input["sessionId"] (camelCase variant)
        # 3. HTMLGRAPH_SESSION_ID environment variable
        # 4. CLAUDE_SESSION_ID environment variable
        # 5. Most recent active session from database (NEW)
        # 6. "unknown" as last resort
        #
        # NOTE: We intentionally do NOT use SessionManager.get_active_session()
        # as a fallback because the "active session" is stored in a global file
        # (.htmlgraph/session.json) that's shared across all Claude windows.
        # Using it would cause cross-window event contamination where tool calls
        # from Window B get linked to UserQuery events from Window A.
        #
        # However, we DO query the database by status='active' and created_at,
        # which is different because it retrieves the most recent session that
        # was explicitly marked as active (e.g., by SessionStart hook), without
        # relying on a shared global agent state file.
        session_id = (
            hook_input.get("session_id")
            or hook_input.get("sessionId")
            or os.environ.get("HTMLGRAPH_SESSION_ID")
            or os.environ.get("CLAUDE_SESSION_ID")
        )

        # Fallback: Query database for session with most recent UserQuery event
        # This solves the issue where PostToolUse hooks don't receive session_id
        # in hook_input. UserPromptSubmit hooks DO receive it and create UserQuery
        # events with the correct session_id, so we use that as the source of truth.
        if not session_id:
            db_path = graph_dir / "htmlgraph.db"
            if db_path.exists():
                try:
                    import sqlite3

                    conn = sqlite3.connect(str(db_path), timeout=1.0)
                    cursor = conn.cursor()
                    cursor.execute("""
                        SELECT session_id FROM agent_events
                        WHERE tool_name = 'UserQuery'
                        ORDER BY datetime(REPLACE(SUBSTR(timestamp, 1, 19), 'T', ' ')) DESC
                        LIMIT 1
                    """)
                    row = cursor.fetchone()
                    conn.close()
                    if row:
                        session_id = row[0]
                        logger.info(f"Resolved session_id from database: {session_id}")
                except Exception as e:
                    logger.warning(f"Failed to query active session from database: {e}")

            # Final fallback to "unknown" if database query fails
            if not session_id:
                session_id = "unknown"
                logger.warning(
                    "Could not resolve session_id from hook_input, environment, or database. "
                    "Events will not be linked to parent UserQuery. "
                    "For multi-window support, set HTMLGRAPH_SESSION_ID env var."
                )

        # Detect agent ID (priority order)
        # 1. Explicit agent_id in hook input
        # 2. HTMLGRAPH_AGENT_ID environment variable
        # 3. CLAUDE_AGENT_NICKNAME environment variable (Claude Code)
        # 4. Default to 'claude-code'
        agent_id = (
            hook_input.get("agent_id")
            or os.environ.get("HTMLGRAPH_AGENT_ID")
            or os.environ.get("CLAUDE_AGENT_NICKNAME", "claude-code")
        )

        # Detect model name (priority order)
        # 1. Explicit model_name in hook input
        # 2. CLAUDE_MODEL environment variable
        # 3. HTMLGRAPH_MODEL environment variable
        # 4. Status line cache (from ~/.cache/claude-code/status-{session_id}.json)
        # 5. None (not available)
        model_name = (
            hook_input.get("model_name")
            or hook_input.get("model")
            or os.environ.get("CLAUDE_MODEL")
            or os.environ.get("HTMLGRAPH_MODEL")
        )

        # Fallback: Try status line cache if model not detected yet
        if not model_name and session_id and session_id != "unknown":
            from htmlgraph.hooks.event_tracker import get_model_from_status_cache

            model_name = get_model_from_status_cache(session_id)

        logger.info(
            f"Initializing hook context: session={session_id}, "
            f"agent={agent_id}, model={model_name}, project={project_dir}"
        )

        return cls(
            project_dir=project_dir,
            graph_dir=graph_dir,
            session_id=session_id,
            agent_id=agent_id,
            hook_input=hook_input,
            model_name=model_name,
        )

    @property
    def session_manager(self) -> Any:
        """
        Lazy-load and cache SessionManager instance.

        Importing SessionManager is expensive (thousands of file system operations
        for graph initialization), so we defer until first access.

        Returns:
            SessionManager instance for session tracking and activity attribution

        Raises:
            ImportError: If SessionManager cannot be imported
            Exception: If SessionManager initialization fails

        Note:
            SessionManager is cached after first access. Multiple accesses
            return the same instance.
        """
        if self._session_manager is not None:
            return self._session_manager

        try:
            from htmlgraph.session_manager import SessionManager

            logger.debug(f"Loading SessionManager for {self.graph_dir}")
            self._session_manager = SessionManager(graph_dir=self.graph_dir)
            logger.info("SessionManager loaded successfully")
            return self._session_manager
        except ImportError as e:
            logger.error(f"Failed to import SessionManager: {e}")
            raise
        except Exception as e:
            logger.error(f"Failed to initialize SessionManager: {e}")
            raise

    @property
    def database(self) -> Any:
        """
        Lazy-load and cache HtmlGraphDB instance.

        Database access is needed for event recording, but we defer initialization
        until first access to minimize startup overhead.

        Returns:
            HtmlGraphDB instance for recording events and features

        Raises:
            ImportError: If HtmlGraphDB cannot be imported
            Exception: If database connection fails

        Note:
            Database connection is cached after first access. Multiple accesses
            return the same instance.
        """
        if self._database is not None:
            return self._database

        try:
            from htmlgraph.db.schema import HtmlGraphDB

            db_path = self.graph_dir / "htmlgraph.db"
            logger.debug(f"Loading HtmlGraphDB at {db_path}")
            self._database = HtmlGraphDB(str(db_path))
            logger.info("HtmlGraphDB loaded successfully")
            return self._database
        except ImportError as e:
            logger.error(f"Failed to import HtmlGraphDB: {e}")
            raise
        except Exception as e:
            logger.error(f"Failed to initialize HtmlGraphDB: {e}")
            raise

    def close(self) -> None:
        """
        Clean up and close all resources gracefully.

        Closes database connections and session manager resources.
        Safe to call multiple times (idempotent).

        This should be called in a finally block to ensure cleanup:

        Example:
            ```python
            context = HookContext.from_input(hook_input)
            try:
                # Use context
                context.session_manager.track_activity(...)
            finally:
                context.close()  # Always cleanup
            ```
        """
        # Close database if loaded
        if self._database is not None:
            try:
                logger.debug("Closing database connection")
                self._database.close()
                self._database = None
                logger.info("Database closed successfully")
            except Exception as e:
                logger.warning(f"Error closing database: {e}")

        # Close session manager if loaded
        if self._session_manager is not None:
            try:
                logger.debug("Closing session manager")
                # SessionManager doesn't currently have a close method,
                # but we keep this for future resource management
                self._session_manager = None
                logger.info("Session manager cleaned up")
            except Exception as e:
                logger.warning(f"Error closing session manager: {e}")

    def log(self, level: str, message: str) -> None:
        """
        Unified logging for hooks.

        Provides consistent logging across all hook modules with context
        information (session_id, agent_id, project_dir).

        Args:
            level: Log level as string ('debug', 'info', 'warning', 'error', 'critical')
            message: Message to log

        Example:
            ```python
            context.log('info', 'Processing user query')
            context.log('error', f'Failed to track activity: {error}')
            ```
        """
        log_func = getattr(logger, level.lower(), logger.info)

        # Prefix message with context for better debugging
        context_msg = f"[{self.session_id[:8]}][{self.agent_id}] {message}"
        log_func(context_msg)

    def __enter__(self) -> "HookContext":
        """Context manager entry."""
        return self

    def __exit__(self, exc_type: Any, exc_val: Any, exc_tb: Any) -> None:
        """Context manager exit with resource cleanup."""
        self.close()


__all__ = [
    "HookContext",
]
