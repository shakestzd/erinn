from __future__ import annotations

"""HtmlGraph CLI - Infrastructure commands.

Commands for core infrastructure operations:
- serve: Start FastAPI server
- serve-api: Start API dashboard
- init: Initialize .htmlgraph directory
- status: Show graph status
- query: CSS selector query
- debug: Debug mode
- install-hooks: Install Git hooks
- Other utilities
"""


import argparse
import sys
from typing import TYPE_CHECKING

from htmlgraph.cli.base import BaseCommand, CommandError, CommandResult
from htmlgraph.cli.constants import (
    COLLECTIONS,
    DEFAULT_DATABASE_NAME,
    DEFAULT_GRAPH_DIR,
    DEFAULT_SERVER_HOST,
    DEFAULT_SERVER_PORT,
    get_error_message,
)

if TYPE_CHECKING:
    from argparse import _SubParsersAction


def register_commands(subparsers: _SubParsersAction) -> None:
    """Register infrastructure commands with the argument parser.

    Args:
        subparsers: Subparser action from ArgumentParser.add_subparsers()
    """
    # bootstrap
    bootstrap_parser = subparsers.add_parser(
        "bootstrap", help="One-command setup: Initialize HtmlGraph in under 60 seconds"
    )
    bootstrap_parser.add_argument(
        "--project-path",
        default=".",
        help="Directory to bootstrap (default: current directory)",
    )
    bootstrap_parser.add_argument(
        "--no-plugins",
        action="store_true",
        help="Skip plugin installation",
    )
    bootstrap_parser.set_defaults(func=BootstrapCommand.from_args)

    # serve
    serve_parser = subparsers.add_parser("serve", help="Start the HtmlGraph server")
    serve_parser.add_argument(
        "--port",
        "-p",
        type=int,
        default=DEFAULT_SERVER_PORT,
        help="Port (default: 8080)",
    )
    serve_parser.add_argument(
        "--host", default=DEFAULT_SERVER_HOST, help="Host to bind to (default: 0.0.0.0)"
    )
    serve_parser.add_argument(
        "--graph-dir", "-g", default=DEFAULT_GRAPH_DIR, help="Graph directory"
    )
    serve_parser.add_argument(
        "--static-dir", "-s", default=".", help="Static files directory"
    )
    serve_parser.add_argument(
        "--no-watch",
        action="store_true",
        help="Disable file watching (auto-reload disabled)",
    )
    serve_parser.add_argument(
        "--auto-port",
        action="store_true",
        help="Automatically find an available port if default is occupied",
    )
    serve_parser.set_defaults(func=ServeCommand.from_args)

    # serve-api
    serve_api_parser = subparsers.add_parser(
        "serve-api",
        help="Start the FastAPI-based observability dashboard",
    )
    serve_api_parser.add_argument(
        "--port", "-p", type=int, default=8000, help="Port (default: 8000)"
    )
    serve_api_parser.add_argument(
        "--host", default="127.0.0.1", help="Host to bind to (default: 127.0.0.1)"
    )
    serve_api_parser.add_argument(
        "--db", default=None, help="Path to SQLite database file"
    )
    serve_api_parser.add_argument(
        "--auto-port",
        action="store_true",
        help="Automatically find an available port if default is occupied",
    )
    serve_api_parser.add_argument(
        "--reload",
        action="store_true",
        help="Enable auto-reload on file changes (development mode)",
    )
    serve_api_parser.set_defaults(func=ServeApiCommand.from_args)

    # init
    init_parser = subparsers.add_parser("init", help="Initialize .htmlgraph directory")
    init_parser.add_argument(
        "dir", nargs="?", default=".", help="Directory to initialize"
    )
    init_parser.add_argument(
        "--install-hooks",
        action="store_true",
        help="Install Git hooks for event logging",
    )
    init_parser.add_argument(
        "--interactive", "-i", action="store_true", help="Interactive setup wizard"
    )
    init_parser.add_argument(
        "--no-index",
        action="store_true",
        help="Do not create the analytics cache (index.sqlite)",
    )
    init_parser.add_argument(
        "--no-update-gitignore",
        action="store_true",
        help="Do not update/create .gitignore for HtmlGraph cache files",
    )
    init_parser.add_argument(
        "--no-events-keep",
        action="store_true",
        help="Do not create .htmlgraph/events/.gitkeep",
    )
    init_parser.set_defaults(func=InitCommand.from_args)

    # status
    status_parser = subparsers.add_parser("status", help="Show graph status")
    status_parser.add_argument(
        "--graph-dir", "-g", default=DEFAULT_GRAPH_DIR, help="Graph directory"
    )
    status_parser.set_defaults(func=StatusCommand.from_args)

    # debug
    debug_parser = subparsers.add_parser(
        "debug", help="Show debugging resources and system diagnostics"
    )
    debug_parser.add_argument(
        "--graph-dir", "-g", default=DEFAULT_GRAPH_DIR, help="Graph directory"
    )
    debug_parser.set_defaults(func=DebugCommand.from_args)

    # query
    query_parser = subparsers.add_parser("query", help="Query nodes with CSS selector")
    query_parser.add_argument(
        "selector", help="CSS selector (e.g. [data-status='todo'])"
    )
    query_parser.add_argument(
        "--graph-dir", "-g", default=DEFAULT_GRAPH_DIR, help="Graph directory"
    )
    query_parser.set_defaults(func=QueryCommand.from_args)

    # install-hooks
    install_hooks_parser = subparsers.add_parser(
        "install-hooks", help="Install Git hooks for event logging"
    )
    install_hooks_parser.add_argument(
        "--graph-dir", "-g", default=DEFAULT_GRAPH_DIR, help="Graph directory"
    )
    install_hooks_parser.add_argument(
        "--force",
        action="store_true",
        help="Force installation, overwriting existing hooks",
    )
    install_hooks_parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Show what would be installed without making changes",
    )
    install_hooks_parser.set_defaults(func=InstallHooksCommand.from_args)

    # serve-hooks
    serve_hooks_parser = subparsers.add_parser(
        "serve-hooks", help="Start HTTP hook server to receive CloudEvent JSON events"
    )
    serve_hooks_parser.add_argument(
        "--port",
        "-p",
        type=int,
        default=8081,
        help="Port to listen on (default: 8081)",
    )
    serve_hooks_parser.add_argument(
        "--host",
        default="0.0.0.0",
        help="Host to bind to (default: 0.0.0.0)",
    )
    serve_hooks_parser.add_argument(
        "--graph-dir", "-g", default=DEFAULT_GRAPH_DIR, help="Graph directory"
    )
    serve_hooks_parser.set_defaults(func=ServeHooksCommand.from_args)

    # export otel
    export_parser = subparsers.add_parser(
        "export", help="Export HtmlGraph data to external systems"
    )
    export_subparsers = export_parser.add_subparsers(
        dest="export_target", help="Export target"
    )

    export_otel_parser = export_subparsers.add_parser(
        "otel",
        help="Export sessions/events as OTLP traces to an OpenTelemetry collector",
    )
    export_otel_parser.add_argument(
        "--endpoint",
        default="http://localhost:4318",
        help="OTLP HTTP base URL (default: http://localhost:4318)",
    )
    export_otel_parser.add_argument(
        "--session-limit",
        type=int,
        default=100,
        help="Maximum number of recent sessions to export (default: 100)",
    )
    export_otel_parser.add_argument(
        "--service-name",
        default="htmlgraph",
        help="OTLP service.name attribute (default: htmlgraph)",
    )
    export_otel_parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Print OTLP JSON payload instead of sending it",
    )
    export_otel_parser.add_argument(
        "--graph-dir", "-g", default=DEFAULT_GRAPH_DIR, help="Graph directory"
    )
    export_otel_parser.set_defaults(func=ExportOtelCommand.from_args)


