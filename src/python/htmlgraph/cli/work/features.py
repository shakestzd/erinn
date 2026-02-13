from __future__ import annotations

"""HtmlGraph CLI - Feature management commands."""


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


def register_feature_commands(subparsers: _SubParsersAction) -> None:
    """Register feature management commands."""
    feature_parser = subparsers.add_parser("feature", help="Feature management")
    feature_subparsers = feature_parser.add_subparsers(
        dest="feature_command", help="Feature command"
    )

    # feature list
    feature_list = feature_subparsers.add_parser("list", help="List all features")
    feature_list.add_argument(
        "--status",
        choices=["todo", "in_progress", "completed", "blocked"],
        help="Filter by status",
    )
    feature_list.add_argument(
        "--graph-dir", "-g", default=DEFAULT_GRAPH_DIR, help="Graph directory"
    )
    feature_list.add_argument(
        "--format", choices=["json", "text"], default="text", help="Output format"
    )
    feature_list.add_argument(
        "--quiet", "-q", action="store_true", help="Suppress empty output"
    )
    feature_list.set_defaults(func=FeatureListCommand.from_args)

    # feature create
    feature_create = feature_subparsers.add_parser(
        "create", help="Create a new feature"
    )
    feature_create.add_argument("title", help="Feature title")
    feature_create.add_argument("--description", help="Feature description")
    feature_create.add_argument(
        "--priority", choices=["low", "medium", "high", "critical"], default="medium"
    )
    feature_create.add_argument("--steps", type=int, help="Number of steps")
    feature_create.add_argument(
        "--collection", default="features", help="Collection name"
    )
    feature_create.add_argument("--track", help="Track ID to link feature to")
    feature_create.add_argument("--agent", default="claude-code", help="Agent name")
    feature_create.add_argument(
        "--graph-dir", "-g", default=DEFAULT_GRAPH_DIR, help="Graph directory"
    )
    feature_create.add_argument(
        "--format", choices=["json", "text"], default="text", help="Output format"
    )
    feature_create.set_defaults(func=FeatureCreateCommand.from_args)

    # feature start
    feature_start = feature_subparsers.add_parser(
        "start", help="Start working on a feature"
    )
    feature_start.add_argument("id", help="Feature ID")
    feature_start.add_argument(
        "--collection", default="features", help="Collection name"
    )
    feature_start.add_argument("--agent", default="claude-code", help="Agent name")
    feature_start.add_argument(
        "--graph-dir", "-g", default=DEFAULT_GRAPH_DIR, help="Graph directory"
    )
    feature_start.add_argument(
        "--format", choices=["json", "text"], default="text", help="Output format"
    )
    feature_start.set_defaults(func=FeatureStartCommand.from_args)

    # feature complete
    feature_complete = feature_subparsers.add_parser(
        "complete", help="Mark feature as completed"
    )
    feature_complete.add_argument("id", help="Feature ID")
    feature_complete.add_argument(
        "--collection", default="features", help="Collection name"
    )
    feature_complete.add_argument("--agent", default="claude-code", help="Agent name")
    feature_complete.add_argument(
        "--graph-dir", "-g", default=DEFAULT_GRAPH_DIR, help="Graph directory"
    )
    feature_complete.add_argument(
        "--format", choices=["json", "text"], default="text", help="Output format"
    )
    feature_complete.set_defaults(func=FeatureCompleteCommand.from_args)

    # feature claim
    feature_claim = feature_subparsers.add_parser("claim", help="Claim a feature")
    feature_claim.add_argument("id", help="Feature ID")
    feature_claim.add_argument(
        "--collection", default="features", help="Collection name"
    )
    feature_claim.add_argument("--agent", default="claude-code", help="Agent name")
    feature_claim.add_argument(
        "--graph-dir", "-g", default=DEFAULT_GRAPH_DIR, help="Graph directory"
    )
    feature_claim.add_argument(
        "--format", choices=["json", "text"], default="text", help="Output format"
    )
    feature_claim.set_defaults(func=FeatureClaimCommand.from_args)

    # feature release
    feature_release = feature_subparsers.add_parser("release", help="Release a feature")
    feature_release.add_argument("id", help="Feature ID")
    feature_release.add_argument(
        "--collection", default="features", help="Collection name"
    )
    feature_release.add_argument("--agent", default="claude-code", help="Agent name")
    feature_release.add_argument(
        "--graph-dir", "-g", default=DEFAULT_GRAPH_DIR, help="Graph directory"
    )
    feature_release.add_argument(
        "--format", choices=["json", "text"], default="text", help="Output format"
    )
    feature_release.set_defaults(func=FeatureReleaseCommand.from_args)

    # feature primary
    feature_primary = feature_subparsers.add_parser(
        "primary", help="Set primary feature"
    )
    feature_primary.add_argument("id", help="Feature ID")
    feature_primary.add_argument(
        "--collection", default="features", help="Collection name"
    )
    feature_primary.add_argument("--agent", default="claude-code", help="Agent name")
    feature_primary.add_argument(
        "--graph-dir", "-g", default=DEFAULT_GRAPH_DIR, help="Graph directory"
    )
    feature_primary.add_argument(
        "--format", choices=["json", "text"], default="text", help="Output format"
    )
    feature_primary.set_defaults(func=FeaturePrimaryCommand.from_args)


