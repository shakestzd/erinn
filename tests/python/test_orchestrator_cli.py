"""
Integration tests for orchestrator CLI commands.
"""

import subprocess
import sys

import pytest


@pytest.fixture
def temp_graph_dir(tmp_path):
    """Create a temporary graph directory."""
    graph_dir = tmp_path / ".htmlgraph"
    graph_dir.mkdir(parents=True)
    return graph_dir


@pytest.fixture
def cli_base_cmd():
    """Base command for running htmlgraph CLI."""
    return [sys.executable, "-m", "htmlgraph.cli"]


def run_cli(cmd, graph_dir=None):
    """
    Run CLI command and return result.

    Args:
        cmd: List of command arguments
        graph_dir: Optional graph directory to use

    Returns:
        subprocess.CompletedProcess
    """
    full_cmd = [sys.executable, "-m", "htmlgraph.cli"] + cmd
    if graph_dir:
        # Add --graph-dir if not already present
        if "--graph-dir" not in cmd and "-g" not in cmd:
            full_cmd.extend(["--graph-dir", str(graph_dir)])

    result = subprocess.run(
        full_cmd,
        capture_output=True,
        text=True,
        timeout=25,
    )
    return result


class TestOrchestratorEnable:
    """Tests for orchestrator enable command."""

    def test_enable_default_level(self, temp_graph_dir):
        """Test enabling with default strict level."""
        result = run_cli(["orchestrator", "enable", "--graph-dir", str(temp_graph_dir)])

        assert result.returncode == 0
        assert "Orchestrator mode enabled (strict enforcement)" in result.stdout

        # Verify state file was created
        state_file = temp_graph_dir / "orchestrator-mode.json"
        assert state_file.exists()

    def test_enable_strict_level(self, temp_graph_dir):
        """Test enabling with explicit strict level."""
        result = run_cli(
            [
                "orchestrator",
                "enable",
                "--level",
                "strict",
                "--graph-dir",
                str(temp_graph_dir),
            ]
        )

        assert result.returncode == 0
        assert "strict enforcement" in result.stdout

    def test_enable_guidance_level(self, temp_graph_dir):
        """Test enabling with guidance level."""
        result = run_cli(
            [
                "orchestrator",
                "enable",
                "--level",
                "guidance",
                "--graph-dir",
                str(temp_graph_dir),
            ]
        )

        assert result.returncode == 0
        assert "guidance mode" in result.stdout

    def test_enable_short_flag(self, temp_graph_dir):
        """Test enabling with short -l flag."""
        result = run_cli(
            [
                "orchestrator",
                "enable",
                "-l",
                "guidance",
                "--graph-dir",
                str(temp_graph_dir),
            ]
        )

        assert result.returncode == 0
        assert "guidance mode" in result.stdout

    def test_enable_invalid_level(self, temp_graph_dir):
        """Test enabling with invalid level fails."""
        result = run_cli(
            [
                "orchestrator",
                "enable",
                "--level",
                "invalid",
                "--graph-dir",
                str(temp_graph_dir),
            ]
        )

        assert result.returncode != 0
        assert "invalid choice" in result.stderr.lower()


class TestOrchestratorDisable:
    """Tests for orchestrator disable command."""

    def test_disable_when_enabled(self, temp_graph_dir):
        """Test disabling when orchestrator is enabled."""
        # First enable
        run_cli(["orchestrator", "enable", "--graph-dir", str(temp_graph_dir)])

        # Then disable
        result = run_cli(
            ["orchestrator", "disable", "--graph-dir", str(temp_graph_dir)]
        )

        assert result.returncode == 0
        assert "Orchestrator mode disabled" in result.stdout

    def test_disable_when_already_disabled(self, temp_graph_dir):
        """Test disabling when orchestrator is already disabled."""
        result = run_cli(
            ["orchestrator", "disable", "--graph-dir", str(temp_graph_dir)]
        )

        assert result.returncode == 0
        assert "Orchestrator mode disabled" in result.stdout

    def test_disable_sets_user_flag(self, temp_graph_dir):
        """Test that disable sets disabled_by_user flag."""
        # Enable then disable
        run_cli(["orchestrator", "enable", "--graph-dir", str(temp_graph_dir)])
        run_cli(["orchestrator", "disable", "--graph-dir", str(temp_graph_dir)])

        # Check status shows user disabled
        result = run_cli(["orchestrator", "status", "--graph-dir", str(temp_graph_dir)])
        assert "Disabled by user" in result.stdout


