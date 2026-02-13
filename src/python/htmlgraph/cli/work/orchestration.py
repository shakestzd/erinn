from __future__ import annotations

"""HtmlGraph CLI - Orchestration commands (Orchestrator, Claude)."""


import argparse
from typing import TYPE_CHECKING

from rich.console import Console

from htmlgraph.cli.base import BaseCommand, CommandError, CommandResult
from htmlgraph.cli.constants import DEFAULT_GRAPH_DIR

if TYPE_CHECKING:
    from argparse import _SubParsersAction

console = Console()


def register_orchestrator_commands(subparsers: _SubParsersAction) -> None:
    """Register orchestrator commands."""
    orchestrator_parser = subparsers.add_parser(
        "orchestrator", help="Orchestrator management"
    )
    orchestrator_subparsers = orchestrator_parser.add_subparsers(
        dest="orchestrator_command", help="Orchestrator command"
    )

    # orchestrator enable
    orch_enable = orchestrator_subparsers.add_parser(
        "enable", help="Enable orchestrator mode"
    )
    orch_enable.add_argument(
        "--level",
        "-l",
        choices=["strict", "guidance"],
        default="strict",
        help="Enforcement level (default: strict)",
    )
    orch_enable.add_argument(
        "--graph-dir", "-g", default=DEFAULT_GRAPH_DIR, help="Graph directory"
    )
    orch_enable.set_defaults(func=OrchestratorEnableCommand.from_args)

    # orchestrator disable
    orch_disable = orchestrator_subparsers.add_parser(
        "disable", help="Disable orchestrator mode"
    )
    orch_disable.add_argument(
        "--graph-dir", "-g", default=DEFAULT_GRAPH_DIR, help="Graph directory"
    )
    orch_disable.set_defaults(func=OrchestratorDisableCommand.from_args)

    # orchestrator status
    orch_status = orchestrator_subparsers.add_parser(
        "status", help="Show orchestrator status"
    )
    orch_status.add_argument(
        "--graph-dir", "-g", default=DEFAULT_GRAPH_DIR, help="Graph directory"
    )
    orch_status.add_argument(
        "--format", choices=["json", "text"], default="text", help="Output format"
    )
    orch_status.set_defaults(func=OrchestratorStatusCommand.from_args)

    # orchestrator config show
    config_show = orchestrator_subparsers.add_parser(
        "config-show", help="Show orchestrator configuration"
    )
    config_show.add_argument(
        "--format", choices=["json", "text"], default="text", help="Output format"
    )
    config_show.set_defaults(func=OrchestratorConfigShowCommand.from_args)

    # orchestrator config set
    config_set = orchestrator_subparsers.add_parser(
        "config-set", help="Set a configuration value"
    )
    config_set.add_argument(
        "key", help="Config key (e.g., thresholds.exploration_calls)"
    )
    config_set.add_argument("value", type=int, help="New value")
    config_set.add_argument(
        "--format", choices=["json", "text"], default="text", help="Output format"
    )
    config_set.set_defaults(func=OrchestratorConfigSetCommand.from_args)

    # orchestrator config reset
    config_reset = orchestrator_subparsers.add_parser(
        "config-reset", help="Reset configuration to defaults"
    )
    config_reset.add_argument(
        "--format", choices=["json", "text"], default="text", help="Output format"
    )
    config_reset.set_defaults(func=OrchestratorConfigResetCommand.from_args)

    # orchestrator reset-violations
    reset_violations = orchestrator_subparsers.add_parser(
        "reset-violations", help="Reset violation counter"
    )
    reset_violations.add_argument(
        "--graph-dir", "-g", default=DEFAULT_GRAPH_DIR, help="Graph directory"
    )
    reset_violations.set_defaults(func=OrchestratorResetViolationsCommand.from_args)

    # orchestrator set-level
    set_level = orchestrator_subparsers.add_parser(
        "set-level", help="Set enforcement level"
    )
    set_level.add_argument(
        "level", choices=["strict", "guidance"], help="Enforcement level to set"
    )
    set_level.add_argument(
        "--graph-dir", "-g", default=DEFAULT_GRAPH_DIR, help="Graph directory"
    )
    set_level.set_defaults(func=OrchestratorSetLevelCommand.from_args)


def register_claude_commands(subparsers: _SubParsersAction) -> None:
    """Register Claude Code launcher commands."""
    claude_parser = subparsers.add_parser(
        "claude", help="Launch Claude Code with HtmlGraph integration"
    )
    claude_parser.add_argument(
        "--init",
        action="store_true",
        help="Launch with orchestrator prompt and plugin installation",
    )
    claude_parser.add_argument(
        "--continue",
        dest="continue_session",
        action="store_true",
        help="Resume last session with orchestrator rules",
    )
    claude_parser.add_argument(
        "--dev",
        action="store_true",
        help="Launch with local plugin for development",
    )
    claude_parser.set_defaults(func=ClaudeCommand.from_args)


