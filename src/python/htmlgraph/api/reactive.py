"""
Reactive Query System - Phase 3

Dashboard queries that auto-update when underlying data changes.
No more manual refresh - changes propagate instantly via WebSocket.

Features:
- Register queries with SQL templates
- Automatic invalidation on data changes
- Dependency tracking (queries know what data they depend on)
- Sub-100ms query update latency
- WebSocket push notifications to subscribers

Example:
    >>> manager = ReactiveQueryManager(websocket_manager, db_path)
    >>> await manager.register_default_queries(db)
    >>>
    >>> # Subscribe client to query
    >>> result = await manager.subscribe_to_query("features_by_status", client_id)
    >>>
    >>> # When feature status changes, query auto-updates
    >>> await manager.on_resource_updated("feat-123", "feature")
    >>> # Client receives new query result automatically
"""

import logging
import time
from dataclasses import dataclass
from datetime import datetime
from typing import Any

import aiosqlite

from htmlgraph.db.pragmas import apply_async_pragmas

logger = logging.getLogger(__name__)


@dataclass
class QueryDefinition:
    """Definition of a reactive query."""

    query_id: str
    name: str
    description: str
    sql: str  # SQL query template
    depends_on: list[
        str
    ]  # Resource patterns it depends on (e.g., "*features", "feat-123")
    refresh_interval_ms: int = 5000  # How often to refresh if no changes


@dataclass
class QueryResult:
    """Result of executing a query."""

    query_id: str
    timestamp: datetime
    rows: list[dict[str, Any]]
    row_count: int = 0
    execution_time_ms: float = 0.0

    def __post_init__(self) -> None:
        self.row_count = len(self.rows)

    def to_dict(self) -> dict[str, Any]:
        """Convert to dictionary for JSON serialization."""
        return {
            "query_id": self.query_id,
            "timestamp": self.timestamp.isoformat(),
            "rows": self.rows,
            "row_count": self.row_count,
            "execution_time_ms": self.execution_time_ms,
        }


class ReactiveQuery:
    """Single reactive query with change detection."""

    def __init__(self, definition: QueryDefinition, db_path: str):
        """
        Initialize reactive query.

        Args:
            definition: Query definition
            db_path: Path to SQLite database
        """
        self.definition = definition
        self.db_path = db_path
        self.subscribers: list[str] = []  # client_ids subscribed
        self.last_result: QueryResult | None = None
        self.last_execution: datetime | None = None

    async def execute(self) -> QueryResult:
        """Execute query and return result."""
        start_time = time.time()

        try:
            async with aiosqlite.connect(self.db_path) as db:
                db.row_factory = aiosqlite.Row
                await apply_async_pragmas(db)
                cursor = await db.execute(self.definition.sql)
                rows = await cursor.fetchall()

                # Convert rows to dicts
                row_dicts: list[dict[str, Any]] = []
                rows_list = list(rows)
                if rows_list:
                    columns = list(rows_list[0].keys())
                    row_dicts = [dict(zip(columns, row)) for row in rows_list]

                result = QueryResult(
                    query_id=self.definition.query_id,
                    timestamp=datetime.now(),
                    rows=row_dicts,
                    execution_time_ms=(time.time() - start_time) * 1000,
                )

                self.last_execution = datetime.now()
                return result

        except Exception as e:
            logger.error(f"Query {self.definition.query_id} failed: {e}")
            return QueryResult(
                query_id=self.definition.query_id,
                timestamp=datetime.now(),
                rows=[],
                execution_time_ms=(time.time() - start_time) * 1000,
            )

    async def has_changed(self) -> bool:
        """Check if query result has changed since last execution."""
        if self.last_result is None:
            return True

        # Store old rows for comparison
        old_rows = self.last_result.rows

        new_result = await self.execute()

        # Simple comparison: check if rows changed
        changed = new_result.rows != old_rows

        self.last_result = new_result
        return changed

    def add_subscriber(self, client_id: str) -> None:
        """Add client to subscribers list."""
        if client_id not in self.subscribers:
            self.subscribers.append(client_id)

    def remove_subscriber(self, client_id: str) -> None:
        """Remove client from subscribers."""
        if client_id in self.subscribers:
            self.subscribers.remove(client_id)

    def get_subscribers(self) -> list[str]:
        """Get all subscribed clients."""
        return self.subscribers.copy()


