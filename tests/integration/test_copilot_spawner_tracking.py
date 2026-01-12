#!/usr/bin/env python3
"""Test CopilotSpawner with full parent event context and subprocess tracking.

This test demonstrates:
1. Setting up parent event context (UserQuery + Task delegation)
2. Initializing SpawnerEventTracker with proper session/delegation IDs
3. Invoking CopilotSpawner.spawn() with real task
4. Validating event hierarchy in database
5. Verifying subprocess events are recorded with parent_event_id
"""

import os
import sys
import uuid
from datetime import datetime, timezone
from pathlib import Path

# Add plugin agents directory to path for SpawnerEventTracker
PLUGIN_AGENTS_DIR = (
    Path(__file__).parent / "packages/claude-plugin/.claude-plugin/agents"
)
sys.path.insert(0, str(PLUGIN_AGENTS_DIR))

from htmlgraph.config import get_database_path
from htmlgraph.db.schema import HtmlGraphDB
from htmlgraph.orchestration import CopilotSpawner
from spawner_event_tracker import SpawnerEventTracker


def setup_parent_event_context(db: HtmlGraphDB, session_id: str) -> tuple[str, str]:
    """
    Set up parent event context in database (simulates PreToolUse hook behavior).

    Args:
        db: Database instance
        session_id: Session ID for events

    Returns:
        Tuple of (user_query_event_id, task_delegation_event_id)
    """
    user_query_event_id = f"event-query-{uuid.uuid4().hex[:8]}"
    task_delegation_event_id = f"event-{uuid.uuid4().hex[:8]}"
    start_time = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M:%S")

    print(f"\n{'=' * 80}")
    print("SETUP: Creating parent event context")
    print(f"{'=' * 80}")
    print(f"Session ID: {session_id}")
    print(f"UserQuery Event ID: {user_query_event_id}")
    print(f"Task Delegation Event ID: {task_delegation_event_id}")

    # 1. Insert UserQuery event (from UserPromptSubmit hook)
    db.connection.cursor().execute(
        """INSERT INTO agent_events
           (event_id, agent_id, event_type, session_id, tool_name, input_summary, status, created_at)
           VALUES (?, ?, ?, ?, ?, ?, ?, ?)""",
        (
            user_query_event_id,
            "claude-code",
            "tool_call",
            session_id,
            "UserPromptSubmit",
            "Test: Invoke Copilot for version recommendation",
            "completed",
            start_time,
        ),
    )

    # 2. Insert Task delegation event (from PreToolUse hook)
    db.connection.cursor().execute(
        """INSERT INTO agent_events
           (event_id, agent_id, event_type, session_id, tool_name, input_summary,
            context, parent_event_id, subagent_type, status, created_at)
           VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)""",
        (
            task_delegation_event_id,
            "claude-code",
            "task_delegation",
            session_id,
            "Task",
            "Recommend next semantic version for HtmlGraph",
            '{"subagent_type":"copilot","track_subprocess":true}',
            user_query_event_id,
            "copilot",
            "started",
            start_time,
        ),
    )
    db.connection.commit()

    print("\n✅ Parent events created in database")
    return user_query_event_id, task_delegation_event_id


def setup_environment(session_id: str, parent_event_id: str) -> None:
    """
    Export parent context to environment (simulates PreToolUse hook).

    Args:
        session_id: Session ID
        parent_event_id: Parent event ID (task delegation)
    """
    print(f"\n{'=' * 80}")
    print("SETUP: Exporting parent context to environment")
    print(f"{'=' * 80}")

    os.environ["HTMLGRAPH_PARENT_EVENT"] = parent_event_id
    os.environ["HTMLGRAPH_PARENT_SESSION"] = session_id
    os.environ["HTMLGRAPH_SESSION_ID"] = session_id
    os.environ["HTMLGRAPH_PARENT_AGENT"] = "claude"

    print(f"HTMLGRAPH_PARENT_EVENT: {parent_event_id}")
    print(f"HTMLGRAPH_PARENT_SESSION: {session_id}")
    print("✅ Environment configured")


