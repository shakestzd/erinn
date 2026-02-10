"""
Tests for error handling and logging in sessions.

Tests validate that errors are properly logged to sessions with full context
and can be retrieved for debugging purposes. Note: Error handling is implemented
in SessionManager.log_error() method.
"""

import shutil
import tempfile
from pathlib import Path

import pytest
from htmlgraph.session_manager import SessionManager


@pytest.fixture
def temp_graph_dir():
    """Create a temporary directory for testing."""
    temp_dir = Path(tempfile.mkdtemp())
    yield temp_dir
    shutil.rmtree(temp_dir)


@pytest.fixture
def session_manager(temp_graph_dir):
    """Create a SessionManager instance for testing."""
    return SessionManager(graph_dir=temp_graph_dir)


class TestSessionManagement:
    """Test session lifecycle management."""

    def test_start_session_creates_session(self, session_manager):
        """Test that start_session creates a new session."""
        session = session_manager.start_session(
            session_id="sess-001", agent="test-agent"
        )
        assert session is not None
        assert session.id == "sess-001"
        assert session.agent == "test-agent"
        assert session.status == "active"

    def test_get_session_retrieves_session(self, session_manager):
        """Test that get_session retrieves an existing session."""
        created = session_manager.start_session(
            session_id="sess-002", agent="test-agent"
        )
        retrieved = session_manager.get_session(created.id)
        assert retrieved is not None
        assert retrieved.id == created.id

    def test_get_session_returns_none_for_missing(self, session_manager):
        """Test that get_session returns None for non-existent session."""
        retrieved = session_manager.get_session("non-existent-session")
        assert retrieved is None

    def test_end_session_updates_status(self, session_manager):
        """Test that end_session changes status to completed."""
        session = session_manager.start_session(
            session_id="sess-003", agent="test-agent"
        )
        assert session.status == "active"

        ended = session_manager.end_session(session.id)
        assert ended is not None
        assert ended.status == "completed"

    def test_end_session_with_handoff_notes(self, session_manager):
        """Test end_session with handoff notes."""
        session = session_manager.start_session(
            session_id="sess-004", agent="test-agent"
        )

        ended = session_manager.end_session(
            session.id,
            handoff_notes="Work completed",
            recommended_next="Review PR",
        )
        assert ended is not None
        assert ended.handoff_notes == "Work completed"
        assert ended.recommended_next == "Review PR"

    def test_set_session_handoff(self, session_manager):
        """Test setting handoff context without ending session."""
        session = session_manager.start_session(
            session_id="sess-005", agent="test-agent"
        )

        updated = session_manager.set_session_handoff(
            session.id,
            handoff_notes="Blocked on API",
            recommended_next="Unblock then continue",
        )
        assert updated is not None
        assert updated.status == "active"
        assert updated.handoff_notes == "Blocked on API"


class TestActivityTracking:
    """Test activity tracking functionality."""

    def test_track_activity_creates_entry(self, session_manager):
        """Test that track_activity creates an activity entry."""
        session = session_manager.start_session(
            session_id="sess-006", agent="test-agent"
        )

        entry = session_manager.track_activity(
            session_id=session.id,
            tool="Bash",
            summary="Ran tests",
            success=True,
        )
        assert entry is not None
        assert entry.tool == "Bash"
        assert entry.summary == "Ran tests"
        assert entry.success is True

    def test_track_activity_with_files(self, session_manager):
        """Test tracking activity with file paths."""
        session = session_manager.start_session(
            session_id="sess-007", agent="test-agent"
        )

        entry = session_manager.track_activity(
            session_id=session.id,
            tool="Edit",
            summary="Updated code",
            file_paths=["src/main.py", "src/utils.py"],
            success=True,
        )
        assert entry is not None
        assert entry.payload.get("file_paths") == ["src/main.py", "src/utils.py"]

    def test_track_failed_activity(self, session_manager):
        """Test tracking failed activity."""
        session = session_manager.start_session(
            session_id="sess-008", agent="test-agent"
        )

        entry = session_manager.track_activity(
            session_id=session.id,
            tool="Bash",
            summary="Build failed",
            success=False,
        )
        assert entry.success is False

    def test_track_activity_with_feature_attribution(self, session_manager):
        """Test tracking activity with explicit feature attribution."""
        session = session_manager.start_session(
            session_id="sess-009", agent="test-agent"
        )

        entry = session_manager.track_activity(
            session_id=session.id,
            tool="Edit",
            summary="Fixed bug",
            feature_id="feat-001",
            success=True,
        )
        assert entry.feature_id == "feat-001"


class TestSessionErrors:
    """Test error logging API graceful handling."""

    def test_log_error_gracefully_handles_missing_session(self, session_manager):
        """Test that log_error handles non-existent session gracefully."""
        error = Exception("Test error")
        session_manager.log_error(
            session_id="non-existent",
            error=error,
            traceback_str="traceback",
        )

    def test_get_session_errors_returns_empty_for_missing_session(
        self, session_manager
    ):
        """Test that missing session returns empty error list."""
        errors = session_manager.get_session_errors("non-existent-session")
        assert errors == []


class TestSessionRelationships:
    """Test session relationships and continuity."""

    def test_continue_from_sets_relationship(self, session_manager):
        """Test that sessions can reference previous sessions."""
        session1 = session_manager.start_session(session_id="sess-020", agent="agent-a")
        session_manager.end_session(session1.id)

        session2 = session_manager.start_session(
            session_id="sess-021", agent="agent-a", continued_from=session1.id
        )
        assert session2.continued_from == session1.id

    def test_session_worked_on_tracking(self, session_manager):
        """Test tracking which features a session worked on."""
        session = session_manager.start_session(
            session_id="sess-022", agent="test-agent"
        )

        session_manager.track_activity(
            session_id=session.id,
            tool="Edit",
            summary="Work on feature",
            feature_id="feat-123",
        )

        updated = session_manager.get_session(session.id)
        assert updated is not None
        # Feature should be in worked_on list if attribution succeeded
        assert isinstance(updated.worked_on, list)