# ============================================================================
# Command Implementations
# ============================================================================


class ServeCommand(BaseCommand):
    """Start the HtmlGraph server."""

    def __init__(
        self,
        *,
        port: int,
        host: str,
        static_dir: str,
        no_watch: bool,
        auto_port: bool,
    ) -> None:
        super().__init__()
        self.port = port
        self.host = host
        self.static_dir = static_dir
        self.no_watch = no_watch
        self.auto_port = auto_port

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> ServeCommand:
        from pydantic import ValidationError

        from htmlgraph.cli.models import ServeConfig, format_validation_error

        try:
            # Validate args through Pydantic model
            config = ServeConfig(
                port=args.port,
                host=args.host,
                graph_dir=getattr(args, "graph_dir", DEFAULT_GRAPH_DIR),
                static_dir=args.static_dir,
                no_watch=args.no_watch,
                auto_port=args.auto_port,
            )
        except ValidationError as e:
            raise CommandError(format_validation_error(e))

        return cls(
            port=config.port,
            host=config.host,
            static_dir=config.static_dir,
            no_watch=config.no_watch,
            auto_port=config.auto_port,
        )

    def execute(self) -> CommandResult:
        """Start the FastAPI server."""
        import asyncio
        from pathlib import Path

        from rich.console import Console
        from rich.panel import Panel

        from htmlgraph.operations.fastapi_server import (
            run_fastapi_server,
            start_fastapi_server,
        )

        console = Console()

        try:
            # Resolve db_path to an absolute path anchored at CWD so that
            # uvicorn / subprocess changes to working directory don't cause
            # the relative ".htmlgraph/htmlgraph.db" to resolve against the
            # home directory instead of the project directory.
            _graph_dir = Path(self.graph_dir or DEFAULT_GRAPH_DIR)
            if not _graph_dir.is_absolute():
                _graph_dir = Path.cwd() / _graph_dir
            db_path = str(_graph_dir / DEFAULT_DATABASE_NAME)

            result = start_fastapi_server(
                port=self.port,
                host=self.host,
                db_path=db_path,
                auto_port=self.auto_port,
                reload=False,  # Not supported for cmd_serve
            )

            # Display server info using Rich
            console.print()
            console.print(
                Panel.fit(
                    f"[bold blue]{result.handle.url}[/bold blue]",
                    title="[bold cyan]HtmlGraph Server (FastAPI)[/bold cyan]",
                    border_style="cyan",
                )
            )

            console.print(
                f"[dim]Graph directory:[/dim] {self.graph_dir or DEFAULT_GRAPH_DIR}"
            )
            console.print(f"[dim]Database:[/dim] {result.config_used['db_path']}")

            # Show warnings if any
            if result.warnings:
                console.print()
                for warning in result.warnings:
                    console.print(f"[yellow]⚠️  {warning}[/yellow]")

            # Show available features
            console.print()
            console.print("[cyan]Features:[/cyan]")
            console.print("  • Real-time agent activity feed (HTMX)")
            console.print("  • Orchestration chains visualization")
            console.print("  • Feature tracker with Kanban view")
            console.print("  • Session metrics & performance analytics")

            console.print()
            console.print("[cyan]Press Ctrl+C to stop.[/cyan]")
            console.print()

            # Run server (blocking)
            asyncio.run(run_fastapi_server(result.handle))

        except KeyboardInterrupt:
            console.print("\n[yellow]Shutting down...[/yellow]")
        except Exception as e:
            from htmlgraph.cli.base import save_traceback

            log_file = save_traceback(
                e, context={"command": "serve", "port": self.port}
            )
            console.print(f"\n[red]Error:[/red] {e}")
            console.print(f"[dim]Full traceback saved to:[/dim] {log_file}")
            sys.exit(1)

        return CommandResult(text="Server stopped")