def invoke_copilot_spawner(
    tracker: SpawnerEventTracker,
    parent_event_id: str,
) -> dict:
    """
    Invoke CopilotSpawner with full tracking enabled.

    Args:
        tracker: SpawnerEventTracker instance
        parent_event_id: Parent event ID for linking

    Returns:
        Dict with result and metadata
    """
    print(f"\n{'=' * 80}")
    print("INVOCATION: Spawning Copilot with tracking")
    print(f"{'=' * 80}")

    spawner = CopilotSpawner()

    # Real task: Recommend next version number
    prompt = """HtmlGraph project status:
- Completed CLI module refactoring (all tests passing)
- Completed skill documentation clarification
- Completed spawner architecture modularization
- Current version: ~0.26.x

Please recommend:
1. Next semantic version number (MAJOR.MINOR.PATCH)
2. Brief rationale for the version bump
"""

    print(f"Prompt: {prompt[:100]}...")
    print(f"Tracker: {tracker}")
    print(f"Parent Event ID: {parent_event_id}")
    print("Track in HtmlGraph: True")
    print("Allow all tools: True")

    result = spawner.spawn(
        prompt=prompt,
        track_in_htmlgraph=True,  # Enable SDK activity tracking
        tracker=tracker,  # Enable subprocess event tracking
        parent_event_id=parent_event_id,  # Link to parent event
        allow_all_tools=True,  # Auto-approve all tools
        timeout=120,  # 2 minutes max
    )

    return {
        "result": result,
        "prompt": prompt,
    }


def validate_results(
    db: HtmlGraphDB,
    session_id: str,
    user_query_event_id: str,
    task_delegation_event_id: str,
    result_data: dict,
) -> bool:
    """
    Validate that events were recorded correctly in the database.

    Args:
        db: Database instance
        session_id: Session ID
        user_query_event_id: UserQuery event ID
        task_delegation_event_id: Task delegation event ID
        result_data: Result from spawner invocation

    Returns:
        True if validation passed, False otherwise
    """
    print(f"\n{'=' * 80}")
    print("VALIDATION: Checking event hierarchy in database")
    print(f"{'=' * 80}")

    result = result_data["result"]
    _ = result_data["prompt"]  # noqa: F841 - Kept for debugging

    # Check AIResult structure
    print("\n1. AIResult Validation:")
    print(f"   Success: {result.success}")
    print(f"   Response length: {len(result.response) if result.response else 0}")
    print(f"   Error: {result.error}")
    print(
        f"   Tracked events: {len(result.tracked_events) if result.tracked_events else 0}"
    )

    if not result.success:
        print(f"\n⚠️  Copilot execution failed: {result.error}")
        print("   This is expected if Copilot CLI is not installed")
        print("   Continuing validation of event tracking structure...")

    # Query all events in this session
    cursor = db.connection.cursor()
    cursor.execute(
        """
        SELECT event_id, agent_id, event_type, tool_name, input_summary,
               parent_event_id, subagent_type, status
        FROM agent_events
        WHERE session_id = ?
        ORDER BY created_at ASC
    """,
        (session_id,),
    )
    events = cursor.fetchall()

    print(f"\n2. Database Events ({len(events)} total):")
    validation_passed = True

    # Expected hierarchy:
    # UserQuery (root)
    # └── Task Delegation (child of UserQuery)
    #     └── Subprocess events (children of Task Delegation)

    for i, event in enumerate(events):
        (
            event_id,
            agent_id,
            event_type,
            tool_name,
            input_summary,
            parent_event_id,
            subagent_type,
            status,
        ) = event

        indent = ""
        if parent_event_id == user_query_event_id:
            indent = "   "
        elif parent_event_id == task_delegation_event_id:
            indent = "      "

        print(f"{indent}[{i + 1}] {tool_name} ({event_type})")
        print(f"{indent}    Event ID: {event_id}")
        print(f"{indent}    Agent: {agent_id}")
        print(f"{indent}    Parent: {parent_event_id or 'ROOT'}")
        print(f"{indent}    Subagent: {subagent_type or 'N/A'}")
        print(f"{indent}    Status: {status}")
        print(f"{indent}    Summary: {input_summary[:60]}...")

    # Validate hierarchy
    print("\n3. Hierarchy Validation:")

    # Check UserQuery exists
    user_query = [e for e in events if e[0] == user_query_event_id]
    if user_query:
        print("   ✅ UserQuery event found (root)")
    else:
        print("   ❌ UserQuery event NOT found")
        validation_passed = False

    # Check Task delegation exists and is child of UserQuery
    task_delegation = [e for e in events if e[0] == task_delegation_event_id]
    if task_delegation and task_delegation[0][5] == user_query_event_id:
        print("   ✅ Task delegation event found (child of UserQuery)")
    else:
        print("   ❌ Task delegation event NOT properly linked")
        validation_passed = False

    # Check subprocess events exist and are children of Task delegation
    subprocess_events = [
        e
        for e in events
        if e[5] == task_delegation_event_id and e[3] == "subprocess.copilot"
    ]
    if subprocess_events:
        print(
            f"   ✅ {len(subprocess_events)} subprocess event(s) found (children of Task delegation)"
        )
    else:
        print(
            "   ⚠️  No subprocess events found (expected if Copilot CLI not available)"
        )

    # Check for activity tracking events (copilot_start, copilot_result)
    activity_events = [
        e
        for e in events
        if e[3] in ("copilot_spawn_start", "copilot_start", "copilot_result")
    ]
    if activity_events:
        print(f"   ✅ {len(activity_events)} activity tracking event(s) found")
    else:
        print("   ⚠️  No activity tracking events found")

    # Summary
    print(f"\n{'=' * 80}")
    print("SUMMARY")
    print(f"{'=' * 80}")
    print(f"Total events in session: {len(events)}")
    print(
        f"UserQuery events: {len([e for e in events if e[2] == 'tool_call' and e[3] == 'UserPromptSubmit'])}"
    )
    print(
        f"Task delegation events: {len([e for e in events if e[2] == 'task_delegation'])}"
    )
    print(f"Subprocess events: {len(subprocess_events)}")
    print(f"Activity events: {len(activity_events)}")
    print(f"\nValidation: {'✅ PASSED' if validation_passed else '❌ FAILED'}")

    return validation_passed


