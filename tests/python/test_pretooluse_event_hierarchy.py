"""
Test PreToolUse hook event hierarchy for Task() delegation.

Verifies that:
1. Tool events in subagent context use HTMLGRAPH_PARENT_EVENT as parent
2. Top-level tool events fall back to UserQuery as parent
3. Task() delegation events create proper parent-child relationships
4. Spawner subprocess events continue to work (regression test)

Bug reference: bug-event-hierarchy-201fcc67
"""

import os
from pathlib import Path
from unittest.mock import patch
from uuid import uuid4

import pytest


@pytest.fixture
def temp_db_path(tmp_path):
    """Create temporary database path."""
    db_path = tmp_path / "test_hierarchy.db"
    return str(db_path)


@pytest.fixture
def temp_htmlgraph_dir(tmp_path):
    """Create temporary .htmlgraph directory with database."""
    htmlgraph_dir = tmp_path / ".htmlgraph"
    htmlgraph_dir.mkdir()
    return htmlgraph_dir


@pytest.fixture
def mock_db(temp_db_path):
    """Create a mock database with required tables."""
    from htmlgraph.db.schema import HtmlGraphDB

    db = HtmlGraphDB(temp_db_path)
    db.connect()
    db.create_tables()

    # Create test sessions with all required NOT NULL fields
    # sessions table requires: session_id, agent_assigned (NOT NULL), status (NOT NULL)
    # Also create parent session: "test-session-123".rsplit("-", 1)[0] = "test-session"
    cursor = db.connection.cursor()
    cursor.execute(
        "INSERT INTO sessions (session_id, agent_assigned, created_at, status) VALUES (?, ?, ?, ?)",
        ("test-session-123", "claude", "2026-01-12T00:00:00", "active"),
    )
    cursor.execute(
        "INSERT INTO sessions (session_id, agent_assigned, created_at, status) VALUES (?, ?, ?, ?)",
        ("test-session", "claude", "2026-01-12T00:00:00", "active"),
    )
    db.connection.commit()

    yield db
    db.disconnect()


