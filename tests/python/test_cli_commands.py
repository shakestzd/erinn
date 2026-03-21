"""
Backend tests for HtmlGraph CLI commands and functionality.

Run with: uv run pytest tests/python/test_cli_commands.py
"""

import shutil
import tempfile
from datetime import datetime
from pathlib import Path

import pytest
from htmlgraph import HtmlGraph
from htmlgraph.models import Edge, Node


@pytest.fixture
def temp_graph_dir():
    """Create a temporary directory for testing."""
    temp_dir = Path(tempfile.mkdtemp())
    yield temp_dir
    shutil.rmtree(temp_dir)


@pytest.fixture
def sample_graph(temp_graph_dir):
    """Create a sample graph for testing."""
    graph = HtmlGraph(temp_graph_dir)
    return graph


def test_graph_initialization(temp_graph_dir):
    """Test HtmlGraph initialization."""
    graph = HtmlGraph(temp_graph_dir)
    assert graph.directory == temp_graph_dir
    assert graph.directory.exists()


def test_add_node(sample_graph):
    """Test adding a node to the graph."""
    node = Node(
        id="test-feature-001",
        title="Test Feature",
        type="feature",
        status="todo",
        priority="high",
        created=datetime.now(),
        updated=datetime.now(),
        content="Test feature content",
    )

    sample_graph.add(node)

    # Verify file was created
    node_file = sample_graph.directory / "test-feature-001.html"
    assert node_file.exists()


def test_get_node(sample_graph):
    """Test retrieving a node from the graph."""
    # Add a node first
    node = Node(
        id="test-feature-002",
        title="Test Feature 2",
        type="feature",
        status="in-progress",
        priority="medium",
        created=datetime.now(),
        updated=datetime.now(),
    )
    sample_graph.add(node)

    # Retrieve it
    retrieved = sample_graph.get("test-feature-002")
    assert retrieved is not None
    assert retrieved.id == "test-feature-002"
    assert retrieved.title == "Test Feature 2"
    assert retrieved.status == "in-progress"


def test_update_node(sample_graph):
    """Test updating a node in the graph."""
    # Add a node
    node = Node(
        id="test-feature-003",
        title="Test Feature 3",
        type="feature",
        status="todo",
        priority="low",
        created=datetime.now(),
        updated=datetime.now(),
    )
    sample_graph.add(node)

    # Update it
    node.status = "done"
    node.priority = "high"
    sample_graph.update(node)

    # Retrieve and verify
    updated = sample_graph.get("test-feature-003")
    assert updated.status == "done"
    assert updated.priority == "high"


def test_node_with_edges(sample_graph):
    """Test node with edge relationships."""
    node = Node(
        id="test-feature-004",
        title="Test Feature with Edges",
        type="feature",
        status="todo",
        priority="medium",
        created=datetime.now(),
        updated=datetime.now(),
        edges={
            "blocks": [
                Edge(
                    target_id="test-feature-005",
                    relationship="blocks",
                    title="Blocked Feature",
                )
            ],
            "related": [
                Edge(
                    target_id="test-feature-006",
                    relationship="related",
                    title="Related Feature",
                )
            ],
        },
    )

    sample_graph.add(node)

    # Retrieve and verify edges
    retrieved = sample_graph.get("test-feature-004")
    assert "blocks" in retrieved.edges
    assert len(retrieved.edges["blocks"]) == 1
    assert retrieved.edges["blocks"][0].target_id == "test-feature-005"


def test_node_with_steps(sample_graph):
    """Test node with implementation steps."""
    node = Node(
        id="test-feature-005",
        title="Test Feature with Steps",
        type="feature",
        status="in-progress",
        priority="high",
        created=datetime.now(),
        updated=datetime.now(),
        steps=[
            {"description": "Step 1", "completed": True},
            {"description": "Step 2", "completed": False},
            {"description": "Step 3", "completed": False},
        ],
    )

    sample_graph.add(node)

    # Retrieve and verify steps
    retrieved = sample_graph.get("test-feature-005")
    assert len(retrieved.steps) == 3
    assert retrieved.steps[0]["completed"] is True
    assert retrieved.steps[1]["completed"] is False


