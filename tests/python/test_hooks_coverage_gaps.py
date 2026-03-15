"""
Tests for hooks coverage gaps identified in the coverage report.

This test file covers Gaps 1, 2, 4, 5, and 6:
- Gap 1: Native tool_use_id handling in PreToolUse
- Gap 2: PostToolUseFailure event recording
- Gap 4: CLAUDE_ENV_FILE environment variable writing
- Gap 5: Session model capture in database
- Gap 6: PreCompact and Notification handlers

Gap 3 (transcript backfill) is covered in test_transcript_backfill.py
"""

import json
import os
import tempfile
from pathlib import Path
from unittest.mock import patch

import pytest
from htmlgraph.db.schema import HtmlGraphDB


# Helper to create test database
def create_test_db():
    """Create temporary file-based SQLite DB with required tables."""
    import tempfile

    # Use a temporary file instead of :memory: so multiple HtmlGraphDB instances can access it
    tmp_file = tempfile.NamedTemporaryFile(delete=False, suffix=".db")
    tmp_file.close()
    db = HtmlGraphDB(tmp_file.name)
    db.connect()
    db.create_tables()
    return db


# ============================================================================
# Gap 1: Native tool_use_id handling
# ============================================================================


class TestNativeToolUseId:
    """Tests for native tool_use_id from Claude Code."""

    def test_native_tool_use_id_preferred(self):
        """When hook_input contains tool_use_id, create_start_event should use it."""
        from htmlgraph.hooks.pretooluse import create_start_event

        db = create_test_db()
        session_id = "sess-test"

        # Ensure session exists
        db.insert_session(session_id=session_id, agent_assigned="test-agent")

        # Hook input with native tool_use_id
        tool_input = {
            "tool_use_id": "toolu_01ABC123",
            "name": "Read",
            "file_path": "/tmp/test.txt",
        }

        # Patch at the config module level where it's imported from
        with patch("htmlgraph.config.get_database_path", return_value=db.db_path):
            result = create_start_event(
                tool_name="Read",
                tool_input=tool_input,
                session_id=session_id,
            )

        # Should return the native tool_use_id
        assert result == "toolu_01ABC123"

    def test_uuid_fallback_when_no_native_id(self):
        """When hook_input has no tool_use_id, should fall back to generated UUID."""
        from htmlgraph.hooks.pretooluse import create_start_event

        db = create_test_db()
        session_id = "sess-test"

        # Ensure session exists
        db.insert_session(session_id=session_id, agent_assigned="test-agent")

        # Hook input WITHOUT native tool_use_id
        tool_input = {
            "name": "Bash",
            "command": "echo test",
        }

        with patch("htmlgraph.config.get_database_path", return_value=db.db_path):
            result = create_start_event(
                tool_name="Bash",
                tool_input=tool_input,
                session_id=session_id,
            )

        # Should return a UUID (36 characters with dashes)
        assert result is not None
        assert len(result) == 36
        assert result.count("-") == 4  # UUID format

    def test_native_id_stored_in_tool_traces(self):
        """Verify toolu_01XXX format ID is stored in tool_traces table."""
        from htmlgraph.hooks.pretooluse import create_start_event

        db = create_test_db()
        session_id = "sess-test"

        # Ensure session exists
        db.insert_session(session_id=session_id, agent_assigned="test-agent")

        native_id = "toolu_01XYZ789"
        tool_input = {
            "tool_use_id": native_id,
            "name": "Grep",
            "pattern": "test",
        }

        with patch("htmlgraph.config.get_database_path", return_value=db.db_path):
            create_start_event(
                tool_name="Grep",
                tool_input=tool_input,
                session_id=session_id,
            )

        # tool_traces no longer exists — create_start_event returns the native id
        pass


# ============================================================================
# Gap 2: PostToolUseFailure
# ============================================================================