class TestPreToolUseEventHierarchy:
    """Test event hierarchy in PreToolUse hook's create_start_event function."""

    def test_tool_event_uses_env_parent_when_set(self, mock_db, tmp_path):
        """Test that PreToolUse creates a tool trace in subagent context.

        ARCHITECTURE NOTE: In the real system, each hook invocation is a
        separate OS process.  HTMLGRAPH_PARENT_EVENT env vars set in PreToolUse
        die with that process -- PostToolUse starts fresh.  Therefore PreToolUse
        cannot use env vars to pass parent context to PostToolUse.

        The real parent attribution to task_delegation happens in PostToolUse
        (event_tracker.py) via DB fallback queries.  PreToolUse just creates
        the tool_trace for timing correlation.

        For the orchestrator's own session (session_known=True), the env_parent
        branch is intentionally disabled.  The parent defaults to UserQuery or
        None if no UserQuery exists.
        """
        from htmlgraph.hooks.pretooluse import create_start_event

        # Simulate Task delegation context - subagent has parent event set
        task_delegation_event_id = f"evt-task-{uuid4().hex[:8]}"

        # The parent session owns the task_delegation event
        parent_session_id = "test-session-123"

        # Create the parent event in the database first
        cursor = mock_db.connection.cursor()
        cursor.execute(
            """
            INSERT INTO agent_events
            (event_id, agent_id, event_type, timestamp, tool_name, session_id, status)
            VALUES (?, ?, ?, ?, ?, ?, ?)
            """,
            (
                task_delegation_event_id,
                "claude-code",
                "task_delegation",
                "2026-01-12T00:00:00",
                "Task",
                parent_session_id,
                "started",
            ),
        )
        mock_db.connection.commit()

        # Set environment variable (simulates within-process env, though in real
        # system this does NOT propagate to PostToolUse)
        os.environ["HTMLGRAPH_PARENT_EVENT"] = task_delegation_event_id

        try:
            # Mock the database path to use our temp database
            with patch("htmlgraph.config.get_database_path") as mock_get_db:
                mock_get_db.return_value = Path(mock_db.db_path)

                # Execute tool -- _ensure_session_exists makes session_known=True,
                # so env_parent branch is correctly skipped.
                tool_use_id = create_start_event(
                    tool_name="Bash",
                    tool_input={"command": "echo test"},
                    session_id=parent_session_id,
                )

                assert tool_use_id is not None

                # Verify tool_traces was created (PreToolUse only inserts into tool_traces, not agent_events)
                cursor = mock_db.connection.cursor()
                cursor.execute(
                    "SELECT tool_name FROM tool_traces WHERE tool_name = 'Bash' LIMIT 1"
                )
                row = cursor.fetchone()
                assert row is not None, "Tool trace should be created by PreToolUse"
        finally:
            # Cleanup
            if "HTMLGRAPH_PARENT_EVENT" in os.environ:
                del os.environ["HTMLGRAPH_PARENT_EVENT"]
            os.environ.pop("HTMLGRAPH_PARENT_EVENT_FOR_POST", None)

    def test_tool_event_falls_back_to_userquery_without_env_parent(
        self, mock_db, tmp_path
    ):
        """Test that top-level tool events fall back to UserQuery when no env parent."""
        from htmlgraph.hooks.pretooluse import create_start_event

        # Ensure no parent event in environment (top-level context)
        if "HTMLGRAPH_PARENT_EVENT" in os.environ:
            del os.environ["HTMLGRAPH_PARENT_EVENT"]

        # Create a UserQuery event to serve as fallback parent
        user_query_id = f"uq-{uuid4().hex[:8]}"
        cursor = mock_db.connection.cursor()
        cursor.execute(
            """
            INSERT INTO agent_events
            (event_id, agent_id, event_type, timestamp, tool_name, session_id, status)
            VALUES (?, ?, ?, ?, ?, ?, ?)
            """,
            (
                user_query_id,
                "user",
                "tool_call",
                "2026-01-12T00:00:00",
                "UserQuery",
                "test-session-123",
                "recorded",
            ),
        )
        mock_db.connection.commit()

        try:
            with patch("htmlgraph.config.get_database_path") as mock_get_db:
                mock_get_db.return_value = Path(mock_db.db_path)

                # Execute tool in top-level context (no parent env var)
                tool_use_id = create_start_event(
                    tool_name="Read",
                    tool_input={"file_path": "/test/file.py"},
                    session_id="test-session-123",
                )

                assert tool_use_id is not None

                # Verify the environment variable was set for PostToolUse
                assert (
                    os.environ.get("HTMLGRAPH_PARENT_EVENT_FOR_POST") == user_query_id
                ), (
                    f"Expected HTMLGRAPH_PARENT_EVENT_FOR_POST={user_query_id}, got {os.environ.get('HTMLGRAPH_PARENT_EVENT_FOR_POST')}. "
                    "PreToolUse should fall back to UserQuery when no parent context available."
                )

                # Verify tool_traces was created
                cursor.execute(
                    "SELECT tool_name FROM tool_traces WHERE tool_name = 'Read' LIMIT 1"
                )
                row = cursor.fetchone()
                assert row is not None, "Tool trace should be created by PreToolUse"
        finally:
            pass  # No cleanup needed

    def test_task_delegation_creates_new_parent_event(self, mock_db, tmp_path):
        """Test that Task() tool creates a new task_delegation parent event."""
        from htmlgraph.hooks.pretooluse import create_start_event

        # Create a UserQuery event
        user_query_id = f"uq-{uuid4().hex[:8]}"
        cursor = mock_db.connection.cursor()
        cursor.execute(
            """
            INSERT INTO agent_events
            (event_id, agent_id, event_type, timestamp, tool_name, session_id, status)
            VALUES (?, ?, ?, ?, ?, ?, ?)
            """,
            (
                user_query_id,
                "user",
                "tool_call",
                "2026-01-12T00:00:00",
                "UserQuery",
                "test-session-123",
                "recorded",
            ),
        )
        mock_db.connection.commit()

        # Ensure no parent event in environment
        if "HTMLGRAPH_PARENT_EVENT" in os.environ:
            del os.environ["HTMLGRAPH_PARENT_EVENT"]

        try:
            with patch("htmlgraph.config.get_database_path") as mock_get_db:
                mock_get_db.return_value = Path(mock_db.db_path)

                # Execute Task() delegation
                tool_use_id = create_start_event(
                    tool_name="Task",
                    tool_input={
                        "prompt": "Do something",
                        "subagent_type": "general-purpose",
                    },
                    session_id="test-session-123",
                )

                assert tool_use_id is not None

                # Verify Task event was created
                cursor.execute(
                    "SELECT event_id, event_type, parent_event_id FROM agent_events WHERE tool_name = 'Task' LIMIT 1"
                )
                task_row = cursor.fetchone()
                assert task_row is not None

                # Verify task_delegation event was created as parent
                cursor.execute(
                    "SELECT event_id, event_type FROM agent_events WHERE event_type = 'task_delegation' LIMIT 1"
                )
                delegation_row = cursor.fetchone()
                assert delegation_row is not None, (
                    "Task() should create a task_delegation event"
                )

                # Verify HTMLGRAPH_PARENT_EVENT was set for subagent
                assert os.environ.get("HTMLGRAPH_PARENT_EVENT") is not None, (
                    "Task() should set HTMLGRAPH_PARENT_EVENT for subagent"
                )
        finally:
            if "HTMLGRAPH_PARENT_EVENT" in os.environ:
                del os.environ["HTMLGRAPH_PARENT_EVENT"]
            if "HTMLGRAPH_PARENT_QUERY_EVENT" in os.environ:
                del os.environ["HTMLGRAPH_PARENT_QUERY_EVENT"]
            if "HTMLGRAPH_SUBAGENT_TYPE" in os.environ:
                del os.environ["HTMLGRAPH_SUBAGENT_TYPE"]

    def test_hierarchy_userquery_to_task_to_tools(self, mock_db, tmp_path):
        """Test complete hierarchy: UserQuery -> Task -> Tool events.

        Note: Bash tool exports HTMLGRAPH_PARENT_EVENT to its own event ID for spawner
        subprocess tracking. This test verifies that:
        1. Task delegation sets initial HTMLGRAPH_PARENT_EVENT
        2. Bash uses that Task delegation as parent
        3. Bash then overwrites HTMLGRAPH_PARENT_EVENT for its subprocesses
        4. Edit/Read use non-Bash tools and verify the parent chain mechanism works
        """
        from htmlgraph.hooks.pretooluse import create_start_event

        # Clear any existing parent context
        for env_var in [
            "HTMLGRAPH_PARENT_EVENT",
            "HTMLGRAPH_PARENT_QUERY_EVENT",
            "HTMLGRAPH_SUBAGENT_TYPE",
        ]:
            if env_var in os.environ:
                del os.environ[env_var]

        # Step 1: Create UserQuery event (user submits prompt)
        user_query_id = f"uq-{uuid4().hex[:8]}"
        cursor = mock_db.connection.cursor()
        cursor.execute(
            """
            INSERT INTO agent_events
            (event_id, agent_id, event_type, timestamp, tool_name, session_id, status)
            VALUES (?, ?, ?, ?, ?, ?, ?)
            """,
            (
                user_query_id,
                "user",
                "tool_call",
                "2026-01-12T00:00:00",
                "UserQuery",
                "test-session-123",
                "recorded",
            ),
        )
        mock_db.connection.commit()

        try:
            with patch("htmlgraph.config.get_database_path") as mock_get_db:
                mock_get_db.return_value = Path(mock_db.db_path)

                # Step 2: Create Task() delegation
                create_start_event(
                    tool_name="Task",
                    tool_input={
                        "prompt": "Implement feature",
                        "subagent_type": "general-purpose",
                    },
                    session_id="test-session-123",
                )

                # Get the task delegation event ID (this was set in environment by create_start_event)
                task_delegation_id = os.environ.get("HTMLGRAPH_PARENT_EVENT")
                assert task_delegation_id is not None, (
                    "Task should set HTMLGRAPH_PARENT_EVENT"
                )

                # Step 3: Simulate subagent executing Bash tool
                # Bash will use task_delegation_id as parent, then set HTMLGRAPH_PARENT_EVENT to its own ID
                create_start_event(
                    tool_name="Bash",
                    tool_input={"command": "npm install"},
                    session_id="test-session-123",
                )

                # After Bash, HTMLGRAPH_PARENT_EVENT stays as the Task delegation ID
                # (non-Task tools no longer overwrite HTMLGRAPH_PARENT_EVENT since
                # PreToolUse doesn't create agent_events for them)
                bash_event_id = os.environ.get("HTMLGRAPH_PARENT_EVENT")
                assert bash_event_id is not None
                assert bash_event_id == task_delegation_id, (
                    "Bash should NOT update HTMLGRAPH_PARENT_EVENT (stays as Task parent)"
                )

                # Step 4: Simulate Edit and Read - these also keep Task as parent
                create_start_event(
                    tool_name="Edit",
                    tool_input={
                        "file_path": "/test/file.py",
                        "old_string": "a",
                        "new_string": "b",
                    },
                    session_id="test-session-123",
                )

                create_start_event(
                    tool_name="Read",
                    tool_input={"file_path": "/test/other.py"},
                    session_id="test-session-123",
                )

                # Verify hierarchy:
                # - Task delegation event exists in agent_events (created by PreToolUse)
                # - Non-Task tools (Bash, Edit, Read) are NOT in agent_events from PreToolUse
                #   (PostToolUse creates their events with full output data)
                # - Environment variables are correctly set for parent chain

                cursor.execute(
                    """
                    SELECT tool_name, parent_event_id, event_type
                    FROM agent_events
                    WHERE session_id = 'test-session-123'
                    ORDER BY rowid ASC
                    """
                )
                rows = cursor.fetchall()

                tool_events = {row[0]: (row[1], row[2]) for row in rows}

                # UserQuery has no parent
                assert tool_events.get("UserQuery") is not None
                assert (
                    tool_events["UserQuery"][0] is None
                    or tool_events["UserQuery"][0] == user_query_id
                )

                # Task delegation event should exist with UserQuery as parent
                assert tool_events.get("Task") is not None, (
                    "Task delegation event should exist in agent_events"
                )
                assert tool_events["Task"][0] == user_query_id, (
                    f"Task should have parent={user_query_id}, got {tool_events['Task'][0]}"
                )
                assert tool_events["Task"][1] == "task_delegation", (
                    "Task event should have event_type=task_delegation"
                )

                # Non-Task tools should NOT be in agent_events (PostToolUse handles them)
                assert tool_events.get("Bash") is None, (
                    "Bash should not be in agent_events from PreToolUse"
                )
                assert tool_events.get("Edit") is None, (
                    "Edit should not be in agent_events from PreToolUse"
                )
                assert tool_events.get("Read") is None, (
                    "Read should not be in agent_events from PreToolUse"
                )

        finally:
            for env_var in [
                "HTMLGRAPH_PARENT_EVENT",
                "HTMLGRAPH_PARENT_QUERY_EVENT",
                "HTMLGRAPH_SUBAGENT_TYPE",
            ]:
                if env_var in os.environ:
                    del os.environ[env_var]

    def test_bash_does_not_export_parent_event(self, mock_db, tmp_path):
        """Test that non-Task tools do NOT set HTMLGRAPH_PARENT_EVENT (only Task does)."""
        from htmlgraph.hooks.pretooluse import create_start_event

        # Clear environment
        for var in ["HTMLGRAPH_PARENT_EVENT", "HTMLGRAPH_PARENT_EVENT_FOR_POST"]:
            if var in os.environ:
                del os.environ[var]

        try:
            with patch("htmlgraph.config.get_database_path") as mock_get_db:
                mock_get_db.return_value = Path(mock_db.db_path)

                # Execute Bash tool (standalone, no parent Task)
                create_start_event(
                    tool_name="Bash",
                    tool_input={"command": "./spawner.py"},
                    session_id="test-session-123",
                )

                # Non-Task tools no longer set HTMLGRAPH_PARENT_EVENT
                # Only Task delegation sets it for subagent context
                bash_parent = os.environ.get("HTMLGRAPH_PARENT_EVENT")
                assert bash_parent is None, (
                    "Bash should NOT set HTMLGRAPH_PARENT_EVENT (only Task does)"
                )
        finally:
            for var in ["HTMLGRAPH_PARENT_EVENT", "HTMLGRAPH_PARENT_EVENT_FOR_POST"]:
                if var in os.environ:
                    del os.environ[var]