def test_query_nodes(sample_graph):
    """Test querying nodes with CSS selector."""
    # Add multiple nodes
    for i in range(5):
        status = "done" if i < 2 else "todo"
        node = Node(
            id=f"test-feature-{i:03d}",
            title=f"Test Feature {i}",
            type="feature",
            status=status,
            priority="medium",
            created=datetime.now(),
            updated=datetime.now(),
        )
        sample_graph.add(node)

    # Query for done features
    done_features = sample_graph.query('[data-status="done"]')
    assert len(done_features) == 2

    # Query for todo features
    todo_features = sample_graph.query('[data-status="todo"]')
    assert len(todo_features) == 3


def test_node_properties(sample_graph):
    """Test node with custom properties."""
    node = Node(
        id="test-feature-props",
        title="Test Feature with Properties",
        type="feature",
        status="todo",
        priority="high",
        created=datetime.now(),
        updated=datetime.now(),
        properties={"epic_id": "epic-001", "estimated_hours": 8, "assignee": "claude"},
    )

    sample_graph.add(node)

    # Retrieve and verify properties
    retrieved = sample_graph.get("test-feature-props")
    assert retrieved.properties["epic_id"] == "epic-001"
    assert retrieved.properties["estimated_hours"] == 8
    assert retrieved.properties["assignee"] == "claude"


def test_invalid_node_id():
    """Test that invalid node ID raises appropriate error."""
    with pytest.raises((ValueError, KeyError, AttributeError)):
        Node(
            id="",  # Empty ID should fail
            title="Invalid",
            type="feature",
            status="todo",
            priority="medium",
            created=datetime.now(),
            updated=datetime.now(),
        )


@pytest.mark.parametrize("status", ["todo", "in-progress", "blocked", "done"])
def test_node_status_values(sample_graph, status):
    """Test that all valid status values work."""
    node = Node(
        id=f"test-status-{status}",
        title=f"Test {status}",
        type="feature",
        status=status,
        priority="medium",
        created=datetime.now(),
        updated=datetime.now(),
    )

    sample_graph.add(node)
    retrieved = sample_graph.get(f"test-status-{status}")
    assert retrieved.status == status


@pytest.mark.parametrize("priority", ["low", "medium", "high"])
def test_node_priority_values(sample_graph, priority):
    """Test that all valid priority values work."""
    node = Node(
        id=f"test-priority-{priority}",
        title=f"Test {priority}",
        type="feature",
        status="todo",
        priority=priority,
        created=datetime.now(),
        updated=datetime.now(),
    )

    sample_graph.add(node)
    retrieved = sample_graph.get(f"test-priority-{priority}")
    assert retrieved.priority == priority


def test_cli_init_bootstraps_events_index_and_hooks(temp_graph_dir):
    import argparse

    from htmlgraph.cli.core import InitCommand

    # Use a fresh temp project dir (with no .gitignore yet).
    args = argparse.Namespace(
        dir=str(temp_graph_dir),
        install_hooks=False,
        no_index=False,
        no_update_gitignore=False,
        no_events_keep=False,
        interactive=False,
    )

    # Use the new InitCommand class
    command = InitCommand.from_args(args)
    command.execute()

    graph_dir = temp_graph_dir / ".htmlgraph"
    # index.html is no longer generated by init (FastAPI dashboard archived;
    # use `htmlgraph serve` to launch the Docker dashboard instead)
    assert (graph_dir / "index.sqlite").exists()
    assert (graph_dir / "hooks" / "post-commit.sh").exists()
    assert (graph_dir / "hooks" / "post-checkout.sh").exists()
    assert (graph_dir / "hooks" / "post-merge.sh").exists()
    assert (graph_dir / "hooks" / "pre-push.sh").exists()

    gitignore = (temp_graph_dir / ".gitignore").read_text(encoding="utf-8")
    assert ".htmlgraph/index.sqlite" in gitignore
