from __future__ import annotations

"""HtmlGraph CLI - Session management commands."""


import argparse
from typing import TYPE_CHECKING

from rich import box
from rich.console import Console
from rich.panel import Panel
from rich.table import Table

from htmlgraph.cli.base import BaseCommand, CommandError, CommandResult
from htmlgraph.cli.constants import DEFAULT_GRAPH_DIR

if TYPE_CHECKING:
    from argparse import _SubParsersAction

console = Console()


def register_session_commands(subparsers: _SubParsersAction) -> None:
    """Register session management commands."""
    session_parser = subparsers.add_parser("session", help="Session management")
    session_subparsers = session_parser.add_subparsers(
        dest="session_command", help="Session command"
    )

    # session start
    session_start = session_subparsers.add_parser("start", help="Start a new session")
    session_start.add_argument(
        "--id", help="Session ID (auto-generated if not provided)"
    )
    session_start.add_argument("--agent", default="claude-code", help="Agent name")
    session_start.add_argument("--title", help="Session title")
    session_start.add_argument(
        "--graph-dir", "-g", default=DEFAULT_GRAPH_DIR, help="Graph directory"
    )
    session_start.add_argument(
        "--format", choices=["json", "text"], default="text", help="Output format"
    )
    session_start.set_defaults(func=SessionStartCommand.from_args)

    # session end
    session_end = session_subparsers.add_parser("end", help="End a session")
    session_end.add_argument("id", help="Session ID to end")
    session_end.add_argument("--notes", help="Handoff notes for the next session")
    session_end.add_argument("--recommend", help="Recommended next steps")
    session_end.add_argument(
        "--blocker", action="append", default=[], help="Blocker to record"
    )
    session_end.add_argument(
        "--graph-dir", "-g", default=DEFAULT_GRAPH_DIR, help="Graph directory"
    )
    session_end.add_argument(
        "--format", choices=["json", "text"], default="text", help="Output format"
    )
    session_end.set_defaults(func=SessionEndCommand.from_args)

    # session list
    session_list = session_subparsers.add_parser("list", help="List all sessions")
    session_list.add_argument(
        "--graph-dir", "-g", default=DEFAULT_GRAPH_DIR, help="Graph directory"
    )
    session_list.add_argument(
        "--format", choices=["json", "text"], default="text", help="Output format"
    )
    session_list.set_defaults(func=SessionListCommand.from_args)

    # session handoff
    session_handoff = session_subparsers.add_parser(
        "handoff", help="Get or set handoff context"
    )
    session_handoff.add_argument(
        "--session-id", help="Session ID (defaults to last ended)"
    )
    session_handoff.add_argument("--notes", help="Handoff notes")
    session_handoff.add_argument("--recommend", help="Recommended next steps")
    session_handoff.add_argument(
        "--blocker", action="append", default=[], help="Blocker to record"
    )
    session_handoff.add_argument(
        "--show", action="store_true", help="Show handoff context"
    )
    session_handoff.add_argument("--agent", default="claude-code", help="Agent name")
    session_handoff.add_argument(
        "--graph-dir", "-g", default=DEFAULT_GRAPH_DIR, help="Graph directory"
    )
    session_handoff.add_argument(
        "--format", choices=["json", "text"], default="text", help="Output format"
    )
    session_handoff.set_defaults(func=SessionHandoffCommand.from_args)

    # session start-info
    session_start_info = session_subparsers.add_parser(
        "start-info", help="Get session start information"
    )
    session_start_info.add_argument("--agent", default="claude-code", help="Agent name")
    session_start_info.add_argument(
        "--no-git", action="store_true", help="Exclude git log"
    )
    session_start_info.add_argument(
        "--git-count", type=int, default=5, help="Number of git commits"
    )
    session_start_info.add_argument(
        "--top-n", type=int, default=3, help="Top N analytics items"
    )
    session_start_info.add_argument(
        "--max-agents", type=int, default=3, help="Max agents in analytics"
    )
    session_start_info.add_argument(
        "--graph-dir", "-g", default=DEFAULT_GRAPH_DIR, help="Graph directory"
    )
    session_start_info.add_argument(
        "--format", choices=["json", "text"], default="text", help="Output format"
    )
    session_start_info.set_defaults(func=SessionStartInfoCommand.from_args)


# ============================================================================
# Session Commands
# ============================================================================


