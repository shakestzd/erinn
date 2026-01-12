#!/usr/bin/env python3
"""Copilot Spawner Agent - Executable wrapper for Copilot CLI with event tracking."""

import argparse
import json
import os
import sys
import time
import uuid
from typing import Any


def main() -> None:
    """Execute Copilot spawner with comprehensive event tracking and delegation records."""
    parser = argparse.ArgumentParser(
        description="Spawn GitHub Copilot agent in headless mode",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  %(prog)s -p "Debug this issue" --allow-tools shell git
  %(prog)s -p "Implement feature" --allow-all-tools
  %(prog)s -p "Review code" --deny-tool dangerous-operation
        """,
    )

    parser.add_argument(
        "-p",
        "--prompt",
        required=True,
        help="Task description for Copilot",
    )
    parser.add_argument(
        "--allow-tool",
        action="append",
        dest="allow_tools",
        help="Tools to auto-approve (e.g., 'shell(git)'). Can be specified multiple times",
    )
    parser.add_argument(
        "--allow-all-tools",
        action="store_true",
        default=False,
        help="Auto-approve all tools. Default: False",
    )
    parser.add_argument(
        "--deny-tool",
        action="append",
        dest="deny_tools",
        help="Tools to deny. Can be specified multiple times",
    )
    parser.add_argument(
        "--timeout",
        type=int,
        default=120,
        help="Max seconds to wait. Default: 120",
    )
    parser.add_argument(
        "--track",
        action="store_true",
        default=True,
        help="Enable HtmlGraph activity tracking. Default: enabled",
    )
    parser.add_argument(
        "--no-track",
        action="store_false",
        dest="track",
        help="Disable HtmlGraph activity tracking",
    )

    args = parser.parse_args()
    start_time = time.time()

    # Get parent context from environment
    parent_session = os.getenv("HTMLGRAPH_PARENT_SESSION")
    parent_event_id = os.getenv("HTMLGRAPH_PARENT_EVENT")
    parent_query_event = os.getenv("HTMLGRAPH_PARENT_QUERY_EVENT")
    parent_agent = os.getenv("HTMLGRAPH_PARENT_AGENT", "orchestrator")

    try:
        from htmlgraph.orchestration import HeadlessSpawner

        # Initialize database for event tracking
        db = None
        delegation_event_id = None
        try:
            from htmlgraph.db.schema import HtmlGraphDB

            db = HtmlGraphDB()
        except Exception:
            # Tracking is optional, continue without it
            pass

        # 1. RECORD DELEGATION START
        if db and args.track:
            try:
                delegation_event_id = f"event-{uuid.uuid4().hex[:8]}"
                # Use parent_query_event (UserQuery) as the root parent, with parent_event_id (task_delegation) as the intermediate parent
                db.insert_event(
                    event_id=delegation_event_id,
                    agent_id=parent_agent,
                    event_type="delegation",
                    session_id=parent_session or f"session-{uuid.uuid4().hex[:8]}",
                    tool_name="Task",
                    input_summary=args.prompt[:200],
                    context={
                        "spawned_agent": "github-copilot",
                        "spawner_type": "copilot",
                        "allow_all_tools": args.allow_all_tools,
                        "allow_tools": args.allow_tools,
                        "deny_tools": args.deny_tools,
                        "timeout": args.timeout,
                    },
                    parent_event_id=parent_query_event or parent_event_id,
                    subagent_type="copilot",
                    cost_tokens=0,
                )

                # Record collaboration handoff
                db.record_collaboration(
                    handoff_id=f"hand-{uuid.uuid4().hex[:8]}",
                    from_agent=parent_agent,
                    to_agent="github-copilot",
                    session_id=parent_session or f"session-{uuid.uuid4().hex[:8]}",
                    handoff_type="delegation",
                    reason=args.prompt[:200],
                    context={
                        "spawner": "copilot",
                        "allow_all_tools": args.allow_all_tools,
                        "cost": "INCLUDED",
                    },
                )
            except Exception:
                # Non-fatal - tracking is best-effort
                pass

        # Initialize internal activity tracker
        tracker = None
        try:
            from spawner_event_tracker import SpawnerEventTracker

            tracker = SpawnerEventTracker(
                delegation_event_id=delegation_event_id,
                parent_agent=parent_agent,
                spawner_type="copilot",
                session_id=parent_session,
            )
        except Exception:
            # Tracker is optional
            pass

        # Set environment for spawned process
        os.environ["HTMLGRAPH_AGENT"] = "github-copilot"
        if delegation_event_id:
            os.environ["HTMLGRAPH_PARENT_EVENT"] = delegation_event_id

        # 2. RECORD INITIALIZATION PHASE
        init_event = None
        if tracker:
            try:
                init_event = tracker.record_phase(
                    "Initializing Copilot Spawner",
                    spawned_agent="github-copilot",
                    tool_name="HeadlessSpawner.initialize",
                    input_summary=f"Preparing Copilot spawner for: {args.prompt[:100]}...",
                )
            except Exception:
                pass

        # 3. RECORD GITHUB AUTH PHASE
        auth_event = None
        if tracker:
            try:
                auth_event = tracker.record_phase(
                    "Authenticating GitHub",
                    spawned_agent="github-copilot",
                    tool_name="HeadlessSpawner.authenticate_github",
                    input_summary="Setting up GitHub authentication",
                )
            except Exception:
                pass

        # 4. EXECUTE SPAWNER
        exec_event = None
        if tracker:
            try:
                exec_event = tracker.record_phase(
                    "Executing Copilot",
                    spawned_agent="github-copilot",
                    tool_name="copilot-cli",
                    input_summary=args.prompt[:200],
                )
            except Exception:
                pass

        spawner = HeadlessSpawner()
        result = spawner.spawn_copilot(
            prompt=args.prompt,
            allow_tools=args.allow_tools,
            allow_all_tools=args.allow_all_tools,
            deny_tools=args.deny_tools,
            track_in_htmlgraph=args.track,
            timeout=args.timeout,
            tracker=tracker,
            parent_event_id=delegation_event_id,
        )

        duration = time.time() - start_time

        # 5. COMPLETE EXECUTION PHASE
        if tracker and exec_event:
            try:
                output_summary = (
                    result.response[:200] if result.success else result.error[:200]
                )
                tracker.complete_phase(
                    exec_event["event_id"],
                    output_summary=output_summary,
                    status="completed" if result.success else "failed",
                )
            except Exception:
                pass

        # 6. COMPLETE AUTH PHASE
        if tracker and auth_event:
            try:
                tracker.complete_phase(
                    auth_event["event_id"],
                    output_summary="GitHub authentication successful",
                    status="completed",
                )
            except Exception:
                pass

        # 7. COMPLETE INITIALIZATION PHASE
        if tracker and init_event:
            try:
                tracker.complete_phase(
                    init_event["event_id"],
                    output_summary="Copilot spawner initialized successfully",
                    status="completed",
                )
            except Exception:
                pass

        # 8. TRACK COMPLETION
        if db and args.track and delegation_event_id:
            try:
                # Update event with completion metrics
                cursor = db.connection.cursor()
                cursor.execute(
                    """
                    UPDATE agent_events
                    SET output_summary = ?, status = ?, execution_duration_seconds = ?,
                        cost_tokens = ?, updated_at = CURRENT_TIMESTAMP
                    WHERE event_id = ?
                    """,
                    (
                        (
                            result.response[:200]
                            if result.success
                            else result.error[:200]
                        ),
                        "completed" if result.success else "failed",
                        duration,
                        result.tokens_used or 0,
                        delegation_event_id,
                    ),
                )
                db.connection.commit()
            except Exception:
                # Non-fatal
                pass

        if not result.success:
            # Return JSON error to stderr
            error_output: dict[str, Any] = {
                "success": False,
                "error": result.error,
                "tokens": result.tokens_used,
                "agent": "github-copilot",
                "duration": duration,
                "delegation_event_id": delegation_event_id,
            }
            print(json.dumps(error_output), file=sys.stderr)
            sys.exit(1)

        # Return JSON success to stdout
        success_output: dict[str, Any] = {
            "success": True,
            "response": result.response,
            "tokens": result.tokens_used,
            "allow_all_tools": args.allow_all_tools,
            "agent": "github-copilot",
            "duration": duration,
            "delegation_event_id": delegation_event_id,
        }
        print(json.dumps(success_output))
        sys.exit(0)

    except ImportError:
        error_output = {
            "success": False,
            "error": "HtmlGraph SDK not installed. Install with: pip install htmlgraph",
            "agent": "github-copilot",
        }
        print(json.dumps(error_output), file=sys.stderr)
        sys.exit(1)
    except Exception as e:
        error_output = {
            "success": False,
            "error": f"Unexpected error: {type(e).__name__}: {e}",
            "agent": "github-copilot",
        }
        print(json.dumps(error_output), file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
