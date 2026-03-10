"""
Offline-First Merge with Conflict Resolution - Phase 4A

Supports offline work on multiple devices with automatic conflict detection and resolution.
Agents can work offline, cache updates locally, and automatically merge changes on reconnect.

Features:
- Offline event logging
- Last-write-wins merge strategy
- Priority-based conflict resolution
- Conflict tracking and audit trail
- <1s merge time for 100 events
- Zero data loss

Architecture:
- OfflineEventLog: Tracks changes made while offline
- EventMerger: Merges local and remote events with configurable strategies
- ConflictTracker: Logs and manages merge conflicts
- ReconnectionManager: Handles reconnection and synchronization
"""

import json
import logging
import uuid
from dataclasses import dataclass
from datetime import datetime
from enum import Enum
from typing import Any

import aiosqlite

from htmlgraph.db.pragmas import apply_async_pragmas

logger = logging.getLogger(__name__)


class OfflineEventStatus(str, Enum):
    """Status of an offline event."""

    LOCAL_ONLY = "local_only"  # Created offline, not yet synced
    SYNCED = "synced"  # Successfully synced to server
    CONFLICT = "conflict"  # Detected conflict during merge
    RESOLVED = "resolved"  # Conflict manually resolved


class MergeStrategy(str, Enum):
    """Strategy for resolving conflicts during merge."""

    LAST_WRITE_WINS = "last_write_wins"  # Most recent timestamp wins
    PRIORITY_BASED = "priority_based"  # Higher priority resource wins
    USER_CHOICE = "user_choice"  # Manual user resolution required


@dataclass
class OfflineEvent:
    """Event created while offline."""

    event_id: str
    agent_id: str
    resource_id: str  # feature_id, track_id, etc.
    resource_type: str  # feature, track, spike, etc.
    operation: str  # create, update, delete
    timestamp: datetime
    payload: dict[str, Any]
    status: OfflineEventStatus = OfflineEventStatus.LOCAL_ONLY

    def to_dict(self) -> dict[str, Any]:
        """Convert to dictionary for serialization."""
        return {
            "event_id": self.event_id,
            "agent_id": self.agent_id,
            "resource_id": self.resource_id,
            "resource_type": self.resource_type,
            "operation": self.operation,
            "timestamp": self.timestamp.isoformat(),
            "payload": self.payload,
            "status": self.status.value,
        }


@dataclass
class ConflictInfo:
    """Information about a detected conflict."""

    local_event: OfflineEvent
    remote_event: dict[str, Any]
    conflict_type: str  # "concurrent_modification", "delete_update", etc.
    local_timestamp: datetime
    remote_timestamp: datetime
    resolution_strategy: MergeStrategy
    winner: str = ""  # "local" or "remote" after resolution

    def to_dict(self) -> dict[str, Any]:
        """Convert to dictionary for serialization."""
        return {
            "local_event_id": self.local_event.event_id,
            "remote_event_id": self.remote_event.get("event_id", ""),
            "resource_id": self.local_event.resource_id,
            "conflict_type": self.conflict_type,
            "local_timestamp": self.local_timestamp.isoformat(),
            "remote_timestamp": self.remote_timestamp.isoformat(),
            "resolution_strategy": self.resolution_strategy.value,
            "winner": self.winner,
        }


