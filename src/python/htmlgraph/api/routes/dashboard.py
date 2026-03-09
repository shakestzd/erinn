"""
Dashboard routes for HtmlGraph API.

Handles:
- Main dashboard view
- Activity feed (grouped by conversation turns)
- Agents view with workload stats
- Metrics view with session data
- Events API endpoints
"""

import asyncio
import json
import logging
import time
from datetime import datetime
from typing import Any, cast

from fastapi import APIRouter, Request
from fastapi.responses import HTMLResponse, StreamingResponse
from fastapi.templating import Jinja2Templates
from fastapi_cache.decorator import cache
from pydantic import BaseModel

from htmlgraph.api.cache import CACHE_TTL
from htmlgraph.api.dependencies import Dependencies

logger = logging.getLogger(__name__)

router = APIRouter()


class EventModel(BaseModel):
    """Event data model for API responses."""

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


# Templates will be set by main.py
_templates: Jinja2Templates | None = None
_deps: Dependencies | None = None


def init_dashboard_routes(templates: Jinja2Templates, deps: Dependencies) -> None:
    """Initialize dashboard routes with templates and dependencies."""
    global _templates, _deps
    _templates = templates
    _deps = deps


def get_templates() -> Jinja2Templates:
    """Get templates instance, raising error if not initialized."""
    if _templates is None:
        raise RuntimeError(
            "Dashboard routes not initialized. Call init_dashboard_routes first."
        )
    return _templates


def get_deps() -> Dependencies:
    """Get dependencies instance, raising error if not initialized."""
    if _deps is None:
        raise RuntimeError(
            "Dashboard routes not initialized. Call init_dashboard_routes first."
        )
    return _deps


@router.get("/", response_class=HTMLResponse)
async def dashboard(request: Request) -> HTMLResponse:
    """Main dashboard view with navigation tabs."""
    templates = get_templates()
    return templates.TemplateResponse(
        "dashboard-redesign.html",
        {
            "request": request,
            "title": "HtmlGraph Agent Observability",
        },
    )


@router.get("/views/agents", response_class=HTMLResponse)
async def agents_view(request: Request) -> HTMLResponse:
    """Get agent workload and performance stats as HTMX partial."""
    deps = get_deps()
    templates = get_templates()
    db = await deps.get_db()
    cache = deps.query_cache
    query_start_time = time.time()

    try:
        cache_key = "agents_view:all"
        cached_response = cache.get(cache_key)

        if cached_response is not None:
            query_time_ms = (time.time() - query_start_time) * 1000
            cache.record_metric(cache_key, query_time_ms, cache_hit=True)
            logger.debug(
                f"Cache HIT for agents_view (key={cache_key}, time={query_time_ms:.2f}ms)"
            )
            agents, total_actions, total_tokens = cached_response
        else:
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

            exec_start = time.time()
            async with db.execute(query) as cursor:
                rows = await cursor.fetchall()
            exec_time_ms = (time.time() - exec_start) * 1000

            agents = []
            total_actions = 0
            total_tokens = 0

            for row in rows:
                total_actions += row[1]
                total_tokens += row[2] or 0

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


