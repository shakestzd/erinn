"""
Tests for BaseCollection.filter() method.

This method allows filtering with custom lambda predicates for more complex queries.
"""

from datetime import datetime, timedelta
from pathlib import Path

import pytest
from htmlgraph import SDK, Node


@pytest.fixture
def sdk(isolated_graph_dir_full: Path, isolated_db: Path):
    """Create a temporary SDK instance."""
    # isolated_graph_dir_full already has all required subdirectories
    return SDK(
        directory=isolated_graph_dir_full, agent="test-agent", db_path=str(isolated_db)
    )


@pytest.fixture
def test_track(sdk: SDK):
    """Create a test track for feature linkage."""
    return sdk.tracks.create("Test Track").save()


def test_filter_by_title_substring(sdk: SDK, test_track):
    """Test filtering features by substring in title."""
    # Create features with different titles
    sdk.features.create("High Priority Authentication").set_track(test_track.id).save()
    sdk.features.create("Low Priority Logging").set_track(test_track.id).save()
    sdk.features.create("High Priority Database").set_track(test_track.id).save()
    sdk.features.create("Medium Priority API").set_track(test_track.id).save()

    # Filter features with "High" in title
    high_priority = sdk.features.filter(lambda f: "High" in f.title)

    assert len(high_priority) == 2
    assert all("High" in f.title for f in high_priority)


def test_filter_by_multiple_conditions(sdk: SDK, test_track):
    """Test filtering with multiple conditions."""
    # Create features with different priorities and statuses
    sdk.features.create("Feature 1", priority="high", status="todo").set_track(
        test_track.id
    ).save()
    sdk.features.create("Feature 2", priority="high", status="in-progress").set_track(
        test_track.id
    ).save()
    sdk.features.create("Feature 3", priority="low", status="todo").set_track(
        test_track.id
    ).save()
    sdk.features.create("Feature 4", priority="high", status="done").set_track(
        test_track.id
    ).save()

    # Filter: high priority AND todo status
    urgent = sdk.features.filter(lambda f: f.priority == "high" and f.status == "todo")

    assert len(urgent) == 1
    assert urgent[0].title == "Feature 1"
    assert urgent[0].priority == "high"
    assert urgent[0].status == "todo"


def test_filter_by_creation_date(sdk: SDK, test_track):
    """Test filtering by creation date."""
    # Create a feature
    feature = sdk.features.create("Recent Feature").set_track(test_track.id).save()

    # Filter features created in the last hour
    recent = sdk.features.filter(
        lambda f: f.created > datetime.now() - timedelta(hours=1)
    )

    assert len(recent) == 1
    assert recent[0].id == feature.id


def test_filter_empty_results(sdk: SDK, test_track):
    """Test that filter returns empty list when no matches."""
    # Create some features
    sdk.features.create("Feature 1", priority="low").set_track(test_track.id).save()
    sdk.features.create("Feature 2", priority="medium").set_track(test_track.id).save()

    # Filter for non-existent condition
    critical = sdk.features.filter(lambda f: f.priority == "critical")

    assert critical == []


def test_filter_with_agent_assigned(sdk: SDK, test_track):
    """Test filtering by agent assignment."""
    # Create features assigned to different agents
    f1 = sdk.features.create("Feature 1").set_track(test_track.id).save()
    f2 = sdk.features.create("Feature 2").set_track(test_track.id).save()
    f3 = sdk.features.create("Feature 3").set_track(test_track.id).save()

    # Assign to different agents
    with sdk.features.edit(f1.id) as feature:
        feature.agent_assigned = "claude"
    with sdk.features.edit(f2.id) as feature:
        feature.agent_assigned = "gemini"
    with sdk.features.edit(f3.id) as feature:
        feature.agent_assigned = "claude"

    # Filter by agent
    claude_features = sdk.features.filter(lambda f: f.agent_assigned == "claude")

    assert len(claude_features) == 2
    assert all(f.agent_assigned == "claude" for f in claude_features)


def test_filter_respects_node_type(sdk: SDK, test_track):
    """Test that filter only returns nodes of the correct type."""
    # Create features and bugs
    sdk.features.create("Feature 1", priority="high").set_track(test_track.id).save()

    from htmlgraph.graph import HtmlGraph

    bugs_graph = HtmlGraph(sdk._directory / "bugs")
    bug = Node(id="bug-001", title="Bug 1", type="bug", priority="high")
    bugs_graph.add(bug)

    # Filter features by priority
    high_priority_features = sdk.features.filter(lambda f: f.priority == "high")

    # Should only return features, not bugs
    assert len(high_priority_features) == 1
    assert all(f.type == "feature" for f in high_priority_features)


def test_filter_with_complex_logic(sdk: SDK, test_track):
    """Test filter with complex predicate logic."""
    # Create features with various attributes
    sdk.features.create(
        "Urgent Auth Fix", priority="critical", status="todo"
    ).set_track(test_track.id).save()
    sdk.features.create("Normal Feature", priority="medium", status="todo").set_track(
        test_track.id
    ).save()
    sdk.features.create("High Priority Done", priority="high", status="done").set_track(
        test_track.id
    ).save()
    sdk.features.create(
        "Critical In Progress", priority="critical", status="in-progress"
    ).set_track(test_track.id).save()

    # Complex filter: (critical OR high) AND (todo OR in-progress)
    important_active = sdk.features.filter(
        lambda f: (
            (f.priority in ["critical", "high"])
            and (f.status in ["todo", "in-progress"])
        )
    )

    assert len(important_active) == 2
    titles = [f.title for f in important_active]
    assert "Urgent Auth Fix" in titles
    assert "Critical In Progress" in titles
