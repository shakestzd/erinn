#!/usr/bin/env -S uv run
# /// script
# requires-python = ">=3.10"
# dependencies = [
#   "htmlgraph",
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

# Bootstrap Python path and setup
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from bootstrap import bootstrap_pythonpath, resolve_project_dir

project_dir_for_import = resolve_project_dir()
bootstrap_pythonpath(project_dir_for_import)

# Import all business logic from SDK prompt_analyzer
from htmlgraph.hooks.context import HookContext
from htmlgraph.hooks.prompt_analyzer import (
    classify_cigs_intent,
    classify_prompt,
    generate_cigs_guidance,
    generate_guidance,
    get_active_work_item,
    get_open_work_items,
    get_session_violation_count,
)


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

        # Write UserQuery event to DB — required for Turn 1 backfill attribution
        try:
            import uuid

            from htmlgraph.db.schema import HtmlGraphDB

            db = HtmlGraphDB(str(context.graph_dir / "htmlgraph.db"))
            db.insert_event(
                event_id=str(uuid.uuid4()),
                agent_id=context.agent_id or "claude-code",
                event_type="user_query",
                session_id=context.session_id,
                tool_name="UserQuery",
                tool_input={"prompt": prompt[:500]},
            )
            db.close()
        except Exception:
            pass  # Never block on DB write failure

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

        # 5. Generate workflow guidance (SDK)
        workflow_guidance = generate_guidance(
            classification, active_work, prompt, open_work_items=open_items
        )

        # 6. CIGS: Generate imperative delegation guidance (SDK)
        cigs_guidance = generate_cigs_guidance(
            cigs_intent, violation_count, waste_tokens, prompt
        )

        # 7. Combine both guidance types
        combined_guidance = []

        if cigs_guidance:
            combined_guidance.append(cigs_guidance)

        if workflow_guidance:
            combined_guidance.append(workflow_guidance)

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
