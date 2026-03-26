import logging

logger = logging.getLogger(__name__)

"""
Orchestrator Reflection Module

Detects when Claude executes Python code directly via Bash and provides
a gentle reflection prompt to encourage delegation to subagents.

This helps reinforce orchestrator patterns:
- Delegation over direct execution
- Parallel Task() calls for efficiency
- Work item tracking for all efforts

Usage:
    from htmlgraph.hooks.orchestrator_reflector import orchestrator_reflect

    # In a posttooluse hook
    hook_input = {
        "tool_name": "Bash",
        "tool_input": {"command": "uv run pytest"}
    }
    result = orchestrator_reflect(hook_input)
    # Returns: {"continue": True, "hookSpecificOutput": {...}}
"""

import re
from typing import Any, TypedDict


class HookSpecificOutput(TypedDict):
    """Hook-specific output structure."""

    hookEventName: str
    additionalContext: str


class HookResponse(TypedDict):
    """Hook response structure."""

    continue_: bool
    hookSpecificOutput: HookSpecificOutput | None


# Python execution patterns to detect
PYTHON_EXECUTION_PATTERNS = [
    r"\buv\s+run\b",  # uv run <anything>
    r"\bpython\s+-c\b",  # python -c "code"
    r"\bpython\s+[\w/.-]+\.py\b",  # python script.py
    r"\bpython\s+-m\s+\w+",  # python -m module
    r"\bpytest\b",  # pytest
    r"\bpython3\s+",  # python3 command
]

# Commands to exclude from detection
EXCLUDED_COMMAND_PREFIXES = ("ls ", "cat ", "grep ", "find ")


def is_python_execution(command: str) -> bool:
    """
    Detect if a bash command is executing Python code.

    Patterns to detect:
    - uv run <script>
    - python -c <code>
    - python <script>
    - pytest
    - python -m <module>

    Excludes:
    - simple file operations (ls, cat, grep, find)
    - simple tool calls that happen to have "python" in path

    Args:
        command: The bash command to analyze

    Returns:
        True if the command executes Python code
    """
    # Normalize command
    cmd = command.strip().lower()

    # Exclude simple file operations
    if cmd.startswith(EXCLUDED_COMMAND_PREFIXES):
        return False

    # Check for Python execution patterns
    for pattern in PYTHON_EXECUTION_PATTERNS:
        if re.search(pattern, cmd):
            return True

    return False


def should_reflect(hook_input: dict[str, Any]) -> tuple[bool, str]:
    """
    Check if we should show reflection prompt.

    Args:
        hook_input: The hook input data containing tool name and tool input

    Returns:
        (should_show, command_preview) tuple where:
        - should_show: True if reflection should be shown
        - command_preview: Preview of the command (first 60 chars)
    """
    tool_name = hook_input.get("tool_name", "")

    # Only check Bash tool usage
    if tool_name != "Bash":
        return False, ""

    # Get the command
    tool_input = hook_input.get("tool_input", {})
    command = tool_input.get("command", "")

    if not command:
        return False, ""

    # Check if it's Python execution
    if is_python_execution(command):
        # Create a preview of the command (first 60 chars)
        preview = command[:60].replace("\n", " ")
        if len(command) > 60:
            preview += "..."
        return True, preview

    return False, ""


def build_reflection_message(command_preview: str) -> str:
    """
    Build the reflection message for orchestrator.

    This should be:
    - Gentle and non-blocking
    - Encourage reflection without being preachy
    - Point to specific alternatives

    Args:
        command_preview: Preview of the executed command

    Returns:
        The formatted reflection message
    """
    return f"""ORCHESTRATOR REFLECTION: You executed code directly.

Command: {command_preview}

Ask yourself:
- Could this have been delegated to a subagent?
- Would parallel Task() calls have been faster?
- Is a work item tracking this effort?

Continue, but consider delegation for similar future tasks."""


def orchestrator_reflect(tool_input: dict[str, Any]) -> dict[str, Any]:
    """
    Main API function for orchestrator reflection.

    Analyzes tool usage and provides reflection feedback when direct
    Python execution is detected.

    Args:
        tool_input: Hook input containing tool_name and tool_input fields

    Returns:
        Hook response dict with continue=True and optional hookSpecificOutput

    Example:
        >>> hook_input = {
        ...     "tool_name": "Bash",
        ...     "tool_input": {"command": "uv run pytest"}
        ... }
        >>> result = orchestrator_reflect(hook_input)
        >>> result["continue"]
        True
        >>> "hookSpecificOutput" in result
        True
    """
    # Check if we should reflect
    should_show, command_preview = should_reflect(tool_input)

    # Build response
    response: dict[str, Any] = {"continue": True}

    if should_show:
        reflection = build_reflection_message(command_preview)
        response["hookSpecificOutput"] = {
            "hookEventName": "PostToolUse",
            "additionalContext": reflection,
        }

    return response


def main() -> None:
    """Hook entry point for script wrapper."""
    import json
    import os
    import sys

    # Check if tracking is disabled
    if os.environ.get("HTMLGRAPH_DISABLE_TRACKING") == "1":
        print(json.dumps({"continue": True}))
        sys.exit(0)

    try:
        hook_input = json.load(sys.stdin)
    except json.JSONDecodeError:
        hook_input = {}

    # Run reflection logic
    response = orchestrator_reflect(hook_input)

    # Output JSON response
    print(json.dumps(response))
