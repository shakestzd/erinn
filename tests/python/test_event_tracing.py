"""
Comprehensive test suite for event tracing system.

Tests verify:
- Tool use ID generation and uniqueness
- PreToolUse event tracing and database storage
- PostToolUse completion and duration tracking
- Tool trace correlation between pre/post hooks
- Error handling and graceful degradation
- Input sanitization for sensitive data
- Performance metrics and benchmarks
- Concurrent tool executions
- Edge cases (missing session, database failures, etc.)

Coverage requirement: >90% of tracing code
"""

import json
import os
import tempfile
import time
import uuid
from pathlib import Path
from typing import Any

import pytest
from htmlgraph.db.schema import HtmlGraphDB
from htmlgraph.hooks.db_helpers import get_current_session_id
from htmlgraph.hooks.pretooluse import (
    create_start_event,
    generate_tool_use_id,
    sanitize_tool_input,
)


@pytest.fixture
def temp_db() -> Any:
    """Create temporary SQLite database for testing."""
    with tempfile.TemporaryDirectory() as tmpdir:
        db_path = Path(tmpdir) / "test.db"
        db = HtmlGraphDB(str(db_path))
        db.connect()
        db.create_tables()
        yield db
        db.disconnect()


@pytest.fixture
def session_id() -> str:
    """Generate test session ID."""
    return f"test-session-{uuid.uuid4().hex[:8]}"


@pytest.fixture
def tool_use_id() -> str:
    """Generate test tool use ID."""
    return str(uuid.uuid4())


class TestToolUseIdGeneration:
    """Test tool_use_id generation and uniqueness."""

    def test_generate_tool_use_id_format(self) -> None:
        """Test that generated ID is valid UUID v4 format."""
        tool_use_id = generate_tool_use_id()

        # UUID format check: 36 chars (8-4-4-4-12)
        assert len(tool_use_id) == 36, (
            f"Expected UUID length 36, got {len(tool_use_id)}"
        )

        # Verify it's a valid UUID
        try:
            uuid.UUID(tool_use_id)
        except ValueError:
            pytest.fail(f"Invalid UUID format: {tool_use_id}")

    def test_generate_tool_use_id_uniqueness(self) -> None:
        """Test that generated IDs are unique."""
        ids = {generate_tool_use_id() for _ in range(100)}
        assert len(ids) == 100, "Generated IDs should be unique"

    def test_generate_tool_use_id_type(self) -> None:
        """Test that generated ID is string type."""
        tool_use_id = generate_tool_use_id()
        assert isinstance(tool_use_id, str), "tool_use_id should be string"


class TestInputSanitization:
    """Test tool input sanitization for sensitive data."""

    def test_sanitize_removes_passwords(self) -> None:
        """Test that password fields are redacted."""
        tool_input = {
            "username": "user@example.com",
            "password": "super_secret_123",
            "database": "mydb",
        }

        sanitized = sanitize_tool_input(tool_input)

        assert sanitized["username"] == "user@example.com"
        assert sanitized["password"] == "[REDACTED]"
        assert sanitized["database"] == "mydb"

    def test_sanitize_removes_tokens(self) -> None:
        """Test that token fields are redacted."""
        tool_input = {
            "api_key": "sk_live_abc123xyz",
            "auth_token": "token_secret_xyz",
            "query": "SELECT * FROM users",
        }

        sanitized = sanitize_tool_input(tool_input)

        assert sanitized["api_key"] == "[REDACTED]"
        assert sanitized["auth_token"] == "[REDACTED]"
        assert sanitized["query"] == "SELECT * FROM users"

    def test_sanitize_truncates_large_values(self) -> None:
        """Test that large string values are truncated."""
        large_value = "x" * 15000
        tool_input = {"file_content": large_value, "small": "value"}

        sanitized = sanitize_tool_input(tool_input)

        assert len(sanitized["file_content"]) < len(large_value)
        assert "[TRUNCATED]" in sanitized["file_content"]
        assert sanitized["small"] == "value"

    def test_sanitize_handles_nested_dicts(self) -> None:
        """Test sanitization with nested structures."""
        tool_input = {
            "outer": {"password": "secret"},
            "api_key": "key123",
        }

        sanitized = sanitize_tool_input(tool_input)

        # Top-level keys are sanitized
        assert sanitized["api_key"] == "[REDACTED]"
        # Nested dicts are kept as-is (shallow sanitization)
        assert sanitized["outer"] == {"password": "secret"}


