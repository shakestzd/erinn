"""
Tests for HookContext module.

Tests the hook execution context manager including:
- Context initialization from raw hook input
- Lazy-loading of expensive resources (SessionManager, HtmlGraphDB)
- Resource cleanup and context manager protocol
- Environment-based agent detection
- Logging functionality
"""

import os
from unittest import mock

import pytest
from htmlgraph.hooks.context import HookContext


class TestHookContextInitialization:
    """Test HookContext initialization and basic properties."""

    def test_direct_initialization(self, tmp_path):
        """Test direct instantiation of HookContext."""
        graph_dir = tmp_path / ".htmlgraph"
        graph_dir.mkdir()

        context = HookContext(
            project_dir=str(tmp_path),
            graph_dir=graph_dir,
            session_id="test-session-123",
            agent_id="claude-code",
            hook_input={"type": "pretooluse"},
        )

        assert context.project_dir == str(tmp_path)
        assert context.graph_dir == graph_dir
        assert context.session_id == "test-session-123"
        assert context.agent_id == "claude-code"
        assert context.hook_input == {"type": "pretooluse"}

    def test_from_input_with_minimal_input(self, tmp_path):
        """Test HookContext.from_input with minimal hook input falls back to unknown.

        Note: We intentionally do NOT use SessionManager fallback because it's global
        and would cause cross-window event contamination in multi-window scenarios.
        """
        with mock.patch(
            "htmlgraph.hooks.bootstrap.resolve_project_dir",
            return_value=str(tmp_path),
        ):
            with mock.patch(
                "htmlgraph.hooks.bootstrap.get_graph_dir",
                return_value=tmp_path / ".htmlgraph",
            ):
                # Clear environment variables that could provide session_id or agent_id
                # Use empty dict to simulate clean environment
                clean_env = {
                    k: v
                    for k, v in os.environ.items()
                    if k
                    not in (
                        "HTMLGRAPH_SESSION_ID",
                        "CLAUDE_SESSION_ID",
                        "HTMLGRAPH_AGENT_ID",
                        "CLAUDE_AGENT_NICKNAME",
                    )
                }

                with mock.patch.dict(os.environ, clean_env, clear=True):
                    hook_input = {}
                    context = HookContext.from_input(hook_input)

                    assert context.project_dir == str(tmp_path)
                    # Without session_id in hook_input or env, defaults to "unknown"
                    # This is intentional - better than cross-window contamination
                    assert context.session_id == "unknown"
                    assert context.agent_id == "claude-code"

    def test_from_input_with_session_id(self, tmp_path):
        """Test HookContext.from_input extracts session_id from hook input."""
        with mock.patch(
            "htmlgraph.hooks.bootstrap.resolve_project_dir",
            return_value=str(tmp_path),
        ):
            with mock.patch(
                "htmlgraph.hooks.bootstrap.get_graph_dir",
                return_value=tmp_path / ".htmlgraph",
            ):
                hook_input = {"session_id": "sess-abc123"}
                context = HookContext.from_input(hook_input)

                assert context.session_id == "sess-abc123"

    def test_from_input_with_agent_id_in_hook(self, tmp_path):
        """Test HookContext.from_input uses agent_id from hook input."""
        with mock.patch(
            "htmlgraph.hooks.bootstrap.resolve_project_dir",
            return_value=str(tmp_path),
        ):
            with mock.patch(
                "htmlgraph.hooks.bootstrap.get_graph_dir",
                return_value=tmp_path / ".htmlgraph",
            ):
                hook_input = {"agent_id": "codex-v2"}
                context = HookContext.from_input(hook_input)

                assert context.agent_id == "codex-v2"

    def test_from_input_agent_detection_priority(self, tmp_path):
        """Test agent_id detection priority: input > env > default."""
        with mock.patch(
            "htmlgraph.hooks.bootstrap.resolve_project_dir",
            return_value=str(tmp_path),
        ):
            with mock.patch(
                "htmlgraph.hooks.bootstrap.get_graph_dir",
                return_value=tmp_path / ".htmlgraph",
            ):
                # Priority 1: Hook input takes precedence
                hook_input = {"agent_id": "from-hook"}
                context = HookContext.from_input(hook_input)
                assert context.agent_id == "from-hook"

                # Priority 2: Environment variable
                with mock.patch.dict(os.environ, {"HTMLGRAPH_AGENT_ID": "from-env"}):
                    hook_input = {}
                    context = HookContext.from_input(hook_input)
                    assert context.agent_id == "from-env"

                # Priority 3: Claude Code environment variable
                env = {"CLAUDE_AGENT_NICKNAME": "claude-code"}
                with mock.patch.dict(os.environ, env, clear=True):
                    hook_input = {}
                    context = HookContext.from_input(hook_input)
                    assert context.agent_id == "claude-code"


