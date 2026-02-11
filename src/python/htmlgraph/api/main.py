"""
HtmlGraph FastAPI Backend - Real-time Agent Observability Dashboard

Provides REST API and WebSocket support for viewing:
- Agent activity feed with real-time event streaming
- Orchestration chains and delegation handoffs
- Feature tracker with Kanban views
- Session metrics and performance analytics

Architecture:
- FastAPI backend querying SQLite database
- Jinja2 templates for server-side rendering
- HTMX for interactive UI without page reloads
- WebSocket for real-time event streaming
"""

import asyncio
import json
import logging
import random
import sqlite3
import time
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any

import aiosqlite
from fastapi import FastAPI, HTTPException, Request, WebSocket, WebSocketDisconnect
from fastapi.responses import HTMLResponse
from fastapi.staticfiles import StaticFiles
from fastapi.templating import Jinja2Templates
from pydantic import BaseModel

logger = logging.getLogger(__name__)


class QueryCache:
    """Simple in-memory cache with TTL support for query results."""

    def __init__(self, ttl_seconds: float = 30.0):
        """Initialize query cache with TTL."""
        self.cache: dict[str, tuple[Any, float]] = {}
        self.ttl_seconds = ttl_seconds
        self.metrics: dict[str, dict[str, float]] = {}

    def get(self, key: str) -> Any | None:
        """Get cached value if exists and not expired."""
        if key not in self.cache:
            return None

        value, timestamp = self.cache[key]
        if time.time() - timestamp > self.ttl_seconds:
            del self.cache[key]
            return None

        return value

    def set(self, key: str, value: Any) -> None:
        """Store value with current timestamp."""
        self.cache[key] = (value, time.time())

    def record_metric(self, key: str, query_time_ms: float, cache_hit: bool) -> None:
        """Record performance metrics for a query."""
        if key not in self.metrics:
            self.metrics[key] = {"count": 0, "total_ms": 0, "avg_ms": 0, "hits": 0}

        metrics = self.metrics[key]
        metrics["count"] += 1
        metrics["total_ms"] += query_time_ms
        metrics["avg_ms"] = metrics["total_ms"] / metrics["count"]
        if cache_hit:
            metrics["hits"] += 1

    def get_metrics(self) -> dict[str, dict[str, float]]:
        """Get all collected metrics."""
        return self.metrics


class EventModel(BaseModel):
    """Event data model for API responses."""

    event_id: str
    agent_id: str
    event_type: str
    timestamp: str
    tool_name: str | None = None
    input_summary: str | None = None
    output_summary: str | None = None
    session_id: str
    feature_id: str | None = None
    parent_event_id: str | None = None
    status: str
    model: str | None = None


class FeatureModel(BaseModel):
    """Feature data model for API responses."""

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
    """Session data model for API responses."""

    session_id: str
    agent: str | None = None
    status: str
    started_at: str
    ended_at: str | None = None
    event_count: int = 0
    duration_seconds: float | None = None


def _ensure_database_initialized(db_path: str) -> None:
    """
    Ensure SQLite database exists and has correct schema.

    Args:
        db_path: Path to SQLite database file
    """
    db_file = Path(db_path)
    db_file.parent.mkdir(parents=True, exist_ok=True)

    # Check if database exists and has tables
    try:
        conn = sqlite3.connect(db_path)
        cursor = conn.cursor()

        # Query existing tables
        cursor.execute("SELECT name FROM sqlite_master WHERE type='table'")
        tables = cursor.fetchall()
        table_names = [t[0] for t in tables]

        if not table_names:
            # Database is empty, create schema
            logger.info(f"Creating database schema at {db_path}")
            from htmlgraph.db.schema import HtmlGraphDB

            db = HtmlGraphDB(db_path)
            db.connect()
            db.create_tables()
            db.disconnect()
            logger.info("Database schema created successfully")
        else:
            logger.debug(f"Database already initialized with tables: {table_names}")

        conn.close()

    except sqlite3.Error as e:
        logger.warning(f"Database check warning: {e}")
        # Try to create anyway
        try:
            from htmlgraph.db.schema import HtmlGraphDB

            db = HtmlGraphDB(db_path)
            db.connect()
            db.create_tables()
            db.disconnect()
        except Exception as create_error:
            logger.error(f"Failed to create database: {create_error}")
            raise


