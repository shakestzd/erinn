#!/usr/bin/env python3
"""Codex Spawner Agent - Executable wrapper for Codex CLI with event tracking."""

import argparse
import json
import os
import sys
import time
import uuid
from typing import Any


def main() -> None:
    """Execute Codex spawner with comprehensive event tracking and delegation records."""
    parser = argparse.ArgumentParser(
        description="Spawn Codex AI agent in headless mode",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  %(prog)s -p "Implement a feature" --sandbox workspace-write
  %(prog)s -p "Fix bugs in project" -m gpt-4-turbo
  %(prog)s -p "Generate documentation" --sandbox read-only
        """,
    )

    parser.add_argument(
        "-p",
        "--prompt",
        required=True,
        help="Task description for Codex",
    )
    parser.add_argument(
        "-m",
        "--model",
        default=None,
        help="Model selection (e.g., 'gpt-4-turbo'). Default: None",
    )
    parser.add_argument(
        "--sandbox",
        choices=["read-only", "workspace-write"],
        default=None,
        help="Sandbox mode ('read-only', 'workspace-write', or full). Default: None",
    )
    parser.add_argument(
        "--output-json",
        action="store_true",
        default=True,
        help="JSONL output format (enables real-time tracking). Default: enabled",
    )
    parser.add_argument(
        "--no-output-json",
        action="store_false",
        dest="output_json",
        help="Disable JSONL output format",
    )
    parser.add_argument(
        "--full-auto",
        action="store_true",
        default=True,
        help="Enable full auto mode (required for headless). Default: enabled",
    )
    parser.add_argument(
        "--no-full-auto",
        action="store_false",
        dest="full_auto",
        help="Disable full auto mode",
    )
    parser.add_argument(
        "--image",
        action="append",
        dest="images",
        help="Image paths to include (can be specified multiple times)",
    )
    parser.add_argument(
        "--output-last-message",
        default=None,
        help="Write last message to file. Default: None",
    )
    parser.add_argument(
        "--output-schema",
        default=None,
        help="JSON schema for validation. Default: None",
    )
    parser.add_argument(
        "--skip-git-check",
        action="store_true",
        default=False,
        help="Skip git repo check. Default: False",
    )
    parser.add_argument(
        "--cd",
        dest="working_directory",
        default=None,
        help="Workspace directory. Default: None",
    )
    parser.add_argument(
        "--use-oss",
        action="store_true",
        default=False,
        help="Use local Ollama provider. Default: False",
    )
    parser.add_argument(
        "--bypass-approvals",
        action="store_true",
        default=False,
        help="Bypass approval checks. Default: False",
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
                        "spawned_agent": "gpt-4",
                        "spawner_type": "codex",
                        "model": args.model or "gpt-4-turbo",
                        "sandbox": args.sandbox,
                        "timeout": args.timeout,
                    },
                    parent_event_id=parent_query_event or parent_event_id,
                    subagent_type="codex",
                    cost_tokens=0,
                )

                # Record collaboration handoff
                db.record_collaboration(
                    handoff_id=f"hand-{uuid.uuid4().hex[:8]}",
                    from_agent=parent_agent,
                    to_agent="gpt-4",
                    session_id=parent_session or f"session-{uuid.uuid4().hex[:8]}",
                    handoff_type="delegation",
                    reason=args.prompt[:200],
                    context={
                        "model": args.model or "gpt-4-turbo",
                        "spawner": "codex",
                        "sandbox": args.sandbox,
                        "cost": "PAID",
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
                spawner_type="codex",
                session_id=parent_session,
            )
        except Exception:
            # Tracker is optional
            pass

        # Set environment for spawned process
        os.environ["HTMLGRAPH_AGENT"] = "gpt-4"
        if delegation_event_id:
            os.environ["HTMLGRAPH_PARENT_EVENT"] = delegation_event_id

        # 2. RECORD INITIALIZATION PHASE
        init_event = None
        if tracker:
            try:
                init_event = tracker.record_phase(
                    "Initializing Codex Spawner",
                    spawned_agent="gpt-4",
                    tool_name="HeadlessSpawner.initialize",
                    input_summary=f"Preparing Codex spawner for: {args.prompt[:100]}...",
                )
            except Exception:
                pass

        # 3. RECORD SANDBOX SETUP PHASE
        sandbox_event = None
        if tracker and args.sandbox:
            try:
                sandbox_event = tracker.record_phase(
                    "Setting Up Sandbox",
                    spawned_agent="gpt-4",
                    tool_name="HeadlessSpawner.setup_sandbox",
                    input_summary=f"Sandbox mode: {args.sandbox}",
                )
            except Exception:
                pass

        # 4. EXECUTE SPAWNER
        exec_event = None
        if tracker:
            try:
                exec_event = tracker.record_phase(
                    "Executing Codex",
                    spawned_agent="gpt-4",
                    tool_name="codex-cli",
                    input_summary=args.prompt[:200],
                )
            except Exception:
                pass

        spawner = HeadlessSpawner()
        result = spawner.spawn_codex(
            prompt=args.prompt,
            output_json=args.output_json,
            model=args.model,
            sandbox=args.sandbox,
            full_auto=args.full_auto,
            images=args.images,
            output_last_message=args.output_last_message,
            output_schema=args.output_schema,
            skip_git_check=args.skip_git_check,
            working_directory=args.working_directory,
            use_oss=args.use_oss,
            bypass_approvals=args.bypass_approvals,
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

        # 6. COMPLETE SANDBOX SETUP PHASE
        if tracker and sandbox_event:
            try:
                tracker.complete_phase(
                    sandbox_event["event_id"],
                    output_summary="Sandbox configured successfully",
                    status="completed",
                )
            except Exception:
                pass

        # 7. COMPLETE INITIALIZATION PHASE
        if tracker and init_event:
            try:
                tracker.complete_phase(
                    init_event["event_id"],
                    output_summary="Codex spawner initialized successfully",
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
                "agent": "gpt-4",
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
            "sandbox": args.sandbox,
            "agent": "gpt-4",
            "duration": duration,
            "delegation_event_id": delegation_event_id,
        }
        print(json.dumps(success_output))
        sys.exit(0)

    except ImportError:
        error_output = {
            "success": False,
            "error": "HtmlGraph SDK not installed. Install with: pip install htmlgraph",
            "agent": "gpt-4",
        }
        print(json.dumps(error_output), file=sys.stderr)
        sys.exit(1)
    except Exception as e:
        error_output = {
            "success": False,
            "error": f"Unexpected error: {type(e).__name__}: {e}",
            "agent": "gpt-4",
        }
        print(json.dumps(error_output), file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