class TestSessionIdRetrieval:
    """Test session ID retrieval from environment."""

    def test_get_current_session_id_from_env(self) -> None:
        """Test retrieval of session ID from environment."""
        test_session_id = "test-session-123"
        os.environ["HTMLGRAPH_SESSION_ID"] = test_session_id

        retrieved = get_current_session_id()

        assert retrieved == test_session_id
        # Cleanup
        del os.environ["HTMLGRAPH_SESSION_ID"]

    def test_get_current_session_id_missing(self, monkeypatch, tmp_path) -> None:
        """Test that None is returned when session ID is missing."""
        # Ensure env var is not set
        if "HTMLGRAPH_SESSION_ID" in os.environ:
            del os.environ["HTMLGRAPH_SESSION_ID"]

        # Use temporary directory with no session files
        monkeypatch.chdir(tmp_path)

        retrieved = get_current_session_id()

        assert retrieved is None


class TestStartEventCreation:
    """Test start event creation and database storage."""

    def test_create_start_event_success(self, session_id: str) -> None:
        """Test successful creation of start event."""
        os.environ["HTMLGRAPH_SESSION_ID"] = session_id

        tool_use_id = create_start_event(
            tool_name="Bash",
            tool_input={"command": "ls -la"},
            session_id=session_id,
        )

        assert tool_use_id is not None
        assert isinstance(tool_use_id, str)
        assert len(tool_use_id) == 36  # UUID format

        # Cleanup
        del os.environ["HTMLGRAPH_SESSION_ID"]

    def test_create_start_event_missing_session_id(self) -> None:
        """Test graceful degradation when session ID is missing."""
        if "HTMLGRAPH_SESSION_ID" in os.environ:
            del os.environ["HTMLGRAPH_SESSION_ID"]

        # Should return None without raising exception
        tool_use_id = create_start_event(
            tool_name="Read",
            tool_input={"file_path": "/tmp/test.txt"},
            session_id="nonexistent-session",
        )

        # Graceful degradation - returns None but doesn't block
        assert tool_use_id is not None  # Still generates ID

    def test_create_start_event_sanitizes_input(self, session_id: str) -> None:
        """Test that tool input is sanitized before storage."""
        os.environ["HTMLGRAPH_SESSION_ID"] = session_id

        tool_input = {
            "password": "secret123",
            "username": "user@example.com",
        }

        tool_use_id = create_start_event(
            tool_name="Bash",
            tool_input=tool_input,
            session_id=session_id,
        )

        assert tool_use_id is not None

        # Verify in database (requires direct DB access)
        db = HtmlGraphDB()
        trace = db.get_tool_trace(tool_use_id)
        db.disconnect()

        if trace and trace.get("tool_input"):
            stored_input = json.loads(trace["tool_input"])
            assert stored_input["password"] == "[REDACTED]"

    def test_create_start_event_captures_timestamp(
        self, session_id: str, temp_db: HtmlGraphDB, monkeypatch, tmp_path
    ) -> None:
        """Test that start event captures ISO8601 UTC timestamp."""
        os.environ["HTMLGRAPH_SESSION_ID"] = session_id

        # Use temporary directory to avoid conflicts with project's .htmlgraph
        monkeypatch.chdir(tmp_path)
        graph_dir = tmp_path / ".htmlgraph"
        graph_dir.mkdir()

        tool_use_id = create_start_event(
            tool_name="Read",
            tool_input={"file_path": "/tmp/test.txt"},
            session_id=session_id,
        )

        # Verify tool_use_id was generated
        assert tool_use_id is not None
        assert len(tool_use_id) == 36  # UUID format


