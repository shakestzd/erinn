import logging

logger = logging.getLogger(__name__)

"""
Unified PostToolUse Hook - Parallel Execution of Multiple Tasks

This module provides a unified PostToolUse hook that runs multiple tasks
in parallel using asyncio:
1. Event tracking - logs tool usage to session events
2. Orchestrator reflection - provides delegation suggestions
3. Task validation - validates task results
4. Error tracking - logs errors and auto-creates debug spikes
5. Debugging suggestions - suggests resources when errors detected
6. CIGS analysis - cost accounting and reinforcement for delegation
7. Cost tracking - tracks token usage and costs to database

Architecture:
- All tasks run simultaneously via asyncio.gather()
- Error tracking logs to .htmlgraph/errors.jsonl
- Auto-creates debug spikes after 3+ similar errors
- Cost tracking uses CostCalculator estimates (Claude Code doesn't provide actual usage)
- Returns combined response with all feedback

Performance:
- ~40-50% faster than sequential execution
- Single Python process (no subprocess overhead)
- Parallel execution maximizes throughput
"""

import asyncio
import json
import os
import sys
from pathlib import Path
from typing import Any

from htmlgraph.analytics.cost_monitor import CostMonitor
from htmlgraph.cigs import CIGSPostToolAnalyzer
from htmlgraph.cigs.cost import CostCalculator
from htmlgraph.hooks.event_tracker import track_event
from htmlgraph.hooks.orchestrator_reflector import orchestrator_reflect
from htmlgraph.hooks.post_tool_use_failure import run as track_error
from htmlgraph.hooks.task_validator import validate_task_results


async def run_event_tracking(
    hook_type: str, hook_input: dict[str, Any]
) -> dict[str, Any]:
    """
    Run event tracking (async wrapper).

    Args:
        hook_type: "PostToolUse" or "Stop"
        hook_input: Hook input with tool execution details

    Returns:
        Event tracking response: {"continue": True, "hookSpecificOutput": {...}}
    """
    try:
        loop = asyncio.get_event_loop()

        # Run in thread pool since it involves I/O
        return await loop.run_in_executor(
            None,
            track_event,
            hook_type,
            hook_input,
        )
    except Exception:
        # Graceful degradation - allow on error
        return {"continue": True}


async def run_orchestrator_reflection(hook_input: dict[str, Any]) -> dict[str, Any]:
    """
    Run orchestrator reflection (async wrapper).

    Args:
        hook_input: Hook input with tool execution details

    Returns:
        Reflection response: {"continue": True, "hookSpecificOutput": {...}}
    """
    try:
        loop = asyncio.get_event_loop()

        # Run in thread pool
        return await loop.run_in_executor(
            None,
            orchestrator_reflect,
            hook_input,
        )
    except Exception:
        # Graceful degradation - allow on error
        return {"continue": True}


async def run_task_validation(hook_input: dict[str, Any]) -> dict[str, Any]:
    """
    Run task result validation (async wrapper).

    Args:
        hook_input: Hook input with tool execution details

    Returns:
        Validation response: {"continue": True, "hookSpecificOutput": {...}}
    """
    try:
        loop = asyncio.get_event_loop()

        tool_name = hook_input.get("name", "") or hook_input.get("tool_name", "")
        tool_response = hook_input.get("result", {}) or hook_input.get(
            "tool_response", {}
        )

        # Run task validation
        return await loop.run_in_executor(
            None,
            validate_task_results,
            tool_name,
            tool_response,
        )
    except Exception:
        # Graceful degradation - allow on error
        return {"continue": True}


async def run_error_tracking(hook_input: dict[str, Any]) -> dict[str, Any]:
    """
    Track errors to .htmlgraph/errors.jsonl and auto-create debug spikes.

    Only tracks ACTUAL errors, not responses containing the word "error".

    Args:
        hook_input: Hook input with tool execution details

    Returns:
        Error tracking response: {"continue": True}
    """
    try:
        loop = asyncio.get_event_loop()

        # Check if this is an ACTUAL error
        has_error = False
        tool_response = hook_input.get("tool_response") or hook_input.get("result", {})

        if isinstance(tool_response, dict):
            # Bash: non-empty stderr indicates error
            stderr = tool_response.get("stderr", "")
            if stderr and isinstance(stderr, str) and stderr.strip():
                has_error = True

            # Explicit error field with content
            error_field = tool_response.get("error")
            if error_field and str(error_field).strip():
                has_error = True

            # success=false flag
            if tool_response.get("success") is False:
                has_error = True

        # Only track if there's an actual error
        if has_error:
            return await loop.run_in_executor(
                None,
                track_error,
                hook_input,
            )

        return {"continue": True}
    except Exception:
        # Graceful degradation - allow on error
        return {"continue": True}


