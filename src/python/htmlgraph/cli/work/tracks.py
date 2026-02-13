from __future__ import annotations

"""HtmlGraph CLI - Track management commands."""


import argparse
from pathlib import Path
from typing import TYPE_CHECKING, Literal, cast

from htmlgraph.cli.base import BaseCommand, CommandError, CommandResult
from htmlgraph.cli.constants import DEFAULT_GRAPH_DIR

if TYPE_CHECKING:
    from argparse import _SubParsersAction


def register_track_commands(subparsers: _SubParsersAction) -> None:
    """Register track management commands."""
    track_parser = subparsers.add_parser("track", help="Track management")
    track_subparsers = track_parser.add_subparsers(
        dest="track_command", help="Track command"
    )

    # track new
    track_new = track_subparsers.add_parser("new", help="Create a new track")
    track_new.add_argument("title", help="Track title")
    track_new.add_argument("--description", help="Track description")
    track_new.add_argument(
        "--priority", choices=["low", "medium", "high"], default="medium"
    )
    track_new.add_argument(
        "--graph-dir", "-g", default=DEFAULT_GRAPH_DIR, help="Graph directory"
    )
    track_new.add_argument(
        "--format", choices=["json", "text"], default="text", help="Output format"
    )
    track_new.set_defaults(func=TrackNewCommand.from_args)

    # track list
    track_list = track_subparsers.add_parser("list", help="List all tracks")
    track_list.add_argument(
        "--graph-dir", "-g", default=DEFAULT_GRAPH_DIR, help="Graph directory"
    )
    track_list.add_argument(
        "--format", choices=["json", "text"], default="text", help="Output format"
    )
    track_list.set_defaults(func=TrackListCommand.from_args)

    # track spec
    track_spec = track_subparsers.add_parser("spec", help="Create track spec")
    track_spec.add_argument("track_id", help="Track ID")
    track_spec.add_argument("title", help="Spec title")
    track_spec.add_argument("--overview", help="Spec overview")
    track_spec.add_argument("--context", help="Spec context")
    track_spec.add_argument("--author", help="Spec author")
    track_spec.add_argument(
        "--graph-dir", "-g", default=DEFAULT_GRAPH_DIR, help="Graph directory"
    )
    track_spec.add_argument(
        "--format", choices=["json", "text"], default="text", help="Output format"
    )
    track_spec.set_defaults(func=TrackSpecCommand.from_args)

    # track plan
    track_plan = track_subparsers.add_parser("plan", help="Create track plan")
    track_plan.add_argument("track_id", help="Track ID")
    track_plan.add_argument("title", help="Plan title")
    track_plan.add_argument(
        "--graph-dir", "-g", default=DEFAULT_GRAPH_DIR, help="Graph directory"
    )
    track_plan.add_argument(
        "--format", choices=["json", "text"], default="text", help="Output format"
    )
    track_plan.set_defaults(func=TrackPlanCommand.from_args)

    # track delete
    track_delete = track_subparsers.add_parser("delete", help="Delete a track")
    track_delete.add_argument("track_id", help="Track ID")
    track_delete.add_argument(
        "--graph-dir", "-g", default=DEFAULT_GRAPH_DIR, help="Graph directory"
    )
    track_delete.add_argument(
        "--format", choices=["json", "text"], default="text", help="Output format"
    )
    track_delete.set_defaults(func=TrackDeleteCommand.from_args)


# ============================================================================
# Track Commands
# ============================================================================


class TrackNewCommand(BaseCommand):
    """Create a new track."""

    def __init__(
        self,
        *,
        title: str,
        description: str | None,
        priority: str,
    ) -> None:
        super().__init__()
        self.title = title
        self.description = description
        self.priority = priority

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> TrackNewCommand:
        from pydantic import ValidationError

        from htmlgraph.cli.models import TrackNewConfig, format_validation_error

        try:
            # Validate through Pydantic model
            config = TrackNewConfig(
                title=args.title,
                description=args.description,
                priority=args.priority,
            )
        except ValidationError as e:
            raise CommandError(format_validation_error(e))

        return cls(
            title=config.title,
            description=config.description,
            priority=config.priority,
        )

    def execute(self) -> CommandResult:
        """Create a new track."""
        from htmlgraph.track_manager import TrackManager

        if self.graph_dir is None:
            raise CommandError("Missing graph directory")

        manager = TrackManager(self.graph_dir)

        # Type cast priority to expected literal type
        priority_typed = cast(
            Literal["low", "medium", "high", "critical"],
            self.priority,
        )

        try:
            track = manager.create_track(
                title=self.title,
                description=self.description or "",
                priority=priority_typed,
            )
        except ValueError as e:
            raise CommandError(str(e))

        from htmlgraph.cli.base import TextOutputBuilder

        output = TextOutputBuilder()
        output.add_success(f"Created track: {track.id}")
        output.add_field("Title", track.title)
        output.add_field("Status", track.status)
        output.add_field("Priority", track.priority)
        output.add_field("Path", f"{self.graph_dir}/tracks/{track.id}/")
        output.add_blank()
        output.add_line("Next steps:")
        output.add_field(
            "- Create spec", f"htmlgraph track spec {track.id} 'Spec Title'"
        )
        output.add_field(
            "- Create plan", f"htmlgraph track plan {track.id} 'Plan Title'"
        )

        json_data = {
            "id": track.id,
            "title": track.title,
            "status": track.status,
            "priority": track.priority,
            "path": f"{self.graph_dir}/tracks/{track.id}/",
        }

        return CommandResult(
            data=track,
            text=output.build(),
            json_data=json_data,
        )


