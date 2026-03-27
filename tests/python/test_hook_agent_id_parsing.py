"""
Tests for hook agent_id/agent_type parsing and subagent detection.

Verifies that:
1. HookContext.from_input() correctly reads agent_id and agent_type from hook input
2. Both snake_case (agent_id/agent_type) and camelCase (agentId/agentType) keys are supported
3. HookContext.is_subagent property correctly identifies subagent vs main contexts
4. subagent_start.handle_subagent_start reads agent_id/agent_type correctly
5. subagent_stop.handle_subagent_stop reads agent_id from hook input for exact lookup
"""

from pathlib import Path
from unittest.mock import patch

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _make_hook_context(hook_input: dict):
    """Build a HookContext without hitting filesystem/database."""
    from htmlgraph.hooks.context import HookContext

    with (
        patch(
            "htmlgraph.hooks.bootstrap.resolve_project_dir", return_value="/tmp/proj"
        ),
        patch(
            "htmlgraph.hooks.bootstrap.get_graph_dir",
            return_value=Path("/tmp/proj/.htmlgraph"),
        ),
        patch(
            "htmlgraph.hooks.event_tracker.get_model_from_status_cache",
            return_value=None,
        ),
    ):
        return HookContext.from_input(hook_input)


# ---------------------------------------------------------------------------
# HookContext agent_id / agent_type parsing
# ---------------------------------------------------------------------------


class TestHookContextAgentParsing:
    """HookContext.from_input() parses agent_id and agent_type correctly."""

    def test_agent_id_from_snake_case(self):
        """agent_id key (snake_case) is read from hook input."""
        ctx = _make_hook_context(
            {
                "session_id": "sess-abc",
                "agent_id": "agent-deadbeef",
            }
        )
        assert ctx.agent_id == "agent-deadbeef"

    def test_agent_id_from_camel_case(self):
        """agentId key (camelCase) is read as fallback for agent_id."""
        ctx = _make_hook_context(
            {
                "session_id": "sess-abc",
                "agentId": "agent-cafebabe",
            }
        )
        assert ctx.agent_id == "agent-cafebabe"

    def test_agent_id_snake_takes_priority_over_camel(self):
        """When both keys are present snake_case wins."""
        ctx = _make_hook_context(
            {
                "session_id": "sess-abc",
                "agent_id": "agent-snake",
                "agentId": "agent-camel",
            }
        )
        assert ctx.agent_id == "agent-snake"

    def test_agent_type_from_snake_case(self):
        """agent_type key (snake_case) is read from hook input."""
        ctx = _make_hook_context(
            {
                "session_id": "sess-abc",
                "agent_id": "",
                "agent_type": "htmlgraph:sonnet-coder",
            }
        )
        assert ctx.agent_type == "htmlgraph:sonnet-coder"

    def test_agent_type_from_camel_case(self):
        """agentType key (camelCase) is read as fallback for agent_type."""
        ctx = _make_hook_context(
            {
                "session_id": "sess-abc",
                "agentType": "general-purpose",
            }
        )
        assert ctx.agent_type == "general-purpose"

    def test_agent_type_snake_takes_priority_over_camel(self):
        """When both agent_type keys are present snake_case wins."""
        ctx = _make_hook_context(
            {
                "session_id": "sess-abc",
                "agent_type": "snake-type",
                "agentType": "camel-type",
            }
        )
        assert ctx.agent_type == "snake-type"

    def test_agent_type_none_when_absent(self):
        """agent_type is None when not in hook input."""
        ctx = _make_hook_context({"session_id": "sess-abc"})
        assert ctx.agent_type is None

    def test_agent_id_falls_back_to_env_when_missing(self):
        """When agent_id not in hook input, environment variable is used."""
        with patch.dict(
            "os.environ", {"HTMLGRAPH_AGENT_ID": "env-agent-id"}, clear=False
        ):
            ctx = _make_hook_context({"session_id": "sess-abc"})
        assert ctx.agent_id == "env-agent-id"