async def suggest_debugging_resources(hook_input: dict[str, Any]) -> dict[str, Any]:
    """
    Suggest debugging resources based on tool results.

    Only triggers on ACTUAL errors, not on responses that happen to contain
    the word "error" in their content.

    Args:
        hook_input: Hook input with tool execution details

    Returns:
        Suggestion response: {"hookSpecificOutput": {"additionalContext": "..."}}
    """
    try:
        tool_name = hook_input.get("name", "") or hook_input.get("tool_name", "")
        tool_response = hook_input.get("result", {}) or hook_input.get(
            "tool_response", {}
        )

        suggestions = []

        # Check for ACTUAL errors (not just text containing "error")
        has_actual_error = False

        if isinstance(tool_response, dict):
            # Bash: non-empty stderr indicates error
            stderr = tool_response.get("stderr", "")
            if stderr and isinstance(stderr, str) and stderr.strip():
                has_actual_error = True

            # Explicit error field
            if tool_response.get("error"):
                has_actual_error = True

            # success=false flag
            if tool_response.get("success") is False:
                has_actual_error = True

        if has_actual_error:
            suggestions.append("⚠️ Error detected in tool response")
            suggestions.append("Debugging resources:")
            suggestions.append("  📚 DEBUGGING.md - Systematic debugging guide")
            suggestions.append("  🔬 Researcher agent - Research error patterns")
            suggestions.append("  🐛 Debugger agent - Root cause analysis")
            suggestions.append("  Built-in: /doctor, /hooks, claude --debug")

        # Check for Task tool without save evidence
        if tool_name == "Task":
            result_text = str(tool_response).lower()
            save_indicators = [".save()", "spike", "htmlgraph", ".create("]
            if not any(ind in result_text for ind in save_indicators):
                suggestions.append("💡 Task completed - remember to document findings")
                suggestions.append(
                    "  See DEBUGGING.md for research documentation patterns"
                )

        if suggestions:
            return {
                "hookSpecificOutput": {
                    "hookEventName": "PostToolUse",
                    "additionalContext": "\n".join(suggestions),
                }
            }

        return {}
    except Exception:
        # Graceful degradation - no suggestions on error
        return {}


async def run_cigs_analysis(hook_input: dict[str, Any]) -> dict[str, Any]:
    """
    Run CIGS cost accounting and reinforcement analysis.

    Args:
        hook_input: Hook input with tool execution details

    Returns:
        CIGS analysis response: {"hookSpecificOutput": {...}}
    """
    try:
        loop = asyncio.get_event_loop()

        # Extract tool info
        tool_name = hook_input.get("name", "") or hook_input.get("tool_name", "")
        tool_params = hook_input.get("input", {}) or hook_input.get("tool_input", {})
        tool_response = hook_input.get("result", {}) or hook_input.get(
            "tool_response", {}
        )

        # Initialize CIGS analyzer
        graph_dir = Path.cwd() / ".htmlgraph"
        analyzer = CIGSPostToolAnalyzer(graph_dir)

        # Run analysis in executor (may involve I/O)
        return await loop.run_in_executor(
            None,
            analyzer.analyze,
            tool_name,
            tool_params,
            tool_response,
        )
    except Exception:
        # Graceful degradation - allow on error
        return {}


async def run_cost_tracking(hook_input: dict[str, Any]) -> dict[str, Any]:
    """
    Track token costs using CostMonitor.

    Since Claude Code doesn't provide actual token usage in hook_input,
    we estimate costs using CostCalculator heuristics.

    Args:
        hook_input: Hook input with tool execution details

    Returns:
        Cost tracking response: {"continue": True}
    """
    try:
        loop = asyncio.get_event_loop()

        # Extract tool info
        tool_name = hook_input.get("name", "") or hook_input.get("tool_name", "")
        tool_params = hook_input.get("input", {}) or hook_input.get("tool_input", {})
        session_id = hook_input.get("session_id", "") or hook_input.get("sessionId", "")

        # Skip if no session_id
        if not session_id:
            return {"continue": True}

        # Get model from environment or hook_input
        model = os.environ.get("HTMLGRAPH_MODEL") or "claude-sonnet-4-5-20250929"

        # Get agent info
        agent_id = os.environ.get("HTMLGRAPH_AGENT") or "claude-code"
        subagent_type = os.environ.get("HTMLGRAPH_SUBAGENT_TYPE")

        # Estimate token costs using CostCalculator
        cost_calc = CostCalculator()
        estimated_tokens = cost_calc.predict_cost(tool_name, tool_params)

        # For simplicity, assume 70% input tokens, 30% output tokens
        input_tokens = int(estimated_tokens * 0.7)
        output_tokens = int(estimated_tokens * 0.3)

        # Track costs using CostMonitor
        def track_cost() -> None:
            monitor = CostMonitor()
            try:
                # Generate event_id for this cost record
                from htmlgraph.ids import generate_id

                event_id = generate_id("event")

                monitor.track_token_usage(
                    session_id=session_id,
                    event_id=event_id,
                    tool_name=tool_name,
                    model=model,
                    input_tokens=input_tokens,
                    output_tokens=output_tokens,
                    agent_id=agent_id,
                    subagent_type=subagent_type,
                )
            finally:
                monitor.disconnect()

        # Run in executor (involves database I/O)
        await loop.run_in_executor(None, track_cost)

        return {"continue": True}
    except Exception:
        # Graceful degradation - allow on error
        return {"continue": True}


