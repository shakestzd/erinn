import logging

logger = logging.getLogger(__name__)

"""
Work Validation Module for HtmlGraph Hooks

Provides intelligent guidance for HtmlGraph workflow based on:
1. Current workflow state (work items, spikes)
2. Recent tool usage patterns (anti-pattern detection)
3. Learned patterns from transcript analytics

Subagents spawned via Task() have unrestricted tool access.
Detection uses 5-level strategy: env vars, session state, database.

This module can be used by hook scripts or imported directly for validation logic.

Main API:
    validate_tool_call(tool_name, tool_params, config, history) -> dict

Example:
    from htmlgraph.hooks.validator import validate_tool_call, load_validation_config, load_tool_history

    config = load_validation_config()
    history = load_tool_history()
    result = validate_tool_call("Edit", {"file_path": "test.py"}, config, history)

    if result["decision"] == "block":
        logger.debug("Validation reason: %s", result["reason"])
    elif "guidance" in result:
        logger.debug("Validation guidance: %s", result["guidance"])
"""

import json
import re
from pathlib import Path
from typing import Any, cast

from htmlgraph.hooks.subagent_detection import is_subagent_context
from htmlgraph.orchestrator_config import load_orchestrator_config


def get_anti_patterns(config: Any | None = None) -> dict[tuple[str, ...], str]:
    """
    Build anti-pattern rules from configuration.

    Args:
        config: Optional OrchestratorConfig. If None, loads from file.

    Returns:
        Dict mapping tool sequences to warning messages
    """
    if config is None:
        config = load_orchestrator_config()

    patterns = config.anti_patterns

    return {
        tuple(["Bash"] * patterns.consecutive_bash): (
            f"{patterns.consecutive_bash} consecutive Bash commands. "
            "Check for errors or consider a different approach."
        ),
        tuple(["Edit"] * patterns.consecutive_edit): (
            f"{patterns.consecutive_edit} consecutive Edits. "
            "Consider batching changes or reading file first."
        ),
        tuple(["Grep"] * patterns.consecutive_grep): (
            f"{patterns.consecutive_grep} consecutive Greps. "
            "Consider reading results before searching more."
        ),
        tuple(["Read"] * patterns.consecutive_read): (
            f"{patterns.consecutive_read} consecutive Reads. "
            "Consider caching file content."
        ),
    }


# Legacy constant for backwards compatibility (now uses config)
ANTI_PATTERNS = get_anti_patterns()

# Tools that indicate exploration/implementation (require work item in strict mode)
EXPLORATION_TOOLS = {"Grep", "Glob", "Task"}
IMPLEMENTATION_TOOLS = {"Edit", "Write", "NotebookEdit"}

# Optimal patterns to encourage
OPTIMAL_PATTERNS = {
    ("Grep", "Read"): "Good: Search then read - efficient exploration.",
    ("Read", "Edit"): "Good: Read then edit - informed changes.",
    ("Edit", "Bash"): "Good: Edit then test - verify changes.",
}

# Maximum number of recent tool calls to consider for pattern detection
MAX_HISTORY = 20


# Import shared db helper
from htmlgraph.hooks.db_helpers import get_db_from_config


def load_tool_history(session_id: str) -> list[dict]:
    """
    Load recent tool history from database (session-isolated).

    Args:
        session_id: Session identifier to filter tool history

    Returns:
        List of recent tool calls with tool name and timestamp
    """
    try:
        # Use shared db helper
        db = get_db_from_config()
        if not db or db.connection is None:
            return []

        cursor = db.connection.cursor()
        cursor.execute(
            """
            SELECT tool_name, timestamp
            FROM agent_events
            WHERE session_id = ?
            ORDER BY datetime(REPLACE(SUBSTR(timestamp, 1, 19), 'T', ' ')) DESC
            LIMIT ?
        """,
            (session_id, MAX_HISTORY),
        )

        # Return in chronological order (oldest first) for pattern detection
        rows = cursor.fetchall()
        db.disconnect()

        return [{"tool": row[0], "timestamp": row[1]} for row in reversed(rows)]
    except Exception:
        # Graceful degradation - return empty history on error
        return []