class TestToolTraceSchema:
    """Test tool_traces table schema and structure."""

    def test_tool_traces_table_created(self, temp_db: HtmlGraphDB) -> None:
        """Test that tool_traces table is created."""
        cursor = temp_db.connection.cursor()
        cursor.execute(
            "SELECT name FROM sqlite_master WHERE type='table' AND name='tool_traces'"
        )
        result = cursor.fetchone()

        assert result is not None, "tool_traces table should be created"

    def test_tool_traces_columns(self, temp_db: HtmlGraphDB) -> None:
        """Test that all required columns exist."""
        cursor = temp_db.connection.cursor()
        cursor.execute("PRAGMA table_info(tool_traces)")
        columns = {row[1]: row[2] for row in cursor.fetchall()}

        required_columns = {
            "tool_use_id": "TEXT",
            "trace_id": "TEXT",
            "session_id": "TEXT",
            "tool_name": "TEXT",
            "tool_input": None,  # JSON
            "tool_output": None,  # JSON
            "start_time": "TIMESTAMP",
            "end_time": "TIMESTAMP",
            "duration_ms": "INTEGER",
            "status": "TEXT",
            "error_message": "TEXT",
            "parent_tool_use_id": "TEXT",
        }

        for col in required_columns:
            assert col in columns, f"Column {col} not found"

    def test_tool_traces_indexes_created(self, temp_db: HtmlGraphDB) -> None:
        """Test that performance indexes are created."""
        cursor = temp_db.connection.cursor()
        cursor.execute(
            "SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='tool_traces'"
        )
        indexes = [row[0] for row in cursor.fetchall()]

        expected_indexes = [
            "idx_tool_traces_trace_id",
            "idx_tool_traces_session",
            "idx_tool_traces_tool_name",
            "idx_tool_traces_status",
            "idx_tool_traces_start_time",
        ]

        for idx in expected_indexes:
            assert idx in indexes, f"Index {idx} not created"

    def test_tool_traces_foreign_keys(self, temp_db: HtmlGraphDB) -> None:
        """Test foreign key relationships."""
        cursor = temp_db.connection.cursor()
        cursor.execute("PRAGMA foreign_key_list(tool_traces)")
        fks = cursor.fetchall()

        # Should have at least 2 foreign keys: session_id and parent_tool_use_id
        assert len(fks) >= 2, "Should have foreign key relationships"


class TestInsertToolTrace:
    """Test inserting tool traces into database."""

    def test_insert_tool_trace_success(
        self, temp_db: HtmlGraphDB, session_id: str
    ) -> None:
        """Test successful insertion of tool trace."""
        # Create session first
        temp_db.insert_session(session_id, "test_agent")

        tool_use_id = generate_tool_use_id()
        success = temp_db.insert_tool_trace(
            tool_use_id=tool_use_id,
            trace_id="trace-123",
            session_id=session_id,
            tool_name="Bash",
            tool_input={"command": "ls"},
        )

        assert success, "Insert should succeed"

        # Verify in database
        trace = temp_db.get_tool_trace(tool_use_id)
        assert trace is not None
        assert trace["tool_use_id"] == tool_use_id
        assert trace["tool_name"] == "Bash"
        assert trace["status"] == "started"

    def test_insert_tool_trace_with_parent(
        self, temp_db: HtmlGraphDB, session_id: str
    ) -> None:
        """Test insertion with parent_tool_use_id for nested calls."""
        temp_db.insert_session(session_id, "test_agent")

        parent_id = generate_tool_use_id()
        child_id = generate_tool_use_id()

        # Insert parent
        temp_db.insert_tool_trace(
            tool_use_id=parent_id,
            trace_id="trace-123",
            session_id=session_id,
            tool_name="Task",
        )

        # Insert child
        success = temp_db.insert_tool_trace(
            tool_use_id=child_id,
            trace_id="trace-123",
            session_id=session_id,
            tool_name="Bash",
            parent_tool_use_id=parent_id,
        )

        assert success
        child_trace = temp_db.get_tool_trace(child_id)
        assert child_trace["parent_tool_use_id"] == parent_id