class TrackListCommand(BaseCommand):
    """List all tracks."""

    def __init__(
        self,
        *,
        status: str | None = None,
        priority: str | None = None,
        has_spec: bool | None = None,
        has_plan: bool | None = None,
    ) -> None:
        super().__init__()
        self.status = status
        self.priority = priority
        self.has_spec = has_spec
        self.has_plan = has_plan

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> TrackListCommand:
        # Validate inputs using TrackFilter model
        from pydantic import ValidationError

        from htmlgraph.cli.models import TrackFilter, format_validation_error

        # Get optional filter arguments
        status = getattr(args, "status", None)
        priority = getattr(args, "priority", None)
        has_spec = getattr(args, "has_spec", None)
        has_plan = getattr(args, "has_plan", None)

        try:
            filter_model = TrackFilter(
                status=status, priority=priority, has_spec=has_spec, has_plan=has_plan
            )
        except ValidationError as e:
            raise CommandError(format_validation_error(e))

        return cls(
            status=filter_model.status,
            priority=filter_model.priority,
            has_spec=filter_model.has_spec,
            has_plan=filter_model.has_plan,
        )

    def execute(self) -> CommandResult:
        """List all tracks."""
        from htmlgraph.track_manager import TrackManager

        if self.graph_dir is None:
            raise CommandError("Missing graph directory")

        manager = TrackManager(self.graph_dir)
        track_ids = manager.list_tracks()

        if not track_ids:
            from htmlgraph.cli.base import TextOutputBuilder

            output = TextOutputBuilder()
            output.add_warning("No tracks found.")
            output.add_blank()
            output.add_dim("Create a track with: htmlgraph track new 'Track Title'")

            return CommandResult(
                text=output.build(),
                json_data={"tracks": []},
            )

        # Create Rich table
        from htmlgraph.cli.base import TableBuilder

        builder = TableBuilder.create_list_table(f"Tracks in {self.graph_dir}/tracks/")
        builder.add_id_column("Track ID", no_wrap=True)
        builder.add_column("Components", style="green")
        builder.add_column("Format", style="blue")

        # Convert to display models for type-safe filtering
        from htmlgraph.cli.models import TrackDisplay

        display_tracks = []

        for track_id in track_ids:
            # Check for both consolidated and directory-based formats
            track_file = Path(self.graph_dir) / "tracks" / f"{track_id}.html"
            track_dir = Path(self.graph_dir) / "tracks" / track_id

            if track_file.exists():
                # Consolidated format
                content = track_file.read_text(encoding="utf-8")
                has_spec = (
                    'data-section="overview"' in content
                    or 'data-section="requirements"' in content
                )
                has_plan = 'data-section="plan"' in content
                format_type = "consolidated"
            else:
                # Directory format
                has_spec = (track_dir / "spec.html").exists()
                has_plan = (track_dir / "plan.html").exists()
                format_type = "directory"

            # Create display model
            track_display = TrackDisplay.from_track_id(
                track_id=track_id,
                has_spec=has_spec,
                has_plan=has_plan,
                format_type=format_type,
            )

            # Apply filters
            if self.has_spec is not None and track_display.has_spec != self.has_spec:
                continue
            if self.has_plan is not None and track_display.has_plan != self.has_plan:
                continue

            display_tracks.append(track_display)

        for track in display_tracks:
            builder.add_row(track.id, track.components_str, track.format_type)

        # Return table object directly - TextFormatter will print it properly
        return CommandResult(
            data=builder.table,
            json_data={"tracks": track_ids},
        )


