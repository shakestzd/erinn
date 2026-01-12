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

    # Create a test session with all required NOT NULL fields
    # sessions table requires: session_id, agent_assigned (NOT NULL), status (NOT NULL)
    cursor = db.connection.cursor()
    cursor.execute(
        "INSERT INTO sessions (session_id, agent_assigned, created_at, status) VALUES (?, ?, ?, ?)",
        ("test-session-123", "claude", "2026-01-12T00:00:00", "active"),
    )
    db.connection.commit()

    yield db
    db.disconnect()


class TestPreToolUseEventHierarchy:
    """Test event hierarchy in PreToolUse hook's create_start_event function."""

    def test_tool_event_uses_env_parent_when_set(self, mock_db, tmp_path):
        """Test that tool events use HTMLGRAPH_PARENT_EVENT when set (subagent context)."""
        from htmlgraph.hooks.pretooluse import create_start_event

        # Simulate Task delegation context - subagent has parent event set
        task_delegation_event_id = f"evt-task-{uuid4().hex[:8]}"

        # Create the parent event in the database first (to satisfy foreign key constraint)
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
                "test-session-123",
                "started",
            ),
        )
        mock_db.connection.commit()

        # Set environment variable as would be set by Task() PreToolUse hook
        os.environ["HTMLGRAPH_PARENT_EVENT"] = task_delegation_event_id

        try:
            # Mock the database path to use our temp database
            with patch("htmlgraph.config.get_database_path") as mock_get_db:
                mock_get_db.return_value = Path(mock_db.db_path)

                # Execute tool in subagent context
                tool_use_id = create_start_event(
                    tool_name="Bash",
                    tool_input={"command": "echo test"},
                    session_id="test-session-123",
                )

                assert tool_use_id is not None

                # Verify the event was created with Task delegation as parent
                cursor = mock_db.connection.cursor()
                cursor.execute(
                    "SELECT parent_event_id FROM agent_events WHERE tool_name = 'Bash' LIMIT 1"
                )
                row = cursor.fetchone()

                assert row is not None
                assert row[0] == task_delegation_event_id, (
                    f"Expected parent_event_id={task_delegation_event_id}, got {row[0]}. "
                    "Tool events in subagent context should use HTMLGRAPH_PARENT_EVENT."
                )
        finally:
            # Cleanup
            if "HTMLGRAPH_PARENT_EVENT" in os.environ:
                del os.environ["HTMLGRAPH_PARENT_EVENT"]

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

                # Verify the event was created with UserQuery as parent
                cursor.execute(
                    "SELECT parent_event_id FROM agent_events WHERE tool_name = 'Read' LIMIT 1"
                )
                row = cursor.fetchone()

                assert row is not None
                assert row[0] == user_query_id, (
                    f"Expected parent_event_id={user_query_id}, got {row[0]}. "
                    "Top-level tool events should fall back to UserQuery."
                )
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

                # After Bash, HTMLGRAPH_PARENT_EVENT is now the Bash event ID (for spawner subprocess tracking)
                bash_event_id = os.environ.get("HTMLGRAPH_PARENT_EVENT")
                assert bash_event_id is not None
                assert bash_event_id != task_delegation_id, (
                    "Bash should update HTMLGRAPH_PARENT_EVENT"
                )

                # Step 4: Simulate Edit and Read - these will use the Bash event as parent
                # This reflects the actual cascading parent behavior
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

                # Verify hierarchy
                cursor.execute(
                    """
                    SELECT tool_name, parent_event_id
                    FROM agent_events
                    WHERE session_id = 'test-session-123'
                    ORDER BY rowid ASC
                    """
                )
                rows = cursor.fetchall()

                # Build tool_name -> parent_event_id mapping
                tool_events = {row[0]: row[1] for row in rows}

                # UserQuery has no parent (or self-reference in some implementations)
                assert (
                    tool_events.get("UserQuery") is None
                    or tool_events.get("UserQuery") == user_query_id
                )

                # Bash should have task_delegation as parent (set before Bash call)
                assert tool_events.get("Bash") == task_delegation_id, (
                    f"Bash should have parent={task_delegation_id}, got {tool_events.get('Bash')}"
                )

                # Edit and Read use Bash's event ID as parent (cascading behavior)
                # This is correct - after Bash runs, it sets itself as parent for subprocess tracking
                assert tool_events.get("Edit") == bash_event_id, (
                    f"Edit should have parent={bash_event_id} (Bash's event), got {tool_events.get('Edit')}"
                )
                assert tool_events.get("Read") == bash_event_id, (
                    f"Read should have parent={bash_event_id} (Bash's event), got {tool_events.get('Read')}"
                )

        finally:
            for env_var in [
                "HTMLGRAPH_PARENT_EVENT",
                "HTMLGRAPH_PARENT_QUERY_EVENT",
                "HTMLGRAPH_SUBAGENT_TYPE",
            ]:
                if env_var in os.environ:
                    del os.environ[env_var]

    def test_bash_exports_parent_for_spawner_subprocess(self, mock_db, tmp_path):
        """Test that Bash tool exports its event_id for spawner subprocess tracking."""
        from htmlgraph.hooks.pretooluse import create_start_event

        # Clear environment
        if "HTMLGRAPH_PARENT_EVENT" in os.environ:
            del os.environ["HTMLGRAPH_PARENT_EVENT"]

        try:
            with patch("htmlgraph.config.get_database_path") as mock_get_db:
                mock_get_db.return_value = Path(mock_db.db_path)

                # Execute Bash tool
                create_start_event(
                    tool_name="Bash",
                    tool_input={"command": "./spawner.py"},
                    session_id="test-session-123",
                )

                # Verify HTMLGRAPH_PARENT_EVENT was set for spawner subprocess
                bash_event_id = os.environ.get("HTMLGRAPH_PARENT_EVENT")
                assert bash_event_id is not None, (
                    "Bash tool should export HTMLGRAPH_PARENT_EVENT for spawner subprocess"
                )
                assert bash_event_id.startswith("evt-"), (
                    f"Expected event ID format evt-*, got {bash_event_id}"
                )
        finally:
            if "HTMLGRAPH_PARENT_EVENT" in os.environ:
                del os.environ["HTMLGRAPH_PARENT_EVENT"]


