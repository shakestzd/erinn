"""
Service layer for HtmlGraph API.

Provides business logic extracted from route handlers:
- ActivityService: Activity feed, grouped events, task notifications
- OrchestrationService: Delegation chains, orchestration summaries
- AnalyticsService: Cost summaries, performance metrics
"""

import json
import logging
import time
from collections import Counter
from datetime import datetime
from typing import Any

import aiosqlite

from htmlgraph.api.cache import QueryCache

logger = logging.getLogger(__name__)


class ActivityService:
    """Service for activity feed and event grouping operations."""

    def __init__(
        self,
        db: aiosqlite.Connection,
        cache: QueryCache,
        logger: logging.Logger | None = None,
    ):
        self.db = db
        self.cache = cache
        self.logger = logger or logging.getLogger(__name__)

    async def get_grouped_events(
        self,
        limit: int = 50,
        agent_id: str | None = None,
        session_id: str | None = None,
    ) -> dict[str, Any]:
        """
        Return activity events grouped by user prompt (conversation turns).

        Each conversation turn includes:
        - userQuery: The original UserQuery event with prompt text
        - children: All child events triggered by this prompt (recursively nested)
        - stats: Aggregated statistics for the conversation turn

        Args:
            limit: Maximum number of conversation turns to return (default 50)
            agent_id: Optional filter — only return turns from this agent
            session_id: Optional filter — only return turns from this session

        Returns:
            Dictionary with conversation turns and metadata
        """
        query_start_time = time.time()

        try:
            cache_key = f"events_grouped_by_prompt:{limit}:{agent_id}:{session_id}"

            cached_result = self.cache.get(cache_key)
            if cached_result is not None:
                query_time_ms = (time.time() - query_start_time) * 1000
                self.cache.record_metric(cache_key, query_time_ms, cache_hit=True)
                self.logger.debug(
                    f"Cache HIT for events_grouped_by_prompt (key={cache_key}, time={query_time_ms:.2f}ms)"
                )
                return cached_result  # type: ignore[no-any-return]

            exec_start = time.time()

            # Step 1: Query UserQuery events (most recent first), with optional filters
            filter_clauses = ["tool_name = 'UserQuery'"]
            filter_params: list[Any] = []

            if agent_id is not None:
                filter_clauses.append(
                    "session_id IN ("
                    "SELECT session_id FROM sessions "
                    "WHERE agent_assigned LIKE '%' || ? || '%')"
                )
                filter_params.append(agent_id)

            if session_id is not None:
                filter_clauses.append("session_id = ?")
                filter_params.append(session_id)

            where_clause = " AND ".join(filter_clauses)
            user_query_sql = f"""
                SELECT
                    event_id,
                    timestamp,
                    input_summary,
                    execution_duration_seconds,
                    status,
                    agent_id,
                    session_id
                FROM agent_events
                WHERE {where_clause}
                ORDER BY timestamp DESC
                LIMIT ?
            """
            filter_params.append(limit)

            async with self.db.execute(user_query_sql, filter_params) as cursor:
                user_query_rows: list[Any] = list(await cursor.fetchall())

            conversation_turns: list[dict[str, Any]] = []

            # Children query used by recursive fetcher
            children_sql = """
                SELECT
                    event_id,
                    tool_name,
                    timestamp,
                    input_summary,
                    execution_duration_seconds,
                    status,
                    agent_id,
                    model,
                    context,
                    subagent_type,
                    feature_id,
                    session_id,
                    event_type
                FROM agent_events
                WHERE parent_event_id = ?
                ORDER BY timestamp DESC
            """

            # First-level children query (includes cross-session lookup)
            first_level_children_sql = """
                SELECT
                    event_id,
                    tool_name,
                    timestamp,
                    input_summary,
                    execution_duration_seconds,
                    status,
                    agent_id,
                    model,
                    context,
                    subagent_type,
                    feature_id,
                    session_id,
                    event_type
                FROM agent_events
                WHERE (
                    parent_event_id = ?
                    OR (
                        parent_event_id IS NULL
                        AND session_id LIKE ? || '%'
                        AND session_id != ?
                        AND tool_name != 'UserQuery'
                    )
                )
                ORDER BY timestamp DESC
            """

            # Step 2: For each UserQuery, fetch child events
            for uq_idx, uq_row in enumerate(user_query_rows):
                uq_event_id = uq_row[0]
                uq_timestamp = uq_row[1]
                uq_input = uq_row[2] or ""
                uq_duration = uq_row[3] or 0.0
                uq_status = uq_row[4]
                uq_agent_id = uq_row[5]
                uq_session_id = uq_row[6]

                prompt_text = uq_input

                # Recursive helper to fetch children at any depth
                async def fetch_children_recursive(
                    parent_id: str,
                    parent_session_id: str | None = None,
                    depth: int = 0,
                    max_depth: int = 4,
                ) -> tuple[list[dict[str, Any]], float, int, int]:
                    """Recursively fetch children up to max_depth levels."""
                    if depth >= max_depth:
                        return [], 0.0, 0, 0

                    # For first level (depth=0), use cross-session query
                    # For deeper levels, use normal parent_event_id query
                    if depth == 0 and parent_session_id:
                        async with self.db.execute(
                            first_level_children_sql,
                            [parent_id, parent_session_id, parent_session_id],
                        ) as cur:
                            rows = await cur.fetchall()
                    else:
                        async with self.db.execute(children_sql, [parent_id]) as cur:
                            rows = await cur.fetchall()

                    children_list: list[dict[str, Any]] = []
                    total_dur = 0.0
                    success_cnt = 0
                    error_cnt = 0

                    for row in rows:
                        evt_id = row[0]
                        tool = row[1]
                        timestamp = row[2]
                        input_text = row[3] or ""
                        duration = row[4] or 0.0
                        status = row[5]
                        agent = row[6] or "unknown"
                        model = row[7]
                        context_json = row[8]
                        subagent_type = row[9]
                        feature_id = row[10]
                        # evt_session_id = row[11]  # Not used currently
                        event_type = row[12] if len(row) > 12 else "tool_call"

                        # Parse context to extract spawner metadata
                        spawner_type = None
                        spawned_agent = None
                        if context_json:
                            try:
                                context = json.loads(context_json)
                                spawner_type = context.get("spawner_type")
                                spawned_agent = context.get("spawned_agent")
                            except (json.JSONDecodeError, TypeError):
                                pass

                        # If no spawner_type but subagent_type is set,
                        # treat it as a spawner delegation
                        if not spawner_type and subagent_type:
                            if ":" in subagent_type:
                                spawner_type = subagent_type.split(":")[-1]
                            else:
                                spawner_type = subagent_type
                            spawned_agent = agent

                        # Build summary
                        summary = input_text[:80] + (
                            "..." if len(input_text) > 80 else ""
                        )

                        # Recursively fetch this child's children
                        # Pass session_id only for first level to enable cross-session lookup
                        (
                            nested_children,
                            nested_dur,
                            nested_success,
                            nested_error,
                        ) = await fetch_children_recursive(
                            evt_id, None, depth + 1, max_depth
                        )

                        child_dict: dict[str, Any] = {
                            "event_id": evt_id,
                            "tool_name": tool,
                            "timestamp": timestamp,
                            "summary": summary,
                            "duration_seconds": round(duration, 2),
                            "agent": agent,
                            "depth": depth,
                            "model": model,
                            "feature_id": feature_id,
                            "status": status,
                            "event_type": event_type or "tool_call",
                        }

                        if spawner_type:
                            child_dict["spawner_type"] = spawner_type
                        if spawned_agent:
                            child_dict["spawned_agent"] = spawned_agent
                        if subagent_type:
                            child_dict["subagent_type"] = subagent_type

                        # Only add children key if there are nested children
                        if nested_children:
                            child_dict["children"] = nested_children

                        children_list.append(child_dict)

                        # Update stats (include nested)
                        total_dur += duration + nested_dur
                        if status == "recorded" or status == "success":
                            success_cnt += 1
                        else:
                            error_cnt += 1
                        success_cnt += nested_success
                        error_cnt += nested_error

                    # Ensure descending order (newest first)
                    children_list.sort(key=lambda c: c["timestamp"], reverse=True)

                    return children_list, total_dur, success_cnt, error_cnt

                # Step 3: Build child events with recursive nesting
                # Pass session_id for first level to enable cross-session lookup
                (
                    children,
                    children_duration,
                    children_success,
                    children_error,
                ) = await fetch_children_recursive(
                    uq_event_id, uq_session_id, depth=0, max_depth=4
                )

                # Step 3.1: Attach orphaned same-session events (NULL parent_event_id).
                # Race condition: PostToolUse hooks sometimes fire before the UserQuery
                # event is written, leaving ~8% of orchestrator events with no parent.
                # Strategy: for each UserQuery, find same-session events with NULL parent
                # that occurred AFTER this UserQuery and BEFORE the next UserQuery.
                # Determine the timestamp boundary for "next" UserQuery in this turn.
                # Note: user_query_rows is ordered DESC, so next chronologically is at uq_idx+1
                next_uq_timestamp: str | None = None
                if uq_idx + 1 < len(user_query_rows):
                    next_uq_timestamp = user_query_rows[uq_idx + 1][1]

                already_fetched_ids = {c["event_id"] for c in children}

                orphan_sql_with_bound = """
                    SELECT
                        event_id,
                        tool_name,
                        timestamp,
                        input_summary,
                        execution_duration_seconds,
                        status,
                        agent_id,
                        model,
                        context,
                        subagent_type,
                        feature_id
                    FROM agent_events
                    WHERE session_id = ?
                      AND (parent_event_id IS NULL OR parent_event_id = '')
                      AND tool_name NOT IN ('UserQuery', 'Stop', 'SessionStart', 'SessionEnd')
                      AND timestamp >= ?
                      AND timestamp < ?
                    ORDER BY timestamp ASC
                """
                orphan_sql_no_bound = """
                    SELECT
                        event_id,
                        tool_name,
                        timestamp,
                        input_summary,
                        execution_duration_seconds,
                        status,
                        agent_id,
                        model,
                        context,
                        subagent_type,
                        feature_id
                    FROM agent_events
                    WHERE session_id = ?
                      AND (parent_event_id IS NULL OR parent_event_id = '')
                      AND tool_name NOT IN ('UserQuery', 'Stop', 'SessionStart', 'SessionEnd')
                      AND timestamp >= ?
                    ORDER BY timestamp ASC
                """

                if next_uq_timestamp is not None:
                    async with self.db.execute(
                        orphan_sql_with_bound,
                        [uq_session_id, uq_timestamp, next_uq_timestamp],
                    ) as cur:
                        orphan_rows = await cur.fetchall()
                else:
                    async with self.db.execute(
                        orphan_sql_no_bound,
                        [uq_session_id, uq_timestamp],
                    ) as cur:
                        orphan_rows = await cur.fetchall()

                for row in orphan_rows:
                    evt_id = row[0]
                    if evt_id in already_fetched_ids:
                        continue

                    tool = row[1]
                    timestamp = row[2]
                    input_text = row[3] or ""
                    duration = row[4] or 0.0
                    status = row[5]
                    agent = row[6] or "unknown"
                    model = row[7]
                    context_json = row[8]
                    subagent_type = row[9]
                    feature_id = row[10]

                    spawner_type = None
                    spawned_agent = None
                    if context_json:
                        try:
                            context = json.loads(context_json)
                            spawner_type = context.get("spawner_type")
                            spawned_agent = context.get("spawned_agent")
                        except (json.JSONDecodeError, TypeError):
                            pass

                    if not spawner_type and subagent_type:
                        if ":" in subagent_type:
                            spawner_type = subagent_type.split(":")[-1]
                        else:
                            spawner_type = subagent_type
                        spawned_agent = agent

                    summary = input_text[:80] + ("..." if len(input_text) > 80 else "")

                    orphan_dict: dict[str, Any] = {
                        "event_id": evt_id,
                        "tool_name": tool,
                        "timestamp": timestamp,
                        "summary": summary,
                        "duration_seconds": round(duration, 2),
                        "agent": agent,
                        "depth": 0,
                        "model": model,
                        "feature_id": feature_id,
                        "status": status,
                    }
                    if spawner_type:
                        orphan_dict["spawner_type"] = spawner_type
                    if spawned_agent:
                        orphan_dict["spawned_agent"] = spawned_agent
                    if subagent_type:
                        orphan_dict["subagent_type"] = subagent_type

                    children.append(orphan_dict)
                    already_fetched_ids.add(evt_id)

                    if status in ("recorded", "success"):
                        children_success += 1
                    else:
                        children_error += 1
                    children_duration += duration

                # Step 3.5: Session-based re-parenting - nest subagent events under their Task events
                # Solution: Use session_id pattern matching to find sub-session events
                if children:
                    import re

                    # Separate Task events from other events
                    task_events = [c for c in children if c["tool_name"] == "Task"]
                    task_output_events = [
                        c for c in children if c["tool_name"] == "TaskOutput"
                    ]

                    # Track which events to remove from top level (they'll be nested)
                    events_to_nest: set[str] = set()

                    # For each Task, extract agent name and fetch sub-session events
                    for task_evt in task_events:
                        input_summary = task_evt.get("summary", "")

                        # Extract agent name from input_summary using regex: (agent-name):
                        match = re.search(r"\(([^)]+)\):", input_summary)
                        if not match:
                            continue

                        agent_name = match.group(1)
                        # Build sub-session ID
                        sub_session_id = f"{uq_session_id}-{agent_name}"

                        # Query ALL events from that sub-session
                        sub_session_query = """
                            SELECT event_id, tool_name, timestamp, input_summary,
                                   execution_duration_seconds, status, agent_id, model,
                                   context, subagent_type, feature_id, parent_event_id
                            FROM agent_events
                            WHERE session_id = ?
                            ORDER BY timestamp ASC
                        """

                        async with self.db.execute(
                            sub_session_query, [sub_session_id]
                        ) as cur:
                            sub_rows = await cur.fetchall()

                        # Build nested events from sub-session
                        subagent_events: list[dict[str, Any]] = []
                        for row in sub_rows:
                            evt_id = row[0]
                            tool = row[1]
                            timestamp = row[2]
                            input_text = row[3] or ""
                            duration = row[4] or 0.0
                            # status = row[5]  # Not used in child dict construction
                            agent = row[6] or "unknown"
                            model = row[7]
                            context_json = row[8]
                            subagent_type = row[9]
                            feature_id = row[10]
                            # parent_event_id = row[11]  # Available if needed for deeper nesting

                            # Parse context to extract spawner metadata
                            spawner_type = None
                            spawned_agent = None
                            if context_json:
                                try:
                                    context = json.loads(context_json)
                                    spawner_type = context.get("spawner_type")
                                    spawned_agent = context.get("spawned_agent")
                                except (json.JSONDecodeError, TypeError):
                                    pass

                            # If no spawner_type but subagent_type is set, treat it as spawner
                            if not spawner_type and subagent_type:
                                if ":" in subagent_type:
                                    spawner_type = subagent_type.split(":")[-1]
                                else:
                                    spawner_type = subagent_type
                                spawned_agent = agent

                            # Build summary
                            summary = input_text[:80] + (
                                "..." if len(input_text) > 80 else ""
                            )

                            child_dict: dict[str, Any] = {
                                "event_id": evt_id,
                                "tool_name": tool,
                                "timestamp": timestamp,
                                "summary": summary,
                                "duration_seconds": round(duration, 2),
                                "agent": agent,
                                "depth": 1,  # Nested under Task
                                "model": model,
                                "feature_id": feature_id,
                                "status": row[5],
                            }

                            if spawner_type:
                                child_dict["spawner_type"] = spawner_type
                            if spawned_agent:
                                child_dict["spawned_agent"] = spawned_agent
                            if subagent_type:
                                child_dict["subagent_type"] = subagent_type

                            subagent_events.append(child_dict)

                        # Nest the subagent events under this Task (newest first)
                        if subagent_events:
                            subagent_events.sort(
                                key=lambda e: e["timestamp"], reverse=True
                            )
                            task_evt["children"] = subagent_events

                        # Also find and nest matching TaskOutput under this Task
                        for output_evt in task_output_events:
                            # Match TaskOutput by checking if it's for the same agent
                            # (temporal proximity could also be used, but agent name is more reliable)
                            output_summary = output_evt.get("summary", "")
                            if agent_name in output_summary or (
                                output_evt["timestamp"] > task_evt["timestamp"]
                                and output_evt["event_id"] not in events_to_nest
                            ):
                                output_evt["depth"] = 1
                                task_evt.setdefault("children", []).append(output_evt)
                                events_to_nest.add(output_evt["event_id"])
                                break  # Only nest first matching TaskOutput

                    # Rebuild children list with only top-level events (orchestrator's direct actions + Tasks)
                    children = [
                        c for c in children if c["event_id"] not in events_to_nest
                    ]

                    # Keep descending order (newest first) for top-level events
                    children.sort(key=lambda c: c["timestamp"], reverse=True)

                total_duration = uq_duration + children_duration
                success_count = (
                    1 if uq_status == "recorded" or uq_status == "success" else 0
                ) + children_success
                error_count = (
                    0 if uq_status == "recorded" or uq_status == "success" else 1
                ) + children_error

                # Check if any child has spawner metadata
                def has_spawner_in_children(
                    children_list: list[dict[str, Any]],
                ) -> bool:
                    """Recursively check if any child has spawner metadata."""
                    for child in children_list:
                        if child.get("spawner_type") or child.get("spawned_agent"):
                            return True
                        if child.get("children") and has_spawner_in_children(
                            child["children"]
                        ):
                            return True
                    return False

                has_spawner = has_spawner_in_children(children)

                # Count total tool calls including all nested levels
                def count_total_children(
                    children_list: list[dict[str, Any]],
                ) -> int:
                    """Recursively count all children at all nesting levels."""
                    total = len(children_list)
                    for child in children_list:
                        if child.get("children"):
                            total += count_total_children(child["children"])
                    return total

                total_tool_count = count_total_children(children)

                # Step 4: Build conversation turn object
                conversation_turn = {
                    "userQuery": {
                        "event_id": uq_event_id,
                        "timestamp": uq_timestamp,
                        "prompt": prompt_text[:200],
                        "duration_seconds": round(uq_duration, 2),
                        "agent_id": uq_agent_id,
                    },
                    "children": children,
                    "has_spawner": has_spawner,
                    "stats": {
                        "tool_count": total_tool_count,
                        "total_duration": round(total_duration, 2),
                        "success_count": success_count,
                        "error_count": error_count,
                    },
                }

                conversation_turns.append(conversation_turn)

            # Step 5: Batch-fetch feature titles and annotate each turn with
            # the most common work item among its children.
            def _collect_feature_ids_recursive(
                nodes: list[dict[str, Any]], result: set[str]
            ) -> None:
                """Recursively collect feature_ids from all nested children at any depth."""
                for node in nodes:
                    if node.get("feature_id"):
                        result.add(node["feature_id"])
                    nested = node.get("children")
                    if nested:
                        _collect_feature_ids_recursive(nested, result)

            feature_ids: set[str] = set()
            for turn in conversation_turns:
                _collect_feature_ids_recursive(turn.get("children", []), feature_ids)

            features_map: dict[str, dict[str, str]] = {}
            if feature_ids:
                placeholders = ",".join("?" * len(feature_ids))
                async with self.db.execute(
                    f"SELECT id, title, type FROM features WHERE id IN ({placeholders})",
                    list(feature_ids),
                ) as feat_cur:
                    feat_rows = await feat_cur.fetchall()
                features_map = {
                    r[0]: {"title": r[1] or "", "type": r[2] or "feature"}
                    for r in feat_rows
                }

            def _collect_feature_ids_flat(
                nodes: list[dict[str, Any]],
            ) -> list[str]:
                """Recursively collect all feature_ids (with duplicates) from all depths."""
                result: list[str] = []
                stack = list(nodes)
                while stack:
                    node = stack.pop()
                    if node.get("feature_id"):
                        result.append(node["feature_id"])
                    nested = node.get("children")
                    if nested:
                        stack.extend(nested)
                return result

            for turn in conversation_turns:
                child_feature_ids = _collect_feature_ids_flat(turn.get("children", []))
                if child_feature_ids:
                    most_common_id = Counter(child_feature_ids).most_common(1)[0][0]
                    feat = features_map.get(most_common_id, {})
                    turn["work_item_id"] = most_common_id
                    turn["work_item_title"] = feat.get("title", "")
                    turn["work_item_type"] = feat.get("type", "feature")
                else:
                    turn["work_item_id"] = None
                    turn["work_item_title"] = None
                    turn["work_item_type"] = None

            exec_time_ms = (time.time() - exec_start) * 1000

            result = {
                "timestamp": datetime.now().isoformat(),
                "total_turns": len(conversation_turns),
                "conversation_turns": conversation_turns,
                "note": "Groups events by UserQuery prompt (conversation turn). Child events are linked via parent_event_id.",
            }

            self.cache.set(cache_key, result)
            query_time_ms = (time.time() - query_start_time) * 1000
            self.cache.record_metric(cache_key, exec_time_ms, cache_hit=False)
            self.logger.debug(
                f"Cache MISS for events_grouped_by_prompt (key={cache_key}, "
                f"db_time={exec_time_ms:.2f}ms, total_time={query_time_ms:.2f}ms, "
                f"turns={len(conversation_turns)})"
            )

            return result

        except Exception as e:
            self.logger.error(f"Error in get_grouped_events: {e}")
            raise

    async def get_task_notifications_linked(self, limit: int = 50) -> dict[str, Any]:
        """
        Get task notifications with links to their originating Task events.

        Returns task completion notifications from background Task() calls,
        with correlation to the original Task events when possible.

        Args:
            limit: Maximum number of notifications to return

        Returns:
            Dictionary with notifications and link metadata
        """
        query_start_time = time.time()

        try:
            cache_key = f"task_notifications_linked:{limit}"

            cached_result = self.cache.get(cache_key)
            if cached_result is not None:
                query_time_ms = (time.time() - query_start_time) * 1000
                self.cache.record_metric(cache_key, query_time_ms, cache_hit=True)
                return cached_result  # type: ignore[no-any-return]

            exec_start = time.time()

            # Query TaskOutput events (task completion notifications)
            notification_query = """
                SELECT
                    event_id,
                    agent_id,
                    timestamp,
                    input_summary,
                    output_summary,
                    status,
                    parent_event_id,
                    context
                FROM agent_events
                WHERE tool_name = 'TaskOutput'
                ORDER BY timestamp DESC
                LIMIT ?
            """

            async with self.db.execute(notification_query, [limit]) as cursor:
                rows = await cursor.fetchall()

            notifications: list[dict[str, Any]] = []
            linked_count = 0
            unlinked_count = 0

            for row in rows:
                evt_id = row[0]
                agent = row[1] or "unknown"
                timestamp = row[2]
                input_summary = row[3] or ""
                output_summary = row[4] or ""
                status = row[5]
                parent_event_id = row[6]
                context_json = row[7]

                # Parse context for link info
                link_method = None
                linked_task_id = None

                if parent_event_id:
                    linked_task_id = parent_event_id
                    link_method = "parent_event_id"
                    linked_count += 1
                elif context_json:
                    try:
                        context = json.loads(context_json)
                        linked_task_id = context.get("claude_task_id")
                        if linked_task_id:
                            link_method = "claude_task_id"
                            linked_count += 1
                        else:
                            unlinked_count += 1
                    except (json.JSONDecodeError, TypeError):
                        unlinked_count += 1
                else:
                    unlinked_count += 1

                notifications.append(
                    {
                        "event_id": evt_id,
                        "agent_id": agent,
                        "timestamp": timestamp,
                        "input_summary": input_summary,
                        "output_summary": output_summary,
                        "status": status,
                        "linked_task_id": linked_task_id,
                        "link_method": link_method,
                    }
                )

            exec_time_ms = (time.time() - exec_start) * 1000

            result = {
                "timestamp": datetime.now().isoformat(),
                "total_notifications": len(notifications),
                "linked_count": linked_count,
                "unlinked_count": unlinked_count,
                "notifications": notifications,
            }

            self.cache.set(cache_key, result)
            query_time_ms = (time.time() - query_start_time) * 1000
            self.cache.record_metric(cache_key, exec_time_ms, cache_hit=False)

            return result

        except Exception as e:
            self.logger.error(f"Error in get_task_notifications_linked: {e}")
            raise