class ServeApiCommand(BaseCommand):
    """Start the FastAPI-based dashboard."""

    def __init__(
        self,
        *,
        port: int,
        host: str,
        db: str | None,
        auto_port: bool,
        reload: bool,
    ) -> None:
        super().__init__()
        self.port = port
        self.host = host
        self.db = db
        self.auto_port = auto_port
        self.reload = reload

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> ServeApiCommand:
        from pydantic import ValidationError

        from htmlgraph.cli.models import ServeApiConfig, format_validation_error

        try:
            # Validate args through Pydantic model
            config = ServeApiConfig(
                port=args.port,
                host=args.host,
                db=args.db,
                auto_port=args.auto_port,
                reload=args.reload,
            )
        except ValidationError as e:
            raise CommandError(format_validation_error(e))

        return cls(
            port=config.port,
            host=config.host,
            db=config.db,
            auto_port=config.auto_port,
            reload=config.reload,
        )

    def execute(self) -> CommandResult:
        """Start the FastAPI dashboard server."""
        import asyncio

        from rich.console import Console
        from rich.panel import Panel

        from htmlgraph.operations.fastapi_server import (
            run_fastapi_server,
            start_fastapi_server,
        )

        console = Console()

        try:
            result = start_fastapi_server(
                port=self.port,
                host=self.host,
                db_path=self.db,
                auto_port=self.auto_port,
                reload=self.reload,
            )

            # Display server info using Rich
            console.print()
            console.print(
                Panel.fit(
                    f"[bold blue]{result.handle.url}[/bold blue]",
                    title="[bold cyan]HtmlGraph FastAPI Dashboard[/bold cyan]",
                    border_style="green",
                )
            )

            console.print("[bold green]✓[/bold green] Started observability dashboard")
            console.print(f"[dim]Database:[/dim] {result.config_used['db_path']}")

            # Show warnings if any
            if result.warnings:
                console.print()
                for warning in result.warnings:
                    console.print(f"[yellow]⚠️  {warning}[/yellow]")

            # Show available features
            console.print()
            console.print("[cyan]Features:[/cyan]")
            console.print("  • Real-time agent activity feed")
            console.print("  • Orchestration chains visualization")
            console.print("  • Feature tracker with Kanban view")
            console.print("  • Session metrics & performance analytics")
            console.print("  • WebSocket live event streaming")

            console.print()
            console.print("[cyan]Press Ctrl+C to stop.[/cyan]")
            console.print()

            # Run server (blocking)
            asyncio.run(run_fastapi_server(result.handle))

        except KeyboardInterrupt:
            console.print("\n[yellow]Shutting down...[/yellow]")
        except Exception as e:
            from htmlgraph.cli.base import save_traceback

            log_file = save_traceback(e, context={"command": "serve-api"})
            console.print(f"\n[red]Error:[/red] {e}")
            console.print(f"[dim]Full traceback saved to:[/dim] {log_file}")
            sys.exit(1)

        return CommandResult(text="Dashboard stopped")