class TestEventHierarchyRegression:
    """Regression tests to ensure spawner subprocess events continue to work."""

    def test_spawner_subprocess_events_not_affected(self, mock_db, tmp_path):
        """Test that spawner subprocess events still get correct parent (regression test)."""
        # This tests that the fix doesn't break the working spawner subprocess tracking

        # Spawner subprocess events are created by spawner scripts that:
        # 1. Read HTMLGRAPH_PARENT_EVENT from environment
        # 2. Create subprocess events with that parent

        # The fix in create_start_event() now respects HTMLGRAPH_PARENT_EVENT,
        # which is the same pattern spawners use. This should work correctly.

        task_delegation_id = f"evt-task-{uuid4().hex[:8]}"

        # Create the parent event in the database first (to satisfy foreign key constraint)
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
            # Spawner would typically create its own event using similar logic
            # The key is that HTMLGRAPH_PARENT_EVENT is respected
            from htmlgraph.hooks.pretooluse import create_start_event

            with patch("htmlgraph.config.get_database_path") as mock_get_db:
                mock_get_db.return_value = Path(mock_db.db_path)

                # Spawner subprocess creates events (simulated as tool events)
                create_start_event(
                    tool_name="Bash",  # Spawner runs via Bash
                    tool_input={"command": "gemini-spawner --prompt 'test'"},
                    session_id="test-session-123",
                )

                # Verify parent is the Task delegation, not UserQuery
                cursor = mock_db.connection.cursor()
                cursor.execute(
                    "SELECT parent_event_id FROM agent_events WHERE tool_name = 'Bash' LIMIT 1"
                )
                row = cursor.fetchone()

                assert row is not None
                assert row[0] == task_delegation_id, (
                    f"Spawner subprocess should have parent={task_delegation_id}, got {row[0]}"
                )
        finally:
            if "HTMLGRAPH_PARENT_EVENT" in os.environ:
                del os.environ["HTMLGRAPH_PARENT_EVENT"]


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
                create_start_event(
                    tool_name="Bash",
                    tool_input={"command": "echo 'deep nested'"},
                    session_id="test-session-123",
                )

                # Verify hierarchy
                cursor.execute(
                    "SELECT tool_name, parent_event_id FROM agent_events WHERE tool_name = 'Bash' ORDER BY rowid DESC LIMIT 1"
                )
                bash_row = cursor.fetchone()

                assert bash_row is not None
                assert bash_row[1] == level3_parent, (
                    f"Bash should have parent={level3_parent} (nested Task), got {bash_row[1]}"
                )

        finally:
            for env_var in [
                "HTMLGRAPH_PARENT_EVENT",
                "HTMLGRAPH_PARENT_QUERY_EVENT",
                "HTMLGRAPH_SUBAGENT_TYPE",
            ]:
                if env_var in os.environ:
                    del os.environ[env_var]
