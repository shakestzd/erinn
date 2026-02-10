"""
SessionStart Hook Integration - Initialize session registry with repo awareness.

Integrates:
- SessionRegistry: File-based session tracking
- RepoHash: Git awareness and repository identification
- AtomicFileWriter: Crash-safe writes

Called by SessionStart hook to:
1. Register new session with repo info
2. Export session IDs to CLAUDE_ENV_FILE
3. Detect parent sessions from environment
4. Initialize heartbeat mechanism
"""

import logging
import os
import uuid
from datetime import datetime, timezone
from pathlib import Path

logger = logging.getLogger(__name__)


def initialize_session_from_hook(env_file: str | None = None) -> str:
    """
    Initialize session registry from SessionStart hook.

    Called automatically by SessionStart hook to set up session tracking.
    Registers session with repository information and exports environment variables.

    Args:
        env_file: Path to CLAUDE_ENV_FILE (from hook environment).
                 Allows exporting session IDs to parent process.

    Returns:
        session_id: Generated session ID for logging/tracking

    Raises:
        OSError: If registry initialization fails
        RuntimeError: If session registration fails
    """
    from htmlgraph.repo_hash import RepoHash
    from htmlgraph.session_registry import SessionRegistry

    try:
        # Initialize registry (creates .htmlgraph/sessions/registry structure)
        registry = SessionRegistry()
        logger.debug(f"Initialized SessionRegistry at {registry.registry_dir}")

        # Get repo information
        try:
            repo_hash = RepoHash()
            git_info = repo_hash.get_git_info()
            repo_hash_value = repo_hash.compute_repo_hash()

            repo_info = {
                "path": str(repo_hash.repo_path),
                "hash": repo_hash_value,
                "branch": git_info.get("branch"),
                "commit": git_info.get("commit"),
                "remote": git_info.get("remote"),
                "dirty": git_info.get("dirty", False),
                "is_monorepo": repo_hash.is_monorepo(),
                "monorepo_project": repo_hash.get_monorepo_project(),
            }
            logger.debug(f"Repo info: {repo_info}")
        except OSError as e:
            logger.warning(f"Failed to get repo info: {e}")
            repo_info = {
                "path": str(Path.cwd()),
                "hash": "unknown",
                "branch": None,
                "commit": None,
                "remote": None,
                "dirty": False,
                "is_monorepo": False,
                "monorepo_project": None,
            }

        # Get instance information
        instance_info = {
            "pid": os.getpid(),
            "hostname": _get_hostname(),
            "start_time": _get_utc_timestamp(),
        }

        # Generate session ID
        session_id = f"sess-{uuid.uuid4().hex[:8]}"

        # Register session atomically
        try:
            registry_file = registry.register_session(
                session_id=session_id,
                repo_info=repo_info,
                instance_info=instance_info,
            )
            logger.info(f"Registered session {session_id} at {registry_file}")
        except OSError as e:
            logger.error(f"Failed to register session: {e}")
            raise RuntimeError(f"Session registration failed: {e}") from e

        # Export to CLAUDE_ENV_FILE if provided
        if env_file:
            try:
                _export_to_env_file(
                    env_file=env_file,
                    session_id=session_id,
                    instance_id=registry.get_instance_id(),
                    repo_hash=repo_hash_value
                    if "repo_hash_value" in locals()
                    else "unknown",
                )
                logger.debug(f"Exported session environment to {env_file}")
            except OSError as e:
                logger.warning(f"Failed to export environment: {e}")
                # Don't fail - session is registered even if export fails

        # Check for parent session
        parent_session_id = _get_parent_session_id()
        if parent_session_id:
            logger.info(f"Parent session detected: {parent_session_id}")
            # Store parent relationship in environment for subprocesses
            os.environ["HTMLGRAPH_PARENT_SESSION_ID"] = parent_session_id

        return session_id

    except Exception as e:
        logger.error(f"Failed to initialize session: {e}", exc_info=True)
        raise