def record_tool(tool: str, session_id: str) -> None:
    """
    Record a tool use in database.

    Note: This is now handled by track-event.py hook, so this function
    is kept for backward compatibility but does nothing.

    Args:
        tool: Tool name being called
        session_id: Session identifier for isolation
    """
    # Tool recording is now handled by track-event.py PostToolUse hook
    # This function is kept for backward compatibility but does nothing
    pass


def detect_anti_pattern(tool: str, history: list[dict]) -> str | None:
    """Check if adding this tool creates an anti-pattern (uses configurable thresholds)."""
    # Load fresh anti-patterns from config
    anti_patterns = get_anti_patterns()

    # Get max pattern length to know how far to look back
    max_pattern_len = max(len(p) for p in anti_patterns.keys()) if anti_patterns else 5
    recent_tools = [h["tool"] for h in history[-max_pattern_len:]] + [tool]

    for pattern, message in anti_patterns.items():
        pattern_len = len(pattern)
        if len(recent_tools) >= pattern_len:
            # Check if recent tools end with this pattern
            if tuple(recent_tools[-pattern_len:]) == pattern:
                return message

    return None


def detect_optimal_pattern(tool: str, history: list[dict]) -> str | None:
    """Check if this tool continues an optimal pattern."""
    if not history:
        return None

    last_tool = history[-1]["tool"]
    pair = (last_tool, tool)

    return OPTIMAL_PATTERNS.get(pair)


def get_pattern_guidance(tool: str, history: list[dict]) -> dict[str, Any]:
    """Get guidance based on tool patterns."""
    # Check for anti-patterns first
    anti_pattern = detect_anti_pattern(tool, history)
    if anti_pattern:
        return {"pattern_warning": f"⚠️ {anti_pattern}", "pattern_type": "anti-pattern"}

    # Check for optimal patterns
    optimal = detect_optimal_pattern(tool, history)
    if optimal:
        return {"pattern_note": optimal, "pattern_type": "optimal"}

    return {}


def get_session_health_hint(history: list[dict]) -> str | None:
    """Get a health hint based on session patterns."""
    if len(history) < 10:
        return None

    tools = [h["tool"] for h in history]

    # Check for excessive retries
    consecutive = 1
    max_consecutive = 1
    for i in range(1, len(tools)):
        if tools[i] == tools[i - 1]:
            consecutive += 1
            max_consecutive = max(max_consecutive, consecutive)
        else:
            consecutive = 1

    if max_consecutive >= 5:
        return f"📊 High retry pattern detected ({max_consecutive} consecutive same-tool calls). Consider varying approach."

    # Check tool diversity
    unique_tools = len(set(tools))
    if unique_tools <= 2 and len(tools) >= 10:
        return f"📊 Low tool diversity. Only using {unique_tools} different tools. Consider using more specialized tools."

    return None


def load_validation_config() -> dict[str, Any]:
    """Load validation config with defaults."""
    config_path = (
        Path(__file__).parent.parent.parent.parent.parent
        / ".claude"
        / "config"
        / "validation-config.json"
    )

    if config_path.exists():
        try:
            with open(config_path) as f:
                return cast(dict[Any, Any], json.load(f))
        except Exception:
            pass

    # Minimal fallback config
    return {
        "always_allow": {
            "tools": ["Read", "Glob", "Grep", "LSP"],
            "bash_patterns": ["^git status", "^git diff", "^ls", "^cat"],
        },
        "sdk_commands": {"patterns": ["^uv run htmlgraph ", "^htmlgraph "]},
    }