@router.get("/views/activity-feed", response_class=HTMLResponse)
async def activity_feed(
    request: Request,
    limit: int = 15,
    session_id: str | None = None,
    agent_id: str | None = None,
) -> HTMLResponse:
    """Get latest agent events grouped by conversation turn (user prompt)."""
    deps = get_deps()
    templates = get_templates()
    db = await deps.get_db()

    try:
        activity_service, _, _ = deps.create_services(db)
        grouped_result = await activity_service.get_grouped_events(
            limit=limit,
            agent_id=agent_id,
            session_id=session_id,
        )

        # Build hierarchical_events list for the hierarchical template.
        # conversation_turns from get_grouped_events are already grouped by UserQuery;
        # each turn has a 'children' list which maps directly to the hierarchical format.
        conversation_turns = grouped_result.get("conversation_turns", [])

        def count_descendants(node: dict) -> int:
            """Recursively count all descendants of a node."""
            children = node.get("children") or []
            return len(children) + sum(count_descendants(ch) for ch in children)

        def build_child_node(c: dict) -> dict:
            """Recursively build a child node, preserving nested children."""
            node: dict = {
                "event_id": c.get("event_id", ""),
                "agent_id": c.get("agent", "claude-code"),
                "event_type": c.get("event_type", "tool_call"),
                "tool_name": c.get("tool_name"),
                "input_summary": c.get("summary", ""),
                "output_summary": None,
                "status": c.get("status", "completed"),
                "timestamp": c.get("timestamp", ""),
                "cost_tokens": None,
                "execution_duration_seconds": c.get("duration_seconds"),
                "subagent_type": c.get("subagent_type"),
                "model": c.get("model"),
                "feature_id": c.get("feature_id"),
            }
            nested = c.get("children")
            if nested:
                node["children"] = [
                    build_child_node(grandchild) for grandchild in nested
                ]
            node["total_count"] = count_descendants(node)
            return node

        hierarchical_events = []
        for turn in conversation_turns:
            user_query = turn.get("userQuery") or {}
            children = turn.get("children", [])
            built_children = [build_child_node(c) for c in children]
            total_count = len(built_children) + sum(
                count_descendants(ch) for ch in built_children
            )
            hierarchical_events.append(
                {
                    "parent": {
                        "event_id": user_query.get("event_id", ""),
                        "agent_id": user_query.get("agent_id", "claude-code"),
                        "event_type": "user_query",
                        "tool_name": "UserQuery",
                        "input_summary": user_query.get("prompt", "")
                        or user_query.get("input_summary", ""),
                        "output_summary": None,
                        "status": user_query.get("status", "completed"),
                        "timestamp": user_query.get("timestamp", ""),
                        "cost_tokens": None,
                        "execution_duration_seconds": turn.get("stats", {}).get(
                            "total_duration_seconds"
                        ),
                    },
                    "children": built_children,
                    "has_children": len(built_children) > 0,
                    "total_count": total_count,
                    "work_item_id": turn.get("work_item_id"),
                    "work_item_title": turn.get("work_item_title"),
                    "work_item_type": turn.get("work_item_type") or "feature",
                }
            )

        return templates.TemplateResponse(
            "partials/activity-feed-hierarchical.html",
            {
                "request": request,
                "hierarchical_events": hierarchical_events,
                "conversation_turns": conversation_turns,
                "total_turns": grouped_result.get("total_turns", 0),
                "limit": limit,
            },
        )
    finally:
        await db.close()


@router.get(
    "/views/activity-feed/children/{parent_event_id}", response_class=HTMLResponse
)
async def activity_feed_children(
    parent_event_id: str,
    request: Request,
) -> HTMLResponse:
    """Return child rows for a parent event (lazy loaded on expand)."""
    deps = get_deps()
    templates = get_templates()
    db = await deps.get_db()

    try:
        activity_service, _, _ = deps.create_services(db)
        grouped_result = await activity_service.get_grouped_events(limit=1000)

        conversation_turns = grouped_result.get("conversation_turns", [])

        def count_descendants(node: dict) -> int:
            """Recursively count all descendants of a node."""
            children = node.get("children") or []
            return len(children) + sum(count_descendants(ch) for ch in children)

        def build_child_node(c: dict) -> dict:
            """Recursively build a child node, preserving nested children."""
            node: dict = {
                "event_id": c.get("event_id", ""),
                "agent_id": c.get("agent", "claude-code"),
                "event_type": c.get("event_type", "tool_call"),
                "tool_name": c.get("tool_name"),
                "input_summary": c.get("summary", ""),
                "output_summary": None,
                "status": c.get("status", "completed"),
                "timestamp": c.get("timestamp", ""),
                "cost_tokens": None,
                "execution_duration_seconds": c.get("duration_seconds"),
                "subagent_type": c.get("subagent_type"),
                "model": c.get("model"),
                "feature_id": c.get("feature_id"),
            }
            nested = c.get("children")
            if nested:
                node["children"] = [
                    build_child_node(grandchild) for grandchild in nested
                ]
            node["total_count"] = count_descendants(node)
            return node

        # Find the matching turn by parent event_id
        target_group = None
        for turn in conversation_turns:
            user_query = turn.get("userQuery") or {}
            if user_query.get("event_id", "") == parent_event_id:
                children = turn.get("children", [])
                built_children = [build_child_node(c) for c in children]
                total_count = len(built_children) + sum(
                    count_descendants(ch) for ch in built_children
                )
                target_group = {
                    "parent": {
                        "event_id": user_query.get("event_id", ""),
                        "agent_id": user_query.get("agent_id", "claude-code"),
                        "event_type": "user_query",
                        "tool_name": "UserQuery",
                        "input_summary": user_query.get("prompt", "")
                        or user_query.get("input_summary", ""),
                        "output_summary": None,
                        "status": user_query.get("status", "completed"),
                        "timestamp": user_query.get("timestamp", ""),
                        "cost_tokens": None,
                        "execution_duration_seconds": turn.get("stats", {}).get(
                            "total_duration_seconds"
                        ),
                    },
                    "children": built_children,
                    "has_children": len(built_children) > 0,
                    "total_count": total_count,
                    "work_item_id": turn.get("work_item_id"),
                    "work_item_title": turn.get("work_item_title"),
                    "work_item_type": turn.get("work_item_type") or "feature",
                }
                break

        if target_group is None or not target_group["has_children"]:
            return HTMLResponse("")

        return templates.TemplateResponse(
            "partials/activity-feed-children.html",
            {"request": request, "group": target_group},
        )
    finally:
        await db.close()