# ============================================================================
# Feature Commands
# ============================================================================


class FeatureListCommand(BaseCommand):
    """List all features."""

    def __init__(self, *, status: str | None, quiet: bool) -> None:
        super().__init__()
        self.status = status
        self.quiet = quiet

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> FeatureListCommand:
        # Validate inputs using FeatureFilter model
        from pydantic import ValidationError

        from htmlgraph.cli.models import FeatureFilter, format_validation_error

        try:
            filter_model = FeatureFilter(
                status=args.status,
                quiet=getattr(args, "quiet", False),
            )
        except ValidationError as e:
            raise CommandError(format_validation_error(e))

        return cls(
            status=filter_model.status,
            quiet=filter_model.quiet,
        )

    def execute(self) -> CommandResult:
        """List all features."""
        from htmlgraph.cli.models import FeatureDisplay
        from htmlgraph.converter import node_to_dict

        sdk = self.get_sdk()

        # Query features with SDK
        if self.status:
            nodes = sdk.features.where(status=self.status)
        else:
            nodes = sdk.features.all()

        # Convert to display models for type-safe sorting
        display_features = [FeatureDisplay.from_node(n) for n in nodes]

        # Sort by priority then updated using display model's sort_key
        display_features.sort(key=lambda f: f.sort_key(), reverse=True)

        if not display_features:
            if not self.quiet:
                from htmlgraph.cli.base import TextOutputBuilder

                status_msg = f"with status '{self.status}'" if self.status else ""
                output = TextOutputBuilder()
                output.add_warning(f"No features found {status_msg}.")
                return CommandResult(text=output.build(), json_data={"features": []})
            return CommandResult(json_data={"features": []})

        # Create Rich table
        table = Table(
            title="Features",
            show_header=True,
            header_style="bold magenta",
            box=box.ROUNDED,
        )
        table.add_column("ID", style="cyan", no_wrap=False, max_width=20)
        table.add_column("Title", style="yellow", max_width=40)
        table.add_column("Status", style="green", width=12)
        table.add_column("Priority", style="blue", width=10)
        table.add_column("Updated", style="white", width=16)

        for feature in display_features:
            table.add_row(
                feature.id,
                feature.title,
                feature.status,
                feature.priority,
                feature.updated_str,
            )

        # Return table object directly - TextFormatter will print it properly
        return CommandResult(
            data=table,
            json_data=[node_to_dict(n) for n in nodes],
        )