# ============================================================================
# Orchestrator Commands
# ============================================================================


class OrchestratorEnableCommand(BaseCommand):
    """Enable orchestrator mode."""

    def __init__(self, *, level: str = "strict") -> None:
        super().__init__()
        self.level: str = level

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> OrchestratorEnableCommand:
        return cls(level=getattr(args, "level", "strict"))

    def execute(self) -> CommandResult:
        """Enable orchestrator mode."""

        from htmlgraph.orchestrator_mode import OrchestratorModeManager

        if self.graph_dir is None:
            raise CommandError("Missing graph directory")

        manager = OrchestratorModeManager(self.graph_dir)
        manager.enable(level=self.level)  # type: ignore[arg-type]
        status = manager.status()

        from htmlgraph.cli.base import TextOutputBuilder

        output = TextOutputBuilder()
        if self.level == "strict":
            output.add_success("Orchestrator mode enabled (strict enforcement)")
        else:
            output.add_success("Orchestrator mode enabled (guidance mode)")
        output.add_field("Level", self.level)
        if status.get("activated_at"):
            output.add_field("Activated at", status["activated_at"])

        return CommandResult(
            text=output.build(),
            json_data=status,
        )


class OrchestratorDisableCommand(BaseCommand):
    """Disable orchestrator mode."""

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> OrchestratorDisableCommand:
        return cls()

    def execute(self) -> CommandResult:
        """Disable orchestrator mode."""
        from htmlgraph.orchestrator_mode import OrchestratorModeManager

        if self.graph_dir is None:
            raise CommandError("Missing graph directory")

        manager = OrchestratorModeManager(self.graph_dir)
        manager.disable(by_user=True)
        status = manager.status()

        from htmlgraph.cli.base import TextOutputBuilder

        output = TextOutputBuilder()
        output.add_success("Orchestrator mode disabled")
        output.add_field("Status", "Disabled by user (auto-activation prevented)")

        return CommandResult(
            text=output.build(),
            json_data=status,
        )


class OrchestratorStatusCommand(BaseCommand):
    """Show orchestrator status."""

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> OrchestratorStatusCommand:
        return cls()

    def execute(self) -> CommandResult:
        """Show orchestrator status."""
        from htmlgraph.orchestrator_mode import OrchestratorModeManager

        if self.graph_dir is None:
            raise CommandError("Missing graph directory")

        manager = OrchestratorModeManager(self.graph_dir)
        mode = manager.load()
        status = manager.status()

        from htmlgraph.cli.base import TextOutputBuilder

        output = TextOutputBuilder()
        if status.get("enabled"):
            if status.get("enforcement_level") == "strict":
                output.add_line("Orchestrator mode: enabled (strict enforcement)")
            else:
                output.add_line("Orchestrator mode: enabled (guidance mode)")
        else:
            output.add_line("Orchestrator mode: disabled")
            if mode.disabled_by_user:
                output.add_field(
                    "Status", "Disabled by user (auto-activation prevented)"
                )

        if status.get("activated_at"):
            output.add_field("Activated at", status["activated_at"])
        if status.get("violations") is not None:
            output.add_field("Violations", f"{status['violations']}/3")
            if status.get("circuit_breaker_triggered"):
                output.add_field("Circuit breaker", "TRIGGERED")

        return CommandResult(
            data=status,
            text=output.build(),
            json_data=status,
        )


class OrchestratorConfigShowCommand(BaseCommand):
    """Show orchestrator configuration."""

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> OrchestratorConfigShowCommand:
        return cls()

    def execute(self) -> CommandResult:
        """Show orchestrator configuration."""
        from htmlgraph.orchestrator_config import (
            format_config_display,
            load_orchestrator_config,
        )

        config = load_orchestrator_config()
        text_output = format_config_display(config)

        return CommandResult(
            text=text_output,
            json_data=config.model_dump(),
        )


class OrchestratorConfigSetCommand(BaseCommand):
    """Set a configuration value."""

    def __init__(self, *, key: str, value: int) -> None:
        super().__init__()
        self.key = key
        self.value = value

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> OrchestratorConfigSetCommand:
        return cls(key=args.key, value=args.value)

    def execute(self) -> CommandResult:
        """Set a configuration value."""
        from htmlgraph.orchestrator_config import (
            get_config_paths,
            load_orchestrator_config,
            save_orchestrator_config,
            set_config_value,
        )

        # Load current config
        config = load_orchestrator_config()

        try:
            # Set the value
            set_config_value(config, self.key, self.value)

            # Save to first config path (project-specific)
            config_path = get_config_paths()[0]
            save_orchestrator_config(config, config_path)

            from htmlgraph.cli.base import TextOutputBuilder

            output = TextOutputBuilder()
            output.add_success(f"Configuration updated: {self.key} = {self.value}")
            output.add_field("Config file", str(config_path))

            return CommandResult(
                text=output.build(),
                json_data={
                    "key": self.key,
                    "value": self.value,
                    "path": str(config_path),
                },
            )
        except KeyError as e:
            raise CommandError(f"Invalid config key: {e}")