def get_app(db_path: str) -> FastAPI:
    """
    Create and configure FastAPI application.

    Args:
        db_path: Path to SQLite database file

    Returns:
        Configured FastAPI application instance
    """
    # Ensure database is initialized
    _ensure_database_initialized(db_path)

    app = FastAPI(
        title="HtmlGraph Dashboard API",
        description="Real-time agent observability dashboard",
        version="0.1.0",
    )

    # Store database path and query cache in app state
    app.state.db_path = db_path
    app.state.query_cache = QueryCache(ttl_seconds=1.0)  # Short TTL for real-time data

    # Setup Jinja2 templates
    template_dir = Path(__file__).parent / "templates"
    template_dir.mkdir(parents=True, exist_ok=True)
    templates = Jinja2Templates(directory=str(template_dir))

    # Add custom filters
    def format_number(value: int | None) -> str:
        if value is None:
            return "0"
        return f"{value:,}"

    def format_duration(seconds: float | int | None) -> str:
        """Format duration in seconds to human-readable string."""
        if seconds is None:
            return "0.00s"
        return f"{float(seconds):.2f}s"

    def format_bytes(bytes_size: int | float | None) -> str:
        """Format bytes to MB with 2 decimal places."""
        if bytes_size is None:
            return "0.00MB"
        return f"{int(bytes_size) / (1024 * 1024):.2f}MB"

    def truncate_text(text: str | None, length: int = 50) -> str:
        """Truncate text to specified length with ellipsis."""
        if text is None:
            return ""
        return text[:length] + "..." if len(text) > length else text

    def format_timestamp(ts: Any) -> str:
        """Format timestamp to readable string."""
        if ts is None:
            return ""
        if hasattr(ts, "strftime"):
            return str(ts.strftime("%Y-%m-%d %H:%M:%S"))
        return str(ts)

    templates.env.filters["format_number"] = format_number
    templates.env.filters["format_duration"] = format_duration
    templates.env.filters["format_bytes"] = format_bytes
    templates.env.filters["truncate"] = truncate_text
    templates.env.filters["format_timestamp"] = format_timestamp

    # Setup static files
    static_dir = Path(__file__).parent / "static"
    static_dir.mkdir(parents=True, exist_ok=True)
    if static_dir.exists():
        app.mount("/static", StaticFiles(directory=str(static_dir)), name="static")

    # ========== DATABASE HELPERS ==========

    async def get_db() -> aiosqlite.Connection:
        """Get database connection with busy_timeout to prevent lock errors."""
        db = await aiosqlite.connect(app.state.db_path)
        db.row_factory = aiosqlite.Row
        # Set busy_timeout to 5 seconds - prevents "database is locked" errors
        # during concurrent access from spawner scripts and WebSocket polling
        await db.execute("PRAGMA busy_timeout = 5000")
        return db

    # ========== ROUTES ==========

    @app.get("/", response_class=HTMLResponse)
    async def dashboard(request: Request) -> HTMLResponse:
        """Main dashboard view with navigation tabs."""
        return templates.TemplateResponse(
            "dashboard-redesign.html",
            {
                "request": request,
                "title": "HtmlGraph Agent Observability",
            },
        )

    # ========== AGENTS ENDPOINTS ==========

    @app.get("/views/agents", response_class=HTMLResponse)
    async def agents_view(request: Request) -> HTMLResponse:
        """Get agent workload and performance stats as HTMX partial."""
        db = await get_db()
        cache = app.state.query_cache
        query_start_time = time.time()

        try:
            # Create cache key for agents view
            cache_key = "agents_view:all"

            # Check cache first
            cached_response = cache.get(cache_key)
            if cached_response is not None:
                query_time_ms = (time.time() - query_start_time) * 1000
                cache.record_metric(cache_key, query_time_ms, cache_hit=True)
                logger.debug(
                    f"Cache HIT for agents_view (key={cache_key}, time={query_time_ms:.2f}ms)"
                )
                agents, total_actions, total_tokens = cached_response
            else:
                # Query agent statistics from 'agent_events' table joined with sessions
                # Optimized with GROUP BY on indexed column
                query = """
                    SELECT
                        e.agent_id,
                        COUNT(*) as event_count,
                        SUM(e.cost_tokens) as total_tokens,
                        COUNT(DISTINCT e.session_id) as session_count,
                        MAX(e.timestamp) as last_active,
                        MAX(e.model) as model,
                        CASE
                            WHEN MAX(e.timestamp) > datetime('now', '-5 minutes') THEN 'active'
                            ELSE 'idle'
                        END as status,
                        AVG(e.execution_duration_seconds) as avg_duration,
                        SUM(CASE WHEN e.event_type = 'error' THEN 1 ELSE 0 END) as error_count,
                        ROUND(
                            100.0 * COUNT(CASE WHEN e.status = 'completed' THEN 1 END) /
                            CAST(COUNT(*) AS FLOAT),
                            1
                        ) as success_rate
                    FROM agent_events e
                    GROUP BY e.agent_id
                    ORDER BY event_count DESC
                """

                # Execute query with timing
                exec_start = time.time()
                async with db.execute(query) as cursor:
                    rows = await cursor.fetchall()
                exec_time_ms = (time.time() - exec_start) * 1000

                agents = []
                total_actions = 0
                total_tokens = 0

                # First pass to calculate totals
                for row in rows:
                    total_actions += row[1]
                    total_tokens += row[2] or 0

                # Second pass to build agent objects with percentages
                for row in rows:
                    event_count = row[1]
                    workload_pct = (
                        (event_count / total_actions * 100) if total_actions > 0 else 0
                    )

                    agents.append(
                        {
                            "id": row[0],
                            "agent_id": row[0],
                            "name": row[0],
                            "event_count": event_count,
                            "total_tokens": row[2] or 0,
                            "session_count": row[3],
                            "last_activity": row[4],
                            "last_active": row[4],
                            "model": row[5] or "unknown",
                            "status": row[6] or "idle",
                            "avg_duration": row[7],
                            "error_count": row[8] or 0,
                            "success_rate": row[9] or 0.0,
                            "workload_pct": round(workload_pct, 1),
                        }
                    )

                # Cache the results
                cache_data = (agents, total_actions, total_tokens)
                cache.set(cache_key, cache_data)
                query_time_ms = (time.time() - query_start_time) * 1000
                cache.record_metric(cache_key, exec_time_ms, cache_hit=False)
                logger.debug(
                    f"Cache MISS for agents_view (key={cache_key}, "
                    f"db_time={exec_time_ms:.2f}ms, total_time={query_time_ms:.2f}ms, "
                    f"agents={len(agents)})"
                )

            return templates.TemplateResponse(
                "partials/agents.html",
                {
                    "request": request,
                    "agents": agents,
                    "total_agents": len(agents),
                    "total_actions": total_actions,
                    "total_tokens": total_tokens,
                },
            )
        finally:
            await db.close()

    # ========== PRESENCE WIDGET DEMO (Phase 6) ==========

    @app.get("/views/presence-widget", response_class=HTMLResponse)
    async def presence_widget_demo(request: Request) -> HTMLResponse:
        """Phase 6 Demo: Real-time Presence Widget showing active agents across sessions.

        This widget demonstrates:
        - WebSocket connections to broadcast stream
        - Real-time presence updates (<100ms latency)
        - Multi-session agent coordination
        - Integration with Phase 5 broadcast API

        Try it out:
        1. Open this page in your browser
        2. In another terminal, emit presence events
        3. Watch the dashboard update in real-time!
        """
        widget_path = Path(__file__).parent / "static" / "presence-widget-demo.html"
        if widget_path.exists():
            return HTMLResponse(content=widget_path.read_text())
        else:
            raise HTTPException(
                status_code=404, detail="Presence widget demo not found"
            )

    # ========== ACTIVITY FEED ENDPOINTS ==========

    @app.get("/views/activity-feed", response_class=HTMLResponse)
    async def activity_feed(
        request: Request,
        limit: int = 50,
        session_id: str | None = None,
        agent_id: str | None = None,
    ) -> HTMLResponse:
        """Get latest agent events grouped by conversation turn (user prompt).

        Returns grouped activity feed showing conversation turns with their child events.
        """
        db = await get_db()
        cache = app.state.query_cache

        try:
            # Call the helper function to get grouped events
            grouped_result = await _get_events_grouped_by_prompt_impl(db, cache, limit)

            return templates.TemplateResponse(
                "partials/activity-feed.html",
                {
                    "request": request,
                    "conversation_turns": grouped_result.get("conversation_turns", []),
                    "total_turns": grouped_result.get("total_turns", 0),
                    "limit": limit,
                },
            )
        finally:
            await db.close()

    @app.get("/api/events", response_model=list[EventModel])
    async def get_events(
        limit: int = 50,
        session_id: str | None = None,
        agent_id: str | None = None,
        offset: int = 0,
    ) -> list[EventModel]:
        """Get events as JSON API with parent-child hierarchical linking."""
        db = await get_db()
        cache = app.state.query_cache
        query_start_time = time.time()

        try:
            # Create cache key from query parameters
            cache_key = (
                f"api_events:{limit}:{offset}:{session_id or 'all'}:{agent_id or 'all'}"
            )

            # Check cache first
            cached_results = cache.get(cache_key)
            if cached_results is not None:
                query_time_ms = (time.time() - query_start_time) * 1000
                cache.record_metric(cache_key, query_time_ms, cache_hit=True)
                logger.debug(
                    f"Cache HIT for api_events (key={cache_key}, time={query_time_ms:.2f}ms)"
                )
                return list(cached_results) if isinstance(cached_results, list) else []
            else:
                # Query from 'agent_events' table from Phase 1 PreToolUse hook implementation
                # Optimized with column selection and proper indexing
                query = """
                    SELECT e.event_id, e.agent_id, e.event_type, e.timestamp, e.tool_name,
                           e.input_summary, e.output_summary, e.session_id,
                           e.parent_event_id, e.status, e.model, e.feature_id
                    FROM agent_events e
                    WHERE 1=1
                """
                params: list = []

                if session_id:
                    query += " AND e.session_id = ?"
                    params.append(session_id)

                if agent_id:
                    query += " AND e.agent_id = ?"
                    params.append(agent_id)

                query += " ORDER BY e.timestamp DESC LIMIT ? OFFSET ?"
                params.extend([limit, offset])

                # Execute query with timing
                exec_start = time.time()
                async with db.execute(query, params) as cursor:
                    rows = await cursor.fetchall()
                exec_time_ms = (time.time() - exec_start) * 1000

                # Build result models
                results = [
                    EventModel(
                        event_id=row[0],
                        agent_id=row[1] or "unknown",
                        event_type=row[2],
                        timestamp=row[3],
                        tool_name=row[4],
                        input_summary=row[5],
                        output_summary=row[6],
                        session_id=row[7],
                        parent_event_id=row[8],
                        status=row[9],
                        model=row[10],
                        feature_id=row[11],
                    )
                    for row in rows
                ]

                # Cache the results
                cache.set(cache_key, results)
                query_time_ms = (time.time() - query_start_time) * 1000
                cache.record_metric(cache_key, exec_time_ms, cache_hit=False)
                logger.debug(
                    f"Cache MISS for api_events (key={cache_key}, "
                    f"db_time={exec_time_ms:.2f}ms, total_time={query_time_ms:.2f}ms, "
                    f"rows={len(results)})"
                )

                return results
        finally:
            await db.close()

    # ========== INITIAL STATS ENDPOINT ==========

    @app.get("/api/initial-stats")
    async def initial_stats() -> dict[str, Any]:
        """Get initial statistics for dashboard header (events, agents, sessions)."""
        db = await get_db()
        try:
            # Query all stats in a single query for efficiency
            stats_query = """
                SELECT
                    (SELECT COUNT(*) FROM agent_events) as total_events,
                    (SELECT COUNT(DISTINCT agent_id) FROM agent_events) as total_agents,
                    (SELECT COUNT(*) FROM sessions) as total_sessions
            """
            async with db.execute(stats_query) as cursor:
                row = await cursor.fetchone()

            # Query distinct agent IDs for the agent set
            agents_query = (
                "SELECT DISTINCT agent_id FROM agent_events WHERE agent_id IS NOT NULL"
            )
            async with db.execute(agents_query) as agents_cursor:
                agents_rows = await agents_cursor.fetchall()
            agents = [row[0] for row in agents_rows]

            if row is None:
                return {
                    "total_events": 0,
                    "total_agents": 0,
                    "total_sessions": 0,
                    "agents": agents,
                }

            return {
                "total_events": int(row[0]) if row[0] else 0,
                "total_agents": int(row[1]) if row[1] else 0,
                "total_sessions": int(row[2]) if row[2] else 0,
                "agents": agents,
            }
        finally:
            await db.close()

    # ========== PERFORMANCE METRICS ENDPOINT ==========

    @app.get("/api/query-metrics")
    async def get_query_metrics() -> dict[str, Any]:
        """Get query performance metrics and cache statistics."""
        cache = app.state.query_cache
        metrics = cache.get_metrics()

        # Calculate aggregate statistics
        total_queries = sum(m.get("count", 0) for m in metrics.values())
        total_cache_hits = sum(m.get("hits", 0) for m in metrics.values())
        hit_rate = (total_cache_hits / total_queries * 100) if total_queries > 0 else 0

        return {
            "timestamp": datetime.now().isoformat(),
            "cache_status": {
                "ttl_seconds": cache.ttl_seconds,
                "cached_queries": len(cache.cache),
                "total_queries_tracked": total_queries,
                "cache_hits": total_cache_hits,
                "cache_hit_rate_percent": round(hit_rate, 2),
            },
            "query_metrics": metrics,
        }

    # ========== EVENT TRACES ENDPOINT (Parent-Child Nesting) ==========

    @app.get("/api/event-traces")
    async def get_event_traces(
        limit: int = 50,
        session_id: str | None = None,
    ) -> dict[str, Any]:
        """
        Get event traces showing parent-child relationships for Task delegations.

        This endpoint returns task delegation events with their child events,
        showing the complete hierarchy of delegated work:

        Example:
        {
            "traces": [
                {
                    "parent_event_id": "evt-abc123",
                    "agent_id": "claude-code",
                    "subagent_type": "gemini-spawner",
                    "started_at": "2025-01-08T16:40:54",
                    "status": "completed",
                    "duration_seconds": 287,
                    "child_events": [
                        {
                            "event_id": "subevt-xyz789",
                            "agent_id": "subagent-gemini-spawner",
                            "event_type": "delegation",
                            "timestamp": "2025-01-08T16:42:01",
                            "status": "completed"
                        }
                    ],
                    "child_spike_count": 2,
                    "child_spikes": ["spk-001", "spk-002"]
                }
            ]
        }

        Args:
            limit: Maximum number of parent events to return (default 50)
            session_id: Filter by session (optional)

        Returns:
            Dict with traces array showing parent-child relationships
        """
        db = await get_db()
        cache = app.state.query_cache
        query_start_time = time.time()

        try:
            # Create cache key
            cache_key = f"event_traces:{limit}:{session_id or 'all'}"

            # Check cache first
            cached_result = cache.get(cache_key)
            if cached_result is not None:
                query_time_ms = (time.time() - query_start_time) * 1000
                cache.record_metric(cache_key, query_time_ms, cache_hit=True)
                return cached_result  # type: ignore[no-any-return]

            exec_start = time.time()

            # Query parent events (task delegations)
            parent_query = """
                SELECT event_id, agent_id, subagent_type, timestamp, status,
                       child_spike_count, output_summary, model
                FROM agent_events
                WHERE event_type = 'task_delegation'
            """
            parent_params: list[Any] = []

            if session_id:
                parent_query += " AND session_id = ?"
                parent_params.append(session_id)

            parent_query += " ORDER BY timestamp DESC LIMIT ?"
            parent_params.append(limit)

            async with db.execute(parent_query, parent_params) as cursor:
                parent_rows = await cursor.fetchall()

            traces: list[dict[str, Any]] = []

            for parent_row in parent_rows:
                parent_event_id = parent_row[0]
                agent_id = parent_row[1]
                subagent_type = parent_row[2]
                started_at = parent_row[3]
                status = parent_row[4]
                child_spike_count = parent_row[5] or 0
                output_summary = parent_row[6]
                model = parent_row[7]

                # Parse output summary to get child spike IDs if available
                child_spikes = []
                try:
                    if output_summary:
                        output_data = (
                            json.loads(output_summary)
                            if isinstance(output_summary, str)
                            else output_summary
                        )
                        # Try to extract spike IDs if present
                        if isinstance(output_data, dict):
                            spikes_info = output_data.get("spikes_created", [])
                            if isinstance(spikes_info, list):
                                child_spikes = spikes_info
                except Exception:
                    pass

                # Query child events (subagent completion events)
                child_query = """
                    SELECT event_id, agent_id, event_type, timestamp, status
                    FROM agent_events
                    WHERE parent_event_id = ?
                    ORDER BY timestamp DESC
                """
                async with db.execute(child_query, (parent_event_id,)) as child_cursor:
                    child_rows = await child_cursor.fetchall()

                child_events = []
                for child_row in child_rows:
                    child_events.append(
                        {
                            "event_id": child_row[0],
                            "agent_id": child_row[1],
                            "event_type": child_row[2],
                            "timestamp": child_row[3],
                            "status": child_row[4],
                        }
                    )

                # Calculate duration if completed
                duration_seconds = None
                if status == "completed" and started_at:
                    try:
                        from datetime import datetime as dt

                        start_dt = dt.fromisoformat(started_at)
                        now_dt = dt.now()
                        duration_seconds = (now_dt - start_dt).total_seconds()
                    except Exception:
                        pass

                trace = {
                    "parent_event_id": parent_event_id,
                    "agent_id": agent_id,
                    "subagent_type": subagent_type or "general-purpose",
                    "started_at": started_at,
                    "status": status,
                    "duration_seconds": duration_seconds,
                    "child_events": child_events,
                    "child_spike_count": child_spike_count,
                    "child_spikes": child_spikes,
                    "model": model,
                }

                traces.append(trace)

            exec_time_ms = (time.time() - exec_start) * 1000

            # Build response
            result = {
                "timestamp": datetime.now().isoformat(),
                "total_traces": len(traces),
                "traces": traces,
                "limitations": {
                    "note": "Child spike count is approximate and based on timestamp proximity",
                    "note_2": "Spike IDs in child_spikes only available if recorded in output_summary",
                },
            }

            # Cache the result
            cache.set(cache_key, result)
            query_time_ms = (time.time() - query_start_time) * 1000
            cache.record_metric(cache_key, exec_time_ms, cache_hit=False)
            logger.debug(
                f"Cache MISS for event_traces (key={cache_key}, "
                f"db_time={exec_time_ms:.2f}ms, total_time={query_time_ms:.2f}ms, "
                f"traces={len(traces)})"
            )

            return result

        finally:
            await db.close()

    # ========== COMPLETE ACTIVITY FEED ENDPOINT ==========

    @app.get("/api/complete-activity-feed")
    async def complete_activity_feed(
        limit: int = 100,
        session_id: str | None = None,
        include_delegations: bool = True,
        include_spikes: bool = True,
    ) -> dict[str, Any]:
        """
        Get unified activity feed combining events from all sources.

        This endpoint aggregates:
        - Hook events (tool_call from PreToolUse)
        - Subagent events (delegation completions from SubagentStop)
        - SDK spike logs (knowledge created by delegated tasks)

        This provides complete visibility into ALL activity, including
        delegated work that would otherwise be invisible due to Claude Code's
        hook isolation design (see GitHub issue #14859).

        Args:
            limit: Maximum number of events to return
            session_id: Filter by session (optional)
            include_delegations: Include delegation events (default True)
            include_spikes: Include spike creation events (default True)

        Returns:
            Dict with events array and metadata
        """
        db = await get_db()
        cache = app.state.query_cache
        query_start_time = time.time()

        try:
            # Create cache key
            cache_key = f"complete_activity:{limit}:{session_id or 'all'}:{include_delegations}:{include_spikes}"

            # Check cache first
            cached_result = cache.get(cache_key)
            if cached_result is not None:
                query_time_ms = (time.time() - query_start_time) * 1000
                cache.record_metric(cache_key, query_time_ms, cache_hit=True)
                return cached_result  # type: ignore[no-any-return]

            events: list[dict[str, Any]] = []

            # 1. Query hook events (tool_call, delegation from agent_events)
            event_types = ["tool_call"]
            if include_delegations:
                event_types.extend(["delegation", "completion"])

            event_type_placeholders = ",".join("?" for _ in event_types)
            query = f"""
                SELECT
                    'hook_event' as source,
                    event_id,
                    agent_id,
                    event_type,
                    timestamp,
                    tool_name,
                    input_summary,
                    output_summary,
                    session_id,
                    status,
                    model,
                    parent_event_id,
                    feature_id
                FROM agent_events
                WHERE event_type IN ({event_type_placeholders})
            """
            params: list[Any] = list(event_types)

            if session_id:
                query += " AND session_id = ?"
                params.append(session_id)

            query += " ORDER BY timestamp DESC LIMIT ?"
            params.append(limit)

            exec_start = time.time()
            async with db.execute(query, params) as cursor:
                rows = await cursor.fetchall()

            for row in rows:
                events.append(
                    {
                        "source": row[0],
                        "event_id": row[1],
                        "agent_id": row[2] or "unknown",
                        "event_type": row[3],
                        "timestamp": row[4],
                        "tool_name": row[5],
                        "input_summary": row[6],
                        "output_summary": row[7],
                        "session_id": row[8],
                        "status": row[9],
                        "model": row[10],
                        "parent_event_id": row[11],
                        "feature_id": row[12],
                    }
                )

            # 2. Query spike logs if requested (knowledge created by delegated tasks)
            if include_spikes:
                try:
                    spike_query = """
                        SELECT
                            'spike_log' as source,
                            id as event_id,
                            assigned_to as agent_id,
                            'knowledge_created' as event_type,
                            created_at as timestamp,
                            title as tool_name,
                            hypothesis as input_summary,
                            findings as output_summary,
                            NULL as session_id,
                            status
                        FROM features
                        WHERE type = 'spike'
                    """
                    spike_params: list[Any] = []

                    spike_query += " ORDER BY created_at DESC LIMIT ?"
                    spike_params.append(limit)

                    async with db.execute(spike_query, spike_params) as spike_cursor:
                        spike_rows = await spike_cursor.fetchall()

                    for row in spike_rows:
                        events.append(
                            {
                                "source": row[0],
                                "event_id": row[1],
                                "agent_id": row[2] or "sdk",
                                "event_type": row[3],
                                "timestamp": row[4],
                                "tool_name": row[5],
                                "input_summary": row[6],
                                "output_summary": row[7],
                                "session_id": row[8],
                                "status": row[9] or "completed",
                            }
                        )
                except Exception as e:
                    # Spike query might fail if columns don't exist
                    logger.debug(
                        f"Spike query failed (expected if schema differs): {e}"
                    )

            # 3. Query delegation handoffs from agent_collaboration
            if include_delegations:
                try:
                    collab_query = """
                        SELECT
                            'delegation' as source,
                            handoff_id as event_id,
                            from_agent || ' -> ' || to_agent as agent_id,
                            'handoff' as event_type,
                            timestamp,
                            handoff_type as tool_name,
                            reason as input_summary,
                            context as output_summary,
                            session_id,
                            status
                        FROM agent_collaboration
                        WHERE handoff_type = 'delegation'
                    """
                    collab_params: list[Any] = []

                    if session_id:
                        collab_query += " AND session_id = ?"
                        collab_params.append(session_id)

                    collab_query += " ORDER BY timestamp DESC LIMIT ?"
                    collab_params.append(limit)

                    async with db.execute(collab_query, collab_params) as collab_cursor:
                        collab_rows = await collab_cursor.fetchall()

                    for row in collab_rows:
                        events.append(
                            {
                                "source": row[0],
                                "event_id": row[1],
                                "agent_id": row[2] or "orchestrator",
                                "event_type": row[3],
                                "timestamp": row[4],
                                "tool_name": row[5],
                                "input_summary": row[6],
                                "output_summary": row[7],
                                "session_id": row[8],
                                "status": row[9] or "pending",
                            }
                        )
                except Exception as e:
                    logger.debug(f"Collaboration query failed: {e}")

            # Sort all events by timestamp DESC
            events.sort(key=lambda e: e.get("timestamp", ""), reverse=True)

            # Limit to requested count
            events = events[:limit]

            exec_time_ms = (time.time() - exec_start) * 1000

            # Build response
            result = {
                "timestamp": datetime.now().isoformat(),
                "total_events": len(events),
                "sources": {
                    "hook_events": sum(
                        1 for e in events if e["source"] == "hook_event"
                    ),
                    "spike_logs": sum(1 for e in events if e["source"] == "spike_log"),
                    "delegations": sum(
                        1 for e in events if e["source"] == "delegation"
                    ),
                },
                "events": events,
                "limitations": {
                    "note": "Subagent tool activity not tracked (Claude Code limitation)",
                    "github_issue": "https://github.com/anthropics/claude-code/issues/14859",
                    "workaround": "SubagentStop hook captures completion, SDK logging captures results",
                },
            }

            # Cache the result
            cache.set(cache_key, result)
            query_time_ms = (time.time() - query_start_time) * 1000
            cache.record_metric(cache_key, exec_time_ms, cache_hit=False)

            return result

        finally:
            await db.close()

    # ========== HELPER: Grouped Events Logic ==========

    async def _get_events_grouped_by_prompt_impl(
        db: aiosqlite.Connection, cache: QueryCache, limit: int = 50
    ) -> dict[str, Any]:
        """
        Implementation helper: Return activity events grouped by user prompt (conversation turns).

        Each conversation turn includes:
        - userQuery: The original UserQuery event with prompt text
        - children: All child events triggered by this prompt
        - stats: Aggregated statistics for the conversation turn

        Args:
            db: Database connection
            cache: Query cache instance
            limit: Maximum number of conversation turns to return (default 50)

        Returns:
            Dictionary with conversation turns and metadata
        """
        query_start_time = time.time()

        try:
            # Create cache key
            cache_key = f"events_grouped_by_prompt:{limit}"

            # Check cache first
            cached_result = cache.get(cache_key)
            if cached_result is not None:
                query_time_ms = (time.time() - query_start_time) * 1000
                cache.record_metric(cache_key, query_time_ms, cache_hit=True)
                logger.debug(
                    f"Cache HIT for events_grouped_by_prompt (key={cache_key}, time={query_time_ms:.2f}ms)"
                )
                return cached_result  # type: ignore[no-any-return]

            exec_start = time.time()

            # Step 1: Query UserQuery events (most recent first)
            user_query_query = """
                SELECT
                    event_id,
                    timestamp,
                    input_summary,
                    execution_duration_seconds,
                    status,
                    agent_id
                FROM agent_events
                WHERE tool_name = 'UserQuery'
                ORDER BY timestamp DESC
                LIMIT ?
            """

            async with db.execute(user_query_query, [limit]) as cursor:
                user_query_rows = await cursor.fetchall()

            conversation_turns: list[dict[str, Any]] = []

            # Step 2: For each UserQuery, fetch child events
            for uq_row in user_query_rows:
                uq_event_id = uq_row[0]
                uq_timestamp = uq_row[1]
                uq_input = uq_row[2] or ""
                uq_duration = uq_row[3] or 0.0
                uq_status = uq_row[4]

                # Extract prompt text from input_summary
                # Since format_tool_summary now properly formats UserQuery events,
                # input_summary contains just the prompt text (preview up to 100 chars)
                prompt_text = uq_input

                # Step 2a: Query child events linked via parent_event_id
                children_query = """
                    SELECT
                        event_id,
                        tool_name,
                        timestamp,
                        input_summary,
                        execution_duration_seconds,
                        status,
                        agent_id,
                        model,
                        context,
                        subagent_type,
                        feature_id
                    FROM agent_events
                    WHERE parent_event_id = ?
                    ORDER BY timestamp DESC
                """

                # Recursive helper to fetch children at any depth
                async def fetch_children_recursive(
                    parent_id: str, depth: int = 0, max_depth: int = 4
                ) -> tuple[list[dict[str, Any]], float, int, int]:
                    """Recursively fetch children up to max_depth levels."""
                    if depth >= max_depth:
                        return [], 0.0, 0, 0

                    async with db.execute(children_query, [parent_id]) as cursor:
                        rows = await cursor.fetchall()

                    children_list: list[dict[str, Any]] = []
                    total_dur = 0.0
                    success_cnt = 0
                    error_cnt = 0

                    for row in rows:
                        evt_id = row[0]
                        tool = row[1]
                        timestamp = row[2]
                        input_text = row[3] or ""
                        duration = row[4] or 0.0
                        status = row[5]
                        agent = row[6] or "unknown"
                        model = row[7]
                        context_json = row[8]
                        subagent_type = row[9]
                        feature_id = row[10]

                        # Parse context to extract spawner metadata
                        context = {}
                        spawner_type = None
                        spawned_agent = None
                        if context_json:
                            try:
                                context = json.loads(context_json)
                                spawner_type = context.get("spawner_type")
                                spawned_agent = context.get("spawned_agent")
                            except (json.JSONDecodeError, TypeError):
                                pass

                        # If no spawner_type but subagent_type is set, treat it as a spawner delegation
                        # This handles both HeadlessSpawner (spawner_type in context) and
                        # Claude Code plugin agents (subagent_type field)
                        if not spawner_type and subagent_type:
                            # Extract spawner name from subagent_type (e.g., ".claude-plugin:gemini" -> "gemini")
                            if ":" in subagent_type:
                                spawner_type = subagent_type.split(":")[-1]
                            else:
                                spawner_type = subagent_type
                            spawned_agent = (
                                agent  # Use the agent_id as the spawned agent
                            )

                        # Build summary (input_text already contains formatted summary)
                        summary = input_text[:80] + (
                            "..." if len(input_text) > 80 else ""
                        )

                        # Recursively fetch this child's children
                        (
                            nested_children,
                            nested_dur,
                            nested_success,
                            nested_error,
                        ) = await fetch_children_recursive(evt_id, depth + 1, max_depth)

                        child_dict: dict[str, Any] = {
                            "event_id": evt_id,
                            "tool_name": tool,
                            "timestamp": timestamp,
                            "summary": summary,
                            "duration_seconds": round(duration, 2),
                            "agent": agent,
                            "depth": depth,
                            "model": model,
                            "feature_id": feature_id,
                        }

                        # Include spawner metadata if present
                        if spawner_type:
                            child_dict["spawner_type"] = spawner_type
                        if spawned_agent:
                            child_dict["spawned_agent"] = spawned_agent
                        if subagent_type:
                            child_dict["subagent_type"] = subagent_type

                        # Only add children key if there are nested children
                        if nested_children:
                            child_dict["children"] = nested_children

                        children_list.append(child_dict)

                        # Update stats (include nested)
                        total_dur += duration + nested_dur
                        if status == "recorded" or status == "success":
                            success_cnt += 1
                        else:
                            error_cnt += 1
                        success_cnt += nested_success
                        error_cnt += nested_error

                    return children_list, total_dur, success_cnt, error_cnt

                # Step 3: Build child events with recursive nesting
                (
                    children,
                    children_duration,
                    children_success,
                    children_error,
                ) = await fetch_children_recursive(uq_event_id, depth=0, max_depth=4)

                total_duration = uq_duration + children_duration
                success_count = (
                    1 if uq_status == "recorded" or uq_status == "success" else 0
                ) + children_success
                error_count = (
                    0 if uq_status == "recorded" or uq_status == "success" else 1
                ) + children_error

                # Check if any child has spawner metadata
                def has_spawner_in_children(
                    children_list: list[dict[str, Any]],
                ) -> bool:
                    """Recursively check if any child has spawner metadata."""
                    for child in children_list:
                        if child.get("spawner_type") or child.get("spawned_agent"):
                            return True
                        if child.get("children") and has_spawner_in_children(
                            child["children"]
                        ):
                            return True
                    return False

                has_spawner = has_spawner_in_children(children)

                # Count total tool calls including all nested levels
                def count_total_children(
                    children_list: list[dict[str, Any]],
                ) -> int:
                    """Recursively count all children at all nesting levels."""
                    total = len(children_list)
                    for child in children_list:
                        if child.get("children"):
                            total += count_total_children(child["children"])
                    return total

                total_tool_count = count_total_children(children)

                # Step 4: Build conversation turn object
                conversation_turn = {
                    "userQuery": {
                        "event_id": uq_event_id,
                        "timestamp": uq_timestamp,
                        "prompt": prompt_text[:200],  # Truncate for display
                        "duration_seconds": round(uq_duration, 2),
                        "agent_id": uq_row[5],  # Include agent_id from UserQuery
                    },
                    "children": children,
                    "has_spawner": has_spawner,
                    "stats": {
                        "tool_count": total_tool_count,
                        "total_duration": round(total_duration, 2),
                        "success_count": success_count,
                        "error_count": error_count,
                    },
                }

                conversation_turns.append(conversation_turn)

            exec_time_ms = (time.time() - exec_start) * 1000

            # Build response
            result = {
                "timestamp": datetime.now().isoformat(),
                "total_turns": len(conversation_turns),
                "conversation_turns": conversation_turns,
                "note": "Groups events by UserQuery prompt (conversation turn). Child events are linked via parent_event_id.",
            }

            # Cache the result
            cache.set(cache_key, result)
            query_time_ms = (time.time() - query_start_time) * 1000
            cache.record_metric(cache_key, exec_time_ms, cache_hit=False)
            logger.debug(
                f"Cache MISS for events_grouped_by_prompt (key={cache_key}, "
                f"db_time={exec_time_ms:.2f}ms, total_time={query_time_ms:.2f}ms, "
                f"turns={len(conversation_turns)})"
            )

            return result

        except Exception as e:
            logger.error(f"Error in _get_events_grouped_by_prompt_impl: {e}")
            raise

    # ========== EVENTS GROUPED BY PROMPT ENDPOINT ==========

    @app.get("/api/events-grouped-by-prompt")
    async def events_grouped_by_prompt(limit: int = 50) -> dict[str, Any]:
        """
        Return activity events grouped by user prompt (conversation turns).

        Each conversation turn includes:
        - userQuery: The original UserQuery event with prompt text
        - children: All child events triggered by this prompt
        - stats: Aggregated statistics for the conversation turn

        Args:
            limit: Maximum number of conversation turns to return (default 50)

        Returns:
            Dictionary with conversation_turns list and metadata
        """
        db = await get_db()
        cache = app.state.query_cache

        try:
            return await _get_events_grouped_by_prompt_impl(db, cache, limit)
        finally:
            await db.close()

    # ========== SESSIONS API ENDPOINT ==========

    @app.get("/api/sessions")
    async def get_sessions(
        status: str | None = None,
        limit: int = 50,
        offset: int = 0,
    ) -> dict[str, Any]:
        """Get sessions from the database.

        Args:
            status: Filter by session status (e.g., 'active', 'completed')
            limit: Maximum number of sessions to return (default 50)
            offset: Number of sessions to skip (default 0)

        Returns:
            {
                "total": int,
                "limit": int,
                "offset": int,
                "sessions": [
                    {
                        "session_id": str,
                        "agent": str | None,
                        "continued_from": str | None,
                        "started_at": str,
                        "status": str,
                        "start_commit": str | None,
                        "ended_at": str | None
                    }
                ]
            }
        """
        db = await get_db()
        cache = app.state.query_cache
        query_start_time = time.time()

        try:
            # Create cache key from query parameters
            cache_key = f"api_sessions:{status or 'all'}:{limit}:{offset}"

            # Check cache first
            cached_result = cache.get(cache_key)
            if cached_result is not None:
                query_time_ms = (time.time() - query_start_time) * 1000
                cache.record_metric(cache_key, query_time_ms, cache_hit=True)
                logger.debug(
                    f"Cache HIT for api_sessions (key={cache_key}, time={query_time_ms:.2f}ms)"
                )
                return cached_result  # type: ignore[no-any-return]

            exec_start = time.time()

            # Build query with optional status filter
            # Note: Database uses agent_assigned, created_at, and completed_at
            query = """
                SELECT
                    session_id,
                    agent_assigned,
                    continued_from,
                    created_at,
                    status,
                    start_commit,
                    completed_at
                FROM sessions
                WHERE 1=1
            """
            params: list[Any] = []

            if status:
                query += " AND status = ?"
                params.append(status)

            query += " ORDER BY created_at DESC LIMIT ? OFFSET ?"
            params.extend([limit, offset])

            async with db.execute(query, params) as cursor:
                rows = await cursor.fetchall()

            # Get total count for pagination
            count_query = "SELECT COUNT(*) FROM sessions WHERE 1=1"
            count_params: list[Any] = []
            if status:
                count_query += " AND status = ?"
                count_params.append(status)

            async with db.execute(count_query, count_params) as count_cursor:
                count_row = await count_cursor.fetchone()
            total = int(count_row[0]) if count_row else 0

            # Build session objects
            # Map schema columns to API response fields for backward compatibility
            sessions = []
            for row in rows:
                sessions.append(
                    {
                        "session_id": row[0],
                        "agent": row[1],  # agent_assigned -> agent for API compat
                        "continued_from": row[2],
                        "created_at": row[3],  # created_at timestamp
                        "status": row[4] or "unknown",
                        "start_commit": row[5],
                        "completed_at": row[6],  # completed_at timestamp
                    }
                )

            exec_time_ms = (time.time() - exec_start) * 1000

            result = {
                "total": total,
                "limit": limit,
                "offset": offset,
                "sessions": sessions,
            }

            # Cache the result
            cache.set(cache_key, result)
            query_time_ms = (time.time() - query_start_time) * 1000
            cache.record_metric(cache_key, exec_time_ms, cache_hit=False)
            logger.debug(
                f"Cache MISS for api_sessions (key={cache_key}, "
                f"db_time={exec_time_ms:.2f}ms, total_time={query_time_ms:.2f}ms, "
                f"sessions={len(sessions)})"
            )

            return result

        finally:
            await db.close()

    # ========== ORCHESTRATION ENDPOINTS ==========

    @app.get("/views/orchestration", response_class=HTMLResponse)
    async def orchestration_view(request: Request) -> HTMLResponse:
        """Get delegation chains and agent handoffs as HTMX partial."""
        db = await get_db()
        try:
            # Query delegation events from agent_events table
            # Use same query as API endpoint - filter by tool_name = 'Task'
            query = """
                SELECT
                    event_id,
                    agent_id as from_agent,
                    subagent_type as to_agent,
                    timestamp,
                    input_summary,
                    session_id,
                    status
                FROM agent_events
                WHERE tool_name = 'Task'
                ORDER BY timestamp DESC
                LIMIT 50
            """

            async with db.execute(query) as cursor:
                rows = list(await cursor.fetchall())
            logger.debug(f"orchestration_view: Query executed, got {len(rows)} rows")

            delegations = []
            for row in rows:
                from_agent = row[1] or "unknown"
                to_agent = row[2]  # May be NULL
                task_summary = row[4] or ""

                # Extract to_agent from input_summary JSON if NULL
                if not to_agent:
                    try:
                        input_data = json.loads(task_summary) if task_summary else {}
                        to_agent = input_data.get("subagent_type", "unknown")
                    except Exception:
                        to_agent = "unknown"

                delegation = {
                    "event_id": row[0],
                    "from_agent": from_agent,
                    "to_agent": to_agent,
                    "timestamp": row[3],
                    "task": task_summary or "Unnamed task",
                    "session_id": row[5],
                    "status": row[6] or "pending",
                    "result": "",  # Not available in agent_events
                }
                delegations.append(delegation)

            logger.debug(
                f"orchestration_view: Created {len(delegations)} delegation dicts"
            )

            return templates.TemplateResponse(
                "partials/orchestration.html",
                {
                    "request": request,
                    "delegations": delegations,
                },
            )
        except Exception as e:
            logger.error(f"orchestration_view ERROR: {e}")
            raise
        finally:
            await db.close()

    @app.get("/api/orchestration")
    async def orchestration_api() -> dict[str, Any]:
        """Get delegation chains and agent coordination information as JSON.

        Returns:
            {
                "delegation_count": int,
                "unique_agents": int,
                "agents": [str],
                "delegation_chains": {
                    "from_agent": [
                        {
                            "to_agent": str,
                            "event_type": str,
                            "timestamp": str,
                            "task": str,
                            "status": str
                        }
                    ]
                }
            }
        """
        db = await get_db()
        try:
            # Query delegation events from agent_events table
            # Filter by tool_name = 'Task' (not event_type)
            query = """
                SELECT
                    event_id,
                    agent_id as from_agent,
                    subagent_type as to_agent,
                    timestamp,
                    input_summary,
                    status
                FROM agent_events
                WHERE tool_name = 'Task'
                ORDER BY timestamp DESC
                LIMIT 1000
            """

            cursor = await db.execute(query)
            rows = await cursor.fetchall()

            # Build delegation chains grouped by from_agent
            delegation_chains: dict[str, list[dict[str, Any]]] = {}
            agents = set()
            delegation_count = 0

            for row in rows:
                from_agent = row[1] or "unknown"
                to_agent = row[2]  # May be NULL
                timestamp = row[3] or ""
                task_summary = row[4] or ""
                status = row[5] or "pending"

                # Extract to_agent from input_summary JSON if NULL
                if not to_agent:
                    try:
                        import json

                        input_data = json.loads(task_summary) if task_summary else {}
                        to_agent = input_data.get("subagent_type", "unknown")
                    except Exception:
                        to_agent = "unknown"

                agents.add(from_agent)
                agents.add(to_agent)
                delegation_count += 1

                if from_agent not in delegation_chains:
                    delegation_chains[from_agent] = []

                delegation_chains[from_agent].append(
                    {
                        "to_agent": to_agent,
                        "event_type": "delegation",
                        "timestamp": timestamp,
                        "task": task_summary or "Unnamed task",
                        "status": status,
                    }
                )

            return {
                "delegation_count": delegation_count,
                "unique_agents": len(agents),
                "agents": sorted(list(agents)),
                "delegation_chains": delegation_chains,
            }

        except Exception as e:
            logger.error(f"Failed to get orchestration data: {e}")
            raise
        finally:
            await db.close()

    @app.get("/api/orchestration/delegations")
    async def orchestration_delegations_api() -> dict[str, Any]:
        """Get delegation statistics and chains as JSON.

        This endpoint is used by the dashboard JavaScript to display
        delegation metrics in the orchestration panel.

        Returns:
            {
                "delegation_count": int,
                "unique_agents": int,
                "delegation_chains": {
                    "from_agent": [
                        {
                            "to_agent": str,
                            "timestamp": str,
                            "task": str,
                            "status": str
                        }
                    ]
                }
            }
        """
        db = await get_db()
        cache = app.state.query_cache
        query_start_time = time.time()

        try:
            # Create cache key
            cache_key = "orchestration_delegations:all"

            # Check cache first
            cached_result = cache.get(cache_key)
            if cached_result is not None:
                query_time_ms = (time.time() - query_start_time) * 1000
                cache.record_metric(cache_key, query_time_ms, cache_hit=True)
                logger.debug(
                    f"Cache HIT for orchestration_delegations (key={cache_key}, "
                    f"time={query_time_ms:.2f}ms)"
                )
                return cached_result  # type: ignore[no-any-return]

            exec_start = time.time()

            # Query delegation events from agent_events table
            # Filter by tool_name = 'Task' to get Task() delegations
            query = """
                SELECT
                    event_id,
                    agent_id as from_agent,
                    subagent_type as to_agent,
                    timestamp,
                    input_summary,
                    status
                FROM agent_events
                WHERE tool_name = 'Task'
                ORDER BY timestamp DESC
                LIMIT 1000
            """

            cursor = await db.execute(query)
            rows = await cursor.fetchall()

            # Build delegation chains grouped by from_agent
            delegation_chains: dict[str, list[dict[str, Any]]] = {}
            agents = set()
            delegation_count = 0

            for row in rows:
                from_agent = row[1] or "unknown"
                to_agent = row[2]  # May be NULL
                timestamp = row[3] or ""
                task_summary = row[4] or ""
                status = row[5] or "pending"

                # Extract to_agent from input_summary JSON if NULL
                if not to_agent:
                    try:
                        input_data = json.loads(task_summary) if task_summary else {}
                        to_agent = input_data.get("subagent_type", "unknown")
                    except Exception:
                        to_agent = "unknown"

                agents.add(from_agent)
                agents.add(to_agent)
                delegation_count += 1

                if from_agent not in delegation_chains:
                    delegation_chains[from_agent] = []

                delegation_chains[from_agent].append(
                    {
                        "to_agent": to_agent,
                        "timestamp": timestamp,
                        "task": task_summary or "Unnamed task",
                        "status": status,
                    }
                )

            exec_time_ms = (time.time() - exec_start) * 1000

            result = {
                "delegation_count": delegation_count,
                "unique_agents": len(agents),
                "delegation_chains": delegation_chains,
            }

            # Cache the result
            cache.set(cache_key, result)
            query_time_ms = (time.time() - query_start_time) * 1000
            cache.record_metric(cache_key, exec_time_ms, cache_hit=False)
            logger.debug(
                f"Cache MISS for orchestration_delegations (key={cache_key}, "
                f"db_time={exec_time_ms:.2f}ms, total_time={query_time_ms:.2f}ms, "
                f"delegations={delegation_count})"
            )

            return result

        except Exception as e:
            logger.error(f"Failed to get orchestration delegations: {e}")
            raise
        finally:
            await db.close()

    # ========== WORK ITEMS ENDPOINTS ==========

    @app.get("/views/features", response_class=HTMLResponse)
    async def features_view_redirect(
        request: Request, status: str = "all"
    ) -> HTMLResponse:
        """Redirect to work-items view (legacy endpoint for backward compatibility)."""
        return await work_items_view(request, status)

    @app.get("/views/work-items", response_class=HTMLResponse)
    async def work_items_view(request: Request, status: str = "all") -> HTMLResponse:
        """Get work items (features, bugs, spikes) by status as HTMX partial."""
        db = await get_db()
        cache = app.state.query_cache
        query_start_time = time.time()

        try:
            # Create cache key from query parameters
            cache_key = f"work_items_view:{status}"

            # Check cache first
            cached_response = cache.get(cache_key)
            work_items_by_status: dict = {
                "todo": [],
                "in_progress": [],
                "blocked": [],
                "done": [],
            }

            if cached_response is not None:
                query_time_ms = (time.time() - query_start_time) * 1000
                cache.record_metric(cache_key, query_time_ms, cache_hit=True)
                logger.debug(
                    f"Cache HIT for work_items_view (key={cache_key}, time={query_time_ms:.2f}ms)"
                )
                work_items_by_status = cached_response
            else:
                # OPTIMIZATION: Use composite index idx_features_status_priority
                # for efficient filtering and ordering
                query = """
                    SELECT id, type, title, status, priority, assigned_to, created_at, updated_at, description
                    FROM features
                    WHERE 1=1
                """
                params: list = []

                if status != "all":
                    query += " AND status = ?"
                    params.append(status)

                query += " ORDER BY priority DESC, created_at DESC LIMIT 1000"

                exec_start = time.time()
                cursor = await db.execute(query, params)
                rows = await cursor.fetchall()

                # Query all unique agents per feature for attribution chain
                # This only works for events that have feature_id populated
                agents_query = """
                    SELECT feature_id, agent_id
                    FROM agent_events
                    WHERE feature_id IS NOT NULL
                    GROUP BY feature_id, agent_id
                """
                agents_cursor = await db.execute(agents_query)
                agents_rows = await agents_cursor.fetchall()

                feature_agents: dict[str, list[str]] = {}
                for row in agents_rows:
                    fid, aid = row[0], row[1]
                    if fid not in feature_agents:
                        feature_agents[fid] = []
                    feature_agents[fid].append(aid)

                exec_time_ms = (time.time() - exec_start) * 1000

                for row in rows:
                    item_id = row[0]
                    item_status = row[3]
                    work_items_by_status.setdefault(item_status, []).append(
                        {
                            "id": item_id,
                            "type": row[1],
                            "title": row[2],
                            "status": item_status,
                            "priority": row[4],
                            "assigned_to": row[5],
                            "created_at": row[6],
                            "updated_at": row[7],
                            "description": row[8],
                            "contributors": feature_agents.get(item_id, []),
                        }
                    )

                # Cache the results
                cache.set(cache_key, work_items_by_status)
                query_time_ms = (time.time() - query_start_time) * 1000
                cache.record_metric(cache_key, exec_time_ms, cache_hit=False)
                logger.debug(
                    f"Cache MISS for work_items_view (key={cache_key}, "
                    f"db_time={exec_time_ms:.2f}ms, total_time={query_time_ms:.2f}ms)"
                )

            return templates.TemplateResponse(
                "partials/work-items.html",
                {
                    "request": request,
                    "work_items_by_status": work_items_by_status,
                },
            )
        finally:
            await db.close()

    # ========== SPAWNERS ENDPOINTS ==========

    @app.get("/views/spawners", response_class=HTMLResponse)
    async def spawners_view(request: Request) -> HTMLResponse:
        """Get spawner activity dashboard as HTMX partial."""
        db = await get_db()
        try:
            # Get spawner statistics
            stats_response = await get_spawner_statistics()
            spawner_stats = stats_response.get("spawner_statistics", [])

            # Get recent spawner activities
            activities_response = await get_spawner_activities(limit=50)
            recent_activities = activities_response.get("spawner_activities", [])

            return templates.TemplateResponse(
                "partials/spawners.html",
                {
                    "request": request,
                    "spawner_stats": spawner_stats,
                    "recent_activities": recent_activities,
                },
            )
        except Exception as e:
            logger.error(f"spawners_view ERROR: {e}")
            return templates.TemplateResponse(
                "partials/spawners.html",
                {
                    "request": request,
                    "spawner_stats": [],
                    "recent_activities": [],
                },
            )
        finally:
            await db.close()

    # ========== METRICS ENDPOINTS ==========

    @app.get("/views/metrics", response_class=HTMLResponse)
    async def metrics_view(request: Request) -> HTMLResponse:
        """Get session metrics and performance data as HTMX partial."""
        db = await get_db()
        cache = app.state.query_cache
        query_start_time = time.time()

        try:
            # Create cache key for metrics view
            cache_key = "metrics_view:all"

            # Check cache first
            cached_response = cache.get(cache_key)
            if cached_response is not None:
                query_time_ms = (time.time() - query_start_time) * 1000
                cache.record_metric(cache_key, query_time_ms, cache_hit=True)
                logger.debug(
                    f"Cache HIT for metrics_view (key={cache_key}, time={query_time_ms:.2f}ms)"
                )
                sessions, stats = cached_response
            else:
                # OPTIMIZATION: Combine session data with event counts in single query
                # This eliminates N+1 query problem (was 20+ queries, now 2)
                # Note: Database uses created_at and completed_at (not started_at/ended_at)
                query = """
                    SELECT
                        s.session_id,
                        s.agent_assigned,
                        s.status,
                        s.created_at,
                        s.completed_at,
                        COUNT(DISTINCT e.event_id) as event_count
                    FROM sessions s
                    LEFT JOIN agent_events e ON s.session_id = e.session_id
                    GROUP BY s.session_id
                    ORDER BY s.created_at DESC
                    LIMIT 20
                """

                exec_start = time.time()
                cursor = await db.execute(query)
                rows = await cursor.fetchall()
                exec_time_ms = (time.time() - exec_start) * 1000

                sessions = []
                for row in rows:
                    started_at = datetime.fromisoformat(row[3])

                    # Calculate duration
                    if row[4]:
                        ended_at = datetime.fromisoformat(row[4])
                        duration_seconds = (ended_at - started_at).total_seconds()
                    else:
                        # Use UTC to handle timezone-aware datetime comparison
                        now = (
                            datetime.now(started_at.tzinfo)
                            if started_at.tzinfo
                            else datetime.now()
                        )
                        duration_seconds = (now - started_at).total_seconds()

                    sessions.append(
                        {
                            "session_id": row[0],
                            "agent": row[1],
                            "status": row[2],
                            "started_at": row[3],
                            "ended_at": row[4],
                            "event_count": int(row[5]) if row[5] else 0,
                            "duration_seconds": duration_seconds,
                        }
                    )

                # OPTIMIZATION: Combine all stats in single query instead of subqueries
                # This reduces query count from 4 subqueries + 1 main to just 1
                stats_query = """
                    SELECT
                        (SELECT COUNT(*) FROM agent_events) as total_events,
                        (SELECT COUNT(DISTINCT agent_id) FROM agent_events) as total_agents,
                        (SELECT COUNT(*) FROM sessions) as total_sessions,
                        (SELECT COUNT(*) FROM features) as total_features
                """

                stats_cursor = await db.execute(stats_query)
                stats_row = await stats_cursor.fetchone()

                if stats_row:
                    stats = {
                        "total_events": int(stats_row[0]) if stats_row[0] else 0,
                        "total_agents": int(stats_row[1]) if stats_row[1] else 0,
                        "total_sessions": int(stats_row[2]) if stats_row[2] else 0,
                        "total_features": int(stats_row[3]) if stats_row[3] else 0,
                    }
                else:
                    stats = {
                        "total_events": 0,
                        "total_agents": 0,
                        "total_sessions": 0,
                        "total_features": 0,
                    }

                # Cache the results
                cache_data = (sessions, stats)
                cache.set(cache_key, cache_data)
                query_time_ms = (time.time() - query_start_time) * 1000
                cache.record_metric(cache_key, exec_time_ms, cache_hit=False)
                logger.debug(
                    f"Cache MISS for metrics_view (key={cache_key}, "
                    f"db_time={exec_time_ms:.2f}ms, total_time={query_time_ms:.2f}ms)"
                )

            # Provide default values for metrics template variables
            # These prevent Jinja2 UndefinedError for variables the template expects
            exec_time_dist = {
                "very_fast": 0,
                "fast": 0,
                "medium": 0,
                "slow": 0,
                "very_slow": 0,
            }

            # Count active sessions from the fetched sessions
            active_sessions = sum(1 for s in sessions if s.get("status") == "active")

            # Default token stats (empty until we compute real values)
            token_stats = {
                "total_tokens": 0,
                "avg_per_event": 0,
                "peak_usage": 0,
                "estimated_cost": 0.0,
            }

            # Default activity timeline (last 24 hours with 0 counts)
            activity_timeline = {str(h): 0 for h in range(24)}
            max_hourly_count = 1  # Avoid division by zero in template

            # Default agent performance (empty list)
            agent_performance: list[dict[str, str | float]] = []

            # Default system health metrics
            error_rate = 0.0
            avg_response_time = 0.5  # seconds

            return templates.TemplateResponse(
                "partials/metrics.html",
                {
                    "request": request,
                    "sessions": sessions,
                    "stats": stats,
                    "exec_time_dist": exec_time_dist,
                    "active_sessions": active_sessions,
                    "token_stats": token_stats,
                    "activity_timeline": activity_timeline,
                    "max_hourly_count": max_hourly_count,
                    "agent_performance": agent_performance,
                    "error_rate": error_rate,
                    "avg_response_time": avg_response_time,
                },
            )
        finally:
            await db.close()

    # ========== SPAWNER OBSERVABILITY ENDPOINTS ==========

    @app.get("/api/spawner-activities")
    async def get_spawner_activities(
        spawner_type: str | None = None,
        session_id: str | None = None,
        limit: int = 100,
        offset: int = 0,
    ) -> dict[str, Any]:
        """
        Get spawner delegation activities with clear attribution.

        Returns events where spawner_type IS NOT NULL, ordered by recency.
        Shows which orchestrator delegated to which spawned AI.

        Args:
            spawner_type: Filter by spawner type (gemini, codex, copilot)
            session_id: Filter by session
            limit: Maximum results (default 100)
            offset: Result offset for pagination

        Returns:
            Dict with spawner_activities array and metadata
        """
        db = await get_db()
        cache = app.state.query_cache
        query_start_time = time.time()

        try:
            # Create cache key
            cache_key = f"spawner_activities:{spawner_type or 'all'}:{session_id or 'all'}:{limit}:{offset}"

            # Check cache first
            cached_result = cache.get(cache_key)
            if cached_result is not None:
                query_time_ms = (time.time() - query_start_time) * 1000
                cache.record_metric(cache_key, query_time_ms, cache_hit=True)
                return cached_result  # type: ignore[no-any-return]

            exec_start = time.time()

            query = """
                SELECT
                    event_id,
                    agent_id AS orchestrator_agent,
                    spawner_type,
                    subagent_type AS spawned_agent,
                    tool_name,
                    input_summary AS task,
                    output_summary AS result,
                    status,
                    execution_duration_seconds AS duration,
                    cost_tokens AS tokens,
                    cost_usd,
                    child_spike_count AS artifacts,
                    timestamp,
                    created_at
                FROM agent_events
                WHERE spawner_type IS NOT NULL
            """

            params: list[Any] = []
            if spawner_type:
                query += " AND spawner_type = ?"
                params.append(spawner_type)
            if session_id:
                query += " AND session_id = ?"
                params.append(session_id)

            query += " ORDER BY timestamp DESC LIMIT ? OFFSET ?"
            params.extend([limit, offset])

            cursor = await db.execute(query, params)
            events = [
                dict(zip([c[0] for c in cursor.description], row))
                for row in await cursor.fetchall()
            ]

            # Get total count
            count_query = (
                "SELECT COUNT(*) FROM agent_events WHERE spawner_type IS NOT NULL"
            )
            count_params: list[Any] = []
            if spawner_type:
                count_query += " AND spawner_type = ?"
                count_params.append(spawner_type)
            if session_id:
                count_query += " AND session_id = ?"
                count_params.append(session_id)

            count_cursor = await db.execute(count_query, count_params)
            count_row = await count_cursor.fetchone()
            total_count = int(count_row[0]) if count_row else 0

            exec_time_ms = (time.time() - exec_start) * 1000

            result = {
                "spawner_activities": events,
                "count": len(events),
                "total": total_count,
                "offset": offset,
                "limit": limit,
            }

            # Cache the result
            cache.set(cache_key, result)
            query_time_ms = (time.time() - query_start_time) * 1000
            cache.record_metric(cache_key, exec_time_ms, cache_hit=False)
            logger.debug(
                f"Cache MISS for spawner_activities (key={cache_key}, "
                f"db_time={exec_time_ms:.2f}ms, total_time={query_time_ms:.2f}ms, "
                f"activities={len(events)})"
            )

            return result
        finally:
            await db.close()

    @app.get("/api/spawner-statistics")
    async def get_spawner_statistics(session_id: str | None = None) -> dict[str, Any]:
        """
        Get aggregated statistics for each spawner type.

        Shows delegations, success rate, average duration, token usage, and costs
        broken down by spawner type (Gemini, Codex, Copilot).

        Args:
            session_id: Filter by session (optional)

        Returns:
            Dict with spawner_statistics array
        """
        db = await get_db()
        cache = app.state.query_cache
        query_start_time = time.time()

        try:
            # Create cache key
            cache_key = f"spawner_statistics:{session_id or 'all'}"

            # Check cache first
            cached_result = cache.get(cache_key)
            if cached_result is not None:
                query_time_ms = (time.time() - query_start_time) * 1000
                cache.record_metric(cache_key, query_time_ms, cache_hit=True)
                return cached_result  # type: ignore[no-any-return]

            exec_start = time.time()

            query = """
                SELECT
                    spawner_type,
                    COUNT(*) as total_delegations,
                    SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END) as successful,
                    ROUND(100.0 * SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END) / COUNT(*), 1) as success_rate,
                    ROUND(AVG(execution_duration_seconds), 2) as avg_duration,
                    SUM(cost_tokens) as total_tokens,
                    ROUND(SUM(cost_usd), 2) as total_cost,
                    MIN(timestamp) as first_used,
                    MAX(timestamp) as last_used
                FROM agent_events
                WHERE spawner_type IS NOT NULL
            """

            params: list[Any] = []
            if session_id:
                query += " AND session_id = ?"
                params.append(session_id)

            query += " GROUP BY spawner_type ORDER BY total_delegations DESC"

            cursor = await db.execute(query, params)
            stats = [
                dict(zip([c[0] for c in cursor.description], row))
                for row in await cursor.fetchall()
            ]

            exec_time_ms = (time.time() - exec_start) * 1000

            result = {"spawner_statistics": stats}

            # Cache the result
            cache.set(cache_key, result)
            query_time_ms = (time.time() - query_start_time) * 1000
            cache.record_metric(cache_key, exec_time_ms, cache_hit=False)
            logger.debug(
                f"Cache MISS for spawner_statistics (key={cache_key}, "
                f"db_time={exec_time_ms:.2f}ms, total_time={query_time_ms:.2f}ms)"
            )

            return result
        finally:
            await db.close()

    # ========== WEBSOCKET FOR REAL-TIME UPDATES ==========

    @app.websocket("/ws/events")
    async def websocket_events(websocket: WebSocket, since: str | None = None) -> None:
        """WebSocket endpoint for real-time event streaming.

        OPTIMIZATION: Uses timestamp-based filtering to minimize data transfers.
        The timestamp > ? filter with DESC index makes queries O(log n) instead of O(n).

        FIX 3: Now supports loading historical events via 'since' parameter.
        - If 'since' provided: Load events from that timestamp onwards
        - If 'since' not provided: Load events from last 1 hour (default)
        - After historical load: Continue streaming real-time events

        LIVE EVENTS: Also polls live_events table for real-time spawner activity
        streaming. These events are marked as broadcast after sending and cleaned up.

        Args:
            since: Optional ISO timestamp to start streaming from (e.g., "2025-01-16T10:00:00")
                   If not provided, defaults to 1 hour ago
        """
        await websocket.accept()

        # FIX 3: Determine starting timestamp
        if since:
            try:
                # Validate timestamp format
                datetime.fromisoformat(since.replace("Z", "+00:00"))
                last_timestamp = since
            except (ValueError, AttributeError):
                # Invalid timestamp - default to 24 hours ago
                last_timestamp = (datetime.now() - timedelta(hours=24)).isoformat()
        else:
            # Default: Load events from last 24 hours (captures all recent events in typical workflow)
            last_timestamp = (datetime.now() - timedelta(hours=24)).isoformat()

        # FIX 3: Load historical events first (before real-time streaming)
        db = await get_db()
        try:
            historical_query = """
                SELECT event_id, agent_id, event_type, timestamp, tool_name,
                       input_summary, output_summary, session_id, status, model,
                       parent_event_id, execution_duration_seconds, context,
                       cost_tokens
                FROM agent_events
                WHERE timestamp >= ? AND timestamp < datetime('now')
                ORDER BY timestamp DESC
                LIMIT 1000
            """
            cursor = await db.execute(historical_query, [last_timestamp])
            historical_rows = await cursor.fetchall()

            # Send historical events first
            if historical_rows:
                historical_rows_list = list(historical_rows)
                for row in historical_rows_list:
                    row_list = list(row)
                    # Parse context JSON if present
                    context_data = {}
                    if row_list[12]:  # context column
                        try:
                            context_data = json.loads(row_list[12])
                        except (json.JSONDecodeError, TypeError):
                            pass

                    event_data = {
                        "type": "event",
                        "event_id": row_list[0],
                        "agent_id": row_list[1] or "unknown",
                        "event_type": row_list[2],
                        "timestamp": row_list[3],
                        "tool_name": row_list[4],
                        "input_summary": row_list[5],
                        "output_summary": row_list[6],
                        "session_id": row_list[7],
                        "status": row_list[8],
                        "model": row_list[9],
                        "parent_event_id": row_list[10],
                        "execution_duration_seconds": row_list[11] or 0.0,
                        "cost_tokens": row_list[13] or 0,
                        "context": context_data,
                    }
                    await websocket.send_json(event_data)

                # Update last_timestamp to last historical event
                last_timestamp = historical_rows_list[-1][3]

        except Exception as e:
            logger.error(f"Error loading historical events: {e}")
        finally:
            await db.close()

        # Update to current time for real-time streaming
        last_timestamp = datetime.now().isoformat()
        poll_interval = 0.5  # OPTIMIZATION: Adaptive polling (reduced from 1s)
        last_live_event_id = 0  # Track last broadcast live event ID

        try:
            while True:
                db = await get_db()
                has_activity = False
                try:
                    # ===== 1. Poll agent_events (existing logic) =====
                    # OPTIMIZATION: Only select needed columns, use DESC index
                    # Pattern uses index: idx_agent_events_timestamp DESC
                    # Only fetch events AFTER last_timestamp to stream new events only
                    query = """
                        SELECT event_id, agent_id, event_type, timestamp, tool_name,
                               input_summary, output_summary, session_id, status, model,
                               parent_event_id, execution_duration_seconds, context,
                               cost_tokens
                        FROM agent_events
                        WHERE timestamp > ?
                        ORDER BY timestamp DESC
                        LIMIT 100
                    """

                    cursor = await db.execute(query, [last_timestamp])
                    rows = await cursor.fetchall()

                    if rows:
                        has_activity = True
                        rows_list: list[list[Any]] = [list(row) for row in rows]
                        # Update last timestamp (last row since ORDER BY ts ASC)
                        last_timestamp = rows_list[-1][3]

                        # Send events in order (no need to reverse with ASC)
                        for event_row in rows_list:
                            # Parse context JSON if present
                            context_data = {}
                            if event_row[12]:  # context column
                                try:
                                    context_data = json.loads(event_row[12])
                                except (json.JSONDecodeError, TypeError):
                                    pass

                            event_data = {
                                "type": "event",
                                "event_id": event_row[0],
                                "agent_id": event_row[1] or "unknown",
                                "event_type": event_row[2],
                                "timestamp": event_row[3],
                                "tool_name": event_row[4],
                                "input_summary": event_row[5],
                                "output_summary": event_row[6],
                                "session_id": event_row[7],
                                "status": event_row[8],
                                "model": event_row[9],
                                "parent_event_id": event_row[10],
                                "execution_duration_seconds": event_row[11] or 0.0,
                                "cost_tokens": event_row[13] or 0,
                                "context": context_data,
                            }
                            await websocket.send_json(event_data)

                    # ===== 2. Poll live_events for spawner streaming =====
                    # Fetch pending live events that haven't been broadcast yet
                    live_query = """
                        SELECT id, event_type, event_data, parent_event_id,
                               session_id, spawner_type, created_at
                        FROM live_events
                        WHERE broadcast_at IS NULL AND id > ?
                        ORDER BY created_at ASC
                        LIMIT 50
                    """
                    live_cursor = await db.execute(live_query, [last_live_event_id])
                    live_rows = list(await live_cursor.fetchall())

                    if live_rows:
                        logger.info(
                            f"[WebSocket] Found {len(live_rows)} pending live_events to broadcast"
                        )
                        has_activity = True
                        broadcast_ids: list[int] = []

                        for live_row in live_rows:
                            live_id: int = live_row[0]
                            event_type: str = live_row[1]
                            event_data_json: str | None = live_row[2]
                            parent_event_id: str | None = live_row[3]
                            session_id: str | None = live_row[4]
                            spawner_type: str | None = live_row[5]
                            created_at: str = live_row[6]

                            # Parse event_data JSON
                            try:
                                event_data_parsed = (
                                    json.loads(event_data_json)
                                    if event_data_json
                                    else {}
                                )
                            except (json.JSONDecodeError, TypeError):
                                event_data_parsed = {}

                            # Send spawner event to client
                            spawner_event = {
                                "type": "spawner_event",
                                "live_event_id": live_id,
                                "event_type": event_type,
                                "spawner_type": spawner_type,
                                "parent_event_id": parent_event_id,
                                "session_id": session_id,
                                "timestamp": created_at,
                                "data": event_data_parsed,
                            }
                            logger.info(
                                f"[WebSocket] Sending spawner_event: id={live_id}, type={event_type}, spawner={spawner_type}"
                            )
                            await websocket.send_json(spawner_event)

                            broadcast_ids.append(live_id)
                            last_live_event_id = max(last_live_event_id, live_id)

                        # Mark events as broadcast
                        if broadcast_ids:
                            logger.info(
                                f"[WebSocket] Marking {len(broadcast_ids)} events as broadcast: {broadcast_ids}"
                            )
                            placeholders = ",".join("?" for _ in broadcast_ids)
                            await db.execute(
                                f"""
                                UPDATE live_events
                                SET broadcast_at = CURRENT_TIMESTAMP
                                WHERE id IN ({placeholders})
                                """,
                                broadcast_ids,
                            )
                            await db.commit()

                    # ===== 3. Periodic cleanup of old broadcast events =====
                    # Clean up events older than 5 minutes (every ~10 poll cycles)
                    if random.random() < 0.1:  # 10% chance each cycle
                        await db.execute(
                            """
                            DELETE FROM live_events
                            WHERE broadcast_at IS NOT NULL
                              AND created_at < datetime('now', '-5 minutes')
                            """
                        )
                        await db.commit()

                    # Adjust poll interval based on activity
                    if has_activity:
                        poll_interval = 0.3  # Speed up when active
                    else:
                        # No new events, increase poll interval (exponential backoff)
                        poll_interval = min(poll_interval * 1.2, 2.0)

                finally:
                    await db.close()

                # OPTIMIZATION: Reduced sleep interval for faster real-time updates
                await asyncio.sleep(poll_interval)

        except WebSocketDisconnect:
            logger.info("WebSocket client disconnected")
        except Exception as e:
            logger.error(f"WebSocket error: {e}")
            await websocket.close(code=1011)

    # ========================================================================
    # PRESENCE TRACKING API - Phase 1: Cross-Agent Presence
    # ========================================================================

    @app.get("/api/presence")
    async def get_all_presence() -> dict[str, Any]:
        """
        Get current presence state for all agents.

        Returns:
            Dictionary with agents list and timestamp
        """
        try:
            from htmlgraph.api.presence import PresenceManager

            presence_mgr = PresenceManager(db_path=db_path)
            agents = presence_mgr.get_all_presence()

            return {
                "agents": [agent.to_dict() for agent in agents],
                "timestamp": datetime.now().isoformat(),
            }
        except Exception as e:
            logger.error(f"Error getting presence: {e}")
            return {"agents": [], "timestamp": datetime.now().isoformat()}

    @app.get("/api/presence/{agent_id}")
    async def get_agent_presence(agent_id: str) -> dict[str, Any]:
        """
        Get presence for specific agent.

        Args:
            agent_id: Agent identifier

        Returns:
            Agent presence dictionary
        """
        try:
            from htmlgraph.api.presence import PresenceManager

            presence_mgr = PresenceManager(db_path=db_path)
            presence = presence_mgr.get_agent_presence(agent_id)

            if not presence:
                raise HTTPException(status_code=404, detail="Agent not found")

            return presence.to_dict()
        except HTTPException:
            raise
        except Exception as e:
            logger.error(f"Error getting agent presence: {e}")
            raise HTTPException(status_code=500, detail=str(e))

    @app.websocket("/ws/presence")
    async def websocket_presence(websocket: WebSocket) -> None:
        """
        WebSocket endpoint for real-time agent presence updates.

        Clients subscribe to receive:
        - Initial presence state for all agents
        - Updates when agents become active/idle/offline
        """
        client_id = f"presence-{int(time.time() * 1000)}"

        try:
            await websocket.accept()
            logger.info(f"Presence WebSocket client connected: {client_id}")

            # Send initial presence state
            from htmlgraph.api.presence import PresenceManager

            presence_mgr = PresenceManager(db_path=db_path)
            initial_presence = presence_mgr.get_all_presence()

            await websocket.send_json(
                {
                    "type": "presence_state",
                    "agents": [p.to_dict() for p in initial_presence],
                    "timestamp": datetime.now().isoformat(),
                }
            )

            # Poll for presence updates
            poll_interval = 1.0  # Check every second

            while True:
                await asyncio.sleep(poll_interval)

                # Get current presence
                current_presence = presence_mgr.get_all_presence()

                # Send update (client will diff against previous state)
                await websocket.send_json(
                    {
                        "type": "presence_update",
                        "agents": [p.to_dict() for p in current_presence],
                        "timestamp": datetime.now().isoformat(),
                    }
                )

        except WebSocketDisconnect:
            logger.info(f"Presence WebSocket client disconnected: {client_id}")
        except Exception as e:
            logger.error(f"Presence WebSocket error: {e}")
            try:
                await websocket.close(code=1011)
            except Exception:
                pass

    return app


# Create default app instance
def create_app(db_path: str | None = None) -> FastAPI:
    """Create FastAPI app with default database path."""
    if db_path is None:
        # Use htmlgraph.db - this is the main database with all events
        # Note: Changed from index.sqlite which was empty analytics cache
        db_path = str(Path.home() / ".htmlgraph" / "htmlgraph.db")

    return get_app(db_path)


# Export for uvicorn
app = create_app()
