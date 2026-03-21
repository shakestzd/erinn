"""
Shared Constants for HtmlGraph Hooks

This module centralizes constants used across multiple hook files to eliminate
duplication and ensure consistency.

Constants:
    NEVER_BLOCK_TOOLS: Tools that should never be blocked by enforcement
    DRIFT_QUEUE_FILE: Filename for drift detection queue
"""

from __future__ import annotations

# Tools that should NEVER be blocked by enforcement
# These are essential for coordination, orchestration, and exploration
NEVER_BLOCK_TOOLS = {
    "Task",
    "TaskCreate",
    "TaskUpdate",
    "TaskList",
    "TaskGet",
    "AskUserQuestion",
    "TodoWrite",
    "TodoRead",
    "Skill",
    "Read",
    "Grep",
    "Glob",
    "WebSearch",
    "WebFetch",
}

# Drift classification queue filename (stored in session directory)
DRIFT_QUEUE_FILE = "drift-queue.json"


__all__ = [
    "NEVER_BLOCK_TOOLS",
    "DRIFT_QUEUE_FILE",
]