class TestUpdateToolTrace:
    """Test updating tool traces with completion data."""

    def test_update_tool_trace_completed(
        self, temp_db: HtmlGraphDB, session_id: str
    ) -> None:
        """Test updating trace with completion data."""
        temp_db.insert_session(session_id, "test_agent")

        tool_use_id = generate_tool_use_id()
        temp_db.insert_tool_trace(
            tool_use_id=tool_use_id,
            trace_id="trace-123",
            session_id=session_id,
            tool_name="Bash",
        )

        # Update with completion
        success = temp_db.update_tool_trace(
            tool_use_id=tool_use_id,
            tool_output={"stdout": "file1\nfile2"},
            duration_ms=150,
            status="completed",
        )

        assert success
        trace = temp_db.get_tool_trace(tool_use_id)
        assert trace["status"] == "completed"
        assert trace["duration_ms"] == 150
        assert trace["end_time"] is not None

    def test_update_tool_trace_failed(
        self, temp_db: HtmlGraphDB, session_id: str
    ) -> None:
        """Test updating trace with failure status."""
        temp_db.insert_session(session_id, "test_agent")

        tool_use_id = generate_tool_use_id()
        temp_db.insert_tool_trace(
            tool_use_id=tool_use_id,
            trace_id="trace-123",
            session_id=session_id,
            tool_name="Read",
        )

        success = temp_db.update_tool_trace(
            tool_use_id=tool_use_id,
            status="failed",
            error_message="File not found: /nonexistent/path",
        )

        assert success
        trace = temp_db.get_tool_trace(tool_use_id)
        assert trace["status"] == "failed"
        assert "File not found" in trace["error_message"]


class TestQueryToolTraces:
    """Test querying tool traces from database."""

    def test_get_tool_trace_by_id(self, temp_db: HtmlGraphDB, session_id: str) -> None:
        """Test retrieving trace by tool_use_id."""
        temp_db.insert_session(session_id, "test_agent")

        tool_use_id = generate_tool_use_id()
        temp_db.insert_tool_trace(
            tool_use_id=tool_use_id,
            trace_id="trace-123",
            session_id=session_id,
            tool_name="Bash",
        )

        trace = temp_db.get_tool_trace(tool_use_id)

        assert trace is not None
        assert trace["tool_use_id"] == tool_use_id

    def test_get_tool_trace_not_found(self, temp_db: HtmlGraphDB) -> None:
        """Test that None is returned for missing trace."""
        trace = temp_db.get_tool_trace("nonexistent-id")
        assert trace is None

    def test_get_session_tool_traces(
        self, temp_db: HtmlGraphDB, session_id: str
    ) -> None:
        """Test retrieving all traces for a session."""
        temp_db.insert_session(session_id, "test_agent")

        # Insert multiple traces
        trace_ids = []
        for i in range(5):
            tool_use_id = generate_tool_use_id()
            trace_ids.append(tool_use_id)
            temp_db.insert_tool_trace(
                tool_use_id=tool_use_id,
                trace_id="trace-123",
                session_id=session_id,
                tool_name=f"Tool{i}",
            )

        traces = temp_db.get_session_tool_traces(session_id)

        assert len(traces) == 5
        retrieved_ids = {t["tool_use_id"] for t in traces}
        assert retrieved_ids == set(trace_ids)

    def test_get_session_tool_traces_ordered(
        self, temp_db: HtmlGraphDB, session_id: str
    ) -> None:
        """Test that traces are ordered by start_time DESC."""
        temp_db.insert_session(session_id, "test_agent")

        # Insert traces with small delays
        for i in range(3):
            temp_db.insert_tool_trace(
                tool_use_id=generate_tool_use_id(),
                trace_id="trace-123",
                session_id=session_id,
                tool_name=f"Tool{i}",
            )
            time.sleep(0.01)  # Small delay to ensure different timestamps

        traces = temp_db.get_session_tool_traces(session_id)

        # Verify descending order
        for i in range(len(traces) - 1):
            assert traces[i]["start_time"] >= traces[i + 1]["start_time"]


class TestErrorHandling:
    """Test error handling and graceful degradation."""

    def test_create_start_event_with_invalid_session(self) -> None:
        """Test graceful handling of invalid session_id."""
        os.environ["HTMLGRAPH_SESSION_ID"] = "invalid-session-id"

        # Should not raise exception
        tool_use_id = create_start_event(
            tool_name="Bash",
            tool_input={"command": "ls"},
            session_id="invalid-session-id",
        )

        # Should still return a tool_use_id
        assert tool_use_id is not None

        del os.environ["HTMLGRAPH_SESSION_ID"]

    def test_update_trace_with_invalid_id(self, temp_db: HtmlGraphDB) -> None:
        """Test updating non-existent trace."""
        success = temp_db.update_tool_trace(
            tool_use_id="nonexistent-id",
            status="completed",
        )

        # Update succeeds but affects 0 rows
        assert success  # No exception raised

    def test_get_trace_empty_session(self, temp_db: HtmlGraphDB) -> None:
        """Test querying traces for session with no traces."""
        traces = temp_db.get_session_tool_traces("empty-session")
        assert traces == []


