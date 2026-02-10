"""
Integration tests for full feature→session→spike lifecycle.

Tests the complete workflow:
1. Session start → session-init spike created
2. Feature start → session-init spike completes
3. Feature complete → transition spike created
4. Next feature start → transition spike completes
5. Session end → all spikes finalized
"""

import pytest
from htmlgraph import SDK
from htmlgraph.converter import NodeConverter, SessionConverter
from htmlgraph.session_manager import SessionManager


class TestFullLifecycle:
    """Test complete feature→session→spike lifecycle."""

    def test_single_feature_lifecycle(self, isolated_graph_dir_full, isolated_db):
        """Test complete lifecycle with a single feature."""
        graph_dir = isolated_graph_dir_full

        manager = SessionManager(graph_dir)

        # 1. Start session → creates session-init spike
        session = manager.start_session(
            agent="test-agent", title="Single Feature Session"
        )

        spike_converter = NodeConverter(graph_dir / "spikes")
        spikes = spike_converter.load_all()
        assert len([s for s in spikes if s.spike_subtype == "session-init"]) == 1
        init_spike = [s for s in spikes if s.spike_subtype == "session-init"][0]
        assert init_spike.status == "in-progress"

        # 2. Create and start feature → session-init spike completes
        feature = manager.create_feature("Test Feature", agent="test-agent")
        manager.start_feature(feature.id, agent="test-agent")

        spikes = spike_converter.load_all()
        init_spike = [s for s in spikes if s.spike_subtype == "session-init"][0]
        assert init_spike.status == "done"
        assert init_spike.to_feature_id == feature.id

        # 3. Complete feature → transition spike created
        manager.complete_feature(feature.id, agent="test-agent")

        spikes = spike_converter.load_all()
        transition_spikes = [s for s in spikes if s.spike_subtype == "transition"]
        assert len(transition_spikes) == 1
        assert transition_spikes[0].status == "in-progress"
        assert transition_spikes[0].from_feature_id == feature.id

        # 4. End session
        manager.end_session(session.id)

        # Verify session ended
        converter = SessionConverter(graph_dir / "sessions")
        final_session = converter.load(session.id)
        assert final_session.status == "completed"
        assert final_session.ended_at is not None

    def test_multi_feature_lifecycle(self, isolated_graph_dir_full, isolated_db):
        """Test lifecycle with multiple features in sequence."""
        graph_dir = isolated_graph_dir_full

        manager = SessionManager(graph_dir)

        # Start session
        session = manager.start_session(
            agent="test-agent", title="Multi Feature Session"
        )

        # Work on Feature 1
        feature1 = manager.create_feature("Feature 1", agent="test-agent")
        manager.start_feature(feature1.id, agent="test-agent")
        manager.track_activity(
            session_id=session.id,
            tool="Edit",
            summary="Working on feature 1",
            feature_id=feature1.id,
        )
        manager.complete_feature(feature1.id, agent="test-agent")

        # Work on Feature 2
        feature2 = manager.create_feature("Feature 2", agent="test-agent")
        manager.start_feature(feature2.id, agent="test-agent")
        manager.track_activity(
            session_id=session.id,
            tool="Edit",
            summary="Working on feature 2",
            feature_id=feature2.id,
        )
        manager.complete_feature(feature2.id, agent="test-agent")

        # Work on Feature 3
        feature3 = manager.create_feature("Feature 3", agent="test-agent")
        manager.start_feature(feature3.id, agent="test-agent")
        manager.track_activity(
            session_id=session.id,
            tool="Edit",
            summary="Working on feature 3",
            feature_id=feature3.id,
        )
        manager.complete_feature(feature3.id, agent="test-agent")

        # Verify spike creation pattern
        spike_converter = NodeConverter(graph_dir / "spikes")
        spikes = spike_converter.load_all()

        # Should have: 1 session-init (done) + 3 transition spikes (2 done, 1 in-progress)
        init_spikes = [s for s in spikes if s.spike_subtype == "session-init"]
        transition_spikes = [s for s in spikes if s.spike_subtype == "transition"]

        assert len(init_spikes) == 1
        assert init_spikes[0].status == "done"

        # After completing 3 features, we should have 3 transition spikes
        # (one after each feature completion)
        assert len(transition_spikes) == 3

        # The last transition spike should be in-progress (nothing after it yet)
        last_transition = sorted(
            transition_spikes, key=lambda s: s.created, reverse=True
        )[0]
        assert last_transition.status == "in-progress"
        assert last_transition.from_feature_id == feature3.id

        # End session
        manager.end_session(session.id)

        # Verify all work is tracked in session
        converter = SessionConverter(graph_dir / "sessions")
        final_session = converter.load(session.id)
        assert feature1.id in final_session.worked_on
        assert feature2.id in final_session.worked_on
        assert feature3.id in final_session.worked_on

    def test_session_with_parallel_features(self, isolated_graph_dir_full, isolated_db):
        """Test lifecycle with multiple features in parallel (WIP limit)."""
        graph_dir = isolated_graph_dir_full

        manager = SessionManager(graph_dir, wip_limit=3)

        manager.start_session(agent="test-agent", title="Parallel Work Session")

        # Start multiple features in parallel
        feature1 = manager.create_feature("Parallel Feature 1", agent="test-agent")
        feature2 = manager.create_feature("Parallel Feature 2", agent="test-agent")
        feature3 = manager.create_feature("Parallel Feature 3", agent="test-agent")

        manager.start_feature(feature1.id, agent="test-agent")
        manager.start_feature(feature2.id, agent="test-agent")
        manager.start_feature(feature3.id, agent="test-agent")

        # All should be in-progress
        active_features = manager.get_active_features()
        assert len(active_features) == 3

        # Complete them one by one
        manager.complete_feature(feature1.id, agent="test-agent")
        manager.complete_feature(feature2.id, agent="test-agent")
        manager.complete_feature(feature3.id, agent="test-agent")

        # Should have transition spikes for each completion
        spike_converter = NodeConverter(graph_dir / "spikes")
        spikes = spike_converter.load_all()
        transition_spikes = [s for s in spikes if s.spike_subtype == "transition"]
        assert len(transition_spikes) == 3

    def test_activity_attribution_with_spikes(
        self, isolated_graph_dir_full, isolated_db
    ):
        """Test that activities are correctly attributed to features after spike completion."""
        graph_dir = isolated_graph_dir_full

        manager = SessionManager(graph_dir)

        # Start session (creates session-init spike)
        session = manager.start_session(agent="test-agent", title="Attribution Test")

        # Verify session-init spike was created
        spike_converter = NodeConverter(graph_dir / "spikes")
        spikes = spike_converter.load_all()
        init_spike = [s for s in spikes if s.spike_subtype == "session-init"][0]
        assert init_spike.status == "in-progress"

        # Start a feature (completes session-init spike)
        feature = manager.create_feature("Test Feature", agent="test-agent")
        manager.start_feature(feature.id, agent="test-agent")

        # Verify spike was completed
        spikes = spike_converter.load_all()
        init_spike = [s for s in spikes if s.spike_subtype == "session-init"][0]
        assert init_spike.status == "done"
        assert init_spike.to_feature_id == feature.id

        # Track activity after feature start (should go to feature)
        manager.track_activity(
            session_id=session.id,
            tool="Edit",
            summary="Implementing feature",
        )

        # Verify activity is attributed to the feature
        converter = SessionConverter(graph_dir / "sessions")
        reloaded = converter.load(session.id)
        edit_activity = [a for a in reloaded.activity_log if a.tool == "Edit"][0]
        assert edit_activity.feature_id == feature.id

    def test_session_continuity_with_spikes(self, isolated_graph_dir_full, isolated_db):
        """Test that spikes work correctly across session continuity."""
        graph_dir = isolated_graph_dir_full

        manager = SessionManager(graph_dir)

        # First session
        session1 = manager.start_session(agent="test-agent", title="Session 1")
        feature1 = manager.create_feature("Feature in Session 1", agent="test-agent")
        manager.start_feature(feature1.id, agent="test-agent")
        manager.complete_feature(feature1.id, agent="test-agent")
        manager.end_session(session1.id)

        # Second session (continued from first)
        session2 = manager.start_session(
            agent="test-agent", title="Session 2", continued_from=session1.id
        )

        # Should have its own session-init spike
        spike_converter = NodeConverter(graph_dir / "spikes")
        spikes = spike_converter.load_all()
        init_spikes = [s for s in spikes if s.spike_subtype == "session-init"]

        # Each session should have its own session-init spike
        session1_spikes = [s for s in init_spikes if s.session_id == session1.id]
        session2_spikes = [s for s in init_spikes if s.session_id == session2.id]

        assert len(session1_spikes) == 1
        assert len(session2_spikes) == 1

    def test_transcript_integration_lifecycle(
        self, isolated_graph_dir_full, isolated_db
    ):
        """Test that transcript fields are preserved through lifecycle."""
        graph_dir = isolated_graph_dir_full

        manager = SessionManager(graph_dir)

        # Start session with transcript metadata
        session = manager.start_session(agent="test-agent", title="Transcript Test")

        # Link transcript
        manager.link_transcript(
            session_id=session.id,
            transcript_id="test-uuid-1234",
            transcript_path="/path/to/transcript.jsonl",
            git_branch="main",
        )

        # Do some work
        sdk = SDK(directory=graph_dir, agent="test-agent", db_path=str(isolated_db))
        track = sdk.tracks.create("Test Track").save()
        feature = sdk.features.create("Test Feature").set_track(track.id).save()
        manager.start_feature(feature.id, agent="test-agent")
        manager.complete_feature(feature.id, agent="test-agent")
        manager.end_session(session.id)

        # Verify transcript metadata survived
        converter = SessionConverter(graph_dir / "sessions")
        final = converter.load(session.id)

        assert final.transcript_id == "test-uuid-1234"
        assert final.transcript_path == "/path/to/transcript.jsonl"
        assert final.transcript_git_branch == "main"
        assert final.transcript_synced_at is not None