class TrackSpecCommand(BaseCommand):
    """Create track spec."""

    def __init__(
        self,
        *,
        track_id: str,
        title: str,
        overview: str | None,
        context: str | None,
        author: str | None,
    ) -> None:
        super().__init__()
        self.track_id = track_id
        self.title = title
        self.overview = overview
        self.context = context
        self.author = author

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> TrackSpecCommand:
        return cls(
            track_id=args.track_id,
            title=args.title,
            overview=args.overview,
            context=args.context,
            author=args.author,
        )

    def execute(self) -> CommandResult:
        """Create track spec."""
        from htmlgraph.track_manager import TrackManager

        if self.graph_dir is None:
            raise CommandError("Missing graph directory")

        manager = TrackManager(self.graph_dir)

        # Check if track uses consolidated format
        if manager.is_consolidated(self.track_id):
            track_file = manager.tracks_dir / f"{self.track_id}.html"
            msg = [
                f"Track '{self.track_id}' uses consolidated single-file format.",
                f"Spec is embedded in: {track_file}",
                "\nTo create a track with separate spec/plan files, use:",
                '  sdk.tracks.builder().separate_files().title("...").create()',
            ]
            return CommandResult(text="\n".join(msg))

        try:
            spec = manager.create_spec(
                track_id=self.track_id,
                title=self.title,
                overview=self.overview or "",
                context=self.context or "",
                author=self.author or "",
            )
        except (ValueError, FileNotFoundError) as e:
            raise CommandError(str(e))

        from htmlgraph.cli.base import TextOutputBuilder

        output = TextOutputBuilder()
        output.add_success(f"Created spec: {spec.id}")
        output.add_field("Title", spec.title)
        output.add_field("Track", spec.track_id)
        output.add_field("Status", spec.status)
        output.add_field("Path", f"{self.graph_dir}/tracks/{self.track_id}/spec.html")
        output.add_blank()
        output.add_line(
            f"View spec: open {self.graph_dir}/tracks/{self.track_id}/spec.html"
        )

        json_data = {
            "id": spec.id,
            "title": spec.title,
            "track_id": spec.track_id,
            "status": spec.status,
            "path": f"{self.graph_dir}/tracks/{self.track_id}/spec.html",
        }

        return CommandResult(
            data=spec,
            text=output.build(),
            json_data=json_data,
        )


class TrackPlanCommand(BaseCommand):
    """Create track plan."""

    def __init__(self, *, track_id: str, title: str) -> None:
        super().__init__()
        self.track_id = track_id
        self.title = title

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> TrackPlanCommand:
        return cls(track_id=args.track_id, title=args.title)

    def execute(self) -> CommandResult:
        """Create track plan."""
        from htmlgraph.track_manager import TrackManager

        if self.graph_dir is None:
            raise CommandError("Missing graph directory")

        manager = TrackManager(self.graph_dir)

        # Check if track uses consolidated format
        if manager.is_consolidated(self.track_id):
            track_file = manager.tracks_dir / f"{self.track_id}.html"
            msg = [
                f"Track '{self.track_id}' uses consolidated single-file format.",
                f"Plan is embedded in: {track_file}",
                "\nTo create a track with separate spec/plan files, use:",
                '  sdk.tracks.builder().separate_files().title("...").create()',
            ]
            return CommandResult(text="\n".join(msg))

        try:
            plan = manager.create_plan(
                track_id=self.track_id,
                title=self.title,
            )
        except (ValueError, FileNotFoundError) as e:
            raise CommandError(str(e))

        from htmlgraph.cli.base import TextOutputBuilder

        output = TextOutputBuilder()
        output.add_success(f"Created plan: {plan.id}")
        output.add_field("Title", plan.title)
        output.add_field("Track", plan.track_id)
        output.add_field("Status", plan.status)
        output.add_field("Path", f"{self.graph_dir}/tracks/{self.track_id}/plan.html")
        output.add_blank()
        output.add_line(
            f"View plan: open {self.graph_dir}/tracks/{self.track_id}/plan.html"
        )

        json_data = {
            "id": plan.id,
            "title": plan.title,
            "track_id": plan.track_id,
            "status": plan.status,
            "path": f"{self.graph_dir}/tracks/{self.track_id}/plan.html",
        }

        return CommandResult(
            data=plan,
            text=output.build(),
            json_data=json_data,
        )


class TrackDeleteCommand(BaseCommand):
    """Delete a track."""

    def __init__(self, *, track_id: str) -> None:
        super().__init__()
        self.track_id = track_id

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> TrackDeleteCommand:
        return cls(track_id=args.track_id)

    def execute(self) -> CommandResult:
        """Delete a track."""
        from htmlgraph.track_manager import TrackManager

        if self.graph_dir is None:
            raise CommandError("Missing graph directory")

        manager = TrackManager(self.graph_dir)

        try:
            manager.delete_track(self.track_id)
        except ValueError as e:
            raise CommandError(str(e))

        from htmlgraph.cli.base import TextOutputBuilder

        output = TextOutputBuilder()
        output.add_success(f"Deleted track: {self.track_id}")
        output.add_field("Removed", f"{self.graph_dir}/tracks/{self.track_id}/")

        json_data = {"deleted": True, "track_id": self.track_id}

        return CommandResult(
            text=output.build(),
            json_data=json_data,
        )