class SessionStartCommand(BaseCommand):
    """Start a new session."""

    def __init__(
        self, *, session_id: str | None, agent: str, title: str | None
    ) -> None:
        super().__init__()
        self.session_id = session_id
        self.agent_name = agent
        self.title = title

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> SessionStartCommand:
        from pydantic import ValidationError

        from htmlgraph.cli.models import SessionStartConfig, format_validation_error

        try:
            # Validate through Pydantic model
            config = SessionStartConfig(
                session_id=getattr(args, "id", None),
                agent=args.agent,
                title=getattr(args, "title", None),
            )
        except ValidationError as e:
            raise CommandError(format_validation_error(e))

        return cls(
            session_id=config.session_id,
            agent=config.agent,
            title=config.title,
        )

    def execute(self) -> CommandResult:
        """Start a new session."""
        from htmlgraph.cli.base import TextOutputBuilder
        from htmlgraph.converter import session_to_dict

        sdk = self.get_sdk()

        with console.status("[blue]Starting session...", spinner="dots"):
            session = sdk.start_session(
                session_id=self.session_id,
                title=self.title,
                agent=self.agent_name,
            )

        output = TextOutputBuilder()
        output.add_success(f"Session started: {session.id}")
        output.add_field("Agent", session.agent)
        output.add_field("Started", session.started_at.isoformat())
        if session.title:
            output.add_field("Title", session.title)

        return CommandResult(
            data=session_to_dict(session),
            text=output.build(),
            json_data=session_to_dict(session),
        )


class SessionEndCommand(BaseCommand):
    """End a session."""

    def __init__(
        self,
        *,
        session_id: str,
        notes: str | None,
        recommend: str | None,
        blockers: list[str],
    ) -> None:
        super().__init__()
        self.session_id = session_id
        self.notes = notes
        self.recommend = recommend
        self.blockers = blockers

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> SessionEndCommand:
        from pydantic import ValidationError

        from htmlgraph.cli.models import SessionEndConfig, format_validation_error

        try:
            # Validate through Pydantic model
            config = SessionEndConfig(
                session_id=args.id,
                notes=args.notes,
                recommend=args.recommend,
                blockers=args.blocker,
            )
        except ValidationError as e:
            raise CommandError(format_validation_error(e))

        return cls(
            session_id=config.session_id,
            notes=config.notes,
            recommend=config.recommend,
            blockers=config.blockers,
        )

    def execute(self) -> CommandResult:
        """End a session."""
        from htmlgraph.cli.base import TextOutputBuilder
        from htmlgraph.converter import session_to_dict

        sdk = self.get_sdk()
        session = sdk.end_session(
            self.session_id,
            handoff_notes=self.notes,
            recommended_next=self.recommend,
            blockers=self.blockers,
        )

        self.require_node(session, "session", self.session_id)

        duration = session.ended_at - session.started_at if session.ended_at else None
        output = TextOutputBuilder()
        output.add_success(f"Session ended: {session.id}")
        output.add_field("Duration", duration)
        output.add_field("Events", session.event_count)
        if session.worked_on:
            output.add_field("Worked on", ", ".join(session.worked_on))

        return CommandResult(
            data=session_to_dict(session),
            text=output.build(),
            json_data=session_to_dict(session),
        )


class SessionListCommand(BaseCommand):
    """List all sessions."""

    def __init__(self, *, status: str | None = None, agent: str | None = None) -> None:
        super().__init__()
        self.status = status
        self.agent_filter = agent

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> SessionListCommand:
        # Validate inputs using SessionFilter model
        from pydantic import ValidationError

        from htmlgraph.cli.models import SessionFilter, format_validation_error

        # Get optional filter arguments
        status = getattr(args, "status", None)
        agent = getattr(args, "agent", None)

        try:
            filter_model = SessionFilter(status=status, agent=agent)
        except ValidationError as e:
            raise CommandError(format_validation_error(e))

        return cls(status=filter_model.status, agent=filter_model.agent)

    def execute(self) -> CommandResult:
        """List all sessions."""
        from pathlib import Path

        from htmlgraph.converter import SessionConverter, session_to_dict

        if self.graph_dir is None:
            raise CommandError("Missing graph directory")

        sessions_dir = Path(self.graph_dir) / "sessions"
        if not sessions_dir.exists():
            from htmlgraph.cli.base import TextOutputBuilder

            output = TextOutputBuilder()
            output.add_warning("No sessions found.")
            return CommandResult(
                text=output.build(),
                json_data={"sessions": []},
            )

        converter = SessionConverter(sessions_dir)

        with console.status("[blue]Loading sessions...", spinner="dots"):
            sessions = converter.load_all()

            # Convert to display models for type-safe filtering and sorting
            from htmlgraph.cli.models import SessionDisplay

            display_sessions = [SessionDisplay.from_node(s) for s in sessions]

            # Apply filters if provided
            if self.status:
                display_sessions = [
                    s for s in display_sessions if s.status == self.status
                ]
            if self.agent_filter:
                display_sessions = [
                    s for s in display_sessions if s.agent == self.agent_filter
                ]

            # Sort by started_at descending using display model's sort_key
            display_sessions.sort(key=lambda s: s.sort_key(), reverse=True)

        if not display_sessions:
            from htmlgraph.cli.base import TextOutputBuilder

            output = TextOutputBuilder()
            output.add_warning("No sessions found.")
            return CommandResult(
                text=output.build(),
                json_data={"sessions": []},
            )

        # Create Rich table
        table = Table(
            title="Sessions",
            show_header=True,
            header_style="bold magenta",
            box=box.ROUNDED,
        )
        table.add_column("ID", style="cyan", no_wrap=False, max_width=30)
        table.add_column("Status", style="green", width=10)
        table.add_column("Agent", style="blue", width=15)
        table.add_column("Events", justify="right", style="yellow", width=8)
        table.add_column("Started", style="white")

        for session in display_sessions:
            table.add_row(
                session.id,
                session.status,
                session.agent,
                str(session.event_count),
                session.started_str,
            )

        # Return table object directly - TextFormatter will print it properly
        return CommandResult(
            data=table,
            json_data=[session_to_dict(s) for s in sessions],
        )