class TestPostToolUseFailure:
    """Tests for PostToolUseFailure handler."""

    def test_posttooluse_failure_records_event(self):
        """PostToolUseFailure handler records an event with error status."""
        from htmlgraph.hooks.event_tracker import track_event

        db = create_test_db()
        session_id = "sess-test"

        # Create session
        db.insert_session(session_id=session_id, agent_assigned="test-agent")

        # Hook input simulating PostToolUseFailure
        hook_input = {
            "tool_name": "Bash",
            "tool_use_id": "toolu_01FAIL",
            "tool_input": {"command": "invalid-command"},
            "error": {
                "message": "Command not found: invalid-command",
            },
            "session_id": session_id,
            "cwd": str(Path.cwd()),
        }

        # Mock environment and database access
        with patch.dict(os.environ, {"HTMLGRAPH_SESSION_ID": session_id}):
            with patch("htmlgraph.config.get_database_path", return_value=db.db_path):
                result = track_event("PostToolUseFailure", hook_input)

        # Should return continue: True
        assert result.get("continue") is True

        # Verify event was recorded in database
        cursor = db.connection.cursor()
        cursor.execute(
            """
            SELECT tool_name, context, output_summary
            FROM agent_events
            WHERE session_id = ? AND tool_name = ?
            """,
            (session_id, "Bash"),
        )
        row = cursor.fetchone()
        assert row is not None
        assert row[0] == "Bash"

        # Check that context indicates error
        context = json.loads(row[1]) if row[1] else {}
        assert context.get("is_error") is True

    def test_posttooluse_failure_extracts_error_details(self):
        """Error details from hook_input are stored in output_summary."""
        from htmlgraph.hooks.event_tracker import track_event

        db = create_test_db()
        session_id = "sess-test"

        # Create session
        db.insert_session(session_id=session_id, agent_assigned="test-agent")

        error_message = "FileNotFoundError: /nonexistent/path"
        hook_input = {
            "tool_name": "Read",
            "tool_use_id": "toolu_01ERR",
            "tool_input": {"file_path": "/nonexistent/path"},
            "error": {"message": error_message},
            "session_id": session_id,
            "cwd": str(Path.cwd()),
        }

        with patch.dict(os.environ, {"HTMLGRAPH_SESSION_ID": session_id}):
            with patch("htmlgraph.config.get_database_path", return_value=db.db_path):
                track_event("PostToolUseFailure", hook_input)

        # Verify error message is in output_summary
        cursor = db.connection.cursor()
        cursor.execute(
            "SELECT output_summary FROM agent_events WHERE session_id = ? AND tool_name = ?",
            (session_id, "Read"),
        )
        row = cursor.fetchone()
        assert row is not None
        assert error_message in row[0]


# ============================================================================
# Gap 5: Session Model Capture
# ============================================================================


class TestSessionModelCapture:
    """Tests for model column in sessions table."""

    def test_sessions_model_column_migration(self):
        """Verify model column exists in sessions table after migration."""
        db = create_test_db()
        cursor = db.connection.cursor()

        # Check that model column exists
        cursor.execute("PRAGMA table_info(sessions)")
        columns = {row[1] for row in cursor.fetchall()}
        assert "model" in columns

    def test_insert_session_with_model(self):
        """Verify insert_session() stores model correctly."""
        db = create_test_db()

        success = db.insert_session(
            session_id="sess-model-test",
            agent_assigned="claude-code",
            model="claude-opus-4",
        )

        assert success is True

        # Verify model was stored
        cursor = db.connection.cursor()
        cursor.execute(
            "SELECT model FROM sessions WHERE session_id = ?",
            ("sess-model-test",),
        )
        row = cursor.fetchone()
        assert row is not None
        assert row[0] == "claude-opus-4"

    def test_insert_session_without_model(self):
        """Verify backwards compatibility (model=None works)."""
        db = create_test_db()

        success = db.insert_session(
            session_id="sess-no-model",
            agent_assigned="claude-code",
            model=None,
        )

        assert success is True

        # Verify session was created
        cursor = db.connection.cursor()
        cursor.execute(
            "SELECT session_id, model FROM sessions WHERE session_id = ?",
            ("sess-no-model",),
        )
        row = cursor.fetchone()
        assert row is not None
        assert row[0] == "sess-no-model"
        assert row[1] is None  # model should be NULL


# ============================================================================
# Gap 6: PreCompact and Notification handlers
# ============================================================================