class OrchestrationService:
    """Service for orchestration, delegation chains, and agent coordination."""

    def __init__(
        self,
        db: aiosqlite.Connection,
        cache: QueryCache,
        logger: logging.Logger | None = None,
    ):
        self.db = db
        self.cache = cache
        self.logger = logger or logging.getLogger(__name__)

    async def get_orchestration_summary(
        self,
        session_id: str | None = None,
        agent_id: str | None = None,
    ) -> dict[str, Any]:
        """
        Get orchestration summary with tool usage and model detection.

        Args:
            session_id: Optional filter by session
            agent_id: Optional filter by agent

        Returns:
            Dictionary with delegation chains and agent coordination data
        """
        query_start_time = time.time()

        try:
            cache_key = (
                f"orchestration_summary:{session_id or 'all'}:{agent_id or 'all'}"
            )

            cached_result = self.cache.get(cache_key)
            if cached_result is not None:
                query_time_ms = (time.time() - query_start_time) * 1000
                self.cache.record_metric(cache_key, query_time_ms, cache_hit=True)
                return cached_result  # type: ignore[no-any-return]

            exec_start = time.time()

            query = """
                SELECT
                    event_id,
                    agent_id as from_agent,
                    subagent_type as to_agent,
                    timestamp,
                    input_summary,
                    status
                FROM agent_events
                WHERE tool_name = 'Task'
            """
            params: list[Any] = []

            if session_id:
                query += " AND session_id = ?"
                params.append(session_id)

            if agent_id:
                query += " AND agent_id = ?"
                params.append(agent_id)

            query += " ORDER BY timestamp DESC LIMIT 1000"

            async with self.db.execute(query, params) as cursor:
                rows = await cursor.fetchall()

            delegation_chains: dict[str, list[dict[str, Any]]] = {}
            agents: set[str] = set()
            delegation_count = 0

            for row in rows:
                from_agent = row[1] or "unknown"
                to_agent = row[2]
                timestamp = row[3] or ""
                task_summary = row[4] or ""
                status = row[5] or "pending"

                if not to_agent:
                    try:
                        input_data = json.loads(task_summary) if task_summary else {}
                        to_agent = input_data.get("subagent_type", "unknown")
                    except Exception:
                        to_agent = "unknown"

                agents.add(from_agent)
                agents.add(to_agent)
                delegation_count += 1

                if from_agent not in delegation_chains:
                    delegation_chains[from_agent] = []

                delegation_chains[from_agent].append(
                    {
                        "to_agent": to_agent,
                        "event_type": "delegation",
                        "timestamp": timestamp,
                        "task": task_summary or "Unnamed task",
                        "status": status,
                    }
                )

            exec_time_ms = (time.time() - exec_start) * 1000

            result = {
                "timestamp": datetime.now().isoformat(),
                "delegation_count": delegation_count,
                "unique_agents": len(agents),
                "agents": sorted(list(agents)),
                "delegation_chains": delegation_chains,
            }

            self.cache.set(cache_key, result)
            query_time_ms = (time.time() - query_start_time) * 1000
            self.cache.record_metric(cache_key, exec_time_ms, cache_hit=False)

            return result

        except Exception as e:
            self.logger.error(f"Error in get_orchestration_summary: {e}")
            raise

    async def get_delegation_chain(self, root_event_id: str) -> dict[str, Any]:
        """
        Trace delegation chain for a specific event.

        Follows parent_event_id links to build a complete chain from root to leaf.

        Args:
            root_event_id: The event_id to trace from

        Returns:
            Dictionary with chain events and metadata
        """
        try:
            chain: list[dict[str, Any]] = []

            # Get the root event
            root_query = """
                SELECT event_id, agent_id, tool_name, timestamp, status,
                       input_summary, parent_event_id, subagent_type
                FROM agent_events
                WHERE event_id = ?
            """

            async with self.db.execute(root_query, [root_event_id]) as cursor:
                root_row = await cursor.fetchone()

            if not root_row:
                return {
                    "root_event_id": root_event_id,
                    "chain": [],
                    "depth": 0,
                    "error": "Event not found",
                }

            chain.append(
                {
                    "event_id": root_row[0],
                    "agent_id": root_row[1] or "unknown",
                    "tool_name": root_row[2],
                    "timestamp": root_row[3],
                    "status": root_row[4],
                    "input_summary": root_row[5],
                    "parent_event_id": root_row[6],
                    "subagent_type": root_row[7],
                    "depth": 0,
                }
            )

            # Recursively fetch children
            children_query = """
                SELECT event_id, agent_id, tool_name, timestamp, status,
                       input_summary, parent_event_id, subagent_type
                FROM agent_events
                WHERE parent_event_id = ?
                ORDER BY timestamp DESC
            """

            async def fetch_chain(parent_id: str, depth: int) -> None:
                if depth > 10:
                    return

                async with self.db.execute(children_query, [parent_id]) as cursor:
                    rows = await cursor.fetchall()

                for row in rows:
                    chain.append(
                        {
                            "event_id": row[0],
                            "agent_id": row[1] or "unknown",
                            "tool_name": row[2],
                            "timestamp": row[3],
                            "status": row[4],
                            "input_summary": row[5],
                            "parent_event_id": row[6],
                            "subagent_type": row[7],
                            "depth": depth,
                        }
                    )
                    await fetch_chain(row[0], depth + 1)

            await fetch_chain(root_event_id, 1)

            return {
                "root_event_id": root_event_id,
                "chain": chain,
                "depth": max(e["depth"] for e in chain) if chain else 0,
                "total_events": len(chain),
            }

        except Exception as e:
            self.logger.error(f"Error in get_delegation_chain: {e}")
            raise