# ---------------------------------------------------------------------------
# HookContext.is_subagent property
# ---------------------------------------------------------------------------


class TestHookContextIsSubagent:
    """HookContext.is_subagent correctly identifies subagent vs main contexts."""

    def test_subagent_when_agent_id_is_subagent_uuid(self):
        """is_subagent=True when agent_id looks like a subagent UUID."""
        ctx = _make_hook_context(
            {"session_id": "sess-abc", "agent_id": "agent-deadbeef"}
        )
        assert ctx.is_subagent is True

    def test_not_subagent_when_agent_id_is_main(self):
        """is_subagent=False when agent_id is the 'main' sentinel."""
        ctx = _make_hook_context({"session_id": "sess-abc", "agent_id": "main"})
        assert ctx.is_subagent is False

    def test_not_subagent_when_agent_id_is_claude_code(self):
        """is_subagent=False when agent_id is 'claude-code'."""
        ctx = _make_hook_context({"session_id": "sess-abc", "agent_id": "claude-code"})
        assert ctx.is_subagent is False

    def test_not_subagent_when_agent_id_is_empty(self):
        """is_subagent=False when agent_id is empty string."""
        ctx = _make_hook_context({"session_id": "sess-abc", "agent_id": ""})
        assert ctx.is_subagent is False

    def test_subagent_when_agent_id_absent_but_agent_type_set(self):
        """is_subagent=True when agent_id absent but agent_type is a non-main type."""
        ctx = _make_hook_context(
            {
                "session_id": "sess-abc",
                "agent_type": "htmlgraph:sonnet-coder",
            }
        )
        assert ctx.is_subagent is True

    def test_not_subagent_when_agent_type_is_main(self):
        """is_subagent=False when agent_id is empty and agent_type is 'main'."""
        ctx = _make_hook_context(
            {"session_id": "sess-abc", "agent_id": "", "agent_type": "main"}
        )
        assert ctx.is_subagent is False

    def test_not_subagent_when_both_absent(self):
        """is_subagent=False when neither agent_id nor agent_type are in hook input."""
        # agent_id will default to 'unknown' from env/CLAUDE_AGENT_NICKNAME
        with patch.dict(
            "os.environ",
            {"CLAUDE_AGENT_NICKNAME": "unknown"},
            clear=False,
        ):
            ctx = _make_hook_context({"session_id": "sess-abc"})
        assert ctx.is_subagent is False


# ---------------------------------------------------------------------------
# subagent_start reads agent_id / agent_type from hook input
# ---------------------------------------------------------------------------


class TestSubagentStartAgentIdParsing:
    """handle_subagent_start correctly reads agent_id and agent_type."""

    def test_no_agent_id_returns_continue(self):
        """When hook_input has no agent_id, handler returns continue=True gracefully."""
        from htmlgraph.hooks.subagent_start import handle_subagent_start

        result = handle_subagent_start({})
        assert result == {"continue": True}

    def test_agent_id_used_for_db_update(self, tmp_path):
        """When agent_id present, handler queries DB and maps it to task_delegation."""
        import sqlite3

        from htmlgraph.hooks.subagent_start import handle_subagent_start

        db_path = str(tmp_path / "test.db")
        conn = sqlite3.connect(db_path)
        conn.execute("""
            CREATE TABLE agent_events (
                event_id TEXT PRIMARY KEY,
                session_id TEXT,
                event_type TEXT,
                agent_id TEXT,
                subagent_type TEXT,
                status TEXT,
                timestamp TEXT,
                updated_at TEXT
            )
        """)
        # Insert a started task_delegation with empty agent_id (unmatched)
        conn.execute(
            """
            INSERT INTO agent_events
            (event_id, session_id, event_type, agent_id, subagent_type, status, timestamp)
            VALUES (?, ?, ?, ?, ?, ?, ?)
            """,
            (
                "evt-test001",
                "sess-parent",
                "task_delegation",
                "",
                "general-purpose",
                "started",
                "2025-01-01T00:00:00",
            ),
        )
        conn.commit()
        conn.close()

        hook_input = {
            "agent_id": "agent-cafebabe",
            "agent_type": "general-purpose",
            "session_id": "sess-parent",
        }

        with patch("htmlgraph.config.get_database_path", return_value=db_path):
            result = handle_subagent_start(hook_input)

        assert result == {"continue": True}

        # Verify agent_id was written to the task_delegation row
        conn = sqlite3.connect(db_path)
        row = conn.execute(
            "SELECT agent_id FROM agent_events WHERE event_id = 'evt-test001'"
        ).fetchone()
        conn.close()
        assert row is not None
        assert row[0] == "agent-cafebabe"


