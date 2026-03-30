"""Tests for CLI Pydantic argument models.

Covers:
- FeatureFilter: status/priority/quiet validation
- FeatureCreateArgs: title/priority/steps validation
- FeatureIdArgs: feature_id / collection validation
- OrchestratorEnableArgs: level validation
- OrchestratorDisableArgs: for_next_task / minutes validation
- format_validation_error: human-readable error formatting
- validate_args: Namespace-to-model conversion helper
- Backward compat: commands without models still work via BaseCommand
"""

from __future__ import annotations

import argparse

import pytest
from htmlgraph.cli.models import (
    FeatureCreateArgs,
    FeatureFilter,
    FeatureIdArgs,
    OrchestratorDisableArgs,
    OrchestratorEnableArgs,
    StatusArgs,
    format_validation_error,
    validate_args,
)
from pydantic import ValidationError

# ============================================================================
# FeatureFilter
# ============================================================================


class TestFeatureFilter:
    def test_defaults(self) -> None:
        f = FeatureFilter()
        assert f.status is None
        assert f.quiet is False

    def test_valid_status(self) -> None:
        for status in ["todo", "in_progress", "completed", "blocked", "all"]:
            f = FeatureFilter(status=status)
            assert f.status == status

    def test_invalid_status_raises(self) -> None:
        with pytest.raises(ValidationError):
            FeatureFilter(status="unknown_status")

    def test_valid_priority(self) -> None:
        for priority in ["high", "medium", "low", "critical", "all"]:
            f = FeatureFilter(priority=priority)
            assert f.priority == priority

    def test_invalid_priority_raises(self) -> None:
        with pytest.raises(ValidationError):
            FeatureFilter(priority="extreme")

    def test_quiet_flag(self) -> None:
        f = FeatureFilter(quiet=True)
        assert f.quiet is True

    def test_none_status_allowed(self) -> None:
        f = FeatureFilter(status=None)
        assert f.status is None


# ============================================================================
# FeatureCreateArgs
# ============================================================================


class TestFeatureCreateArgs:
    def test_minimal_valid(self) -> None:
        a = FeatureCreateArgs(title="My Feature")
        assert a.title == "My Feature"
        assert a.priority == "medium"
        assert a.steps is None
        assert a.collection == "features"
        assert a.agent == "claude-code"

    def test_strips_whitespace_from_title(self) -> None:
        a = FeatureCreateArgs(title="  My Feature  ")
        assert a.title == "My Feature"

    def test_empty_title_raises(self) -> None:
        with pytest.raises(ValidationError):
            FeatureCreateArgs(title="")

    def test_whitespace_only_title_raises(self) -> None:
        with pytest.raises(ValidationError):
            FeatureCreateArgs(title="   ")

    def test_valid_priorities(self) -> None:
        for p in ["low", "medium", "high", "critical"]:
            a = FeatureCreateArgs(title="T", priority=p)
            assert a.priority == p

    def test_invalid_priority_raises(self) -> None:
        with pytest.raises(ValidationError):
            FeatureCreateArgs(title="T", priority="extreme")

    def test_steps_must_be_positive(self) -> None:
        with pytest.raises(ValidationError):
            FeatureCreateArgs(title="T", steps=0)

    def test_steps_max_100(self) -> None:
        with pytest.raises(ValidationError):
            FeatureCreateArgs(title="T", steps=101)

    def test_steps_valid(self) -> None:
        a = FeatureCreateArgs(title="T", steps=5)
        assert a.steps == 5

    def test_track_id_optional(self) -> None:
        a = FeatureCreateArgs(title="T", track_id="trk-123")
        assert a.track_id == "trk-123"

    def test_description_optional(self) -> None:
        a = FeatureCreateArgs(title="T", description="Some desc")
        assert a.description == "Some desc"


# ============================================================================
# FeatureIdArgs
# ============================================================================


class TestFeatureIdArgs:
    def test_valid_id(self) -> None:
        a = FeatureIdArgs(feature_id="feat-abc123")
        assert a.feature_id == "feat-abc123"
        assert a.collection == "features"

    def test_strips_whitespace_from_id(self) -> None:
        a = FeatureIdArgs(feature_id="  feat-abc  ")
        assert a.feature_id == "feat-abc"

    def test_empty_id_raises(self) -> None:
        with pytest.raises(ValidationError):
            FeatureIdArgs(feature_id="")

    def test_whitespace_only_id_raises(self) -> None:
        with pytest.raises(ValidationError):
            FeatureIdArgs(feature_id="   ")

    def test_custom_collection(self) -> None:
        a = FeatureIdArgs(feature_id="feat-x", collection="bugs")
        assert a.collection == "bugs"


# ============================================================================
# OrchestratorEnableArgs
# ============================================================================


class TestOrchestratorEnableArgs:
    def test_default_level_is_strict(self) -> None:
        a = OrchestratorEnableArgs()
        assert a.level == "strict"

    def test_strict_level(self) -> None:
        a = OrchestratorEnableArgs(level="strict")
        assert a.level == "strict"

    def test_guidance_level(self) -> None:
        a = OrchestratorEnableArgs(level="guidance")
        assert a.level == "guidance"

    def test_invalid_level_raises(self) -> None:
        with pytest.raises(ValidationError):
            OrchestratorEnableArgs(level="moderate")


# ============================================================================
# OrchestratorDisableArgs
# ============================================================================