class AnalyticsService:
    """Service for cost summaries and performance metrics."""

    def __init__(
        self,
        db: aiosqlite.Connection,
        cache: QueryCache,
        logger: logging.Logger | None = None,
    ):
        self.db = db
        self.cache = cache
        self.logger = logger or logging.getLogger(__name__)

    async def get_cost_summary(
        self,
        session_id: str | None = None,
        agent_id: str | None = None,
    ) -> dict[str, Any]:
        """
        Get cost summary with token aggregation and breakdown.

        Args:
            session_id: Optional filter by session
            agent_id: Optional filter by agent

        Returns:
            Dictionary with total tokens, cost breakdown by tool/model/agent
        """
        query_start_time = time.time()

        try:
            cache_key = f"cost_summary:{session_id or 'all'}:{agent_id or 'all'}"

            cached_result = self.cache.get(cache_key)
            if cached_result is not None:
                query_time_ms = (time.time() - query_start_time) * 1000
                self.cache.record_metric(cache_key, query_time_ms, cache_hit=True)
                return cached_result  # type: ignore[no-any-return]

            exec_start = time.time()

            # Total tokens and event count
            total_query = """
                SELECT
                    COUNT(*) as event_count,
                    COALESCE(SUM(cost_tokens), 0) as total_tokens
                FROM agent_events
                WHERE 1=1
            """
            params: list[Any] = []

            if session_id:
                total_query += " AND session_id = ?"
                params.append(session_id)
            if agent_id:
                total_query += " AND agent_id = ?"
                params.append(agent_id)

            async with self.db.execute(total_query, params) as cursor:
                total_row = await cursor.fetchone()

            event_count = total_row[0] if total_row else 0
            total_tokens = total_row[1] if total_row else 0

            # Breakdown by tool
            tool_query = """
                SELECT tool_name, COUNT(*) as cnt,
                       COALESCE(SUM(cost_tokens), 0) as tokens
                FROM agent_events
                WHERE tool_name IS NOT NULL
            """
            tool_params: list[Any] = []
            if session_id:
                tool_query += " AND session_id = ?"
                tool_params.append(session_id)
            if agent_id:
                tool_query += " AND agent_id = ?"
                tool_params.append(agent_id)
            tool_query += " GROUP BY tool_name ORDER BY tokens DESC LIMIT 20"

            async with self.db.execute(tool_query, tool_params) as cursor:
                tool_rows = await cursor.fetchall()

            by_tool = [
                {"tool_name": row[0], "event_count": row[1], "tokens": row[2]}
                for row in tool_rows
            ]

            # Breakdown by model
            model_query = """
                SELECT model, COUNT(*) as cnt,
                       COALESCE(SUM(cost_tokens), 0) as tokens
                FROM agent_events
                WHERE model IS NOT NULL
            """
            model_params: list[Any] = []
            if session_id:
                model_query += " AND session_id = ?"
                model_params.append(session_id)
            if agent_id:
                model_query += " AND agent_id = ?"
                model_params.append(agent_id)
            model_query += " GROUP BY model ORDER BY tokens DESC"

            async with self.db.execute(model_query, model_params) as cursor:
                model_rows = await cursor.fetchall()

            by_model = [
                {"model": row[0], "event_count": row[1], "tokens": row[2]}
                for row in model_rows
            ]

            # Breakdown by agent
            agent_query = """
                SELECT agent_id, COUNT(*) as cnt,
                       COALESCE(SUM(cost_tokens), 0) as tokens
                FROM agent_events
                WHERE agent_id IS NOT NULL
            """
            agent_params: list[Any] = []
            if session_id:
                agent_query += " AND session_id = ?"
                agent_params.append(session_id)
            if agent_id:
                agent_query += " AND agent_id = ?"
                agent_params.append(agent_id)
            agent_query += " GROUP BY agent_id ORDER BY tokens DESC"

            async with self.db.execute(agent_query, agent_params) as cursor:
                agent_rows = await cursor.fetchall()

            by_agent = [
                {"agent_id": row[0], "event_count": row[1], "tokens": row[2]}
                for row in agent_rows
            ]

            exec_time_ms = (time.time() - exec_start) * 1000

            avg_per_event = total_tokens / event_count if event_count > 0 else 0

            result: dict[str, Any] = {
                "timestamp": datetime.now().isoformat(),
                "total_tokens": total_tokens,
                "event_count": event_count,
                "avg_tokens_per_event": round(avg_per_event, 2),
                "breakdown": {
                    "by_tool": by_tool,
                    "by_model": by_model,
                    "by_agent": by_agent,
                },
            }

            self.cache.set(cache_key, result)
            query_time_ms = (time.time() - query_start_time) * 1000
            self.cache.record_metric(cache_key, exec_time_ms, cache_hit=False)

            return result

        except Exception as e:
            self.logger.error(f"Error in get_cost_summary: {e}")
            raise

    async def get_performance_metrics(
        self,
        session_id: str | None = None,
        agent_id: str | None = None,
    ) -> dict[str, Any]:
        """
        Get performance metrics (execution time, success rates).

        Args:
            session_id: Optional filter by session
            agent_id: Optional filter by agent

        Returns:
            Dictionary with duration stats, success/error rates, per-tool metrics
        """
        query_start_time = time.time()

        try:
            cache_key = f"performance_metrics:{session_id or 'all'}:{agent_id or 'all'}"

            cached_result = self.cache.get(cache_key)
            if cached_result is not None:
                query_time_ms = (time.time() - query_start_time) * 1000
                self.cache.record_metric(cache_key, query_time_ms, cache_hit=True)
                return cached_result  # type: ignore[no-any-return]

            exec_start = time.time()

            # Overall duration statistics
            duration_query = """
                SELECT
                    COUNT(*) as total_events,
                    AVG(execution_duration_seconds) as avg_duration,
                    MIN(execution_duration_seconds) as min_duration,
                    MAX(execution_duration_seconds) as max_duration,
                    SUM(CASE WHEN status IN ('recorded', 'success', 'completed') THEN 1 ELSE 0 END) as success_count,
                    SUM(CASE WHEN status IN ('error', 'failed') THEN 1 ELSE 0 END) as error_count
                FROM agent_events
                WHERE execution_duration_seconds IS NOT NULL
            """
            params: list[Any] = []

            if session_id:
                duration_query += " AND session_id = ?"
                params.append(session_id)
            if agent_id:
                duration_query += " AND agent_id = ?"
                params.append(agent_id)

            async with self.db.execute(duration_query, params) as cursor:
                dur_row = await cursor.fetchone()

            total_events = dur_row[0] if dur_row else 0
            avg_duration = dur_row[1] if dur_row else 0
            min_duration = dur_row[2] if dur_row else 0
            max_duration = dur_row[3] if dur_row else 0
            success_count = dur_row[4] if dur_row else 0
            error_count = dur_row[5] if dur_row else 0

            success_rate = (
                (success_count / total_events * 100) if total_events > 0 else 0
            )
            error_rate = (error_count / total_events * 100) if total_events > 0 else 0

            # Per-tool performance
            tool_perf_query = """
                SELECT
                    tool_name,
                    COUNT(*) as cnt,
                    AVG(execution_duration_seconds) as avg_dur,
                    MIN(execution_duration_seconds) as min_dur,
                    MAX(execution_duration_seconds) as max_dur,
                    SUM(CASE WHEN status IN ('recorded', 'success', 'completed') THEN 1 ELSE 0 END) as successes,
                    SUM(CASE WHEN status IN ('error', 'failed') THEN 1 ELSE 0 END) as errors
                FROM agent_events
                WHERE tool_name IS NOT NULL
                AND execution_duration_seconds IS NOT NULL
            """
            tool_params: list[Any] = []

            if session_id:
                tool_perf_query += " AND session_id = ?"
                tool_params.append(session_id)
            if agent_id:
                tool_perf_query += " AND agent_id = ?"
                tool_params.append(agent_id)

            tool_perf_query += " GROUP BY tool_name ORDER BY cnt DESC LIMIT 20"

            async with self.db.execute(tool_perf_query, tool_params) as cursor:
                tool_rows = await cursor.fetchall()

            per_tool = [
                {
                    "tool_name": row[0],
                    "event_count": row[1],
                    "avg_duration": round(row[2] or 0, 3),
                    "min_duration": round(row[3] or 0, 3),
                    "max_duration": round(row[4] or 0, 3),
                    "success_count": row[5] or 0,
                    "error_count": row[6] or 0,
                }
                for row in tool_rows
            ]

            exec_time_ms = (time.time() - exec_start) * 1000

            result: dict[str, Any] = {
                "timestamp": datetime.now().isoformat(),
                "total_events": total_events,
                "duration_stats": {
                    "avg": round(avg_duration or 0, 3),
                    "min": round(min_duration or 0, 3),
                    "max": round(max_duration or 0, 3),
                },
                "success_rate": round(success_rate, 2),
                "error_rate": round(error_rate, 2),
                "success_count": success_count,
                "error_count": error_count,
                "per_tool": per_tool,
            }

            self.cache.set(cache_key, result)
            query_time_ms = (time.time() - query_start_time) * 1000
            self.cache.record_metric(cache_key, exec_time_ms, cache_hit=False)

            return result

        except Exception as e:
            self.logger.error(f"Error in get_performance_metrics: {e}")
            raise