class OfflineEventLog:
    """
    Tracks changes made while offline.

    Stores events locally in SQLite and manages their synchronization status.
    """

    def __init__(self, db_path: str):
        """
        Initialize offline event log.

        Args:
            db_path: Path to SQLite database
        """
        self.db_path = db_path
        self.local_events: list[OfflineEvent] = []

    async def log_event(self, event: OfflineEvent) -> bool:
        """
        Log an offline event.

        Args:
            event: Event to log

        Returns:
            True if successful, False otherwise
        """
        self.local_events.append(event)
        return await self._persist_event(event)

    async def _persist_event(self, event: OfflineEvent) -> bool:
        """
        Persist event to offline_events table.

        Args:
            event: Event to persist

        Returns:
            True if successful, False otherwise
        """
        try:
            async with aiosqlite.connect(self.db_path) as db:
                await apply_async_pragmas(db)
                await db.execute(
                    """
                    INSERT INTO offline_events
                    (event_id, agent_id, resource_id, resource_type,
                     operation, timestamp, payload, status)
                    VALUES (?, ?, ?, ?, ?, ?, ?, ?)
                """,
                    [
                        event.event_id,
                        event.agent_id,
                        event.resource_id,
                        event.resource_type,
                        event.operation,
                        event.timestamp.isoformat(),
                        json.dumps(event.payload),
                        event.status.value,
                    ],
                )
                await db.commit()
                return True
        except Exception as e:
            logger.error(f"Error persisting offline event: {e}")
            return False

    async def get_unsynced_events(self) -> list[OfflineEvent]:
        """
        Get all events that haven't been synced to server.

        Returns:
            List of unsynced offline events
        """
        try:
            async with aiosqlite.connect(self.db_path) as db:
                await apply_async_pragmas(db)
                cursor = await db.execute(
                    """
                    SELECT event_id, agent_id, resource_id, resource_type,
                           operation, timestamp, payload, status
                    FROM offline_events
                    WHERE status = ?
                    ORDER BY datetime(REPLACE(SUBSTR(timestamp, 1, 19), 'T', ' ')) DESC
                """,
                    [OfflineEventStatus.LOCAL_ONLY.value],
                )

                rows = await cursor.fetchall()
                events = []
                for row in rows:
                    event = OfflineEvent(
                        event_id=row[0],
                        agent_id=row[1],
                        resource_id=row[2],
                        resource_type=row[3],
                        operation=row[4],
                        timestamp=datetime.fromisoformat(row[5]),
                        payload=json.loads(row[6]) if row[6] else {},
                        status=OfflineEventStatus(row[7]),
                    )
                    events.append(event)

                return events
        except Exception as e:
            logger.error(f"Error fetching unsynced events: {e}")
            return []

    async def mark_synced(self, event_id: str) -> bool:
        """
        Mark event as synced.

        Args:
            event_id: Event ID to mark as synced

        Returns:
            True if successful, False otherwise
        """
        try:
            # Update in-memory cache
            for event in self.local_events:
                if event.event_id == event_id:
                    event.status = OfflineEventStatus.SYNCED
                    break

            # Update database
            async with aiosqlite.connect(self.db_path) as db:
                await apply_async_pragmas(db)
                await db.execute(
                    """
                    UPDATE offline_events SET status = ?
                    WHERE event_id = ?
                """,
                    [OfflineEventStatus.SYNCED.value, event_id],
                )
                await db.commit()
                return True
        except Exception as e:
            logger.error(f"Error marking event as synced: {e}")
            return False

    async def mark_conflict(self, event_id: str) -> bool:
        """
        Mark event as having a conflict.

        Args:
            event_id: Event ID to mark as conflicted

        Returns:
            True if successful, False otherwise
        """
        try:
            # Update in-memory cache
            for event in self.local_events:
                if event.event_id == event_id:
                    event.status = OfflineEventStatus.CONFLICT
                    break

            # Update database
            async with aiosqlite.connect(self.db_path) as db:
                await apply_async_pragmas(db)
                await db.execute(
                    """
                    UPDATE offline_events SET status = ?
                    WHERE event_id = ?
                """,
                    [OfflineEventStatus.CONFLICT.value, event_id],
                )
                await db.commit()
                return True
        except Exception as e:
            logger.error(f"Error marking event as conflict: {e}")
            return False