class ReactiveQueryManager:
    """
    Manages all reactive queries and handles invalidation.

    Tracks dependencies between queries and resources, automatically
    invalidating and re-executing queries when dependent data changes.
    """

    def __init__(self, websocket_manager: Any, db_path: str):
        """
        Initialize reactive query manager.

        Args:
            websocket_manager: WebSocketManager for broadcasting updates
            db_path: Path to SQLite database
        """
        self.websocket_manager = websocket_manager
        self.db_path = db_path
        self.queries: dict[str, ReactiveQuery] = {}
        self.dependencies: dict[str, set[str]] = {}  # resource_pattern -> query_ids

    def register_query(self, definition: QueryDefinition) -> None:
        """
        Register a new reactive query.

        Args:
            definition: Query definition with SQL and dependencies
        """
        query = ReactiveQuery(definition, self.db_path)
        self.queries[definition.query_id] = query

        # Track dependencies
        for dep in definition.depends_on:
            if dep not in self.dependencies:
                self.dependencies[dep] = set()
            self.dependencies[dep].add(definition.query_id)

        logger.info(f"Registered query: {definition.query_id} ({definition.name})")

    async def register_default_queries(self) -> None:
        """Register common queries used by dashboard."""

        # Query 1: Features by status
        self.register_query(
            QueryDefinition(
                query_id="features_by_status",
                name="Features by Status",
                description="Count of features in each status",
                sql="""
                    SELECT status, COUNT(*) as count
                    FROM features
                    GROUP BY status
                    ORDER BY status
                """,
                depends_on=["*features"],  # Depends on all features
            )
        )

        # Query 2: Agent workload
        self.register_query(
            QueryDefinition(
                query_id="agent_workload",
                name="Agent Workload",
                description="Active tasks per agent",
                sql="""
                    SELECT
                        agent_id,
                        COUNT(DISTINCT session_id) as active_sessions,
                        COUNT(*) as total_events
                    FROM agent_events
                    WHERE timestamp > datetime('now', '-1 hour')
                    GROUP BY agent_id
                    ORDER BY total_events DESC
                """,
                depends_on=["*events"],
            )
        )

        # Query 3: Recent activity
        self.register_query(
            QueryDefinition(
                query_id="recent_activity",
                name="Recent Activity",
                description="Last 20 tool executions",
                sql="""
                    SELECT
                        event_id, agent_id, tool_name,
                        input_summary, timestamp
                    FROM agent_events
                    WHERE event_type = 'tool_call'
                    ORDER BY datetime(REPLACE(SUBSTR(timestamp, 1, 19), 'T', ' ')) DESC
                    LIMIT 20
                """,
                depends_on=["*events"],
            )
        )

        # Query 4: Blocked features
        self.register_query(
            QueryDefinition(
                query_id="blocked_features",
                name="Blocked Features",
                description="Features waiting on other features",
                sql="""
                    SELECT id, title, status, created_at
                    FROM features
                    WHERE status = 'blocked'
                    ORDER BY created_at DESC
                """,
                depends_on=["*features"],
            )
        )

        # Query 5: Cost trends (hourly)
        self.register_query(
            QueryDefinition(
                query_id="cost_trends",
                name="Cost Trends",
                description="Cost aggregated by hour",
                sql="""
                    SELECT
                        strftime('%Y-%m-%d %H:00', timestamp) as hour,
                        SUM(cost_tokens) as total_tokens,
                        COUNT(*) as event_count
                    FROM agent_events
                    WHERE cost_tokens > 0
                    GROUP BY hour
                    ORDER BY hour DESC
                    LIMIT 24
                """,
                depends_on=["*events"],
            )
        )

        # Query 6: Active sessions
        self.register_query(
            QueryDefinition(
                query_id="active_sessions",
                name="Active Sessions",
                description="Currently active agent sessions",
                sql="""
                    SELECT
                        session_id, agent_assigned,
                        created_at, updated_at
                    FROM sessions
                    WHERE status = 'active'
                    ORDER BY updated_at DESC
                """,
                depends_on=["*sessions"],
            )
        )

        logger.info(f"Registered {len(self.queries)} default queries")

    async def on_resource_updated(self, resource_id: str, resource_type: str) -> None:
        """
        Called when a resource (feature, track, event, etc.) is updated.

        Invalidates all queries that depend on this resource.

        Args:
            resource_id: ID of updated resource (e.g., "feat-123")
            resource_type: Type of resource (e.g., "feature", "event", "session")
        """
        affected_queries: set[str] = set()

        # Check exact match dependencies
        if resource_id in self.dependencies:
            affected_queries.update(self.dependencies[resource_id])

        # Check wildcard dependencies (e.g., "*features")
        wildcard_key = f"*{resource_type}s"
        if wildcard_key in self.dependencies:
            affected_queries.update(self.dependencies[wildcard_key])

        # Re-execute affected queries and notify subscribers
        for query_id in affected_queries:
            if query_id in self.queries:
                await self.invalidate_query(query_id)

    async def invalidate_query(self, query_id: str) -> None:
        """
        Re-execute query and broadcast new result to subscribers.

        Args:
            query_id: Query to invalidate
        """
        if query_id not in self.queries:
            logger.warning(f"Query not found: {query_id}")
            return

        query = self.queries[query_id]

        # Execute query
        result = await query.execute()
        query.last_result = result

        # Broadcast to subscribers
        subscribers_notified = 0
        for client_id in query.get_subscribers():
            try:
                # Send update via WebSocketManager's broadcast method
                # Note: Using a pseudo-session "queries" for query subscriptions
                sent = await self.websocket_manager.broadcast_to_session(
                    session_id="queries",
                    event={
                        "type": "query_update",
                        "query_id": query_id,
                        **result.to_dict(),
                    },
                )
                subscribers_notified += sent
            except Exception as e:
                logger.error(f"Error broadcasting to {client_id}: {e}")
                query.remove_subscriber(client_id)

        logger.debug(
            f"Query {query_id} invalidated, notified {subscribers_notified} subscribers"
        )

    async def subscribe_to_query(
        self, query_id: str, client_id: str
    ) -> QueryResult | None:
        """
        Subscribe client to query and return initial result.

        Args:
            query_id: Query to subscribe to
            client_id: Client subscribing

        Returns:
            Initial query result or None if query not found
        """
        if query_id not in self.queries:
            logger.warning(f"Query not found: {query_id}")
            return None

        query = self.queries[query_id]
        query.add_subscriber(client_id)

        # Return initial result (execute if not cached)
        if query.last_result is None:
            query.last_result = await query.execute()

        logger.info(f"Client {client_id} subscribed to query {query_id}")
        return query.last_result

    def unsubscribe_from_query(self, query_id: str, client_id: str) -> None:
        """
        Unsubscribe client from query.

        Args:
            query_id: Query to unsubscribe from
            client_id: Client unsubscribing
        """
        if query_id in self.queries:
            self.queries[query_id].remove_subscriber(client_id)
            logger.info(f"Client {client_id} unsubscribed from query {query_id}")

    async def get_query_result(self, query_id: str) -> QueryResult | None:
        """
        Get current result of a query.

        Args:
            query_id: Query to get result for

        Returns:
            Query result or None if not found
        """
        if query_id not in self.queries:
            return None

        query = self.queries[query_id]
        if query.last_result is None:
            query.last_result = await query.execute()

        return query.last_result

    def list_queries(self) -> list[dict[str, Any]]:
        """
        List all registered queries.

        Returns:
            List of query metadata
        """
        return [
            {
                "query_id": q.definition.query_id,
                "name": q.definition.name,
                "description": q.definition.description,
                "subscribers": len(q.get_subscribers()),
                "last_execution": q.last_execution.isoformat()
                if q.last_execution
                else None,
            }
            for q in self.queries.values()
        ]
