"""
Tests for Phase 2 Feature 3: Cross-Session Continuity

Tests handoff, resumption, and context recommendation features.
"""

import tempfile
from datetime import datetime, timedelta, timezone
from pathlib import Path

import pytest
from htmlgraph import SDK
from htmlgraph.db.schema import HtmlGraphDB
from htmlgraph.session_manager import SessionManager
from htmlgraph.sessions.handoff import (
    ContextRecommender,
    HandoffBuilder,
    HandoffTracker,
    SessionResume,
)


def init_test_database(graph_dir: Path) -> None:
    """Initialize test database."""
    db = HtmlGraphDB(str(graph_dir / "htmlgraph.db"))
    db.disconnect()


class TestHandoffBuilder:
    """Test HandoffBuilder fluent API."""

    def test_basic_handoff_creation(self, isolated_db):
        """Test creating basic handoff with builder."""
        with tempfile.TemporaryDirectory() as tmpdir:
            manager = SessionManager(tmpdir)
            session = manager.start_session("test-session", agent="alice")

            builder = HandoffBuilder(session)
            handoff = (
                builder.add_summary("Completed feature X")
                .add_next_focus("Start feature Y")
                .add_blocker("Waiting for API key")
                .add_context_file("src/main.py")
                .build()
            )

            assert handoff["handoff_notes"] == "Completed feature X"
            assert handoff["recommended_next"] == "Start feature Y"
            assert "Waiting for API key" in handoff["blockers"]
            assert "src/main.py" in handoff["recommended_context"]

    def test_multiple_blockers(self, isolated_db):
        """Test adding multiple blockers."""
        with tempfile.TemporaryDirectory() as tmpdir:
            manager = SessionManager(tmpdir)
            session = manager.start_session("test-session", agent="alice")

            builder = HandoffBuilder(session)
            handoff = (
                builder.add_blockers(["Blocker 1", "Blocker 2"])
                .add_blocker("Blocker 3")
                .build()
            )

            assert len(handoff["blockers"]) == 3
            assert "Blocker 1" in handoff["blockers"]
            assert "Blocker 3" in handoff["blockers"]

    def test_multiple_context_files(self, isolated_db):
        """Test adding multiple context files."""
        with tempfile.TemporaryDirectory() as tmpdir:
            manager = SessionManager(tmpdir)
            session = manager.start_session("test-session", agent="alice")

            builder = HandoffBuilder(session)
            handoff = (
                builder.add_context_files(["file1.py", "file2.py"])
                .add_context_file("file3.py")
                .build()
            )

            assert len(handoff["recommended_context"]) == 3
            assert "file1.py" in handoff["recommended_context"]
            assert "file3.py" in handoff["recommended_context"]


class TestContextRecommender:
    """Test ContextRecommender git integration."""

    def test_get_recent_files_no_git(self, isolated_db):
        """Test graceful handling when not in git repo."""
        recommender = ContextRecommender(repo_root=Path("/nonexistent"))
        files = recommender.get_recent_files()
        assert files == []

    def test_matches_pattern(self, isolated_db):
        """Test pattern matching."""
        recommender = ContextRecommender()
        assert recommender._matches_pattern("test.md", "*.md")
        assert recommender._matches_pattern("src/test.py", "src/*.py")
        assert not recommender._matches_pattern("test.py", "*.md")