@router.get("/activity-feed/stream")
async def activity_feed_stream(request: Request) -> StreamingResponse:
    """
    SSE endpoint for live activity feed updates.

    Polls SQLite every 3 seconds for MAX(rowid) change in agent_events.
    When a change is detected, sends a 'feed-update' SSE event so the
    dashboard can fetch only the delta rows instead of re-rendering the
    full tbody.
    """
    deps = get_deps()

    async def event_generator() -> Any:
        last_rowid: int = 0
        last_sync_signature: tuple[int, int, int, int, int] | None = None
        while True:
            if await request.is_disconnected():
                break
            try:
                db = await deps.get_db()
                try:
                    async with db.execute(
                        "SELECT MAX(rowid) FROM agent_events"
                    ) as cursor:
                        row = await cursor.fetchone()
                    current_rowid: int = (
                        int(row[0]) if (row and row[0] is not None) else 0
                    )

                    async with db.execute(
                        "SELECT COALESCE(MAX(seq), 0) FROM oplog"
                    ) as cursor:
                        row = await cursor.fetchone()
                    server_max_seq = int(row[0]) if (row and row[0] is not None) else 0

                    async with db.execute(
                        "SELECT COUNT(*) FROM sync_conflicts WHERE status != 'resolved'"
                    ) as cursor:
                        row = await cursor.fetchone()
                    pending_conflicts = int(row[0]) if row else 0

                    async with db.execute(
                        "SELECT COUNT(*), COALESCE(MAX(last_seen_seq - last_acked_seq), 0), COALESCE(MIN(last_acked_seq), 0) FROM sync_cursors"
                    ) as cursor:
                        row = await cursor.fetchone()
                    consumer_count = int(row[0]) if row else 0
                    max_consumer_lag = int(row[1]) if row else 0
                    min_acked = int(row[2]) if row else 0
                    pipeline_lag = (
                        max(server_max_seq - min_acked, 0) if consumer_count > 0 else 0
                    )
                finally:
                    await db.close()

                if current_rowid != last_rowid:
                    last_rowid = current_rowid
                    yield "event: feed-update\ndata: refresh\n\n"

                sync_signature = (
                    server_max_seq,
                    pending_conflicts,
                    max_consumer_lag,
                    consumer_count,
                    pipeline_lag,
                )
                if sync_signature != last_sync_signature:
                    last_sync_signature = sync_signature
                    sync_payload = {
                        "server_max_seq": server_max_seq,
                        "pending_conflicts": pending_conflicts,
                        "max_consumer_lag": max_consumer_lag,
                        "consumer_count": consumer_count,
                        "pipeline_lag": pipeline_lag,
                    }
                    yield f"event: sync-status\ndata: {json.dumps(sync_payload)}\n\n"
            except Exception as exc:
                logger.debug(f"SSE poll error: {exc}")

            await asyncio.sleep(3)

    return StreamingResponse(
        event_generator(),
        media_type="text/event-stream",
        headers={
            "Cache-Control": "no-cache",
            "X-Accel-Buffering": "no",
        },
    )