# ---------------------------------------------------------------------------
# subagent_stop reads agent_id from hook input for exact parent lookup
# ---------------------------------------------------------------------------


class TestSubagentStopAgentIdParsing:
    """handle_subagent_stop correctly reads agent_id from hook input."""

    def test_agent_id_used_for_exact_parent_lookup(self, tmp_path):
        """When agent_id present, stop handler queries DB by agent_id for exact match."""
        import sqlite3
        from datetime import datetime, timezone

        from htmlgraph.hooks.subagent_stop import get_parent_event_from_db

        db_path = str(tmp_path / "test.db")
        conn = sqlite3.connect(db_path)
        conn.execute("""
            CREATE TABLE agent_events (
                event_id TEXT PRIMARY KEY,
                session_id TEXT,
                event_type TEXT,
                agent_id TEXT,
                status TEXT,
                timestamp TEXT
            )
        """)
        ts = datetime.now(timezone.utc).isoformat()
        # Insert two task_delegations; only one has the correct agent_id
        conn.executemany(
            """
            INSERT INTO agent_events (event_id, session_id, event_type, agent_id, status, timestamp)
            VALUES (?, ?, ?, ?, ?, ?)
            """,
            [
                (
                    "evt-wrong",
                    "sess-p",
                    "task_delegation",
                    "agent-wrong",
                    "started",
                    ts,
                ),
                (
                    "evt-right",
                    "sess-p",
                    "task_delegation",
                    "agent-cafebabe",
                    "started",
                    ts,
                ),
            ],
        )
        conn.commit()
        conn.close()

        result = get_parent_event_from_db(
            db_path, agent_id="agent-cafebabe", session_id="sess-p"
        )
        assert result == "evt-right"

    def test_fallback_heuristic_when_no_agent_id(self, tmp_path):
        """When agent_id absent, stop handler falls back to most-recent task_delegation."""
        import sqlite3

        from htmlgraph.hooks.subagent_stop import get_parent_event_from_db

        db_path = str(tmp_path / "test.db")
        conn = sqlite3.connect(db_path)
        conn.execute("""
            CREATE TABLE agent_events (
                event_id TEXT PRIMARY KEY,
                session_id TEXT,
                event_type TEXT,
                agent_id TEXT,
                status TEXT,
                timestamp TEXT
            )
        """)
        ts_old = "2025-01-01T00:00:00"
        ts_new = "2025-01-01T00:01:00"
        conn.executemany(
            """
            INSERT INTO agent_events (event_id, session_id, event_type, agent_id, status, timestamp)
            VALUES (?, ?, ?, ?, ?, ?)
            """,
            [
                ("evt-old", "sess-p", "task_delegation", "", "started", ts_old),
                ("evt-new", "sess-p", "task_delegation", "", "started", ts_new),
            ],
        )
        conn.commit()
        conn.close()

        # No agent_id → falls back to most-recent (DESC order)
        result = get_parent_event_from_db(db_path, agent_id=None, session_id="sess-p")
        assert result == "evt-new"
