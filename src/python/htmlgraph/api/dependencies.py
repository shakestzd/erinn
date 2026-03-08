"""
Shared dependencies for HtmlGraph API routes.

This module provides dependency injection for:
- Database connections with proper timeout handling
- Service factories for ActivityService, OrchestrationService, AnalyticsService
- Query cache access
"""

import logging
from typing import Any

import aiosqlite

from htmlgraph.api.cache import QueryCache
from htmlgraph.api.services import (
    ActivityService,
    AnalyticsService,
    OrchestrationService,
)
from htmlgraph.db.pragmas import apply_async_pragmas

logger = logging.getLogger(__name__)


class Dependencies:
    """Container for shared dependencies that require app state."""

    def __init__(self, db_path: str, query_cache: QueryCache):
        self.db_path = db_path
        self.query_cache = query_cache

    async def get_db(self) -> aiosqlite.Connection:
        """Get database connection with standard PRAGMAs to prevent lock errors."""
        db = await aiosqlite.connect(self.db_path)
        db.row_factory = aiosqlite.Row
        await apply_async_pragmas(db)
        return db

    def create_services(
        self,
        db: aiosqlite.Connection,
    ) -> tuple[ActivityService, OrchestrationService, AnalyticsService]:
        """
        Create service instances with dependencies.

        Args:
            db: Database connection

        Returns:
            Tuple of (ActivityService, OrchestrationService, AnalyticsService)
        """
        activity_service = ActivityService(db=db, cache=self.query_cache, logger=logger)
        orch_service = OrchestrationService(
            db=db, cache=self.query_cache, logger=logger
        )
        analytics_service = AnalyticsService(
            db=db, cache=self.query_cache, logger=logger
        )
        return activity_service, orch_service, analytics_service


# Type alias for route handlers
ServiceTuple = tuple[ActivityService, OrchestrationService, AnalyticsService]


def get_dependencies_from_app(app: Any) -> Dependencies:
    """
    Get Dependencies instance from FastAPI app state.

    Args:
        app: FastAPI application instance

    Returns:
        Dependencies instance
    """
    return Dependencies(
        db_path=app.state.db_path,
        query_cache=app.state.query_cache,
    )