async def posttooluse_hook(
    hook_type: str, hook_input: dict[str, Any]
) -> dict[str, Any]:
    """
    Unified PostToolUse hook - runs tracking, reflection, validation, error tracking, debugging suggestions, CIGS analysis, and cost tracking in parallel.

    Args:
        hook_type: "PostToolUse" or "Stop"
        hook_input: Hook input with tool execution details

    Returns:
        Claude Code standard format:
        {
            "continue": True,
            "hookSpecificOutput": {
                "hookEventName": "PostToolUse",
                "additionalContext": "Combined feedback",
                "systemMessage": "Warnings/alerts"
            }
        }
    """
    # Run all seven in parallel using asyncio.gather
    (
        event_response,
        reflection_response,
        validation_response,
        error_tracking_response,
        debug_suggestions,
        cigs_response,
        cost_response,
    ) = await asyncio.gather(
        run_event_tracking(hook_type, hook_input),
        run_orchestrator_reflection(hook_input),
        run_task_validation(hook_input),
        run_error_tracking(hook_input),
        suggest_debugging_resources(hook_input),
        run_cigs_analysis(hook_input),
        run_cost_tracking(hook_input),
    )

    # Combine responses (all should return continue=True)
    # Event tracking is async and shouldn't block
    # Reflection provides optional guidance
    # Validation provides warnings but doesn't block

    # Collect all guidance and messages
    guidance_parts = []
    system_messages = []

    # Event tracking guidance (e.g., drift warnings)
    if "hookSpecificOutput" in event_response:
        ctx = event_response["hookSpecificOutput"].get("additionalContext", "")
        if ctx:
            guidance_parts.append(ctx)

    # Orchestrator reflection
    if "hookSpecificOutput" in reflection_response:
        ctx = reflection_response["hookSpecificOutput"].get("additionalContext", "")
        if ctx:
            guidance_parts.append(ctx)

    # Task validation feedback
    if "hookSpecificOutput" in validation_response:
        ctx = validation_response["hookSpecificOutput"].get("additionalContext", "")
        if ctx:
            guidance_parts.append(ctx)

        # Task validation may provide systemMessage for warnings
        sys_msg = validation_response["hookSpecificOutput"].get("systemMessage", "")
        if sys_msg:
            system_messages.append(sys_msg)

    # Debugging suggestions
    if "hookSpecificOutput" in debug_suggestions:
        ctx = debug_suggestions["hookSpecificOutput"].get("additionalContext", "")
        if ctx:
            guidance_parts.append(ctx)

    # CIGS analysis (cost accounting and reinforcement)
    if "hookSpecificOutput" in cigs_response:
        ctx = cigs_response["hookSpecificOutput"].get("additionalContext", "")
        if ctx:
            guidance_parts.append(ctx)

    # Build unified response
    response: dict[str, Any] = {"continue": True}  # PostToolUse never blocks

    if guidance_parts or system_messages:
        response["hookSpecificOutput"] = {
            "hookEventName": "PostToolUse",
        }

        if guidance_parts:
            response["hookSpecificOutput"]["additionalContext"] = "\n".join(
                guidance_parts
            )

        if system_messages:
            response["hookSpecificOutput"]["systemMessage"] = "\n\n".join(
                system_messages
            )

    return response


def main() -> None:
    """Hook entry point for script wrapper."""
    # Check environment override
    if os.environ.get("HTMLGRAPH_DISABLE_TRACKING") == "1":
        print(json.dumps({"continue": True}))
        sys.exit(0)

    # Determine hook type from environment
    hook_type = os.environ.get("HTMLGRAPH_HOOK_TYPE", "PostToolUse")

    # Read tool input from stdin
    try:
        hook_input = json.load(sys.stdin)
    except json.JSONDecodeError:
        hook_input = {}

    # Run hook with parallel execution
    result = asyncio.run(posttooluse_hook(hook_type, hook_input))

    # Output response
    print(json.dumps(result))
    sys.exit(0)


if __name__ == "__main__":
    main()