class InitCommand(BaseCommand):
    """Initialize .htmlgraph directory."""

    def __init__(
        self,
        *,
        dir: str,
        install_hooks: bool,
        interactive: bool,
        no_index: bool,
        no_update_gitignore: bool,
        no_events_keep: bool,
    ) -> None:
        super().__init__()
        self.dir = dir
        self.install_hooks = install_hooks
        self.interactive = interactive
        self.no_index = no_index
        self.no_update_gitignore = no_update_gitignore
        self.no_events_keep = no_events_keep

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> InitCommand:
        return cls(
            dir=args.dir,
            install_hooks=args.install_hooks,
            interactive=args.interactive,
            no_index=args.no_index,
            no_update_gitignore=args.no_update_gitignore,
            no_events_keep=args.no_events_keep,
        )

    def execute(self) -> CommandResult:
        """Initialize the .htmlgraph directory."""
        from pydantic import ValidationError

        from htmlgraph.cli.base import TextOutputBuilder
        from htmlgraph.cli.models import InitConfig, format_validation_error
        from htmlgraph.operations.initialization import initialize_htmlgraph

        # Create config from command parameters with Pydantic validation
        try:
            config = InitConfig(
                dir=self.dir,
                install_hooks=self.install_hooks,
                interactive=self.interactive,
                no_index=self.no_index,
                no_update_gitignore=self.no_update_gitignore,
                no_events_keep=self.no_events_keep,
            )
        except ValidationError as e:
            raise CommandError(format_validation_error(e))

        # Initialize using new module
        result = initialize_htmlgraph(config)

        # Return result
        if result.success:
            output = TextOutputBuilder()
            output.add_success("Initialized .htmlgraph directory")
            output.add_field("Location", result.graph_dir)

            # Show what was created
            if result.directories_created:
                output.add_info(
                    f"Created {len(result.directories_created)} directories"
                )
            if result.files_created:
                output.add_info(f"Created/updated {len(result.files_created)} files")
            if result.hooks_installed:
                output.add_info("Git hooks installed")

            # Show any warnings
            for warning in result.warnings:
                output.add_warning(warning)

            return CommandResult(text=output.build(), json_data=result.dict())
        else:
            # Build error message from all errors
            error_msg = (
                "\n".join(result.errors) if result.errors else "Initialization failed"
            )
            raise CommandError(error_msg)


class StatusCommand(BaseCommand):
    """Show graph status."""

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> StatusCommand:
        return cls()

    def execute(self) -> CommandResult:
        """Show the current graph status."""
        from collections import Counter

        from rich.console import Console
        from rich.progress import Progress, SpinnerColumn, TextColumn

        console = Console()

        # Initialize SDK
        with console.status("[blue]Initializing SDK...", spinner="dots"):
            sdk = self.get_sdk()

        total = 0
        by_status: Counter[str] = Counter()
        by_collection: dict[str, int] = {}

        # Scan all collections
        with Progress(
            SpinnerColumn(),
            TextColumn("[progress.description]{task.description}"),
            console=console,
            transient=True,
        ) as progress:
            task = progress.add_task("Scanning collections...", total=len(COLLECTIONS))

            for coll_name in COLLECTIONS:
                progress.update(task, description=f"Scanning {coll_name}...")
                try:
                    coll = getattr(sdk, coll_name)
                    nodes = coll.all()
                    count = len(nodes)

                    if count > 0:
                        by_collection[coll_name] = count
                        total += count

                        # Count by status
                        for node in nodes:
                            status = getattr(node, "status", "unknown")
                            by_status[status] += 1

                except Exception:
                    # Collection might not exist yet
                    pass

                progress.update(task, advance=1)

        # Build status table
        from htmlgraph.cli.base import TableBuilder

        builder = TableBuilder.create_list_table(f"HtmlGraph Status: {self.graph_dir}")
        builder.add_column("Collection", style="cyan")
        builder.add_numeric_column("Count", style="green")

        for coll_name in sorted(by_collection.keys()):
            builder.add_row(coll_name, str(by_collection[coll_name]))

        builder.add_separator()
        builder.add_row("[bold]Total", f"[bold]{total}")
        table = builder.table

        # Display results
        console.print()
        console.print(table)

        # Show status breakdown
        if by_status:
            console.print()
            console.print("[cyan]By Status:[/cyan]")
            for status, count in sorted(by_status.items()):
                console.print(f"  {status}: {count}")

        return CommandResult(
            data={
                "total_nodes": total,
                "by_collection": dict(sorted(by_collection.items())),
                "by_status": dict(sorted(by_status.items())),
            },
            text=f"Total nodes: {total}",
        )


