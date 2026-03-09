from __future__ import annotations

"""Server operations for HtmlGraph."""


from dataclasses import dataclass
from pathlib import Path
from typing import Any


@dataclass(frozen=True)
class ServerHandle:
    url: str
    port: int
    host: str
    server: Any | None = None


@dataclass(frozen=True)
class ServerStatus:
    running: bool
    url: str | None = None
    port: int | None = None
    host: str | None = None


@dataclass(frozen=True)
class ServerStartResult:
    handle: ServerHandle
    warnings: list[str]
    config_used: dict[str, Any]


class ServerStartError(RuntimeError):
    """Server failed to start."""


class PortInUseError(ServerStartError):
    """Requested port is already in use."""


def start_server(
    *,
    port: int,
    graph_dir: Path,
    static_dir: Path,
    host: str = "localhost",
    watch: bool = True,
    auto_port: bool = False,
) -> ServerStartResult:
    """
    Start the HtmlGraph server with validated configuration.

    Args:
        port: Port to listen on
        graph_dir: Directory containing graph data (.htmlgraph/)
        static_dir: Directory for static files (index.html, etc.)
        host: Host to bind to
        watch: Enable file watching for auto-reload
        auto_port: Automatically find available port if specified port is in use

    Returns:
        ServerStartResult with handle, warnings, and config used

    Raises:
        PortInUseError: If port is in use and auto_port=False
        ServerStartError: If server fails to start
    """
    from http.server import HTTPServer

    from htmlgraph.analytics_index import AnalyticsIndex
    from htmlgraph.event_log import JsonlEventLog
    from htmlgraph.file_watcher import GraphWatcher
    from htmlgraph.graph import HtmlGraph
    from htmlgraph.server import HtmlGraphAPIHandler

    warnings: list[str] = []
    original_port = port

    # Handle auto-port selection
    if auto_port and _check_port_in_use(port, host):
        port = _find_available_port(port + 1)
        warnings.append(f"Port {original_port} is in use, using {port} instead")

    # Check if port is still in use (and we're not in auto-port mode)
    if not auto_port and _check_port_in_use(port, host):
        raise PortInUseError(
            f"Port {port} is already in use. Use auto_port=True or choose a different port."
        )

    # Create graph directories
    graph_dir.mkdir(parents=True, exist_ok=True)
    for collection in HtmlGraphAPIHandler.COLLECTIONS:
        (graph_dir / collection).mkdir(exist_ok=True)

    # Copy default stylesheet
    styles_dest = graph_dir / "styles.css"
    if not styles_dest.exists():
        styles_src = Path(__file__).parent.parent / "styles.css"
        if styles_src.exists():
            styles_dest.write_text(styles_src.read_text())

    # Build analytics index if needed
    events_dir = graph_dir / "events"
    db_path = graph_dir / "index.sqlite"
    index_needs_build = (
        not db_path.exists() and events_dir.exists() and any(events_dir.glob("*.jsonl"))
    )

    if index_needs_build:
        try:
            log = JsonlEventLog(events_dir)
            index = AnalyticsIndex(db_path)
            events = (event for _, event in log.iter_events())
            index.rebuild_from_events(events)
        except Exception as e:
            warnings.append(f"Failed to build analytics index: {e}")

    # Configure handler
    HtmlGraphAPIHandler.graph_dir = graph_dir
    HtmlGraphAPIHandler.static_dir = static_dir
    HtmlGraphAPIHandler.graphs = {}
    HtmlGraphAPIHandler.analytics_db = None

    # Start HTTP server
    try:
        server = HTTPServer((host, port), HtmlGraphAPIHandler)
    except OSError as e:
        if e.errno == 48 or "Address already in use" in str(e):
            raise PortInUseError(f"Port {port} is already in use") from e
        raise ServerStartError(f"Failed to start server: {e}") from e

    # Start file watcher if enabled
    watcher = None
    if watch:

        def get_graph(collection: str) -> HtmlGraph:
            """Callback to get graph instance for a collection."""
            handler = HtmlGraphAPIHandler
            if collection not in handler.graphs:
                collection_dir = handler.graph_dir / collection
                handler.graphs[collection] = HtmlGraph(
                    collection_dir, stylesheet_path="../styles.css", auto_load=True
                )
            return handler.graphs[collection]

        watcher = GraphWatcher(
            graph_dir=graph_dir,
            collections=HtmlGraphAPIHandler.COLLECTIONS,
            get_graph_callback=get_graph,
        )
        watcher.start()

    # Create handle with localhost URL for display (even if binding to 0.0.0.0)
    display_host = "localhost" if host in ("0.0.0.0", "127.0.0.1") else host
    handle = ServerHandle(
        url=f"http://{display_host}:{port}",
        port=port,
        host=host,
        server={"httpserver": server, "watcher": watcher},
    )

    # Configuration used
    config_used = {
        "port": port,
        "original_port": original_port,
        "host": host,
        "graph_dir": str(graph_dir),
        "static_dir": str(static_dir),
        "watch": watch,
        "auto_port": auto_port,
    }

    return ServerStartResult(
        handle=handle,
        warnings=warnings,
        config_used=config_used,
    )


def stop_server(handle: ServerHandle) -> None:
    """
    Stop a running HtmlGraph server.

    Args:
        handle: ServerHandle returned from start_server()

    Raises:
        ServerStartError: If shutdown fails
    """
    if handle.server is None:
        return

    try:
        # Extract server components
        if isinstance(handle.server, dict):
            httpserver = handle.server.get("httpserver")
            watcher = handle.server.get("watcher")

            # Stop file watcher first
            if watcher is not None:
                try:
                    watcher.stop()
                except Exception:
                    pass  # Best effort

            # Shutdown HTTP server
            if httpserver is not None:
                httpserver.shutdown()
        else:
            # Assume it's the HTTPServer directly
            handle.server.shutdown()
    except Exception as e:
        raise ServerStartError(f"Failed to stop server: {e}") from e


def get_server_status(handle: ServerHandle | None = None) -> ServerStatus:
    """
    Return server status for a handle or best-effort local check.

    Args:
        handle: Optional ServerHandle to check

    Returns:
        ServerStatus indicating whether server is running
    """
    if handle is None:
        # No handle provided - cannot determine status
        return ServerStatus(running=False)

    # Check if server is running by testing the port
    try:
        is_running = not _check_port_in_use(handle.port, handle.host)
        return ServerStatus(
            running=is_running,
            url=handle.url if is_running else None,
            port=handle.port if is_running else None,
            host=handle.host if is_running else None,
        )
    except Exception:
        return ServerStatus(running=False)


# Helper functions (private)


def _check_port_in_use(port: int, host: str = "localhost") -> bool:
    """
    Check if a port is already in use.

    Args:
        port: Port number to check
        host: Host to check on

    Returns:
        True if port is in use, False otherwise
    """
    import socket

    try:
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
            s.bind((host, port))
            return False
    except OSError:
        return True


def _find_available_port(start_port: int = 8080, max_attempts: int = 10) -> int:
    """
    Find an available port starting from start_port.

    Args:
        start_port: Port to start searching from
        max_attempts: Maximum number of ports to try

    Returns:
        Available port number

    Raises:
        ServerStartError: If no available port found in range
    """
    import socket

    for port in range(start_port, start_port + max_attempts):
        try:
            with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
                s.bind(("", port))
                return port
        except OSError:
            continue
    raise ServerStartError(
        f"No available ports found in range {start_port}-{start_port + max_attempts}"
    )
