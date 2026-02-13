from __future__ import annotations

"""HtmlGraph CLI - Work management commands.

Commands for managing work items:
- Features: Work item tracking
- Sessions: Session management
- Tracks: Multi-feature planning
- Orchestrator: Claude Code integration
- Other work-related operations
"""


from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from argparse import _SubParsersAction


def register_commands(subparsers: _SubParsersAction) -> None:
    """Register work management commands with the argument parser.

    Args:
        subparsers: Subparser action from ArgumentParser.add_subparsers()
    """
    from htmlgraph.cli.work.activity import register_activity_commands
    from htmlgraph.cli.work.browse import BrowseCommand
    from htmlgraph.cli.work.features import register_feature_commands
    from htmlgraph.cli.work.orchestration import (
        register_claude_commands,
        register_orchestrator_commands,
    )
    from htmlgraph.cli.work.report import register_report_commands
    from htmlgraph.cli.work.sessions import register_session_commands
    from htmlgraph.cli.work.snapshot import SnapshotCommand
    from htmlgraph.cli.work.tracks import register_track_commands

    # Register all command groups
    register_activity_commands(subparsers)
    register_session_commands(subparsers)
    register_feature_commands(subparsers)
    register_track_commands(subparsers)
    register_orchestrator_commands(subparsers)
    register_claude_commands(subparsers)
    register_report_commands(subparsers)

    # Snapshot command
    snapshot_parser = subparsers.add_parser(
        "snapshot",
        help="Output current graph state with refs",
    )
    snapshot_parser.add_argument(
        "--output-format",
        choices=["refs", "json", "text"],
        default="refs",
        help="Output format (default: refs)",
    )
    snapshot_parser.add_argument(
        "--type",
        help="Filter by type (feature, track, bug, spike, chore, epic, all)",
    )
    snapshot_parser.add_argument(
        "--status",
        help="Filter by status (todo, in_progress, blocked, done, all)",
    )
    snapshot_parser.add_argument(
        "--track",
        help="Show only items in a specific track (by track ID or ref)",
    )
    snapshot_parser.add_argument(
        "--active",
        action="store_true",
        help="Show only TODO/IN_PROGRESS items (filters out metadata spikes)",
    )
    snapshot_parser.add_argument(
        "--blockers",
        action="store_true",
        help="Show only critical/blocked items",
    )
    snapshot_parser.add_argument(
        "--summary",
        action="store_true",
        help="Show counts and progress summary instead of listing all items",
    )
    snapshot_parser.add_argument(
        "--my-work",
        action="store_true",
        help="Show items assigned to current agent",
    )
    snapshot_parser.set_defaults(func=SnapshotCommand.from_args)

    # Browse command
    browse_parser = subparsers.add_parser(
        "browse",
        help="Open dashboard in browser",
    )
    browse_parser.add_argument(
        "--port",
        type=int,
        default=8080,
        help="Server port (default: 8080)",
    )
    browse_parser.add_argument(
        "--query-type",
        help="Filter by type (feature, track, bug, spike, chore, epic)",
    )
    browse_parser.add_argument(
        "--query-status",
        help="Filter by status (todo, in_progress, blocked, done)",
    )
    browse_parser.set_defaults(func=BrowseCommand.from_args)


# Re-export all command classes for backward compatibility
from htmlgraph.cli.work.browse import BrowseCommand
from htmlgraph.cli.work.features import (
    FeatureClaimCommand,
    FeatureCompleteCommand,
    FeatureCreateCommand,
    FeatureListCommand,
    FeaturePrimaryCommand,
    FeatureReleaseCommand,
    FeatureStartCommand,
)
from htmlgraph.cli.work.orchestration import (
    ClaudeCommand,
    OrchestratorDisableCommand,
    OrchestratorEnableCommand,
    OrchestratorResetViolationsCommand,
    OrchestratorSetLevelCommand,
    OrchestratorStatusCommand,
)
from htmlgraph.cli.work.report import SessionReportCommand
from htmlgraph.cli.work.sessions import (
    SessionEndCommand,
    SessionHandoffCommand,
    SessionListCommand,
    SessionStartCommand,
    SessionStartInfoCommand,
)
from htmlgraph.cli.work.snapshot import SnapshotCommand
from htmlgraph.cli.work.tracks import (
    TrackDeleteCommand,
    TrackListCommand,
    TrackNewCommand,
    TrackPlanCommand,
    TrackSpecCommand,
)

__all__ = [
    "register_commands",
    # Session commands
    "SessionStartCommand",
    "SessionEndCommand",
    "SessionListCommand",
    "SessionHandoffCommand",
    "SessionStartInfoCommand",
    # Report commands
    "SessionReportCommand",
    # Snapshot commands
    "SnapshotCommand",
    # Browse commands
    "BrowseCommand",
    # Feature commands
    "FeatureListCommand",
    "FeatureCreateCommand",
    "FeatureStartCommand",
    "FeatureCompleteCommand",
    "FeatureClaimCommand",
    "FeatureReleaseCommand",
    "FeaturePrimaryCommand",
    # Track commands
    "TrackNewCommand",
    "TrackListCommand",
    "TrackSpecCommand",
    "TrackPlanCommand",
    "TrackDeleteCommand",
    # Orchestration commands
    "OrchestratorStatusCommand",
    "OrchestratorEnableCommand",
    "OrchestratorDisableCommand",
    "OrchestratorResetViolationsCommand",
    "OrchestratorSetLevelCommand",
    "ClaudeCommand",
]


# Convenience functions for backward compatibility with tests
def cmd_orchestrator_reset_violations(args: object) -> None:
    """Reset violations command."""
    from argparse import Namespace

    if isinstance(args, Namespace):
        cmd = OrchestratorResetViolationsCommand.from_args(args)
        cmd.graph_dir = (
            str(args.graph_dir) if hasattr(args, "graph_dir") else ".htmlgraph"
        )
        result = cmd.execute()
        from htmlgraph.cli.base import TextFormatter

        formatter = TextFormatter()
        formatter.output(result)


def cmd_orchestrator_set_level(args: object) -> None:
    """Set level command."""
    from argparse import Namespace

    if isinstance(args, Namespace):
        cmd = OrchestratorSetLevelCommand.from_args(args)
        cmd.graph_dir = (
            str(args.graph_dir) if hasattr(args, "graph_dir") else ".htmlgraph"
        )
        result = cmd.execute()
        from htmlgraph.cli.base import TextFormatter

        formatter = TextFormatter()
        formatter.output(result)


def cmd_orchestrator_status(args: object) -> None:
    """Status command."""
    from argparse import Namespace

    if isinstance(args, Namespace):
        cmd = OrchestratorStatusCommand.from_args(args)
        cmd.graph_dir = (
            str(args.graph_dir) if hasattr(args, "graph_dir") else ".htmlgraph"
        )
        result = cmd.execute()
        from htmlgraph.cli.base import TextFormatter

        formatter = TextFormatter()
        formatter.output(result)