class DebugCommand(BaseCommand):
    """Show debugging resources."""

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> DebugCommand:
        return cls()

    def execute(self) -> CommandResult:
        """Show debugging resources and diagnostics."""
        import os
        import sys
        from pathlib import Path

        from rich.console import Console
        from rich.panel import Panel

        console = Console()

        # Header
        console.print()
        console.print(
            Panel.fit(
                "[bold cyan]HtmlGraph Debugging Resources[/bold cyan]",
                border_style="cyan",
            )
        )

        # Documentation section
        console.print("\n[bold yellow]Documentation:[/bold yellow]")
        console.print("  • DEBUGGING.md - Complete debugging guide")
        console.print("  • AGENTS.md - SDK and agent documentation")
        console.print("  • CLAUDE.md - Project workflow")

        # Debugging Agents section
        console.print("\n[bold yellow]Debugging Agents:[/bold yellow]")
        agents_dir = Path("packages/claude-plugin/agents")
        if agents_dir.exists():
            console.print(f"  • {agents_dir}/researcher.md")
            console.print(f"  • {agents_dir}/debugger.md")
            console.print(f"  • {agents_dir}/test-runner.md")
        else:
            console.print(
                "  • researcher.md - Research documentation before implementing"
            )
            console.print("  • debugger.md - Systematic error analysis")
            console.print("  • test-runner.md - Quality gates and validation")

        # Diagnostic Commands section
        from htmlgraph.cli.base import TableBuilder

        console.print("\n[bold yellow]Diagnostic Commands:[/bold yellow]")
        cmd_builder = TableBuilder.create_compact_table()
        cmd_builder.add_column("Command", style="cyan")
        cmd_builder.add_column("Description", style="dim")
        cmd_builder.add_row("htmlgraph status", "Show current graph state")
        cmd_builder.add_row("htmlgraph feature list", "List all features")
        cmd_builder.add_row("htmlgraph session list", "List all sessions")
        cmd_builder.add_row("htmlgraph analytics", "Project analytics")
        console.print(cmd_builder.table)

        # Current Status section
        console.print("\n[bold yellow]Current Status:[/bold yellow]")
        graph_path = Path(self.graph_dir or DEFAULT_GRAPH_DIR)

        status_builder = TableBuilder.create_compact_table()
        status_builder.add_column("Item", style="dim")
        status_builder.add_column("Value")

        status_builder.add_row("Graph directory:", str(graph_path))

        if graph_path.exists():
            status_builder.add_row("Status:", "[green]✓ Initialized[/green]")

            # Try to get quick stats
            try:
                sdk = self.get_sdk()

                # Count features
                features = sdk.features.all()
                status_builder.add_row("Features:", str(len(features)))

                # Count sessions
                sessions = sdk.sessions.all()
                status_builder.add_row("Sessions:", str(len(sessions)))

                # Count other collections
                for coll_name in [
                    "bugs",
                    "chores",
                    "spikes",
                    "epics",
                    "phases",
                    "tracks",
                ]:
                    try:
                        coll = getattr(sdk, coll_name)
                        nodes = coll.all()
                        if len(nodes) > 0:
                            status_builder.add_row(
                                f"{coll_name.capitalize()}:", str(len(nodes))
                            )
                    except Exception:
                        pass

            except Exception as e:
                status_builder.add_row(
                    "Warning:", f"[yellow]Could not load graph data: {e}[/yellow]"
                )
        else:
            status_builder.add_row("Status:", "[yellow]⚠️  Not initialized[/yellow]")
            status_builder.add_row(
                "", "[dim]Run 'htmlgraph init' to create .htmlgraph directory[/dim]"
            )

        console.print(status_builder.table)

        # Environment Info section
        console.print("\n[bold yellow]Environment:[/bold yellow]")
        env_builder = TableBuilder.create_compact_table()
        env_builder.add_column("Item", style="dim")
        env_builder.add_column("Value")
        env_builder.add_row("Python:", sys.version.split()[0])
        env_builder.add_row("Working dir:", os.getcwd())
        console.print(env_builder.table)

        # Project Files section
        console.print("\n[bold yellow]Project Files:[/bold yellow]")
        files_builder = TableBuilder.create_compact_table()
        files_builder.add_column("Status", justify="center")
        files_builder.add_column("File")
        for filename in ["pyproject.toml", "package.json", ".git", "README.md"]:
            exists = "[green]✓[/green]" if Path(filename).exists() else "[red]✗[/red]"
            files_builder.add_row(exists, filename)
        console.print(files_builder.table)

        # Footer
        console.print()
        console.print(
            "[dim]For more help: https://github.com/Shakes-tzd/htmlgraph[/dim]"
        )
        console.print()

        return CommandResult(text="Debug info displayed")


