"""Tests for skill_scout.installer — plugin installation and tracking."""

from __future__ import annotations

import json
import subprocess
from pathlib import Path
from unittest.mock import MagicMock, patch

from htmlgraph.skill_scout.installer import (
    InstallResult,
    get_install_history,
    install_plugin,
    verify_plugin,
)

# ============================================================================
# InstallResult
# ============================================================================


def test_install_result_sets_installed_at_on_success() -> None:
    result = InstallResult(plugin_name="my-plugin", success=True, message="ok")
    assert result.installed_at != ""


def test_install_result_no_installed_at_on_failure() -> None:
    result = InstallResult(plugin_name="my-plugin", success=False, message="fail")
    assert result.installed_at == ""


def test_install_result_preserves_explicit_installed_at() -> None:
    result = InstallResult(
        plugin_name="my-plugin",
        success=True,
        message="ok",
        installed_at="2026-01-01T00:00:00+00:00",
    )
    assert result.installed_at == "2026-01-01T00:00:00+00:00"


# ============================================================================
# install_plugin — success / failure / edge cases
# ============================================================================


def test_install_plugin_success(tmp_path: Path) -> None:
    mock_result = MagicMock()
    mock_result.returncode = 0
    mock_result.stderr = ""

    with patch("subprocess.run", return_value=mock_result) as mock_run:
        result = install_plugin("commit-commands", project_root=tmp_path)

    assert result.success is True
    assert "commit-commands" in result.message
    mock_run.assert_called_once_with(
        ["claude", "plugin", "install", "commit-commands"],
        capture_output=True,
        text=True,
        timeout=60,
    )


def test_install_plugin_failure(tmp_path: Path) -> None:
    mock_result = MagicMock()
    mock_result.returncode = 1
    mock_result.stderr = "plugin not found"

    with patch("subprocess.run", return_value=mock_result):
        result = install_plugin("nonexistent-plugin", project_root=tmp_path)

    assert result.success is False
    assert "plugin not found" in result.message


def test_install_plugin_claude_not_found(tmp_path: Path) -> None:
    with patch("subprocess.run", side_effect=FileNotFoundError):
        result = install_plugin("some-plugin", project_root=tmp_path)

    assert result.success is False
    assert "claude CLI not found" in result.message


def test_install_plugin_timeout(tmp_path: Path) -> None:
    with patch(
        "subprocess.run",
        side_effect=subprocess.TimeoutExpired(cmd="claude", timeout=60),
    ):
        result = install_plugin("slow-plugin", project_root=tmp_path)

    assert result.success is False
    assert "timed out" in result.message


def test_install_with_marketplace(tmp_path: Path) -> None:
    mock_result = MagicMock()
    mock_result.returncode = 0
    mock_result.stderr = ""

    with patch("subprocess.run", return_value=mock_result) as mock_run:
        result = install_plugin(
            "commit-commands",
            marketplace="anthropics-claude-code",
            project_root=tmp_path,
        )

    assert result.success is True
    mock_run.assert_called_once_with(
        ["claude", "plugin", "install", "commit-commands@anthropics-claude-code"],
        capture_output=True,
        text=True,
        timeout=60,
    )