class TestSessionResume:
    """Test SessionResume functionality."""

    def test_get_last_session_empty(self, isolated_db):
        """Test getting last session when none exist."""
        with tempfile.TemporaryDirectory() as tmpdir:
            graph_dir = Path(tmpdir) / ".htmlgraph"
            graph_dir.mkdir()
            init_test_database(graph_dir)

            sdk = SDK(directory=graph_dir, agent="alice", db_path=str(isolated_db))
            resume = SessionResume(sdk)

            last_session = resume.get_last_session()
            assert last_session is None

    def test_get_last_session_filters_by_agent(self, isolated_db):
        """Test filtering by agent."""
        with tempfile.TemporaryDirectory() as tmpdir:
            graph_dir = Path(tmpdir) / ".htmlgraph"
            graph_dir.mkdir()

            manager = SessionManager(graph_dir)

            # Create sessions for different agents
            session_alice = manager.start_session("sess-alice", agent="alice")
            _session_bob = manager.start_session("sess-bob", agent="bob")  # noqa: F841

            # End Alice's session
            manager.end_session(session_alice.id)

            # Get last session for Alice
            sdk = SDK(directory=graph_dir, agent="alice", db_path=str(isolated_db))
            resume = SessionResume(sdk)
            last = resume.get_last_session(agent="alice")

            assert last is not None
            assert last.id == session_alice.id
            assert last.agent == "alice"

    def test_build_resume_info(self, isolated_db):
        """Test building resume information."""
        with tempfile.TemporaryDirectory() as tmpdir:
            graph_dir = Path(tmpdir) / ".htmlgraph"
            graph_dir.mkdir()

            manager = SessionManager(graph_dir)
            session = manager.start_session("test-session", agent="alice")

            # Set handoff info
            session.handoff_notes = "Completed OAuth"
            session.recommended_next = "Add JWT refresh"
            session.blockers = ["Waiting for review"]
            session.recommended_context = ["src/auth.py"]
            session.worked_on = ["feature-001"]
            manager.session_converter.save(session)

            # End session
            manager.end_session(session.id)

            # Build resume info
            sdk = SDK(directory=graph_dir, agent="alice", db_path=str(isolated_db))
            resume = SessionResume(sdk)
            info = resume.build_resume_info(session)

            assert info.summary == "Completed OAuth"
            assert info.next_focus == "Add JWT refresh"
            assert "Waiting for review" in info.blockers
            assert "src/auth.py" in info.recommended_files
            assert "feature-001" in info.worked_on_features

    def test_format_resume_prompt(self, isolated_db):
        """Test formatting resume prompt."""
        with tempfile.TemporaryDirectory() as tmpdir:
            graph_dir = Path(tmpdir) / ".htmlgraph"
            graph_dir.mkdir()

            manager = SessionManager(graph_dir)
            session = manager.start_session("test-session", agent="alice")

            # Set handoff info
            session.handoff_notes = "Completed OAuth"
            session.recommended_next = "Add JWT refresh"
            session.blockers = ["Waiting for review"]
            session.recommended_context = ["src/auth.py"]
            session.ended_at = datetime.now(timezone.utc) - timedelta(hours=2)
            manager.session_converter.save(session)

            # Build and format
            sdk = SDK(directory=graph_dir, agent="alice", db_path=str(isolated_db))
            resume = SessionResume(sdk)
            info = resume.build_resume_info(session)
            prompt = resume.format_resume_prompt(info)

            assert "CONTINUE FROM LAST SESSION" in prompt
            assert "Completed OAuth" in prompt
            assert "Add JWT refresh" in prompt
            assert "Waiting for review" in prompt
            assert "src/auth.py" in prompt
            assert "2 hours ago" in prompt


class TestHandoffTracker:
    """Test handoff effectiveness tracking."""

    def test_create_handoff(self, isolated_db):
        """Test creating handoff tracking record."""
        with tempfile.TemporaryDirectory() as tmpdir:
            graph_dir = Path(tmpdir) / ".htmlgraph"
            graph_dir.mkdir()

            sdk = SDK(directory=graph_dir, agent="alice", db_path=str(isolated_db))
            manager = SessionManager(graph_dir)
            session = manager.start_session("test-session", agent="alice")

            tracker = HandoffTracker(sdk)
            handoff_id = tracker.create_handoff(
                from_session_id=session.id, items_in_context=5
            )

            assert handoff_id is not None
            assert handoff_id.startswith("hand-")

    def test_resume_handoff(self, isolated_db):
        """Test updating handoff with resumption data."""
        with tempfile.TemporaryDirectory() as tmpdir:
            graph_dir = Path(tmpdir) / ".htmlgraph"
            graph_dir.mkdir()

            sdk = SDK(directory=graph_dir, agent="alice", db_path=str(isolated_db))
            manager = SessionManager(graph_dir)
            session1 = manager.start_session("sess-1", agent="alice")
            session2 = manager.start_session("sess-2", agent="alice")

            tracker = HandoffTracker(sdk)
            handoff_id = tracker.create_handoff(
                from_session_id=session1.id, items_in_context=5
            )

            success = tracker.resume_handoff(
                handoff_id=handoff_id,
                to_session_id=session2.id,
                items_accessed=3,
                time_to_resume_seconds=120,
            )

            assert success is True

    def test_rate_handoff(self, isolated_db):
        """Test rating handoff effectiveness."""
        with tempfile.TemporaryDirectory() as tmpdir:
            graph_dir = Path(tmpdir) / ".htmlgraph"
            graph_dir.mkdir()

            sdk = SDK(directory=graph_dir, agent="alice", db_path=str(isolated_db))
            manager = SessionManager(graph_dir)
            session = manager.start_session("test-session", agent="alice")

            tracker = HandoffTracker(sdk)
            handoff_id = tracker.create_handoff(
                from_session_id=session.id, items_in_context=5
            )

            success = tracker.rate_handoff(handoff_id, rating=4)
            assert success is True

            # Test invalid rating
            with pytest.raises(ValueError):
                tracker.rate_handoff(handoff_id, rating=6)

    def test_get_handoff_metrics(self, isolated_db):
        """Test retrieving handoff metrics."""
        with tempfile.TemporaryDirectory() as tmpdir:
            graph_dir = Path(tmpdir) / ".htmlgraph"
            graph_dir.mkdir()

            sdk = SDK(directory=graph_dir, agent="alice", db_path=str(isolated_db))
            manager = SessionManager(graph_dir)
            session = manager.start_session("test-session", agent="alice")

            tracker = HandoffTracker(sdk)
            handoff_id = tracker.create_handoff(
                from_session_id=session.id, items_in_context=5
            )

            metrics = tracker.get_handoff_metrics(limit=10)
            assert len(metrics) == 1
            assert metrics[0].handoff_id == handoff_id
            assert metrics[0].items_in_context == 5