class QueryCommand(BaseCommand):
    """Query nodes with CSS selector."""

    def __init__(self, *, selector: str) -> None:
        super().__init__()
        self.selector = selector

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> QueryCommand:
        return cls(selector=args.selector)

    def execute(self) -> CommandResult:
        """Execute CSS selector query."""
        from pathlib import Path
        from typing import Any

        from rich.console import Console
        from rich.table import Table

        from htmlgraph.converter import node_to_dict
        from htmlgraph.graph import HtmlGraph

        console = Console()

        graph_dir = Path(self.graph_dir or DEFAULT_GRAPH_DIR)
        if not graph_dir.exists():
            raise CommandError(
                get_error_message("missing_graph_dir", path=str(graph_dir))
            )

        # Query across all collections
        results: list[dict[str, Any]] = []

        with console.status(
            f"[blue]Querying with selector '{self.selector}'...", spinner="dots"
        ):
            for collection_dir in graph_dir.iterdir():
                if collection_dir.is_dir() and not collection_dir.name.startswith("."):
                    graph = HtmlGraph(collection_dir, auto_load=True)
                    for node in graph.query(self.selector):
                        data = node_to_dict(node)
                        data["_collection"] = collection_dir.name
                        results.append(data)

        # Display results in table
        if results:
            table = Table(
                title=f"Query Results: {self.selector}",
                show_header=True,
                header_style="bold cyan",
            )
            table.add_column("Collection", style="dim")
            table.add_column("ID", style="cyan")
            table.add_column("Title", style="white")
            table.add_column("Status", style="blue")
            table.add_column("Priority", style="yellow")

            for result in results:
                table.add_row(
                    result.get("_collection", "?"),
                    result.get("id", "?"),
                    result.get("title", "?"),
                    result.get("status", "?"),
                    result.get("priority", "?"),
                )

            console.print()
            console.print(table)
            console.print(f"\n[green]Found {len(results)} results[/green]")
        else:
            console.print(f"\n[yellow]No results found for '{self.selector}'[/yellow]")

        return CommandResult(data=results, text=f"Found {len(results)} results")


class InstallHooksCommand(BaseCommand):
    """Install Git hooks for event logging."""

    def __init__(self, *, force: bool = False, dry_run: bool = False) -> None:
        super().__init__()
        self.force = force
        self.dry_run = dry_run

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> InstallHooksCommand:
        return cls(
            force=getattr(args, "force", False),
            dry_run=getattr(args, "dry_run", False),
        )

    def execute(self) -> CommandResult:
        """Install Git hooks."""
        from pathlib import Path

        from rich.console import Console

        from htmlgraph.hooks.installer import HookConfig, HookInstaller

        console = Console()

        graph_dir = Path(self.graph_dir or DEFAULT_GRAPH_DIR).resolve()

        # Validate environment
        if not (graph_dir.parent / ".git").exists():
            raise CommandError("Not a git repository (no .git directory found)")

        if not graph_dir.exists():
            raise CommandError(f"Graph directory not found: {graph_dir}")

        # Create hook config and installer
        config_path = graph_dir / "hooks-config.json"
        config = HookConfig(config_path)
        installer = HookInstaller(graph_dir.parent, config)

        # Validate environment
        is_valid, error_msg = installer.validate_environment()
        if not is_valid:
            raise CommandError(error_msg)

        # Install hooks
        with console.status("[blue]Installing Git hooks...", spinner="dots"):
            results = installer.install_all_hooks(
                dry_run=self.dry_run, force=self.force
            )

        # Build output
        from htmlgraph.cli.base import TextOutputBuilder

        output = TextOutputBuilder()

        if self.dry_run:
            output.add_info("DRY RUN - No changes made")

        # Count results
        success_count = sum(1 for success, _ in results.values() if success)
        total = len(results)

        output.add_success(f"Installed {success_count}/{total} hooks")

        # Show individual results
        for hook_name, (success, message) in sorted(results.items()):
            status = "[green]✓[/green]" if success else "[yellow]✗[/yellow]"
            output.add_line(f"{status} {hook_name}: {message}")

        return CommandResult(
            text=output.build(),
            json_data={
                "dry_run": self.dry_run,
                "installed": success_count,
                "total": total,
                "results": {
                    name: {"success": success, "message": msg}
                    for name, (success, msg) in results.items()
                },
            },
        )


class BootstrapCommand(BaseCommand):
    """Bootstrap HtmlGraph in under 60 seconds."""

    def __init__(self, *, project_path: str, no_plugins: bool) -> None:
        super().__init__()
        self.project_path = project_path
        self.no_plugins = no_plugins

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> BootstrapCommand:
        return cls(
            project_path=args.project_path,
            no_plugins=args.no_plugins,
        )

    def execute(self) -> CommandResult:
        """Bootstrap HtmlGraph setup."""
        from pydantic import ValidationError
        from rich.console import Console
        from rich.panel import Panel

        from htmlgraph.cli.models import BootstrapConfig, format_validation_error
        from htmlgraph.operations.bootstrap import bootstrap_htmlgraph

        console = Console()

        # Create config with Pydantic validation
        try:
            config = BootstrapConfig(
                project_path=self.project_path,
                no_plugins=self.no_plugins,
            )
        except ValidationError as e:
            raise CommandError(format_validation_error(e))

        # Run bootstrap
        console.print()
        console.print("[bold cyan]Bootstrapping HtmlGraph...[/bold cyan]")
        console.print()

        result = bootstrap_htmlgraph(config)

        if not result["success"]:
            raise CommandError(result.get("message", "Bootstrap failed"))

        # Display success message
        console.print()
        console.print(
            Panel.fit(
                "[bold green]✓ HtmlGraph initialized successfully![/bold green]",
                border_style="green",
            )
        )
        console.print()

        # Show project info
        console.print(f"[cyan]Project type:[/cyan] {result['project_type']}")
        console.print(f"[cyan]Location:[/cyan] {result['graph_dir']}")
        console.print()

        # Show next steps
        console.print("[bold yellow]Next steps:[/bold yellow]")
        for step in result["next_steps"]:
            console.print(f"  {step}")
        console.print()

        # Show documentation link
        console.print(
            "[dim]📚 Learn more: https://github.com/Shakes-tzd/htmlgraph[/dim]"
        )
        console.print()

        return CommandResult(
            text="Bootstrap completed successfully",
            json_data={
                "project_type": result["project_type"],
                "graph_dir": result["graph_dir"],
                "directories_created": len(result["directories_created"]),
                "files_created": len(result["files_created"]),
                "has_claude": result["has_claude"],
                "plugin_installed": result["plugin_installed"],
            },
        )