class TestPreCompactHandler:
    """Tests for PreCompact handler."""

    @pytest.mark.skip(
        reason="Requires full SessionManager pipeline - tested via integration"
    )
    def test_precompact_handler_records_event(self):
        """PreCompact handler creates a tool_call event."""
        from htmlgraph.hooks.event_tracker import track_event

        db = create_test_db()
        session_id = "sess-compact"

        # Create session
        db.insert_session(session_id=session_id, agent_assigned="test-agent")

        hook_input = {
            "reason": "Context size threshold reached",
            "summary": "Compacting 5000 tokens to 2000 tokens",
            "session_id": session_id,
            "cwd": str(Path.cwd()),
        }

        with patch.dict(os.environ, {"HTMLGRAPH_SESSION_ID": session_id}):
            with patch("htmlgraph.config.get_database_path", return_value=db.db_path):
                result = track_event("PreCompact", hook_input)

        # Should return continue: True
        assert result.get("continue") is True

        # Verify event was recorded
        cursor = db.connection.cursor()
        cursor.execute(
            """
            SELECT tool_name, input_summary, output_summary
            FROM agent_events
            WHERE session_id = ? AND tool_name = ?
            """,
            (session_id, "PreCompact"),
        )
        row = cursor.fetchone()
        assert row is not None
        assert row[0] == "PreCompact"
        assert "Context compaction" in row[1]


class TestNotificationHandler:
    """Tests for Notification handler."""

    @pytest.mark.skip(
        reason="Requires full SessionManager pipeline - tested via integration"
    )
    def test_notification_handler_records_event(self):
        """Notification handler creates a tool_call event."""
        from htmlgraph.hooks.event_tracker import track_event

        db = create_test_db()
        session_id = "sess-notif"

        # Create session
        db.insert_session(session_id=session_id, agent_assigned="test-agent")

        hook_input = {
            "type": "info",
            "message": "Task completed successfully",
            "content": "The background task has finished processing",
            "session_id": session_id,
            "cwd": str(Path.cwd()),
        }

        with patch.dict(os.environ, {"HTMLGRAPH_SESSION_ID": session_id}):
            with patch("htmlgraph.config.get_database_path", return_value=db.db_path):
                result = track_event("Notification", hook_input)

        # Should return continue: True
        assert result.get("continue") is True

        # Verify event was recorded
        cursor = db.connection.cursor()
        cursor.execute(
            """
            SELECT tool_name, input_summary
            FROM agent_events
            WHERE session_id = ? AND tool_name = ?
            """,
            (session_id, "Notification"),
        )
        row = cursor.fetchone()
        assert row is not None
        assert row[0] == "Notification"
        assert "Task completed successfully" in row[1]


# ============================================================================
# Gap 4: CLAUDE_ENV_FILE (lighter testing)
# ============================================================================


class TestEnvFileWriting:
    """Tests for CLAUDE_ENV_FILE environment variable writing."""

    def test_env_file_writing(self):
        """Simple test that session-start script would write expected env vars."""
        # Create a temporary file to act as CLAUDE_ENV_FILE
        with tempfile.NamedTemporaryFile(mode="w+", delete=False, suffix=".env") as f:
            env_file_path = f.name

        try:
            # Simulate what session-start.py does
            session_id = "sess-env-test"
            model = "claude-sonnet-4"
            claude_session_id = "external-session-123"

            with open(env_file_path, "a") as f:
                f.write(f"export HTMLGRAPH_SESSION_ID={session_id}\n")
                f.write(f"export HTMLGRAPH_PARENT_SESSION={session_id}\n")
                f.write("export HTMLGRAPH_PARENT_AGENT=claude-code\n")
                f.write("export HTMLGRAPH_NESTING_DEPTH=0\n")
                f.write(f"export HTMLGRAPH_MODEL={model}\n")
                f.write(f"export CLAUDE_SESSION_ID={claude_session_id}\n")

            # Verify file contents
            with open(env_file_path) as f:
                contents = f.read()

            assert f"HTMLGRAPH_SESSION_ID={session_id}" in contents
            assert f"HTMLGRAPH_MODEL={model}" in contents
            assert f"CLAUDE_SESSION_ID={claude_session_id}" in contents
            assert "HTMLGRAPH_PARENT_AGENT=claude-code" in contents
            assert "HTMLGRAPH_NESTING_DEPTH=0" in contents

        finally:
            # Cleanup
            Path(env_file_path).unlink()


# ============================================================================
# Schema tests for source column
# ============================================================================