def is_always_allowed(
    tool: str, params: dict[str, Any], config: dict[str, Any]
) -> bool:
    """Check if tool is always allowed (read-only operations)."""
    # Always-allow tools
    if tool in config.get("always_allow", {}).get("tools", []):
        return True

    # Read-only Bash patterns
    if tool == "Bash":
        command = params.get("command", "")

        # Check git commands using shared classification
        if command.strip().startswith("git"):
            from htmlgraph.hooks.git_commands import should_allow_git_command

            if should_allow_git_command(command):
                return True

        # Check other bash patterns
        for pattern in config.get("always_allow", {}).get("bash_patterns", []):
            if re.match(pattern, command):
                return True

    return False


def is_direct_htmlgraph_write(tool: str, params: dict[str, Any]) -> tuple[bool, str]:
    """Check if attempting direct write to .htmlgraph/ (always denied)."""
    if tool not in ["Write", "Edit", "Delete", "NotebookEdit"]:
        return False, ""

    file_path = params.get("file_path", "")
    if ".htmlgraph/" in file_path or file_path.startswith(".htmlgraph/"):
        return True, file_path

    return False, ""


def is_sdk_command(tool: str, params: dict[str, Any], config: dict[str, Any]) -> bool:
    """Check if Bash command is an SDK command."""
    if tool != "Bash":
        return False

    command = params.get("command", "")
    for pattern in config.get("sdk_commands", {}).get("patterns", []):
        if re.match(pattern, command):
            return True

    return False


def is_code_operation(
    tool: str, params: dict[str, Any], config: dict[str, Any]
) -> bool:
    """Check if operation modifies code."""
    # Direct file operations
    if tool in config.get("code_operations", {}).get("tools", []):
        return True

    # Code-modifying Bash commands
    if tool == "Bash":
        command = params.get("command", "")
        for pattern in config.get("code_operations", {}).get("bash_patterns", []):
            if re.match(pattern, command):
                return True

    return False


def get_active_work_item() -> dict | None:
    """Get active work item using SDK."""
    try:
        from htmlgraph import SDK

        sdk = SDK()
        active = sdk.get_active_work_item()
        return cast(dict | None, active)
    except Exception:
        # If SDK fails, assume no active work item
        return None


def check_orchestrator_violation(
    tool: str, params: dict[str, Any], session_id: str = "unknown"
) -> dict | None:
    """
    Check if operation violates orchestrator mode rules.

    This function detects when orchestrator.py would warn about a violation
    and converts it to a blocking decision when in strict mode.

    Args:
        tool: Tool name
        params: Tool parameters
        session_id: Session identifier for loading tool history

    Returns:
        Blocking response dict if violation detected in strict mode, None otherwise
    """
    try:
        from pathlib import Path

        from htmlgraph.orchestrator_mode import OrchestratorModeManager

        # Find .htmlgraph directory
        cwd = Path.cwd()
        graph_dir = cwd / ".htmlgraph"

        if not graph_dir.exists():
            for parent in [cwd.parent, cwd.parent.parent, cwd.parent.parent.parent]:
                candidate = parent / ".htmlgraph"
                if candidate.exists():
                    graph_dir = candidate
                    break

        if not graph_dir.exists():
            return None

        manager = OrchestratorModeManager(graph_dir)

        if not manager.is_enabled():
            return None

        if manager.get_enforcement_level() != "strict":
            return None

        # Import orchestrator logic
        from htmlgraph.hooks.orchestrator import (
            create_task_suggestion,
            is_allowed_orchestrator_operation,
        )

        is_allowed, reason, category = is_allowed_orchestrator_operation(
            tool, params, session_id
        )

        # If orchestrator would block (but returns continue=True), we block here
        if not is_allowed:
            suggestion = create_task_suggestion(tool, params)

            return {
                "decision": "block",
                "reason": (
                    f"🚫 ORCHESTRATOR MODE VIOLATION: {reason}\n\n"
                    f"⚠️  WARNING: Direct operations waste context and break delegation pattern!\n\n"
                    f"Suggested delegation:\n"
                    f"{suggestion}\n\n"
                    f"See ORCHESTRATOR_DIRECTIVES in session context for HtmlGraph delegation pattern.\n"
                    f"To disable orchestrator mode: uv run htmlgraph orchestrator disable"
                ),
                "suggestion": "Use Task tool to delegate this work to a subagent",
                "required_action": "DELEGATE_TO_SUBAGENT",
            }

        return None

    except Exception:
        # Graceful degradation - allow on error
        return None