@router.get("/views/activity-feed/delta", response_class=HTMLResponse)
async def activity_feed_delta(
    request: Request,
    since: str | None = None,
) -> HTMLResponse:
    """
    Return only NEW top-level UserQuery rows that appeared after *since*.

    *since* is the event_id of the first (most-recent) row currently
    rendered in the client tbody.  The endpoint returns the same HTML
    fragment format as activity-feed-hierarchical.html but limited to
    rows whose event_id is newer than *since*.

    If *since* is omitted, returns the last 5 turns (initial bootstrap).
    """
    deps = get_deps()
    templates = get_templates()
    db = await deps.get_db()

    try:
        activity_service, _, _ = deps.create_services(db)
        grouped_result = await activity_service.get_grouped_events(limit=50)
        conversation_turns = grouped_result.get("conversation_turns", [])

        def count_descendants(node: dict) -> int:
            children = node.get("children") or []
            return len(children) + sum(count_descendants(ch) for ch in children)

        def build_child_node(c: dict) -> dict:
            node: dict = {
                "event_id": c.get("event_id", ""),
                "agent_id": c.get("agent", "claude-code"),
                "event_type": c.get("event_type", "tool_call"),
                "tool_name": c.get("tool_name"),
                "input_summary": c.get("summary", ""),
                "output_summary": None,
                "status": c.get("status", "completed"),
                "timestamp": c.get("timestamp", ""),
                "cost_tokens": None,
                "execution_duration_seconds": c.get("duration_seconds"),
                "subagent_type": c.get("subagent_type"),
                "model": c.get("model"),
                "feature_id": c.get("feature_id"),
            }
            nested = c.get("children")
            if nested:
                node["children"] = [
                    build_child_node(grandchild) for grandchild in nested
                ]
            node["total_count"] = count_descendants(node)
            return node

        # Build all turns in same format as the full endpoint
        all_turns: list[dict[str, Any]] = []
        for turn in conversation_turns:
            user_query = turn.get("userQuery") or {}
            children = turn.get("children", [])
            built_children = [build_child_node(c) for c in children]
            total_count = len(built_children) + sum(
                count_descendants(ch) for ch in built_children
            )
            all_turns.append(
                {
                    "parent": {
                        "event_id": user_query.get("event_id", ""),
                        "agent_id": user_query.get("agent_id", "claude-code"),
                        "event_type": "user_query",
                        "tool_name": "UserQuery",
                        "input_summary": user_query.get("prompt", "")
                        or user_query.get("input_summary", ""),
                        "output_summary": None,
                        "status": user_query.get("status", "completed"),
                        "timestamp": user_query.get("timestamp", ""),
                        "cost_tokens": None,
                        "execution_duration_seconds": turn.get("stats", {}).get(
                            "total_duration_seconds"
                        ),
                    },
                    "children": built_children,
                    "has_children": len(built_children) > 0,
                    "total_count": total_count,
                    "work_item_id": turn.get("work_item_id"),
                    "work_item_title": turn.get("work_item_title"),
                    "work_item_type": turn.get("work_item_type") or "feature",
                }
            )

        # Filter to only new turns (those not yet rendered in the client)
        if since:
            # Return only turns that come before (i.e., newer than) the
            # row with event_id == since in the ordered list.
            new_turns = []
            for turn in all_turns:
                if turn["parent"]["event_id"] == since:
                    break
                new_turns.append(turn)
            hierarchical_events = new_turns
        else:
            # No anchor — return last 5 turns for initial render
            hierarchical_events = all_turns[:5]

        if not hierarchical_events:
            return HTMLResponse("")

        return templates.TemplateResponse(
            "partials/activity-feed-hierarchical-rows.html",
            {
                "request": request,
                "hierarchical_events": hierarchical_events,
            },
        )
    finally:
        await db.close()


@router.get("/api/events", response_model=list[EventModel])
@cache(expire=CACHE_TTL["events"])
async def get_events(
    limit: int = 50,
    session_id: str | None = None,
    agent_id: str | None = None,
    offset: int = 0,
) -> list[EventModel]:
    """Get events as JSON API with parent-child hierarchical linking."""
    deps = get_deps()
    db = await deps.get_db()
    cache = deps.query_cache
    query_start_time = time.time()

    try:
        cache_key = (
            f"api_events:{limit}:{offset}:{session_id or 'all'}:{agent_id or 'all'}"
        )
        cached_results = cache.get(cache_key)

        if cached_results is not None:
            query_time_ms = (time.time() - query_start_time) * 1000
            cache.record_metric(cache_key, query_time_ms, cache_hit=True)
            logger.debug(
                f"Cache HIT for api_events (key={cache_key}, time={query_time_ms:.2f}ms)"
            )
            return list(cached_results) if isinstance(cached_results, list) else []
        else:
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

            exec_start = time.time()
            async with db.execute(query, params) as cursor:
                rows = await cursor.fetchall()
            exec_time_ms = (time.time() - exec_start) * 1000

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


