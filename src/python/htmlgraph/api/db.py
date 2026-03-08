"""
Database session management for HtmlGraph API.

Provides FastSQLA session factory and dependency injection for async database access.
Integrates with aiosqlite for direct SQL queries (non-ORM) with proper connection pooling.
"""

import logging
from collections.abc import AsyncGenerator
from pathlib import Path
from typing import Any

import aiosqlite
from sqlalchemy import Connection, create_engine
from sqlalchemy.ext.asyncio import AsyncEngine, AsyncSession, create_async_engine
from sqlalchemy.orm import sessionmaker

from htmlgraph.db.pragmas import apply_async_pragmas

logger = logging.getLogger(__name__)


class DatabaseManager:
    """Manages database connections and session factory.

    Uses aiosqlite for async SQLite access with proper connection pooling
    and lifecycle management. Supports both direct SQL queries and SQLAlchemy ORM.
    """

    def __init__(self, db_path: str):
        """Initialize database manager.

        Args:
            db_path: Path to SQLite database file.
        """
        self.db_path = db_path
        self.async_engine: AsyncEngine | None = None
        self.SessionLocal: sessionmaker[AsyncSession] | None = None  # type: ignore[type-var,type-arg]

    def initialize(self) -> None:
        """Initialize async engine and session factory."""
        # Ensure database directory exists
        db_file = Path(self.db_path)
        db_file.parent.mkdir(parents=True, exist_ok=True)

        # Create async SQLite engine
        # Using sqlite+aiosqlite for async support
        database_url = f"sqlite+aiosqlite:///{self.db_path}"
        self.async_engine = create_async_engine(
            database_url,
            echo=False,
            future=True,
            pool_pre_ping=True,
            connect_args={"timeout": 30},
        )

        # Create session factory
        self.SessionLocal = sessionmaker(  # type: ignore[call-overload]
            self.async_engine,
            class_=AsyncSession,
            expire_on_commit=False,
            autoflush=False,
        )

        logger.info(f"Database manager initialized with {self.db_path}")

    async def get_session(self) -> AsyncGenerator[AsyncSession, None]:
        """Get new database session.

        Yields:
            AsyncSession instance for database access.
        """
        if self.SessionLocal is None:
            raise RuntimeError(
                "Database manager not initialized. Call initialize() first."
            )

        async with self.SessionLocal() as session:
            yield session

    async def close(self) -> None:
        """Close database engine and connections."""
        if self.async_engine:
            await self.async_engine.dispose()
            logger.info("Database engine closed")


async def get_db(db_path: str) -> AsyncGenerator[aiosqlite.Connection, None]:
    """Dependency for getting database connection.

    Provides async SQLite connection with Row factory and busy timeout configured.
    This is the FastAPI dependency injection function for repository access.

    Args:
        db_path: Path to SQLite database file.

    Yields:
        Async database connection with proper configuration.
    """
    # Ensure database file exists
    db_file = Path(db_path)
    db_file.parent.mkdir(parents=True, exist_ok=True)

    async with aiosqlite.connect(db_path) as db:
        # Configure for named column access
        db.row_factory = aiosqlite.Row
        await apply_async_pragmas(db)
        yield db


class SyncDatabaseManager:
    """Synchronous database manager for CLI and initialization operations.

    Used for operations that don't need async support (schema creation, migrations).
    """

    def __init__(self, db_path: str):
        """Initialize sync database manager.

        Args:
            db_path: Path to SQLite database file.
        """
        self.db_path = db_path
        self.engine: Any = None

    def initialize(self) -> None:
        """Initialize sync engine."""
        # Ensure database directory exists
        db_file = Path(self.db_path)
        db_file.parent.mkdir(parents=True, exist_ok=True)

        # Create sync SQLite engine
        database_url = f"sqlite:///{self.db_path}"
        self.engine = create_engine(
            database_url,
            echo=False,
            future=True,
            connect_args={"timeout": 30},
        )

        logger.info(f"Sync database manager initialized with {self.db_path}")

    def get_connection(self) -> Connection:
        """Get sync database connection.

        Returns:
            Sync database connection.
        """
        if self.engine is None:
            raise RuntimeError(
                "Database manager not initialized. Call initialize() first."
            )
        return self.engine.connect()  # type: ignore[no-any-return]

    def close(self) -> None:
        """Close database engine."""
        if self.engine:
            self.engine.dispose()
            logger.info("Sync database engine closed")