def test_install_plugin_no_marketplace_qualifier(tmp_path: Path) -> None:
    """Empty marketplace string means no @ qualifier in the install target."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    mock_result.stderr = ""

    with patch("subprocess.run", return_value=mock_result) as mock_run:
        install_plugin("my-plugin", marketplace="", project_root=tmp_path)

    args = mock_run.call_args[0][0]
    assert "@" not in args[-1]


# ============================================================================
# _record_installation / get_install_history
# ============================================================================


def test_record_installation(tmp_path: Path) -> None:
    mock_result = MagicMock()
    mock_result.returncode = 0
    mock_result.stderr = ""

    with patch("subprocess.run", return_value=mock_result):
        install_plugin("test-plugin", project_root=tmp_path)

    history_file = tmp_path / ".htmlgraph" / "installed-plugins.json"
    assert history_file.exists()
    history = json.loads(history_file.read_text())
    assert len(history) == 1
    assert history[0]["plugin_name"] == "test-plugin"
    assert history[0]["installed_at"] != ""


def test_record_installation_appends(tmp_path: Path) -> None:
    """Existing history is preserved; new entry is appended."""
    mock_result = MagicMock()
    mock_result.returncode = 0
    mock_result.stderr = ""

    with patch("subprocess.run", return_value=mock_result):
        install_plugin("plugin-a", project_root=tmp_path)
        install_plugin("plugin-b", project_root=tmp_path)

    history = get_install_history(tmp_path)
    assert len(history) == 2
    assert history[0]["plugin_name"] == "plugin-a"
    assert history[1]["plugin_name"] == "plugin-b"


def test_record_installation_not_written_on_failure(tmp_path: Path) -> None:
    mock_result = MagicMock()
    mock_result.returncode = 1
    mock_result.stderr = "error"

    with patch("subprocess.run", return_value=mock_result):
        install_plugin("bad-plugin", project_root=tmp_path)

    history_file = tmp_path / ".htmlgraph" / "installed-plugins.json"
    assert not history_file.exists()


def test_record_installation_handles_corrupt_json_dict(tmp_path: Path) -> None:
    """If installed-plugins.json contains a dict instead of list, recover gracefully."""
    history_file = tmp_path / ".htmlgraph" / "installed-plugins.json"
    history_file.parent.mkdir(parents=True)
    # Write a dict (corrupt format) instead of a list
    history_file.write_text(json.dumps({"error": "corrupted"}))

    mock_result = MagicMock()
    mock_result.returncode = 0
    mock_result.stderr = ""

    with patch("subprocess.run", return_value=mock_result):
        result = install_plugin("recovery-plugin", project_root=tmp_path)

    assert result.success is True
    # History should be reset to a list with the new plugin
    history = json.loads(history_file.read_text())
    assert isinstance(history, list)
    assert len(history) == 1
    assert history[0]["plugin_name"] == "recovery-plugin"


# ============================================================================
# get_install_history
# ============================================================================


def test_get_install_history_empty(tmp_path: Path) -> None:
    result = get_install_history(tmp_path)
    assert result == []


def test_get_install_history_reads(tmp_path: Path) -> None:
    history_file = tmp_path / ".htmlgraph" / "installed-plugins.json"
    history_file.parent.mkdir(parents=True)
    data = [{"plugin_name": "foo", "installed_at": "2026-01-01", "message": "ok"}]
    history_file.write_text(json.dumps(data))

    result = get_install_history(tmp_path)
    assert len(result) == 1
    assert result[0]["plugin_name"] == "foo"


def test_get_install_history_handles_corrupt_json(tmp_path: Path) -> None:
    history_file = tmp_path / ".htmlgraph" / "installed-plugins.json"
    history_file.parent.mkdir(parents=True)
    history_file.write_text("not valid json{{{")

    result = get_install_history(tmp_path)
    assert result == []


# ============================================================================
# verify_plugin
# ============================================================================


def test_verify_plugin_found() -> None:
    mock_result = MagicMock()
    mock_result.stdout = "commit-commands  htmlgraph  pr-review-toolkit"

    with patch("subprocess.run", return_value=mock_result):
        assert verify_plugin("commit-commands") is True


def test_verify_plugin_not_found() -> None:
    mock_result = MagicMock()
    mock_result.stdout = "htmlgraph  pr-review-toolkit"

    with patch("subprocess.run", return_value=mock_result):
        assert verify_plugin("commit-commands") is False


def test_verify_plugin_claude_not_found() -> None:
    with patch("subprocess.run", side_effect=FileNotFoundError):
        assert verify_plugin("any-plugin") is False


def test_verify_plugin_timeout() -> None:
    with patch(
        "subprocess.run",
        side_effect=subprocess.TimeoutExpired(cmd="claude", timeout=30),
    ):
        assert verify_plugin("any-plugin") is False


def test_verify_plugin_not_matching_substring() -> None:
    """Searching 'code' should NOT match 'code-review' — exact match per line only."""
    mock_result = MagicMock()
    mock_result.stdout = "htmlgraph  code-review-toolkit  pr-review"

    with patch("subprocess.run", return_value=mock_result):
        assert verify_plugin("code") is False
        assert verify_plugin("code-review-toolkit") is True
