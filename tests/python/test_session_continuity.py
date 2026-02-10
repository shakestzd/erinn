"""
Tests for Enhanced Session Continuity features.

Tests the new session-start hook enhancements:
- Strategic recommendations integration
- Multi-agent awareness
- Conflict detection
- Enhanced session summaries
"""

import subprocess

import pytest
from htmlgraph import SDK
from htmlgraph.converter import SessionConverter
from htmlgraph.session_manager import SessionManager


class TestStrategicRecommendations:
    """Test strategic recommendations in session-start hook."""

    def test_get_strategic_recommendations(
        self, tmp_path, isolated_graph_dir_full, isolated_db
    ):
        """Test that strategic recommendations are retrieved correctly."""
        # Setup: Create a test project with features
        graph_dir = tmp_path / ".htmlgraph"
        graph_dir.mkdir(exist_ok=True)

        sdk = SDK(directory=graph_dir, agent="test-agent", db_path=str(isolated_db))

        # Create track for features
        track = sdk.tracks.create("Test Track").save()

        # Create some features with dependencies
        sdk.features.create("Feature 1").set_track(track.id).set_priority("high").save()
        sdk.features.create("Feature 2").set_track(track.id).set_priority(
            "medium"
        ).save()
        sdk.features.create("Feature 3").set_track(track.id).set_priority("low").save()

        # Get recommendations
        recs = sdk.recommend_next_work(agent_count=1)

        # Verify recommendations exist
        assert recs is not None
        assert isinstance(recs, list)

        # Verify recommendation structure
        if len(recs) > 0:
            rec = recs[0]
            assert "id" in rec
            assert "title" in rec
            assert "score" in rec
            assert "reasons" in rec

    def test_get_bottlenecks(self, tmp_path, isolated_graph_dir_full, isolated_db):
        """Test bottleneck detection."""
        graph_dir = tmp_path / ".htmlgraph"
        graph_dir.mkdir(exist_ok=True)

        sdk = SDK(directory=graph_dir, agent="test-agent", db_path=str(isolated_db))

        # Create track for features
        track = sdk.tracks.create("Test Track").save()

        # Create features with blocking relationships
        f1 = sdk.features.create("Blocker Feature").set_track(track.id).save()
        sdk.features.create("Blocked Feature 1").set_track(track.id).blocked_by(
            f1.id
        ).save()
        sdk.features.create("Blocked Feature 2").set_track(track.id).blocked_by(
            f1.id
        ).save()

        # Get bottlenecks
        bottlenecks = sdk.find_bottlenecks(top_n=3)

        # Verify bottleneck structure
        assert isinstance(bottlenecks, list)
        if len(bottlenecks) > 0:
            bn = bottlenecks[0]
            assert "id" in bn
            assert "title" in bn
            assert "blocks_count" in bn
            assert "impact_score" in bn

    def test_get_parallel_work(self, tmp_path, isolated_graph_dir_full, isolated_db):
        """Test parallel work capacity calculation."""
        graph_dir = tmp_path / ".htmlgraph"
        graph_dir.mkdir(exist_ok=True)

        sdk = SDK(directory=graph_dir, agent="test-agent", db_path=str(isolated_db))

        # Create track for features
        track = sdk.tracks.create("Test Track").save()

        # Create independent features (no dependencies)
        sdk.features.create("Independent 1").set_track(track.id).save()
        sdk.features.create("Independent 2").set_track(track.id).save()
        sdk.features.create("Independent 3").set_track(track.id).save()

        # Get parallel capacity
        parallel = sdk.get_parallel_work(max_agents=5)

        # Verify parallel work structure
        assert isinstance(parallel, dict)
        assert "max_parallelism" in parallel
        assert "ready_now" in parallel
        assert "total_ready" in parallel


class TestMultiAgentAwareness:
    """Test multi-agent session awareness."""

    def test_get_active_agents(self, tmp_path, isolated_graph_dir_full, isolated_db):
        """Test detecting active agents from sessions."""
        graph_dir = tmp_path / ".htmlgraph"
        sessions_dir = graph_dir / "sessions"
        sessions_dir.mkdir(parents=True, exist_ok=True)

        manager = SessionManager(graph_dir)

        # Create sessions for different agents
        manager.start_session(agent="agent-1", title="Agent 1 Session")
        manager.start_session(agent="agent-2", title="Agent 2 Session")

        # Get all active sessions
        converter = SessionConverter(sessions_dir)
        all_sessions = converter.load_all()

        active_agents = []
        for session in all_sessions:
            if session.status == "active":
                active_agents.append(
                    {
                        "agent": session.agent,
                        "session_id": session.id,
                        "event_count": session.event_count,
                    }
                )

        # Verify we found multiple active agents
        assert len(active_agents) >= 2
        agent_names = [a["agent"] for a in active_agents]
        assert "agent-1" in agent_names
        assert "agent-2" in agent_names

    def test_detect_feature_conflicts(
        self, tmp_path, isolated_graph_dir_full, isolated_db
    ):
        """Test conflict detection when multiple agents work on same feature."""
        graph_dir = tmp_path / ".htmlgraph"
        graph_dir.mkdir(exist_ok=True)

        sdk = SDK(directory=graph_dir, agent="agent-1", db_path=str(isolated_db))

        # Create track for features
        track = sdk.tracks.create("Test Track").save()

        # Create a feature and start it with agent-1
        feature = sdk.features.create("Shared Feature").set_track(track.id).save()

        with sdk.features.edit(feature.id) as f:
            f.status = "in-progress"

        # Simulate another agent working on the same feature
        # (In real scenario, this would be detected from session worked_on)
        manager = SessionManager(graph_dir)
        session1 = manager.start_session(agent="agent-1", title="Session 1")
        session2 = manager.start_session(agent="agent-2", title="Session 2")

        # Track activity for both agents on the same feature
        manager.track_activity(
            session_id=session1.id,
            tool="Edit",
            summary="Working on shared feature",
            feature_id=feature.id,
        )
        manager.track_activity(
            session_id=session2.id,
            tool="Edit",
            summary="Also working on shared feature",
            feature_id=feature.id,
        )

        # Reload sessions to get worked_on
        from htmlgraph.converter import SessionConverter

        converter = SessionConverter(graph_dir / "sessions")
        all_sessions = converter.load_all()

        # Build feature -> agents map
        feature_agents = {}
        for session in all_sessions:
            if session.status == "active":
                for fid in session.worked_on:
                    if fid not in feature_agents:
                        feature_agents[fid] = []
                    feature_agents[fid].append(session.agent)

        # Check for conflicts
        conflicts = []
        for fid, agents in feature_agents.items():
            if len(set(agents)) > 1:  # More than one unique agent
                conflicts.append({"feature_id": fid, "agents": list(set(agents))})

        # Verify conflict detected
        assert len(conflicts) > 0
        assert feature.id in [c["feature_id"] for c in conflicts]