class IngestGeminiCommand(BaseCommand):
    """Ingest Gemini CLI sessions into HtmlGraph."""

    def __init__(
        self,
        *,
        path: str | None,
        agent: str,
        limit: int | None,
        dry_run: bool,
    ) -> None:
        super().__init__()
        self.path = path
        self.agent = agent
        self.limit = limit
        self.dry_run = dry_run

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> IngestGeminiCommand:
        return cls(
            path=getattr(args, "path", None),
            agent=getattr(args, "agent", "gemini"),
            limit=getattr(args, "limit", None),
            dry_run=getattr(args, "dry_run", False),
        )

    def execute(self) -> CommandResult:
        """Ingest Gemini CLI sessions into HtmlGraph."""
        from pathlib import Path

        from rich.console import Console

        from htmlgraph.cli.base import TextOutputBuilder
        from htmlgraph.ingest.gemini import ingest_gemini_sessions

        console = Console()

        base_path = Path(self.path) if self.path else None

        dry_run_label = " (dry run)" if self.dry_run else ""
        with console.status(
            f"[blue]Ingesting Gemini sessions{dry_run_label}...", spinner="dots"
        ):
            result = ingest_gemini_sessions(
                graph_dir=self.graph_dir,
                agent=self.agent or "gemini",
                base_path=base_path,
                limit=self.limit,
                dry_run=self.dry_run,
            )

        output = TextOutputBuilder()
        if self.dry_run:
            output.add_success(f"Dry run: found {result.ingested} sessions to ingest")
        else:
            output.add_success(f"Ingested {result.ingested} Gemini sessions")

        if result.skipped:
            output.add_field("Skipped", str(result.skipped))
        if result.errors:
            output.add_field("Errors", str(result.errors))
        if result.session_ids:
            output.add_field("Sessions", str(len(result.session_ids)))
        if result.error_files:
            output.add_field("Failed files", ", ".join(result.error_files[:3]))

        return CommandResult(
            text=output.build(),
            json_data={
                "ingested": result.ingested,
                "skipped": result.skipped,
                "errors": result.errors,
                "session_ids": result.session_ids,
                "error_files": result.error_files,
                "dry_run": self.dry_run,
            },
        )


class ServeHooksCommand(BaseCommand):
    """Start an HTTP server that accepts CloudEvent JSON and stores events."""

    def __init__(self, *, host: str, port: int) -> None:
        super().__init__()
        self.host = host
        self.port = port

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> ServeHooksCommand:
        return cls(
            host=getattr(args, "host", "0.0.0.0"),
            port=getattr(args, "port", 8081),
        )

    def execute(self) -> CommandResult:
        """Start the HTTP hook server (blocking)."""
        from htmlgraph.http_hook import run_http_hook_server

        run_http_hook_server(
            host=self.host,
            port=self.port,
            graph_dir=self.graph_dir or ".htmlgraph",
        )
        return CommandResult(text="HTTP hook server stopped.", json_data={})


class ExportOtelCommand(BaseCommand):
    """Export HtmlGraph sessions/events as OTLP traces."""

    def __init__(
        self,
        *,
        endpoint: str,
        session_limit: int,
        service_name: str,
        dry_run: bool,
    ) -> None:
        super().__init__()
        self.endpoint = endpoint
        self.session_limit = session_limit
        self.service_name = service_name
        self.dry_run = dry_run

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> ExportOtelCommand:
        return cls(
            endpoint=getattr(args, "endpoint", "http://localhost:4318"),
            session_limit=getattr(args, "session_limit", 100),
            service_name=getattr(args, "service_name", "htmlgraph"),
            dry_run=getattr(args, "dry_run", False),
        )

    def execute(self) -> CommandResult:
        """Export sessions as OTLP traces."""
        from htmlgraph.otel import export_to_otlp

        count = export_to_otlp(
            endpoint=self.endpoint,
            graph_dir=self.graph_dir or ".htmlgraph",
            session_limit=self.session_limit,
            service_name=self.service_name,
            dry_run=self.dry_run,
        )

        msg = f"Exported {count} sessions to {self.endpoint}"
        if self.dry_run:
            msg = f"Dry run: {count} sessions would be exported to {self.endpoint}"

        return CommandResult(
            text=msg,
            json_data={
                "exported": count,
                "endpoint": self.endpoint,
                "dry_run": self.dry_run,
            },
        )


