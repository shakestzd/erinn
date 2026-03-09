"""Tests for server operations module."""

from __future__ import annotations

import socket
from pathlib import Path
from unittest.mock import MagicMock, Mock, patch

import pytest
from htmlgraph.operations.server import (
    PortInUseError,
    ServerHandle,
    ServerStartError,
    ServerStartResult,
    _check_port_in_use,
    _find_available_port,
    get_server_status,
    start_server,
    stop_server,
)


class TestHelperFunctions:
    """Test helper functions."""

    def test_check_port_in_use_available(self) -> None:
        """Test checking an available port."""
        # Use a high port that's unlikely to be in use
        assert not _check_port_in_use(54321, "localhost")

    def test_check_port_in_use_occupied(self) -> None:
        """Test checking an occupied port."""
        # Create a socket to occupy a port
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
            s.bind(("localhost", 0))  # Bind to any available port
            port = s.getsockname()[1]
            # Port should be in use
            assert _check_port_in_use(port, "localhost")

    def test_find_available_port_success(self) -> None:
        """Test finding an available port."""
        port = _find_available_port(start_port=50000)
        assert 50000 <= port < 50010

    def test_find_available_port_no_ports_available(self) -> None:
        """Test when no ports are available."""
        with patch("socket.socket") as mock_socket:
            mock_socket.return_value.__enter__.return_value.bind.side_effect = OSError
            with pytest.raises(ServerStartError, match="No available ports found"):
                _find_available_port(start_port=8080, max_attempts=3)


class TestStartServer:
    """Test start_server function."""

    @pytest.fixture
    def temp_dirs(self, tmp_path: Path) -> tuple[Path, Path]:
        """Create temporary graph and static directories."""
        graph_dir = tmp_path / "graph"
        static_dir = tmp_path / "static"
        graph_dir.mkdir()
        static_dir.mkdir()
        return graph_dir, static_dir

    @patch("http.server.HTTPServer")
    @patch("htmlgraph.file_watcher.GraphWatcher")
    @patch("htmlgraph.operations.server._check_port_in_use")
    def test_start_server_basic(
        self,
        mock_check: Mock,
        mock_watcher: Mock,
        mock_http: Mock,
        temp_dirs: tuple[Path, Path],
    ) -> None:
        """Test basic server start."""
        graph_dir, static_dir = temp_dirs
        mock_check.return_value = False  # Port is available

        result = start_server(
            port=8080,
            graph_dir=graph_dir,
            static_dir=static_dir,
            watch=False,
        )

        assert isinstance(result, ServerStartResult)
        assert result.handle.port == 8080
        assert result.handle.host == "localhost"
        assert result.handle.url == "http://localhost:8080"
        assert result.config_used["port"] == 8080
        assert result.config_used["watch"] is False

    @patch("http.server.HTTPServer")
    @patch("htmlgraph.file_watcher.GraphWatcher")
    @patch("htmlgraph.operations.server._check_port_in_use")
    def test_start_server_auto_port(
        self,
        mock_check: Mock,
        mock_watcher: Mock,
        mock_http: Mock,
        temp_dirs: tuple[Path, Path],
    ) -> None:
        """Test server start with auto-port when port is in use."""
        graph_dir, static_dir = temp_dirs
        mock_check.side_effect = [True, False]  # First port in use, second available

        result = start_server(
            port=8080,
            graph_dir=graph_dir,
            static_dir=static_dir,
            watch=False,
            auto_port=True,
        )

        # Port should be different from original (8080) since it was in use
        assert result.handle.port > 8080
        assert len(result.warnings) > 0
        assert "Port 8080 is in use" in result.warnings[0]

    @patch("htmlgraph.operations.server._check_port_in_use")
    def test_start_server_port_in_use_no_auto(
        self,
        mock_check: Mock,
        temp_dirs: tuple[Path, Path],
    ) -> None:
        """Test server start fails when port is in use and auto_port=False."""
        graph_dir, static_dir = temp_dirs
        mock_check.return_value = True

        with pytest.raises(PortInUseError, match="Port 8080 is already in use"):
            start_server(
                port=8080,
                graph_dir=graph_dir,
                static_dir=static_dir,
                auto_port=False,
            )

    @patch("http.server.HTTPServer")
    @patch("htmlgraph.file_watcher.GraphWatcher")
    @patch("htmlgraph.operations.server._check_port_in_use")
    def test_start_server_with_watcher(
        self,
        mock_check: Mock,
        mock_watcher_class: Mock,
        mock_http: Mock,
        temp_dirs: tuple[Path, Path],
    ) -> None:
        """Test server start with file watcher enabled."""
        graph_dir, static_dir = temp_dirs
        mock_check.return_value = False  # Port is available
        mock_watcher = MagicMock()
        mock_watcher_class.return_value = mock_watcher

        result = start_server(
            port=8080,
            graph_dir=graph_dir,
            static_dir=static_dir,
            watch=True,
        )

        # Watcher should be created and started
        mock_watcher_class.assert_called_once()
        mock_watcher.start.assert_called_once()
        assert result.config_used["watch"] is True


