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
import logging
import sqlite3
from contextlib import asynccontextmanager
from pathlib import Path
from typing import Any

import aiosqlite
from fastapi import FastAPI
from fastapi.staticfiles import StaticFiles
from fastapi.templating import Jinja2Templates
from pydantic import BaseModel

logger = logging.getLogger(__name__)

# Import QueryCache from cache module (single source of truth)
from htmlgraph.api.cache import QueryCache  # noqa: E402


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


async def _pragma_optimize_loop(db_path: str) -> None:
    """Run PRAGMA optimize every 6 hours."""
    while True:
        await asyncio.sleep(6 * 3600)
        try:
            async with aiosqlite.connect(db_path) as conn:
                await conn.execute("PRAGMA optimize")
                logger.debug("PRAGMA optimize completed")
        except Exception:
            logger.exception("PRAGMA optimize failed")


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

    # ========== LIFESPAN ==========

    @asynccontextmanager
    async def lifespan(app: FastAPI) -> Any:
        """Manage application startup and shutdown."""
        from htmlgraph.api.cache import init_cache_backend
        from htmlgraph.db.pragmas import check_integrity

        # Startup: initialize cache backend
        await init_cache_backend()
        logger.info("FastAPICache initialized")

        # Startup: run integrity check
        with sqlite3.connect(db_path) as conn:
            if not check_integrity(conn):
                logger.critical(
                    "Database integrity check failed at startup — proceeding with caution"
                )
            else:
                logger.info("Database integrity check passed")

        # Startup: schedule periodic PRAGMA optimize
        optimize_task = asyncio.create_task(_pragma_optimize_loop(db_path))

        yield

        # Shutdown: cancel optimize task
        optimize_task.cancel()
        try:
            await optimize_task
        except asyncio.CancelledError:
            pass

    app = FastAPI(
        title="HtmlGraph Dashboard API",
        description="Real-time agent observability dashboard",
        version="0.1.0",
        lifespan=lifespan,
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

    # ========== INITIALIZE ROUTES ==========

    # Initialize route dependencies
    from htmlgraph.api.dependencies import Dependencies
    from htmlgraph.api.routes.analytics import init_analytics_routes
    from htmlgraph.api.routes.analytics import router as analytics_router
    from htmlgraph.api.routes.dashboard import init_dashboard_routes
    from htmlgraph.api.routes.dashboard import router as dashboard_router
    from htmlgraph.api.routes.orchestration import init_orchestration_routes
    from htmlgraph.api.routes.orchestration import router as orchestration_router
    from htmlgraph.api.routes.presence import init_presence_routes
    from htmlgraph.api.routes.presence import router as presence_router
    from htmlgraph.api.routes.testing import init_testing_routes
    from htmlgraph.api.routes.testing import router as testing_router

    deps = Dependencies(db_path=db_path, query_cache=app.state.query_cache)

    # Initialize each route module with required dependencies
    init_dashboard_routes(templates, deps)
    init_orchestration_routes(templates, deps)
    init_analytics_routes(deps)
    init_presence_routes(deps, db_path)
    init_testing_routes(deps)

    # Include all routers
    app.include_router(dashboard_router)
    app.include_router(orchestration_router)
    app.include_router(analytics_router)
    app.include_router(presence_router)
    app.include_router(testing_router)

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