class OrchestratorConfigResetCommand(BaseCommand):
    """Reset configuration to defaults."""

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> OrchestratorConfigResetCommand:
        return cls()

    def execute(self) -> CommandResult:
        """Reset configuration to defaults."""
        from htmlgraph.orchestrator_config import (
            OrchestratorConfig,
            get_config_paths,
            save_orchestrator_config,
        )

        # Create default config
        config = OrchestratorConfig()

        # Save to first config path (project-specific)
        config_path = get_config_paths()[0]
        save_orchestrator_config(config, config_path)

        from htmlgraph.cli.base import TextOutputBuilder

        output = TextOutputBuilder()
        output.add_success("Configuration reset to defaults")
        output.add_field("Config file", str(config_path))
        output.add_field("Exploration calls", config.thresholds.exploration_calls)
        output.add_field(
            "Circuit breaker", config.thresholds.circuit_breaker_violations
        )
        output.add_field(
            "Violation decay", f"{config.thresholds.violation_decay_seconds}s"
        )

        return CommandResult(
            text=output.build(),
            json_data=config.model_dump(),
        )


class OrchestratorResetViolationsCommand(BaseCommand):
    """Reset violation counter."""

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> OrchestratorResetViolationsCommand:
        return cls()

    def execute(self) -> CommandResult:
        """Reset violation counter."""
        from htmlgraph.orchestrator_mode import OrchestratorModeManager

        if self.graph_dir is None:
            raise CommandError("Missing graph directory")

        manager = OrchestratorModeManager(self.graph_dir)

        if not manager.status().get("enabled"):
            console.print("[yellow]Orchestrator mode is not enabled[/yellow]")
            return CommandResult(
                text="Orchestrator mode is not enabled",
                json_data={"success": False, "message": "not enabled"},
            )

        manager.reset_violations()
        status = manager.status()

        from htmlgraph.cli.base import TextOutputBuilder

        output = TextOutputBuilder()
        output.add_success("Violations reset")
        output.add_field("Violation count", status.get("violations", 0))
        output.add_field(
            "Circuit breaker",
            "Normal" if not status.get("circuit_breaker_triggered") else "TRIGGERED",
        )

        return CommandResult(
            text=output.build(),
            json_data=status,
        )


class OrchestratorSetLevelCommand(BaseCommand):
    """Set enforcement level."""

    def __init__(self, *, level: str) -> None:
        super().__init__()
        self.level: str = level

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> OrchestratorSetLevelCommand:
        return cls(level=args.level)

    def execute(self) -> CommandResult:
        """Set enforcement level."""
        from htmlgraph.orchestrator_mode import OrchestratorModeManager

        if self.graph_dir is None:
            raise CommandError("Missing graph directory")

        manager = OrchestratorModeManager(self.graph_dir)
        manager.set_level(self.level)  # type: ignore[arg-type]
        status = manager.status()

        from htmlgraph.cli.base import TextOutputBuilder

        output = TextOutputBuilder()
        output.add_success(f"Enforcement level changed to '{self.level}'")
        if self.level == "strict":
            output.add_field("Mode", "Strict enforcement")
        else:
            output.add_field("Mode", "Guidance mode")

        return CommandResult(
            text=output.build(),
            json_data=status,
        )


# ============================================================================
# Claude Code Launcher Commands
# ============================================================================


class ClaudeCommand(BaseCommand):
    """Launch Claude Code with HtmlGraph integration."""

    def __init__(
        self,
        *,
        init: bool,
        continue_session: bool,
        dev: bool,
        quiet: bool,
        format: str,
    ) -> None:
        super().__init__()
        self.init = init
        self.continue_session = continue_session
        self.dev = dev
        self.quiet = quiet
        self.format = format

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> ClaudeCommand:
        return cls(
            init=getattr(args, "init", False),
            continue_session=getattr(args, "continue_session", False),
            dev=getattr(args, "dev", False),
            quiet=getattr(args, "quiet", False),
            format=getattr(args, "format", "text"),
        )

    def execute(self) -> CommandResult:
        """Launch Claude Code."""
        from htmlgraph.orchestration.claude_launcher import ClaudeLauncher

        # Create args namespace for launcher
        launcher_args = argparse.Namespace(
            init=self.init,
            continue_session=self.continue_session,
            dev=self.dev,
            quiet=self.quiet,
            format=self.format,
        )

        # Launch Claude Code
        launcher = ClaudeLauncher(launcher_args)
        launcher.launch()

        # This won't be reached because launcher.launch() calls subprocess
        return CommandResult(text="Claude Code launched")