class TestSessionManagerHandoff:
    """Test SessionManager handoff methods."""

    def test_end_session_with_handoff(self, isolated_db):
        """Test ending session with handoff."""
        with tempfile.TemporaryDirectory() as tmpdir:
            graph_dir = Path(tmpdir) / ".htmlgraph"
            graph_dir.mkdir()

            manager = SessionManager(graph_dir)
            session = manager.start_session("test-session", agent="alice")

            updated = manager.end_session_with_handoff(
                session_id=session.id,
                summary="Completed OAuth integration",
                next_focus="Implement JWT refresh",
                blockers=["Waiting for security review"],
                keep_context=["src/auth/oauth.py"],
                auto_recommend_context=False,  # Disable git recommendations for test
            )

            assert updated is not None
            assert updated.handoff_notes == "Completed OAuth integration"
            assert updated.recommended_next == "Implement JWT refresh"
            assert "Waiting for security review" in updated.blockers
            assert updated.status == "completed"

    def test_continue_from_last(self, isolated_db):
        """Test continuing from last session."""
        with tempfile.TemporaryDirectory() as tmpdir:
            graph_dir = Path(tmpdir) / ".htmlgraph"
            graph_dir.mkdir()

            manager = SessionManager(graph_dir)

            # Create and end first session
            session1 = manager.start_session("sess-1", agent="alice")
            manager.end_session_with_handoff(
                session_id=session1.id,
                summary="Completed feature X",
                next_focus="Start feature Y",
                blockers=["Need API key"],
                auto_recommend_context=False,
            )

            # Continue from last
            new_session, resume_info = manager.continue_from_last(agent="alice")

            assert new_session is not None
            assert new_session.continued_from == session1.id
            assert resume_info is not None
            assert resume_info.summary == "Completed feature X"
            assert resume_info.next_focus == "Start feature Y"
            assert "Need API key" in resume_info.blockers

    def test_continue_from_last_no_previous(self, isolated_db):
        """Test continuing when no previous session exists."""
        with tempfile.TemporaryDirectory() as tmpdir:
            graph_dir = Path(tmpdir) / ".htmlgraph"
            graph_dir.mkdir()

            manager = SessionManager(graph_dir)
            new_session, resume_info = manager.continue_from_last(agent="alice")

            assert new_session is None
            assert resume_info is None