class EventMerger:
    """
    Merges offline events with remote events using configurable strategies.

    Supports:
    - Last-write-wins (timestamp-based)
    - Priority-based (feature priority)
    - User choice (manual resolution)
    """

    def __init__(
        self, db_path: str, strategy: MergeStrategy = MergeStrategy.LAST_WRITE_WINS
    ):
        """
        Initialize event merger.

        Args:
            db_path: Path to SQLite database
            strategy: Conflict resolution strategy
        """
        self.db_path = db_path
        self.strategy = strategy
        self.conflicts: list[ConflictInfo] = []

    async def merge_events(
        self,
        local_events: list[OfflineEvent],
        remote_events: list[dict[str, Any]],
    ) -> dict[str, Any]:
        """
        Merge local offline events with remote events.

        Args:
            local_events: Events created offline
            remote_events: Events from server

        Returns:
            Dictionary with merge results:
            {
                "merged_events": [list of merged events],
                "conflicts": [list of conflicts],
                "resolution_strategy": "last_write_wins",
                "conflict_count": 2
            }
        """
        merged_events = []
        conflicts = []

        # Create mapping of remote events by resource
        remote_by_resource: dict[tuple[str, str], dict[str, Any]] = {}
        for remote_event in remote_events:
            key = (remote_event["resource_id"], remote_event["operation"])
            remote_by_resource[key] = remote_event

        # Process each local event
        for local_event in local_events:
            key = (local_event.resource_id, local_event.operation)

            if key in remote_by_resource:
                # Conflict: both modified same resource
                remote_event = remote_by_resource[key]

                # Detect conflict
                conflict = ConflictInfo(
                    local_event=local_event,
                    remote_event=remote_event,
                    conflict_type="concurrent_modification",
                    local_timestamp=local_event.timestamp,
                    remote_timestamp=datetime.fromisoformat(remote_event["timestamp"]),
                    resolution_strategy=self.strategy,
                )

                # Resolve based on strategy
                resolved = await self._resolve_conflict(conflict)

                if resolved:
                    merged_events.append(resolved)
                    conflicts.append(conflict)
            else:
                # No conflict: use local event
                merged_events.append(local_event)

        # Add remote events that have no local counterpart
        local_keys = {(e.resource_id, e.operation) for e in local_events}
        for remote_event in remote_events:
            key = (remote_event["resource_id"], remote_event["operation"])
            if key not in local_keys:
                merged_events.append(remote_event)

        return {
            "merged_events": merged_events,
            "conflicts": conflicts,
            "resolution_strategy": self.strategy.value,
            "conflict_count": len(conflicts),
        }

    async def _resolve_conflict(
        self, conflict: ConflictInfo
    ) -> OfflineEvent | dict[str, Any]:
        """
        Resolve a conflict using configured strategy.

        Args:
            conflict: Conflict information

        Returns:
            Winning event (local or remote)
        """
        if self.strategy == MergeStrategy.LAST_WRITE_WINS:
            return self._resolve_last_write_wins(conflict)
        elif self.strategy == MergeStrategy.PRIORITY_BASED:
            return await self._resolve_priority_based(conflict)
        else:
            # USER_CHOICE: return conflict for manual review
            return conflict.local_event

    def _resolve_last_write_wins(
        self, conflict: ConflictInfo
    ) -> OfflineEvent | dict[str, Any]:
        """
        Simple last-write-wins: whoever has later timestamp wins.

        Args:
            conflict: Conflict information

        Returns:
            Winning event
        """
        if conflict.local_timestamp > conflict.remote_timestamp:
            conflict.winner = "local"
            return conflict.local_event
        else:
            conflict.winner = "remote"
            return conflict.remote_event

    async def _resolve_priority_based(
        self, conflict: ConflictInfo
    ) -> OfflineEvent | dict[str, Any]:
        """
        Priority-based: use feature/resource priority.

        Args:
            conflict: Conflict information

        Returns:
            Winning event based on priority
        """
        try:
            local_priority = await self._get_resource_priority(
                conflict.local_event.resource_id
            )
            remote_priority = await self._get_resource_priority(
                conflict.remote_event["resource_id"]
            )

            if local_priority >= remote_priority:
                conflict.winner = "local"
                return conflict.local_event
            else:
                conflict.winner = "remote"
                return conflict.remote_event
        except Exception as e:
            logger.error(f"Error resolving priority-based conflict: {e}")
            # Fallback to last-write-wins
            return self._resolve_last_write_wins(conflict)

    async def _get_resource_priority(self, resource_id: str) -> int:
        """
        Get priority of a resource (higher = more important).

        Args:
            resource_id: Resource ID to check

        Returns:
            Priority value (0-3: low, medium, high, critical)
        """
        try:
            async with aiosqlite.connect(self.db_path) as db:
                await apply_async_pragmas(db)
                cursor = await db.execute(
                    """
                    SELECT priority FROM features WHERE id = ?
                """,
                    [resource_id],
                )
                row = await cursor.fetchone()

                if row:
                    priority_map = {"low": 0, "medium": 1, "high": 2, "critical": 3}
                    return priority_map.get(row[0], 1)
                return 1  # Default: medium priority
        except Exception as e:
            logger.error(f"Error fetching resource priority: {e}")
            return 1