@router.get("/api/initial-stats")
@cache(expire=CACHE_TTL["stats"])
async def initial_stats() -> dict[str, Any]:
    """Get initial statistics for dashboard header (events, agents, sessions)."""
    deps = get_deps()
    db = await deps.get_db()
    try:
        stats_query = """
            SELECT
                (SELECT COUNT(*) FROM agent_events) as total_events,
                (SELECT COUNT(DISTINCT agent_assigned) FROM sessions WHERE agent_assigned IS NOT NULL) as total_agents,
                (SELECT COUNT(*) FROM sessions) as total_sessions
        """
        async with db.execute(stats_query) as cursor:
            row = await cursor.fetchone()

        agents_query = "SELECT DISTINCT agent_assigned FROM sessions WHERE agent_assigned IS NOT NULL"
        async with db.execute(agents_query) as agents_cursor:
            agents_rows = await agents_cursor.fetchall()
        agents = [r[0] for r in agents_rows]

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


@router.get("/api/events-grouped-by-prompt")
@cache(expire=CACHE_TTL["events"])
async def events_grouped_by_prompt(limit: int = 50) -> dict[str, Any]:
    """Return activity events grouped by user prompt (conversation turns)."""
    deps = get_deps()
    db = await deps.get_db()

    try:
        activity_service, _, _ = deps.create_services(db)
        return cast(
            dict[str, Any], await activity_service.get_grouped_events(limit=limit)
        )
    finally:
        await db.close()


@router.get("/api/task-notifications")
@cache(expire=CACHE_TTL["events"])
async def get_task_notifications(limit: int = 50) -> dict[str, Any]:
    """
    Get task notifications with links to their originating Task events.

    Returns task completion notifications from background Task() calls,
    with correlation to the original Task events when possible.

    Response includes:
    - notifications: List of task notifications with parsed fields
    - linked_count: Number of notifications linked to Task events
    - unlinked_count: Number of notifications without links (older data)
    - link_method: How the link was established (claude_task_id or proximity)
    """
    deps = get_deps()
    db = await deps.get_db()

    try:
        activity_service, _, _ = deps.create_services(db)
        return cast(
            dict[str, Any],
            await activity_service.get_task_notifications_linked(limit=limit),
        )
    finally:
        await db.close()


@router.get("/views/metrics", response_class=HTMLResponse)
async def metrics_view(request: Request) -> HTMLResponse:
    """Get session metrics and performance data as HTMX partial."""
    deps = get_deps()
    templates = get_templates()
    db = await deps.get_db()
    cache = deps.query_cache
    query_start_time = time.time()

    try:
        cache_key = "metrics_view:all"
        cached_response = cache.get(cache_key)

        if cached_response is not None:
            query_time_ms = (time.time() - query_start_time) * 1000
            cache.record_metric(cache_key, query_time_ms, cache_hit=True)
            logger.debug(
                f"Cache HIT for metrics_view (key={cache_key}, time={query_time_ms:.2f}ms)"
            )
            sessions, stats = cached_response
        else:
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

                if row[4]:
                    ended_at = datetime.fromisoformat(row[4])
                    duration_seconds = (ended_at - started_at).total_seconds()
                else:
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

            cache_data = (sessions, stats)
            cache.set(cache_key, cache_data)
            query_time_ms = (time.time() - query_start_time) * 1000
            cache.record_metric(cache_key, exec_time_ms, cache_hit=False)
            logger.debug(
                f"Cache MISS for metrics_view (key={cache_key}, "
                f"db_time={exec_time_ms:.2f}ms, total_time={query_time_ms:.2f}ms)"
            )

        exec_time_dist = {
            "very_fast": 0,
            "fast": 0,
            "medium": 0,
            "slow": 0,
            "very_slow": 0,
        }
        active_sessions = sum(1 for s in sessions if s.get("status") == "active")
        token_stats = {
            "total_tokens": 0,
            "avg_per_event": 0,
            "peak_usage": 0,
            "estimated_cost": 0.0,
        }
        activity_timeline = {str(h): 0 for h in range(24)}
        max_hourly_count = 1
        agent_performance: list[dict[str, str | float]] = []
        error_rate = 0.0
        avg_response_time = 0.5

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