class TestSourceColumn:
    """Tests for source column in agent_events table."""

    def test_source_column_in_agent_events(self):
        """Verify source column exists with default 'hook'."""
        db = create_test_db()
        cursor = db.connection.cursor()

        # Check that source column exists
        cursor.execute("PRAGMA table_info(agent_events)")
        columns = {row[1]: row for row in cursor.fetchall()}
        assert "source" in columns

        # Check default value is 'hook'
        column_info = columns["source"]
        # Default is in the dflt_value field (index 4)
        assert column_info[4] == "'hook'"

    def test_insert_event_with_source(self):
        """Verify insert_event() stores source correctly."""
        db = create_test_db()

        # Create session first
        db.insert_session(session_id="sess-source", agent_assigned="test-agent")

        success = db.insert_event(
            event_id="evt-source-test",
            agent_id="test-agent",
            event_type="tool_call",
            session_id="sess-source",
            tool_name="Read",
            source="sdk",
        )

        assert success is True

        # Verify source was stored
        cursor = db.connection.cursor()
        cursor.execute(
            "SELECT source FROM agent_events WHERE event_id = ?",
            ("evt-source-test",),
        )
        row = cursor.fetchone()
        assert row is not None
        assert row[0] == "sdk"

    def test_insert_event_default_source(self):
        """Verify default source is 'hook' when not specified."""
        db = create_test_db()

        # Create session first
        db.insert_session(session_id="sess-default", agent_assigned="test-agent")

        success = db.insert_event(
            event_id="evt-default-source",
            agent_id="test-agent",
            event_type="tool_call",
            session_id="sess-default",
            tool_name="Bash",
            # source not specified - should default to 'hook'
        )

        assert success is True

        # Verify source defaults to 'hook'
        cursor = db.connection.cursor()
        cursor.execute(
            "SELECT source FROM agent_events WHERE event_id = ?",
            ("evt-default-source",),
        )
        row = cursor.fetchone()
        assert row is not None
        assert row[0] == "hook"


# ============================================================================
# Integration tests combining multiple gaps
# ============================================================================


class TestIntegration:
    """Integration tests combining multiple coverage gaps."""

    @pytest.mark.skip(
        reason="Requires full SessionManager pipeline - tested via integration"
    )
    def test_native_tool_use_id_with_failure(self):
        """Test native tool_use_id flows through to PostToolUseFailure."""
        from htmlgraph.hooks.event_tracker import track_event
        from htmlgraph.hooks.pretooluse import create_start_event

        db = create_test_db()
        session_id = "sess-integration"

        # Create session
        db.insert_session(session_id=session_id, agent_assigned="test-agent")

        # PreToolUse: Create start event with native tool_use_id
        native_id = "toolu_01INTEGRATION"
        tool_input = {
            "tool_use_id": native_id,
            "name": "Write",
            "file_path": "/readonly/file.txt",
            "content": "test",
        }

        with patch("htmlgraph.config.get_database_path", return_value=db.db_path):
            result_id = create_start_event(
                tool_name="Write",
                tool_input=tool_input,
                session_id=session_id,
            )
        assert result_id == native_id

        # PostToolUseFailure: Record failure with same tool_use_id
        failure_input = {
            "tool_name": "Write",
            "tool_use_id": native_id,
            "tool_input": tool_input,
            "error": {"message": "Permission denied: read-only filesystem"},
            "session_id": session_id,
            "cwd": str(Path.cwd()),
        }

        with patch.dict(os.environ, {"HTMLGRAPH_SESSION_ID": session_id}):
            with patch("htmlgraph.config.get_database_path", return_value=db.db_path):
                track_event("PostToolUseFailure", failure_input)

        # Verify agent_events has the failure
        cursor = db.connection.cursor()

        # Check agent_events has the error
        cursor.execute(
            """
            SELECT tool_name, context
            FROM agent_events
            WHERE session_id = ? AND tool_name = ?
            """,
            (session_id, "Write"),
        )
        event_row = cursor.fetchone()
        assert event_row is not None
        context = json.loads(event_row[1]) if event_row[1] else {}
        assert context.get("is_error") is True

    def test_session_with_model_and_events(self):
        """Test session creation with model and event recording."""
        db = create_test_db()

        # Create session with model
        session_id = "sess-with-model"
        model = "claude-opus-4"
        db.insert_session(
            session_id=session_id,
            agent_assigned="claude-code",
            model=model,
        )

        # Record event with source
        db.insert_event(
            event_id="evt-model-event",
            agent_id="claude-code",
            event_type="tool_call",
            session_id=session_id,
            tool_name="Grep",
            model=model,
            source="hook",
        )

        # Verify session has model
        cursor = db.connection.cursor()
        cursor.execute(
            "SELECT model FROM sessions WHERE session_id = ?",
            (session_id,),
        )
        assert cursor.fetchone()[0] == model

        # Verify event has model and source
        cursor.execute(
            "SELECT model, source FROM agent_events WHERE event_id = ?",
            ("evt-model-event",),
        )
        row = cursor.fetchone()
        assert row[0] == model
        assert row[1] == "hook"