class TestLazyLoading:
    """Test lazy-loading of expensive resources."""

    def test_session_manager_not_loaded_on_init(self, tmp_path):
        """SessionManager should not be loaded until first access."""
        graph_dir = tmp_path / ".htmlgraph"
        graph_dir.mkdir()

        context = HookContext(
            project_dir=str(tmp_path),
            graph_dir=graph_dir,
            session_id="test-123",
            agent_id="test",
            hook_input={},
        )

        # Should be None until accessed
        assert context._session_manager is None

    def test_database_not_loaded_on_init(self, tmp_path):
        """HtmlGraphDB should not be loaded until first access."""
        graph_dir = tmp_path / ".htmlgraph"
        graph_dir.mkdir()

        context = HookContext(
            project_dir=str(tmp_path),
            graph_dir=graph_dir,
            session_id="test-123",
            agent_id="test",
            hook_input={},
        )

        # Should be None until accessed
        assert context._database is None

    @mock.patch("htmlgraph.session_manager.SessionManager")
    def test_session_manager_lazy_loading(self, mock_sm_class, tmp_path):
        """SessionManager should be loaded on first property access."""
        mock_sm_instance = mock.MagicMock()
        mock_sm_class.return_value = mock_sm_instance

        graph_dir = tmp_path / ".htmlgraph"
        graph_dir.mkdir()

        context = HookContext(
            project_dir=str(tmp_path),
            graph_dir=graph_dir,
            session_id="test-123",
            agent_id="test",
            hook_input={},
        )

        # Access property - should load
        sm = context.session_manager
        assert sm is mock_sm_instance
        mock_sm_class.assert_called_once_with(graph_dir=graph_dir)

        # Second access - should return cached instance
        sm2 = context.session_manager
        assert sm2 is sm
        assert mock_sm_class.call_count == 1  # Only called once

    @mock.patch("htmlgraph.db.schema.HtmlGraphDB")
    def test_database_lazy_loading(self, mock_db_class, tmp_path):
        """HtmlGraphDB should be loaded on first property access."""
        mock_db_instance = mock.MagicMock()
        mock_db_class.return_value = mock_db_instance

        graph_dir = tmp_path / ".htmlgraph"
        graph_dir.mkdir()

        context = HookContext(
            project_dir=str(tmp_path),
            graph_dir=graph_dir,
            session_id="test-123",
            agent_id="test",
            hook_input={},
        )

        # Access property - should load
        db = context.database
        assert db is mock_db_instance
        expected_db_path = str(graph_dir / "htmlgraph.db")
        mock_db_class.assert_called_once_with(expected_db_path)

        # Second access - should return cached instance
        db2 = context.database
        assert db2 is db
        assert mock_db_class.call_count == 1  # Only called once

    @mock.patch("htmlgraph.session_manager.SessionManager")
    def test_session_manager_import_error(self, mock_sm_class, tmp_path):
        """SessionManager import failure should raise ImportError."""
        mock_sm_class.side_effect = ImportError("Module not found")

        graph_dir = tmp_path / ".htmlgraph"
        graph_dir.mkdir()

        context = HookContext(
            project_dir=str(tmp_path),
            graph_dir=graph_dir,
            session_id="test-123",
            agent_id="test",
            hook_input={},
        )

        with pytest.raises(ImportError):
            _ = context.session_manager

    @mock.patch("htmlgraph.db.schema.HtmlGraphDB")
    def test_database_initialization_error(self, mock_db_class, tmp_path):
        """Database initialization failure should raise Exception."""
        mock_db_class.side_effect = Exception("Database connection failed")

        graph_dir = tmp_path / ".htmlgraph"
        graph_dir.mkdir()

        context = HookContext(
            project_dir=str(tmp_path),
            graph_dir=graph_dir,
            session_id="test-123",
            agent_id="test",
            hook_input={},
        )

        with pytest.raises(Exception):
            _ = context.database