class IngestCopilotCommand(BaseCommand):
    """Ingest GitHub Copilot CLI sessions into HtmlGraph."""

    def __init__(
        self,
        *,
        path: str | None,
        agent: str,
        limit: int | None,
        dry_run: bool,
    ) -> None:
        super().__init__()
        self.path = path
        self.agent = agent
        self.limit = limit
        self.dry_run = dry_run

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> IngestCopilotCommand:
        return cls(
            path=getattr(args, "path", None),
            agent=getattr(args, "agent", "copilot"),
            limit=getattr(args, "limit", None),
            dry_run=getattr(args, "dry_run", False),
        )

    def execute(self) -> CommandResult:
        """Ingest GitHub Copilot CLI sessions into HtmlGraph."""
        from pathlib import Path

        from rich.console import Console

        from htmlgraph.cli.base import TextOutputBuilder
        from htmlgraph.ingest.copilot import ingest_copilot_sessions

        console = Console()

        base_path = Path(self.path) if self.path else None

        dry_run_label = " (dry run)" if self.dry_run else ""
        with console.status(
            f"[blue]Ingesting Copilot sessions{dry_run_label}...", spinner="dots"
        ):
            result = ingest_copilot_sessions(
                graph_dir=self.graph_dir,
                agent=self.agent or "copilot",
                base_path=base_path,
                limit=self.limit,
                dry_run=self.dry_run,
            )

        output = TextOutputBuilder()
        if self.dry_run:
            output.add_success(f"Dry run: found {result.ingested} sessions to ingest")
        else:
            output.add_success(f"Ingested {result.ingested} Copilot sessions")

        if result.skipped:
            output.add_field("Skipped", str(result.skipped))
        if result.errors:
            output.add_field("Errors", str(result.errors))
        if result.session_ids:
            output.add_field("Sessions", str(len(result.session_ids)))
        if result.error_files:
            output.add_field("Failed files", ", ".join(result.error_files[:3]))

        return CommandResult(
            text=output.build(),
            json_data={
                "ingested": result.ingested,
                "skipped": result.skipped,
                "errors": result.errors,
                "session_ids": result.session_ids,
                "error_files": result.error_files,
                "dry_run": self.dry_run,
            },
        )


class IngestCodexCommand(BaseCommand):
    """Ingest OpenAI Codex CLI sessions into HtmlGraph."""

    def __init__(
        self,
        *,
        path: str | None,
        agent: str,
        limit: int | None,
        dry_run: bool,
    ) -> None:
        super().__init__()
        self.path = path
        self.agent = agent
        self.limit = limit
        self.dry_run = dry_run

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> IngestCodexCommand:
        return cls(
            path=getattr(args, "path", None),
            agent=getattr(args, "agent", "codex"),
            limit=getattr(args, "limit", None),
            dry_run=getattr(args, "dry_run", False),
        )

    def execute(self) -> CommandResult:
        """Ingest OpenAI Codex CLI sessions into HtmlGraph."""
        from pathlib import Path

        from rich.console import Console

        from htmlgraph.cli.base import TextOutputBuilder
        from htmlgraph.ingest.codex import ingest_codex_sessions

        console = Console()

        base_path = Path(self.path) if self.path else None

        dry_run_label = " (dry run)" if self.dry_run else ""
        with console.status(
            f"[blue]Ingesting Codex sessions{dry_run_label}...", spinner="dots"
        ):
            result = ingest_codex_sessions(
                graph_dir=self.graph_dir,
                agent=self.agent or "codex",
                base_path=base_path,
                limit=self.limit,
                dry_run=self.dry_run,
            )

        output = TextOutputBuilder()
        if self.dry_run:
            output.add_success(f"Dry run: found {result.ingested} sessions to ingest")
        else:
            output.add_success(f"Ingested {result.ingested} Codex sessions")

        if result.skipped:
            output.add_field("Skipped", str(result.skipped))
        if result.errors:
            output.add_field("Errors", str(result.errors))
        if result.session_ids:
            output.add_field("Sessions", str(len(result.session_ids)))
        if result.error_files:
            output.add_field("Failed files", ", ".join(result.error_files[:3]))

        return CommandResult(
            text=output.build(),
            json_data={
                "ingested": result.ingested,
                "skipped": result.skipped,
                "errors": result.errors,
                "session_ids": result.session_ids,
                "error_files": result.error_files,
                "dry_run": self.dry_run,
            },
        )