class TestOrchestratorStatus:
    """Tests for orchestrator status command."""

    def test_status_when_disabled(self, temp_graph_dir):
        """Test status when orchestrator is disabled."""
        result = run_cli(["orchestrator", "status", "--graph-dir", str(temp_graph_dir)])

        assert result.returncode == 0
        assert "Orchestrator mode: disabled" in result.stdout

    def test_status_when_enabled_strict(self, temp_graph_dir):
        """Test status when orchestrator is enabled with strict level."""
        # Enable first
        run_cli(["orchestrator", "enable", "--graph-dir", str(temp_graph_dir)])

        result = run_cli(["orchestrator", "status", "--graph-dir", str(temp_graph_dir)])

        assert result.returncode == 0
        assert "Orchestrator mode: enabled (strict enforcement)" in result.stdout
        assert "Activated at:" in result.stdout

    def test_status_when_enabled_guidance(self, temp_graph_dir):
        """Test status when orchestrator is enabled with guidance level."""
        # Enable with guidance
        run_cli(
            [
                "orchestrator",
                "enable",
                "--level",
                "guidance",
                "--graph-dir",
                str(temp_graph_dir),
            ]
        )

        result = run_cli(["orchestrator", "status", "--graph-dir", str(temp_graph_dir)])

        assert result.returncode == 0
        assert "Orchestrator mode: enabled (guidance mode)" in result.stdout

    def test_status_shows_disabled_by_user(self, temp_graph_dir):
        """Test status shows disabled_by_user flag."""
        # Enable then disable
        run_cli(["orchestrator", "enable", "--graph-dir", str(temp_graph_dir)])
        run_cli(["orchestrator", "disable", "--graph-dir", str(temp_graph_dir)])

        result = run_cli(["orchestrator", "status", "--graph-dir", str(temp_graph_dir)])

        assert result.returncode == 0
        assert "Disabled by user (auto-activation prevented)" in result.stdout


class TestOrchestratorHelp:
    """Tests for orchestrator help commands."""

    def test_orchestrator_help(self):
        """Test orchestrator --help."""
        result = run_cli(["orchestrator", "--help"])

        assert result.returncode == 0
        assert "enable" in result.stdout
        assert "disable" in result.stdout
        assert "status" in result.stdout

    def test_enable_help(self):
        """Test orchestrator enable --help."""
        result = run_cli(["orchestrator", "enable", "--help"])

        assert result.returncode == 0
        assert "--level" in result.stdout
        assert "strict" in result.stdout
        assert "guidance" in result.stdout

    def test_disable_help(self):
        """Test orchestrator disable --help."""
        result = run_cli(["orchestrator", "disable", "--help"])

        assert result.returncode == 0
        assert "orchestrator disable" in result.stdout
        assert "--graph-dir" in result.stdout

    def test_status_help(self):
        """Test orchestrator status --help."""
        result = run_cli(["orchestrator", "status", "--help"])

        assert result.returncode == 0
        assert "orchestrator status" in result.stdout
        assert "--graph-dir" in result.stdout


class TestOrchestratorWorkflow:
    """Tests for complete orchestrator workflows."""

    def test_enable_status_disable_workflow(self, temp_graph_dir):
        """Test complete enable -> status -> disable workflow."""
        # Enable
        result = run_cli(["orchestrator", "enable", "--graph-dir", str(temp_graph_dir)])
        assert result.returncode == 0
        assert "enabled" in result.stdout

        # Check status
        result = run_cli(["orchestrator", "status", "--graph-dir", str(temp_graph_dir)])
        assert result.returncode == 0
        assert "enabled" in result.stdout

        # Disable
        result = run_cli(
            ["orchestrator", "disable", "--graph-dir", str(temp_graph_dir)]
        )
        assert result.returncode == 0
        assert "disabled" in result.stdout

        # Check status again
        result = run_cli(["orchestrator", "status", "--graph-dir", str(temp_graph_dir)])
        assert result.returncode == 0
        assert "disabled" in result.stdout

    def test_change_level_while_enabled(self, temp_graph_dir):
        """Test changing enforcement level while enabled."""
        # Enable with strict
        result = run_cli(["orchestrator", "enable", "--graph-dir", str(temp_graph_dir)])
        assert "strict enforcement" in result.stdout

        # Change to guidance
        result = run_cli(
            [
                "orchestrator",
                "enable",
                "--level",
                "guidance",
                "--graph-dir",
                str(temp_graph_dir),
            ]
        )
        assert "guidance mode" in result.stdout

        # Verify in status
        result = run_cli(["orchestrator", "status", "--graph-dir", str(temp_graph_dir)])
        assert "guidance mode" in result.stdout

    def test_re_enable_after_disable(self, temp_graph_dir):
        """Test re-enabling after user disable."""
        # Enable
        run_cli(["orchestrator", "enable", "--graph-dir", str(temp_graph_dir)])

        # Disable
        run_cli(["orchestrator", "disable", "--graph-dir", str(temp_graph_dir)])

        # Re-enable (should clear disabled_by_user flag)
        result = run_cli(["orchestrator", "enable", "--graph-dir", str(temp_graph_dir)])
        assert result.returncode == 0

        # Status should not show "disabled by user" message
        result = run_cli(["orchestrator", "status", "--graph-dir", str(temp_graph_dir)])
        assert "Disabled by user" not in result.stdout
        assert "enabled" in result.stdout


class TestOrchestratorGraphDir:
    """Tests for --graph-dir option."""

    def test_custom_graph_dir(self, tmp_path):
        """Test using custom graph directory."""
        custom_dir = tmp_path / "custom" / ".htmlgraph"

        result = run_cli(["orchestrator", "enable", "--graph-dir", str(custom_dir)])
        assert result.returncode == 0

        # Verify state file in custom location
        state_file = custom_dir / "orchestrator-mode.json"
        assert state_file.exists()

    def test_short_graph_dir_flag(self, tmp_path):
        """Test using -g short flag for graph directory."""
        custom_dir = tmp_path / "custom" / ".htmlgraph"

        result = run_cli(["orchestrator", "enable", "-g", str(custom_dir)])
        assert result.returncode == 0

        state_file = custom_dir / "orchestrator-mode.json"
        assert state_file.exists()