def validate_tool_call(
    tool: str,
    params: dict[str, Any],
    config: dict[str, Any],
    history: list[dict],
    session_id: str | None = None,
) -> dict[str, Any]:
    """
    Validate tool call and return GUIDANCE with active learning.

    Subagents spawned via Task() have unrestricted tool access.
    Detection uses 5-level strategy: env vars, session state, database.

    Args:
        tool: Tool name (e.g., "Edit", "Bash", "Read")
        params: Tool parameters (e.g., {"file_path": "test.py"})
        config: Validation configuration (from load_validation_config())
        history: Tool usage history (from load_tool_history(session_id))
        session_id: Optional session ID for loading history if not provided

    Returns:
        dict[str, Any]: {"decision": "allow" | "block", "guidance": "...", "suggestion": "...", ...}
              All operations are ALLOWED unless blocked for safety reasons.

    Example:
        session_id = tool_input.get("session_id", "unknown")
        history = load_tool_history(session_id)
        result = validate_tool_call("Edit", {"file_path": "test.py"}, config, history)
        if result["decision"] == "block":
            logger.debug("Validation reason: %s", result["reason"])
        elif "guidance" in result:
            logger.debug("Validation guidance: %s", result["guidance"])
    """
    # Check if this is a subagent context - subagents have unrestricted tool access
    if is_subagent_context():
        return {"decision": "allow"}

    result = {"decision": "allow"}
    guidance_parts = []

    # PHASE 1 FIX: Remove double enforcement
    # Let orchestrator handle orchestration checks, validator only handles other patterns
    # (Orchestrator violations were being checked twice: once in orchestrator.py, once here)

    # Step 0b: Check for pattern-based guidance (Active Learning)
    pattern_info = get_pattern_guidance(tool, history)
    if pattern_info.get("pattern_warning"):
        guidance_parts.append(pattern_info["pattern_warning"])

    # Check session health
    health_hint = get_session_health_hint(history)
    if health_hint:
        guidance_parts.append(health_hint)

    # Step 1: Read-only tools - minimal guidance
    if is_always_allowed(tool, params, config):
        if guidance_parts:
            result["guidance"] = " | ".join(guidance_parts)
        return result

    # Step 2: Direct writes to .htmlgraph/ - BLOCK (not guidance)
    # This is the ONLY blocking rule - all other rules are guidance only
    is_htmlgraph_write, file_path = is_direct_htmlgraph_write(tool, params)
    if is_htmlgraph_write:
        # Return blocking response - this will be handled specially
        return {
            "decision": "block",
            "reason": f"BLOCKED: Direct edits to .htmlgraph/ files are not allowed. File: {file_path}",
            "suggestion": "Use SDK instead: `from htmlgraph import SDK; sdk = SDK(); sdk.features.complete('id')`",
            "documentation": "See AGENTS.md line 3: 'AI agents must NEVER edit .htmlgraph/ HTML files directly'",
        }

    # Step 3: Classify operation
    is_sdk_cmd = is_sdk_command(tool, params, config)
    is_code_op = is_code_operation(tool, params, config)

    # Step 4: Get active work item
    active = get_active_work_item()

    # Step 5: No active work item
    if active is None:
        # Check for strict enforcement mode
        strict_mode = config.get("enforcement", {}).get(
            "strict_work_item_required", False
        )

        if is_sdk_cmd:
            guidance_parts.append("Creating work item via SDK")
        elif strict_mode and (tool in IMPLEMENTATION_TOOLS or is_code_op):
            # STRICT MODE: BLOCK implementation without work item
            return {
                "decision": "block",
                "reason": (
                    "🛑 BLOCKED: No active work item.\n\n"
                    "You MUST create and start a work item BEFORE making code changes.\n\n"
                    "Run this FIRST:\n"
                    "  sdk = SDK(agent='claude')\n"
                    "  feature = sdk.features.create('Your feature title').save()\n"
                    "  sdk.features.start(feature.id)\n\n"
                    "Then retry your edit."
                ),
                "suggestion": "sdk.features.create('Title').save() then sdk.features.start(id)",
                "required_action": "CREATE_WORK_ITEM",
            }
        elif strict_mode and tool in EXPLORATION_TOOLS:
            # STRICT MODE: Strong guidance for exploration (allow but warn loudly)
            result["required_action"] = "CREATE_WORK_ITEM"
            result["imperative"] = (
                "⚠️ WARNING: No active work item for exploration.\n"
                "Consider creating a spike first:\n"
                "  sdk = SDK(agent='claude')\n"
                "  spike = sdk.spikes.create('Investigation title').save()\n"
                "  sdk.spikes.start(spike.id)"
            )
            guidance_parts.append("⚠️ No work item - consider creating a spike first")
        elif tool in EXPLORATION_TOOLS or tool in IMPLEMENTATION_TOOLS or is_code_op:
            guidance_parts.append(
                "⚠️ No active work item. Create one to track this work."
            )
            result["suggestion"] = (
                "sdk.features.create('Title').save() then sdk.features.start(id)"
            )

        if guidance_parts:
            result["guidance"] = " | ".join(guidance_parts)
        return result

    # Step 6: Active work is a spike (planning phase)
    if active.get("type") == "spike":
        spike_id = active.get("id")

        if is_sdk_cmd:
            guidance_parts.append(f"Planning with spike {spike_id}")
        elif tool in ["Write", "Edit", "Delete", "NotebookEdit"] or is_code_op:
            guidance_parts.append(
                f"Active spike ({spike_id}) is for planning. Consider creating a feature for implementation."
            )
            result["suggestion"] = "uv run htmlgraph feature create 'Feature title'"

        if guidance_parts:
            result["guidance"] = " | ".join(guidance_parts)
        return result

    # Step 7: Active work is feature/bug/chore - all good
    work_item_id = active.get("id")
    guidance_parts.append(f"Working on {work_item_id}")

    # Add positive reinforcement for optimal patterns
    if pattern_info.get("pattern_note"):
        guidance_parts.append(pattern_info["pattern_note"])

    if guidance_parts:
        result["guidance"] = " | ".join(guidance_parts)

    return result


def main() -> None:
    """Hook entry point for script wrapper."""
    import sys

    try:
        # Read tool input from stdin
        tool_input = json.load(sys.stdin)

        # Claude Code uses "name" and "input", fallback to "tool" and "params"
        tool = tool_input.get("name", "") or tool_input.get("tool", "")
        params = tool_input.get("input", {}) or tool_input.get("params", {})

        # Get session_id from hook_input (NEW: required for session-isolated history)
        session_id = tool_input.get("session_id", "unknown")

        # Load config
        config = load_validation_config()

        # Load session-isolated tool history (NEW: from database, not file)
        history = load_tool_history(session_id)

        # Get guidance with pattern awareness
        result = validate_tool_call(tool, params, config, history)

        # Note: Tool recording is now handled by track-event.py PostToolUse hook
        # No need to call record_tool() or save_tool_history() here

        # Output JSON with guidance/block message
        print(json.dumps(result))

        # Exit 1 to BLOCK if decision is "block", otherwise allow
        if result.get("decision") == "block":
            sys.exit(1)
        else:
            sys.exit(0)

    except Exception as e:
        # Graceful degradation - allow on error
        print(
            json.dumps({"decision": "allow", "guidance": f"Validation hook error: {e}"})
        )
        sys.exit(0)