class SessionHandoffCommand(BaseCommand):
    """Get or set session handoff context."""

    def __init__(
        self,
        *,
        session_id: str | None,
        notes: str | None,
        recommend: str | None,
        blockers: list[str],
        show: bool,
        agent: str,
    ) -> None:
        super().__init__()
        self.session_id = session_id
        self.notes = notes
        self.recommend = recommend
        self.blockers = blockers
        self.show = show
        self.agent_name = agent

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> SessionHandoffCommand:
        return cls(
            session_id=args.session_id,
            notes=args.notes,
            recommend=args.recommend,
            blockers=args.blocker,
            show=args.show,
            agent=args.agent,
        )

    def execute(self) -> CommandResult:
        """Get or set session handoff context."""
        from htmlgraph.converter import session_to_dict

        sdk = self.get_sdk()

        if self.show:
            # Show handoff context
            if self.session_id:
                session = sdk.session_manager.get_session(self.session_id)
            else:
                session = sdk.session_manager.get_last_ended_session(
                    agent=self.agent_name
                )

            if not session:
                return CommandResult(
                    text="No handoff context found.",
                    json_data={},
                )

            from htmlgraph.cli.base import TextOutputBuilder

            output = TextOutputBuilder()
            output.add_line(f"Session: {session.id}")
            if session.handoff_notes:
                output.add_field("Notes", session.handoff_notes)
            if session.recommended_next:
                output.add_field("Recommended next", session.recommended_next)
            if session.blockers:
                output.add_field("Blockers", ", ".join(session.blockers))

            return CommandResult(
                data=session_to_dict(session),
                text=output.build(),
                json_data=session_to_dict(session),
            )

        # Set handoff context (not implemented in old CLI, just return error)
        raise CommandError("Setting handoff context is not yet implemented")


class SessionStartInfoCommand(BaseCommand):
    """Get comprehensive session start information."""

    def __init__(
        self,
        *,
        agent: str,
        no_git: bool,
        git_count: int,
        top_n: int,
        max_agents: int,
    ) -> None:
        super().__init__()
        self.agent_name = agent
        self.no_git = no_git
        self.git_count = git_count
        self.top_n = top_n
        self.max_agents = max_agents

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> SessionStartInfoCommand:
        return cls(
            agent=args.agent,
            no_git=args.no_git,
            git_count=args.git_count,
            top_n=args.top_n,
            max_agents=args.max_agents,
        )

    def execute(self) -> CommandResult:
        """Get comprehensive session start information."""
        sdk = self.get_sdk()

        info = sdk.get_session_start_info(
            include_git_log=not self.no_git,
            git_log_count=self.git_count,
            analytics_top_n=self.top_n,
            analytics_max_agents=self.max_agents,
        )

        # Human-readable format
        status: dict = info["status"]  # type: ignore
        by_status = status.get("by_status", {})

        project_info = (
            f"Project: {status.get('project_name', 'HtmlGraph')}\n"
            f"Total features: {status.get('total_features', 0)}\n"
            f"In progress: {status.get('wip_count', 0)}\n"
            f"Completed: {by_status.get('done', 0)}"
        )

        panel = Panel(project_info, title="SESSION START INFO", border_style="cyan")

        # Return panel object directly - TextFormatter will print it properly
        return CommandResult(
            data=panel,
            json_data=info,
        )