class TestEnhancedSessionSummary:
    """Test enhanced previous session summary formatting."""

    def test_session_summary_formatting(
        self, tmp_path, isolated_graph_dir_full, isolated_db
    ):
        """Test that session summary includes all expected fields."""
        graph_dir = tmp_path / ".htmlgraph"
        graph_dir.mkdir(exist_ok=True)

        manager = SessionManager(graph_dir)

        # Create and end a session with handoff notes
        session = manager.start_session(agent="test-agent", title="Test Session")

        # Add some activity
        manager.track_activity(
            session_id=session.id, tool="Edit", summary="Made some changes"
        )

        # Set handoff context
        manager.set_session_handoff(
            session_id=session.id,
            handoff_notes="Completed feature X, started feature Y",
            recommended_next="Continue with feature Y tests",
            blockers=["Waiting for API key"],
        )

        # End the session
        manager.end_session(session.id)

        # Reload and verify summary
        converter = SessionConverter(graph_dir / "sessions")
        sessions = converter.load_all()
        ended = [s for s in sessions if s.status == "completed"]

        assert len(ended) > 0
        prev_session = ended[0]

        # Verify all expected fields
        assert prev_session.id is not None
        assert prev_session.event_count > 0
        assert prev_session.handoff_notes == "Completed feature X, started feature Y"
        assert prev_session.recommended_next == "Continue with feature Y tests"
        assert prev_session.blockers == ["Waiting for API key"]


class TestGitIntegration:
    """Test git commit integration in session continuity."""

    def test_get_recent_commits(self, isolated_graph_dir_full, isolated_db):
        """Test getting recent git commits."""
        # This test requires a real git repo
        # We'll test with the current repo
        try:
            result = subprocess.run(
                ["git", "log", "--oneline", "-5"],
                capture_output=True,
                text=True,
                timeout=5,
            )

            if result.returncode == 0:
                commits = result.stdout.strip().split("\n")
                assert len(commits) > 0
                # Verify commit format (hash + message)
                for commit in commits:
                    parts = commit.split(" ", 1)
                    assert len(parts) == 2  # hash and message
                    assert len(parts[0]) >= 7  # Short hash is at least 7 chars
        except (subprocess.TimeoutExpired, FileNotFoundError):
            pytest.skip("Git not available or timeout")


class TestSessionStartHookIntegration:
    """Integration tests for session-start hook output."""

    def test_hook_output_structure(
        self, tmp_path, isolated_graph_dir_full, isolated_db
    ):
        """Test that session-start hook produces valid JSON output."""
        # Create a minimal .htmlgraph directory
        graph_dir = tmp_path / ".htmlgraph"
        graph_dir.mkdir(exist_ok=True)

        sdk = SDK(directory=graph_dir, agent="test-agent", db_path=str(isolated_db))
        track = sdk.tracks.create("Test Track").save()
        sdk.features.create("Test Feature").set_track(track.id).save()

        # Note: This test would need to actually invoke the hook script
        # For now, we verify the components work independently
        # Full integration test would require running the actual hook

        # Verify SDK methods work
        recs = sdk.recommend_next_work(agent_count=1)
        assert isinstance(recs, list)

        parallel = sdk.get_parallel_work(max_agents=3)
        assert isinstance(parallel, dict)
        assert "max_parallelism" in parallel

    def test_hook_handles_no_features(
        self, tmp_path, isolated_graph_dir_full, isolated_db
    ):
        """Test hook gracefully handles empty project."""
        graph_dir = tmp_path / ".htmlgraph"
        graph_dir.mkdir(exist_ok=True)

        # With no features, methods should return empty/default values
        sdk = SDK(directory=graph_dir, agent="test-agent", db_path=str(isolated_db))

        recs = sdk.recommend_next_work(agent_count=1)
        assert recs == []

        bottlenecks = sdk.find_bottlenecks(top_n=3)
        assert bottlenecks == []


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
