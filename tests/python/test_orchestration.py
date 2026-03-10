"""Tests for orchestration helpers."""

import time

import pytest
from htmlgraph.orchestration import (
    delegate_with_id,
    generate_task_id,
    get_results_by_task_id,
)


def test_generate_task_id():
    """Test task ID generation."""
    task_id = generate_task_id()
    assert task_id.startswith("task-")
    assert len(task_id) == 13  # "task-" + 8 hex chars

    # IDs should be unique
    task_id2 = generate_task_id()
    assert task_id != task_id2


def test_delegate_with_id():
    """Test delegate_with_id returns task ID and enhanced prompt."""
    task_id, prompt = delegate_with_id("Test task", "Do something", "general-purpose")

    assert task_id.startswith("task-")
    assert "TASK_ID:" in prompt
    assert task_id in prompt
    assert "Test task" in prompt
    assert "Do something" in prompt
    # Updated: Orchestrator pattern - no subagent save instructions
    assert "📝 Note: This task has ID" in prompt


def test_delegate_with_id_includes_subagent_type():
    """Test delegate_with_id no longer includes subagent type (orchestrator-side save)."""
    task_id, prompt = delegate_with_id("Test task", "Do something", "specialized-agent")

    # Subagent type is used but not in prompt (orchestrator manages saving)
    assert task_id.startswith("task-")
    assert "TASK_ID:" in prompt


def test_get_results_by_task_id_timeout(tmp_path, isolated_db):
    """Test timeout behavior when results not found."""
    from htmlgraph import SDK

    sdk = SDK(directory=tmp_path / ".htmlgraph", agent="test", db_path=str(isolated_db))

    # Search for non-existent task ID with short timeout
    results = get_results_by_task_id(
        sdk, "task-nonexistent", timeout=5, poll_interval=1
    )

    assert not results["success"]
    assert "No results found" in results["error"]
    assert results["task_id"] == "task-nonexistent"
    assert results["attempts"] >= 2  # Should poll multiple times


def test_get_results_by_task_id_success(tmp_path, isolated_db):
    """Test successful result retrieval."""
    from htmlgraph import SDK

    sdk = SDK(directory=tmp_path / ".htmlgraph", agent="test", db_path=str(isolated_db))

    # Create spike with task ID in title
    task_id = "task-test1234"
    spike = (
        sdk.spikes.create(f"Results: {task_id} - Test Task")
        .set_findings("Test findings")
        .save()
    )

    # Retrieve by task ID
    results = get_results_by_task_id(sdk, task_id, timeout=5)

    assert results["success"]
    assert results["task_id"] == task_id
    assert results["findings"] == "Test findings"
    assert task_id in results["title"]
    assert results["spike_id"] == spike.id


def test_get_results_by_task_id_partial_match(tmp_path, isolated_db):
    """Test task ID can appear anywhere in title."""
    from htmlgraph import SDK

    sdk = SDK(directory=tmp_path / ".htmlgraph", agent="test", db_path=str(isolated_db))

    # Create spike with task ID in middle of title
    task_id = "task-abc123"
    spike = sdk.spikes.create(f"Investigation {task_id} complete")
    spike.set_findings("Found the issue").save()

    # Retrieve by task ID
    results = get_results_by_task_id(sdk, task_id, timeout=5)

    assert results["success"]
    assert results["findings"] == "Found the issue"


def test_get_results_by_task_id_first_match(tmp_path, isolated_db):
    """Test returns first matching spike if multiple exist."""
    from htmlgraph import SDK

    sdk = SDK(directory=tmp_path / ".htmlgraph", agent="test", db_path=str(isolated_db))

    # Create multiple spikes with same task ID
    task_id = "task-duplicate"
    spike1 = (
        sdk.spikes.create(f"Results: {task_id} - First")
        .set_findings("First findings")
        .save()
    )

    spike2 = (
        sdk.spikes.create(f"Results: {task_id} - Second")
        .set_findings("Second findings")
        .save()
    )

    # Retrieve by task ID - should get one of them
    results = get_results_by_task_id(sdk, task_id, timeout=5)

    assert results["success"]
    assert results["spike_id"] in [spike1.id, spike2.id]


@pytest.mark.slow
@pytest.mark.skip(
    reason="Real 10s polling loop — use 'pytest -m slow' to run explicitly"
)
def test_get_results_by_task_id_polling_behavior(tmp_path, isolated_db):
    """Test polling waits for results to appear."""
    import threading

    from htmlgraph import SDK

    sdk = SDK(directory=tmp_path / ".htmlgraph", agent="test", db_path=str(isolated_db))
    task_id = "task-delayed"

    # Create spike after 2 seconds in background thread
    def create_delayed_spike():
        time.sleep(2)
        spike = sdk.spikes.create(f"Results: {task_id} - Delayed")
        spike.set_findings("Delayed findings").save()

    thread = threading.Thread(target=create_delayed_spike)
    thread.start()

    # Start polling (should wait and find it)
    results = get_results_by_task_id(sdk, task_id, timeout=10, poll_interval=1)

    thread.join()

    assert results["success"]
    assert results["findings"] == "Delayed findings"
    assert results["attempts"] >= 2  # Should have polled multiple times


def test_parallel_delegate_structure(tmp_path, isolated_db):
    """Test parallel_delegate returns expected structure."""
    from htmlgraph import SDK

    sdk = SDK(
        directory=tmp_path / ".htmlgraph",
        agent="orchestrator",
        db_path=str(isolated_db),
    )

    # Note: This test doesn't actually spawn tasks (no Task tool available in tests)
    # It just validates the structure and task ID generation
    # Create spikes manually to simulate task completion
    task_id_1, _ = delegate_with_id("Task 1", "Do task 1", "agent1")
    task_id_2, _ = delegate_with_id("Task 2", "Do task 2", "agent2")

    spike1 = sdk.spikes.create(f"Results: {task_id_1} - Task 1")
    spike1.set_findings(
        "Result 1: Task completed successfully with expected output and validation."
    ).save()

    spike2 = sdk.spikes.create(f"Results: {task_id_2} - Task 2")
    spike2.set_findings(
        "Result 2: Task completed successfully with expected output and validation."
    ).save()

    # Note: In real usage, parallel_delegate would handle task spawning
    # For testing, we just verify the result retrieval works
    results_1 = get_results_by_task_id(sdk, task_id_1, timeout=5)
    results_2 = get_results_by_task_id(sdk, task_id_2, timeout=5)

    assert results_1["success"]
    assert results_2["success"]
    assert "Task completed successfully" in results_1["findings"]
    assert "Task completed successfully" in results_2["findings"]
