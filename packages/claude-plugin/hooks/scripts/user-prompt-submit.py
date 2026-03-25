#!/usr/bin/env -S uv run
# /// script
# requires-python = ">=3.10"
# dependencies = [
#   "htmlgraph>=0.34.13",
# ]
# ///
"""
UserPromptSubmit Hook - Analyze prompts and guide workflow with CIGS integration.

This hook fires when the user submits a prompt. It analyzes the intent
and provides guidance to ensure proper HtmlGraph workflow:

1. Implementation requests -> Ensure work item exists + CIGS imperative guidance
2. Bug reports -> Guide to create bug first
3. Investigation requests -> Guide to create spike first
4. Continue/resume -> Check for existing work context
5. CIGS integration -> Pre-response delegation reminders based on intent

Hook Input (stdin): JSON with prompt details
Hook Output (stdout): JSON with guidance (additionalContext)

Thin wrapper around SDK prompt_analyzer module. All business logic lives in:
    htmlgraph.hooks.prompt_analyzer
"""

import json
import os
import sys
from pathlib import Path

# Bootstrap Python path and setup
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from bootstrap import bootstrap_pythonpath, resolve_project_dir

# Early exit if not an HtmlGraph project
_project_dir_check = os.environ.get("CLAUDE_PROJECT_DIR") or os.getcwd()
if not Path(_project_dir_check, ".htmlgraph").is_dir():
    print(json.dumps({}))
    sys.exit(0)

project_dir_for_import = resolve_project_dir()
bootstrap_pythonpath(project_dir_for_import)

# Import all business logic from SDK prompt_analyzer
from htmlgraph.hooks.context import HookContext
from htmlgraph.hooks.prompt_analyzer import (
    classify_cigs_intent,
    classify_prompt,
    create_user_query_event,
    generate_guidance,
    get_active_work_item,
    get_open_work_items,
    get_plugin_recommendations,
    get_session_violation_count,
)
from htmlgraph.hooks.version_check import check_hook_version

check_hook_version("0.34.14")


def main() -> None:
    """Main entry point with CIGS integration."""
    try:
        # Read prompt input from stdin
        hook_input = json.load(sys.stdin)
        prompt = hook_input.get("prompt", "")

        if not prompt:
            # No prompt - no guidance
            print(json.dumps({}))
            sys.exit(0)

        # Build HookContext for SDK functions that require it
        context = HookContext.from_input(hook_input)

        # 0. Create UserQuery event with automatic attribution to active work item
        # This ensures the user's prompt is recorded in the database and attributed
        # to the currently active feature/bug/spike (via fallback logic in _get_active_feature_id)
        create_user_query_event(context, prompt)

        # 0b. Auto-re-enable orchestrator if timeout/scope has expired
        auto_re_enable_note = None
        try:
            from pathlib import Path

            from htmlgraph.orchestrator_mode import OrchestratorModeManager

            _orch_manager = OrchestratorModeManager(Path(context.graph_dir))
            _orch_manager.increment_prompt_count()
            _re_enabled, _reason = _orch_manager.check_auto_re_enable()
            if _re_enabled:
                auto_re_enable_note = (
                    f"INFO: Orchestrator mode auto-re-enabled (reason: {_reason})"
                )
        except Exception:
            pass

        # 1. Classify the prompt (SDK)
        classification = classify_prompt(prompt)

        # 2. CIGS: Classify for delegation guidance (SDK)
        cigs_intent = classify_cigs_intent(prompt)

        # 3. CIGS: Get violation count (SDK)
        violation_count, waste_tokens = get_session_violation_count(context)

        # 4. Get active work item (SDK)
        active_work = get_active_work_item(context)

        # 4b. Get all open work items for attribution guidance (SDK)
        open_items = get_open_work_items(context)

        # 5. Generate workflow guidance (SDK) — compact attribution block only.
        # Static delegation imperatives live in the system prompt; per-turn
        # injection is the compact attribution block (~60 tokens) only.
        from pathlib import Path as _Path

        workflow_guidance = generate_guidance(
            classification,
            active_work,
            prompt,
            open_work_items=open_items,
            graph_dir=_Path(context.graph_dir),
        )

        # 6. CIGS imperative guidance suppressed per-turn: static rules are now
        # in orchestrator-system-prompt-optimized.txt. Calling generate_cigs_guidance
        # here would add ~500 tokens/turn unnecessarily.
        # cigs_guidance = generate_cigs_guidance(cigs_intent, violation_count, waste_tokens, prompt)

        # 7. Assemble combined guidance (attribution block only)
        combined_guidance = []

        if auto_re_enable_note:
            combined_guidance.append(auto_re_enable_note)

        if workflow_guidance:
            combined_guidance.append(workflow_guidance)

        # Skill Scout recommendations (from SessionStart cache, no network calls)
        try:
            plugin_recs = get_plugin_recommendations(context)
            if plugin_recs:
                combined_guidance.append(plugin_recs)
        except Exception:
            pass  # Never break prompt handling

        # Print the JSON output for Claude Code
        if combined_guidance:
            # Return combined guidance as additionalContext
            result = {
                "hookSpecificOutput": {
                    "hookEventName": "UserPromptSubmit",
                    "additionalContext": "\n\n".join(combined_guidance),
                },
                "classification": {
                    "implementation": classification["is_implementation"],
                    "investigation": classification["is_investigation"],
                    "bug_report": classification["is_bug_report"],
                    "continuation": classification["is_continuation"],
                    "confidence": classification["confidence"],
                },
                "cigs_classification": {
                    "involves_exploration": cigs_intent["involves_exploration"],
                    "involves_code_changes": cigs_intent["involves_code_changes"],
                    "involves_git": cigs_intent["involves_git"],
                    "intent_confidence": cigs_intent["intent_confidence"],
                },
                "cigs_session_status": {
                    "violation_count": violation_count,
                    "waste_tokens": waste_tokens,
                },
            }
            print(json.dumps(result))
        else:
            print(json.dumps({}))

        # Always allow - this hook provides guidance, not blocking
        sys.exit(0)

    except Exception as e:
        # Graceful degradation
        import traceback

        error_detail = traceback.format_exc()
        print(json.dumps({"error": str(e), "traceback": error_detail}), file=sys.stderr)
        # Still return empty result to not block
        print(json.dumps({}))
        sys.exit(0)


if __name__ == "__main__":
    main()
