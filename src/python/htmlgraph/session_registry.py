"""
Session File Registry - Core file-based session tracking system.

Manages the session registry for parallel Claude instance support with:
- Per-instance registration files (atomic write, no locks needed)
- Index file for fast lookups
- Archive support for completed sessions
- Heartbeat tracking for liveness detection

Architecture:
    .htmlgraph/sessions/
    ├── registry/
    │   ├── active/
    │   │   ├── {instance_id}.json   # One file per Claude instance
    │   │   └── ...
    │   ├── .index.json              # Fast lookup index
    │   └── archive/                 # Archived session registrations
    │       └── {instance_id}.json
    ├── {session_id}.html            # Session data files
    └── _archive/
        └── {year}/{month}/
            ├── {session_id}.html
            └── ...

Data Formats:

Instance Registration File (.htmlgraph/sessions/registry/active/{instance_id}.json):
    {
        "instance_id": "inst-12345-hostname-1234567890",
        "session_id": "sess-abc123",
        "created": "2026-01-08T12:34:56Z",
        "repo": {
            "path": "/Users/shakes/DevProjects/htmlgraph",
            "remote": "https://github.com/user/htmlgraph.git",
            "branch": "main",
            "commit": "d78e458"
        },
        "instance": {
            "pid": 12345,
            "hostname": "hostname",
            "start_time": "2026-01-08T12:34:56Z"
        },
        "status": "active",
        "last_activity": "2026-01-08T12:35:10Z"
    }

Index File (.htmlgraph/sessions/registry/.index.json):
    {
        "version": "1.0",
        "updated_at": "2026-01-08T12:35:10Z",
        "active_sessions": {
            "sess-abc123": {
                "instance_id": "inst-12345-hostname-1234567890",
                "created": "2026-01-08T12:34:56Z",
                "last_activity": "2026-01-08T12:35:10Z"
            }
        }
    }
"""

import json
import logging
import os
import socket
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, cast

logger = logging.getLogger(__name__)

# Module-level cache: maps PID -> integer start timestamp (seconds).
# Populated on first call to get_instance_id() for a given PID so that the
# instance ID string is identical across all calls within the same process,
# regardless of wall-clock seconds ticking over.
_INSTANCE_START_TIMES: dict[int, int] = {}


