#!/usr/bin/env -S uv run
# /// script
# requires-python = ">=3.10"
# dependencies = [
#   "htmlgraph",
# ]
# ///
"""
HtmlGraph SessionResume Hook (Thin Wrapper)

Fires when Claude Code resumes an existing conversation (SessionStart with
"resume" matcher). All business logic lives in htmlgraph.hooks.session_resume.

CRITICAL REQUIREMENTS:
- MUST exit with code 0 (never block Claude)
- MUST execute quickly (< 1 second)
- MUST handle all exceptions gracefully
"""

import json
import os
import sys

# Bootstrap Python path and setup
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from bootstrap import bootstrap_pythonpath, is_tracking_disabled, resolve_project_dir

if is_tracking_disabled():
    print(json.dumps({"continue": True}))
    sys.exit(0)


def main() -> None:
    """Main entry point - delegate to SDK handler."""
    # 1. Read hook input from stdin FIRST so we have cwd
    try:
        hook_input = json.load(sys.stdin)
    except json.JSONDecodeError:
        hook_input = {}

    # 2. Resolve project dir using cwd from hook input
    cwd = hook_input.get("cwd", os.getcwd())
    project_dir = resolve_project_dir(cwd)
    bootstrap_pythonpath(project_dir)

    # 3. Import handler now that pythonpath is set
    try:
        from htmlgraph.hooks.session_resume import run as handle_session_resume
    except Exception as e:
        print(
            f"Warning: HtmlGraph not available ({e}). Install with: pip install htmlgraph",
            file=sys.stderr,
        )
        print(json.dumps({"continue": True}))
        return

    # 4. Delegate to SDK handler
    try:
        response = handle_session_resume(hook_input)
        print(json.dumps(response))
    except Exception as e:
        print(f"Error: session_resume handler failed: {e}", file=sys.stderr)
        import traceback

        traceback.print_exc(file=sys.stderr)
        print(json.dumps({"continue": True}))


if __name__ == "__main__":
    main()
    sys.exit(0)