class TestResourceCleanup:
    """Test resource cleanup and context manager protocol."""

    @mock.patch("htmlgraph.db.schema.HtmlGraphDB")
    def test_close_database(self, mock_db_class, tmp_path):
        """Test that close() closes database connection."""
        mock_db_instance = mock.MagicMock()
        mock_db_class.return_value = mock_db_instance

        graph_dir = tmp_path / ".htmlgraph"
        graph_dir.mkdir()

        context = HookContext(
            project_dir=str(tmp_path),
            graph_dir=graph_dir,
            session_id="test-123",
            agent_id="test",
            hook_input={},
        )

        # Load database
        _ = context.database

        # Close context
        context.close()

        # Database should be closed
        mock_db_instance.close.assert_called_once()
        assert context._database is None

    def test_close_idempotent(self, tmp_path):
        """Test that close() can be called multiple times safely."""
        graph_dir = tmp_path / ".htmlgraph"
        graph_dir.mkdir()

        context = HookContext(
            project_dir=str(tmp_path),
            graph_dir=graph_dir,
            session_id="test-123",
            agent_id="test",
            hook_input={},
        )

        # Should not raise error
        context.close()
        context.close()
        context.close()

    @mock.patch("htmlgraph.db.schema.HtmlGraphDB")
    def test_context_manager_protocol(self, mock_db_class, tmp_path):
        """Test HookContext works as context manager."""
        mock_db_instance = mock.MagicMock()
        mock_db_class.return_value = mock_db_instance

        graph_dir = tmp_path / ".htmlgraph"
        graph_dir.mkdir()

        with HookContext(
            project_dir=str(tmp_path),
            graph_dir=graph_dir,
            session_id="test-123",
            agent_id="test",
            hook_input={},
        ) as context:
            # Load database inside context
            _ = context.database

        # Should be cleaned up after exiting context
        mock_db_instance.close.assert_called_once()

    @mock.patch("htmlgraph.db.schema.HtmlGraphDB")
    def test_context_manager_exception_handling(self, mock_db_class, tmp_path):
        """Test that context manager cleans up even on exception."""
        mock_db_instance = mock.MagicMock()
        mock_db_class.return_value = mock_db_instance

        graph_dir = tmp_path / ".htmlgraph"
        graph_dir.mkdir()

        try:
            with HookContext(
                project_dir=str(tmp_path),
                graph_dir=graph_dir,
                session_id="test-123",
                agent_id="test",
                hook_input={},
            ) as context:
                _ = context.database
                raise ValueError("Test exception")
        except ValueError:
            pass

        # Should still be cleaned up
        mock_db_instance.close.assert_called_once()


class TestLogging:
    """Test unified logging functionality."""

    def test_log_info(self, tmp_path, caplog):
        """Test info-level logging."""
        graph_dir = tmp_path / ".htmlgraph"
        graph_dir.mkdir()

        context = HookContext(
            project_dir=str(tmp_path),
            graph_dir=graph_dir,
            session_id="sess-abc",
            agent_id="claude",
            hook_input={},
        )

        with caplog.at_level("INFO"):
            context.log("info", "Test message")

        assert "Test message" in caplog.text
        assert "sess-abc" in caplog.text or "sess" in caplog.text

    def test_log_error(self, tmp_path, caplog):
        """Test error-level logging."""
        graph_dir = tmp_path / ".htmlgraph"
        graph_dir.mkdir()

        context = HookContext(
            project_dir=str(tmp_path),
            graph_dir=graph_dir,
            session_id="sess-def",
            agent_id="codex",
            hook_input={},
        )

        with caplog.at_level("ERROR"):
            context.log("error", "Error occurred")

        assert "Error occurred" in caplog.text

    def test_log_case_insensitive(self, tmp_path, caplog):
        """Test that log level is case-insensitive."""
        graph_dir = tmp_path / ".htmlgraph"
        graph_dir.mkdir()

        context = HookContext(
            project_dir=str(tmp_path),
            graph_dir=graph_dir,
            session_id="test",
            agent_id="test",
            hook_input={},
        )

        with caplog.at_level("DEBUG"):
            context.log("DEBUG", "Uppercase level")
            context.log("Debug", "Mixed case level")
            context.log("debug", "Lowercase level")

        assert "Uppercase level" in caplog.text
        assert "Mixed case level" in caplog.text
        assert "Lowercase level" in caplog.text


class TestDataclassProperties:
    """Test HookContext dataclass properties."""

    def test_repr_excludes_internal_fields(self, tmp_path):
        """Test that __repr__ excludes internal _session_manager and _database."""
        graph_dir = tmp_path / ".htmlgraph"
        graph_dir.mkdir()

        context = HookContext(
            project_dir=str(tmp_path),
            graph_dir=graph_dir,
            session_id="test-123",
            agent_id="test",
            hook_input={"key": "value"},
        )

        repr_str = repr(context)

        # Should contain public fields
        assert "project_dir" in repr_str
        assert "session_id" in repr_str
        assert "agent_id" in repr_str

        # Should not contain internal fields
        assert "_session_manager" not in repr_str
        assert "_database" not in repr_str

    def test_equality(self, tmp_path):
        """Test HookContext equality comparison."""
        graph_dir = tmp_path / ".htmlgraph"
        graph_dir.mkdir()

        context1 = HookContext(
            project_dir=str(tmp_path),
            graph_dir=graph_dir,
            session_id="test-123",
            agent_id="test",
            hook_input={},
        )

        context2 = HookContext(
            project_dir=str(tmp_path),
            graph_dir=graph_dir,
            session_id="test-123",
            agent_id="test",
            hook_input={},
        )

        # Should be equal (same field values)
        assert context1 == context2

        # Different session_id should not be equal
        context3 = HookContext(
            project_dir=str(tmp_path),
            graph_dir=graph_dir,
            session_id="different",
            agent_id="test",
            hook_input={},
        )

        assert context1 != context3


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