class SessionRegistry:
    """
    Manages session file registry for parallel instance support.

    Provides atomic file operations, instance tracking, and index management
    without requiring locks or external dependencies.

    Attributes:
        registry_dir: Path to the registry directory (.htmlgraph/sessions/registry)
    """

    # Default registry location relative to working directory
    DEFAULT_REGISTRY_SUBPATH = ".htmlgraph/sessions/registry"

    # Index file name
    INDEX_FILE = ".index.json"

    # Subdirectories
    ACTIVE_DIR = "active"
    ARCHIVE_DIR = "archive"

    def __init__(self, registry_dir: Path | None = None):
        """
        Initialize registry with custom or default directory.

        Args:
            registry_dir: Optional custom registry directory path.
                         Defaults to .htmlgraph/sessions/registry in current directory.

        Raises:
            OSError: If directory creation fails due to permission issues.
        """
        if registry_dir is None:
            registry_dir = Path.cwd() / self.DEFAULT_REGISTRY_SUBPATH
        else:
            registry_dir = Path(registry_dir)

        self.registry_dir = registry_dir
        self.active_dir = self.registry_dir / self.ACTIVE_DIR
        self.archive_dir = self.registry_dir / self.ARCHIVE_DIR
        self.index_file = self.registry_dir / self.INDEX_FILE

        # Create directory structure if missing
        self._ensure_directories()

    def _ensure_directories(self) -> None:
        """
        Create registry directory structure if it doesn't exist.

        Creates:
        - registry_dir
        - registry_dir/active
        - registry_dir/archive

        Raises:
            OSError: If directory creation fails.
        """
        try:
            self.registry_dir.mkdir(parents=True, exist_ok=True)
            self.active_dir.mkdir(parents=True, exist_ok=True)
            self.archive_dir.mkdir(parents=True, exist_ok=True)
        except OSError as e:
            logger.error(f"Failed to create registry directories: {e}")
            raise

    def get_instance_id(self) -> str:
        """
        Get unique instance ID for this process.

        Generates a stable instance ID based on:
        - Process ID (PID)
        - Hostname
        - Start timestamp (seconds since epoch, captured once per process)

        Format: inst-{pid}-{hostname}-{timestamp}

        The ID is stable for the lifetime of this process (same PID always
        generates the same ID). Different processes always get different IDs.

        The timestamp is cached at first call so that repeated calls within
        the same process always return the identical string, even if wall-clock
        seconds tick over between the registration and a later heartbeat call.

        Returns:
            Unique instance identifier string.

        Example:
            >>> registry = SessionRegistry()
            >>> instance_id = registry.get_instance_id()
            >>> instance_id
            'inst-12345-hostname-1234567890'
        """
        pid = os.getpid()
        # Use the per-process cached timestamp so the ID is truly stable
        # across multiple calls within the same process (prevents flaky
        # heartbeat failures when a wall-clock second ticks over between
        # register_session() and update_activity()).
        if pid not in _INSTANCE_START_TIMES:
            _INSTANCE_START_TIMES[pid] = int(time.time())
        start_time = _INSTANCE_START_TIMES[pid]

        hostname = socket.gethostname()
        return f"inst-{pid}-{hostname}-{start_time}"

    def register_session(
        self,
        session_id: str,
        repo_info: dict[str, Any],
        instance_info: dict[str, Any],
    ) -> Path:
        """
        Register new session, return registry file path.

        Creates a registration file in .htmlgraph/sessions/registry/active/
        and updates the index file atomically.

        Args:
            session_id: Unique session identifier (e.g., "sess-abc123")
            repo_info: Repository information dict with keys:
                - path: str (repository path)
                - remote: str (remote URL)
                - branch: str (current branch)
                - commit: str (current commit hash)
            instance_info: Instance information dict with keys:
                - pid: int (process ID)
                - hostname: str (machine hostname)
                - start_time: str (ISO 8601 timestamp)

        Returns:
            Path to the created registration file.

        Raises:
            OSError: If file write fails.
            ValueError: If session_id is empty or invalid.

        Example:
            >>> registry = SessionRegistry()
            >>> repo_info = {
            ...     "path": "/path/to/repo",
            ...     "remote": "https://github.com/user/repo.git",
            ...     "branch": "main",
            ...     "commit": "abc123"
            ... }
            >>> instance_info = {
            ...     "pid": 12345,
            ...     "hostname": "myhost",
            ...     "start_time": "2026-01-08T12:34:56Z"
            ... }
            >>> path = registry.register_session("sess-abc123", repo_info, instance_info)
            >>> path.exists()
            True
        """
        if not session_id or not isinstance(session_id, str):
            raise ValueError(f"Invalid session_id: {session_id}")

        instance_id = self.get_instance_id()
        now = self._get_utc_timestamp()

        registration = {
            "instance_id": instance_id,
            "session_id": session_id,
            "created": now,
            "repo": repo_info,
            "instance": instance_info,
            "status": "active",
            "last_activity": now,
        }

        # Write registration file
        reg_file = self.active_dir / f"{instance_id}.json"
        self._write_atomic(reg_file, registration)

        # Update index
        self._update_index(session_id, instance_id, now)

        logger.info(
            f"Registered session {session_id} with instance {instance_id} at {reg_file}"
        )

        return reg_file

    def get_current_sessions(self) -> list[dict[str, Any]]:
        """
        Get all active session registrations.

        Reads all JSON files in the active/ directory and returns their contents.
        Handles and logs parsing errors gracefully.

        Returns:
            List of session registration dicts, each containing:
            - instance_id: str
            - session_id: str
            - created: str (ISO 8601)
            - repo: dict
            - instance: dict
            - status: str
            - last_activity: str (ISO 8601)

        Example:
            >>> registry = SessionRegistry()
            >>> sessions = registry.get_current_sessions()
            >>> len(sessions)
            2
            >>> sessions[0]["session_id"]
            'sess-abc123'
        """
        sessions: list[dict[str, Any]] = []

        if not self.active_dir.exists():
            return sessions

        try:
            for reg_file in self.active_dir.glob("*.json"):
                try:
                    session = self._read_json(reg_file)
                    if session:
                        sessions.append(session)
                except (json.JSONDecodeError, OSError) as e:
                    logger.warning(f"Failed to read registration {reg_file}: {e}")
                    continue
        except OSError as e:
            logger.warning(f"Failed to list active registrations: {e}")

        return sessions

    def read_session(self, instance_id: str) -> dict[str, Any] | None:
        """
        Read specific session registration.

        Args:
            instance_id: Instance identifier to read.

        Returns:
            Session registration dict if found, None otherwise.

        Example:
            >>> registry = SessionRegistry()
            >>> session = registry.read_session("inst-12345-hostname-1234567890")
            >>> session is not None
            True
            >>> session["session_id"]
            'sess-abc123'
        """
        reg_file = self.active_dir / f"{instance_id}.json"

        if not reg_file.exists():
            logger.debug(f"Registration file not found: {reg_file}")
            return None

        try:
            return self._read_json(reg_file)
        except (json.JSONDecodeError, OSError) as e:
            logger.error(f"Failed to read registration {reg_file}: {e}")
            return None

    def update_activity(self, instance_id: str) -> bool:
        """
        Update last_activity timestamp for heartbeat.

        Updates the last_activity field in the registration file to current time.
        Used to indicate that the session is still active (liveness heartbeat).

        Args:
            instance_id: Instance identifier to update.

        Returns:
            True if update succeeded, False otherwise.

        Example:
            >>> registry = SessionRegistry()
            >>> success = registry.update_activity("inst-12345-hostname-1234567890")
            >>> success
            True
        """
        session = self.read_session(instance_id)
        if not session:
            logger.warning(f"Cannot update activity: session {instance_id} not found")
            return False

        try:
            session["last_activity"] = self._get_utc_timestamp()
            reg_file = self.active_dir / f"{instance_id}.json"
            self._write_atomic(reg_file, session)

            # Update index
            self._update_index(
                session["session_id"], instance_id, session["last_activity"]
            )

            logger.debug(f"Updated activity for instance {instance_id}")
            return True
        except OSError as e:
            logger.error(f"Failed to update activity for {instance_id}: {e}")
            return False

    def archive_session(self, instance_id: str) -> bool:
        """
        Move session from active to archive.

        Reads the active registration, writes it to archive directory,
        and removes the active registration.

        Args:
            instance_id: Instance identifier to archive.

        Returns:
            True if archival succeeded, False otherwise.

        Example:
            >>> registry = SessionRegistry()
            >>> success = registry.archive_session("inst-12345-hostname-1234567890")
            >>> success
            True
            >>> # File now in archive/
            >>> (registry.archive_dir / f"{instance_id}.json").exists()
            True
        """
        active_file = self.active_dir / f"{instance_id}.json"

        if not active_file.exists():
            logger.warning(f"Cannot archive: registration {instance_id} not found")
            return False

        try:
            session = self._read_json(active_file)
            if not session:
                return False

            # Write to archive
            archive_file = self.archive_dir / f"{instance_id}.json"
            self._write_atomic(archive_file, session)

            # Remove from active
            active_file.unlink()

            # Update index
            self._remove_from_index(session["session_id"])

            logger.info(
                f"Archived session {session['session_id']} (instance {instance_id})"
            )
            return True
        except OSError as e:
            logger.error(f"Failed to archive session {instance_id}: {e}")
            return False

    def get_session_file_path(self, instance_id: str) -> Path:
        """
        Get file path for session registration.

        Returns the path where the registration file should be stored.
        Does not verify if the file exists.

        Args:
            instance_id: Instance identifier.

        Returns:
            Path to the registration file.

        Example:
            >>> registry = SessionRegistry()
            >>> path = registry.get_session_file_path("inst-12345-hostname-1234567890")
            >>> str(path)
            '.htmlgraph/sessions/registry/active/inst-12345-hostname-1234567890.json'
        """
        return self.active_dir / f"{instance_id}.json"

    # Private helper methods

    @staticmethod
    def _get_utc_timestamp() -> str:
        """
        Get current UTC timestamp in ISO 8601 format.

        Returns:
            Timestamp string (e.g., "2026-01-08T12:34:56.123456Z")
        """
        now = datetime.now(timezone.utc)
        return now.strftime("%Y-%m-%dT%H:%M:%S.%fZ")

    @staticmethod
    def _write_atomic(path: Path, data: dict[str, Any]) -> None:
        """
        Atomic file write using temp file + rename pattern.

        Ensures:
        - No partial writes visible to readers
        - No corruption from concurrent writes
        - Crash-safe (either old or new content, never mixed)

        Args:
            path: File path to write to.
            data: Data dict to write as JSON.

        Raises:
            OSError: If write or rename fails.
        """
        pid = os.getpid()
        temp_path = Path(f"{path}.{pid}.tmp")

        try:
            # Write to temp file
            with open(temp_path, "w") as f:
                json.dump(data, f, indent=2)
                f.flush()
                os.fsync(f.fileno())  # Ensure written to disk

            # Atomic rename
            temp_path.replace(path)
        except OSError as e:
            # Clean up temp file on failure
            try:
                temp_path.unlink(missing_ok=True)
            except OSError:
                pass
            raise e

    @staticmethod
    def _read_json(path: Path) -> dict[str, Any] | None:
        """
        Read JSON file with retry for transient failures.

        Handles file-not-found gracefully (returns None).
        Retries on JSON decode errors in case file is mid-write.

        Args:
            path: Path to JSON file.

        Returns:
            Parsed dict if successful, None if file not found or unrecoverable.

        Raises:
            OSError: For non-transient I/O errors.
        """
        max_retries = 3

        for attempt in range(max_retries):
            try:
                with open(path) as f:
                    data = json.load(f)
                    return cast(dict[str, Any], data)
            except FileNotFoundError:
                return None
            except json.JSONDecodeError:
                # File might be mid-write, retry with backoff
                if attempt < max_retries - 1:
                    time.sleep(0.1 * (attempt + 1))
                else:
                    logger.error(
                        f"Failed to parse JSON after {max_retries} retries: {path}"
                    )
                    return None
            except OSError as e:
                # Non-transient error
                raise e

        return None

    def _update_index(self, session_id: str, instance_id: str, timestamp: str) -> None:
        """
        Update index file with session information.

        Atomically updates the index file to include/update the session entry.

        Args:
            session_id: Session identifier.
            instance_id: Instance identifier.
            timestamp: ISO 8601 timestamp of last activity.

        Raises:
            OSError: If index update fails.
        """
        index_data = self._read_json(self.index_file) or {
            "version": "1.0",
            "updated_at": self._get_utc_timestamp(),
            "active_sessions": {},
        }

        # Update session entry
        if "active_sessions" not in index_data:
            index_data["active_sessions"] = {}

        index_data["active_sessions"][session_id] = {
            "instance_id": instance_id,
            "created": self._get_utc_timestamp(),
            "last_activity": timestamp,
        }

        # Update timestamp
        index_data["updated_at"] = self._get_utc_timestamp()

        # Write atomically
        try:
            self._write_atomic(self.index_file, index_data)
        except OSError as e:
            logger.error(f"Failed to update index file: {e}")
            raise

    def _remove_from_index(self, session_id: str) -> None:
        """
        Remove session entry from index file.

        Args:
            session_id: Session identifier to remove.

        Raises:
            OSError: If index update fails.
        """
        index_data = self._read_json(self.index_file)
        if not index_data:
            return

        if (
            "active_sessions" in index_data
            and session_id in index_data["active_sessions"]
        ):
            del index_data["active_sessions"][session_id]
            index_data["updated_at"] = self._get_utc_timestamp()

            try:
                self._write_atomic(self.index_file, index_data)
            except OSError as e:
                logger.error(f"Failed to update index file: {e}")
                raise