class TestEdgeCases:
    """Test edge cases and error conditions."""

    def test_no_features_only_spikes(self, isolated_graph_dir_full, isolated_db):
        """Test session with no regular features, only auto-spikes."""
        graph_dir = isolated_graph_dir_full

        manager = SessionManager(graph_dir)

        # Start and end session without starting any features
        session = manager.start_session(agent="test-agent", title="No Features Session")
        manager.track_activity(
            session_id=session.id, tool="Read", summary="Just reading code"
        )
        manager.end_session(session.id)

        # Should have session-init spike that never completed
        spike_converter = NodeConverter(graph_dir / "spikes")
        spikes = spike_converter.load_all()
        init_spikes = [s for s in spikes if s.spike_subtype == "session-init"]

        assert len(init_spikes) == 1
        # Still in-progress since no feature was started
        assert init_spikes[0].status == "in-progress"

    def test_feature_start_without_session(self, isolated_graph_dir_full, isolated_db):
        """Test that feature start without active session still works."""
        graph_dir = isolated_graph_dir_full

        manager = SessionManager(graph_dir)

        # Start feature WITHOUT explicitly starting session
        # (SessionManager should auto-create session)
        feature = manager.create_feature(
            "No Explicit Session Feature", agent="test-agent"
        )
        manager.start_feature(feature.id, agent="test-agent")

        # Should have auto-created session and spikes
        converter = SessionConverter(graph_dir / "sessions")
        sessions = converter.load_all()
        active = [s for s in sessions if s.status == "active"]

        assert len(active) >= 1
        assert feature.id in active[0].worked_on

    def test_spike_persistence_across_reloads(
        self, isolated_graph_dir_full, isolated_db
    ):
        """Test that spikes persist correctly when reloading from disk."""
        graph_dir = isolated_graph_dir_full

        # Create session and feature with one manager instance
        manager1 = SessionManager(graph_dir)
        manager1.start_session(agent="test-agent", title="Persistence Test")
        feature = manager1.create_feature("Test Feature", agent="test-agent")
        manager1.start_feature(feature.id, agent="test-agent")
        manager1.complete_feature(feature.id, agent="test-agent")

        # Create new manager instance (simulates process restart)
        SessionManager(graph_dir)

        # Verify spikes are still there
        spike_converter = NodeConverter(graph_dir / "spikes")
        spikes = spike_converter.load_all()

        assert len(spikes) > 0
        init_spikes = [s for s in spikes if s.spike_subtype == "session-init"]
        transition_spikes = [s for s in spikes if s.spike_subtype == "transition"]

        assert len(init_spikes) == 1
        assert len(transition_spikes) == 1
        assert init_spikes[0].status == "done"


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