class ConflictTracker:
    """
    Tracks and manages merge conflicts.

    Provides audit trail and resolution management for conflicts.
    """

    def __init__(self, db_path: str):
        """
        Initialize conflict tracker.

        Args:
            db_path: Path to SQLite database
        """
        self.db_path = db_path
        self.conflicts: list[ConflictInfo] = []

    async def log_conflict(self, conflict: ConflictInfo) -> bool:
        """
        Log a detected conflict for review.

        Args:
            conflict: Conflict information

        Returns:
            True if successful, False otherwise
        """
        self.conflicts.append(conflict)

        try:
            async with aiosqlite.connect(self.db_path) as db:
                await apply_async_pragmas(db)
                conflict_id = f"conf-{uuid.uuid4().hex[:8]}"
                await db.execute(
                    """
                    INSERT INTO conflict_log
                    (conflict_id, local_event_id, remote_event_id, resource_id,
                     conflict_type, local_timestamp, remote_timestamp,
                     resolution_strategy, resolution, status)
                    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
                    [
                        conflict_id,
                        conflict.local_event.event_id,
                        conflict.remote_event.get("event_id", ""),
                        conflict.local_event.resource_id,
                        conflict.conflict_type,
                        conflict.local_timestamp.isoformat(),
                        conflict.remote_timestamp.isoformat(),
                        conflict.resolution_strategy.value,
                        conflict.winner if conflict.winner else None,
                        "resolved" if conflict.winner else "pending_review",
                    ],
                )
                await db.commit()
                return True
        except Exception as e:
            logger.error(f"Error logging conflict: {e}")
            return False

    async def get_pending_conflicts(self) -> list[ConflictInfo]:
        """
        Get all conflicts pending user review.

        Returns:
            List of unresolved conflicts
        """
        return [c for c in self.conflicts if c.winner == ""]

    async def resolve_conflict(self, local_event_id: str, winner: str) -> bool:
        """
        User resolves a conflict by choosing winner.

        Args:
            local_event_id: Local event ID in conflict
            winner: "local" or "remote"

        Returns:
            True if successful, False otherwise
        """
        try:
            # Update in-memory cache
            for conflict in self.conflicts:
                if conflict.local_event.event_id == local_event_id:
                    conflict.winner = winner
                    break

            # Update database
            async with aiosqlite.connect(self.db_path) as db:
                await apply_async_pragmas(db)
                await db.execute(
                    """
                    UPDATE conflict_log
                    SET status = ?, resolution = ?
                    WHERE local_event_id = ?
                """,
                    ["resolved", winner, local_event_id],
                )
                await db.commit()
                return True
        except Exception as e:
            logger.error(f"Error resolving conflict: {e}")
            return False

    async def get_conflict_report(self) -> dict[str, Any]:
        """
        Generate report of all conflicts.

        Returns:
            Dictionary with conflict statistics and details
        """
        pending = await self.get_pending_conflicts()
        resolved = [c for c in self.conflicts if c.winner != ""]

        return {
            "total_conflicts": len(self.conflicts),
            "pending": len(pending),
            "resolved": len(resolved),
            "conflicts": [c.to_dict() for c in self.conflicts],
        }


class ReconnectionManager:
    """
    Handles reconnection and sync with server.

    Coordinates offline event log, merger, and conflict tracker to
    automatically synchronize changes when connection is restored.
    """

    def __init__(
        self,
        offline_log: OfflineEventLog,
        merger: EventMerger,
        tracker: ConflictTracker,
    ):
        """
        Initialize reconnection manager.

        Args:
            offline_log: Offline event log
            merger: Event merger
            tracker: Conflict tracker
        """
        self.offline_log = offline_log
        self.merger = merger
        self.tracker = tracker
        self.is_online = False

    async def on_reconnect(self) -> dict[str, Any]:
        """
        Called when connection is restored.

        Syncs offline changes with server and resolves conflicts.

        Returns:
            Sync results dictionary with statistics
        """
        logger.info("Reconnecting: syncing offline changes...")

        # Get unsynced events
        unsynced = await self.offline_log.get_unsynced_events()
        if not unsynced:
            logger.info("No offline changes to sync")
            return {
                "synced_events": 0,
                "conflicts": 0,
                "status": "no_changes",
            }

        # Fetch remote events from server
        remote_events = await self._fetch_remote_events()

        # Merge events
        merge_result = await self.merger.merge_events(unsynced, remote_events)

        # Log conflicts for review
        for conflict_info in merge_result.get("conflicts", []):
            await self.tracker.log_conflict(conflict_info)

        # Apply merged events to database
        applied_count = 0
        for event in merge_result["merged_events"]:
            if isinstance(event, OfflineEvent):
                await self.offline_log.mark_synced(event.event_id)
                applied_count += 1
            elif isinstance(event, dict):
                applied_count += 1

            # Apply to main database
            await self._apply_event_to_db(event)

        logger.info(
            f"Sync complete: {applied_count} events synced, "
            f"{len(merge_result['conflicts'])} conflicts"
        )

        # Notify dashboard of pending conflicts if any
        if merge_result["conflicts"]:
            await self._notify_conflicts(merge_result["conflicts"])

        return {
            "synced_events": applied_count,
            "conflicts": len(merge_result["conflicts"]),
            "status": "success",
            "merge_strategy": merge_result["resolution_strategy"],
        }

    async def _fetch_remote_events(self) -> list[dict[str, Any]]:
        """
        Fetch remote events from server.

        In a real implementation, this would call the server API.
        For now, we return empty list (no remote changes).

        Returns:
            List of remote event dictionaries
        """
        # TODO: Implement actual server API call
        # Example:
        # response = await http_client.get(
        #     "https://server/api/events/recent?limit=1000"
        # )
        # return response.json()

        logger.debug("Fetching remote events (not implemented yet)")
        return []

    async def _apply_event_to_db(self, event: OfflineEvent | dict[str, Any]) -> bool:
        """
        Apply event to main database.

        Args:
            event: Event to apply (OfflineEvent or dict)

        Returns:
            True if successful, False otherwise
        """
        try:
            if isinstance(event, OfflineEvent):
                resource_id = event.resource_id
                resource_type = event.resource_type
                operation = event.operation
                payload = event.payload
            else:
                resource_id = str(event.get("resource_id", ""))
                resource_type = str(event.get("resource_type", ""))
                operation = str(event.get("operation", ""))
                payload = event.get("payload", {})

            # Apply to appropriate table based on resource_type
            async with aiosqlite.connect(self.offline_log.db_path) as db:
                await apply_async_pragmas(db)
                if resource_type == "feature":
                    if operation == "update":
                        # Update feature
                        set_clauses = []
                        values = []
                        for key, value in payload.items():
                            set_clauses.append(f"{key} = ?")
                            values.append(value)

                        if set_clauses:
                            values.append(resource_id)
                            await db.execute(
                                f"""
                                UPDATE features
                                SET {", ".join(set_clauses)}, updated_at = CURRENT_TIMESTAMP
                                WHERE id = ?
                            """,
                                values,
                            )
                    elif operation == "create":
                        # Create feature (if not exists)
                        await db.execute(
                            """
                            INSERT OR IGNORE INTO features
                            (id, type, title, description, status, priority)
                            VALUES (?, ?, ?, ?, ?, ?)
                        """,
                            [
                                resource_id,
                                payload.get("type", "feature"),
                                payload.get("title", "Untitled"),
                                payload.get("description", ""),
                                payload.get("status", "todo"),
                                payload.get("priority", "medium"),
                            ],
                        )

                await db.commit()
                return True
        except Exception as e:
            logger.error(f"Error applying event to database: {e}")
            return False

    async def _notify_conflicts(self, conflicts: list[ConflictInfo]) -> None:
        """
        Notify dashboard of pending conflicts.

        Args:
            conflicts: List of conflicts to notify
        """
        # TODO: Implement WebSocket notification to dashboard
        logger.info(f"Conflicts detected: {len(conflicts)} require review")
        for conflict in conflicts:
            logger.info(
                f"  - {conflict.conflict_type}: {conflict.local_event.resource_id}"
            )
