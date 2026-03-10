from htmlgraph.session_manager import SessionManager


def test_cross_agent_attribution(tmp_path):
    manager = SessionManager(tmp_path)

    # Setup:
    # Feature A is claimed by "gemini" (and is primary)
    feat_a = manager.create_feature("Gemini Feature")
    manager.claim_feature(feat_a.id, agent="gemini")
    manager.start_feature(feat_a.id, agent="gemini")
    manager.set_primary_feature(feat_a.id, agent="gemini")

    # Feature B is claimed by "claude"
    # No sleep needed: generate_id uses random entropy, not timestamp-only
    feat_b = manager.create_feature("Claude Feature")
    manager.claim_feature(feat_b.id, agent="claude")
    manager.start_feature(feat_b.id, agent="claude")

    # Scenario: Claude performs an activity
    # We use a somewhat generic activity that might match "Gemini Feature"
    # if we aren't careful (especially since Gemini Feature is marked PRIMARY).
    session_claude = manager.start_session("sess-claude", agent="claude")

    entry = manager.track_activity(
        session_id=session_claude.id,
        tool="Edit",
        summary="Edited generic_helper.py",
        file_paths=["src/utils/generic_helper.py"],
    )

    # Expectation: It should NOT be attributed to Gemini's feature,
    # even though Gemini's feature is primary.
    # Ideally, it should go to Claude's feature (if relevant) or be unattributed.
    # It definitely shouldn't go to feat_a.

    print(f"Attributed to: {entry.feature_id}")

    assert entry.feature_id != feat_a.id, (
        f"Activity incorrectly attributed to Gemini's feature {feat_a.id} despite being performed by Claude"
    )

    # Even better, if we simulate relevance to Claude's feature, it should go there.
    # Let's say Claude's feature involves 'claude_stuff.py'
    feat_b.properties["file_patterns"] = ["*claude*"]
    manager.features_graph.update(feat_b)

    entry2 = manager.track_activity(
        session_id=session_claude.id,
        tool="Edit",
        summary="Edited claude_stuff.py",
        file_paths=["src/claude_stuff.py"],
    )

    assert entry2.feature_id == feat_b.id, (
        f"Activity should be attributed to Claude's feature {feat_b.id}, got {entry2.feature_id}"
    )