class TestStopServer:
    """Test stop_server function."""

    def test_stop_server_with_dict_server(self) -> None:
        """Test stopping server with dict-style handle."""
        mock_http = MagicMock()
        mock_watcher = MagicMock()

        handle = ServerHandle(
            url="http://localhost:8080",
            port=8080,
            host="localhost",
            server={"httpserver": mock_http, "watcher": mock_watcher},
        )

        stop_server(handle)

        mock_watcher.stop.assert_called_once()
        mock_http.shutdown.assert_called_once()

    def test_stop_server_with_direct_server(self) -> None:
        """Test stopping server with direct HTTPServer."""
        mock_http = MagicMock()

        handle = ServerHandle(
            url="http://localhost:8080",
            port=8080,
            host="localhost",
            server=mock_http,
        )

        stop_server(handle)

        mock_http.shutdown.assert_called_once()

    def test_stop_server_none_server(self) -> None:
        """Test stopping server with None server (should not raise)."""
        handle = ServerHandle(
            url="http://localhost:8080",
            port=8080,
            host="localhost",
            server=None,
        )

        # Should not raise
        stop_server(handle)

    def test_stop_server_shutdown_failure(self) -> None:
        """Test handling shutdown failure."""
        mock_http = MagicMock()
        mock_http.shutdown.side_effect = RuntimeError("Shutdown failed")

        handle = ServerHandle(
            url="http://localhost:8080",
            port=8080,
            host="localhost",
            server=mock_http,
        )

        with pytest.raises(ServerStartError, match="Failed to stop server"):
            stop_server(handle)


class TestGetServerStatus:
    """Test get_server_status function."""

    def test_get_server_status_no_handle(self) -> None:
        """Test status check with no handle."""
        status = get_server_status(None)
        assert not status.running
        assert status.url is None

    @patch("htmlgraph.operations.server._check_port_in_use")
    def test_get_server_status_running(self, mock_check: Mock) -> None:
        """Test status check when server is running."""
        mock_check.return_value = False  # Port is available (server running)

        handle = ServerHandle(
            url="http://localhost:8080",
            port=8080,
            host="localhost",
            server=MagicMock(),
        )

        status = get_server_status(handle)
        assert status.running
        assert status.url == "http://localhost:8080"
        assert status.port == 8080
        assert status.host == "localhost"

    @patch("htmlgraph.operations.server._check_port_in_use")
    def test_get_server_status_not_running(self, mock_check: Mock) -> None:
        """Test status check when server is not running."""
        mock_check.return_value = True  # Port is in use (server not running)

        handle = ServerHandle(
            url="http://localhost:8080",
            port=8080,
            host="localhost",
            server=MagicMock(),
        )

        status = get_server_status(handle)
        assert not status.running
        assert status.url is None
