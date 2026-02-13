"""
Tests for drift queue cleanup functionality.

Ensures that drift queue entries are properly cleaned up after processing
and don't accumulate indefinitely.
"""

import json
import tempfile
from datetime import datetime, timedelta
from pathlib import Path

from htmlgraph.hooks.drift import (
    add_to_drift_queue,
    clear_drift_queue_activities,
    load_drift_queue,
)


def test_drift_queue_cleanup_after_age():
    """Test that stale drift queue entries are removed based on max_age_hours."""

    # Create a temporary directory for testing
    with tempfile.TemporaryDirectory() as tmpdir:
        graph_dir = Path(tmpdir)

        # Create a drift queue with old and new activities
        old_timestamp = (datetime.now() - timedelta(hours=50)).isoformat()
        recent_timestamp = (datetime.now() - timedelta(hours=1)).isoformat()

        queue = {
            "activities": [
                {
                    "timestamp": old_timestamp,
                    "tool": "Write",
                    "summary": "Old activity (should be removed)",
                    "drift_score": 0.9,
                },
                {
                    "timestamp": recent_timestamp,
                    "tool": "Write",
                    "summary": "New activity (should be kept)",
                    "drift_score": 0.75,
                },
            ],
            "last_classification": None,
        }

        # Save the queue
        queue_path = graph_dir / "drift-queue.json"
        with open(queue_path, "w") as f:
            json.dump(queue, f)

        # Load the queue with default max_age_hours (48)
        loaded_queue = load_drift_queue(graph_dir, max_age_hours=48)

        # Verify old entries were removed
        assert len(loaded_queue["activities"]) == 1
        assert (
            loaded_queue["activities"][0]["summary"] == "New activity (should be kept)"
        )

        # Verify the file was updated
        with open(queue_path) as f:
            saved_queue = json.load(f)
        assert len(saved_queue["activities"]) == 1


def test_clear_drift_queue_activities():
    """Test that clear_drift_queue_activities removes all activities."""
    # Create a temporary directory for testing
    with tempfile.TemporaryDirectory() as tmpdir:
        graph_dir = Path(tmpdir)

        # Create a drift queue with activities
        queue = {
            "activities": [
                {
                    "timestamp": datetime.now().isoformat(),
                    "tool": "Write",
                    "summary": "Activity 1",
                    "drift_score": 0.9,
                },
                {
                    "timestamp": datetime.now().isoformat(),
                    "tool": "Edit",
                    "summary": "Activity 2",
                    "drift_score": 0.85,
                },
            ],
            "last_classification": None,
        }

        # Save the queue
        queue_path = graph_dir / "drift-queue.json"
        with open(queue_path, "w") as f:
            json.dump(queue, f)

        # Clear the activities
        clear_drift_queue_activities(graph_dir)

        # Verify queue was cleared
        with open(queue_path) as f:
            cleared_queue = json.load(f)

        assert len(cleared_queue["activities"]) == 0
        assert (
            cleared_queue["last_classification"] is not None
        )  # Should have a timestamp


def test_clear_drift_queue_preserves_last_classification():
    """Test that clearing the queue preserves the last_classification timestamp."""
    # Create a temporary directory for testing
    with tempfile.TemporaryDirectory() as tmpdir:
        graph_dir = Path(tmpdir)

        # Create a drift queue with a classification timestamp
        original_timestamp = "2025-12-25T10:00:00"
        queue = {
            "activities": [
                {
                    "timestamp": datetime.now().isoformat(),
                    "tool": "Write",
                    "summary": "Activity 1",
                    "drift_score": 0.9,
                }
            ],
            "last_classification": original_timestamp,
        }

        # Save the queue
        queue_path = graph_dir / "drift-queue.json"
        with open(queue_path, "w") as f:
            json.dump(queue, f)

        # Clear the activities
        clear_drift_queue_activities(graph_dir)

        # Verify timestamp was preserved
        with open(queue_path) as f:
            cleared_queue = json.load(f)

        assert len(cleared_queue["activities"]) == 0
        assert cleared_queue["last_classification"] == original_timestamp


def test_drift_queue_cleanup_integration():
    """
    Integration test: Verify that the entire flow works correctly.

    1. Add activities to queue
    2. Verify they're stored
    3. Clear the queue
    4. Verify they're removed
    """
    with tempfile.TemporaryDirectory() as tmpdir:
        graph_dir = Path(tmpdir)

        # Configuration
        config = {"queue": {"max_pending_classifications": 5, "max_age_hours": 48}}

        # Add multiple high-drift activities
        for i in range(3):
            activity = {
                "tool": "Write",
                "summary": f"Activity {i}",
                "file_paths": [f"file{i}.py"],
                "drift_score": 0.9,
                "feature_id": "feat-test",
            }
            queue = add_to_drift_queue(graph_dir, activity, config)

        # Verify activities were added
        assert len(queue["activities"]) == 3

        # Simulate successful classification - clear the queue
        clear_drift_queue_activities(graph_dir)

        # Load the queue and verify it's empty
        final_queue = load_drift_queue(graph_dir)
        assert len(final_queue["activities"]) == 0
        assert final_queue["last_classification"] is not None