def finalize_session(session_id: str, status: str = "completed") -> bool:
    """
    Finalize session (called by SessionEnd hook).

    Archives the session and updates last activity timestamp.

    Args:
        session_id: Session ID to finalize
        status: Final status (ended, failed, etc.)

    Returns:
        True if finalization succeeded, False otherwise
    """
    from htmlgraph.session_registry import SessionRegistry

    try:
        registry = SessionRegistry()
        instance_id = registry.get_instance_id()

        # Archive the session
        success = registry.archive_session(instance_id)
        if success:
            logger.info(f"Archived session {session_id} (status: {status})")
        else:
            logger.warning(f"Failed to archive session {session_id}")

        return success
    except Exception as e:
        logger.error(f"Failed to finalize session {session_id}: {e}")
        return False


def heartbeat(session_id: str | None = None) -> bool:
    """
    Update session activity timestamp (liveness heartbeat).

    Called periodically to indicate the session is still active.
    Should be called on each tool use or periodically (e.g., every 5 minutes).

    Args:
        session_id: Optional session ID (uses current instance if None)

    Returns:
        True if heartbeat succeeded, False otherwise
    """
    from htmlgraph.session_registry import SessionRegistry

    try:
        registry = SessionRegistry()
        instance_id = registry.get_instance_id()

        success = registry.update_activity(instance_id)
        if success:
            logger.debug(f"Updated activity for instance {instance_id}")
        else:
            logger.warning(f"Failed to update activity for instance {instance_id}")

        return success
    except Exception as e:
        logger.error(f"Failed to update activity: {e}")
        return False


def get_current_session() -> dict | None:
    """
    Get current session for this instance.

    Returns the registration data for the current instance's session.

    Returns:
        Session dict with instance_id, session_id, repo, etc., or None if not found
    """
    from htmlgraph.session_registry import SessionRegistry

    try:
        registry = SessionRegistry()
        instance_id = registry.get_instance_id()
        session = registry.read_session(instance_id)
        return session
    except Exception as e:
        logger.error(f"Failed to get current session: {e}")
        return None


def get_parent_session_id() -> str | None:
    """
    Get parent session ID if this is a spawned task.

    Returns:
        Parent session ID from environment, or None if not a spawned task
    """
    return _get_parent_session_id()


# Private helpers


def _get_hostname() -> str:
    """Get hostname safely."""
    try:
        import socket

        return socket.gethostname()
    except Exception:
        return "unknown"


def _get_utc_timestamp() -> str:
    """Get current UTC timestamp in ISO 8601 format."""
    now = datetime.now(timezone.utc)
    return now.strftime("%Y-%m-%dT%H:%M:%S.%fZ")


def _export_to_env_file(
    env_file: str,
    session_id: str,
    instance_id: str,
    repo_hash: str,
) -> None:
    """
    Export session environment variables to CLAUDE_ENV_FILE.

    Appends to the file to preserve any existing variables.

    Args:
        env_file: Path to environment file
        session_id: Session ID to export
        instance_id: Instance ID to export
        repo_hash: Repository hash to export

    Raises:
        OSError: If file write fails
    """
    env_path = Path(env_file)

    try:
        # Append to environment file
        with open(env_path, "a") as f:
            f.write(f"export HTMLGRAPH_SESSION_ID={session_id}\n")
            f.write(f"export HTMLGRAPH_INSTANCE_ID={instance_id}\n")
            f.write(f"export HTMLGRAPH_REPO_HASH={repo_hash}\n")

        logger.debug(f"Exported environment variables to {env_file}")
    except OSError as e:
        logger.error(f"Failed to export environment to {env_file}: {e}")
        raise


def _get_parent_session_id() -> str | None:
    """
    Detect parent session from environment.

    Checks:
    1. HTMLGRAPH_PARENT_SESSION_ID env var (set by Task spawning)
    2. HTMLGRAPH_PARENT_SESSION env var (alternate name)

    Returns:
        Parent session ID if found, None otherwise
    """
    parent_id = os.environ.get("HTMLGRAPH_PARENT_SESSION_ID")
    if parent_id:
        return parent_id

    parent_id = os.environ.get("HTMLGRAPH_PARENT_SESSION")
    if parent_id:
        return parent_id

    return None