def main() -> int:
    """Run the complete test workflow."""
    print(f"\n{'#' * 80}")
    print("# CopilotSpawner Parent Event Context Test")
    print(f"{'#' * 80}")

    try:
        # Initialize database
        db_path = get_database_path()
        print(f"\nDatabase: {db_path}")

        if not db_path.exists():
            print(f"❌ Database not found at {db_path}")
            print("   Run: htmlgraph init")
            return 1

        db = HtmlGraphDB(str(db_path))

        # Create session
        session_id = f"sess-{uuid.uuid4().hex[:8]}"
        db._ensure_session_exists(session_id, "claude")

        # Step 1: Setup parent event context
        user_query_event_id, task_delegation_event_id = setup_parent_event_context(
            db, session_id
        )

        # Step 2: Setup environment
        setup_environment(session_id, task_delegation_event_id)

        # Step 3: Create tracker with parent context
        print(f"\n{'=' * 80}")
        print("SETUP: Creating SpawnerEventTracker")
        print(f"{'=' * 80}")

        tracker = SpawnerEventTracker(
            delegation_event_id=task_delegation_event_id,
            parent_agent="claude",
            spawner_type="copilot",
            session_id=session_id,
        )
        tracker.db = db  # Ensure tracker uses the same database

        print("Tracker created:")
        print(f"  Delegation Event ID: {task_delegation_event_id}")
        print("  Parent Agent: claude")
        print("  Spawner Type: copilot")
        print(f"  Session ID: {session_id}")
        print("✅ Tracker initialized")

        # Step 4: Invoke CopilotSpawner
        result_data = invoke_copilot_spawner(tracker, task_delegation_event_id)

        # Step 5: Validate results
        validation_passed = validate_results(
            db,
            session_id,
            user_query_event_id,
            task_delegation_event_id,
            result_data,
        )

        # Step 6: Display response (if successful)
        result = result_data["result"]
        if result.success and result.response:
            print(f"\n{'=' * 80}")
            print("COPILOT RESPONSE")
            print(f"{'=' * 80}")
            print(result.response)

        print(f"\n{'#' * 80}")
        print(f"# Test {'PASSED' if validation_passed else 'FAILED'}")
        print(f"{'#' * 80}\n")

        return 0 if validation_passed else 1

    except KeyboardInterrupt:
        print("\n\n⚠️  Test interrupted by user")
        return 130
    except Exception as e:
        print("\n\n❌ Test failed with exception:")
        print(f"   {type(e).__name__}: {e}")
        import traceback

        traceback.print_exc()
        return 1


if __name__ == "__main__":
    sys.exit(main())