class FeatureCreateCommand(BaseCommand):
    """Create a new feature."""

    def __init__(
        self,
        *,
        title: str,
        description: str | None,
        priority: str,
        steps: int | None,
        collection: str,
        track_id: str | None,
    ) -> None:
        super().__init__()
        self.title = title
        self.description = description
        self.priority = priority
        self.steps = steps
        self.collection = collection
        self.track_id = track_id

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> FeatureCreateCommand:
        from pydantic import ValidationError

        from htmlgraph.cli.models import FeatureCreateConfig, format_validation_error

        try:
            # Validate through Pydantic model
            config = FeatureCreateConfig(
                title=args.title,
                description=args.description,
                priority=args.priority,
                steps=args.steps,
                collection=args.collection,
                track=args.track,
                agent=getattr(args, "agent", "claude-code"),
            )
        except ValidationError as e:
            raise CommandError(format_validation_error(e))

        return cls(
            title=config.title,
            description=config.description,
            priority=config.priority,
            steps=config.steps,
            collection=config.collection,
            track_id=config.track,
        )

    def execute(self) -> CommandResult:
        """Create a new feature."""
        from rich.prompt import Prompt

        from htmlgraph.converter import node_to_dict

        sdk = self.get_sdk()

        # Convert steps count to list of step names
        step_names = None
        if self.steps:
            step_names = [f"Step {i + 1}" for i in range(self.steps)]

        # Determine track_id for feature creation
        track_id = self.track_id

        # Only enforce track selection for main features collection
        if self.collection == "features":
            if not track_id:
                # Get available tracks
                try:
                    tracks = sdk.tracks.all()
                    if not tracks:
                        raise CommandError(
                            "No tracks found. Create a track first:\n"
                            "  uv run htmlgraph track new 'Track Title'"
                        )

                    if len(tracks) == 1:
                        # Auto-select if only one track exists
                        track_id = tracks[0].id
                        console.print(
                            f"[dim]Auto-selected track: {tracks[0].title}[/dim]"
                        )
                    else:
                        # Interactive selection
                        console.print("[bold]Available Tracks:[/bold]")
                        for i, track in enumerate(tracks, 1):
                            console.print(f"  {i}. {track.title} ({track.id})")

                        selection = Prompt.ask(
                            "Select track",
                            choices=[str(i) for i in range(1, len(tracks) + 1)],
                        )
                        track_id = tracks[int(selection) - 1].id
                except Exception as e:
                    raise CommandError(f"Failed to get available tracks: {e}")

            builder = sdk.features.create(
                title=self.title,
                description=self.description or "",
                priority=self.priority,
            )
            if step_names:
                builder.add_steps(step_names)
            if track_id:
                builder.set_track(track_id)
            node = builder.save()
        else:
            node = sdk.session_manager.create_feature(
                title=self.title,
                collection=self.collection,
                description=self.description or "",
                priority=self.priority,
                steps=step_names,
                agent=self.agent,
            )

        # Create Rich table for output
        table = Table(show_header=False, box=None)
        table.add_column(style="bold cyan")
        table.add_column()

        table.add_row("Created:", f"[green]{node.id}[/green]")
        table.add_row("Title:", f"[yellow]{node.title}[/yellow]")
        table.add_row("Status:", f"[blue]{node.status}[/blue]")
        if node.track_id:
            table.add_row("Track:", f"[cyan]{node.track_id}[/cyan]")
        table.add_row(
            "Path:", f"[dim]{self.graph_dir}/{self.collection}/{node.id}.html[/dim]"
        )

        # Return table object directly - TextFormatter will print it properly
        return CommandResult(
            data=table,
            json_data=node_to_dict(node),
        )


class FeatureStartCommand(BaseCommand):
    """Start working on a feature."""

    def __init__(self, *, feature_id: str, collection: str) -> None:
        super().__init__()
        self.feature_id = feature_id
        self.collection = collection

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> FeatureStartCommand:
        return cls(feature_id=args.id, collection=args.collection)

    def execute(self) -> CommandResult:
        """Start working on a feature."""
        from htmlgraph.converter import node_to_dict

        sdk = self.get_sdk()
        collection = getattr(sdk, self.collection, None)
        self.require_collection(collection, self.collection)
        assert collection is not None  # Type narrowing for mypy

        node = collection.start(self.feature_id)
        self.require_node(node, "feature", self.feature_id)

        status = sdk.session_manager.get_status()

        # Create Rich table for output
        table = Table(show_header=False, box=None)
        table.add_column(style="bold cyan")
        table.add_column()

        table.add_row("Started:", f"[green]{node.id}[/green]")
        table.add_row("Title:", f"[yellow]{node.title}[/yellow]")
        table.add_row("Status:", f"[blue]{node.status}[/blue]")
        wip_color = "red" if status["wip_count"] >= status["wip_limit"] else "green"
        table.add_row(
            "WIP:",
            f"[{wip_color}]{status['wip_count']}/{status['wip_limit']}[/{wip_color}]",
        )

        # Return table object directly - TextFormatter will print it properly
        return CommandResult(
            data=table,
            json_data=node_to_dict(node),
        )