class TestOrchestratorDisableArgs:
    def test_defaults(self) -> None:
        a = OrchestratorDisableArgs()
        assert a.for_next_task is False
        assert a.minutes is None

    def test_for_next_task(self) -> None:
        a = OrchestratorDisableArgs(for_next_task=True)
        assert a.for_next_task is True

    def test_minutes_valid(self) -> None:
        a = OrchestratorDisableArgs(minutes=30)
        assert a.minutes == 30

    def test_minutes_must_be_positive(self) -> None:
        with pytest.raises(ValidationError):
            OrchestratorDisableArgs(minutes=0)

    def test_minutes_negative_raises(self) -> None:
        with pytest.raises(ValidationError):
            OrchestratorDisableArgs(minutes=-5)


# ============================================================================
# StatusArgs
# ============================================================================


class TestStatusArgs:
    def test_defaults(self) -> None:
        a = StatusArgs()
        assert a.format == "text"
        assert a.verbose is False
        assert a.graph_dir == ".htmlgraph"

    def test_valid_formats(self) -> None:
        for fmt in ["text", "json", "html"]:
            a = StatusArgs(format=fmt)
            assert a.format == fmt

    def test_invalid_format_raises(self) -> None:
        with pytest.raises(ValidationError):
            StatusArgs(format="xml")


# ============================================================================
# format_validation_error
# ============================================================================


class TestFormatValidationError:
    def test_formats_single_error(self) -> None:
        with pytest.raises(ValidationError) as exc_info:
            FeatureCreateArgs(title="")
        msg = format_validation_error(exc_info.value)
        assert "Validation error:" in msg
        assert "title" in msg

    def test_formats_multiple_errors(self) -> None:
        with pytest.raises(ValidationError) as exc_info:
            OrchestratorEnableArgs(level="bad")  # type: ignore[arg-type]
        msg = format_validation_error(exc_info.value)
        assert "Validation error:" in msg
        assert "level" in msg

    def test_uses_bullet_format(self) -> None:
        with pytest.raises(ValidationError) as exc_info:
            FeatureCreateArgs(title="")
        msg = format_validation_error(exc_info.value)
        assert "•" in msg


# ============================================================================
# validate_args helper
# ============================================================================


class TestValidateArgs:
    def test_converts_namespace_to_model(self) -> None:
        ns = argparse.Namespace(format="json", verbose=True, graph_dir=".htmlgraph")
        result = validate_args(StatusArgs, ns)
        assert result.format == "json"
        assert result.verbose is True

    def test_filters_routing_fields(self) -> None:
        ns = argparse.Namespace(
            format="text",
            verbose=False,
            graph_dir=".htmlgraph",
            command="status",
            func=lambda x: x,
        )
        # Should not raise even though 'command' and 'func' are not in StatusArgs
        result = validate_args(StatusArgs, ns)
        assert result.format == "text"

    def test_accepts_dict_input(self) -> None:
        result = validate_args(OrchestratorEnableArgs, {"level": "guidance"})
        assert result.level == "guidance"

    def test_invalid_dict_raises_validation_error(self) -> None:
        with pytest.raises(ValidationError):
            validate_args(OrchestratorEnableArgs, {"level": "wrong"})


# ============================================================================
# Backward compat: commands without Pydantic models still work
# ============================================================================


class TestBackwardCompat:
    """Verify commands that don't use Pydantic still function correctly."""

    def test_feature_release_from_args_no_pydantic(self) -> None:
        """FeatureReleaseCommand.from_args works without Pydantic validation."""
        from htmlgraph.cli.work.features import FeatureReleaseCommand

        ns = argparse.Namespace(id="feat-abc", collection="features")
        cmd = FeatureReleaseCommand.from_args(ns)
        assert cmd.feature_id == "feat-abc"
        assert cmd.collection == "features"

    def test_orchestrator_status_from_args_no_pydantic(self) -> None:
        """OrchestratorStatusCommand.from_args works without Pydantic validation."""
        from htmlgraph.cli.work.orchestration import OrchestratorStatusCommand

        ns = argparse.Namespace(graph_dir=".htmlgraph")
        cmd = OrchestratorStatusCommand.from_args(ns)
        assert cmd is not None

    def test_feature_start_with_pydantic_validation(self) -> None:
        """FeatureStartCommand.from_args validates via Pydantic."""
        from htmlgraph.cli.work.features import FeatureStartCommand

        ns = argparse.Namespace(id="  feat-abc  ", collection="features")
        cmd = FeatureStartCommand.from_args(ns)
        # Whitespace stripped by Pydantic validator
        assert cmd.feature_id == "feat-abc"

    def test_feature_start_empty_id_raises_command_error(self) -> None:
        """FeatureStartCommand.from_args raises CommandError on empty ID."""
        from htmlgraph.cli.base import CommandError
        from htmlgraph.cli.work.features import FeatureStartCommand

        ns = argparse.Namespace(id="", collection="features")
        with pytest.raises(CommandError):
            FeatureStartCommand.from_args(ns)

    def test_feature_create_invalid_priority_raises_command_error(self) -> None:
        """FeatureCreateCommand.from_args raises CommandError on invalid priority."""
        from htmlgraph.cli.base import CommandError
        from htmlgraph.cli.work.features import FeatureCreateCommand

        ns = argparse.Namespace(
            title="My Feature",
            description=None,
            priority="extreme",
            steps=None,
            collection="features",
            track=None,
            agent="claude-code",
        )
        with pytest.raises(CommandError):
            FeatureCreateCommand.from_args(ns)

    def test_orchestrator_enable_invalid_level_raises_command_error(self) -> None:
        """OrchestratorEnableCommand.from_args raises CommandError on invalid level."""
        from htmlgraph.cli.base import CommandError
        from htmlgraph.cli.work.orchestration import OrchestratorEnableCommand

        ns = argparse.Namespace(level="extreme")
        with pytest.raises(CommandError):
            OrchestratorEnableCommand.from_args(ns)