@router.get("/api/event-traces")
@cache(expire=CACHE_TTL["events"])
async def get_event_traces(
    limit: int = 50,
    session_id: str | None = None,
) -> dict[str, Any]:
    """Get event traces showing parent-child relationships for Task delegations."""
    deps = get_deps()
    db = await deps.get_db()
    cache = deps.query_cache
    query_start_time = time.time()

    try:
        cache_key = f"event_traces:{limit}:{session_id or 'all'}"
        cached_result = cache.get(cache_key)

        if cached_result is not None:
            query_time_ms = (time.time() - query_start_time) * 1000
            cache.record_metric(cache_key, query_time_ms, cache_hit=True)
            return cached_result  # type: ignore[no-any-return]

        exec_start = time.time()

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

        parent_query += " ORDER BY datetime(REPLACE(SUBSTR(timestamp, 1, 19), 'T', ' ')) DESC LIMIT ?"
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

            child_spikes = []
            try:
                if output_summary:
                    output_data = (
                        json.loads(output_summary)
                        if isinstance(output_summary, str)
                        else output_summary
                    )
                    if isinstance(output_data, dict):
                        spikes_info = output_data.get("spikes_created", [])
                        if isinstance(spikes_info, list):
                            child_spikes = spikes_info
            except Exception:
                pass

            child_query = """
                SELECT event_id, agent_id, event_type, timestamp, status
                FROM agent_events
                WHERE parent_event_id = ?
                ORDER BY datetime(REPLACE(SUBSTR(timestamp, 1, 19), 'T', ' ')) DESC
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

        result = {
            "timestamp": datetime.now().isoformat(),
            "total_traces": len(traces),
            "traces": traces,
            "limitations": {
                "note": "Child spike count is approximate and based on timestamp proximity",
                "note_2": "Spike IDs in child_spikes only available if recorded in output_summary",
            },
        }

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


@router.get("/api/complete-activity-feed")
@cache(expire=CACHE_TTL["activity_feed"])
async def complete_activity_feed(
    limit: int = 100,
    session_id: str | None = None,
    include_delegations: bool = True,
    include_spikes: bool = True,
) -> dict[str, Any]:
    """Get unified activity feed combining events from all sources."""
    deps = get_deps()
    db = await deps.get_db()
    cache = deps.query_cache
    query_start_time = time.time()

    try:
        cache_key = f"complete_activity:{limit}:{session_id or 'all'}:{include_delegations}:{include_spikes}"
        cached_result = cache.get(cache_key)

        if cached_result is not None:
            query_time_ms = (time.time() - query_start_time) * 1000
            cache.record_metric(cache_key, query_time_ms, cache_hit=True)
            return cached_result  # type: ignore[no-any-return]

        events: list[dict[str, Any]] = []

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

        query += " ORDER BY datetime(REPLACE(SUBSTR(timestamp, 1, 19), 'T', ' ')) DESC LIMIT ?"
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
                logger.debug(f"Spike query failed (expected if schema differs): {e}")

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

                collab_query += " ORDER BY datetime(REPLACE(SUBSTR(timestamp, 1, 19), 'T', ' ')) DESC LIMIT ?"
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

        events.sort(key=lambda e: e.get("timestamp", ""), reverse=True)
        events = events[:limit]

        exec_time_ms = (time.time() - exec_start) * 1000

        result = {
            "timestamp": datetime.now().isoformat(),
            "total_events": len(events),
            "sources": {
                "hook_events": sum(1 for e in events if e["source"] == "hook_event"),
                "spike_logs": sum(1 for e in events if e["source"] == "spike_log"),
                "delegations": sum(1 for e in events if e["source"] == "delegation"),
            },
            "events": events,
            "limitations": {
                "note": "Subagent tool activity not tracked (Claude Code limitation)",
                "github_issue": "https://github.com/anthropics/claude-code/issues/14859",
                "workaround": "SubagentStop hook captures completion, SDK logging captures results",
            },
        }

        cache.set(cache_key, result)
        query_time_ms = (time.time() - query_start_time) * 1000
        cache.record_metric(cache_key, exec_time_ms, cache_hit=False)

        return result

    finally:
        await db.close()