class FeatureCompleteCommand(BaseCommand):
    """Mark feature as completed."""

    def __init__(self, *, feature_id: str, collection: str) -> None:
        super().__init__()
        self.feature_id = feature_id
        self.collection = collection

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> FeatureCompleteCommand:
        return cls(feature_id=args.id, collection=args.collection)

    def execute(self) -> CommandResult:
        """Mark feature as completed."""
        from htmlgraph.converter import node_to_dict

        sdk = self.get_sdk()
        collection = getattr(sdk, self.collection, None)
        self.require_collection(collection, self.collection)
        assert collection is not None  # Type narrowing for mypy

        node = collection.complete(self.feature_id)
        self.require_node(node, "feature", self.feature_id)

        # Create Rich panel for output
        panel = Panel(
            f"[bold green]✓ Completed[/bold green]\n"
            f"[cyan]{node.id}[/cyan]\n"
            f"[yellow]{node.title}[/yellow]",
            border_style="green",
        )

        # Return panel object directly - TextFormatter will print it properly
        return CommandResult(
            data=panel,
            json_data=node_to_dict(node),
        )


class FeatureClaimCommand(BaseCommand):
    """Claim a feature."""

    def __init__(self, *, feature_id: str, collection: str) -> None:
        super().__init__()
        self.feature_id = feature_id
        self.collection = collection

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> FeatureClaimCommand:
        return cls(feature_id=args.id, collection=args.collection)

    def execute(self) -> CommandResult:
        """Claim a feature."""
        from htmlgraph.converter import node_to_dict

        sdk = self.get_sdk()
        collection = getattr(sdk, self.collection, None)
        self.require_collection(collection, self.collection)
        assert collection is not None  # Type narrowing for mypy

        try:
            node = collection.claim(self.feature_id)
        except ValueError as e:
            raise CommandError(str(e))

        self.require_node(node, "feature", self.feature_id)

        from htmlgraph.cli.base import TextOutputBuilder

        output = TextOutputBuilder()
        output.add_success(f"Claimed: {node.id}")
        output.add_field("Agent", node.agent_assigned)
        output.add_field("Session", node.claimed_by_session)

        return CommandResult(
            data=node_to_dict(node),
            text=output.build(),
            json_data=node_to_dict(node),
        )


class FeatureReleaseCommand(BaseCommand):
    """Release a feature."""

    def __init__(self, *, feature_id: str, collection: str) -> None:
        super().__init__()
        self.feature_id = feature_id
        self.collection = collection

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> FeatureReleaseCommand:
        return cls(feature_id=args.id, collection=args.collection)

    def execute(self) -> CommandResult:
        """Release a feature."""
        from htmlgraph.converter import node_to_dict

        sdk = self.get_sdk()
        collection = getattr(sdk, self.collection, None)
        self.require_collection(collection, self.collection)
        assert collection is not None  # Type narrowing for mypy

        try:
            node = collection.release(self.feature_id)
        except ValueError as e:
            raise CommandError(str(e))

        self.require_node(node, "feature", self.feature_id)

        from htmlgraph.cli.base import TextOutputBuilder

        output = TextOutputBuilder()
        output.add_success(f"Released: {node.id}")

        return CommandResult(
            data=node_to_dict(node),
            text=output.build(),
            json_data=node_to_dict(node),
        )


class FeaturePrimaryCommand(BaseCommand):
    """Set primary feature."""

    def __init__(self, *, feature_id: str, collection: str) -> None:
        super().__init__()
        self.feature_id = feature_id
        self.collection = collection

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> FeaturePrimaryCommand:
        return cls(feature_id=args.id, collection=args.collection)

    def execute(self) -> CommandResult:
        """Set primary feature."""
        from htmlgraph.converter import node_to_dict

        sdk = self.get_sdk()

        # Only FeatureCollection has set_primary currently
        if self.collection == "features":
            node = sdk.features.set_primary(self.feature_id)
        else:
            # Fallback to direct session manager
            node = sdk.session_manager.set_primary_feature(
                self.feature_id, collection=self.collection, agent=self.agent
            )

        self.require_node(node, "feature", self.feature_id)
        assert node is not None  # Type narrowing for mypy

        from htmlgraph.cli.base import TextOutputBuilder

        output = TextOutputBuilder()
        output.add_success(f"Primary feature set: {node.id}")
        output.add_field("Title", node.title)

        return CommandResult(
            data=node_to_dict(node),
            text=output.build(),
            json_data=node_to_dict(node),
        )