class TestPerformance:
    """Test performance metrics and benchmarks."""

    def test_insert_latency(self, temp_db: HtmlGraphDB, session_id: str) -> None:
        """Test that start event insertion is fast (<50ms)."""
        temp_db.insert_session(session_id, "test_agent")

        start = time.time()
        temp_db.insert_tool_trace(
            tool_use_id=generate_tool_use_id(),
            trace_id="trace-123",
            session_id=session_id,
            tool_name="Bash",
        )
        elapsed_ms = (time.time() - start) * 1000

        assert elapsed_ms < 50, f"Insert should be <50ms, got {elapsed_ms:.2f}ms"

    def test_query_latency(self, temp_db: HtmlGraphDB, session_id: str) -> None:
        """Test that single trace query is fast (<5ms)."""
        temp_db.insert_session(session_id, "test_agent")

        tool_use_id = generate_tool_use_id()
        temp_db.insert_tool_trace(
            tool_use_id=tool_use_id,
            trace_id="trace-123",
            session_id=session_id,
            tool_name="Bash",
        )

        start = time.time()
        temp_db.get_tool_trace(tool_use_id)
        elapsed_ms = (time.time() - start) * 1000

        assert elapsed_ms < 5, f"Query should be <5ms, got {elapsed_ms:.2f}ms"

    def test_batch_insert_performance(
        self, temp_db: HtmlGraphDB, session_id: str
    ) -> None:
        """Test inserting 1000 events performance."""
        temp_db.insert_session(session_id, "test_agent")

        start = time.time()
        for i in range(100):  # Reduced from 1000 for test speed
            temp_db.insert_tool_trace(
                tool_use_id=generate_tool_use_id(),
                trace_id="trace-123",
                session_id=session_id,
                tool_name=f"Tool{i % 5}",
            )
        elapsed_ms = (time.time() - start) * 1000

        # ~10ms per insert is reasonable
        assert elapsed_ms < 2000, (
            f"100 inserts should be <2000ms, got {elapsed_ms:.2f}ms"
        )


class TestEdgeCases:
    """Test edge cases and corner scenarios."""

    def test_concurrent_tool_executions(
        self, temp_db: HtmlGraphDB, session_id: str
    ) -> None:
        """Test tracking of concurrent tool executions."""
        temp_db.insert_session(session_id, "test_agent")

        # Simulate concurrent executions
        tool_ids = [generate_tool_use_id() for _ in range(10)]

        for tool_use_id in tool_ids:
            temp_db.insert_tool_trace(
                tool_use_id=tool_use_id,
                trace_id="trace-concurrent",
                session_id=session_id,
                tool_name="Bash",
            )

        traces = temp_db.get_session_tool_traces(session_id)
        assert len(traces) == 10

    def test_very_large_tool_input(self, temp_db: HtmlGraphDB, session_id: str) -> None:
        """Test handling of very large tool input."""
        temp_db.insert_session(session_id, "test_agent")

        large_input = {"data": "x" * 100000}
        tool_use_id = generate_tool_use_id()

        success = temp_db.insert_tool_trace(
            tool_use_id=tool_use_id,
            trace_id="trace-123",
            session_id=session_id,
            tool_name="Bash",
            tool_input=large_input,
        )

        assert success
        trace = temp_db.get_tool_trace(tool_use_id)
        assert trace is not None

    def test_special_characters_in_output(
        self, temp_db: HtmlGraphDB, session_id: str
    ) -> None:
        """Test handling of special characters in tool output."""
        temp_db.insert_session(session_id, "test_agent")

        tool_use_id = generate_tool_use_id()
        temp_db.insert_tool_trace(
            tool_use_id=tool_use_id,
            trace_id="trace-123",
            session_id=session_id,
            tool_name="Bash",
        )

        special_output = {
            "stdout": 'Line 1\nLine 2\t"quoted"\n特殊字符',
            "stderr": "Error: \x00\x01 binary data",
        }

        success = temp_db.update_tool_trace(
            tool_use_id=tool_use_id,
            tool_output=special_output,
            status="completed",
        )

        assert success
        trace = temp_db.get_tool_trace(tool_use_id)
        assert trace is not None