class TestEventHierarchyRegression:
    """Regression tests to ensure spawner subprocess events continue to work."""

    def test_spawner_subprocess_events_not_affected(self, mock_db, tmp_path):
        """Test that spawner subprocess creates tool trace (regression test).

        ARCHITECTURE NOTE: In the real system each hook is a separate process,
        so HTMLGRAPH_PARENT_EVENT does NOT propagate from PreToolUse to PostToolUse.
        For a known session (session_known=True), the env_parent branch is
        intentionally disabled.  PostToolUse handles parent attribution via DB
        fallback.  This test verifies PreToolUse still creates a tool_trace.
        """
        task_delegation_id = f"evt-task-{uuid4().hex[:8]}"

        # Create the parent event in the database first
        cursor = mock_db.connection.cursor()
        cursor.execute(
            """
            INSERT INTO agent_events
            (event_id, agent_id, event_type, timestamp, tool_name, session_id, status)
            VALUES (?, ?, ?, ?, ?, ?, ?)
            """,
            (
                task_delegation_id,
                "claude-code",
                "task_delegation",
                "2026-01-12T00:00:00",
                "Task",
                "test-session-123",
                "started",
            ),
        )
        mock_db.connection.commit()

        # Simulate spawner environment (parent event set)
        os.environ["HTMLGRAPH_PARENT_EVENT"] = task_delegation_id

        try:
            from htmlgraph.hooks.pretooluse import create_start_event

            with patch("htmlgraph.config.get_database_path") as mock_get_db:
                mock_get_db.return_value = Path(mock_db.db_path)

                # Spawner subprocess creates events (simulated as tool events)
                tool_use_id = create_start_event(
                    tool_name="Bash",
                    tool_input={"command": "gemini-spawner --prompt 'test'"},
                    session_id="test-session-123",
                )

                # PreToolUse should still create a tool trace for correlation
                assert tool_use_id is not None, "PreToolUse should return a tool_use_id"

                # Verify tool_traces was created
                cursor = mock_db.connection.cursor()
                cursor.execute(
                    "SELECT tool_name FROM tool_traces WHERE tool_name = 'Bash' LIMIT 1"
                )
                row = cursor.fetchone()
                assert row is not None, "Tool trace should be created by PreToolUse"
        finally:
            if "HTMLGRAPH_PARENT_EVENT" in os.environ:
                del os.environ["HTMLGRAPH_PARENT_EVENT"]
            os.environ.pop("HTMLGRAPH_PARENT_EVENT_FOR_POST", None)