class TestSDKHandoffMethods:
    """Test SDK handoff and continuity methods."""

    def test_sdk_end_session_with_handoff(self, isolated_db):
        """Test SDK end_session_with_handoff method."""
        with tempfile.TemporaryDirectory() as tmpdir:
            graph_dir = Path(tmpdir) / ".htmlgraph"
            graph_dir.mkdir()

            sdk = SDK(directory=graph_dir, agent="alice", db_path=str(isolated_db))
            manager = SessionManager(graph_dir)
            session = manager.start_session("test-session", agent="alice")

            updated = sdk.end_session_with_handoff(
                session_id=session.id,
                summary="Completed feature",
                next_focus="Next task",
                blockers=["Blocker 1"],
                auto_recommend_context=False,
            )

            assert updated is not None
            assert updated.handoff_notes == "Completed feature"

    def test_sdk_continue_from_last(self, isolated_db):
        """Test SDK continue_from_last method."""
        with tempfile.TemporaryDirectory() as tmpdir:
            graph_dir = Path(tmpdir) / ".htmlgraph"
            graph_dir.mkdir()

            sdk = SDK(directory=graph_dir, agent="alice", db_path=str(isolated_db))
            manager = SessionManager(graph_dir)

            # Create and end first session
            session1 = manager.start_session("sess-1", agent="alice")
            sdk.end_session_with_handoff(
                session_id=session1.id,
                summary="Completed work",
                next_focus="Continue here",
                auto_recommend_context=False,
            )

            # Continue from last
            new_session, resume_info = sdk.continue_from_last()

            assert new_session is not None
            assert resume_info is not None
            assert resume_info.summary == "Completed work"


class TestSessionModel:
    """Test Session model handoff fields."""

    def test_session_has_continuity_fields(self, isolated_db):
        """Test Session model has all continuity fields."""
        with tempfile.TemporaryDirectory() as tmpdir:
            manager = SessionManager(tmpdir)
            session = manager.start_session("test-session", agent="alice")

            # Check all continuity fields exist
            assert hasattr(session, "handoff_notes")
            assert hasattr(session, "recommended_next")
            assert hasattr(session, "blockers")
            assert hasattr(session, "recommended_context")
            assert hasattr(session, "continued_from")

    def test_session_continuity_fields_default_values(self, isolated_db):
        """Test continuity fields have correct defaults."""
        with tempfile.TemporaryDirectory() as tmpdir:
            manager = SessionManager(tmpdir)
            session = manager.start_session("test-session", agent="alice")

            assert session.handoff_notes is None
            assert session.recommended_next is None
            assert session.blockers == []
            assert session.recommended_context == []
            assert session.continued_from is None

    def test_session_continuity_fields_can_be_set(self, isolated_db):
        """Test continuity fields can be set and saved."""
        with tempfile.TemporaryDirectory() as tmpdir:
            manager = SessionManager(tmpdir)
            session = manager.start_session("test-session", agent="alice")

            # Set fields
            session.handoff_notes = "Test handoff"
            session.recommended_next = "Next step"
            session.blockers = ["Blocker 1"]
            session.recommended_context = ["file1.py", "file2.py"]
            session.continued_from = "sess-previous"

            # Save and reload
            manager.session_converter.save(session)
            reloaded = manager.get_session(session.id)

            assert reloaded.handoff_notes == "Test handoff"
            assert reloaded.recommended_next == "Next step"
            assert reloaded.blockers == ["Blocker 1"]
            assert reloaded.recommended_context == ["file1.py", "file2.py"]
            assert reloaded.continued_from == "sess-previous"


class TestEndToEndWorkflow:
    """Test complete end-to-end handoff workflow."""

    def test_complete_handoff_workflow(self, isolated_db):
        """Test complete workflow from handoff to resume."""
        with tempfile.TemporaryDirectory() as tmpdir:
            graph_dir = Path(tmpdir) / ".htmlgraph"
            graph_dir.mkdir()

            _sdk = SDK(directory=graph_dir, agent="alice", db_path=str(isolated_db))  # noqa: F841
            manager = SessionManager(graph_dir)

            # Day 1: Alice works on feature
            session1 = manager.start_session("sess-friday", agent="alice")

            # Alice ends with handoff
            manager.end_session_with_handoff(
                session_id=session1.id,
                summary="Completed OAuth integration, JWT setup done",
                next_focus="Implement refresh token rotation",
                blockers=["Waiting for security review on token storage"],
                keep_context=["src/auth/oauth.py", "docs/security.md"],
                auto_recommend_context=False,
            )

            # Day 2: Alice resumes
            session2, resume = manager.continue_from_last(agent="alice")

            assert session2 is not None
            assert resume is not None

            # Check resume context
            assert resume.summary == "Completed OAuth integration, JWT setup done"
            assert resume.next_focus == "Implement refresh token rotation"
            assert "Waiting for security review on token storage" in resume.blockers
            assert "src/auth/oauth.py" in resume.recommended_files
            assert "docs/security.md" in resume.recommended_files

            # Check session linking
            assert session2.continued_from == session1.id


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
