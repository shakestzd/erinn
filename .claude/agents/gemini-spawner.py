#!/usr/bin/env python3
"""Gemini Spawner Agent - Executable wrapper for Gemini CLI with event tracking."""

import argparse
import json
import os
import sys
import time
import uuid
from typing import Any


def main() -> None:
    """Execute Gemini spawner with comprehensive event tracking and delegation records."""
    parser = argparse.ArgumentParser(
        description="Spawn Gemini AI agent in headless mode",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  %(prog)s -p "Write a Python function" -m gemini-2.0-flash
  %(prog)s -p "Analyze code" --include-directories src/
  %(prog)s -p "Generate test cases" --output-format json
        """,
    )

    parser.add_argument(
        "-p",
        "--prompt",
        required=True,
        help="Task description for Gemini",
    )
    parser.add_argument(
        "-m",
        "--model",
        default=None,
        help="Model selection (e.g., 'gemini-2.0-flash'). Default: None",
    )
    parser.add_argument(
        "--output-format",
        choices=["json", "stream-json"],
        default="stream-json",
        help="Output format (enables real-time tracking if stream-json). Default: stream-json",
    )
    parser.add_argument(
        "--include-directories",
        action="append",
        dest="include_directories",
        help="Directories to include for context (can be specified multiple times)",
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
                        "spawned_agent": "gemini-2.0-flash",
                        "spawner_type": "gemini",
                        "model": args.model,
                        "timeout": args.timeout,
                        "output_format": args.output_format,
                    },
                    parent_event_id=parent_query_event or parent_event_id,
                    subagent_type="gemini",
                    cost_tokens=0,
                )

                # Record collaboration handoff
                db.record_collaboration(
                    handoff_id=f"hand-{uuid.uuid4().hex[:8]}",
                    from_agent=parent_agent,
                    to_agent="gemini-2.0-flash",
                    session_id=parent_session or f"session-{uuid.uuid4().hex[:8]}",
                    handoff_type="delegation",
                    reason=args.prompt[:200],
                    context={
                        "model": args.model,
                        "spawner": "gemini",
                        "cost": "FREE",
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
                spawner_type="gemini",
                session_id=parent_session,
            )
        except Exception:
            # Tracker is optional
            pass

        # Set environment for spawned process
        os.environ["HTMLGRAPH_AGENT"] = "gemini-2.0-flash"
        if delegation_event_id:
            os.environ["HTMLGRAPH_PARENT_EVENT"] = delegation_event_id

        # 2. RECORD INITIALIZATION PHASE
        init_event = None
        if tracker:
            try:
                init_event = tracker.record_phase(
                    "Initializing Spawner",
                    spawned_agent="gemini-2.0-flash",
                    tool_name="HeadlessSpawner.initialize",
                    input_summary=f"Preparing Gemini spawner for: {args.prompt[:100]}...",
                )
            except Exception:
                pass

        # 3. EXECUTE SPAWNER
        exec_event = None
        if tracker:
            try:
                exec_event = tracker.record_phase(
                    "Executing Gemini",
                    spawned_agent="gemini-2.0-flash",
                    tool_name="gemini-cli",
                    input_summary=args.prompt[:200],
                )
            except Exception:
                pass

        spawner = HeadlessSpawner()
        result = spawner.spawn_gemini(
            prompt=args.prompt,
            output_format=args.output_format,
            model=args.model,
            include_directories=args.include_directories,
            track_in_htmlgraph=args.track,
            timeout=args.timeout,
            tracker=tracker,
            parent_event_id=delegation_event_id,
        )

        duration = time.time() - start_time

        # 4. COMPLETE EXECUTION PHASE
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

        # 5. COMPLETE INITIALIZATION PHASE
        if tracker and init_event:
            try:
                tracker.complete_phase(
                    init_event["event_id"],
                    output_summary="Spawner initialized successfully",
                    status="completed",
                )
            except Exception:
                pass

        # 6. TRACK COMPLETION
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
                "agent": "gemini-2.0-flash",
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
            "model": args.model,
            "agent": "gemini-2.0-flash",
            "duration": duration,
            "cost": 0.0,
            "delegation_event_id": delegation_event_id,
        }
        print(json.dumps(success_output))
        sys.exit(0)

    except ImportError:
        error_output = {
            "success": False,
            "error": "HtmlGraph SDK not installed. Install with: pip install htmlgraph",
            "agent": "gemini-2.0-flash",
        }
        print(json.dumps(error_output), file=sys.stderr)
        sys.exit(1)
    except Exception as e:
        error_output = {
            "success": False,
            "error": f"Unexpected error: {type(e).__name__}: {e}",
            "agent": "gemini-2.0-flash",
        }
        print(json.dumps(error_output), file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