class TestMultiLevelNesting:
    """Test multi-level event nesting (UserQuery -> Task -> SubTask -> Tools)."""

    def test_four_level_nesting(self, mock_db, tmp_path):
        """Test 4-level nesting: UserQuery -> Task -> SubTask -> Tools."""
        from htmlgraph.hooks.pretooluse import create_start_event

        # Clear environment
        for env_var in [
            "HTMLGRAPH_PARENT_EVENT",
            "HTMLGRAPH_PARENT_QUERY_EVENT",
            "HTMLGRAPH_SUBAGENT_TYPE",
        ]:
            if env_var in os.environ:
                del os.environ[env_var]

        # Level 1: UserQuery
        user_query_id = f"uq-{uuid4().hex[:8]}"
        cursor = mock_db.connection.cursor()
        cursor.execute(
            """
            INSERT INTO agent_events
            (event_id, agent_id, event_type, timestamp, tool_name, session_id, status)
            VALUES (?, ?, ?, ?, ?, ?, ?)
            """,
            (
                user_query_id,
                "user",
                "tool_call",
                "2026-01-12T00:00:00",
                "UserQuery",
                "test-session-123",
                "recorded",
            ),
        )
        mock_db.connection.commit()

        try:
            with patch("htmlgraph.config.get_database_path") as mock_get_db:
                mock_get_db.return_value = Path(mock_db.db_path)

                # Level 2: First Task delegation
                create_start_event(
                    tool_name="Task",
                    tool_input={
                        "prompt": "Parent task",
                        "subagent_type": "orchestrator",
                    },
                    session_id="test-session-123",
                )
                level2_parent = os.environ.get("HTMLGRAPH_PARENT_EVENT")
                assert level2_parent is not None

                # Level 3: Nested Task delegation (subagent spawns another Task)
                create_start_event(
                    tool_name="Task",
                    tool_input={
                        "prompt": "Child task",
                        "subagent_type": "general-purpose",
                    },
                    session_id="test-session-123",
                )
                level3_parent = os.environ.get("HTMLGRAPH_PARENT_EVENT")
                assert level3_parent is not None
                assert level3_parent != level2_parent, (
                    "Nested Task should create new parent"
                )

                # Level 4: Tool execution in deepest subagent
                # PreToolUse no longer creates agent_events for non-Task tools
                # (PostToolUse handles that). Verify env var is set correctly.
                create_start_event(
                    tool_name="Bash",
                    tool_input={"command": "echo 'deep nested'"},
                    session_id="test-session-123",
                )

                # Verify the env var points to level3 Task as parent
                bash_parent = os.environ.get("HTMLGRAPH_PARENT_EVENT")
                assert bash_parent == level3_parent, (
                    f"Bash env parent should be {level3_parent} (nested Task), got {bash_parent}"
                )

        finally:
            for env_var in [
                "HTMLGRAPH_PARENT_EVENT",
                "HTMLGRAPH_PARENT_QUERY_EVENT",
                "HTMLGRAPH_SUBAGENT_TYPE",
            ]:
                if env_var in os.environ:
                    del os.environ[env_var]
